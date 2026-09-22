package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebSessionChangeDrainsSubmittedMutation(t *testing.T) {
	h := newHost(nil, io.Discard)
	var profile atomic.Int32
	profile.Store(1)
	started := make(chan struct{})
	complete := make(chan struct{})
	var release sync.Once
	defer release.Do(func() { close(complete) })
	applied := make(chan int32, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-complete
		applied <- profile.Load()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(api.Close)
	connectionCanceled := make(chan struct{})
	mutationCanceled := make(chan struct{}, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originalContext := r.Context()
		r, finish, ok := h.beginWebRequest(w, r)
		if !ok {
			http.Error(w, "session changing", http.StatusServiceUnavailable)
			return
		}
		defer finish()
		// Consume the body so net/http starts monitoring the connection. An
		// expired I/O deadline then cancels the original request context.
		if _, err := io.ReadAll(r.Body); err != nil {
			t.Error(err)
			return
		}
		go func() {
			<-originalContext.Done()
			close(connectionCanceled)
		}()
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPatch, api.URL, http.NoBody)
		if err != nil {
			t.Error(err)
			return
		}
		resp, err := api.Client().Do(req)
		if err != nil {
			mutationCanceled <- struct{}{}
			return
		}
		resp.Body.Close()
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(proxy.Close)
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		req, _ := http.NewRequest(http.MethodPatch, proxy.URL, strings.NewReader("{}"))
		resp, err := proxy.Client().Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()
	waitWebMutationTest(t, started, "mutation reaching LocalAPI")
	transition := make(chan func(), 1)
	go func() { transition <- h.beginWebSessionChange() }()
	waitWebMutationTest(t, connectionCanceled, "connection cancellation")
	select {
	case <-mutationCanceled:
		t.Fatal("transition canceled a submitted mutation")
	case finish := <-transition:
		finish()
		t.Fatal("transition finished before the submitted mutation")
	case <-time.After(50 * time.Millisecond):
	}
	release.Do(func() { close(complete) })
	select {
	case finish := <-transition:
		profile.Store(2)
		finish()
	case <-time.After(5 * time.Second):
		t.Fatal("transition failed to drain completed mutation")
	}
	if got := <-applied; got != 1 {
		t.Fatalf("mutation applied to profile %d, want original profile 1", got)
	}
	waitWebMutationTest(t, requestDone, "browser request completion")
}

func TestWebSessionChangeInterruptsSlowMutationBody(t *testing.T) {
	h := newHost(nil, io.Discard)
	reading := make(chan struct{})
	bodyError := make(chan error, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r, finish, ok := h.beginWebRequest(w, r)
		if !ok {
			http.Error(w, "session changing", http.StatusServiceUnavailable)
			return
		}
		defer finish()
		close(reading)
		_, err := io.ReadAll(r.Body)
		bodyError <- err
	}))
	t.Cleanup(proxy.Close)
	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	fmt.Fprintf(conn, "PATCH /api/local/v0/prefs HTTP/1.1\r\nHost: 100.100.100.100\r\nContent-Length: 100\r\n\r\n{")
	waitWebMutationTest(t, reading, "request body read")
	transition := make(chan func(), 1)
	go func() { transition <- h.beginWebSessionChange() }()
	select {
	case finish := <-transition:
		finish()
	case <-time.After(5 * time.Second):
		t.Fatal("incomplete body blocked the profile transition")
	}
	if err := <-bodyError; err == nil {
		t.Fatal("incomplete body was accepted during the transition")
	}
}

func waitWebMutationTest(t *testing.T, done <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}
