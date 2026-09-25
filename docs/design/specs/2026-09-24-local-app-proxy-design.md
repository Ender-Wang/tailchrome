# Local App Proxy and Resident macOS Helper Design

**Date:** 2026-09-24  
**Last updated:** 2026-09-25
**Status:** Implemented
**Scope:** macOS helper lifetime, native-message bridging, and opt-in local-app proxy

## Problem

Tailchrome's browser helper historically lived only as long as a browser native
messaging connection. A non-browser application such as Nextcloud needs a
stable proxy endpoint that remains available after the browser closes, but it
must not require Tailchrome to install system routes or use the browser
extension as a traffic relay.

The change must preserve Tailchrome's existing isolation and routing contract:

- every Chrome or Firefox profile remains a distinct Tailscale node;
- browser PAC/`proxy.onRequest`, domain split, and bypass behavior remain owned
  by the extension;
- adding or losing the local-app capability cannot disable existing browser
  features; and
- protected traffic never falls back to an unprotected direct connection.

## Decision

On macOS, helper registration installs a per-user LaunchAgent that runs the
same helper binary with the `daemon` subcommand. Browser-launched native hosts
become short-lived stdin/stdout bridges to that daemon. The daemon owns a
separate `Host`, `tsnet.Server`, browser proxy listener, IPN watcher, and
ephemeral browser credential for every browser-profile UUID it has seen.

The daemon additionally owns one optional local-app listener on `127.0.0.1`.
It multiplexes authenticated HTTP proxying, HTTPS through CONNECT, and SOCKS5
on one stable port. It is attached to the independent Tailchrome profile that
enabled it and stays attached across daemon restarts. To select another
profile, the user disables the listener and enables it from that profile.

The resident-service integration is currently macOS-only and capability-gated;
the HTTP/SOCKS5 proxy implementation itself uses portable Go networking APIs.
Linux and Windows retain the existing browser-lifetime helper until they gain
their own per-user service and bridge implementations. Older helpers keep every
feature they already advertise; the extension merely omits the local-app
control.

## Architecture

```text
Chrome profile A ─ native bridge ─┐       ┌─ Host A ─ tsnet node A
Firefox profile B ─ native bridge ├─ Unix ├─ Host B ─ tsnet node B
                                  │ socket│
                                  │       └─ app proxy ─ owner Host ─ tsnet
                                  └─ macOS LaunchAgent daemon
```

The bridge socket and token are local control-plane mechanisms, not traffic
proxies. Application payloads connect directly to the daemon's TCP listener;
they never traverse native messaging or extension JavaScript.

### Browser connection selection

The extension already sends `init` immediately after opening native messaging.
The daemon therefore waits for that first framed request before selecting a
runtime and returning `procRunning`.

1. The bridge authenticates with the random installation token.
2. The first native request must be `init` with a valid 1–64 character profile
   identifier.
3. The daemon creates or selects exactly that profile runtime.
4. The connection is added only to that runtime's writer hub.
5. A later `init` with a different identifier is rejected.

Multiple popups for one browser profile may share a runtime and receive its
status updates. Different profile identifiers never share a writer hub,
browser listener, credential, state directory, or `tsnet.Server`.

### Resident state

`daemon.json` stores a map from browser-profile identifier to the last requested
`WantRunning` state. At daemon launch, every known profile is restored using
its own historical `~/.config/tailscale-browser-ext/<UUID>/` directory. The
daemon extends profile lifetime; it does not create a canonical shared identity.

The local-app configuration stores its enabled state, stable port, username,
random password, and owner profile. If enabled, the owner profile is restored
before the listener starts.

## Proxy Boundaries

| Boundary | Browser proxy | Local-app proxy |
| --- | --- | --- |
| Lifetime | Profile runtime | Persistent until disabled/uninstalled |
| Listener | Random `127.0.0.1` port | Stable `127.0.0.1` port |
| Credential | Fresh random value per runtime | Stable random per-install password; rotatable |
| Protocol used | HTTP for Chromium; SOCKS5 for Firefox | HTTP, HTTPS CONNECT, and SOCKS5 |
| Route selection | Extension PAC/Firefox matching, including split/bypass | Every request submitted by the configured app |
| Dial path | Owning profile's policy-checked `tsnet` dialer | Owning profile's same policy-checked `tsnet` dialer |
| Direct fallback | None for protected routes | None |
| Local web client | Available with its independent authorization checks | Not exposed |

