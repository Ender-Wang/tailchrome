package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// chromiumBrowserTarget is one Chromium-family browser's native-messaging
// manifest target on Windows. Dir is the shared on-disk JSON directory; Path
// is the per-browser HKCU registry key path that points at the JSON.
type chromiumBrowserTarget struct {
	Name string
	Dir  string
	Path string
}

// chromiumManifestDirs returns the per-browser native-messaging manifest
// targets on Windows. Every target shares the same on-disk Dir; only the
// registry Path differs. Multiple registry keys safely point at the same
// JSON file.
func chromiumManifestDirs() []chromiumBrowserTarget {
	jsonDir := chromiumJSONDir()
	return []chromiumBrowserTarget{
		// Chrome Stable, Beta, Dev, and Canary use the Chrome native messaging
		// registry root; Canary's SxS suffix is for install/user-data paths.
		{Name: "Chrome", Dir: jsonDir, Path: `Software\Google\Chrome\NativeMessagingHosts\` + manifestNameChrome},
		{Name: "Chromium", Dir: jsonDir, Path: `Software\Chromium\NativeMessagingHosts\` + manifestNameChrome},
		{Name: "Brave", Dir: jsonDir, Path: `Software\BraveSoftware\Brave-Browser\NativeMessagingHosts\` + manifestNameChrome},
		{Name: "Edge", Dir: jsonDir, Path: `Software\Microsoft\Edge\NativeMessagingHosts\` + manifestNameChrome},
		{Name: "Vivaldi", Dir: jsonDir, Path: `Software\Vivaldi\NativeMessagingHosts\` + manifestNameChrome},
		{Name: "Opera", Dir: jsonDir, Path: `Software\Opera Software\Opera Stable\NativeMessagingHosts\` + manifestNameChrome},
	}
}

func chromiumJSONDir() string {
	appData := os.Getenv("LOCALAPPDATA")
	return filepath.Join(appData, "Tailscale", "BrowserExt")
}

func firefoxManifestDir() string {
	appData := os.Getenv("LOCALAPPDATA")
	return filepath.Join(appData, "Tailscale", "BrowserExt")
}

func binaryInstallDir() string {
	appData := os.Getenv("LOCALAPPDATA")
	return filepath.Join(appData, "Tailscale", "BrowserExt")
}

// platformPostInstallChromium creates the HKCU registry key for the named
// Chromium-family browser pointing at the manifest JSON.
func platformPostInstallChromium(name, manifestPath string) error {
	for _, target := range chromiumManifestDirs() {
		if target.Name != name {
			continue
		}
		return createRegistryKey(registry.CURRENT_USER, target.Path, manifestPath)
	}
	return fmt.Errorf("no Windows registry path defined for browser %q", name)
}

// browserHasFootprint reports whether there is evidence on this machine that
// the named browser has ever run — either its config dir exists (Linux/macOS)
// or its vendor registry key exists (Windows). Used to label install status.
func browserHasFootprint(target chromiumBrowserTarget) bool {
	// target.Path is like `Software\Google\Chrome\NativeMessagingHosts\<name>`.
	// The vendor key is the path up to (but excluding) `NativeMessagingHosts`.
	idx := strings.LastIndex(target.Path, `\NativeMessagingHosts\`)
	if idx < 0 {
		return false
	}
	vendorKey := target.Path[:idx]
	for _, root := range []registry.Key{registry.CURRENT_USER, registry.LOCAL_MACHINE} {
		k, err := registry.OpenKey(root, vendorKey, registry.QUERY_VALUE)
		if err == nil {
			_ = k.Close()
			return true
		}
	}
	// Some enterprise installs expose only an App Paths entry. Treat that as
	// installation evidence while continuing to write registrations in HKCU.
	appPath := map[string]string{
		"Chrome":   `Software\Microsoft\Windows\CurrentVersion\App Paths\chrome.exe`,
		"Chromium": `Software\Microsoft\Windows\CurrentVersion\App Paths\chromium.exe`,
		"Brave":    `Software\Microsoft\Windows\CurrentVersion\App Paths\brave.exe`,
		"Edge":     `Software\Microsoft\Windows\CurrentVersion\App Paths\msedge.exe`,
		"Vivaldi":  `Software\Microsoft\Windows\CurrentVersion\App Paths\vivaldi.exe`,
		"Opera":    `Software\Microsoft\Windows\CurrentVersion\App Paths\opera.exe`,
	}
	if path, ok := appPath[target.Name]; ok {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE)
		if err == nil {
			_ = k.Close()
			return true
		}
	}
	return false
}

// platformPostInstallFirefox creates the Windows registry key for Firefox after
// the manifest file has been written.
func platformPostInstallFirefox(manifestPath string) error {
	return createRegistryKey(
		registry.CURRENT_USER,
		`Software\Mozilla\NativeMessagingHosts\`+manifestNameFirefox,
		manifestPath,
	)
}

type windowsRegistrySnapshot struct {
	keyExists   bool
	valueExists bool
	value       string
}

var snapshotRegistryValueForInstall = snapshotRegistryValue
var deleteRegistryKeyForInstall = func(baseKey registry.Key, path string) error {
	return registry.DeleteKey(baseKey, path)
}
var setRegistryValueForInstall = func(baseKey registry.Key, path, value string) error {
	key, _, err := registry.CreateKey(baseKey, path, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.SetStringValue("", value)
}
var deleteRegistryValueForInstall = func(baseKey registry.Key, path string) error {
	key, err := registry.OpenKey(baseKey, path, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	return key.DeleteValue("")
}

func platformChromiumRegistryKeys(target chromiumBrowserTarget) []string {
	return []string{target.Path}
}

func platformFirefoxRegistryKeys() []string {
	return []string{`Software\Mozilla\NativeMessagingHosts\` + manifestNameFirefox}
}

func snapshotPlatformChromium(target chromiumBrowserTarget, _ string) (any, error) {
	return snapshotRegistryValueForInstall(registry.CURRENT_USER, target.Path)
}

func snapshotPlatformFirefox(_ string) (any, error) {
	return snapshotRegistryValueForInstall(registry.CURRENT_USER, `Software\Mozilla\NativeMessagingHosts\`+manifestNameFirefox)
}

func restorePlatformChromium(target chromiumBrowserTarget, expected string, snapshot any) error {
	return restoreRegistryValue(registry.CURRENT_USER, target.Path, expected, snapshot)
}

func restorePlatformFirefox(expected string, snapshot any) error {
	return restoreRegistryValue(registry.CURRENT_USER, `Software\Mozilla\NativeMessagingHosts\`+manifestNameFirefox, expected, snapshot)
}

func snapshotRegistryValue(baseKey registry.Key, path string) (windowsRegistrySnapshot, error) {
	key, err := registry.OpenKey(baseKey, path, registry.QUERY_VALUE)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return windowsRegistrySnapshot{}, nil
		}
		return windowsRegistrySnapshot{}, fmt.Errorf("open registry key %s: %w", path, err)
	}
	defer key.Close()
	value, _, err := key.GetStringValue("")
	if errors.Is(err, registry.ErrNotExist) {
		return windowsRegistrySnapshot{keyExists: true}, nil
	}
	if err != nil {
		return windowsRegistrySnapshot{}, fmt.Errorf("read registry value %s: %w", path, err)
	}
	return windowsRegistrySnapshot{keyExists: true, valueExists: true, value: value}, nil
}

func platformUninstallChromium(target chromiumBrowserTarget, expected string) error {
	return removeRegistryValueIfExpected(registry.CURRENT_USER, target.Path, expected)
}

func platformUninstallFirefox(expected string) error {
	return removeRegistryValueIfExpected(registry.CURRENT_USER, `Software\Mozilla\NativeMessagingHosts\`+manifestNameFirefox, expected)
}

func restoreRegistryValue(baseKey registry.Key, path, expected string, snapshot any) error {
	want, ok := snapshot.(windowsRegistrySnapshot)
	if !ok {
		return fmt.Errorf("invalid registry snapshot for %s", path)
	}
	current, err := snapshotRegistryValueForInstall(baseKey, path)
	if err != nil {
		return err
	}
	if !current.keyExists || !current.valueExists || current.value != expected {
		// Another installer replaced the value during rollback. Preserve it.
		return nil
	}
	if want.keyExists {
		if want.valueExists {
			if err := setRegistryValueForInstall(baseKey, path, want.value); err != nil {
				return fmt.Errorf("restore registry value %s: %w", path, err)
			}
		} else if err := deleteRegistryValueForInstall(baseKey, path); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("restore absent registry value %s: %w", path, err)
		}
		return nil
	}
	return removeRegistryValueIfExpected(baseKey, path, expected)
}

func removeRegistryValueIfExpected(baseKey registry.Key, path, expected string) error {
	snapshot, err := snapshotRegistryValueForInstall(baseKey, path)
	if err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read registry value %s before removal: %w", path, err)
	}
	if !snapshot.keyExists || !snapshot.valueExists {
		// Missing or foreign/replacement values are not ours to remove.
		return nil
	}
	if snapshot.value != expected {
		// A foreign/replacement value is not ours to remove.
		return nil
	}
	if err := deleteRegistryKeyForInstall(baseKey, path); err != nil {
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("delete registry key %s: %w", path, err)
	}
	return nil
}

// createRegistryKey creates a Windows registry key pointing to the manifest JSON file.
func createRegistryKey(baseKey registry.Key, path, manifestPath string) error {
	key, _, err := registry.CreateKey(baseKey, path, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("failed to create registry key %s: %w", path, err)
	}
	defer key.Close()

	if err := key.SetStringValue("", manifestPath); err != nil {
		return fmt.Errorf("failed to set registry value: %w", err)
	}
	return nil
}

// replaceBinary stages and flushes a complete helper before activating it. A
// mapped destination is refused; it is never truncated or renamed aside while
// a browser can still execute it.
func replaceBinary(destPath string, src io.Reader, perm os.FileMode) error {
	stage, err := stageWindowsBinary(destPath, src, perm)
	if err != nil {
		return err
	}
	defer os.Remove(stage)
	p, err := windows.UTF16PtrFromString(destPath)
	if err != nil {
		return err
	}
	// Hold a write-denying handle through activation. A mapped image cannot be
	// opened with this share mode, and a late opener cannot race the replace.
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if isWindowsInUseError(err) {
			return fmt.Errorf("destination executable is in use; close browsers using Tailchrome and retry: %w", err)
		}
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return activateWindowsNewBinary(destPath, stage)
		}
		return fmt.Errorf("open destination executable: %w", err)
	}
	defer windows.CloseHandle(h)
	stagePtr, err := windows.UTF16PtrFromString(stage)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(stagePtr, p, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("activate replacement executable: %w", err)
	}
	return nil
}

func isWindowsInUseError(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

func stageWindowsBinary(destPath string, src io.Reader, perm os.FileMode) (string, error) {
	tmp, err := os.CreateTemp(filepath.Dir(destPath), "."+filepath.Base(destPath)+".tmp-")
	if err != nil {
		return "", fmt.Errorf("failed to create staged binary: %w", err)
	}
	name := tmp.Name()
	cleanup := func() { _ = tmp.Close(); _ = os.Remove(name) }
	if err := tmp.Chmod(perm); err != nil {
		cleanup()
		return "", fmt.Errorf("failed to set staged binary permissions: %w", err)
	}
	if _, err := io.Copy(tmp, src); err != nil {
		cleanup()
		return "", fmt.Errorf("failed to stage binary: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return "", fmt.Errorf("failed to flush staged binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("failed to close staged binary: %w", err)
	}
	return name, nil
}

func activateWindowsNewBinary(destPath, stage string) error {
	destPtr, err := windows.UTF16PtrFromString(destPath)
	if err != nil {
		return err
	}
	stagePtr, err := windows.UTF16PtrFromString(stage)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(stagePtr, destPtr, windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("activate new executable: %w", err)
	}
	return nil
}
