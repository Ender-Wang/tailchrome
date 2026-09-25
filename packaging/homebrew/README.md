# Homebrew installation

This repository is a Homebrew tap. The macOS cask uses a signed, notarized
universal package. The formula builds from a checksummed source archive on
macOS and Linux, with Go as a build dependency. Both remain pinned to the
latest published release; development version bumps do not update the tap.

```bash
brew tap dantraynor/tailchrome https://github.com/dantraynor/tailchrome
```

Install the browser extension separately from the
[Chrome Web Store](https://chromewebstore.google.com/detail/tailchrome/bhfeceecialgilpedkoflminjgcjljll)
or [Firefox Add-ons](https://addons.mozilla.org/en-US/firefox/addon/tailchrome/).

## macOS cask

```bash
brew install --cask dantraynor/tailchrome/tailchrome
```

The package requires administrator access and supports Intel and Apple Silicon.
It installs `/Applications/Tailchrome Helper.app` and the helper at
`/Library/Application Support/Tailscale/BrowserExt/tailscale-browser-ext`.
Open that app in each account to register or repair discovery. Starting with
v0.1.14, registration points directly at the system package payload. Older
releases create a separate per-user runtime copy.

Registration also installs a per-user LaunchAgent that keeps each browser
profile's independent Tailchrome node available after the browser closes. On
macOS, the extension can use that daemon to enable a separate authenticated
HTTP(S)/SOCKS5 loopback proxy for local applications. Formula users receive the
same LaunchAgent when they run the v0.1.14-or-later registration command.

Disconnect Tailchrome and close browsers before upgrading:

```bash
brew update
brew upgrade --cask dantraynor/tailchrome/tailchrome
```

Reopen the helper app in other registered accounts after upgrading an older
release. To remove the package:

```bash
brew uninstall --cask dantraynor/tailchrome/tailchrome
```

The cask chooses the uninstall command supported by its pinned helper. Other
accounts should run the command printed by `brew info --cask tailchrome`
before the package is removed. Node identities are preserved.

## Source formula (macOS and Linux)

```bash
brew install --formula dantraynor/tailchrome/tailchrome
brew info --formula dantraynor/tailchrome/tailchrome
```

Run the registration command shown in the caveats without `sudo`. For v0.1.14
and later, this registers Homebrew's stable opt path:

```bash
helper="$(brew --prefix tailchrome)/bin/tailscale-browser-ext"
"$helper" install --binary-path "$helper"
```

The formula also provides the `tailchrome` command alias. Use the explicit
opt path for registration so normal `brew upgrade` and `brew cleanup` continue
to work without refreshing a separate runtime copy. The same registration
command repairs discovery.

The currently pinned v0.1.13 helper uses the legacy command instead:

```bash
tailscale-browser-ext -install-now
```

That older command copies a runtime to
`~/Library/Application Support/Tailscale/BrowserExt/` on macOS or
`~/.local/share/tailscale/browser-ext/` on Linux. Repeat it after upgrading an
older formula. Follow the installed formula's caveats when the tap is updated.

To remove a v0.1.14 or later formula, unregister in each account first:

```bash
helper="$(brew --prefix tailchrome)/bin/tailscale-browser-ext"
"$helper" uninstall --binary-path "$helper"
brew uninstall --formula dantraynor/tailchrome/tailchrome
```

For older helpers, use `tailscale-browser-ext -uninstall` before `brew uninstall`.
Older uninstallers do not understand ownership receipts: remove old packages
before switching installation methods, or rerun the new method's registration
afterward. Native node identities are preserved. See
[helper installation](../../docs/helper-installation.md).

Homebrew in WSL does not install a Windows helper. Use the
[Windows installer](../windows/README.md) for browsers running on Windows.

## Maintaining the tap

`Casks/tailchrome.rb` and `Formula/tailchrome.rb` track the latest **published**
stable helper release. Do not bump them with `scripts/bump-version.sh`: the
cask's final checksum is available only after signing, notarization, and packaging.

After protected helper publication succeeds, `Publish Helper Release` calls
`Update Homebrew`. That workflow verifies the public release and the approved
`SHA256SUMS.txt` digest for the cask. It also downloads the release tag's source
archive, checks its declared version, and computes the formula's SHA-256.
GitHub's source archive is separate from the signed package manifest; review
its tag and checksum independently. The workflow updates both definitions,
tests the updater, and opens a pull request against `main`. It uses this
repository's `GITHUB_TOKEN`; no separate tap repository or PAT is needed.
Merge the update PR to make the new version
available through `brew update`.

For automatic PR creation, enable **Settings → Actions → General → Workflow
permissions → Allow GitHub Actions to create and approve pull requests**. This
workflow only creates PRs; it does not approve or merge them. Approve any pending
CI workflow runs on the automated PR before merging; see GitHub's
[GITHUB_TOKEN workflow rules](https://docs.github.com/en/actions/concepts/security/github_token).
If your GitHub deployment does not create runs for these PRs, close and reopen
the PR as a maintainer to trigger CI. The workflow also saves a
`homebrew-update-vX.Y.Z` patch artifact so updates remain available when PR
creation is disabled or branch protection prevents the push. A PR creation
failure produces a warning with this fallback and does not fail the helper
publication job.

If an update fails, the helper release stays published. Re-run `Update Homebrew`
from `main` with the same `release_tag` and `sha256sums_digest`. The workflow
rejects drafts and prereleases; the updater rejects malformed/missing/duplicate
checksums and version downgrades. Existing update PRs are left for review.

For a manual update, replace `vX.Y.Z` with the published release tag:

```bash
mkdir -p .context/homebrew-update
gh release download vX.Y.Z --repo dantraynor/tailchrome \
  --pattern SHA256SUMS.txt --dir .context/homebrew-update
# Compare this digest with the approved publication summary before continuing.
shasum -a 256 .context/homebrew-update/SHA256SUMS.txt
curl --fail --location --proto '=https' --tlsv1.2 \
  https://github.com/dantraynor/tailchrome/archive/refs/tags/vX.Y.Z.tar.gz \
  --output .context/homebrew-update/vX.Y.Z.tar.gz
shasum -a 256 .context/homebrew-update/vX.Y.Z.tar.gz
node scripts/update-homebrew.mjs vX.Y.Z .context/homebrew-update/SHA256SUMS.txt \
  .context/homebrew-update/vX.Y.Z.tar.gz
pnpm test:homebrew
git diff --check
git diff -- Casks/tailchrome.rb Formula/tailchrome.rb
```

Commit the reviewed definition updates through a pull request. CI builds the
formula from source on macOS and Linux and tests registration and removal in
Homebrew's temporary test home. It also checks cask syntax/style on macOS and
fetches the package to verify its checksum, signature, and stapled notarization
ticket. Full macOS cask install/upgrade/uninstall still needs a Mac with a
logged-in user; follow the commands above and check browser discovery after
each step.

## Preparing a Homebrew core submission

`Formula/tailchrome.rb` builds from source on both supported platforms and can
be proposed to [Homebrew/homebrew-core](https://github.com/Homebrew/homebrew-core)
as `Formula/t/tailchrome.rb`. Follow Homebrew's
[new formula checklist](https://docs.brew.sh/Adding-Software-to-Homebrew) and
[acceptance requirements](https://docs.brew.sh/Acceptable-Formulae). In a local
checkout of that tap, validate the formula before opening the upstream PR:

```bash
brew style --formula homebrew/core/tailchrome
brew audit --new --strict --online --formula homebrew/core/tailchrome
HOMEBREW_NO_INSTALL_FROM_API=1 brew install --build-from-source --formula homebrew/core/tailchrome
brew test homebrew/core/tailchrome
```

Homebrew reviews eligibility and generates bottles through its own CI. Core
acceptance is separate from this project's tap; the release workflow above
updates only this repository. Once accepted, submit later core updates through
Homebrew's contribution process as well.
