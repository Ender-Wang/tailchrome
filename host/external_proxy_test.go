package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

func TestExternalProxyConfigIsStableAndProtected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "external-proxy.json")
	h := newHost(nil, nil)
	first, err := newExternalProxy(h, path)
	if err != nil {
		t.Fatal(err)
	}
	firstStatus := first.status(true)
	if firstStatus.Username != externalProxyUsername || firstStatus.Password == "" {
		t.Fatalf("unexpected credentials: %#v", firstStatus)
	}
	if firstStatus.Password == "tailchrome" {
		t.Fatal("password must not be the fixed username")
	}
	if info, err := os.Stat(path); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0600 {
		t.Fatalf("config mode = %o, want 600", info.Mode().Perm())
	}

	second, err := newExternalProxy(h, path)
	if err != nil {
		t.Fatal(err)
	}
	secondStatus := second.status(true)
	if secondStatus.Username != firstStatus.Username || secondStatus.Password != firstStatus.Password {
		t.Fatalf("credentials changed across reload: first=%#v second=%#v", firstStatus, secondStatus)
	}
	if second.status(false).Password != "" {
		t.Fatal("ordinary status must not reveal the password")
	}
}

func TestExternalProxyAuthenticatesSOCKS5(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "socks-ok")
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}

	h := newHost(nil, nil)
	h.proxyDial = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	p, err := newExternalProxy(h, filepath.Join(t.TempDir(), "external-proxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.close() })
	status := p.status(true)
	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", status.Port), &proxy.Auth{
		User: status.Username, Password: status.Password,
	}, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{DialContext: func(_ context.Context, network, address string) (net.Conn, error) {
		return dialer.Dial(network, address)
	}}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	response, err := client.Get("http://" + upstreamURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %s", response.Status)
	}
}

func TestExternalProxyEnablePersistsPortAndAuthenticatesHTTP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "through-proxy")
	}))
	defer upstream.Close()

	h := newHost(nil, nil)
	h.proxyDial = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	path := filepath.Join(t.TempDir(), "external-proxy.json")
	p, err := newExternalProxy(h, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.close() })
	status := p.status(true)
	if !status.Enabled || !status.Running || status.Port == 0 {
		t.Fatalf("proxy not running: %#v", status)
	}

	proxyURL, err := url.Parse(fmt.Sprintf("http://%s:%s@127.0.0.1:%d", status.Username, status.Password, status.Port))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 3 * time.Second}
	response, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %s", response.Status)
	}

	var persisted externalProxyConfig
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	if !persisted.Enabled || persisted.Port != status.Port {
		t.Fatalf("persisted config = %#v, status = %#v", persisted, status)
	}

	unauthenticated := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", status.Port)})},
		Timeout:   3 * time.Second,
	}
	unauthResponse, err := unauthenticated.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	unauthResponse.Body.Close()
	if unauthResponse.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("unauthenticated status = %s, want 407", unauthResponse.Status)
	}
}

func TestExternalProxyDisableClosesListenerAndSessions(t *testing.T) {
	h := newHost(nil, nil)
	p, err := newExternalProxy(h, filepath.Join(t.TempDir(), "external-proxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	status := p.status(false)
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", status.Port))
	if err != nil {
		t.Fatal(err)
	}
	if err := p.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := conn.Read(make([]byte, 1)); err == nil {
		t.Fatal("existing session remained open after disable")
	}
	if _, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", status.Port), 200*time.Millisecond); err == nil {
		t.Fatal("listener still accepts connections after disable")
	}
}

func TestExternalHTTPProxyDoesNotExposeLocalWebClient(t *testing.T) {
	h := newHost(nil, nil)
	h.proxyDial = func(context.Context, string, string) (net.Conn, error) {
		return nil, fmt.Errorf("dial reached")
	}
	auth := &ProxyAuth{Version: 1, Username: "tailchrome", Password: "secret"}
	req := httptest.NewRequest(http.MethodGet, "http://100.100.100.100/", nil)
	req.Header.Set("Proxy-Authorization", "Basic dGFpbGNocm9tZTpzZWNyZXQ=")
	recorder := httptest.NewRecorder()
	h.httpProxyHandlerWithAuth(auth, false).ServeHTTP(recorder, req)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadGateway)
	}
}

func TestBrowserAndExternalProxyCredentialsAreIndependent(t *testing.T) {
	h := newHost(nil, nil)
	h.proxyAuth = &ProxyAuth{Version: 1, Username: "tailchrome", Password: "browser-secret"}
	externalAuth := &ProxyAuth{Version: 1, Username: "tailchrome", Password: "external-secret"}
	req := httptest.NewRequest(http.MethodGet, "http://100.64.0.2/", nil)
	req.SetBasicAuth(h.proxyAuth.Username, h.proxyAuth.Password)
	// Proxy authentication uses Proxy-Authorization, not Authorization.
	req.Header.Set("Proxy-Authorization", req.Header.Get("Authorization"))
	req.Header.Del("Authorization")
	if authenticateProxyRequest(req, externalAuth) {
		t.Fatal("browser credential authenticated to the external listener")
	}
	if !h.authenticateProxyRequest(req) {
		t.Fatal("browser credential did not authenticate to the browser listener")
	}
}

func TestExternalProxyOwnerSwitchRequiresDisable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "external-proxy.json")
	firstHost := newHost(nil, nil)
	secondHost := newHost(nil, nil)
	p, err := newExternalProxy(nil, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.close() })
	if err := p.setEnabledFor(true, "chrome-profile", firstHost); err != nil {
		t.Fatal(err)
	}
	if owner := p.ownerInitID(); owner != "chrome-profile" {
		t.Fatalf("owner = %q", owner)
	}
	if err := p.setEnabledFor(true, "firefox-profile", secondHost); err == nil {
		t.Fatal("enabled proxy changed owners without being disabled")
	}
	if err := p.setEnabledFor(false, "firefox-profile", secondHost); err != nil {
		t.Fatal(err)
	}
	if err := p.setEnabledFor(true, "firefox-profile", secondHost); err != nil {
		t.Fatal(err)
	}
	if owner := p.ownerInitID(); owner != "firefox-profile" {
		t.Fatalf("owner after switch = %q", owner)
	}
}
