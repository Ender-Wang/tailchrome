package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

// manifestName is the name used for the native messaging host registration.
const manifestNameChrome = "com.tailscale.browserext.chrome"
const manifestNameFirefox = "com.tailscale.browserext.firefox"

// chromeWebStoreExtensionID is the stable extension ID assigned by the Chrome Web Store.
const chromeWebStoreExtensionID = "bhfeceecialgilpedkoflminjgcjljll"

// firefoxExtensionID is the gecko addon ID for the Firefox extension.
const firefoxExtensionID = "tailchrome@tesseras.org"

// nativeManifest is the native messaging host manifest format shared by
// Chromium-family browsers and Firefox.
type nativeManifest struct {
	Name              string   `json:"name"`
	Description       string   `json:"description"`
	Path              string   `json:"path"`
	Type              string   `json:"type"`
	AllowedOrigins    []string `json:"allowed_origins,omitempty"`    // Chromium-family
	AllowedExtensions []string `json:"allowed_extensions,omitempty"` // Firefox
}

// BrowserInstallResult captures per-browser status from installChromiumFamily.
type BrowserInstallResult struct {
	Name          string
	ParentExisted bool
	ManifestPath  string
	RegistryKeys  []string
	RolledBack    bool
	Err           error
}

type registrationSnapshot struct {
	target       chromiumBrowserTarget
	manifestPath string
	manifest     optionalFile
	receipt      optionalFile
	platform     any
	firefox      bool
}

// install parses the install argument and installs the native messaging host manifest.
// The argument format is "C<extensionID>" for the Chromium family or
// "F<extensionID>" for Firefox.
func install(arg string) error {
	if len(arg) < 2 {
		return fmt.Errorf("install argument must be C<extensionID> or F<extensionID>")
	}

	browserType := arg[0]
	extensionID := arg[1:]

	switch browserType {
	case 'C', 'c':
		_, err := installChromiumFamily(extensionID)
		return err
	case 'F', 'f':
		return installFirefox(extensionID)
	default:
		return fmt.Errorf("unknown browser type %q; use C for Chromium-family or F for Firefox", string(browserType))
	}
}

// installChromiumFamily writes the native messaging manifest into every
// supported Chromium-family browser's directory on this platform and reports
// per-browser status. Returns a non-nil error only when every browser failed.
func installChromiumFamily(extensionID string) ([]BrowserInstallResult, error) {
	binPath, err := installBinary()
	if err != nil {
		return nil, err
	}

	manifest := nativeManifest{
		Name:        manifestNameChrome,
		Description: "Tailscale Browser Extension Native Messaging Host",
		Path:        binPath,
		Type:        "stdio",
		AllowedOrigins: []string{
			fmt.Sprintf("chrome-extension://%s/", extensionID),
		},
	}

	dirs := chromiumManifestDirs()

	// Pre-pass: snapshot which browsers have a pre-existing footprint, before
	// we mutate anything. This makes "ParentExisted" mean "did this look
	// installed at the moment we started?" on every platform — important on
	// Windows where browsers share a common manifest JSON directory, so
	// stat'ing inside the loop would report a misleading footprint for
	// browsers iterated after the first.
	parentExisted := make([]bool, len(dirs))
	for i := range dirs {
		parentExisted[i] = browserHasFootprint(dirs[i])
	}

	results := make([]BrowserInstallResult, 0, len(dirs))
	successes := 0
	for i, target := range dirs {
		manifestPath := filepath.Join(target.Dir, manifestNameChrome+".json")
		r := BrowserInstallResult{Name: target.Name, ParentExisted: parentExisted[i], ManifestPath: manifestPath}
		if err := installOneChromium(target, manifest); err != nil {
			r.Err = err
		} else {
			r.RegistryKeys = platformChromiumRegistryKeys(target)
			successes++
		}
		results = append(results, r)
	}

	if successes == 0 {
		errs := make([]error, 0, len(results))
		for _, r := range results {
			if r.Err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", r.Name, r.Err))
			}
		}
		return results, fmt.Errorf("no Chromium-family browser manifests installed: %w", errors.Join(errs...))
	}
	return results, nil
}

