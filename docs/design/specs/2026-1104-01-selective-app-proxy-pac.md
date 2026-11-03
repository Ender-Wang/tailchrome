# Selective automatic proxy configuration

Date: 2026-11-04

Status: Implemented; automated checks pass. iPhone authentication and physical
network-transition verification remain pending.

Related: [transport recovery tracker](2026-1103-01-transport-recovery-and-pac.md),
[resident app proxy](2026-09-24-local-app-proxy-design.md),
[LAN sharing](2026-1102-01-lan-app-proxy.md).

## Scope

The resident helper serves a PAC file from the existing app listener at
`http://<selected-Mac-address>:<saved-port>/proxy.pac`. The extension offers a
copy button beside the existing connection details. No browser relays traffic
or hosts this URL; it remains available while the browser is closed.

PAC directs Tailscale IPv4/IPv6 addresses, the owning node's MagicDNS namespace
and known node names, DNS aliases/restricted domains, and accepted approved
subnets to the app proxy. Ordinary destinations return `DIRECT`, including when
an exit node is selected. A protected match returns only `PROXY host:port`,
without a DIRECT fallback. The proxy's existing destination checks remain the
authority for forwarding. Browser filter/bypass settings are unchanged.

The script performs literal address parsing and string matching only. It does
not resolve private names through the client's DNS. IPv6 and IPv4 subnet
matching must work without optional PAC helpers such as `isInNetEx`.

## Listener and authentication boundaries

- Only the app listener exposes the exact origin-form `/proxy.pac` route.
  Browser listeners and absolute-form forwarding requests retain authentication.
- PAC download is unauthenticated because iPhone Automatic has only a URL field.
  The document contains routing metadata, never passwords or credential URLs.
  Existing LAN exposure remains opt-in; the PAC route follows that listener.
- The request authority must match the receiving socket's local address. The
  proxy address in the script comes from that socket, not a supplied Host header.
- GET and HEAD are supported. Other methods and unavailable policy snapshots
  produce errors; responses request no caching.
- HTTP/CONNECT and SOCKS5 forwarding still require the existing credentials.
  PAC cannot supply them. Safari may present a proxy-authentication challenge;
  other apps may differ. Do not promise a complete one-URL authenticated setup
  until physical-device verification succeeds; Manual remains the fallback.

## Configuration refresh and limitations

Each download reflects the owner's current cached routing policy. iOS controls
PAC refresh timing; after route/DNS changes, reload Automatic configuration.
Changing networks may change the Mac's address and require updating the URL.
The Mac must remain awake and reachable. PAC affects only applications that
honor the Wi-Fi HTTP proxy and does not create a device-wide VPN.

If iOS cannot download or execute PAC, the OS determines fallback behavior.
The script's protected-route rules cannot enforce behavior before it is loaded.

## Progress and acceptance

- [x] Implement helper PAC generation and local download route.
- [x] Expose the PAC URL in the extension with accurate setup guidance.
- [x] Execute generated PAC JavaScript in Node: IPv4/IPv6 (including mapped
  addresses), names and suffix boundaries, approved/unapproved/self subnets,
  disabled route/DNS acceptance and public DIRECT routing with an exit node.
- [x] Check GET/HEAD, Host spoofing, methods, query parameters, missing policy,
  disabled listener, browser isolation and authenticated forwarding boundaries.
- [x] Run helper regression checks and vet, shared/browser extension tests and
  TypeScript checks; compile the arm64 helper and macOS tests plus Windows helper.
- [ ] Manual: download PAC from iPhone and authenticate through Safari.
- [ ] Manual: NAS access uses the Mac; an ordinary website works directly with
  the app proxy unavailable after PAC has been loaded.
- [ ] Manual: changing Wi-Fi recovers without restarting the helper.

No credentials are embedded in PAC and no unauthenticated forwarding mode is
introduced to work around missing Automatic-mode credential fields.

## Validation and delivery

Validation: 262 Go test cases, 781 shared-extension tests and 118 browser tests
pass. Go vet and TypeScript checks pass. The Node-based PAC execution tests
require Node.js on PATH; Go-only environments explicitly skip that execution
test instead of claiming JavaScript execution coverage.

Keep this feature in a separate signed commit after transport fix `1d22735`.
Generate final artifacts from the committed feature HEAD with version
`0.1.15-<commit>-pac` into `~/Documents`:

- macOS arm64 installer `.pkg` and per-user app `.zip`;
- Chromium/Edge extension `.zip`;
- Firefox/Zen extension `.xpi` (not the Firefox source-code archive).

The new packages include both commits. Do not install automatically or push
without a separate request. Physical tests remain the user's next step.
