//go:build !windows

package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestChromiumManifestDirsContainsExpectedTargets(t *testing.T) {
	home := setupTempHome(t)
	dirs := chromiumManifestDirs()
	if len(dirs) == 0 {
		t.Fatal("chromiumManifestDirs() returned empty slice")
	}

	var want []chromiumBrowserTarget
	switch runtime.GOOS {
	case "darwin":
		base := filepath.Join(home, "Library", "Application Support")
		want = []chromiumBrowserTarget{
			{Name: "Chrome", Dir: filepath.Join(base, "Google", "Chrome", "NativeMessagingHosts")},
			{Name: "Chrome Beta", Dir: filepath.Join(base, "Google", "Chrome Beta", "NativeMessagingHosts")},
			{Name: "Chrome Canary", Dir: filepath.Join(base, "Google", "Chrome Canary", "NativeMessagingHosts")},
			{Name: "Chrome Dev", Dir: filepath.Join(base, "Google", "Chrome Dev", "NativeMessagingHosts")},
			{Name: "Chromium", Dir: filepath.Join(base, "Chromium", "NativeMessagingHosts")},
			{Name: "Brave", Dir: filepath.Join(base, "BraveSoftware", "Brave-Browser", "NativeMessagingHosts")},
			{Name: "Edge", Dir: filepath.Join(base, "Microsoft Edge", "NativeMessagingHosts")},
			{Name: "Vivaldi", Dir: filepath.Join(base, "Vivaldi", "NativeMessagingHosts")},
			{Name: "Opera", Dir: filepath.Join(base, "com.operasoftware.Opera", "NativeMessagingHosts")},
			{Name: "Arc", Dir: filepath.Join(base, "Arc", "User Data", "NativeMessagingHosts")},
		}
	case "linux":
		cfg := filepath.Join(home, ".config")
		want = []chromiumBrowserTarget{
			{Name: "Chrome", Dir: filepath.Join(cfg, "google-chrome", "NativeMessagingHosts")},
			{Name: "Chrome Beta", Dir: filepath.Join(cfg, "google-chrome-beta", "NativeMessagingHosts")},
			{Name: "Chrome Dev", Dir: filepath.Join(cfg, "google-chrome-unstable", "NativeMessagingHosts")},
			{Name: "Chromium", Dir: filepath.Join(cfg, "chromium", "NativeMessagingHosts")},
			{Name: "Brave", Dir: filepath.Join(cfg, "BraveSoftware", "Brave-Browser", "NativeMessagingHosts")},
			{Name: "Edge", Dir: filepath.Join(cfg, "microsoft-edge", "NativeMessagingHosts")},
			{Name: "Vivaldi", Dir: filepath.Join(cfg, "vivaldi", "NativeMessagingHosts")},
			{Name: "Opera", Dir: filepath.Join(cfg, "opera", "NativeMessagingHosts")},
		}
	default:
		t.Skipf("no expected list for GOOS=%s", runtime.GOOS)
	}

	if !reflect.DeepEqual(dirs, want) {
		t.Errorf("chromiumManifestDirs() mismatch\n got: %#v\nwant: %#v", dirs, want)
	}
}

func TestChromiumManifestDirsAllUnderHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome-tailchrome-test")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CHROME_CONFIG_HOME", "")
	dirs := chromiumManifestDirs()
	for _, d := range dirs {
		if d.Dir == "" {
			t.Errorf("chromiumManifestDirs() entry %q has empty dir", d.Name)
			continue
		}
		if !strings.HasPrefix(d.Dir, "/tmp/fakehome-tailchrome-test") {
			t.Errorf("chromiumManifestDirs() entry %q dir %q is not under HOME", d.Name, d.Dir)
		}
	}
}

const testExtensionID = "test-extension-id-123"

func setupTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CHROME_CONFIG_HOME", "")
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", dir)
		t.Setenv("LOCALAPPDATA", filepath.Join(dir, "AppData", "Local"))
	}
	return dir
}

