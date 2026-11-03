package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/health"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tsconst"
	"tailscale.com/types/dnstype"
	"tailscale.com/types/key"
	"tailscale.com/types/views"
)

func TestBuildStatusUpdateUsesAuthURLFallback(t *testing.T) {
	h := newHost(nil, nil)
	st := &ipnstate.Status{
		BackendState: "NeedsLogin",
		AuthURL:      "https://login.tailscale.com/a/from-status",
	}

	update := h.buildStatusUpdate(st)

	if update.BrowseToURL != st.AuthURL {
		t.Fatalf("BrowseToURL = %q, want %q", update.BrowseToURL, st.AuthURL)
	}
	if update.AuthURL != st.AuthURL {
		t.Fatalf("AuthURL = %q, want %q", update.AuthURL, st.AuthURL)
	}
}

func TestBuildStatusUpdatePrefersCachedBrowseToURL(t *testing.T) {
	h := newHost(nil, nil)
	h.lastBrowseToURL = "https://login.tailscale.com/a/from-ipn"
	st := &ipnstate.Status{
		BackendState: "NeedsLogin",
		AuthURL:      "https://login.tailscale.com/a/from-status",
	}

	update := h.buildStatusUpdate(st)

	if update.BrowseToURL != h.lastBrowseToURL {
		t.Fatalf("BrowseToURL = %q, want %q", update.BrowseToURL, h.lastBrowseToURL)
	}
	if update.AuthURL != st.AuthURL {
		t.Fatalf("AuthURL = %q, want %q", update.AuthURL, st.AuthURL)
	}
}

func TestClearCachedStatusResetsVolatileFieldsAndKeepsPrefs(t *testing.T) {
	h := newHost(nil, nil)
	h.lastState = "Running"
	h.lastBrowseToURL = "https://login.tailscale.com/a/old"
	h.lastHealth = []string{"old warning"}
	h.lastPrefs = &PrefsView{ControlURL: "https://old.example.com"}
	h.lastSplitDNSDomains = []string{"old.example.com"}

	prefs := &ipn.Prefs{
		ControlURL:  "https://hs.example.com",
		WantRunning: true,
		CorpDNS:     true,
	}
	h.clearCachedStatus(prefs)

	if h.lastState != "" {
		t.Fatalf("lastState = %q, want empty", h.lastState)
	}
	if h.lastBrowseToURL != "" {
		t.Fatalf("lastBrowseToURL = %q, want empty", h.lastBrowseToURL)
	}
	if h.lastHealth != nil {
		t.Fatalf("lastHealth = %#v, want nil", h.lastHealth)
	}
	if h.lastSplitDNSDomains != nil {
		t.Fatal("DNS routes from the previous session were retained")
	}
	if h.lastPrefs == nil {
		t.Fatal("lastPrefs = nil, want prefs view")
	}
	if h.lastPrefs.ControlURL != prefs.ControlURL {
		t.Fatalf("ControlURL = %q, want %q", h.lastPrefs.ControlURL, prefs.ControlURL)
	}
	if !h.lastPrefs.WantRunning {
		t.Fatal("WantRunning = false, want true")
	}
}

func TestConfiguredSplitDNSDomains(t *testing.T) {
	config := &tailcfg.DNSConfig{Routes: map[string][]*dnstype.Resolver{
		"INTERNAL.Example.com.":                  {{Addr: "192.168.1.53"}},
		"internal.example.com":                   {{Addr: "192.168.1.54"}},
		"records.example.com":                    nil,
		"home":                                   {},
		".":                                      {{Addr: "1.1.1.1"}},
		"":                                       nil,
		".example.com":                           nil,
		"example.com..":                          nil,
		"bad..example.com":                       nil,
		"https://example.com":                    nil,
		"bad\".example.com":                      nil,
		"-bad.example.com":                       nil,
		strings.Repeat("a", 64) + ".example.com": nil,
	}}
	want := []string{"home", "internal.example.com", "records.example.com"}
	if got := configuredSplitDNSDomains(config); !slices.Equal(got, want) {
		t.Fatalf("domains = %q, want %q", got, want)
	}
}

