package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"

	"tailscale.com/tailcfg"
)

const externalProxyPACPath = "/proxy.pac"

// Only external app listeners install this wrapper. A PAC download is a local
// origin request, never an absolute-form proxy request or a CONNECT tunnel.
func (h *Host) appProxyPACHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.IsAbs() || r.Method == http.MethodConnect || r.URL.Path != externalProxyPACPath {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		local, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
		if !ok || local == nil || r.Host != local.String() || r.URL.RawQuery != "" || r.URL.ForceQuery {
			http.Error(w, "Invalid PAC download address", http.StatusBadRequest)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "PAC download requires GET or HEAD", http.StatusMethodNotAllowed)
			return
		}
		program, err := h.appProxyPAC(local.String())
		if err != nil {
			http.Error(w, "Tailscale routing policy is not ready; retry after connecting", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/x-ns-proxy-autoconfig")
		w.Header().Set("Content-Length", fmt.Sprint(len(program)))
		if r.Method != http.MethodHead {
			_, _ = w.Write(program)
		}
	})
}

func (h *Host) appProxyPAC(address string) ([]byte, error) {
	endpoint, err := netip.ParseAddrPort(address)
	if err != nil || endpoint.Addr().IsUnspecified() || endpoint.Port() == 0 {
		return nil, fmt.Errorf("invalid PAC proxy address")
	}
	h.stateMu.Lock()
	nm, prefs := h.lastNetMap, h.lastPrefs
	h.stateMu.Unlock()
	if nm == nil || !nm.SelfNode.Valid() || prefs == nil {
		return nil, fmt.Errorf("routing policy unavailable")
	}
	names, domains := []string{}, []string{}
	networks := []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48"}
	addName := func(name string) {
		if name, ok := dnsName(name); ok {
			names = append(names, name)
		}
	}
	addNode := func(node tailcfg.NodeView) {
		if !node.Valid() {
			return
		}
		addName(node.Name())
		short, _, _ := strings.Cut(node.Name(), ".")
		addName(short)
		if prefs.RouteAll && node.ID() != nm.SelfNode.ID() {
			for _, route := range node.PrimaryRoutes().All() {
				if safeProxyRoute(route) && slices.Contains(node.AllowedIPs().AsSlice(), route) {
					networks = append(networks, route.String())
				}
			}
		}
	}
	addNode(nm.SelfNode)
	for _, peer := range nm.Peers {
		addNode(peer)
	}
	if _, suffix, ok := strings.Cut(nm.SelfNode.Name(), "."); ok {
		if suffix, valid := dnsName(suffix); valid {
			domains = append(domains, suffix)
		}
	}
	for _, record := range nm.DNS.ExtraRecords {
		if record.Type == "" || record.Type == "A" || record.Type == "AAAA" {
			if _, err := netip.ParseAddr(record.Value); err == nil {
				addName(record.Name)
			}
		}
	}
	if prefs.CorpDNS {
		domains = append(domains, dnsRouteDomains(nm, prefs.ExitNodeID)...)
	}
	for _, values := range [][]string{names, domains, networks} {
		slices.Sort(values)
	}
	data, err := json.Marshal(struct {
		Proxy    string   `json:"proxy"`
		Names    []string `json:"names"`
		Domains  []string `json:"domains"`
		Networks []string `json:"networks"`
	}{"PROXY " + address, slices.Compact(names), slices.Compact(domains), slices.Compact(networks)})
	if err != nil {
		return nil, err
	}
	return []byte("// Tailchrome: protected destinations have no DIRECT fallback.\nvar tailchromePAC = " + string(data) + ";\n" + appProxyPACScript), nil
}

// ES5-compatible and deliberately independent of dnsResolve/isInNetEx: private
// names must not be resolved through the client's ordinary network to route them.
const appProxyPACScript = `
function tailchromeAddress(host) {
  function ipv4(text) {
    var parts = text.split("."), result = [];
    if (parts.length !== 4) return null;
    for (var i = 0; i < 4; i++) {
      if (!/^[0-9]{1,3}$/.test(parts[i]) || +parts[i] > 255) return null;
      result.push(+parts[i]);
    }
    return result;
  }
  function words(text) {
    if (!text) return [];
    var parts = text.split(":"), result = [];
    for (var i = 0; i < parts.length; i++) {
      if (parts[i].indexOf(".") !== -1 && i === parts.length - 1) {
        var v4 = ipv4(parts[i]);
        if (!v4) return null;
        result.push(v4[0] * 256 + v4[1], v4[2] * 256 + v4[3]);
      } else {
        if (!/^[0-9a-f]{1,4}$/.test(parts[i])) return null;
        result.push(parseInt(parts[i], 16));
      }
    }
    return result;
  }
  if (host.indexOf(":") === -1) return ipv4(host);
  var halves = host.split("::");
  if (halves.length > 2) return null;
  var left = words(halves[0]), right = words(halves.length === 2 ? halves[1] : "");
  if (!left || !right) return null;
  var length = left.length + right.length;
  if ((halves.length === 1 && length !== 8) || (halves.length === 2 && length >= 8)) return null;
  while (left.length + right.length < 8) left.push(0);
  var result = left.concat(right);
  if (result[0] === 0 && result[1] === 0 && result[2] === 0 && result[3] === 0 && result[4] === 0 && result[5] === 65535) {
    return [result[6] >>> 8, result[6] & 255, result[7] >>> 8, result[7] & 255];
  }
  return result;
}
function FindProxyForURL(url, host) {
  host = host.toLowerCase().replace(/\.$/, "");
  if (host.charAt(0) === "[" && host.charAt(host.length - 1) === "]") host = host.slice(1, -1);
  var address = tailchromeAddress(host), i, j;
  if (address) {
    for (i = 0; i < tailchromePAC.networks.length; i++) {
      var network = tailchromePAC.networks[i].split("/"), base = tailchromeAddress(network[0]), bits = +network[1];
      if (!base || address.length !== base.length) continue;
      var width = address.length === 4 ? 8 : 16, match = true;
      for (j = 0; j < address.length && bits > 0; j++) {
        var take = Math.min(bits, width), shift = width - take;
        if ((address[j] >>> shift) !== (base[j] >>> shift)) { match = false; break; }
        bits -= take;
      }
      if (match) return tailchromePAC.proxy;
    }
    return "DIRECT";
  }
  for (i = 0; i < tailchromePAC.names.length; i++) {
    if (host === tailchromePAC.names[i]) return tailchromePAC.proxy;
  }
  for (i = 0; i < tailchromePAC.domains.length; i++) {
    var domain = tailchromePAC.domains[i];
    if (host === domain || host.slice(-(domain.length + 1)) === "." + domain) return tailchromePAC.proxy;
  }
  return "DIRECT";
}
`