// installOneChromium writes the manifest JSON into target.Dir and runs the
// per-browser platform post-install hook. Returns nil on success.
func installOneChromium(target chromiumBrowserTarget, manifest nativeManifest) error {
	return installOneChromiumOwned(target, manifest, ownerKindLegacy)
}

func installOneChromiumOwned(target chromiumBrowserTarget, manifest nativeManifest, kind string) error {
	if err := os.MkdirAll(target.Dir, 0755); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}
	manifestPath := filepath.Join(target.Dir, manifestNameChrome+".json")
	oldManifest, err := readOptionalFile(manifestPath)
	if err != nil {
		return err
	}
	oldReceipt, err := readOptionalFile(registrationReceiptPath(manifestPath))
	if err != nil {
		return err
	}
	if err := writeRegistration(manifestPath, manifest, manifest.Path, kind); err != nil {
		return err
	}
	if err := platformPostInstallChromiumForInstall(target.Name, manifestPath); err != nil {
		if rollbackErr := restoreRegistration(manifestPath, oldManifest, oldReceipt); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return nil
}

func snapshotChromiumRegistration(target chromiumBrowserTarget, path string) (registrationSnapshot, error) {
	manifest, err := readOptionalFile(path)
	if err != nil {
		return registrationSnapshot{}, err
	}
	receipt, err := readOptionalFile(registrationReceiptPath(path))
	if err != nil {
		return registrationSnapshot{}, err
	}
	platform, err := snapshotPlatformChromium(target, path)
	if err != nil {
		return registrationSnapshot{}, err
	}
	return registrationSnapshot{target: target, manifestPath: path, manifest: manifest, receipt: receipt, platform: platform}, nil
}

func snapshotFirefoxRegistration(path string) (registrationSnapshot, error) {
	manifest, err := readOptionalFile(path)
	if err != nil {
		return registrationSnapshot{}, err
	}
	receipt, err := readOptionalFile(registrationReceiptPath(path))
	if err != nil {
		return registrationSnapshot{}, err
	}
	platform, err := snapshotPlatformFirefox(path)
	if err != nil {
		return registrationSnapshot{}, err
	}
	return registrationSnapshot{manifestPath: path, manifest: manifest, receipt: receipt, platform: platform, firefox: true}, nil
}

func rollbackRegistrationSnapshots(snapshots []registrationSnapshot) error {
	var rollbackErrs []error
	for _, snapshot := range snapshots {
		var err error
		if snapshot.firefox {
			err = restorePlatformFirefox(snapshot.manifestPath, snapshot.platform)
		} else {
			err = restorePlatformChromium(snapshot.target, snapshot.manifestPath, snapshot.platform)
		}
		if err != nil {
			rollbackErrs = append(rollbackErrs, err)
		}
	}
	seen := make(map[string]bool)
	for _, snapshot := range snapshots {
		if seen[snapshot.manifestPath] {
			continue
		}
		seen[snapshot.manifestPath] = true
		if err := restoreRegistration(snapshot.manifestPath, snapshot.manifest, snapshot.receipt); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore %s: %w", snapshot.manifestPath, err))
		}
	}
	return errors.Join(rollbackErrs...)
}

// installFirefox installs the native messaging host for Firefox.
func installFirefox(extensionID string) error {
	dir := firefoxManifestDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create manifest dir: %w", err)
	}

	binPath, err := installBinary()
	if err != nil {
		return err
	}

	manifest := nativeManifest{
		Name:        manifestNameFirefox,
		Description: "Tailscale Browser Extension Native Messaging Host",
		Path:        binPath,
		Type:        "stdio",
		AllowedExtensions: []string{
			extensionID,
		},
	}

	manifestPath := filepath.Join(dir, manifestNameFirefox+".json")
	oldManifest, err := readOptionalFile(manifestPath)
	if err != nil {
		return err
	}
	oldReceipt, err := readOptionalFile(registrationReceiptPath(manifestPath))
	if err != nil {
		return err
	}
	if err := writeRegistration(manifestPath, manifest, manifest.Path, ownerKindLegacy); err != nil {
		return err
	}
	if err := platformPostInstallFirefoxForInstall(manifestPath); err != nil {
		if rollbackErr := restoreRegistration(manifestPath, oldManifest, oldReceipt); rollbackErr != nil {
			return errors.Join(err, rollbackErr)
		}
		return err
	}
	return nil
}