// Collect full nodes and reject partial snapshots across policy transitions.
func TestCurrentNetworkMapUsesAuthoritativeNodes(t *testing.T) {
	for _, scenario := range []string{"complete", "engine-only-peer", "no-map", "profile-change", "route-removal", "peer-removal", "dns-error", "peer-error"} {
		t.Run(scenario, func(t *testing.T) {
			self := &tailcfg.Node{ID: 1, StableID: "self", Key: key.NewNode().Public(), Name: "self.test.ts.net."}
			route := netip.MustParsePrefix("10.44.0.0/24")
			peer := &tailcfg.Node{ID: 2, StableID: "router", Key: key.NewNode().Public(), Name: "router.test.ts.net.", Cap: 26,
				AllowedIPs: []netip.Prefix{route}, PrimaryRoutes: []netip.Prefix{route}}
			peerStatus := func(node *tailcfg.Node) *ipnstate.PeerStatus {
				allowed, primary := views.SliceOf(node.AllowedIPs), views.SliceOf(node.PrimaryRoutes)
				return &ipnstate.PeerStatus{NodeID: node.ID, ID: node.StableID, PublicKey: node.Key, DNSName: node.Name,
					AllowedIPs: &allowed, PrimaryRoutes: &primary, InNetworkMap: true}
			}
			statusCalls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/localapi/v0/status":
					statusCalls++
					status := &ipnstate.Status{Self: peerStatus(self), Peer: map[key.NodePublic]*ipnstate.PeerStatus{peer.Key: peerStatus(peer)}}
					if scenario == "engine-only-peer" {
						status.Peer[key.NewNode().Public()] = &ipnstate.PeerStatus{InNetworkMap: false}
					}
					if scenario == "no-map" {
						status.Self.InNetworkMap = false
					}
					if statusCalls == 2 {
						switch scenario {
						case "profile-change":
							status.Self.ID = "other-profile"
						case "route-removal":
							status.Peer[peer.Key].AllowedIPs = nil
						case "peer-removal":
							delete(status.Peer, peer.Key)
						}
					}
					json.NewEncoder(w).Encode(status)
				case "/localapi/v0/peer-by-id":
					if r.URL.Query().Get("id") == "1" {
						json.NewEncoder(w).Encode(self)
					} else if scenario == "peer-error" {
						http.Error(w, "peer disappeared", http.StatusNotFound)
					} else {
						json.NewEncoder(w).Encode(peer)
					}
				case "/localapi/v0/dns-config":
					if scenario == "dns-error" {
						http.Error(w, "DNS unavailable", http.StatusServiceUnavailable)
						return
					}
					json.NewEncoder(w).Encode(&tailcfg.DNSConfig{Routes: map[string][]*dnstype.Resolver{"internal.example": nil}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer api.Close()
			lc := &local.Client{OmitAuth: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, api.Listener.Addr().String())
			}}
			nm, err := currentNetworkMap(t.Context(), lc)
			switch scenario {
			case "complete", "engine-only-peer":
				if err != nil || nm == nil || len(nm.Peers) != 1 || nm.Peers[0].Cap() != 26 || !approvedProxySubnet(nm, netip.MustParseAddr("10.44.0.80")) || !slices.Equal(configuredSplitDNSDomains(&nm.DNS), []string{"internal.example"}) {
					t.Fatalf("lost authoritative routing or DNS data: %v, %v", nm, err)
				}
			case "no-map":
				if err != nil || nm != nil {
					t.Fatalf("no-map result = %v, %v", nm, err)
				}
			default:
				if err == nil || nm != nil {
					t.Fatalf("incomplete or changed policy accepted: %v, %v", nm, err)
				}
			}
		})
	}
}

