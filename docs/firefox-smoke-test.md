# Firefox Smoke Test Matrix

## Matrix

Run the full checklist on:

| OS | Architecture | Firefox | Helper installer |
| --- | --- | --- | --- |
| macOS 14+ | Intel and Apple Silicon | 142+ stable | Per-user `tailchrome-helper-macos-user.zip`; system `.pkg` alternative |
| Windows 11 | x64 | 142+ stable | `tailchrome-helper-windows-x64.msi` |
| Ubuntu 24.04+ | amd64 | 142+ stable | Per-user `tailchrome-install.sh`; `.deb` alternative |
| Ubuntu 24.04+ | arm64 | 142+ stable | Per-user `tailchrome-install.sh` selecting the native ARM64 helper |

## Preconditions

- Fresh Firefox profile
- Matching Firefox candidate archive, with its hash recorded; temporary
  installation is sufficient for prepublication functional checks. Persistent
  installation/restart acceptance requires a signed installed build separately
  (scenario 8).
- Matching helper artifacts from the same candidate or published release,
  verified against its checksums. macOS requires platform signing; Windows
  follows the candidate's declared mode under the
  [Windows code-signing policy](WINDOWS_CODE_SIGNING_POLICY.md).
- For an explicitly unsigned Windows candidate, record signatures as absent
  under that exception, never as passing signature validation. A PowerShell
  install requires the documented `-AllowUnsigned` opt-in; invalid signatures
  must still be rejected.
- Disposable Tailscale reviewer account
- Test tailnet with MagicDNS peer, subnet route, exit node, and Taildrop target

## Scenarios

### 1. Helper Install

Steps:

1. Open the popup immediately after installing the extension.
2. Confirm the setup-required view appears.
3. Run the primary per-user setup for the current OS. For unpublished candidates,
   use verified local package/app assets or place the verified raw helper at a
   stable user-owned path and run its `install` command. The scripts download
   from public release URLs, so their real download flow remains a separate
   check once those assets are available.
4. Re-open the popup.

Pass:

- The setup-required state clears after the helper is installed.
- No Firefox native messaging permission errors remain in the popup.
- Linux amd64 and arm64 profiles offer the per-user terminal command first.
- On Linux amd64, **Other installation options** offers DEB/RPM packages.
- A Linux arm64 profile does not offer an incompatible amd64 package.

### 2. Login Flow

Steps:

1. Click the login action from the popup.
2. Complete login with the disposable account.
3. Return to Firefox and reopen the popup.

Pass:

- Tailnet name and self node appear.
- The extension reaches `Running` state.

### 3. Toggle On / Off

Steps:

1. Toggle Tailchrome off.
2. Confirm tailnet routes stop working.
3. Toggle Tailchrome back on.

Pass:

- Proxy state transitions cleanly between direct and tailnet routing.
- Re-enabling restores running state without reinstalling the helper.

### 4. MagicDNS

Steps:

1. Open a known MagicDNS hostname from the popup or browser location bar.

Pass:

- The host resolves and loads through Tailchrome.

### 5. Subnet Routing

Steps:

1. Open a service that is reachable only through an advertised subnet route.

Pass:

- The request succeeds while Tailchrome is enabled.
- The same target is unreachable after Tailchrome is disabled.

### 6. Exit Nodes

Steps:

1. Select an exit node in the popup.
2. Browse to an external site.
3. Clear the exit node.

Pass:

- External browsing is routed through the selected exit node while enabled.
- Clearing the exit node returns external traffic to direct routing.

### 7. Taildrop

Steps:

1. Send a small text file to a Taildrop-capable peer.

Pass:

- Progress updates appear in the popup.
- The target peer receives the file.

### 8. Browser Restart / Background Wake

Steps:

1. Use a signed installed extension for this persistence check; temporary
   extensions must be reloaded after restart and cannot establish persistent
   installation. Enable **Auto-connect on start**, then fully quit and restart
   Firefox while Tailchrome is running.
