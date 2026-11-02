# LAN access for the app proxy

Date: 2026-11-02

Status: Implemented; iPhone-to-NAS HTTPS access verified on 2026-11-02.

Extends [the resident helper and local-app proxy design](2026-09-24-local-app-proxy-design.md).

## Goal

An iPhone on the same trusted network can access tailnet HTTP/HTTPS services
through the Mac without installing Tailscale on the phone. The Mac must remain
awake and reachable, and the client application must honor its Wi-Fi proxy.

## Behavior

- Localhost remains the default, including for existing configurations.
- A separate **Allow devices on the local network** toggle enables an IPv4
  wildcard listener (`0.0.0.0`). It affects only the app proxy.
- HTTP/CONNECT and SOCKS5 continue to require the existing username/password.
- Changing scope closes established sessions and rebinds the same saved port.
- LAN mode persists across helper restarts. Disabling the app proxy also clears
  LAN mode so enabling it later starts on localhost.
- The extension offers localhost and private IPv4 connection addresses in a
  selector. It never suggests `0.0.0.0` as a client destination.
- Passwords remain available only through explicit reveal/rotation replies.
- Connection details show one selected address, four compact fields and two URL
  copy actions. The password starts masked; copy feedback names the field rather
  than repeating credentials. iPhone setup is an expandable disclosure.
- Older helpers do not advertise LAN support and do not show the new toggle.
- All app traffic uses the owner's tsnet profile; browser filters and bypass
  rules, browser authentication probes, and browser listeners stay independent.

## Exposure and iPhone setup

Wildcard binding accepts connections on every IPv4 interface allowed by the
Mac firewall, not just the current Wi-Fi network. The private-address list is
connection guidance, not a source-address firewall. Enable this only on trusted
networks; no firewall rules or router port forwarding are installed.

HTTP Basic and SOCKS5 credentials are not encrypted on the phone-to-Mac hop.
HTTPS payloads remain encrypted through CONNECT. No proxy TLS transport,
IPv6 listener, per-client Tailscale identity, or full-device VPN is introduced.

On iPhone: Settings → Wi-Fi → current network → HTTP Proxy → Manual.
Enter a listed Mac LAN IPv4 address, the proxy port, and enable authentication
with the revealed username/password. Open the NAS's Tailscale address in Safari.
Do not paste a URL or credentials into the Server field.

Use the service's HTTPS hostname when its TLS listener requires a matching
server name. During verification, the NAS's Tailscale IP produced a TLS
internal-error alert, while its MagicDNS hostname completed certificate
verification and returned HTTP 200 through the same proxy.

The proxy retains its enabling browser profile as owner. LAN-scope changes
must come from that profile. To transfer ownership, disable the app proxy and
enable it from the desired profile. Currently a non-owner can see the LAN
toggle, but its rejected request appears as a generic error; improving that
ownership feedback remains a UI follow-up.

## Validation tracker

- [x] Go tests: safe default, scope persistence, rebind and revocation,
  credential retention, IPv4-only socket and HTTP/SOCKS authentication.
- [x] Extension tests: capability gate, toggle command, scope changes, address
  display/copy and secret handling.
- [x] Type-check and build Edge-compatible Chromium and Firefox extensions.
- [x] Build macOS arm64 helper packages with explicit development identification.
- [x] Manual: iPhone Safari reaches the NAS over HTTPS through the Mac's LAN
  proxy without Tailscale running on the iPhone (user confirmed).
- [ ] Manual: Safari → NAS on the same Wi-Fi, Mac browser closed.
- [ ] Manual: turn LAN mode off and confirm the phone loses access while a
  localhost client still works.
- [ ] Manual: helper restart retains enabled LAN scope and port; password
  rotation rejects old credentials; firewall/guest Wi-Fi failures are visible.

## Delivery

The original local-proxy commit is rebased on upstream `13c87cb` (v0.1.15).
LAN access is delivered as a separate feature commit. Test artifacts go in
`~/Documents`; they are development builds rather than store-signed releases.

The signed rebased original local-proxy feature commit is `7de37ab`.
The local build version is `0.1.15-7de37ab-lanproxy-dev20261102`. That suffix
explicitly identifies uncommitted LAN changes, rather than claiming that HEAD
alone contains them. Browser manifests retain the valid numeric version 0.1.15.

The subsequent extension-only UI refresh uses the artifact suffix
`0.1.15-7de37ab-lanproxy-ui2-dev20261102`. It works with the same helper and
does not require reinstalling the macOS package. The connection card uses one
address selector instead of listing every URL, masks the password, and keeps
clipboard confirmations free of credentials.

Validation runs in sequential Docker containers capped at two CPU cores.
The complete Linux Go suite and vet passed; macOS-specific daemon code is
cross-compiled. iPhone HTTPS access was subsequently verified; browser-closed
access, restart, rotation and firewall scenarios remain manual checklist items.
The 780 shared and 118 browser-adapter unit tests passed. After the UI refresh,
49 focused popup and utility tests, type-checking and Firefox lint passed.
