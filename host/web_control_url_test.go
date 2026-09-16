package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/client/web"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/logger"
)

func TestControlURLChangeInvalidatesWebSession(t *testing.T) {
	const oldURL, newURL = "https://old.example.com", "https://new.example.com"
	for _, tc := range []struct {
		name, currentURL, requestedURL, failAt, wantError string
		changesSession                                    bool
		wantMutations                                     []string
	}{
		{name: "new server", currentURL: oldURL, requestedURL: newURL, changesSession: true,
			wantMutations: []string{"edit", "logout", "start " + newURL}},
		{name: "same server", currentURL: oldURL, requestedURL: oldURL,
			wantMutations: []string{"edit"}},
		{name: "equivalent server URL", currentURL: oldURL, requestedURL: "https://OLD.example.com:443/",
			wantMutations: []string{"edit"}},
		{name: "default server alias", currentURL: "", requestedURL: "https://login.tailscale.com",
			wantMutations: []string{"edit"}},
		{name: "edit fails", currentURL: oldURL, requestedURL: newURL, changesSession: true, failAt: "edit",
			wantMutations: []string{"edit"}, wantError: "failed to set prefs"},
		{name: "logout fails", currentURL: oldURL, requestedURL: newURL, changesSession: true, failAt: "logout",
			wantMutations: []string{"edit", "logout", "start " + oldURL}, wantError: "restored previous control URL"},
		{name: "restart fails", currentURL: oldURL, requestedURL: newURL, changesSession: true, failAt: "restart",
			wantMutations: []string{"edit", "logout", "start " + newURL, "start " + oldURL}, wantError: "reverted to your previous server"},
		{name: "rollback also fails", currentURL: oldURL, requestedURL: newURL, changesSession: true, failAt: "rollback",
			wantMutations: []string{"edit", "logout", "start " + newURL, "start " + oldURL}, wantError: "also failed to restore previous control URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			h := newHost(nil, &output)
			watchCtx, watchCancel := context.WithCancel(t.Context())
			h.watchCancel = watchCancel
			mutations := make(chan string, 4)
			watcherStarted := make(chan struct{}, 1)
			checkSessionBeforeMutation := func(mutation string) {
				t.Helper()
				h.sessionMu.RLock()
				generation := h.sessionGeneration
				h.sessionMu.RUnlock()
				h.webMu.Lock()
				cacheCleared := h.webCache == nil
				h.webMu.Unlock()
				wantGeneration := uint64(7)
				if tc.changesSession {
					wantGeneration++
				}
				if cacheCleared != tc.changesSession || generation != wantGeneration {
					t.Errorf("before %s: cleared cache = %v, generation = %d; want %v, %d", mutation, cacheCleared, generation, tc.changesSession, wantGeneration)
				}
				request := httptest.NewRequest(http.MethodGet, "http://100.100.100.100/api/auth", nil)
				_, done, admitted := h.beginWebRequest(httptest.NewRecorder(), request)
				if admitted {
					done()
				}
				if admitted == tc.changesSession {
					t.Errorf("before %s: web admission = %v, want %v", mutation, admitted, !tc.changesSession)
				}
				mutations <- mutation
			}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/localapi/v0/prefs":
					if r.Method == http.MethodGet {
						json.NewEncoder(w).Encode(&ipn.Prefs{ControlURL: tc.currentURL})
						return
					}
					checkSessionBeforeMutation("edit")
					if tc.failAt == "edit" {
						http.Error(w, "edit failed", http.StatusInternalServerError)
						return
					}
					var prefs ipn.MaskedPrefs
					if err := json.NewDecoder(r.Body).Decode(&prefs); err != nil {
						t.Error(err)
					}
					json.NewEncoder(w).Encode(&prefs.Prefs)
				case "/localapi/v0/logout":
					checkSessionBeforeMutation("logout")
					if tc.failAt == "logout" {
						http.Error(w, "logout failed", http.StatusInternalServerError)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				case "/localapi/v0/start":
					var opts ipn.Options
					if err := json.NewDecoder(r.Body).Decode(&opts); err != nil || opts.UpdatePrefs == nil {
						t.Errorf("invalid start options: %+v, %v", opts, err)
						http.Error(w, "invalid options", http.StatusBadRequest)
						return
					}
					checkSessionBeforeMutation("start " + opts.UpdatePrefs.ControlURL)
					if tc.failAt == "rollback" || (tc.failAt == "restart" && opts.UpdatePrefs.ControlURL == newURL) {
						http.Error(w, "start failed", http.StatusInternalServerError)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				case "/localapi/v0/status":
					json.NewEncoder(w).Encode(&ipnstate.Status{BackendState: "NeedsLogin"})
				case "/localapi/v0/watch-ipn-bus":
					watcherStarted <- struct{}{}
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				default:
					// web.NewServer reports an initialization metric asynchronously.
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			t.Cleanup(api.Close)
			lc := &local.Client{OmitAuth: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, api.Listener.Addr().String())
			}}
			server, err := web.NewServer(web.ServerOpts{
				Mode: web.ManageServerMode, LocalClient: lc, Logf: logger.Discard,
				NewAuthURL: func(context.Context, tailcfg.NodeID) (*tailcfg.WebClientAuthResponse, error) {
					return &tailcfg.WebClientAuthResponse{}, nil
				},
				WaitAuthURL: func(context.Context, string, tailcfg.NodeID) (*tailcfg.WebClientAuthResponse, error) {
					return &tailcfg.WebClientAuthResponse{Complete: true}, nil
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			h.lc = lc
			h.sessionGeneration = 7
			cache := &webServerCache{client: lc, generation: 7, server: server}
			h.webCache = cache
			t.Cleanup(func() {
				h.sessionMu.Lock()
				if h.watchCancel != nil {
					h.watchCancel()
				}
				h.sessionMu.Unlock()
				h.clearWebServer()
			})

			prefs, err := json.Marshal(map[string]string{"controlURL": tc.requestedURL})
			if err != nil {
				t.Fatal(err)
			}
			h.handleSetPrefs(Request{Prefs: prefs})

			close(mutations)
			var gotMutations []string
			for mutation := range mutations {
				gotMutations = append(gotMutations, mutation)
			}
			if !slices.Equal(gotMutations, tc.wantMutations) {
				t.Fatalf("mutations = %q, want %q", gotMutations, tc.wantMutations)
			}
			if tc.changesSession {
				if h.webCache != nil || h.sessionGeneration != 8 || watchCtx.Err() == nil {
					t.Fatal("control server transition restored the previous web session or watcher")
				}
				select {
				case <-watcherStarted:
				case <-time.After(2 * time.Second):
					t.Fatal("IPN watcher was not restarted")
				}
			} else if h.webCache != cache || h.sessionGeneration != 7 || watchCtx.Err() != nil {
				t.Fatal("equivalent control URL invalidated the active web session")
			}
			request := httptest.NewRequest(http.MethodGet, "http://100.100.100.100/api/auth", nil)
			_, done, admitted := h.beginWebRequest(httptest.NewRecorder(), request)
			if !admitted {
				t.Fatal("web admission remained blocked after preference update")
			}
			done()
			var gotError string
			for _, reply := range decodeAllReplies(t, &output) {
				if reply.Error != nil {
					gotError += reply.Error.Message
				}
			}
			if (gotError != "") != (tc.wantError != "") || !strings.Contains(gotError, tc.wantError) {
				t.Fatalf("error = %q, want %q", gotError, tc.wantError)
			}
		})
	}
}
