cask "tailchrome" do
  version "0.1.14"
  sha256 "ecef929fccc590e4607e0fa0e5c15712893fb3c2fbe9091d1f0ed85756809a9a"

  url "https://github.com/dantraynor/tailchrome/releases/download/v#{version}/tailchrome-helper-macos.pkg"
  name "Tailchrome Helper"
  desc "Native helper for accessing a Tailscale network from your browser"
  homepage "https://github.com/dantraynor/tailchrome"

  depends_on :macos

  pkg "tailchrome-helper-macos.pkg"

  uninstall script:  {
              executable:   "/bin/sh",
              args:         ["-c", "helper=\"$1\"; if \"$helper\" --help 2>&1 | grep -Fq -- '--binary-path'; then \"$helper\" uninstall --binary-path \"$helper\"; else \"$helper\" -uninstall; fi", "tailchrome-uninstall", "/Library/Application Support/Tailscale/BrowserExt/tailscale-browser-ext"],
              sudo:         false,
              must_succeed: false,
            },
            pkgutil: "org.tesseras.tailchrome.helper"

  caveats <<~EOS
    Install the Tailchrome browser extension from the Chrome Web Store or Firefox Add-ons.
    Restart your browser after installing or upgrading the helper.

    To register another macOS user or repair browser discovery, open:
      /Applications/Tailchrome Helper.app

    Before uninstalling, disconnect Tailchrome and close your browsers.
    Uninstall removes registrations owned by the package binary for the current user. Other users should run
    the following capability-compatible command in their account before the package is removed:
      helper="/Library/Application Support/Tailscale/BrowserExt/tailscale-browser-ext"; if "$helper" --help 2>&1 | grep -Fq -- '--binary-path'; then "$helper" uninstall --binary-path "$helper"; else "$helper" -uninstall; fi
    Tailscale identities and profile data are preserved.
  EOS
end