const (
	ownerKindLegacy = "legacy"
	ownerKindDirect = "direct"
)

// Platform hook seams make multi-target transaction failures testable without
// changing native registry or browser integration behavior in production.
var platformPostInstallChromiumForInstall = platformPostInstallChromium
var platformPostInstallFirefoxForInstall = platformPostInstallFirefox
var platformUninstallChromiumForInstall = platformUninstallChromium
var platformUninstallFirefoxForInstall = platformUninstallFirefox

type registrationReceipt struct {
	Owner string `json:"owner"`
	Kind  string `json:"kind"`
}

// uninstall is the legacy uninstall entry point. It only removes manifests
// belonging to the historical staged runtime and never direct registrations.
func uninstall() error {
	return uninstallOwned(ownerKindLegacy, installedBinaryPath(), true, "")
}

func uninstallOwned(kind, owner string, removeLegacyBinary bool, userDataDir string) error {
	var firstErr error
	targets := chromiumManifestDirs()
	if userDataDir != "" {
		for i := range targets {
			if normalizeBrowserName(targets[i].Name) == "chrome" {
				targets[i].Dir = filepath.Join(userDataDir, "NativeMessagingHosts")
				break
			}
		}
	}
	var ownedChromium []struct {
		target       chromiumBrowserTarget
		manifestPath string
	}
	for _, target := range targets {
		path := filepath.Join(target.Dir, manifestNameChrome+".json")
		owned, err := inspectOwnedManifest(path, kind, owner)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("failed to inspect %s manifest: %w", target.Name, err)
		} else if owned {
			ownedChromium = append(ownedChromium, struct {
				target       chromiumBrowserTarget
				manifestPath string
			}{target: target, manifestPath: path})
		}
	}

	firefoxPath := filepath.Join(firefoxManifestDir(), manifestNameFirefox+".json")
	ownedFirefox, err := inspectOwnedManifest(firefoxPath, kind, owner)
	if err != nil && firstErr == nil {
		firstErr = fmt.Errorf("failed to inspect Firefox manifest: %w", err)
	}

	// Windows has one shared JSON file but several registry registrations. Take
	// the ownership snapshot first, then remove every owned registry key before
	// deleting the shared file once.
	for _, registration := range ownedChromium {
		if err := platformUninstallChromiumForInstall(registration.target, registration.manifestPath); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("failed to remove %s registration: %w", registration.target.Name, err)
		}
	}
	if ownedFirefox && firstErr == nil {
		if err := platformUninstallFirefoxForInstall(firefoxPath); err != nil {
			firstErr = fmt.Errorf("failed to remove Firefox registration: %w", err)
		}
	}
	if firstErr == nil {
		seen := make(map[string]bool)
		for _, registration := range ownedChromium {
			if !seen[registration.manifestPath] {
				if _, err := removeOwnedManifest(registration.manifestPath, kind, owner); err != nil && firstErr == nil {
					firstErr = err
				}
				seen[registration.manifestPath] = true
			}
		}
		if ownedFirefox {
			if _, err := removeOwnedManifest(firefoxPath, kind, owner); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	// A direct install can intentionally use the old staged path during
	// migration. Its receipt protects that executable from legacy uninstall.
	if firstErr == nil && removeLegacyBinary && !hasDirectRegistration(owner, userDataDir) {
		if err := os.Remove(installedBinaryPath()); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = fmt.Errorf("failed to remove installed binary: %w", err)
		}
		if err := os.Remove(installedBinaryPath() + ".old"); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = fmt.Errorf("failed to remove old installed binary: %w", err)
		}
	}
	return firstErr
}