2. Reopen the popup and access a MagicDNS host.

Pass:

- The extension reconnects to the helper.
- Saved account-scoped routing protection restores, and a healthy authenticated
  helper session restores routing without manual reconfiguration.
- If only a temporarily loaded candidate is available, record persistence as
  pending; reloading it tests recovery separately.

### 9. Missing Helper

Steps:

1. Remove the helper/native messaging manifest.
2. Restart Firefox and open the popup.

Pass:

- The popup says that no registered helper was found without naming a guessed
  browser product, antivirus action, or manifest path.
- The platform's per-user setup remains the primary action before an install
  attempt; system packages stay under **Other installation options**.
- After discovery retries are exhausted, **Repair registration for this
  browser** becomes prominent and survives a background-context restart.
- A successful repair clears the recommendation and returns to the normal
  state.

### 10. Registration Refused

Steps:

1. Keep the helper installed, but remove this Firefox extension ID from its
   `allowed_extensions` registration in the disposable profile.
2. Restart Firefox and open the popup.

Pass:

- The popup says that the browser refused access to the registered helper.
- Registration repair and local diagnostic actions are offered.
- Raw browser error text is absent from the recovery copy.

### 11. Early Start Failure and Later Stop

Steps:

1. Force the registered helper to exit before its first valid reply.
2. Restore it, connect successfully, then terminate it after initialization.

Pass:

- The first case says that the helper stopped before setup completed.
- The second case says that the helper stopped after connecting and shows
  reconnect progress.
- Retry reconnects without changing an unrelated connection preference.
- A healthy reply clears failure state and reconnect backoff.

### 12. Helper-Reported Startup Error

Steps:

1. Use a test helper that returns an `init.error` or `procRunning.error`.

Pass:

- The popup says that the helper reported a startup error.
- The primary copy does not expose the raw helper string.
- The sanitized detail appears only after generating a local diagnostic
  report.

### 13. Helper Release Difference and Capabilities

Steps:

1. Use a helper fixture that supplies a valid authenticated-proxy session but
   reports an older version than the extension release.
2. Open the popup.
3. Repeat with a newer, missing, and unparsable reported version, retaining
   the valid authenticated-proxy session.
4. Repeat with fixtures that omit all optional capability flags and advertise
   one optional capability, still supplying the mandatory proxy session.
5. Separately pair the current extension with the actual v0.1.13 helper, which
   lacks authenticated-proxy support. Then pair the v0.1.13 extension with the
   current helper and request a tailnet destination.

Pass:

- With a valid authenticated-proxy session, older and newer reported versions
  reach the same normal login/running views.
- A valid difference shows only a non-blocking release notice.
- An unparsable or missing helper version does not create an incompatibility
  error.
- Controls are enabled only for advertised capabilities.
- Version difference alone does not change Firefox proxy recovery or the
  warning badge.
- The current extension rejects the v0.1.13 helper with an incompatible-helper
  setup view. Its diagnostic code is `helper-proxy-auth-required`, the diagnostic
  message explains that the helper and extension must be updated together, and
  that helper's proxy is not enabled.
- The current helper rejects the v0.1.13 extension's unauthenticated SOCKS
  request. Normal protected browsing resumes only after both components are
  updated. Do not treat a working popup/native-messaging connection as proof
  that this mixed pair can route traffic.

### 14. Local Diagnostic Report

Steps:

1. Trigger one helper failure containing a home-directory path, URL, control
   characters, and oversized text.
2. Click **Copy diagnostic report**.
3. Click **Export diagnostic report**.

Pass:

- Clipboard and file contain the same bounded allowlisted report.
- The report contains the failure category/code and sanitized detail.
- It contains no URL, home-directory user name, browser history/tab data,
  authentication data, tailnet, MagicDNS suffix, Tailscale IP, peer, profile,
  traffic, or Taildrop data.
- No report is created or submitted before either button is clicked.
