package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"
	"tailscale.com/ipn"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/net/netns"
	"tailscale.com/tsnet"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"
)

// Exercise the real RFC 1928/1929 handshake, rather than calling the SOCKS
// dialer directly. Firefox sends ordinary HTTP over this authenticated stream.
func socksWebTestDialer(t *testing.T, h *Host, port int, password string) proxy.ContextDialer {
	t.Helper()
	dialer, err := proxy.SOCKS5("tcp", fmt.Sprintf("127.0.0.1:%d", port), &proxy.Auth{
		User: h.proxyAuth.Username, Password: password,
	}, &net.Dialer{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return dialer.(proxy.ContextDialer)
}

func TestSOCKSProxyServesAuthenticatedWebClient(t *testing.T) {
	if testing.Short() {
		t.Skip("starts an embedded tailnet node")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })
	control := &testcontrol.Server{DERPMap: splitDNSTestDERP(t), Logf: logger.Discard}
	control.HTTPTestServer = httptest.NewServer(control)
	t.Cleanup(control.HTTPTestServer.Close)
	node := &tsnet.Server{
		Dir: t.TempDir(), Hostname: "socks-web-client-test", ControlURL: control.HTTPTestServer.URL,
		Store: new(mem.Store), Ephemeral: true, RunWebClient: true,
		Logf: logger.Discard, UserLogf: logger.Discard,
	}
	t.Cleanup(func() { node.Close() })
	if _, err := node.Up(ctx); err != nil {
		t.Fatal(err)
	}
	lc, err := node.LocalClient()
	if err != nil {
		t.Fatal(err)
	}
	watcher, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialNetMap)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { watcher.Close() })
	notify, err := watcher.Next()
	if err != nil || notify.NetMap == nil {
		t.Fatalf("initial network map: %v, %v", notify, err)
	}
	h := newHost(nil, io.Discard)
	h.ts, h.lc, h.sessionGeneration, h.lastNetMap = node, lc, 1, notify.NetMap
	t.Cleanup(h.shutdownSession)
	port, err := h.startProxy()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.proxyListener.Close() })
	dialer := socksWebTestDialer(t, h, port, h.proxyAuth.Password)
	transport := &http.Transport{DialContext: dialer.DialContext}
	t.Cleanup(transport.CloseIdleConnections)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transport, Jar: jar, Timeout: 10 * time.Second}
	request := func(method, path, body, fetchSite string, want int) []byte {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, "http://100.100.100.100"+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if fetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", fetchSite)
		}
		if fetchSite == "cross-site" {
			req.Header.Set("Origin", "https://attacker.example")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s through authenticated SOCKS: %v", method, path, err)
		}
		defer resp.Body.Close()
		content, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s %s status = %d, want %d; body: %s", method, path, resp.StatusCode, want, content)
		}
		return content
	}
	if body := request(http.MethodGet, "/", "", "", http.StatusOK); !strings.Contains(string(body), "Tailscale") {
		t.Fatal("SOCKS response does not contain the local web client")
	}
	var status struct {
		ServerMode     string          `json:"serverMode"`
		ViewerIdentity json.RawMessage `json:"viewerIdentity"`
	}
	if err := json.Unmarshal(request(http.MethodGet, "/api/auth", "", "", http.StatusOK), &status); err != nil {
		t.Fatal(err)
	}
	if status.ServerMode != "manage" || len(status.ViewerIdentity) == 0 {
		t.Fatalf("web client did not identify the local managing node: %+v", status)
	}
	// SOCKS authentication does not replace the web client's separate session
	// authorization or CSRF checks, even for its specially serialized logout.
	request(http.MethodPatch, "/api/local/v0/prefs", `{"RunSSHSet":true,"RunSSH":false}`, "same-origin", http.StatusUnauthorized)
	request(http.MethodPost, "/api/local/v0/logout", "", "same-origin", http.StatusUnauthorized)
	authorize := func() {
		t.Helper()
		request(http.MethodGet, "/api/auth/session/new", "", "", http.StatusOK)
		request(http.MethodGet, "/api/auth/session/wait", "", "", http.StatusOK)
	}
	authorize()
	request(http.MethodPatch, "/api/local/v0/prefs", `{"RunSSHSet":true,"RunSSH":false}`, "cross-site", http.StatusForbidden)
	request(http.MethodPost, "/api/local/v0/logout", "", "cross-site", http.StatusForbidden)
	request(http.MethodPatch, "/api/local/v0/prefs", `{"RunSSHSet":true,"RunSSH":false}`, "same-origin", http.StatusOK)

	// A submitted but incomplete HTTP body must not stall a profile change.
	// This also exercises ResponseController deadlines on the in-memory bridge.
	slowConn, err := dialer.DialContext(ctx, "tcp", "100.100.100.100:80")
	if err != nil {
		t.Fatal(err)
	}
	defer slowConn.Close()
	slowConn.SetDeadline(time.Now().Add(10 * time.Second))
	slowRequest, _ := http.NewRequest(http.MethodPatch, "http://100.100.100.100/api/local/v0/prefs", nil)
	for _, cookie := range jar.Cookies(slowRequest.URL) {
		slowRequest.AddCookie(cookie)
	}
	fmt.Fprintf(slowConn, "PATCH /api/local/v0/prefs HTTP/1.1\r\nHost: 100.100.100.100\r\nCookie: %s\r\nSec-Fetch-Site: same-origin\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{", slowRequest.Header.Get("Cookie"))
	for {
		h.webMu.Lock()
		active := len(h.webActive) > 0
		h.webMu.Unlock()
		if active {
			break
		}
		select {
		case <-time.After(time.Millisecond):
		case <-ctx.Done():
			t.Fatal("incomplete request did not enter the web admission gate")
		}
	}
	changed := make(chan func(), 1)
	go func() { changed <- h.beginProxyProfileChange(lc) }()
	var finishChange func()
	select {
	case finishChange = <-changed:
	case <-time.After(5 * time.Second):
		t.Fatal("incomplete SOCKS HTTP request blocked the profile transition")
	}
	// The bridge must consult the admission gate on every keep-alive request.
	request(http.MethodGet, "/api/auth", "", "", http.StatusServiceUnavailable)
	finishChange()
	request(http.MethodPatch, "/api/local/v0/prefs", `{"RunSSHSet":true,"RunSSH":false}`, "same-origin", http.StatusUnauthorized)
	authorize()
	request(http.MethodPost, "/api/local/v0/logout", "", "same-origin", http.StatusNoContent)
	if _, _, generation := h.sessionSnapshot(); generation != 3 {
		t.Fatalf("web logout did not advance the session generation: %d", generation)
	}
	profile, _, err := lc.ProfileStatus(ctx)
	if err != nil || profile.ID != "" {
		t.Fatalf("web logout did not clear the authenticated profile: %+v, %v", profile, err)
	}
}

