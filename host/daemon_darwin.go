package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const daemonServiceLabel = "org.tesseras.tailchrome.helper"

type daemonProfileState struct {
	WantRunning bool `json:"wantRunning"`
}

type daemonState struct {
	Profiles map[string]daemonProfileState `json:"profiles,omitempty"`

	// Read-only migration fields from the initial single-profile prototype.
	LegacyInitID      string `json:"initID,omitempty"`
	LegacyWantRunning bool   `json:"wantRunning,omitempty"`
}

type daemonStateStore struct {
	mu    sync.Mutex
	state daemonState
}

func (s *daemonStateStore) profileStates() map[string]daemonProfileState {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]daemonProfileState, len(s.state.Profiles))
	for initID, state := range s.state.Profiles {
		result[initID] = state
	}
	return result
}

func (s *daemonStateStore) update(initID string, request Request) {
	if !validInitID.MatchString(initID) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.Profiles == nil {
		s.state.Profiles = make(map[string]daemonProfileState)
	}
	profile, known := s.state.Profiles[initID]
	if !known {
		// Preserve the legacy nil hint: tsnet starts connected unless the
		// extension explicitly records a stopped preference.
		profile.WantRunning = true
	}
	switch request.Cmd {
	case "init":
		if request.WantRunning != nil {
			profile.WantRunning = *request.WantRunning
		}
	case "up":
		profile.WantRunning = true
	case "down", "logout":
		profile.WantRunning = false
	default:
		return
	}
	s.state.Profiles[initID] = profile
	if err := saveDaemonState(s.state); err != nil {
		log.Printf("save daemon state: %v", err)
	}
}

func daemonDataDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "Application Support", "Tailchrome")
}

func daemonSocketPath() string { return filepath.Join(daemonDataDir(), "helper.sock") }
func daemonTokenPath() string  { return filepath.Join(daemonDataDir(), "bridge.token") }
func daemonStatePath() string  { return filepath.Join(daemonDataDir(), "daemon.json") }
func externalProxyConfigPath() string {
	return filepath.Join(daemonDataDir(), "external-proxy.json")
}

func ensureDaemonDataDir() error {
	if err := os.MkdirAll(daemonDataDir(), 0700); err != nil {
		return err
	}
	return os.Chmod(daemonDataDir(), 0700)
}

type lockedWriter struct {
	mu sync.Mutex
	w  net.Conn
}

func (w *lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.w.SetWriteDeadline(time.Now().Add(5 * time.Second))
	defer w.w.SetWriteDeadline(time.Time{})
	return w.w.Write(data)
}

type writerHub struct {
	mu      sync.Mutex
	writers map[*lockedWriter]struct{}
}

func newWriterHub() *writerHub { return &writerHub{writers: make(map[*lockedWriter]struct{})} }

func (h *writerHub) add(w *lockedWriter) {
	h.mu.Lock()
	h.writers[w] = struct{}{}
	h.mu.Unlock()
}

func (h *writerHub) remove(w *lockedWriter) {
	h.mu.Lock()
	delete(h.writers, w)
	h.mu.Unlock()
}

func (h *writerHub) Write(data []byte) (int, error) {
	h.mu.Lock()
	writers := make([]*lockedWriter, 0, len(h.writers))
	for writer := range h.writers {
		writers = append(writers, writer)
	}
	h.mu.Unlock()
	for _, writer := range writers {
		if _, err := writer.Write(data); err != nil {
			h.remove(writer)
		}
	}
	return len(data), nil
}

type daemonProfile struct {
	initID      string
	host        *Host
	hub         *writerHub
	port        int
	initMu      sync.Mutex
	initialized bool
}

func (p *daemonProfile) initialize(request Request, writer *lockedWriter) {
	p.initMu.Lock()
	defer p.initMu.Unlock()
	request.InitID = p.initID
	if !p.initialized {
		p.host.handleRequest(request)
		p.initialized = p.host.hasSession(p.initID)
		return
	}
	if writer != nil {
		_ = writeReply(writer, Reply{Cmd: "init", Init: &InitReply{}})
	}
	if request.WantRunning != nil {
		if *request.WantRunning {
			p.host.handleRequest(Request{Cmd: "up"})
		} else {
			p.host.handleRequest(Request{Cmd: "down"})
		}
	}
}

func (p *daemonProfile) close() {
	if p.host.proxyListener != nil {
		_ = p.host.proxyListener.Close()
	}
	p.host.shutdownSession()
}

type daemonRuntime struct {
	mu       sync.Mutex
	profiles map[string]*daemonProfile
	state    *daemonStateStore
	external *externalProxy
}