func TestInstallChromiumFamilyWritesManifestPerBrowser(t *testing.T) {
	home := setupTempHome(t)
	results, err := installChromiumFamily(testExtensionID)
	if err != nil {
		t.Fatalf("installChromiumFamily returned error: %v", err)
	}

	dirs := chromiumManifestDirs()
	if len(results) != len(dirs) {
		t.Errorf("expected %d results, got %d", len(dirs), len(results))
	}

	for _, d := range dirs {
		manifestPath := filepath.Join(d.Dir, manifestNameChrome+".json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Errorf("manifest for %s missing at %s: %v", d.Name, manifestPath, err)
			continue
		}
		var m nativeManifest
		if err := json.Unmarshal(data, &m); err != nil {
			t.Errorf("manifest for %s not valid JSON: %v", d.Name, err)
			continue
		}
		if m.Name != manifestNameChrome {
			t.Errorf("manifest for %s has wrong Name: %q", d.Name, m.Name)
		}
		if m.Type != "stdio" {
			t.Errorf("manifest for %s has wrong Type: %q", d.Name, m.Type)
		}
		expectedOrigin := "chrome-extension://" + testExtensionID + "/"
		if len(m.AllowedOrigins) != 1 || m.AllowedOrigins[0] != expectedOrigin {
			t.Errorf("manifest for %s has wrong AllowedOrigins: %v", d.Name, m.AllowedOrigins)
		}
		if !strings.HasPrefix(m.Path, home) {
			t.Errorf("manifest for %s has Path outside HOME: %q", d.Name, m.Path)
		}
	}

	for _, r := range results {
		if r.Err != nil {
			t.Errorf("result for %s has error: %v", r.Name, r.Err)
		}
		if r.ParentExisted {
			t.Errorf("result for %s reports ParentExisted=true on a fresh temp HOME", r.Name)
		}
	}
}

func TestUninstallRemovesAllChromiumManifests(t *testing.T) {
	setupTempHome(t)

	if _, err := installChromiumFamily(testExtensionID); err != nil {
		t.Fatalf("install failed: %v", err)
	}

	dirs := chromiumManifestDirs()
	for _, d := range dirs {
		manifestPath := filepath.Join(d.Dir, manifestNameChrome+".json")
		if _, err := os.Stat(manifestPath); err != nil {
			t.Fatalf("pre-uninstall: %s manifest missing at %s: %v", d.Name, manifestPath, err)
		}
	}

	if err := uninstall(); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}

	for _, d := range dirs {
		manifestPath := filepath.Join(d.Dir, manifestNameChrome+".json")
		if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
			t.Errorf("post-uninstall: %s manifest still exists at %s (err=%v)", d.Name, manifestPath, err)
		}
	}
}

func TestUninstallRemovesInstalledBinary(t *testing.T) {
	setupTempHome(t)

	binPath := installedBinaryPath()
	if err := os.MkdirAll(filepath.Dir(binPath), 0755); err != nil {
		t.Fatalf("failed to create install dir: %v", err)
	}
	if err := os.WriteFile(binPath, []byte("helper"), 0755); err != nil {
		t.Fatalf("failed to stage binary: %v", err)
	}

	if err := uninstall(); err != nil {
		t.Fatalf("uninstall failed: %v", err)
	}

	if _, err := os.Stat(binPath); !os.IsNotExist(err) {
		t.Errorf("post-uninstall: installed binary still exists at %s (err=%v)", binPath, err)
	}
}

func TestUninstallIsIdempotent(t *testing.T) {
	setupTempHome(t)
	if err := uninstall(); err != nil {
		t.Fatalf("first uninstall on empty home failed: %v", err)
	}
	if err := uninstall(); err != nil {
		t.Fatalf("second uninstall failed: %v", err)
	}
}

func TestReplaceBinaryCreatesWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "tailscale-browser-ext")
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
	info, err := os.Stat(dest)
	if err != nil {
		t.Fatalf("stat dest: %v", err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("dest is not owner-executable: mode=%v", info.Mode().Perm())
	}
}

func TestReplaceBinaryOverwritesAndTruncates(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "tailscale-browser-ext")
	// Seed with longer content so a non-truncating write would leave a tail.
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
		t.Errorf("dest content = %q, want %q (old content not fully replaced)", got, "NEW")
	}
}

