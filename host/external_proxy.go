package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
)

const externalProxyUsername = "tailchrome"

type externalProxyConfig struct {
	Enabled     bool   `json:"enabled"`
	Port        int    `json:"port,omitempty"`
	Username    string `json:"username"`
	Password    string `json:"password"`
	OwnerInitID string `json:"ownerInitID,omitempty"`
}

type ExternalProxyStatus struct {
	Enabled      bool     `json:"enabled"`
	Running      bool     `json:"running"`
	Host         string   `json:"host"`
	Port         int      `json:"port,omitempty"`
	Protocols    []string `json:"protocols"`
	AuthRequired bool     `json:"authRequired"`
	Username     string   `json:"username,omitempty"`
	Password     string   `json:"password,omitempty"`
	Error        string   `json:"error,omitempty"`
}

// externalProxy owns the app-facing proxy listener. It never reads browser
// domain-split settings: every accepted request is either sent through tsnet's
// policy-checked dialer or rejected.
type externalProxy struct {
	mu         sync.Mutex
	host       *Host
	configPath string
	config     externalProxyConfig
	listener   *trackingListener
	lastError  string
}

func newExternalProxy(host *Host, configPath string) (*externalProxy, error) {
	p := &externalProxy{host: host, configPath: configPath}
	data, err := os.ReadFile(configPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read external proxy config: %w", err)
	}
	if err == nil {
		if err := json.Unmarshal(data, &p.config); err != nil {
			return nil, fmt.Errorf("decode external proxy config: %w", err)
		}
	}
	p.config.Username = externalProxyUsername
	if p.config.Password == "" {
		p.config.Password = rand.Text() + rand.Text()
	}
	if p.config.Port < 0 || p.config.Port > 65535 {
		return nil, fmt.Errorf("invalid external proxy port %d", p.config.Port)
	}
	if err := p.saveLocked(); err != nil {
		return nil, err
	}
	if p.config.Enabled && p.host != nil {
		if err := p.startLocked(); err != nil {
			p.lastError = err.Error()
		}
	}
	return p, nil
}

func (p *externalProxy) ownerInitID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.config.OwnerInitID
}

// attach restores an enabled listener through the profile that owns it. The
// daemon calls this after restoring that profile's independent tsnet session.
func (p *externalProxy) attach(ownerInitID string, host *Host) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.config.OwnerInitID != ownerInitID {
		return fmt.Errorf("external proxy owner does not match restored profile")
	}
	p.host = host
	if p.config.Enabled && p.listener == nil {
		if err := p.startLocked(); err != nil {
			p.lastError = err.Error()
			return err
		}
	}
	return nil
}

func (p *externalProxy) setEnabledFor(enabled bool, ownerInitID string, host *Host) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if enabled {
		if !validInitID.MatchString(ownerInitID) || host == nil {
			return errors.New("a valid browser profile is required")
		}
		if p.config.Enabled && p.config.OwnerInitID != "" && p.config.OwnerInitID != ownerInitID {
			return errors.New("external proxy is owned by another browser profile; disable it before switching")
		}
		p.config.Enabled = true
		p.config.OwnerInitID = ownerInitID
		p.host = host
		if p.listener == nil {
			if err := p.startLocked(); err != nil {
				p.lastError = err.Error()
				return errors.Join(err, p.saveLocked())
			}
		}
	} else {
		p.config.Enabled = false
		p.config.OwnerInitID = ""
		p.host = nil
		p.lastError = ""
		if p.listener != nil {
			_ = p.listener.Close()
			p.listener = nil
		}
	}
	return p.saveLocked()
}

func (p *externalProxy) setEnabled(enabled bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if enabled {
		if p.host == nil {
			return errors.New("external proxy has no Tailscale profile")
		}
		p.config.Enabled = true
		if p.listener == nil {
			if err := p.startLocked(); err != nil {
				p.lastError = err.Error()
				return errors.Join(err, p.saveLocked())
			}
		}
	} else {
		p.config.Enabled = false
		p.config.OwnerInitID = ""
		p.lastError = ""
		if p.listener != nil {
			_ = p.listener.Close()
			p.listener = nil
		}
	}
	return p.saveLocked()
}

func (p *externalProxy) rotateCredentials() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.config.Password = rand.Text() + rand.Text()
	if err := p.saveLocked(); err != nil {
		return err
	}
	if p.listener != nil {
		_ = p.listener.Close()
		p.listener = nil
		if err := p.startLocked(); err != nil {
			p.lastError = err.Error()
			return err
		}
	}
	return p.saveLocked()
}

func (p *externalProxy) status(revealPassword bool) ExternalProxyStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	status := ExternalProxyStatus{
		Enabled:      p.config.Enabled,
		Running:      p.listener != nil,
		Host:         "127.0.0.1",
		Port:         p.config.Port,
		Protocols:    []string{"http", "socks5"},
		AuthRequired: true,
		Username:     p.config.Username,
		Error:        p.lastError,
	}
	if revealPassword {
		status.Password = p.config.Password
	}
	return status
}

func (p *externalProxy) close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.listener == nil {
		return nil
	}
	err := p.listener.Close()
	p.listener = nil
	return err
}

func (p *externalProxy) startLocked() error {
	if p.host == nil {
		return errors.New("external proxy has no Tailscale profile")
	}
	address := "127.0.0.1:0"
	if p.config.Port != 0 {
		address = fmt.Sprintf("127.0.0.1:%d", p.config.Port)
	}
	base, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on external proxy %s: %w", address, err)
	}
	listener := newTrackingListener(base)
	port := base.Addr().(*net.TCPAddr).Port
	if p.config.Port == 0 {
		p.config.Port = port
		if err := p.saveLocked(); err != nil {
			_ = listener.Close()
			return err
		}
	}
	p.listener = listener
	p.lastError = ""
	auth := &ProxyAuth{Version: 1, Username: p.config.Username, Password: p.config.Password}
	p.host.serveAuthenticatedProxy(listener, auth, false)
	return nil
}

func (p *externalProxy) saveLocked() error {
	data, err := json.MarshalIndent(p.config, "", "  ")
	if err != nil {
		return fmt.Errorf("encode external proxy config: %w", err)
	}
	dir := filepath.Dir(p.configPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create external proxy config directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return fmt.Errorf("protect external proxy config directory: %w", err)
	}
	return atomicWriteFile(p.configPath, append(data, '\n'), 0600, "external proxy config")
}

// trackingListener closes accepted connections as well as the listening
// socket. Disabling or rotating credentials therefore revokes existing proxy
// sessions instead of only preventing new ones.
type trackingListener struct {
	net.Listener
	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
}

func newTrackingListener(listener net.Listener) *trackingListener {
	return &trackingListener{Listener: listener, conns: make(map[net.Conn]struct{})}
}

func (l *trackingListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	tracked := &trackedConn{Conn: conn, owner: l}
	l.conns[tracked] = struct{}{}
	return tracked, nil
}

func (l *trackingListener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	conns := make([]net.Conn, 0, len(l.conns))
	for conn := range l.conns {
		conns = append(conns, conn)
	}
	l.mu.Unlock()
	err := l.Listener.Close()
	for _, conn := range conns {
		err = errors.Join(err, conn.Close())
	}
	return err
}

func (l *trackingListener) remove(conn net.Conn) {
	l.mu.Lock()
	delete(l.conns, conn)
	l.mu.Unlock()
}

type trackedConn struct {
	net.Conn
	once  sync.Once
	owner *trackingListener
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.owner.remove(c) })
	return err
}
