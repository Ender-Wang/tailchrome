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

func TestHTTPProxyWebLogoutDrainsMutationAndResetsProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("starts an embedded tailnet node")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	netns.SetEnabled(false)
	t.Cleanup(func() { netns.SetEnabled(true) })

	control := &testcontrol.Server{DERPMap: splitDNSTestDERP(t), Logf: logger.Discard}
	control.HTTPTestServer = httptest.NewUnstartedServer(control)
	control.HTTPTestServer.Start()
	t.Cleanup(control.HTTPTestServer.Close)
	node := &tsnet.Server{
		Dir:          t.TempDir(),
		Hostname:     "web-logout-test",
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
	t.Cleanup(func() { watcher.Close() })
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
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, newRequest(http.MethodGet, path, nil))
		return recorder
	}
	if response := request("/"); response.Code != http.StatusOK {
		t.Fatalf("root status = %d; body: %s", response.Code, response.Body.String())
	}
	if response := request("/api/auth"); response.Code != http.StatusOK {
		t.Fatalf("auth status = %d; body: %s", response.Code, response.Body.String())
	}
	newAuth, err := h.webClientAuthRequest(ctx, node, lc, 1, "", notify.NetMap.SelfNode.ID())
	if err != nil {
		t.Fatal(err)
	}
	if newAuth.ID != "testcontrol-webclient-auth" {
		t.Fatalf("new auth response = %+v", newAuth)
	}
	if _, err := h.webClientAuthRequest(ctx, node, lc, 1, newAuth.ID, notify.NetMap.SelfNode.ID()); err != nil {
		t.Fatal(err)
	}
	newSession := request("/api/auth/session/new")
	if newSession.Code != http.StatusOK {
		t.Fatalf("new session status = %d; body: %s", newSession.Code, newSession.Body.String())
	}
	for _, cookie := range newSession.Result().Cookies() {
		if cookie.Name == "TS-Web-Session" {
			webCookie = cookie
		}
	}
	if webCookie == nil {
		t.Fatal("web authentication did not issue a session cookie")
	}
	if response := request("/api/auth/session/wait"); response.Code != http.StatusOK {
		t.Fatalf("wait session status = %d; body: %s", response.Code, response.Body.String())
	}
	previousCache := h.webCache
	unauthorized := httptest.NewRequest(http.MethodPost, "http://100.100.100.100/api/local/v0/logout", nil).WithContext(ctx)
	unauthorized.Host = "100.100.100.100"
	unauthorized.Header.Set("Proxy-Authorization", "Basic dGVzdDp0ZXN0LXBhc3N3b3Jk")
	unauthorized.Header.Set("Sec-Fetch-Site", "same-origin")
	unauthorizedResponse := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedResponse, unauthorized)
	if unauthorizedResponse.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized web logout status = %d, want %d; body: %s", unauthorizedResponse.Code, http.StatusUnauthorized, unauthorizedResponse.Body.String())
	}
	checkAdmission := func(label string) {
		req := httptest.NewRequest(http.MethodGet, "http://100.100.100.100/api/auth", nil)
		_, finish, ok := h.beginWebRequest(httptest.NewRecorder(), req)
		if !ok {
			t.Fatalf("web admission remained closed after %s logout rejection", label)
		}
		finish()
	}
	checkAdmission("unauthorized")
	crossOrigin := newRequest(http.MethodPost, "/api/local/v0/logout", nil)
	crossOrigin.Header.Set("Sec-Fetch-Site", "cross-site")
	crossOrigin.Header.Set("Origin", "https://attacker.example")
	crossOriginResponse := httptest.NewRecorder()
	handler.ServeHTTP(crossOriginResponse, crossOrigin)
	if crossOriginResponse.Code != http.StatusForbidden {
		t.Fatalf("cross-origin web logout status = %d, want %d; body: %s", crossOriginResponse.Code, http.StatusForbidden, crossOriginResponse.Body.String())
	}
	checkAdmission("cross-origin")
	if h.webCache != previousCache || h.sessionGeneration != 1 {
		t.Fatalf("denied web logout changed session: cache retained=%t generation=%d", h.webCache == previousCache, h.sessionGeneration)
	}
	profileBefore, _, err := lc.ProfileStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	statusBefore, err := lc.StatusWithoutPeers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if profileBefore.ID == "" || statusBefore.Self == nil || statusBefore.Self.NodeID == 0 {
		t.Fatalf("web logout fixture is not logged into a real profile: profile=%#v status-self=%#v", profileBefore, statusBefore.Self)
	}
	t.Logf("logout fixture identity: profile=%q node=%v", profileBefore.ID, statusBefore.Self.NodeID)

	readStarted := make(chan struct{})
	releaseBody := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseBody) }) })
	body := &pausedWebRequestBody{
		reader:  strings.NewReader(`{"SetRoutes":true,"AdvertiseRoutes":["10.123.0.0/24"]}`),
		started: readStarted,
		release: releaseBody,
	}
	patch := newRequest(http.MethodPost, "/api/routes", body)
	patch.Header.Set("Content-Type", "application/json")
	patch.Header.Set("Sec-Fetch-Site", "same-origin")
	patchResponse := httptest.NewRecorder()
	patchResponseStarted := make(chan struct{})
	patchResponseRelease := make(chan struct{})
	var patchResponseReleaseOnce sync.Once
	t.Cleanup(func() { patchResponseReleaseOnce.Do(func() { close(patchResponseRelease) }) })
	patchWriter := &blockingResponseWriter{
		target:  patchResponse,
		started: patchResponseStarted,
		release: patchResponseRelease,
	}
	patchDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(patchWriter, patch)
		close(patchDone)
	}()
	select {
	case <-readStarted:
	case <-patchDone:
		t.Fatalf("POST /api/routes completed before body release: %d", patchResponse.Code)
	case <-ctx.Done():
		t.Fatal("authorized route mutation did not start")
	}

	logoutResponse := httptest.NewRecorder()
	logout := newRequest(http.MethodPost, "/api/local/v0/logout", nil)
	logout.Header.Set("Origin", "http://100.100.100.100")
	logout.Header.Set("Sec-Fetch-Site", "same-origin")
	logoutDone := make(chan struct{})
	go func() {
		handler.ServeHTTP(logoutResponse, logout)
		close(logoutDone)
	}()
	for {
		h.webMu.Lock()
		changing := h.webChanging
		h.webMu.Unlock()
		if changing {
			break
		}
		select {
		case <-time.After(time.Millisecond):
		case <-logoutDone:
			t.Fatalf("web logout completed before draining the authorized route mutation: status=%d", logoutResponse.Code)
		case <-ctx.Done():
			t.Fatal("web logout did not start draining")
		}
	}
	select {
	case <-logoutDone:
		t.Fatal("web logout completed while the authorized route mutation was paused")
	case <-time.After(100 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(releaseBody) })
	select {
	case <-patchResponseStarted:
	case <-logoutDone:
		t.Fatal("web logout completed before the old route mutation reached its response boundary")
	case <-ctx.Done():
		t.Fatal("route mutation did not reach its response boundary")
	}
	prefsBeforeLogout, err := lc.GetPrefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldRouteApplied := false
	for _, route := range prefsBeforeLogout.AdvertiseRoutes {
		if route.String() == "10.123.0.0/24" {
			oldRouteApplied = true
		}
	}
	if !oldRouteApplied {
		t.Fatalf("drained route mutation did not reach the old profile before logout: %v", prefsBeforeLogout.AdvertiseRoutes)
	}
	patchResponseReleaseOnce.Do(func() { close(patchResponseRelease) })
	select {
	case <-patchDone:
	case <-ctx.Done():
		t.Fatal("authorized route mutation did not drain")
	}
	if patchResponse.Code != http.StatusOK {
		t.Fatalf("POST /api/routes status = %d, want %d; body: %s", patchResponse.Code, http.StatusOK, patchResponse.Body.String())
	}
	select {
	case <-logoutDone:
	case <-ctx.Done():
		t.Fatal("web logout did not complete after route mutation drained")
	}
	if logoutResponse.Code != http.StatusNoContent {
		t.Fatalf("web logout status = %d, want %d; body: %s", logoutResponse.Code, http.StatusNoContent, logoutResponse.Body.String())
	}
	if h.sessionGeneration != 2 || h.webCache != nil {
		t.Fatalf("logout transition state = generation %d, cache nil %t; want 2, true", h.sessionGeneration, h.webCache == nil)
	}
	profile, _, err := lc.ProfileStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if profile.ID != "" {
		t.Fatalf("profile after logout = %#v, want logged out", profile)
	}
	prefsAfterLogout, err := lc.GetPrefs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("prefs immediately after logout: advertise routes = %v", prefsAfterLogout.AdvertiseRoutes)
	if len(prefsAfterLogout.AdvertiseRoutes) != 0 {
		t.Fatalf("post-logout profile retained advertised routes: %v", prefsAfterLogout.AdvertiseRoutes)
	}
}

type pausedWebRequestBody struct {
	reader  io.Reader
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

type blockingResponseWriter struct {
	target  *httptest.ResponseRecorder
	started chan struct{}
	release <-chan struct{}
	once    sync.Once
}

func (w *blockingResponseWriter) Header() http.Header {
	return w.target.Header()
}

func (w *blockingResponseWriter) WriteHeader(status int) {
	w.target.WriteHeader(status)
	w.block()
}

func (w *blockingResponseWriter) Write(p []byte) (int, error) {
	n, err := w.target.Write(p)
	w.block()
	return n, err
}

func (w *blockingResponseWriter) block() {
	w.once.Do(func() {
		close(w.started)
		<-w.release
	})
}

func (b *pausedWebRequestBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.started)
		<-b.release
	})
	return b.reader.Read(p)
}