func TestSOCKSProxyQuad100ScopeAndAuthentication(t *testing.T) {
	h := newHost(nil, io.Discard)
	dials := make(chan string, 4)
	h.proxyDial = func(_ context.Context, network, address string) (net.Conn, error) {
		dials <- network + " " + address
		return nil, fmt.Errorf("test tailnet destination unavailable")
	}
	port, err := h.startProxy()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.proxyListener.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	badAuth := socksWebTestDialer(t, h, port, "incorrect")
	if conn, err := badAuth.DialContext(ctx, "tcp", "100.100.100.100:80"); err == nil {
		conn.Close()
		t.Fatal("wrong SOCKS credential accessed the local web client")
	}
	select {
	case dial := <-dials:
		t.Fatalf("unauthenticated client reached dialer: %s", dial)
	default:
	}
	dialer := socksWebTestDialer(t, h, port, h.proxyAuth.Password)
	for _, address := range []string{"100.100.100.100:443", "100.100.100.100:8080", "100.64.0.2:80"} {
		if conn, err := dialer.DialContext(ctx, "tcp", address); err == nil {
			conn.Close()
			t.Fatalf("unexpected connection to %s", address)
		}
		select {
		case dial := <-dials:
			if dial != "tcp "+address {
				t.Fatalf("normal routing changed: %s", dial)
			}
		case <-ctx.Done():
			t.Fatalf("non-web destination did not use the normal dialer: %s", address)
		}
	}
	for _, tc := range []struct {
		name, method, target, host string
		want                       int
	}{
		{"origin-form", "GET", "/", "100.100.100.100", http.StatusServiceUnavailable},
		{"explicit-http-port", "GET", "/", "100.100.100.100:80", http.StatusServiceUnavailable},
		{"absolute-form", "GET", "http://100.100.100.100/", "100.100.100.100", http.StatusServiceUnavailable},
		{"foreign-host", "GET", "/", "attacker.example", http.StatusBadRequest},
		{"foreign-port", "GET", "/", "100.100.100.100:8080", http.StatusBadRequest},
		{"foreign-absolute-authority", "GET", "http://attacker.example/", "100.100.100.100", http.StatusBadRequest},
		{"wrong-scheme", "GET", "https://100.100.100.100/", "100.100.100.100", http.StatusBadRequest},
		{"connect", "CONNECT", "100.100.100.100:80", "100.100.100.100:80", http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, err := dialer.DialContext(ctx, "tcp", "100.100.100.100:80")
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(5 * time.Second))
			fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", tc.method, tc.target, tc.host)
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			io.Copy(io.Discard, resp.Body)
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
	select {
	case dial := <-dials:
		t.Fatalf("local web stream escaped to the tailnet dialer: %s", dial)
	default:
	}
}
