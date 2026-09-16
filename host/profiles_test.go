package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"tailscale.com/client/local"
	"tailscale.com/client/web"
	"tailscale.com/ipn"
	"tailscale.com/tailcfg"
	"tailscale.com/types/logger"
)

func TestDeleteProfilePreservesUnchangedWebSession(t *testing.T) {
	for _, tc := range []struct {
		name           string
		profileID      string
		lookupFails    bool
		changesSession bool
	}{
		{name: "inactive profile", profileID: "saved"},
		{name: "active profile", profileID: "active", changesSession: true},
		{name: "unknown current profile", profileID: "saved", lookupFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			h := newHost(nil, &output)
			var deleted atomic.Bool
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/localapi/v0/profiles/current":
					if tc.lookupFails {
						http.Error(w, "profile unavailable", http.StatusServiceUnavailable)
						return
					}
					current := ipn.LoginProfile{ID: "active", Name: "Active account"}
					if deleted.Load() && tc.changesSession {
						current = ipn.LoginProfile{}
					}
					json.NewEncoder(w).Encode(current)
				case "/localapi/v0/profiles/":
					profiles := []ipn.LoginProfile{{ID: "active", Name: "Active account"}, {ID: "saved", Name: "Saved account"}}
					if deleted.Load() {
						if tc.changesSession {
							profiles = profiles[1:]
						} else {
							profiles = profiles[:1]
						}
					}
					json.NewEncoder(w).Encode(profiles)
				case "/localapi/v0/profiles/" + tc.profileID:
					if r.Method != http.MethodDelete {
						t.Errorf("profile mutation method = %s, want DELETE", r.Method)
					}
					// Active authorization must already be invalidated before
					// the local API changes the active identity.
					h.webMu.Lock()
					cacheCleared := h.webCache == nil
					h.webMu.Unlock()
					if cacheCleared != tc.changesSession {
						t.Errorf("web cache cleared before deletion = %v, want %v", cacheCleared, tc.changesSession)
					}
					deleted.Store(true)
					w.WriteHeader(http.StatusNoContent)
				case "/localapi/v0/watch-ipn-bus":
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
			watchCtx, watchCancel := context.WithCancel(t.Context())
			h.watchCancel = watchCancel
			correctionCtx, correctionCancel := context.WithCancel(t.Context())
			h.correctionCancel = correctionCancel
			t.Cleanup(func() {
				h.sessionMu.Lock()
				if h.watchCancel != nil {
					h.watchCancel()
				}
				h.sessionMu.Unlock()
				correctionCancel()
				h.clearWebServer()
			})

			h.handleDeleteProfile(tc.profileID)

			if deleted.Load() == tc.lookupFails {
				t.Fatalf("deleted = %v, lookupFails = %v", deleted.Load(), tc.lookupFails)
			}
			if tc.changesSession {
				if h.webCache != nil || h.sessionGeneration != 8 {
					t.Fatal("active deletion retained the previous web session")
				}
			} else if h.webCache != cache || h.sessionGeneration != 7 {
				t.Fatal("unchanged active identity lost its web session")
			}
			if (watchCtx.Err() != nil) != tc.changesSession || (correctionCtx.Err() != nil) != tc.changesSession {
				t.Fatal("watcher/startup correction canceled without an active identity change")
			}
			reply := decodeReply(t, &output)
			if tc.lookupFails {
				if reply.Error == nil || reply.Error.Cmd != "delete-profile" {
					t.Fatalf("reply = %+v, want delete-profile error", reply)
				}
			} else if reply.Profiles == nil || len(reply.Profiles.Profiles) != 1 || reply.Profiles.Profiles[0].ID == tc.profileID {
				t.Fatalf("reply = %+v, want remaining profile", reply)
			}
		})
	}
}