func TestInstallChromiumFamilyParentExistedTrueWhenDirPresent(t *testing.T) {
	setupTempHome(t)
	dirs := chromiumManifestDirs()
	if len(dirs) == 0 {
		t.Skip("no chromium manifest dirs on this platform")
	}
	// Pre-create one browser's parent (config) dir so its result
	// reports ParentExisted=true.
	first := dirs[0]
	if err := os.MkdirAll(filepath.Dir(first.Dir), 0755); err != nil {
		t.Fatalf("pre-create dir: %v", err)
	}

	results, err := installChromiumFamily(testExtensionID)
	if err != nil {
		t.Fatalf("installChromiumFamily returned error: %v", err)
	}

	for _, r := range results {
		if r.Name == first.Name {
			if !r.ParentExisted {
				t.Errorf("expected ParentExisted=true for %s", r.Name)
			}
		} else {
			if r.ParentExisted {
				t.Errorf("expected ParentExisted=false for %s, got true", r.Name)
			}
		}
	}
}

func TestParseCommandSupportsHelpAndLegacyEqualsForms(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}, {"install", "--help"}} {
		cmd, err := parseCommand(args)
		if err != nil || cmd.Kind != commandHelp {
			t.Errorf("parseCommand(%q) = %#v, %v; want help", args, cmd, err)
		}
	}
	if cmd, err := parseCommand([]string{"--native-browser-argument", "value"}); err != nil || cmd.Kind != commandNative {
		t.Fatalf("native arguments parsed as %#v, %v; want commandNative", cmd, err)
	}
	for _, args := range [][]string{{"-install=C" + chromeWebStoreExtensionID}, {"--install=C" + chromeWebStoreExtensionID}, {"-version=true"}} {
		cmd, err := parseCommand(args)
		if err != nil {
			t.Errorf("parseCommand(%q) error: %v", args, err)
			continue
		}
		if args[0] == "-version=true" {
			if cmd.Kind != commandVersion {
				t.Errorf("parseCommand(%q) kind=%v, want version", args, cmd.Kind)
			}
		} else if cmd.Kind != commandLegacyInstall {
			t.Errorf("parseCommand(%q) kind=%v, want legacy install", args, cmd.Kind)
		}
	}
}

func TestValidateExtensionIDsAcceptsFirefoxAddonForms(t *testing.T) {
	valid := []string{"addon@example.org", "{12345678-1234-1234-1234-1234567890ab}"}
	for _, firefoxID := range valid {
		if err := validateExtensionIDs(chromeWebStoreExtensionID, firefoxID); err != nil {
			t.Errorf("Firefox ID %q rejected: %v", firefoxID, err)
		}
	}
	for _, firefoxID := range []string{"not-an-addon-id", "{not-a-uuid}"} {
		if err := validateExtensionIDs(chromeWebStoreExtensionID, firefoxID); err == nil {
			t.Errorf("Firefox ID %q accepted; want validation error", firefoxID)
		}
	}
}

func TestDirectManifestUsesOnlyNativeMessagingFields(t *testing.T) {
	setupTempHome(t)
	path := filepath.Join(t.TempDir(), "manifest.json")
	manifest := nativeManifest{
		Name: manifestNameChrome, Description: "description", Path: "/bin/helper", Type: "stdio",
		AllowedOrigins: []string{"chrome-extension://" + chromeWebStoreExtensionID + "/"},
	}
	if err := writeManifest(path, manifest); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"owner", "owner_kind"} {
		if _, ok := fields[field]; ok {
			t.Errorf("manifest contains private ownership field %q", field)
		}
	}
}

