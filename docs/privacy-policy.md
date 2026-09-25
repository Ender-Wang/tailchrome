# Tailchrome Privacy Policy

Last updated: 2026-09-25

## Summary

Tailchrome does not include analytics, advertising trackers, or data brokers. The extension does transmit user data when it is necessary to connect the browser to the user's Tailscale network through the local native helper.

## Data Stored In The Browser

Tailchrome stores the following data locally in browser storage:

- `profileId`: a generated identifier used to keep one Tailscale node per browser profile.
- `customUrls`: per-device custom open targets configured by the user.
- `domainSplitConfig`: split-tunneling mode and domain list configured by the user.
- `autoConnectOnStart`: the user's auto-connect preference.
- `uiSurface`: whether the toolbar opens Tailchrome as a popup or in the browser's side panel/sidebar.
- `routingProtectionV1`: sanitized, account-scoped routing snapshots used to keep protected traffic fail-closed across background or helper restarts. They can include control-server and node scope identifiers, the selected exit node, MagicDNS and restricted DNS names, known peer short names, subnet routes, split-tunneling rules, and pending routing transitions. Helper ports and proxy credentials are excluded.
- `lastSessionWantRunning`: a temporary local-storage fallback for the current session's connect or disconnect intent across an extension update or reload. It is cleared at the next browser startup.
- `autoConnectHandled` in session storage: a per-session flag used to avoid reconnecting automatically after an explicit manual disconnect.
- `desiredWantRunning` in session storage: the current session's connect or disconnect intent, sent to the helper when it initializes so a helper restart preserves the user's most recent choice.
- helper discovery retry progress in session storage: the retry source, next retry index, and absolute retry deadline used to resume an interrupted package or repair check.
- the current-session registration repair recommendation, which is cleared after the helper initializes successfully.

This data stays on the local device unless the user exports or syncs their browser profile separately.

On macOS, the resident helper stores its per-profile run-state map, a random
bridge token, and the optional local-app proxy configuration under
`~/Library/Application Support/Tailchrome/`. The directory is accessible only
to the current operating-system account; secret files use mode `0600`. The
local-app configuration contains its stable loopback port, fixed username,
random per-install password, enabled state, and owning browser-profile
identifier. The password is not written to browser storage or diagnostics.

## Data Transmitted By Tailchrome

When the extension is enabled, Tailchrome communicates with a local native helper over the browser's native messaging channel. That helper runs the Tailscale client logic for the current browser profile.

Depending on the features the user enables, Tailchrome may transmit:

- Browsing activity and website content needed to proxy tailnet-bound traffic, exit-node traffic, and Taildrop transfers.
- Traffic from local applications that the user explicitly configures to use the optional macOS HTTP(S)/SOCKS5 proxy.
- Authentication and session data needed to sign in to Tailscale or a custom coordination server the user configures.
- Device and network metadata required to discover peers, MagicDNS names, subnet routes, and exit nodes.
- User-initiated file contents when the user sends a file with Taildrop.

Tailchrome sends this data only to:

- the local native helper on the same machine,
- the user's tailnet and configured coordination plane (Tailscale by default), and
- the sites or services the user chooses to access through Tailchrome.

For information about how Tailscale handles data, see the
[Tailscale Privacy Policy](https://tailscale.com/privacy-policy).

## Data Tailchrome Does Not Collect

Tailchrome does not send product analytics, crash telemetry, advertising identifiers, or marketing data to the developer.

## Helper Diagnostic Reports

Tailchrome creates a helper diagnostic report only after the user clicks
**Copy diagnostic report** or **Export diagnostic report**. The report is
generated from current in-memory extension state and browser platform
information. It is not generated in the background, assigned a persistent
report identifier, uploaded automatically, or retained by Tailchrome after the
popup closes.

The report is limited to:

- report schema, extension, companion release, and reported helper versions;
- operating system, CPU architecture, and Chromium/Firefox build family;
- helper connection, initialization, and reconnect status;
- a categorized helper failure and bounded, sanitized diagnostic detail;
- helper capability flags; and
- whether current-user registration repair is available.

The formatter uses an allowlist and removes URL-like strings, control
characters, and user-profile or home-directory prefixes. It caps individual
messages and the complete report. Clipboard and exported-file actions use the
same formatted data.

The report excludes visited pages, current tabs, history, cookies, referrers,
proxy destinations, login/control URLs, authentication data, tailnet names,
MagicDNS suffixes, Tailscale IP addresses, peers, profiles, user or node
identifiers, Taildrop details, traffic counters, payloads, credentials,
filesystem user names, and registry values containing user data.

Local-app proxy credentials and the resident helper's bridge token are also
excluded. Revealed proxy credentials are sent only over the local native
connection to the popup that requested them and are not merged into persistent
extension state.

The report remains on the local device until the user copies, saves, or
chooses to share it.

## User Controls

Users can:

- disable Tailchrome from the extension popup,
- turn off auto-connect on start,
- clear split-tunneling domains,
- clear custom peer URLs from the popup,
- remove exit-node selection,
- disable the macOS local-app proxy or rotate its password, which closes existing proxy sessions,
- copy or export a local helper diagnostic report,
- log out of Tailscale, and
- uninstall the extension and native helper.

## Contact

For privacy or security questions, contact `admin@tesseras.org`.