// Exercise notification delivery, DNS removals, and disabled DNS preferences.
func TestWatchIPNBusPublishesSplitDNSChanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	notifications := make(chan ipn.Notify)
	var dnsConfig atomic.Pointer[tailcfg.DNSConfig]
	var dnsUnavailable atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/localapi/v0/watch-ipn-bus":
			mask, err := strconv.ParseUint(r.URL.Query().Get("mask"), 10, 64)
			if err != nil || ipn.NotifyWatchOpt(mask)&ipn.NotifyPeerChanges == 0 {
				t.Error("watcher must subscribe to peer changes")
			}
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			for {
				select {
				case <-r.Context().Done():
					return
				case n := <-notifications:
					if err := json.NewEncoder(w).Encode(n); err != nil {
						return
					}
					w.(http.Flusher).Flush()
				}
			}
		case "/localapi/v0/status":
			status := &ipnstate.Status{BackendState: "Running"}
			if dnsConfig.Load() != nil {
				status.Self = &ipnstate.PeerStatus{NodeID: 1, ID: "self", InNetworkMap: true}
			}
			json.NewEncoder(w).Encode(status)
		case "/localapi/v0/peer-by-id":
			json.NewEncoder(w).Encode(&tailcfg.Node{ID: 1, StableID: "self"})
		case "/localapi/v0/dns-config":
			if dnsUnavailable.Load() {
				http.Error(w, "DNS unavailable", http.StatusServiceUnavailable)
				return
			}
			json.NewEncoder(w).Encode(dnsConfig.Load())
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	lc := &local.Client{OmitAuth: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, api.Listener.Addr().String())
	}}
	writer, reader := net.Pipe()
	defer writer.Close()
	defer reader.Close()
	h := newHost(nil, writer)
	h.lc = lc
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- h.watchIPNBusSession(ctx, lc, 0)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("IPN watcher did not stop")
		}
	}()
	update := func(n ipn.Notify, want []string) {
		t.Helper()
		select {
		case notifications <- n:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		reader.SetReadDeadline(time.Now().Add(2 * time.Second))
		reply := decodeReply(t, reader)
		if reply.Status == nil || !slices.Equal(reply.Status.SplitDNSDomains, want) {
			t.Fatalf("status = %+v, want split DNS domains %q", reply.Status, want)
		}
		if reply.Status.SplitDNSDomains == nil {
			t.Fatal("empty routes must be sent explicitly as [], not null or omitted")
		}
	}
	prefs := (&ipn.Prefs{CorpDNS: true}).View()
	// Login can complete after an initial snapshot with no netmap.
	update(ipn.Notify{SessionID: "initial", Prefs: &prefs, State: new(ipn.NeedsLogin)}, nil)
	dnsConfig.Store(&tailcfg.DNSConfig{
		Routes: map[string][]*dnstype.Resolver{"login.example.com": nil},
	})
	update(ipn.Notify{SelfChange: &tailcfg.Node{}}, []string{"login.example.com"})
	// Initial snapshots now carry status; DNS is fetched through LocalAPI.
	dnsConfig.Store(&tailcfg.DNSConfig{
		Routes: map[string][]*dnstype.Resolver{"internal.example.com": {{Addr: "192.168.1.53"}}},
	})
	update(ipn.Notify{Prefs: &prefs, InitialStatus: &ipnstate.Status{}}, []string{"internal.example.com"})
	dnsConfig.Store(&tailcfg.DNSConfig{
		Routes: map[string][]*dnstype.Resolver{"new.example.com": nil},
	})
	update(ipn.Notify{SelfChange: &tailcfg.Node{}}, []string{"new.example.com"})
	// macOS/Linux announce DNS-only updates with SelfChange.
	dnsConfig.Store(&tailcfg.DNSConfig{
		Routes: map[string][]*dnstype.Resolver{"replacement.example.com": nil},
	})
	update(ipn.Notify{SelfChange: &tailcfg.Node{}}, []string{"replacement.example.com"})
	update(ipn.Notify{PeersChanged: []*tailcfg.Node{{ID: 1}}}, []string{"replacement.example.com"})
	update(ipn.Notify{PeersRemoved: []tailcfg.NodeID{1}}, []string{"replacement.example.com"})
	disabled := (&ipn.Prefs{CorpDNS: false}).View()
	update(ipn.Notify{Prefs: &disabled}, nil)
	update(ipn.Notify{Prefs: &prefs}, []string{"replacement.example.com"})
	dnsConfig.Store(&tailcfg.DNSConfig{})
	update(ipn.Notify{SelfChange: &tailcfg.Node{}}, nil)
	update(ipn.Notify{InitialStatus: &ipnstate.Status{}}, nil)
	for _, state := range []ipn.State{ipn.NoState, ipn.NeedsLogin} {
		dnsConfig.Store(&tailcfg.DNSConfig{
			Routes: map[string][]*dnstype.Resolver{"old-profile.example.com": nil},
		})
		update(ipn.Notify{SelfChange: &tailcfg.Node{}}, []string{"old-profile.example.com"})
		update(ipn.Notify{State: &state}, nil)
	}
	dnsConfig.Store(&tailcfg.DNSConfig{
		Routes: map[string][]*dnstype.Resolver{"old-profile.example.com": nil},
	})
	update(ipn.Notify{SelfChange: &tailcfg.Node{}}, []string{"old-profile.example.com"})
	// A reconnect during a profile transition can start with no netmap even
	// when the state is Starting rather than NeedsLogin.
	dnsConfig.Store(nil)
	update(ipn.Notify{SessionID: "reconnected", State: new(ipn.Starting)}, nil)
	// A failed fetch must restart the watcher so its next initial snapshot
	// recovers the configuration without waiting for another DNS change.
	dnsConfig.Store(&tailcfg.DNSConfig{})
	update(ipn.Notify{SelfChange: &tailcfg.Node{}}, nil)
	dnsUnavailable.Store(true)
	select {
	case notifications <- ipn.Notify{SelfChange: &tailcfg.Node{}}:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "failed to refresh network map") {
			t.Fatalf("watcher error = %v, want DNS refresh failure", err)
		}
		h.stateMu.Lock()
		staleMap := h.lastNetMap != nil
		h.stateMu.Unlock()
		if staleMap {
			t.Fatal("failed refresh retained stale proxy permissions")
		}
	case <-ctx.Done():
		t.Fatal("watcher did not restart after a failed DNS fetch")
	}
}