The app listener deliberately has no knowledge of URL filters or browser
settings. It sees only proxy destination host/port values. Accepting a request
means “attempt this through the selected Tailchrome node”; an unavailable or
unauthorized route is an error.

### Chromium authentication probe

Chromium cannot expose another extension's proxy authentication challenge to
Tailchrome. The browser listener therefore recognizes an authenticated `HEAD`
request to the reserved `tailchrome-proxy-auth.invalid` hostname and answers it
locally so the extension can prepare Chromium's credential cache before
enabling protected routes. This endpoint is enabled only on browser listeners.
The local-app listener uses separate credentials and sends the same reserved
hostname through its normal tsnet dial path, preserving the rule that it has no
browser-only routing behavior.

## Credentials and Secret Flow

- Username is the fixed value `tailchrome`.
- Password is generated randomly once per installation and stored in
  `external-proxy.json`.
- Authentication is mandatory for HTTP and SOCKS5.
- Normal status replies and `TailscaleState` omit the password.
- Reveal and rotation replies are delivered only to the popup port that made
  the request; they are not broadcast or persisted by the extension.
- Disabling the listener or rotating the password closes the listener and all
  accepted connections before any replacement listener starts.
- Logging out or creating, switching, or deleting a Tailscale account profile
  disables the app listener before the identity transition. The user must
  explicitly re-enable it for the resulting identity.
- Browser and app credentials are independent and cannot authenticate to the
  other listener.

## Local Files and Permissions

```text
~/Library/LaunchAgents/org.tesseras.tailchrome.helper.plist   0644
~/Library/Application Support/Tailchrome/                    0700
  helper.sock                                                0600
  bridge.token                                               0600
  daemon.json                                                0600
  external-proxy.json                                        0600
  helper.log
```

The Unix socket restricts filesystem access to the current user, and the
random token adds an application-level check before native framing. This does
not defend against a process that can inspect memory or control the same macOS
account.

## Installation and Removal

Every macOS registration path installs or repairs the LaunchAgent after browser
manifests are registered. It uses the exact validated helper path, `RunAtLoad`,
and `KeepAlive`. Upgrade bootstraps the new plist after booting out an older
instance.

Uninstall boots out the agent and removes the plist, socket, token, daemon
run-state file, local-app proxy configuration, and helper log. It intentionally
preserves the historical per-profile Tailscale identity directories.

## Protocol Additions

`procRunning` adds the optional capability flags `supportsDaemonControl` and
`supportsExternalProxy`. New commands are:

- `get-external-proxy-status`
- `set-external-proxy { enabled }`
- `reveal-external-proxy`
- `rotate-external-proxy-credentials`

The `externalProxy` reply reports enabled/running state, host, port, protocols,
authentication requirement, username, optional error, and a password only for
explicit reveal or rotation.

## Failure Behavior

- A missing capability hides/disables only the new control.
- A stale bridge token with no daemon is an explicit helper-unavailable failure;
  it does not start a competing in-process session.
- Failure to rebind the saved app port leaves the feature enabled but reports a
  non-secret error; it does not choose a different silent port.
- App-proxy route failure is returned to the app and never retried directly.
- Browser routing continues on its own listener if the app listener fails.
- A stalled bridge writer is bounded by a write deadline and removed from its
  profile hub without stopping the profile.

## Verification Contract

Automated tests cover protected config persistence, HTTP and SOCKS5
authentication, credential separation, session revocation, web-client and
browser-auth-probe exclusion, native protocol validation, secret delivery,
profile-state isolation, daemon bridge startup, service configuration,
Chromium credential-cache preparation, and existing Chromium and Firefox
routing suites. The release checklist separately requires manual verification
of simultaneous browser profiles, daemon restart, browser-closed app traffic,
package upgrade, and complete service-file cleanup on uninstall.
