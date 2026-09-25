# macOS helper installation

## Install for this user — no administrator access required

Download `tailchrome-helper-macos-user.zip` from the matching release, unzip it,
and open **Tailchrome Helper**. The signed, notarized app includes the universal
helper and establishes its stable location at `~/Applications/Tailchrome Helper.app`.
Open that app again to repair registration. These instructions describe v0.1.14
and later; older releases use their historical runtime-copy installer.
It works on Apple Silicon and Intel Macs.

Your organization’s browser policy may still block extensions or native messaging.

## System package

Homebrew users can install the same signed release package with the
[Tailchrome cask](../homebrew/README.md#macos). It supports Apple Silicon and
Intel Macs and uses the package's existing registration and repair flow.
The [source formula](../homebrew/README.md#source-formula-macos-and-linux) is also
available for users who prefer to build the helper locally and register it
without administrator privileges.

The script `build-pkg.sh` builds both installers. The system package
`dist/tailchrome-helper-macos.pkg` requires administrator access and installs:

1. **Universal** `tailscale-browser-ext` at  
   `/Library/Application Support/Tailscale/BrowserExt/tailscale-browser-ext`
2. **Tailchrome Helper** in `/Applications` — a repair/re-run fallback app.

The package postinstall script registers its installed executable directly
with `install --binary-path` for the logged-in console user. Registration also
creates and starts the per-user LaunchAgent
`~/Library/LaunchAgents/org.tesseras.tailchrome.helper.plist`. The daemon keeps
each browser profile's independent Tailscale node and the opt-in local-app
proxy available independently of browser lifetime; browser native-messaging
launches attach to their own profile through a protected Unix socket instead
of creating a second node. The app proxy belongs to the profile that enables
it and never consumes browser split-tunneling or bypass rules.

If browser discovery is later damaged, open
`/Applications/Tailchrome Helper.app`. The signed app launches the installed
system helper with `install --binary-path` and recreates the current user's
native-messaging registrations without copying the executable.

## Unsigned builds

CI and local runs without signing identities produce unsigned installers. Gatekeeper may require **right-click → Open** the first time, or **System Settings → Privacy & Security**.

## Signing and notarization (release quality)

Requirements: Apple Developer Program, **Developer ID Application** and **Developer ID Installer** certificates installed in the Keychain (or provided to CI via a `.p12` export — prefer a dedicated CI keychain on a runner you control).

1. Set identities (exact names from `security find-identity -p basic -v`):

   ```bash
   export MACOS_SIGN_APPLICATION_IDENTITY="Developer ID Application: Your Team (TEAMID)"
   export MACOS_SIGN_INSTALLER_IDENTITY="Developer ID Installer: Your Team (TEAMID)"
   ```

2. Build:

   ```bash
   ./packaging/macos/build-pkg.sh
   ```

3. Notarize both installers and staple their tickets:

   ```bash
   xcrun notarytool submit dist/tailchrome-helper-macos.pkg \
     --apple-id "$APPLE_ID" \
     --team-id "$APPLE_TEAM_ID" \
     --password "$APPLE_APP_SPECIFIC_PASSWORD" \
     --wait
   xcrun stapler staple dist/tailchrome-helper-macos.pkg
   xcrun notarytool submit dist/tailchrome-helper-macos-user.zip \
     --apple-id "$APPLE_ID" \
     --team-id "$APPLE_TEAM_ID" \
     --password "$APPLE_APP_SPECIFIC_PASSWORD" \
     --wait
   xcrun stapler staple "dist/Tailchrome Helper.app"
   rm dist/tailchrome-helper-macos-user.zip
   ditto -c -k --keepParent "dist/Tailchrome Helper.app" dist/tailchrome-helper-macos-user.zip
   ```

Store Apple credentials in GitHub Actions secrets for automated release; do not commit them.

Verify the final, stapled package and its repair app before candidate assembly:

```bash
pkgutil --check-signature dist/tailchrome-helper-macos.pkg
xcrun stapler validate dist/tailchrome-helper-macos.pkg
pkgutil --expand-full dist/tailchrome-helper-macos.pkg expanded-pkg
codesign --verify --deep --strict --verbose=2 \
  "expanded-pkg/Payload/Applications/Tailchrome Helper.app"
spctl --assess --type execute --verbose=2 \
  "expanded-pkg/Payload/Applications/Tailchrome Helper.app"
```

## GitHub Actions

Pull-request CI builds and inspects both installers and tests the per-user
launcher. Release CI signs and notarizes both, staples the package and app,
and includes the final archive in release checksums and provenance attestations.
Publication rechecks signatures and tickets without rebuilding.

## Per-user fallback

For terminal setup or repair, use the popup's version-pinned command or
`tailchrome-install.sh` from a published release. The script defaults to latest
stable and also accepts `--version vX.Y.Z`. To inspect it before running,
replace `vX.Y.Z` below with the chosen release:

```bash
VERSION=vX.Y.Z
BASE_URL="https://github.com/dantraynor/tailchrome/releases/download/$VERSION"
curl --fail --location --proto '=https' --tlsv1.2 \
  --output tailchrome-install.sh "$BASE_URL/tailchrome-install.sh"
curl --fail --location --proto '=https' --tlsv1.2 \
  --output SHA256SUMS.txt "$BASE_URL/SHA256SUMS.txt"
awk '$2 == "tailchrome-install.sh" { print }' SHA256SUMS.txt \
  > tailchrome-install.sh.sha256
test "$(wc -l < tailchrome-install.sh.sha256)" -eq 1
shasum -a 256 --check tailchrome-install.sh.sha256
gh attestation verify tailchrome-install.sh \
  --repo dantraynor/tailchrome
less tailchrome-install.sh
bash ./tailchrome-install.sh --version "$VERSION"
```

`gh attestation verify` is recommended when GitHub CLI is installed and
authenticated (`gh auth login`). Without it, the checksum still detects
corruption, but the script and checksum share the same GitHub Release trust
boundary.

The fallback installs the helper at:

```text
$HOME/.local/bin/tailchrome
```

It invokes the verified helper with `install --binary-path` at that final
location. On macOS this also installs or repairs the same per-user LaunchAgent.
The app, script, system package and Homebrew are separate installation
methods; choose one per account.

## Uninstall

For a system package, run this in each account before removing its payload:

```bash
helper="/Library/Application Support/Tailscale/BrowserExt/tailscale-browser-ext"
"$helper" uninstall --binary-path "$helper"
```

Then remove the system package payload and receipt:

```bash
sudo rm -f "/Library/Application Support/Tailscale/BrowserExt/tailscale-browser-ext"
sudo rmdir "/Library/Application Support/Tailscale/BrowserExt" 2>/dev/null || true
sudo rm -rf "/Applications/Tailchrome Helper.app"
sudo pkgutil --forget org.tesseras.tailchrome.helper
```

For the per-user app:

```bash
helper="$HOME/Applications/Tailchrome Helper.app/Contents/MacOS/tailscale-browser-ext"
"$helper" uninstall --binary-path "$helper"
```

Then move `~/Applications/Tailchrome Helper.app` to the Trash. For the script:

```bash
bash ./tailchrome-install.sh --uninstall
```

The helper's uninstall command unloads the LaunchAgent and removes its plist,
Unix socket, bridge token, daemon settings, and local-app proxy credential. It
does not remove the existing `tsnet` identity.

These new commands preserve other methods' registrations and node identities.
For v0.1.13 and older, use that release's `-uninstall` command before switching
methods, or rerun the new method's registration afterward. See
[helper installation](../../docs/helper-installation.md).
