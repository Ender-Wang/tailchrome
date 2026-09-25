package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestLaunchAgentPlistRunsDaemonAndEscapesPaths(t *testing.T) {
	plist := string(launchAgentPlist("/Applications/A&B<Tailchrome>", "/tmp/helper&log"))
	for _, expected := range []string{
		"<string>org.tesseras.tailchrome.helper</string>",
		"<string>/Applications/A&amp;B&lt;Tailchrome&gt;</string><string>daemon</string>",
		"<key>RunAtLoad</key><true/>",
		"<key>KeepAlive</key><true/>",
		"<string>/tmp/helper&amp;log</string>",
	} {
		if !strings.Contains(plist, expected) {
			t.Fatalf("LaunchAgent plist missing %q:\n%s", expected, plist)
		}
	}
}

func TestDaemonBridgeEndToEnd(t *testing.T) {
	if os.Getenv("TAILCHROME_DAEMON_TEST_CHILD") == "1" {
		if err := runDaemon(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
	home, err := os.MkdirTemp("/tmp", "tc-daemon-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	command := exec.Command(os.Args[0], "-test.run=^TestDaemonBridgeEndToEnd$")
	command.Env = []string{"HOME=" + home, "TAILCHROME_DAEMON_TEST_CHILD=1"}
	for _, value := range os.Environ() {
		if len(value) >= 5 && value[:5] == "HOME=" || len(value) >= 29 && value[:29] == "TAILCHROME_DAEMON_TEST_CHILD=" {
			continue
		}
		command.Env = append(command.Env, value)
	}
	var childOutput bytes.Buffer
	command.Stdout = &childOutput
	command.Stderr = &childOutput
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})

	deadline := time.Now().Add(5 * time.Second)
	var conn net.Conn
	for time.Now().Before(deadline) {
		var err error
		conn, err = net.Dial("unix", filepathForHome(home, "helper.sock"))
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if conn == nil {
		t.Fatalf("daemon socket did not become ready: %s", childOutput.String())
	}
	defer conn.Close()
	token, err := os.ReadFile(filepathForHome(home, "bridge.token"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintf(conn, "%s\n", token); err != nil {
		t.Fatal(err)
	}
	wantRunning := false
	writeTestRequest(t, conn, Request{Cmd: "init", InitID: "e2e-profile", WantRunning: &wantRunning})
	proc := readTestReply(t, conn)
	if proc.ProcRunning == nil || !proc.ProcRunning.SupportsExternalProxy || proc.ProcRunning.Port == 0 {
		t.Fatalf("procRunning = %#v", proc.ProcRunning)
	}
	if initReply := readTestReply(t, conn); initReply.Init == nil || initReply.Init.Error != "" {
		t.Fatalf("init = %#v", initReply.Init)
	}
	writeTestRequest(t, conn, Request{Cmd: "get-external-proxy-status"})
	status := readTestReply(t, conn)
	if status.ExternalProxy == nil || status.ExternalProxy.Enabled || status.ExternalProxy.Password != "" {
		t.Fatalf("external proxy status = %#v", status.ExternalProxy)
	}
}

func filepathForHome(home, name string) string {
	return home + "/Library/Application Support/Tailchrome/" + name
}

func readTestReply(t *testing.T, reader io.Reader) Reply {
	t.Helper()
	var length uint32
	if err := binary.Read(reader, binary.LittleEndian, &length); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(reader, data); err != nil {
		t.Fatal(err)
	}
	var reply Reply
	if err := json.Unmarshal(data, &reply); err != nil {
		t.Fatal(err)
	}
	return reply
}

func writeTestRequest(t *testing.T, writer io.Writer, request Request) {
	t.Helper()
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(writer, binary.LittleEndian, uint32(len(data))); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(data); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonStatePreservesIndependentBrowserIdentities(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := &daemonStateStore{state: daemonState{Profiles: make(map[string]daemonProfileState)}}
	wantRunning := true
	store.update("chrome-profile", Request{Cmd: "init", InitID: "chrome-profile", WantRunning: &wantRunning})
	wantRunning = false
	store.update("firefox-profile", Request{Cmd: "init", InitID: "firefox-profile", WantRunning: &wantRunning})
	state, err := loadDaemonState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Profiles) != 2 || !state.Profiles["chrome-profile"].WantRunning || state.Profiles["firefox-profile"].WantRunning {
		t.Fatalf("persisted state = %#v", state)
	}
	if info, err := os.Stat(daemonStatePath()); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0600 {
		t.Fatalf("daemon state mode = %o, want 600", info.Mode().Perm())
	}
	if info, err := os.Stat(daemonDataDir()); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0700 {
		t.Fatalf("daemon data directory mode = %o, want 700", info.Mode().Perm())
	}
}

func TestDaemonRuntimeKeepsProfileHostsAndBrowserProxiesSeparate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	external, err := newExternalProxy(nil, externalProxyConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &daemonRuntime{
		profiles: make(map[string]*daemonProfile),
		state:    &daemonStateStore{state: daemonState{Profiles: make(map[string]daemonProfileState)}},
		external: external,
	}
	t.Cleanup(runtime.close)
	chrome, err := runtime.profile("chrome-profile")
	if err != nil {
		t.Fatal(err)
	}
	firefox, err := runtime.profile("firefox-profile")
	if err != nil {
		t.Fatal(err)
	}
	if chrome == firefox || chrome.host == firefox.host || chrome.hub == firefox.hub {
		t.Fatal("different browser profiles shared a daemon runtime")
	}
	if chrome.port == firefox.port {
		t.Fatalf("different browser profiles shared proxy port %d", chrome.port)
	}
	if chrome.host.proxyAuth.Password == firefox.host.proxyAuth.Password {
		t.Fatal("different browser profiles shared proxy credentials")
	}
}

func TestIdentityChangeDisablesOnlyOwnedExternalProxy(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	external, err := newExternalProxy(nil, externalProxyConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &daemonRuntime{
		profiles: make(map[string]*daemonProfile),
		state:    &daemonStateStore{state: daemonState{Profiles: make(map[string]daemonProfileState)}},
		external: external,
	}
	t.Cleanup(runtime.close)
	owner, err := runtime.profile("owner-profile")
	if err != nil {
		t.Fatal(err)
	}
	other, err := runtime.profile("other-profile")
	if err != nil {
		t.Fatal(err)
	}
	if err := external.setEnabledFor(true, owner.initID, owner.host); err != nil {
		t.Fatal(err)
	}
	if err := runtime.disableExternalForIdentityChange(other); err != nil {
		t.Fatal(err)
	}
	if !external.status(false).Enabled {
		t.Fatal("unrelated profile transition disabled the external proxy")
	}
	if err := runtime.disableExternalForIdentityChange(owner); err != nil {
		t.Fatal(err)
	}
	if external.status(false).Enabled {
		t.Fatal("owner identity transition left the external proxy enabled")
	}
}

func TestProcRunningAdvertisesExternalProxyOnlyForDaemon(t *testing.T) {
	host := newHost(nil, nil)
	host.proxyAuth = &ProxyAuth{Version: 1, Username: "browser", Password: "secret"}
	daemonReply := procRunningReply(host, 1234, true).ProcRunning
	if daemonReply == nil || !daemonReply.SupportsDaemonControl || !daemonReply.SupportsExternalProxy {
		t.Fatalf("daemon capabilities = %#v", daemonReply)
	}
	legacyReply := procRunningReply(host, 1234, false).ProcRunning
	if legacyReply == nil || legacyReply.SupportsDaemonControl || legacyReply.SupportsExternalProxy {
		t.Fatalf("legacy capabilities = %#v", legacyReply)
	}
}