func TestWriteRegistrationReceiptFailureRestoresManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "host.json")
	old := []byte("{\n  \"name\": \"old\"\n}\n")
	if err := os.WriteFile(path, old, 0644); err != nil {
		t.Fatal(err)
	}
	original := atomicWriteFileForTest
	atomicWriteFileForTest = func(path string, data []byte, perm os.FileMode, label string) error {
		if label == "registration receipt" {
			return errors.New("injected receipt activation failure")
		}
		return original(path, data, perm, label)
	}
	defer func() { atomicWriteFileForTest = original }()

	err := writeRegistration(path, nativeManifest{Name: manifestNameChrome, Path: "/new/helper"}, "/new/helper", ownerKindDirect)
	if err == nil || !strings.Contains(err.Error(), "receipt activation failure") {
		t.Fatalf("writeRegistration error = %v, want injected receipt failure", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(old) {
		t.Fatalf("manifest after failed receipt = %q, want prior content %q", got, old)
	}
}

func TestLegacyUninstallPreservesDirectRegistration(t *testing.T) {
	setupTempHome(t)
	target := chromiumManifestDirs()[0]
	path := filepath.Join(target.Dir, manifestNameChrome+".json")
	owner := installedBinaryPath()
	manifest := nativeManifest{Name: manifestNameChrome, Path: owner, Type: "stdio"}
	if err := writeRegistration(path, manifest, owner, ownerKindDirect); err != nil {
		t.Fatal(err)
	}
	if err := uninstall(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("legacy uninstall removed direct manifest: %v", err)
	}
}

func TestDirectUninstallSupportsCustomUserDataDir(t *testing.T) {
	setupTempHome(t)
	custom := filepath.Join(t.TempDir(), "Chrome User Data")
	opts := registrationOptions{
		ChromeID:    chromeWebStoreExtensionID,
		FirefoxID:   firefoxExtensionID,
		Browsers:    []string{"Chrome"},
		UserDataDir: custom,
	}
	if _, err := installDirectRegistration(opts); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(custom, "NativeMessagingHosts", manifestNameChrome+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("custom manifest missing: %v", err)
	}
	owner, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := uninstallOwned(ownerKindDirect, owner, false, custom); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("custom manifest remains after uninstall: %v", err)
	}
}

