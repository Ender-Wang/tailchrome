package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"tailscale.com/tailcfg"
	"tailscale.com/types/dnstype"
	"tailscale.com/types/netmap"
)

func pacTestHost() *Host {
	h := newHost(nil, nil)
	subnet := netip.MustParsePrefix("192.168.50.0/24")
	v6Subnet := netip.MustParsePrefix("fd42:abcd::/48")
	h.lastNetMap = &netmap.NetworkMap{
		SelfNode: (&tailcfg.Node{ID: 1, Name: "mac.tail123.ts.net.",
			AllowedIPs: []netip.Prefix{netip.MustParsePrefix("172.16.0.0/16")}, PrimaryRoutes: []netip.Prefix{netip.MustParsePrefix("172.16.0.0/16")}}).View(),
		Peers: []tailcfg.NodeView{(&tailcfg.Node{ID: 2, StableID: "nas", Name: "nas.tail123.ts.net.", Cap: 26,
			AllowedIPs:    []netip.Prefix{subnet, v6Subnet, netip.MustParsePrefix("0.0.0.0/0")},
			PrimaryRoutes: []netip.Prefix{subnet, v6Subnet, netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("0.0.0.0/0")}}).View()},
		DNS: tailcfg.DNSConfig{
			Routes:       map[string][]*dnstype.Resolver{"internal.example": {{Addr: "100.64.0.53", UseWithExitNode: true}}, ".": {{Addr: "1.1.1.1"}}},
			ExtraRecords: []tailcfg.DNSRecord{{Name: "photos.example", Value: "100.99.94.96"}},
		},
	}
	h.lastPrefs = &PrefsView{CorpDNS: true, RouteAll: true, ExitNodeID: "nas", WantRunning: true}
	return h
}

// Execute the generated PAC without browser-specific DNS helpers. A Node-less
// Go-only environment can still run the HTTP/auth tests below.
func TestAppProxyPACRouting(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node.js is required to execute PAC JavaScript")
	}
	for _, settings := range []struct{ routes, dns bool }{{true, true}, {false, true}, {true, false}} {
		t.Run(fmt.Sprintf("accept-routes-%v-dns-%v", settings.routes, settings.dns), func(t *testing.T) {
			h := pacTestHost()
			h.lastPrefs.RouteAll, h.lastPrefs.CorpDNS = settings.routes, settings.dns
			program, err := h.appProxyPAC("192.168.31.80:54936")
			if err != nil {
				t.Fatal(err)
			}
			cases := map[string]string{}
			for _, host := range []string{"100.64.0.0", "100.99.94.96", "100.127.255.255", "FD7A:115C:A1E0::1", "[fd7a:115c:a1e0::1]", "fd7a:115c:a1e0:0000:0000:0000:0000:0001", "::ffff:100.99.94.96", "::ffff:6463:5e60", "nas", "NAS.TAIL123.TS.NET.", "new-node.tail123.ts.net", "photos.example", "internal.example", "wiki.internal.example"} {
				cases[host] = "PROXY 192.168.31.80:54936"
			}
			for _, host := range []string{"example.com", "1.1.1.1", "100.63.255.255", "100.128.0.0", "2001:db8::1", "fd7a:115c:a1e1::1", "fd42:abcd:1::1", "192.168.51.1", "10.1.1.1", "notinternal.example", "internal.example.evil", "tail123.ts.net.evil", "nas.evil", "127.0.0.1"} {
				cases[host] = "DIRECT"
			}
			cases["172.16.5.1"] = "DIRECT" // Self-advertised routes are not approved peer routes.
			if !settings.dns {
				cases["internal.example"], cases["wiki.internal.example"] = "DIRECT", "DIRECT"
			}
			for _, host := range []string{"192.168.50.0", "192.168.50.255", "fd42:abcd::1", "fd42:abcd:0:ffff::1"} {
				cases[host] = "DIRECT"
				if settings.routes {
					cases[host] = "PROXY 192.168.31.80:54936"
				}
			}
			input, _ := json.Marshal(struct {
				Program string            `json:"program"`
				Cases   map[string]string `json:"cases"`
			}{string(program), cases})
			cmd := exec.CommandContext(t.Context(), node, "--v8-pool-size=1", "-e", `
const fs = require("node:fs"), vm = require("node:vm"), assert = require("node:assert/strict");
const data = JSON.parse(fs.readFileSync(0, "utf8"));
const context = vm.createContext({});
vm.runInContext(data.program, context, {timeout: 1000});
for (const [host, expected] of Object.entries(data.cases)) {
  context.host = host;
  assert.equal(vm.runInContext('FindProxyForURL("https://" + host + "/", host)', context, {timeout: 1000}), expected, host);
}
`)
			cmd.Stdin = strings.NewReader(string(input))
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("PAC execution: %v\n%s", err, output)
			}
		})
	}
}

