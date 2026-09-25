# Security Policy

## Supported Versions

Only the latest release is supported with security updates.

## Reporting a Vulnerability

Please report security vulnerabilities by emailing admin@tesseras.org.

Do **not** open a public issue for security vulnerabilities.

We will acknowledge your report within 48 hours and aim to release a fix for critical issues within 7 days.

## Verifying Release Artifacts

Download artifacts only from this repository's GitHub Releases page. Verify the release checksum manifest before running an installer or helper:

```bash
sha256sum --check SHA256SUMS.txt
```

On macOS, verify the Developer ID and notarization assessment:

```bash
pkgutil --check-signature tailchrome-helper-macos.pkg
xcrun stapler validate tailchrome-helper-macos.pkg
spctl --assess --type install --verbose=2 tailchrome-helper-macos.pkg
```

On Windows, inspect both the raw helper and MSI:

```powershell
Get-AuthenticodeSignature .\tailscale-browser-ext-windows-amd64.exe |
  Format-List Status,SignerCertificate,TimeStamperCertificate
Get-AuthenticodeSignature .\tailchrome-helper-windows-x64.msi |
  Format-List Status,SignerCertificate,TimeStamperCertificate
```

Both Windows files must report `Valid`, contain a timestamp certificate, and use the exact publisher subject recorded in the Windows code-signing policy. The MSI contains the same signed helper released as the raw EXE; publication verifies the embedded file's SHA-256 against the raw file.

Where GitHub artifact attestations are available, verify them against this repository:

```bash
gh attestation verify <artifact> --repo dantraynor/tailchrome
```

## Scanner Detections and SmartScreen

An actual Defender or Malwarebytes malware, potentially unwanted application, or behavioral detection blocks release. Report a suspected false positive with the exact file hash and detection details through the [Microsoft Security Intelligence submission portal](https://www.microsoft.com/en-us/wdsi/filesubmission) or [Malwarebytes false-positive process](https://help.malwarebytes.com/hc/en-us/articles/31589211404571-Report-a-false-positive-to-Malwarebytes-Support). Do not publish the affected candidate while a vendor determination is pending.

A validly signed application can still show a normal Microsoft Defender SmartScreen “unrecognized app” prompt while the publisher or file builds reputation. That prompt alone is not a malware determination. Confirm the signature first; report a malicious or PUA classification, invalid signature, or other concrete detection separately.

## Local Helper Diagnostics

Helper diagnostic reports are generated only when the user clicks the copy or export action. They remain local until the user chooses to share them. Reports use an explicit allowlist, bound and sanitize native error text, and exclude browsing data, URLs, authentication data, tailnet and peer identity, profile identity, traffic data, credentials, and persistent tracking identifiers.

## Local Proxy Trust Boundary

The browser proxy listens on a randomly assigned `127.0.0.1` port and requires a fresh random credential on every helper launch. Credentials are sent over native messaging and held only by the background proxy manager, outside popup state, storage, and diagnostics. Chromium uses authenticated HTTP proxying; Firefox uses authenticated SOCKS5.

On macOS, users may opt in to a separate mixed HTTP/SOCKS5 listener for local apps. It has an independent stable port and a per-install random password stored in a mode-`0600` file inside a mode-`0700` directory. Authentication is mandatory. The extension keeps non-secret status only; it requests the password on demand and returns it solely to the popup that requested reveal or rotation. Disabling the listener or rotating its password closes established sessions. The listener binds only to `127.0.0.1` and does not expose the helper's local web client.

The macOS LaunchAgent accepts browser bridges on a mode-`0600` Unix socket and
requires a random mode-`0600` bridge token before native-message framing begins.
The bridge must then identify its browser profile in the first `init` message;
that connection cannot change profiles later. Each profile retains a separate
`tsnet` server, state directory, browser listener, and ephemeral browser proxy
credential. The local-app listener is attached to the profile that enabled it,
and changing that owner requires disable then enable rather than merging
profile identities.

Logging out or creating, switching, or deleting a Tailscale account profile
disables the local-app listener and closes its sessions before the identity
transition. Re-enablement is explicit.

Update the extension and helper together. The extension blocks proxy use when a helper does not provide the authenticated proxy capability; current helpers do not expose an unauthenticated compatibility listener.

The helper checks destinations against its authoritative network map and current preferences. It permits Tailscale addresses, approved subnet routes, public destinations through a selected exit node, and attached private LAN destinations when LAN access is explicitly enabled. Loopback, link-local, and multicast destinations are blocked. The local Tailscale web client is authenticated separately before dispatch. DNS answers are checked before literal addresses are dialed; protected connections cannot fall back to the system network after route removal. Browser domain split and bypass settings remain a browser-only decision; they neither filter nor directly route requests accepted by the local-app listener.

This limits access by other local users and processes that can discover the port. It does not protect against processes that can read the browser or helper memory or control the same operating-system account.
