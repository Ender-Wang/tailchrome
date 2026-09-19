package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// This file is compiled only on Windows (the _windows suffix is a GOOS build
// constraint), so the Linux/macOS CI run skips it. It is the only place the
// core issue #84 fix -- updating a host binary while it is running -- is
// verified end-to-end, because the running-executable lock cannot be
// reproduced on other platforms.

func TestReplaceBinaryOverwritesUnlocked(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "tailscale-browser-ext.exe")
	if err := os.WriteFile(dest, []byte("OLDOLDOLD"), 0o755); err != nil {
		t.Fatalf("seed dest: %v", err)
	}
	if err := replaceBinary(dest, strings.NewReader("NEW"), 0o755); err != nil {
		t.Fatalf("replaceBinary returned error: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "NEW" {
		t.Errorf("dest content = %q, want %q", got, "NEW")
	}
	// When nothing is locked, the fast path writes in place and leaves no
	// ".old" sibling.
	if _, err := os.Stat(dest + ".old"); !os.IsNotExist(err) {
		t.Errorf("unlocked overwrite should not leave a .old file (stat err=%v)", err)
	}
}

type windowsFailingReader struct {
	data []byte
}

func (r *windowsFailingReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, errors.New("short reader failure")
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

func TestReplaceBinaryRetainsOldBytesWhenStagingFails(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "tailscale-browser-ext.exe")
	old := []byte("OLD-BINARY")
	if err := os.WriteFile(dest, old, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(dest, &windowsFailingReader{data: []byte("partial")}, 0o755); err == nil {
		t.Fatal("replaceBinary unexpectedly succeeded")
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(old) {
		t.Fatalf("destination after staging failure = %q, want %q", got, old)
	}
}

func TestUninstallSharedManifestRemovesEveryOwnedRegistryRegistration(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	targets := chromiumManifestDirs()
	if len(targets) < 2 {
		t.Skip("requires multiple Chromium targets")
	}
	for _, target := range targets[1:] {
		if target.Dir != targets[0].Dir {
			t.Fatalf("test requires shared Chromium JSON directory: %q != %q", target.Dir, targets[0].Dir)
		}
	}
	owner := `C:\Tailchrome\owner.exe`
	manifestPath := filepath.Join(targets[0].Dir, manifestNameChrome+".json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeRegistration(manifestPath, nativeManifest{Name: manifestNameChrome, Path: owner}, owner, ownerKindDirect); err != nil {
		t.Fatal(err)
	}
	registryValues := make(map[string]string, len(targets))
	for _, target := range targets {
		registryValues[target.Path] = manifestPath
	}
	foreignTarget := targets[len(targets)-1]
	registryValues[foreignTarget.Path] = `C:\OtherInstaller\replacement.json`
	var removed []string
	originalSnapshot := snapshotRegistryValueForInstall
	originalDelete := deleteRegistryKeyForInstall
	originalPlatform := platformUninstallChromiumForInstall
	snapshotRegistryValueForInstall = func(_ registry.Key, path string) (windowsRegistrySnapshot, error) {
		value, ok := registryValues[path]
		if !ok {
			return windowsRegistrySnapshot{}, nil
		}
		return windowsRegistrySnapshot{keyExists: true, valueExists: true, value: value}, nil
	}
	deleteRegistryKeyForInstall = func(_ registry.Key, path string) error {
		if _, ok := registryValues[path]; ok {
			delete(registryValues, path)
			for _, target := range targets {
				if target.Path == path {
					removed = append(removed, target.Name)
					break
				}
			}
		}
		return nil
	}
	platformUninstallChromiumForInstall = platformUninstallChromium
	defer func() {
		snapshotRegistryValueForInstall = originalSnapshot
		deleteRegistryKeyForInstall = originalDelete
		platformUninstallChromiumForInstall = originalPlatform
	}()
	if err := uninstallOwned(ownerKindDirect, owner, false, ""); err != nil {
		t.Fatal(err)
	}
	if len(removed) != len(targets)-1 {
		t.Fatalf("removed registry targets = %v, want %d owned keys", removed, len(targets)-1)
	}
	for _, target := range targets[:len(targets)-1] {
		if _, ok := registryValues[target.Path]; ok {
			t.Errorf("owned registry key %s survived uninstall", target.Name)
		}
	}
	if got := registryValues[foreignTarget.Path]; got == "" {
		t.Fatal("foreign registry default value was removed")
	}
	if got := registryValues[foreignTarget.Path]; got != `C:\OtherInstaller\replacement.json` {
		t.Fatalf("foreign registry default = %q, want replacement", got)
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("shared manifest remains after uninstall: %v", err)
	}
	if _, err := os.Stat(registrationReceiptPath(manifestPath)); !os.IsNotExist(err) {
		t.Fatalf("shared registration receipt remains after uninstall: %v", err)
	}
}

func TestUninstallRegistryFailureKeepsLegacyBinary(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	targets := chromiumManifestDirs()
	owner := installedBinaryPath()
	manifestPath := filepath.Join(targets[0].Dir, manifestNameChrome+".json")
	if err := os.MkdirAll(filepath.Dir(manifestPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeRegistration(manifestPath, nativeManifest{Name: manifestNameChrome, Path: owner}, owner, ownerKindLegacy); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(owner, []byte("legacy-runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := platformUninstallChromiumForInstall
	failure := errors.New("injected registry delete failure")
	platformUninstallChromiumForInstall = func(target chromiumBrowserTarget, _ string) error {
		if target.Name == targets[1].Name {
			return failure
		}
		return nil
	}
	defer func() { platformUninstallChromiumForInstall = original }()
	if err := uninstallOwned(ownerKindLegacy, owner, true, ""); !errors.Is(err, failure) {
		t.Fatalf("uninstall error = %v, want %v", err, failure)
	}
	if _, err := os.Stat(owner); err != nil {
		t.Fatalf("legacy binary removed after registry failure: %v", err)
	}
}

func TestUninstallMissingSharedManifestDoesNotTouchRegistry(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	called := 0
	original := platformUninstallChromiumForInstall
	platformUninstallChromiumForInstall = func(chromiumBrowserTarget, string) error { called++; return nil }
	defer func() { platformUninstallChromiumForInstall = original }()
	if err := uninstallOwned(ownerKindDirect, `C:\Tailchrome\missing.exe`, false, ""); err != nil {
		t.Fatal(err)
	}
	if called != 0 {
		t.Fatalf("missing manifest triggered %d registry removals", called)
	}
}

func TestRemoveRegistryValueIfExpectedPreservesForeignAndReportsReadErrors(t *testing.T) {
	originalSnapshot := snapshotRegistryValueForInstall
	originalDelete := deleteRegistryKeyForInstall
	originalSet := setRegistryValueForInstall
	originalDeleteValue := deleteRegistryValueForInstall
	defer func() {
		snapshotRegistryValueForInstall = originalSnapshot
		deleteRegistryKeyForInstall = originalDelete
		setRegistryValueForInstall = originalSet
		deleteRegistryValueForInstall = originalDeleteValue
	}()
	deletes := 0
	deleteRegistryKeyForInstall = func(registry.Key, string) error { deletes++; return nil }
	snapshotRegistryValueForInstall = func(registry.Key, string) (windowsRegistrySnapshot, error) {
		return windowsRegistrySnapshot{keyExists: true, valueExists: true, value: "foreign"}, nil
	}
	if err := removeRegistryValueIfExpected(registry.CURRENT_USER, "test", "ours"); err != nil {
		t.Fatal(err)
	}
	if deletes != 0 {
		t.Fatal("foreign registry value was deleted")
	}
	readErr := errors.New("registry read failed")
	snapshotRegistryValueForInstall = func(registry.Key, string) (windowsRegistrySnapshot, error) { return windowsRegistrySnapshot{}, readErr }
	if err := removeRegistryValueIfExpected(registry.CURRENT_USER, "test", "ours"); !errors.Is(err, readErr) {
		t.Fatalf("registry read error = %v, want %v", err, readErr)
	}
}

func TestRestoreRegistryPreservesAbsentDefaultValue(t *testing.T) {
	originalSnapshot := snapshotRegistryValueForInstall
	originalDeleteValue := deleteRegistryValueForInstall
	originalSet := setRegistryValueForInstall
	defer func() {
		snapshotRegistryValueForInstall = originalSnapshot
		deleteRegistryValueForInstall = originalDeleteValue
		setRegistryValueForInstall = originalSet
	}()
	snapshotRegistryValueForInstall = func(registry.Key, string) (windowsRegistrySnapshot, error) {
		return windowsRegistrySnapshot{keyExists: true, valueExists: true, value: "new"}, nil
	}
	deleted := false
	set := false
	deleteRegistryValueForInstall = func(registry.Key, string) error { deleted = true; return nil }
	setRegistryValueForInstall = func(registry.Key, string, string) error { set = true; return nil }
	if err := restoreRegistryValue(registry.CURRENT_USER, "test", "new", windowsRegistrySnapshot{keyExists: true, valueExists: false}); err != nil {
		t.Fatal(err)
	}
	if !deleted || set {
		t.Fatalf("restore absent default: deleted=%v set=%v, want delete without set", deleted, set)
	}
}

// TestReplaceBinaryRefusesLocked simulates a running executable: the
// destination is held open with a share mode that denies writes but permits
// rename/delete -- exactly how the Windows image loader holds a running .exe.
// A plain overwrite fails with ERROR_SHARING_VIOLATION; replaceBinary must
// must refuse to replace the mapped destination and leave the old binary intact.
func TestReplaceBinaryRefusesLocked(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "tailscale-browser-ext.exe")
	if err := os.WriteFile(dest, []byte("OLD"), 0o755); err != nil {
		t.Fatalf("seed dest: %v", err)
	}

	closeLock := lockFileLikeRunningExe(t, dest)
	defer closeLock()

	err := replaceBinary(dest, strings.NewReader("NEWBINARY"), 0o755)
	if err == nil || !strings.Contains(err.Error(), "close browsers") {
		t.Fatalf("replaceBinary under lock error = %v, want actionable in-use error", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "OLD" {
		t.Errorf("dest content = %q, want old binary to remain intact", got)
	}
}

// lockFileLikeRunningExe opens path with GENERIC_READ and a share mode of
// READ|DELETE (no WRITE), mirroring the restriction the loader places on a
// running executable: others may read or rename/delete it, but may not open it
// for writing. Returns a closer for the handle.
func lockFileLikeRunningExe(t *testing.T, path string) func() {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("UTF16PtrFromString: %v", err)
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("CreateFile (simulate running-exe lock): %v", err)
	}
	return func() { _ = windows.CloseHandle(h) }
}
