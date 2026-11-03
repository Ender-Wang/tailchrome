# Transport recovery first, selective iPhone proxy second

Date: 2026-11-03

Status: Stable v1.104.5 migration implemented and automated tests pass;
arm64 test packages delivered, physical network-transition verification pending.

Related designs: [resident helper](2026-09-24-local-app-proxy-design.md),
[LAN app proxy](2026-1102-01-lan-app-proxy.md).

## Verified failure

The user disconnected office Wi-Fi at 18:51 on November 3. Both embedded nodes
logged an IPv4 UDP read error (`network is unreachable`), then WireGuard's
receive routine stopped. Rejoining home Wi-Fi and rebinding sockets did not
restore the routine. The helper remained running, so the resident service's
process supervision could not detect this transport failure.

At 23:14, manually restarting the resident helper restored UDP checks and
direct NAS connections. The earlier slow DERP traffic and a running control
plane did not prove that the direct data path was healthy.

## Phase 1: recover at the transport layer

Use Tailscale's merged [PR #21902](https://github.com/tailscale/tailscale/pull/21902),
commit `e9b151bd652514884d54711e692db8fa2bdaa93b`. It treats ENETUNREACH and
EHOSTUNREACH (including the corresponding Windows errors) as UDP feedback and
continues reading instead of allowing WireGuard's receive routine to exit.
Shutdown errors still terminate the receiver normally.

The existing August dependency predates the fix. Validate the updated upstream
revision, including its required Go toolchain, against the helper before
accepting the dependency update. Preserve the previous subnet-DNS regression
coverage and existing proxy/security contracts. Do not introduce a whole-daemon
restart loop or restart healthy profile runtimes to mask a dead receiver.

The selected module version is
`v1.104.5`, requiring Go 1.27.3. The released source includes the UDP feedback
retry fix. Its transitive dependency updates are part of the validation scope.
The earlier uncommitted experiment used the merge's development revision;
the implementation now targets the stable release.

### API migration progress

- [x] Select stable Tailscale v1.104.5 and update module checksums.
- [x] Replace removed `NotifyInitialNetMap` / `Notify.NetMap` subscriptions
  with `NotifyInitialStatus`, `Status`, `PeerByID` and `DNSConfig`.
- [x] Keep full authoritative peer records for AllowedIPs, PrimaryRoutes,
  MagicDNS and exit-node DNS capabilities; do not infer permissions from
  advertised routes or the extension's abbreviated peer list.
- [x] Reject collections spanning detected profile/route changes and discard
  cached permissions if a refresh fails.
- [x] Adapt existing integration fixtures and the typed DERP region ID.
  Stable v1.104.5 retains the existing resolver response type; the development
  revision's resolver API migration is unnecessary.
- [x] Complete focused and regression validation: 251 helper test cases pass,
  no skips/failures; `go vet ./...` passes; notification integration tests
  pass ten consecutive runs.
- [x] Build the arm64 helper and deliver a macOS test installer.

Validation caught a fixture incompatibility: Tailscale's `AddFakeNode` can
generate negative random node IDs, but the production `PeerByID` endpoint rejects
non-positive IDs. The notification integration fixture now assigns its fake
router a positive ID before starting the control server. Production lookup
validation remains intact. Repeated runs cover this previously intermittent
failure.

PAC is outside this dependency-upgrade implementation. No restart button or
helper restart watchdog is added.

Acceptance checks:

- [x] Upstream `TestReceiveFuncReadFeedback` passes against stable v1.104.5,
  including repeated wrapped reachability errors and normal shutdown.
- [x] Helper Go tests and vet pass, including split-DNS routing.
- [x] macOS arm64 helper and platform-specific tests compile. Windows amd64
  helper also compiles; Go module checksums verify successfully.
- [ ] Manual: disconnect Wi-Fi and reconnect on another network without
  restarting the helper; both address-family receive loops recover.
- [ ] Manual: browser and LAN app proxies reconnect with unchanged identities,
  listener settings, ports and credentials.

A network transition will interrupt active requests. Recovery means that new
requests work after connectivity returns, not that every TCP stream survives.
It also cannot guarantee a direct path through a network that blocks UDP.

## Phase 2: PAC routing and iOS authentication

The desired iPhone setting is Wi-Fi → HTTP Proxy → Automatic → URL.
PAC should send tailnet destinations to the Mac's app proxy and ordinary
websites DIRECT. Protected matches must return only the proxy, without a DIRECT
fallback. This changes routing decisions on the client; it does not add the
browser extension's filter/bypass settings to the app proxy.

PAC does not carry HTTP proxy credentials. The iPhone Automatic settings have
no username/password fields, so a PAC URL alone is not a complete authenticated
setup. Do not embed credentials in the PAC body or assume credentials in the
PAC download URL authenticate later CONNECT requests. Do not silently disable
authentication to make Automatic mode work.

Before implementing the PAC setup flow:

- Verify whether iOS/Safari presents and retains the proxy's HTTP 407 challenge
  after choosing Automatic, and document that other apps may behave differently.
- Define supported destination matches (MagicDNS, Tailscale IPv4/IPv6 ranges,
  accepted subnets and restricted DNS domains) and collision handling.
- Define a non-secret PAC download endpoint distinct from authenticated proxy
  forwarding, its LAN exposure, cache policy and behavior when the Mac is absent.
- Verify the platform's behavior when the PAC URL is unreachable; an intended
  fail-closed PAC rule cannot control what the OS does before loading the PAC.

Manual mode with explicit credentials remains the supported configuration until
these authentication and failure-behavior checks pass.

## Delivery boundaries

Keep lifecycle recovery and PAC support as separate reviewable changes. Do not
commit or push without the user's instruction. macOS test packages remain
arm64-only; Firefox/Zen artifacts use `.xpi`, Edge artifacts use `.zip`.

### Local test delivery

Helper version: `0.1.15-844705a-transport-uncommitted`, embedding Tailscale
`1.104.5`. The suffix identifies an uncommitted build based on `844705a`.
No browser extension source or native protocol change is required.

Packages in `~/Documents`:

- `tailchrome-helper-macos-arm64-0.1.15-844705a-transport-uncommitted.pkg`
- `tailchrome-helper-macos-user-arm64-0.1.15-844705a-transport-uncommitted.zip`

The installer payload was expanded and compared with the built binary; bundle
relocation is disabled. The arm64 binary runs its version command and its
ad-hoc code signature verifies. Packages have not been installed automatically.

Physical verification:

1. Install the helper package and confirm NAS access through the existing
   browser and authenticated app proxy configurations.
2. Disconnect Wi-Fi, then reconnect, preferably on another network. Leave the
   helper running throughout.
3. Confirm new requests recover without manually restarting the helper, and
   check that the receive-worker warning does not persist.
4. For the iPhone test, update the proxy server to the Mac's new LAN address
   when changing networks. The saved proxy port and credentials should remain
   unchanged. Existing TCP connections are allowed to drop during the switch.