func TestDirectRegistrationPreservesExplicitOptSymlinkAndDoesNotCopy(t *testing.T) {
	setupTempHome(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	optDir := t.TempDir()
	optPath := filepath.Join(optDir, "opt", "bin", "tailchrome")
	if err := os.MkdirAll(filepath.Dir(optPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, optPath); err != nil {
		t.Fatal(err)
	}
	opts := registrationOptions{
		BinaryPath: optPath,
		ChromeID:   chromeWebStoreExtensionID,
		FirefoxID:  firefoxExtensionID,
		Browsers:   []string{"Chrome"},
	}
	if _, err := installDirectRegistration(opts); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(chromiumManifestDirs()[0].Dir, manifestNameChrome+".json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest nativeManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Path != optPath {
		t.Fatalf("manifest path = %q, want explicit opt symlink %q", manifest.Path, optPath)
	}
	if _, err := os.Stat(installedBinaryPath()); !os.IsNotExist(err) {
		t.Fatalf("direct install copied a staged runtime: stat error=%v", err)
	}
}

func TestDefaultRegistrationDetectsOptionalBrowserFootprint(t *testing.T) {
	setupTempHome(t)
	targets := chromiumManifestDirs()
	var optional chromiumBrowserTarget
	for _, target := range targets {
		if normalizeBrowserName(target.Name) == "brave" {
			optional = target
			break
		}
	}
	if optional.Name == "" {
		t.Skip("Brave is not listed on this platform")
	}
	if err := os.MkdirAll(filepath.Dir(optional.Dir), 0755); err != nil {
		t.Fatal(err)
	}
	selected, firefox, err := selectedRegistrationTargets(registrationOptions{
		ChromeID: chromeWebStoreExtensionID, FirefoxID: firefoxExtensionID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !firefox {
		t.Fatal("default registration did not select Firefox")
	}
	seen := false
	for _, target := range selected {
		if target.Name == optional.Name {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("detected optional browser %q was not selected: %#v", optional.Name, selected)
	}
}

func TestChromiumManifestDirsHonorsXDGOverrides(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG Chromium config overrides are Linux-specific")
	}
	home := setupTempHome(t)
	xdg := filepath.Join(t.TempDir(), "xdg-config")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("CHROME_CONFIG_HOME", "")
	if got, want := chromiumManifestDirs()[0].Dir, filepath.Join(xdg, "google-chrome", "NativeMessagingHosts"); got != want {
		t.Fatalf("XDG Chrome manifest dir = %q, want %q", got, want)
	}
	chromeConfig := filepath.Join(t.TempDir(), "chrome-config")
	t.Setenv("CHROME_CONFIG_HOME", chromeConfig)
	if got, want := chromiumManifestDirs()[0].Dir, filepath.Join(chromeConfig, "google-chrome", "NativeMessagingHosts"); got != want {
		t.Fatalf("CHROME_CONFIG_HOME manifest dir = %q, want %q", got, want)
	}
	if got := home; got == "" {
		t.Fatal("setup temp HOME returned empty path")
	}
	t.Setenv("CHROME_CONFIG_HOME", "relative-chrome")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	if got, want := chromiumManifestDirs()[0].Dir, filepath.Join(xdg, "google-chrome", "NativeMessagingHosts"); got != want {
		t.Fatalf("relative CHROME_CONFIG_HOME did not fall back to absolute XDG path: got %q want %q", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative-xdg")
	if got, want := chromiumManifestDirs()[0].Dir, filepath.Join(home, ".config", "google-chrome", "NativeMessagingHosts"); got != want {
		t.Fatalf("relative config overrides did not fall back to default: got %q want %q", got, want)
	}
}

type failingShortReader struct {
	data []byte
}

func (r *failingShortReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.ErrUnexpectedEOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

func TestAtomicReplacementRetainsOldBinaryWhenReaderFails(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "tailchrome")
	old := []byte("old-working-binary")
	if err := os.WriteFile(dest, old, 0755); err != nil {
		t.Fatal(err)
	}
	if err := replaceBinary(dest, &failingShortReader{data: []byte("partial-new")}, 0755); err == nil {
		t.Fatal("replaceBinary unexpectedly succeeded with a failing short reader")
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(old) {
		t.Fatalf("binary after failed atomic replacement = %q, want %q", got, old)
	}
}

func TestDirectUninstallDoesNotRemoveAnotherOwner(t *testing.T) {
	setupTempHome(t)
	target := chromiumManifestDirs()[0]
	path := filepath.Join(target.Dir, manifestNameChrome+".json")
	ownerA := filepath.Join(t.TempDir(), "owner-a")
	ownerB := filepath.Join(t.TempDir(), "owner-b")
	if err := writeRegistration(path, nativeManifest{Name: manifestNameChrome, Path: ownerA}, ownerA, ownerKindDirect); err != nil {
		t.Fatal(err)
	}
	if err := uninstallOwned(ownerKindDirect, ownerB, false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("owner B uninstall removed owner A registration: %v", err)
	}
}

func TestDirectInstallRollsBackEveryTargetOnFailure(t *testing.T) {
	setupTempHome(t)
	target := chromiumManifestDirs()[0]
	manifestPath := filepath.Join(target.Dir, manifestNameChrome+".json")
	old := nativeManifest{Name: manifestNameChrome, Description: "old", Path: "/old/helper", Type: "stdio"}
	if err := writeRegistration(manifestPath, old, old.Path, ownerKindDirect); err != nil {
		t.Fatal(err)
	}
	originalFirefox := platformPostInstallFirefoxForInstall
	platformPostInstallFirefoxForInstall = func(string) error { return errors.New("injected registration failure") }
	defer func() { platformPostInstallFirefoxForInstall = originalFirefox }()

	results, err := installDirectRegistration(registrationOptions{
		ChromeID: chromeWebStoreExtensionID, FirefoxID: firefoxExtensionID,
		Browsers: []string{"Chrome", "Firefox"},
	})
	if err == nil || !strings.Contains(err.Error(), "injected registration failure") {
		t.Fatalf("installDirectRegistration error = %v, want injected failure", err)
	}
	rolledBack := false
	for _, result := range results {
		if result.Name == "Chrome" {
			rolledBack = result.RolledBack
		}
		if result.Err == nil && !result.RolledBack {
			t.Errorf("successful result %s was not marked rolled back", result.Name)
		}
	}
	if !rolledBack {
		t.Fatal("Chrome result was not marked rolled back")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var got nativeManifest
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Path != old.Path || got.Description != old.Description {
		t.Fatalf("prior Chrome registration not restored: %#v", got)
	}
}

func TestBrowserFootprintRequiresDirectory(t *testing.T) {
	setupTempHome(t)
	target := chromiumManifestDirs()[0]
	parent := filepath.Dir(target.Dir)
	if err := os.MkdirAll(filepath.Dir(parent), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(parent, []byte("not-a-directory"), 0644); err != nil {
		t.Fatal(err)
	}
	if browserHasFootprint(target) {
		t.Fatal("file browser footprint was treated as a directory")
	}
}
