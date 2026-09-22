package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestWebSessionChangeCancelsAuthWaitAndBlocksAdmission(t *testing.T) {
	h := newHost(nil, nil)
	started := make(chan struct{})
	canceled := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, finish, ok := h.beginWebRequest(w, r)
		if !ok {
			http.Error(w, "session changing", http.StatusServiceUnavailable)
			return
		}
		defer finish()
		if r.URL.Path == "/api/auth/session/wait" {
			close(started)
			// The upstream web client waits for control-plane authorization
			// with this context, without its own timeout.
			<-r.Context().Done()
			close(canceled)
			<-release
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(releaseHandler)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)

	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/auth/session/wait", nil)
		if err != nil {
			t.Errorf("create auth wait request: %v", err)
			return
		}
		resp, err := server.Client().Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("auth wait did not start")
	}

	changed := make(chan func(), 1)
	go func() { changed <- h.beginWebSessionChange() }()
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("profile change did not cancel the pending auth wait")
	}
	select {
	case finish := <-changed:
		finish()
		t.Fatal("profile change did not drain the active auth handler")
	default:
	}

	requestStatus := func() int {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/ok", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}
	if status := requestStatus(); status != http.StatusServiceUnavailable {
		t.Fatalf("status while draining = %d, want %d", status, http.StatusServiceUnavailable)
	}

	releaseHandler()
	var finishChange func()
	select {
	case finishChange = <-changed:
	case <-ctx.Done():
		t.Fatal("profile change did not finish draining the canceled auth wait")
	}
	if status := requestStatus(); status != http.StatusServiceUnavailable {
		finishChange()
		t.Fatalf("status before profile mutation completes = %d, want %d", status, http.StatusServiceUnavailable)
	}
	finishChange()
	if status := requestStatus(); status != http.StatusNoContent {
		t.Fatalf("status after profile mutation completes = %d, want %d", status, http.StatusNoContent)
	}
	select {
	case <-waitDone:
	case <-ctx.Done():
		t.Fatal("canceled auth request did not return")
	}
}