func removeOwnedManifest(path, kind, owner string) (bool, error) {
	owned, err := inspectOwnedManifest(path, kind, owner)
	if err != nil || !owned {
		return false, err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, err
	}
	_ = os.Remove(registrationReceiptPath(path))
	return true, nil
}

func inspectOwnedManifest(path, kind, owner string) (bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var manifest nativeManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return false, fmt.Errorf("read manifest: %w", err)
	}
	receipt, receiptErr := readRegistrationReceipt(path)
	if !manifestOwnedBy(manifest, receipt, receiptErr, kind, owner) {
		return false, nil
	}
	return true, nil
}

func manifestOwnedBy(manifest nativeManifest, receipt registrationReceipt, receiptErr error, kind, owner string) bool {
	if !sameExecutablePath(manifest.Path, owner) {
		return false
	}
	if receiptErr == nil {
		return receipt.Kind == kind && sameExecutablePath(receipt.Owner, owner)
	}
	// Before receipts existed, matching the old staged path is the only safe
	// legacy fallback. A direct uninstall requires a receipt. A malformed or
	// unreadable receipt is not equivalent to an absent pre-receipt file.
	return kind == ownerKindLegacy && errors.Is(receiptErr, os.ErrNotExist) && owner == installedBinaryPath()
}

func hasDirectRegistration(owner, userDataDir string) bool {
	targets := chromiumManifestDirs()
	if userDataDir != "" {
		for i := range targets {
			if normalizeBrowserName(targets[i].Name) == "chrome" {
				targets[i].Dir = filepath.Join(userDataDir, "NativeMessagingHosts")
			}
		}
	}
	for _, target := range targets {
		path := filepath.Join(target.Dir, manifestNameChrome+".json")
		if registrationOwnedDirect(path, owner) {
			return true
		}
	}
	return registrationOwnedDirect(filepath.Join(firefoxManifestDir(), manifestNameFirefox+".json"), owner)
}

func registrationOwnedDirect(path, owner string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var manifest nativeManifest
	if json.Unmarshal(data, &manifest) != nil || !sameExecutablePath(manifest.Path, owner) {
		return false
	}
	receipt, err := readRegistrationReceipt(path)
	return err == nil && receipt.Kind == ownerKindDirect && sameExecutablePath(receipt.Owner, owner)
}

func readRegistrationReceipt(manifestPath string) (registrationReceipt, error) {
	data, err := os.ReadFile(registrationReceiptPath(manifestPath))
	if err != nil {
		return registrationReceipt{}, err
	}
	var receipt registrationReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return registrationReceipt{}, err
	}
	return receipt, nil
}

func registrationReceiptPath(manifestPath string) string { return manifestPath + ".tailchrome-receipt" }

func sameExecutablePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	aa, errA := filepath.Abs(a)
	bb, errB := filepath.Abs(b)
	if errA != nil || errB != nil {
		return false
	}
	aa, bb = filepath.Clean(aa), filepath.Clean(bb)
	if aa == bb {
		return true
	}
	infoA, errA := os.Stat(aa)
	infoB, errB := os.Stat(bb)
	// If either executable has gone away, do not guess based on a symlink or
	// basename. Stable opt symlink strings still match exactly above.
	return errA == nil && errB == nil && os.SameFile(infoA, infoB)
}

// installedBinaryPath returns the destination path installBinary copies the
// helper to.
func installedBinaryPath() string {
	binaryName := "tailscale-browser-ext"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	return filepath.Join(binaryInstallDir(), binaryName)
}

// cleanupStaleBinary retries cleanup of a Windows update sidecar whenever the
// new helper starts. This needs no elevated reboot-time file operation: once
// the old browser-spawned process exits, the next helper launch removes it.
func cleanupStaleBinary() {
	if runtime.GOOS == "windows" {
		_ = os.Remove(installedBinaryPath() + ".old")
	}
}

