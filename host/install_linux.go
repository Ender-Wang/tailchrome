package main

import (
	"io"
	"os"
	"path/filepath"
)

// chromiumBrowserTarget is one Chromium-family browser's native-messaging
// manifest target on this platform. On Linux only Name and Dir are used.
type chromiumBrowserTarget struct {
	Name string
	Dir  string
	Path string // unused on Linux
}

// chromiumManifestDirs returns the per-browser native-messaging manifest
// directories on Linux. Writing the same manifest JSON into every directory
// ensures the extension works in whichever Chromium-family browser the user
// installs it into.
func chromiumManifestDirs() []chromiumBrowserTarget {
	home, _ := os.UserHomeDir()
	configHome := os.Getenv("CHROME_CONFIG_HOME")
	if configHome == "" || !filepath.IsAbs(configHome) {
		configHome = os.Getenv("XDG_CONFIG_HOME")
	}
	if configHome == "" || !filepath.IsAbs(configHome) {
		configHome = filepath.Join(home, ".config")
	}
	return []chromiumBrowserTarget{
		{Name: "Chrome", Dir: filepath.Join(configHome, "google-chrome", "NativeMessagingHosts")},
		{Name: "Chrome Beta", Dir: filepath.Join(configHome, "google-chrome-beta", "NativeMessagingHosts")},
		{Name: "Chrome Dev", Dir: filepath.Join(configHome, "google-chrome-unstable", "NativeMessagingHosts")},
		{Name: "Chromium", Dir: filepath.Join(configHome, "chromium", "NativeMessagingHosts")},
		{Name: "Brave", Dir: filepath.Join(configHome, "BraveSoftware", "Brave-Browser", "NativeMessagingHosts")},
		{Name: "Edge", Dir: filepath.Join(configHome, "microsoft-edge", "NativeMessagingHosts")},
		{Name: "Vivaldi", Dir: filepath.Join(configHome, "vivaldi", "NativeMessagingHosts")},
		{Name: "Opera", Dir: filepath.Join(configHome, "opera", "NativeMessagingHosts")},
	}
}

func firefoxManifestDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mozilla", "native-messaging-hosts")
}

func binaryInstallDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "tailscale", "browser-ext")
}

// Linux has no registry cleanup beyond removing manifest files.
func platformUninstallChromium(_ chromiumBrowserTarget, _ string) error {
	return nil
}

func platformUninstallFirefox(_ string) error { return nil }

func snapshotPlatformChromium(_ chromiumBrowserTarget, _ string) (any, error) { return nil, nil }
func restorePlatformChromium(_ chromiumBrowserTarget, _ string, _ any) error  { return nil }
func snapshotPlatformFirefox(_ string) (any, error)                           { return nil, nil }
func restorePlatformFirefox(_ string, _ any) error                            { return nil }
func platformChromiumRegistryKeys(_ chromiumBrowserTarget) []string           { return nil }
func platformFirefoxRegistryKeys() []string                                   { return nil }

// platformPostInstallChromium is the per-browser hook used by the new
// installChromiumFamily loop. No-op on Linux.
func platformPostInstallChromium(_ string, _ string) error { return nil }

// browserHasFootprint reports whether there is evidence on this machine that
// the named browser has ever run — either its config dir exists (Linux/macOS)
// or its vendor registry key exists (Windows). Used to label install status.
func browserHasFootprint(target chromiumBrowserTarget) bool {
	info, err := os.Stat(filepath.Dir(target.Dir))
	return err == nil && info.IsDir()
}

func platformPostInstallFirefox(_ string) error { return nil }

// replaceBinary atomically activates a fully staged helper on this platform.
func replaceBinary(destPath string, src io.Reader, perm os.FileMode) error {
	return atomicReplaceBinary(destPath, src, perm)
}