func TestAppProxyPACDownloadAndAuthBoundaries(t *testing.T) {
	h := pacTestHost()
	p, err := newExternalProxy(h, filepath.Join(t.TempDir(), "external-proxy.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.close() })
	if err := p.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	status := p.status(true)
	if status.PACPath != externalProxyPACPath || !status.AuthRequired {
		t.Fatalf("PAC status = %+v", status)
	}
	authority := fmt.Sprintf("127.0.0.1:%d", status.Port)
	client := &http.Client{Transport: &http.Transport{}}
	t.Cleanup(client.CloseIdleConnections)
	for _, test := range []struct {
		method, path, host string
		want               int
	}{
		{http.MethodGet, externalProxyPACPath, authority, 200},
		{http.MethodHead, externalProxyPACPath, authority, 200},
		{http.MethodPost, externalProxyPACPath, authority, 405},
		{http.MethodGet, externalProxyPACPath + "?host=evil", authority, 400},
		{http.MethodGet, externalProxyPACPath, "attacker.example", 400},
		{http.MethodGet, "/", authority, 407},
	} {
		t.Run(test.method+test.path+test.host, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(t.Context(), test.method, "http://"+authority+test.path, nil)
			req.Host = test.host
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, err := io.ReadAll(res.Body)
			if err != nil || res.StatusCode != test.want {
				t.Fatalf("PAC response = %d, %v, %s", res.StatusCode, err, body)
			}
			if test.want == 200 {
				if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Content-Type") != "application/x-ns-proxy-autoconfig" || res.ContentLength <= 0 {
					t.Fatal("missing PAC download headers")
				}
				if strings.Contains(string(body), status.Password) || strings.Contains(string(body), "PROXY 0.0.0.0:") {
					t.Fatal("PAC disclosed credentials or a wildcard endpoint")
				}
				if test.method == http.MethodGet && !strings.Contains(string(body), "PROXY "+authority) {
					t.Fatal("PAC did not use receiving listener address")
				}
			}
		})
	}
	proxyURL, _ := url.Parse("http://" + authority)
	proxied := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	t.Cleanup(proxied.CloseIdleConnections)
	for _, target := range []string{"http://example.com/proxy.pac", "https://100.99.94.96:2283/"} {
		res, err := proxied.Get(target)
		if res != nil {
			res.Body.Close()
			if res.StatusCode != http.StatusProxyAuthRequired {
				t.Fatalf("unauthenticated forwarding returned %d", res.StatusCode)
			}
		} else if err == nil || !strings.Contains(err.Error(), "Proxy Authentication Required") {
			t.Fatalf("CONNECT did not require authentication: %v", err)
		}
	}
	proxyURL.User = url.UserPassword(status.Username, status.Password)
	authenticated := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	t.Cleanup(authenticated.CloseIdleConnections)
	forwarded, err := authenticated.Get("http://example.com/proxy.pac")
	if err != nil {
		t.Fatal(err)
	}
	forwarded.Body.Close()
	if forwarded.StatusCode != http.StatusBadGateway || forwarded.Header.Get("Content-Type") == "application/x-ns-proxy-autoconfig" {
		t.Fatal("absolute-form request was served as a local PAC download")
	}
	recorder := httptest.NewRecorder()
	h.httpProxyHandlerWithAuth(&ProxyAuth{Username: "browser", Password: "secret"}, true).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, externalProxyPACPath, nil))
	if recorder.Code != http.StatusProxyAuthRequired {
		t.Fatal("browser listener exposed the app PAC route")
	}
	h.stateMu.Lock()
	h.lastNetMap = nil
	h.stateMu.Unlock()
	res, err := client.Get("http://" + authority + externalProxyPACPath)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatal("unavailable policy produced an incomplete PAC")
	}
	if err := p.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	if p.status(false).PACPath != "" {
		t.Fatal("disabled listener advertises a PAC URL")
	}
}