// installBinary copies the current binary to the install directory and returns
// the installed path.
func installBinary() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("failed to get executable path: %w", err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("failed to resolve executable path: %w", err)
	}

	destPath := installedBinaryPath()
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create install dir: %w", err)
	}

	if sameExecutablePath(exe, destPath) {
		return destPath, nil
	}

	src, err := os.Open(exe)
	if err != nil {
		return "", fmt.Errorf("failed to open source binary: %w", err)
	}
	defer src.Close()

	if err := replaceBinary(destPath, src, 0755); err != nil {
		return "", err
	}

	return destPath, nil
}

// atomicReplaceBinary stages beside the destination and renames it into
// place, so an interrupted legacy upgrade cannot leave a truncated helper.
func atomicReplaceBinary(destPath string, src io.Reader, perm os.FileMode) error {
	dir := filepath.Dir(destPath)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(destPath)+".tmp-")
	if err != nil {
		return fmt.Errorf("failed to create staged binary: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to set staged binary permissions: %w", err)
	}
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to stage binary: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to sync staged binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close staged binary: %w", err)
	}
	if err := os.Rename(tmpName, destPath); err != nil {
		return fmt.Errorf("failed to activate staged binary: %w", err)
	}
	return nil
}

// writeManifest writes a native messaging host manifest JSON file.
func writeManifest(path string, manifest nativeManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal manifest: %w", err)
	}

	return atomicWriteFileForTest(path, data, 0644, "manifest")
}

// atomicWriteFileForTest is replaceable by focused tests that need to model a
// failed activation step. Production code always uses atomicWriteFile.
var atomicWriteFileForTest = atomicWriteFile

func writeRegistration(path string, manifest nativeManifest, owner, kind string) error {
	oldManifest, err := readOptionalFile(path)
	if err != nil {
		return err
	}
	oldReceipt, err := readOptionalFile(registrationReceiptPath(path))
	if err != nil {
		return err
	}
	if err := writeManifest(path, manifest); err != nil {
		return err
	}
	receiptData, err := json.Marshal(registrationReceipt{Owner: owner, Kind: kind})
	if err != nil {
		rollbackErr := restoreRegistration(path, oldManifest, oldReceipt)
		return errors.Join(fmt.Errorf("failed to marshal registration receipt: %w", err), rollbackErr)
	}
	if err := atomicWriteFileForTest(registrationReceiptPath(path), receiptData, 0600, "registration receipt"); err != nil {
		return errors.Join(err, restoreRegistration(path, oldManifest, oldReceipt))
	}
	return nil
}

type optionalFile struct {
	data   []byte
	exists bool
}

func readOptionalFile(path string) (optionalFile, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return optionalFile{}, nil
	}
	if err != nil {
		return optionalFile{}, fmt.Errorf("failed to read existing %s: %w", filepath.Base(path), err)
	}
	return optionalFile{data: data, exists: true}, nil
}

func restoreRegistration(path string, manifest, receipt optionalFile) error {
	var firstErr error
	if manifest.exists {
		if err := atomicWriteFileForTest(path, manifest.data, 0644, "manifest rollback"); err != nil {
			firstErr = err
		}
	} else if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		firstErr = err
	}
	receiptPath := registrationReceiptPath(path)
	if receipt.exists {
		if err := atomicWriteFileForTest(receiptPath, receipt.data, 0600, "receipt rollback"); err != nil && firstErr == nil {
			firstErr = err
		}
	} else if err := os.Remove(receiptPath); err != nil && !os.IsNotExist(err) && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

func atomicWriteFile(path string, data []byte, perm os.FileMode, label string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create %s dir: %w", label, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return fmt.Errorf("failed to create %s temporary file: %w", label, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to set %s permissions: %w", label, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to write %s: %w", label, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("failed to sync %s: %w", label, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("failed to close %s: %w", label, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("failed to activate %s: %w", label, err)
	}
	return nil
}