func (d *daemonRuntime) profile(initID string) (*daemonProfile, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if profile := d.profiles[initID]; profile != nil {
		return profile, nil
	}
	hub := newWriterHub()
	host := newHost(nil, hub)
	port, err := host.startProxy()
	if err != nil {
		return nil, err
	}
	profile := &daemonProfile{initID: initID, host: host, hub: hub, port: port}
	d.profiles[initID] = profile
	return profile, nil
}

func (d *daemonRuntime) close() {
	d.mu.Lock()
	profiles := make([]*daemonProfile, 0, len(d.profiles))
	for _, profile := range d.profiles {
		profiles = append(profiles, profile)
	}
	d.mu.Unlock()
	_ = d.external.close()
	for _, profile := range profiles {
		profile.close()
	}
}

func (d *daemonRuntime) disableExternalForIdentityChange(profile *daemonProfile) error {
	if d.external.ownerInitID() != profile.initID {
		return nil
	}
	return d.external.setEnabledFor(false, profile.initID, profile.host)
}

func runDaemon() error {
	sanitizeNativeHostEnvironment()
	if err := ensureDaemonDataDir(); err != nil {
		return err
	}
	token, err := loadOrCreateDaemonToken()
	if err != nil {
		return err
	}
	if conn, err := net.Dial("unix", daemonSocketPath()); err == nil {
		_ = conn.Close()
		return errors.New("daemon is already running")
	}
	_ = os.Remove(daemonSocketPath())
	listener, err := net.Listen("unix", daemonSocketPath())
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(daemonSocketPath())
	if err := os.Chmod(daemonSocketPath(), 0600); err != nil {
		return err
	}

	state, err := loadDaemonState()
	if err != nil {
		return err
	}
	stateStore := &daemonStateStore{state: state}
	external, err := newExternalProxy(nil, externalProxyConfigPath())
	if err != nil {
		return err
	}
	runtime := &daemonRuntime{profiles: make(map[string]*daemonProfile), state: stateStore, external: external}
	defer runtime.close()

	// Restore every browser profile independently. The daemon extends lifetime;
	// it does not merge profiles or change their existing isolation boundary.
	for initID, profileState := range stateStore.profileStates() {
		profile, err := runtime.profile(initID)
		if err != nil {
			return fmt.Errorf("restore profile %s: %w", initID, err)
		}
		wantRunning := profileState.WantRunning
		profile.initialize(Request{Cmd: "init", InitID: initID, WantRunning: &wantRunning}, nil)
	}
	if owner := external.ownerInitID(); owner != "" {
		profile, err := runtime.profile(owner)
		if err != nil {
			return fmt.Errorf("restore external proxy profile: %w", err)
		}
		if !profile.initialized {
			wantRunning := true
			profile.initialize(Request{Cmd: "init", InitID: owner, WantRunning: &wantRunning}, nil)
		}
		if err := external.attach(owner, profile.host); err != nil {
			log.Printf("restore external proxy: %v", err)
		}
	}

	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go serveDaemonClient(conn, token, runtime)
	}
}