func startHealthWatcher(t *testing.T, initial ipn.Notify, cachedHealth []string) (chan<- ipn.Notify, net.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	notifications := make(chan ipn.Notify)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/localapi/v0/watch-ipn-bus":
			mask, err := strconv.ParseUint(r.URL.Query().Get("mask"), 10, 64)
			if err != nil {
				t.Errorf("invalid watcher mask: %v", err)
				http.Error(w, "invalid mask", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			if initial.Health != nil {
				n := initial
				if ipn.NotifyWatchOpt(mask)&ipn.NotifyInitialHealthState == 0 {
					n.Health = nil
				}
				if err := json.NewEncoder(w).Encode(n); err != nil {
					return
				}
			}
			w.(http.Flusher).Flush()
			for {
				select {
				case <-r.Context().Done():
					return
				case n := <-notifications:
					if err := json.NewEncoder(w).Encode(n); err != nil {
						return
					}
					w.(http.Flusher).Flush()
				}
			}
		case "/localapi/v0/status":
			json.NewEncoder(w).Encode(&ipnstate.Status{BackendState: "Running"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	lc := &local.Client{OmitAuth: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, api.Listener.Addr().String())
	}}
	writer, reader := net.Pipe()
	t.Cleanup(func() { writer.Close() })
	t.Cleanup(func() { reader.Close() })
	h := newHost(nil, writer)
	h.lc = lc
	h.lastHealth = slices.Clone(cachedHealth)
	done := make(chan error, 1)
	go func() {
		done <- h.watchIPNBusSession(ctx, lc, 0)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("IPN watcher did not stop")
		}
	})
	return notifications, reader
}

func TestWatchIPNBusPublishesInitialHealth(t *testing.T) {
	const warning = "The network is down."
	for _, tt := range []struct {
		name   string
		health *health.State
		cached []string
		want   []string
	}{
		{"existing warning", &health.State{Warnings: map[health.WarnableCode]health.UnhealthyState{
			tsconst.HealthWarnableNetworkStatus: {WarnableCode: tsconst.HealthWarnableNetworkStatus, Text: warning},
		}}, nil, []string{warning}},
		{"empty snapshot clears stale warning", &health.State{}, []string{warning}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, reader := startHealthWatcher(t, ipn.Notify{State: new(ipn.Running), Health: tt.health}, tt.cached)
			// No later health event is sent: the initial snapshot must suffice.
			reader.SetReadDeadline(time.Now().Add(2 * time.Second))
			reply := decodeReply(t, reader)
			if reply.Status == nil || !slices.Equal(reply.Status.Health, tt.want) {
				t.Fatalf("status = %+v, want health %q", reply.Status, tt.want)
			}
		})
	}
}

