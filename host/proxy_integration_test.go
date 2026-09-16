package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"tailscale.com/ipn"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/net/netns"
	"tailscale.com/tsnet"
	"tailscale.com/tstest/integration/testcontrol"
	"tailscale.com/types/logger"
)

func TestHTTPProxyServesAuthenticatedWebClient(t *testing.T) {
	if testing.Short() {
		t.Skip("starts an embedded tailnet node")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })

	control := &testcontrol.Server{
		DERPMap: splitDNSTestDERP(t),
		Logf:    logger.Discard,
	}
	control.HTTPTestServer = httptest.NewUnstartedServer(control)
	control.HTTPTestServer.Start()
	t.Cleanup(control.HTTPTestServer.Close)

	node := &tsnet.Server{
		Dir:          t.TempDir(),
		Hostname:     "web-client-test",
		ControlURL:   control.HTTPTestServer.URL,
		Store:        new(mem.Store),
		Ephemeral:    true,
		RunWebClient: true,
		Logf:         logger.Discard,
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
	defer watcher.Close()
	notify, err := watcher.Next()
	if err != nil {
		t.Fatal(err)
	}
	if notify.NetMap == nil {
		t.Fatal("initial notification did not include a network map")
	}

	h := newHost(nil, io.Discard)
	h.ts = node
	h.lc = lc
	h.sessionGeneration = 1
	h.lastNetMap = notify.NetMap
	h.proxyAuth = &ProxyAuth{Version: 1, Username: "test", Password: "test-password"}
	t.Cleanup(func() {
		h.sessionMu.Lock()
		if h.watchCancel != nil {
			h.watchCancel()
		}
		h.sessionMu.Unlock()
		h.clearWebServer()
	})
	var webCookie *http.Cookie
	newRequest := func(method, path string, body io.Reader) *http.Request {
		req := httptest.NewRequest(method, "http://100.100.100.100"+path, body).WithContext(ctx)
		req.Host = "100.100.100.100"
		req.Header.Set("Proxy-Authorization", "Basic dGVzdDp0ZXN0LXBhc3N3b3Jk")
		if webCookie != nil {
			req.AddCookie(webCookie)
		}
		return req
	}
	handler := h.httpProxyHandler()
	request := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, newRequest(http.MethodGet, path, nil))
		return recorder
	}

	root := request("/")
	if root.Code != http.StatusOK {
		t.Fatalf("root status = %d, want %d; body: %s", root.Code, http.StatusOK, root.Body.String())
	}
	if body := root.Body.String(); !strings.Contains(body, "Tailscale") {
		t.Fatalf("response did not contain the Tailscale web client: %q", body)
	}

	authStatus := request("/api/auth")
	if authStatus.Code != http.StatusOK {
		t.Fatalf("auth status = %d, want %d; body: %s", authStatus.Code, http.StatusOK, authStatus.Body.String())
	}
	var statusBody struct {
		ServerMode     string          `json:"serverMode"`
		ViewerIdentity json.RawMessage `json:"viewerIdentity"`
	}
	if err := json.Unmarshal(authStatus.Body.Bytes(), &statusBody); err != nil {
		t.Fatal(err)
	}
	if statusBody.ServerMode != "manage" || len(statusBody.ViewerIdentity) == 0 {
		t.Fatalf("auth response did not identify the local managing node: %s", authStatus.Body.String())
	}

	newAuth, err := h.webClientAuthRequest(ctx, node, lc, 1, "", notify.NetMap.SelfNode.ID())
	if err != nil {
		t.Fatal(err)
	}
	if newAuth.ID != "testcontrol-webclient-auth" || newAuth.URL != "https://control.tailscale/test-web-auth" {
		t.Fatalf("new auth response = %+v", newAuth)
	}
	waitAuth, err := h.webClientAuthRequest(ctx, node, lc, 1, newAuth.ID, notify.NetMap.SelfNode.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !waitAuth.Complete {
		t.Fatalf("wait auth response = %+v, want complete", waitAuth)
	}

	newSession := request("/api/auth/session/new")
	if newSession.Code != http.StatusOK {
		t.Fatalf("new session status = %d; body: %s", newSession.Code, newSession.Body.String())
	}
	for _, cookie := range newSession.Result().Cookies() {
		if cookie.Name == "TS-Web-Session" {
			webCookie = cookie
			break
		}
	}
	if webCookie == nil {
		t.Fatal("web authentication did not issue a session cookie")
	}
	if waitSession := request("/api/auth/session/wait"); waitSession.Code != http.StatusOK {
		t.Fatalf("wait session status = %d; body: %s", waitSession.Code, waitSession.Body.String())
	}

	if h.webCache == nil {
		t.Fatal("web server was not cached")
	}
	previousCache := h.webCache
	readStarted := make(chan struct{})
	releaseBody := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseBody) }) }
	t.Cleanup(release)
	body := &pausedWebRequestBody{
		reader:  strings.NewReader(`{"RunSSHSet":true,"RunSSH":false}`),
		started: readStarted,
		release: releaseBody,
	}
	patch := newRequest(http.MethodPatch, "/api/local/v0/prefs", body)
	patch.Header.Set("Content-Type", "application/json")
	patch.Header.Set("Sec-Fetch-Site", "same-origin")
	patchResponse := httptest.NewRecorder()
	patchDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(patchResponse, patch)
		close(patchDone)
	}()
	select {
	case <-readStarted:
		// web.Server authenticates the cookie and selects the peer's
		// capabilities before it starts decoding the PATCH body.
	case <-patchDone:
		t.Fatalf("PATCH finished before reading its body: status %d; body: %s", patchResponse.Code, patchResponse.Body.String())
	case <-ctx.Done():
		t.Fatal("authenticated PATCH did not start reading its body")
	}

	changed := make(chan func(), 1)
	go func() { changed <- h.beginProxyProfileChange(lc) }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		h.webMu.Lock()
		changing := h.webChanging
		h.webMu.Unlock()
		if changing {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("profile change did not start draining web requests")
		}
	}
	if _, _, generation := h.sessionSnapshot(); generation != 1 {
		t.Fatalf("generation advanced to %d before the authenticated PATCH completed", generation)
	}
	select {
	case <-changed:
		t.Fatal("profile change completed while the authenticated PATCH body was paused")
	default:
	}

	// A ResponseRecorder cannot interrupt body reads through I/O deadlines.
	// The profile change must therefore drain this request through its
	// synchronous preference mutation before advancing the generation.
	release()
	select {
	case <-patchDone:
	case <-ctx.Done():
		t.Fatal("authenticated PATCH did not finish after its body was released")
	}
	if patchResponse.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want %d; body: %s", patchResponse.Code, http.StatusOK, patchResponse.Body.String())
	}
	var finishChange func()
	select {
	case finishChange = <-changed:
	case <-ctx.Done():
		t.Fatal("profile change did not finish draining the authenticated PATCH")
	}
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(finishChange) }
	t.Cleanup(finish)
	if h.webCache != nil {
		t.Fatal("profile change retained the previous profile's web server")
	}
	if _, _, generation := h.sessionSnapshot(); generation != 2 {
		t.Fatalf("generation after the PATCH completed = %d, want 2", generation)
	}
	finish()

	staleResponse := httptest.NewRecorder()
	stalePatch := newRequest(http.MethodPatch, "/api/local/v0/prefs", strings.NewReader(`{"RunSSHSet":true,"RunSSH":false}`))
	stalePatch.Header.Set("Content-Type", "application/json")
	stalePatch.Header.Set("Sec-Fetch-Site", "same-origin")
	handler.ServeHTTP(staleResponse, stalePatch)
	if staleResponse.Code != http.StatusUnauthorized {
		t.Fatalf("old session cookie status = %d, want %d; body: %s", staleResponse.Code, http.StatusUnauthorized, staleResponse.Body.String())
	}
	if h.webCache == nil || h.webCache == previousCache {
		t.Fatal("profile change did not create a fresh web server")
	}
}

type pausedWebRequestBody struct {
	reader  io.Reader
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (b *pausedWebRequestBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.started)
		<-b.release
	})
	return b.reader.Read(p)
}
