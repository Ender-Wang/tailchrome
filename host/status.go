package main

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/tsconst"
	"tailscale.com/types/netmap"
	"tailscale.com/util/dnsname"
)

// watchIPNBus watches the IPN notification bus for state changes and sends
// status updates to the extension. It runs in its own goroutine.
func (h *Host) watchIPNBus(ctx context.Context, lc *local.Client, generation uint64) {
	backoff := time.Second
	for {
		err := h.watchIPNBusSession(ctx, lc, generation)
		if ctx.Err() != nil || !h.isCurrentSession(lc, generation) {
			return
		}
		log.Printf("IPN bus watcher error: %v; retrying in %v", err, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (h *Host) watchIPNBusSession(ctx context.Context, lc *local.Client, generation uint64) error {
	watcher, err := lc.WatchIPNBus(ctx, ipn.NotifyInitialState|ipn.NotifyInitialPrefs|ipn.NotifyInitialStatus|ipn.NotifyInitialHealthState|ipn.NotifyPeerChanges)
	if err != nil {
		return err
	}
	defer watcher.Close()

	// debounce coalesces rapid IPN notifications into a single status refresh.
	const debounceDuration = 150 * time.Millisecond
	var debounceTimer *time.Timer
	var debounceMu sync.Mutex
	stopDebounce := func() {
		debounceMu.Lock()
		defer debounceMu.Unlock()
		if debounceTimer != nil {
			debounceTimer.Stop()
			debounceTimer = nil
		}
	}
	defer stopDebounce()

	// sendDebounced schedules a status refresh after debounceDuration,
	// resetting any pending timer so only the last event in a burst fires.
	sendDebounced := func() {
		debounceMu.Lock()
		defer debounceMu.Unlock()
		if debounceTimer != nil {
			debounceTimer.Stop()
		}
		debounceTimer = time.AfterFunc(debounceDuration, func() {
			debounceMu.Lock()
			debounceTimer = nil
			debounceMu.Unlock()
			status, err := h.refreshFullStatusFor(lc, generation)
			if err != nil {
				log.Printf("failed to refresh status after IPN bus change: %v", err)
				return
			}
			h.send(Reply{
				Cmd:    "status",
				Status: status,
			})
		})
	}

	for {
		select {
		case <-ctx.Done():
			log.Printf("IPN bus watcher cancelled")
			return ctx.Err()
		default:
		}

		n, err := watcher.Next()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if !h.isCurrentSession(lc, generation) {
			return context.Canceled
		}

		stateChanged := n.State != nil
		prefsChanged := n.Prefs != nil && n.Prefs.Valid()
		browseURLChanged := n.BrowseToURL != nil
		healthChanged := n.Health != nil
		resetMap := stateChanged && (*n.State == ipn.NoState || *n.State == ipn.NeedsLogin)
		netMapChanged := n.InitialStatus != nil || n.SessionID != "" || n.SelfChange != nil || resetMap
		peersChanged := n.PeersChanged != nil || n.PeersRemoved != nil
		changed := stateChanged || prefsChanged || browseURLChanged || netMapChanged || peersChanged || healthChanged
		var currentMap *netmap.NetworkMap
		if !resetMap && (netMapChanged || peersChanged) {
			// Notifications no longer carry a full netmap. Fetch authoritative
			// nodes and DNS configuration from the replacement LocalAPI methods.
			mapCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			currentMap, err = currentNetworkMap(mapCtx, lc)
			cancel()
			if err != nil {
				// Never leave stale route permissions usable while reconnecting.
				h.sessionMu.RLock()
				if h.lc == lc && h.sessionGeneration == generation {
					h.stateMu.Lock()
					h.lastNetMap = nil
					h.lastSplitDNSDomains = nil
					h.stateMu.Unlock()
				}
				h.sessionMu.RUnlock()
				// Reconnect the watcher to recover a complete initial snapshot
				// rather than leave this configuration change unprocessed.
				return fmt.Errorf("failed to refresh network map: %w", err)
			}
		}
		var prefs *PrefsView
		if prefsChanged {
			prefs = prefsViewFromIPN(*n.Prefs)
		}
		var health []string
		if n.Health != nil {
			for code, w := range n.Health.Warnings {
				// A development-build notice does not indicate a connection failure.
				if code == tsconst.HealthWarnableIsUsingUnstableVersion {
					continue
				}
				// tsnet doesn't manage system DNS; suppress irrelevant warnings
				if strings.Contains(w.Text, "getting OS base config is not supported") {
					continue
				}
				health = append(health, w.Text)
			}
		}

		if changed {
			// Hold the session read lock while updating the cache. A re-init must
			// therefore wait, then clears these values after replacing the session.
			h.sessionMu.RLock()
			if h.lc != lc || h.sessionGeneration != generation {
				h.sessionMu.RUnlock()
				return context.Canceled
			}
			h.stateMu.Lock()
			if stateChanged {
				h.lastState = n.State.String()
			}
			if prefsChanged {
				h.lastPrefs = prefs
			}
			if browseURLChanged {
				h.lastBrowseToURL = *n.BrowseToURL
			}
			if healthChanged {
				h.lastHealth = health
			}
			if netMapChanged || peersChanged {
				// Replace the domain list so removed routes disappear too.
				h.lastNetMap = currentMap
				h.lastSplitDNSDomains = nil
				if currentMap != nil {
					h.lastSplitDNSDomains = configuredSplitDNSDomains(&currentMap.DNS)
				}
			}
			h.stateMu.Unlock()
			h.sessionMu.RUnlock()
			sendDebounced()
		}
	}
}

// currentNetworkMap collects the authoritative nodes and DNS configuration.
// Status is used to enumerate nodes, never to invent route permissions or peer
// capabilities. Reject a collection spanning a profile or routing change.
func currentNetworkMap(ctx context.Context, lc *local.Client) (*netmap.NetworkMap, error) {
	status, err := lc.Status(ctx)
	if err != nil {
		return nil, err
	}
	if status.Self == nil || !status.Self.InNetworkMap {
		return nil, nil
	}
	self, err := lc.PeerByID(ctx, status.Self.NodeID)
	if err != nil {
		return nil, fmt.Errorf("fetch self node %d: %w", status.Self.NodeID, err)
	}
	nm := &netmap.NetworkMap{SelfNode: self.View()}
	for _, peer := range status.Peer {
		// Status also contains engine peers retained after removal from the
		// control-plane map. They are not members of this policy snapshot.
		if !peer.InNetworkMap {
			continue
		}
		node, err := lc.PeerByID(ctx, peer.NodeID)
		if err != nil {
			return nil, fmt.Errorf("fetch peer node %d: %w", peer.NodeID, err)
		}
		nm.Peers = append(nm.Peers, node.View())
	}
	dns, err := lc.DNSConfig(ctx)
	if err != nil {
		return nil, err
	}
	if dns == nil {
		return nil, fmt.Errorf("DNS configuration unavailable")
	}
	nm.DNS = *dns
	latest, err := lc.Status(ctx)
	if err != nil {
		return nil, err
	}
	currentPeers := 0
	for _, peer := range latest.Peer {
		if peer.InNetworkMap {
			currentPeers++
		}
	}
	if !nodeMatchesStatus(nm.SelfNode, status.Self) || !nodeMatchesStatus(nm.SelfNode, latest.Self) || len(nm.Peers) != currentPeers {
		return nil, fmt.Errorf("profile or routes changed while collecting network map")
	}
	for _, node := range nm.Peers {
		if !nodeMatchesStatus(node, status.Peer[node.Key()]) || !nodeMatchesStatus(node, latest.Peer[node.Key()]) {
			return nil, fmt.Errorf("peers or routes changed while collecting network map")
		}
	}
	return nm, nil
}

func nodeMatchesStatus(node tailcfg.NodeView, status *ipnstate.PeerStatus) bool {
	if !node.Valid() || status == nil || node.ID() != status.NodeID || node.StableID() != status.ID || node.Key() != status.PublicKey || node.Name() != status.DNSName {
		return false
	}
	allowedMatch := node.AllowedIPs().Len() == 0
	if status.AllowedIPs != nil {
		allowedMatch = slices.Equal(node.AllowedIPs().AsSlice(), status.AllowedIPs.AsSlice())
	}
	primaryMatch := node.PrimaryRoutes().Len() == 0
	if status.PrimaryRoutes != nil {
		primaryMatch = slices.Equal(node.PrimaryRoutes().AsSlice(), status.PrimaryRoutes.AsSlice())
	}
	return status.InNetworkMap && allowedMatch && primaryMatch
}

func prefsViewFromIPN(p ipn.PrefsView) *PrefsView {
	pv := &PrefsView{
		ControlURL:             p.ControlURL(),
		RouteAll:               p.RouteAll(),
		ExitNodeAllowLANAccess: p.ExitNodeAllowLANAccess(),
		CorpDNS:                p.CorpDNS(),
		WantRunning:            p.WantRunning(),
		ShieldsUp:              p.ShieldsUp(),
		Hostname:               p.Hostname(),
		RunSSH:                 p.RunSSH(),
		RunWebClient:           p.RunWebClient(),
		AdvertiseExitNode:      p.AdvertisesExitNode(),
	}
	ar := p.AdvertiseRoutes()
	if n := ar.Len(); n > 0 {
		pv.AdvertiseRoutes = make([]string, 0, n)
		for i := 0; i < n; i++ {
			pv.AdvertiseRoutes = append(pv.AdvertiseRoutes, ar.At(i).String())
		}
	}
	if p.ExitNodeID() != "" {
		pv.ExitNodeID = string(p.ExitNodeID())
	}
	return pv
}

// refreshFullStatus calls the local client Status API and builds a full
// StatusUpdate from the enriched peer info plus cached IPN bus state.
func (h *Host) refreshFullStatus() (*StatusUpdate, error) {
	_, lc, generation := h.sessionSnapshot()
	if lc == nil {
		return nil, fmt.Errorf("local client not initialized")
	}
	return h.refreshFullStatusFor(lc, generation)
}

func (h *Host) refreshFullStatusFor(lc *local.Client, generation uint64) (*StatusUpdate, error) {

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	st, err := lc.Status(ctx)
	if err != nil {
		return nil, err
	}
	if !h.isCurrentSession(lc, generation) {
		return nil, fmt.Errorf("session changed while refreshing status")
	}

	update := h.buildStatusUpdate(st)
	if !h.isCurrentSession(lc, generation) {
		return nil, fmt.Errorf("session changed while building status")
	}
	return update, nil
}

// buildStatusUpdate constructs a StatusUpdate from the ipnstate.Status
// and the cached IPN bus state (lastState, lastPrefs, lastBrowseToURL, lastHealth).
func (h *Host) buildStatusUpdate(st *ipnstate.Status) *StatusUpdate {
	h.stateMu.Lock()
	state := h.lastState
	browseToURL := h.lastBrowseToURL
	prefs := h.lastPrefs
	health := h.lastHealth
	dnsDomains := h.lastSplitDNSDomains
	var dnsRoutes *[]string
	// An unavailable map cannot clear routes saved by the browser. A known empty
	// map or disabled DNS setting can, so preserve that distinction on the wire.
	if prefs != nil && (!prefs.CorpDNS || h.lastNetMap != nil) {
		domains := []string{}
		if prefs.CorpDNS {
			domains = dnsRouteDomains(h.lastNetMap, prefs.ExitNodeID)
		}
		dnsRoutes = &domains
	}
	h.stateMu.Unlock()

	// Use the backend state from the status if we don't have one cached.
	if state == "" {
		state = st.BackendState
	}
	authURL := st.AuthURL
	if browseToURL == "" {
		browseToURL = authURL
	}

	update := &StatusUpdate{
		BackendState:    state,
		Running:         state == "Running",
		NeedsLogin:      state == "NeedsLogin" || state == "NeedsMachineAuth",
		BrowseToURL:     browseToURL,
		AuthURL:         authURL,
		Prefs:           prefs,
		Health:          health,
		Peers:           []PeerInfo{},
		SplitDNSDomains: []string{},
		DNSRoutes:       dnsRoutes,
	}
	if prefs != nil && prefs.CorpDNS && len(dnsDomains) > 0 {
		update.SplitDNSDomains = dnsDomains
	}

	if st.CurrentTailnet != nil {
		update.Tailnet = st.CurrentTailnet.Name
		update.MagicDNSSuffix = st.CurrentTailnet.MagicDNSSuffix
	}

	// Build self node info.
	if st.Self != nil {
		selfInfo := peerStatusToPeerInfo(st.Self, st)
		update.SelfNode = &selfInfo
	}

	// Build peer list and find the active exit node.
	peers := extractPeers(st)
	update.Peers = peers

	// Find the current exit node.
	for i := range peers {
		if peers[i].ExitNode {
			p := peers[i]
			update.ExitNode = &p
			break
		}
	}

	return update
}

// configuredSplitDNSDomains returns the suffixes the browser must send to the
// helper for resolution. Empty resolver lists are local-authoritative routes
// (for example ExtraRecords), so they still need to reach the internal resolver.
// The root route is a global DNS override, not a restricted domain.
func configuredSplitDNSDomains(config *tailcfg.DNSConfig) []string {
	domains := []string{}
	if config == nil {
		return domains
	}
	for suffix := range config.Routes {
		domain := strings.ToLower(strings.TrimSuffix(suffix, "."))
		if domain == "" || len(domain) > 253 || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || dnsname.ValidHostname(domain) != nil {
			continue
		}
		domains = append(domains, domain)
	}
	slices.Sort(domains)
	return slices.Compact(domains)
}