func serveDaemonClient(conn net.Conn, token string, runtime *daemonRuntime) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	presented, err := reader.ReadString('\n')
	if err != nil || presented != token+"\n" {
		return
	}
	writer := &lockedWriter{w: conn}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	first, err := readNativeRequest(reader)
	_ = conn.SetReadDeadline(time.Time{})
	if err != nil {
		return
	}
	if first.Cmd != "init" || !validInitID.MatchString(first.InitID) {
		_ = writeReply(writer, Reply{Cmd: "error", Error: &ErrorReply{Cmd: first.Cmd, Message: "the first command must contain a valid initID"}})
		return
	}
	profile, err := runtime.profile(first.InitID)
	if err != nil {
		_ = writeReply(writer, Reply{Cmd: "procRunning", ProcRunning: &ProcRunningReply{PID: os.Getpid(), Version: version, Error: err.Error()}})
		return
	}
	profile.hub.add(writer)
	defer profile.hub.remove(writer)
	if err := writeReply(writer, procRunningReply(profile.host, profile.port, true)); err != nil {
		return
	}
	runtime.state.update(profile.initID, first)
	profile.initialize(first, writer)

	profile.host.readMessagesFrom(reader, func(request Request) {
		if request.Cmd == "init" && request.InitID != profile.initID {
			_ = writeReply(writer, Reply{Cmd: "error", Error: &ErrorReply{Cmd: request.Cmd, Message: "a native connection cannot change browser profiles"}})
			return
		}
		runtime.state.update(profile.initID, request)
		switch request.Cmd {
		case "get-external-proxy-status":
			_ = writeReply(writer, Reply{Cmd: "externalProxy", ExternalProxy: ptrExternalStatus(runtime.external.status(false))})
		case "reveal-external-proxy":
			_ = writeReply(writer, Reply{Cmd: "externalProxy", ExternalProxy: ptrExternalStatus(runtime.external.status(true))})
		case "set-external-proxy":
			if request.Enabled == nil {
				_ = writeReply(writer, Reply{Cmd: "error", Error: &ErrorReply{Cmd: request.Cmd, Message: "enabled is required"}})
				return
			}
			if err := runtime.external.setEnabledFor(*request.Enabled, profile.initID, profile.host); err != nil {
				_ = writeReply(writer, Reply{Cmd: "error", Error: &ErrorReply{Cmd: request.Cmd, Message: err.Error()}})
				return
			}
			_ = writeReply(writer, Reply{Cmd: "externalProxy", ExternalProxy: ptrExternalStatus(runtime.external.status(false))})
		case "rotate-external-proxy-credentials":
			if err := runtime.external.rotateCredentials(); err != nil {
				_ = writeReply(writer, Reply{Cmd: "error", Error: &ErrorReply{Cmd: request.Cmd, Message: err.Error()}})
				return
			}
			_ = writeReply(writer, Reply{Cmd: "externalProxy", ExternalProxy: ptrExternalStatus(runtime.external.status(true))})
		case "init":
			profile.initialize(request, writer)
		default:
			switch request.Cmd {
			case "logout", "switch-profile", "new-profile", "delete-profile":
				if err := runtime.disableExternalForIdentityChange(profile); err != nil {
					_ = writeReply(writer, Reply{Cmd: "error", Error: &ErrorReply{Cmd: request.Cmd, Message: "disable local app proxy before identity change: " + err.Error()}})
					return
				}
			}
			profile.host.handleRequest(request)
		}
	})
}

func ptrExternalStatus(status ExternalProxyStatus) *ExternalProxyStatus { return &status }

func procRunningReply(host *Host, port int, daemon bool) Reply {
	return Reply{Cmd: "procRunning", ProcRunning: &ProcRunningReply{
		Port: port, ProxyAuth: host.proxyAuth, PID: os.Getpid(), Version: version,
		SupportsPingPeer: true, SupportsLogin: true, SupportsCustomControlURL: true,
		SupportsDaemonControl: daemon, SupportsExternalProxy: daemon,
	}}
}

func loadOrCreateDaemonToken() (string, error) {
	if err := ensureDaemonDataDir(); err != nil {
		return "", err
	}
	if data, err := os.ReadFile(daemonTokenPath()); err == nil {
		return string(data), nil
	} else if !os.IsNotExist(err) {
		return "", err
	}
	token := rand.Text() + rand.Text()
	if err := atomicWriteFile(daemonTokenPath(), []byte(token), 0600, "daemon token"); err != nil {
		return "", err
	}
	return token, nil
}

func loadDaemonState() (daemonState, error) {
	data, err := os.ReadFile(daemonStatePath())
	if os.IsNotExist(err) {
		return daemonState{Profiles: make(map[string]daemonProfileState)}, nil
	}
	if err != nil {
		return daemonState{}, err
	}
	var state daemonState
	if err := json.Unmarshal(data, &state); err != nil {
		return daemonState{}, err
	}
	if state.Profiles == nil {
		state.Profiles = make(map[string]daemonProfileState)
	}
	if validInitID.MatchString(state.LegacyInitID) {
		state.Profiles[state.LegacyInitID] = daemonProfileState{WantRunning: state.LegacyWantRunning}
		state.LegacyInitID = ""
		state.LegacyWantRunning = false
	}
	return state, nil
}

func saveDaemonState(state daemonState) error {
	if err := ensureDaemonDataDir(); err != nil {
		return err
	}
	state.LegacyInitID = ""
	state.LegacyWantRunning = false
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteFile(daemonStatePath(), append(data, '\n'), 0600, "daemon state")
}

func runNativeBridge(stdin io.Reader, stdout io.Writer) (bool, error) {
	token, err := os.ReadFile(daemonTokenPath())
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	conn, err := net.Dial("unix", daemonSocketPath())
	if err != nil {
		return true, fmt.Errorf("resident helper unavailable: %w", err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "%s\n", token); err != nil {
		return true, err
	}
	errCh := make(chan error, 2)
	go func() {
		_, err := io.Copy(conn, stdin)
		if closer, ok := conn.(interface{ CloseWrite() error }); ok {
			_ = closer.CloseWrite()
		}
		errCh <- err
	}()
	go func() {
		_, err := io.Copy(stdout, conn)
		errCh <- err
	}()
	return true, <-errCh
}