func TestWatchIPNBusFiltersBuildNoticesAndPublishesHealthChanges(t *testing.T) {
	notifications, reader := startHealthWatcher(t, ipn.Notify{}, nil)

	buildNotice := health.UnhealthyState{
		WarnableCode: tsconst.HealthWarnableIsUsingUnstableVersion,
		Severity:     health.SeverityLow,
		// Deliberately different wording: the stable code identifies the notice.
		Text: "Experimental build notice with changed wording.",
	}
	networkWarning := health.UnhealthyState{
		WarnableCode:        tsconst.HealthWarnableNetworkStatus,
		Severity:            health.SeverityMedium,
		Text:                "The network is down.",
		ImpactsConnectivity: true,
	}
	lowWarning := health.UnhealthyState{
		WarnableCode: tsconst.HealthWarnableUpdateAvailable,
		Severity:     health.SeverityLow,
		Text:         "An update is available.",
	}
	unknownWarning := health.UnhealthyState{
		WarnableCode: "future-warning-code",
		Severity:     health.SeverityLow,
		Text:         "An experimental feature could not connect.",
	}
	dnsWarning := health.UnhealthyState{
		WarnableCode: "os-dns-warning",
		Text:         "getting OS base config is not supported",
	}
	withHealth := func(warnings ...health.UnhealthyState) ipn.Notify {
		state := &health.State{Warnings: make(map[health.WarnableCode]health.UnhealthyState)}
		for _, warning := range warnings {
			state.Warnings[warning.WarnableCode] = warning
		}
		return ipn.Notify{Health: state}
	}
	wantMixed := []string{networkWarning.Text, lowWarning.Text, unknownWarning.Text}
	for _, tt := range []struct {
		name   string
		notify ipn.Notify
		want   []string
	}{
		{"build notice only", withHealth(buildNotice), nil},
		{"genuine failure", withHealth(networkWarning), []string{networkWarning.Text}},
		{"build notice clears stale failure", withHealth(buildNotice), nil},
		{"mixed warnings preserve low and unknown codes", withHealth(buildNotice, networkWarning, lowWarning, unknownWarning, dnsWarning), wantMixed},
		{"notification without health preserves warnings", ipn.Notify{State: new(ipn.Running)}, wantMixed},
		{"empty health clears stale warnings", withHealth(), nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			select {
			case notifications <- tt.notify:
			case <-time.After(2 * time.Second):
				t.Fatal("timed out sending health notification")
			}
			reader.SetReadDeadline(time.Now().Add(2 * time.Second))
			reply := decodeReply(t, reader)
			if reply.Status == nil {
				t.Fatalf("reply = %+v, want status update", reply)
			}
			got, want := slices.Clone(reply.Status.Health), slices.Clone(tt.want)
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("health = %q, want %q", got, want)
			}
		})
	}
}

func TestPrefsViewFromIPNIncludesControlURLAndAdvertisedRoutes(t *testing.T) {
	route := netip.MustParsePrefix("10.0.0.0/24")
	prefs := &ipn.Prefs{
		ControlURL:             "https://hs.example.com",
		RouteAll:               true,
		ExitNodeID:             tailcfg.StableNodeID("node-1"),
		ExitNodeAllowLANAccess: true,
		CorpDNS:                true,
		WantRunning:            true,
		ShieldsUp:              true,
		Hostname:               "browser-ext",
		RunSSH:                 true,
		RunWebClient:           true,
		AdvertiseRoutes:        []netip.Prefix{route},
	}
	prefs.SetAdvertiseExitNode(true)

	pv := prefsViewFromIPN(prefs.View())

	if pv.ControlURL != prefs.ControlURL {
		t.Fatalf("ControlURL = %q, want %q", pv.ControlURL, prefs.ControlURL)
	}
	if !pv.RouteAll || !pv.ExitNodeAllowLANAccess || !pv.CorpDNS || !pv.WantRunning || !pv.ShieldsUp || !pv.RunSSH || !pv.RunWebClient {
		t.Fatalf("boolean prefs not copied correctly: %#v", pv)
	}
	if pv.Hostname != prefs.Hostname {
		t.Fatalf("Hostname = %q, want %q", pv.Hostname, prefs.Hostname)
	}
	if pv.ExitNodeID != string(prefs.ExitNodeID) {
		t.Fatalf("ExitNodeID = %q, want %q", pv.ExitNodeID, prefs.ExitNodeID)
	}
	if !pv.AdvertiseExitNode {
		t.Fatal("AdvertiseExitNode = false, want true")
	}
	foundRoute := false
	for _, got := range pv.AdvertiseRoutes {
		if got == route.String() {
			foundRoute = true
		}
	}
	if !foundRoute {
		t.Fatalf("AdvertiseRoutes = %#v, want route %q", pv.AdvertiseRoutes, route)
	}
}
