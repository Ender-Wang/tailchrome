package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const chromeFlatpakID = "com.google.Chrome"

// hostStateDir keeps the historical native state location stable. A helper
// launched by Chrome Flatpak is a different installation and must use the
// app's writable configuration tree instead.
func hostStateDir(homeDir, initID string) (string, error) {
	if !validInitID.MatchString(initID) {
		return "", errors.New("invalid Chrome Flatpak sandbox state root: invalid profile ID")
	}
	if os.Getenv("FLATPAK_ID") != chromeFlatpakID {
		return filepath.Join(homeDir, ".config", "tailscale-browser-ext", initID), nil
	}

	configRoot := os.Getenv("XDG_CONFIG_HOME")
	if configRoot == "" {
		configRoot = filepath.Join(homeDir, ".var", "app", chromeFlatpakID, "config")
	}
	if err := validateFlatpakConfigRoot(homeDir, configRoot); err != nil {
		return "", err
	}
	stateDir := filepath.Join(configRoot, "tailscale-browser-ext", initID)
	if err := rejectPathSymlinks(stateDir); err != nil {
		return "", fmt.Errorf("invalid Chrome Flatpak sandbox state root: %w", err)
	}
	if info, err := os.Lstat(stateDir); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", fmt.Errorf("invalid Chrome Flatpak sandbox state root %q: profile state must be a directory, not a symlink or file", stateDir)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("invalid Chrome Flatpak sandbox state root %q: %w", stateDir, err)
	}
	return stateDir, nil
}

func flatpakAppDir(homeDir string) string {
	return filepath.Join(homeDir, ".var", "app", chromeFlatpakID)
}

func validateFlatpakConfigRoot(homeDir, configRoot string) error {
	if configRoot == "" || !filepath.IsAbs(configRoot) {
		return errors.New("invalid Chrome Flatpak sandbox state root: XDG_CONFIG_HOME must be an absolute path inside the Chrome Flatpak app directory")
	}
	configRoot = filepath.Clean(configRoot)
	appDir := filepath.Clean(flatpakAppDir(homeDir))
	expectedParent := filepath.Join(appDir, "config")
	if err := rejectPathSymlinks(appDir); err != nil {
		return fmt.Errorf("invalid Chrome Flatpak sandbox state root: %w", err)
	}
	if !pathWithinFlatpak(appDir, configRoot) || configRoot == appDir || !pathWithinFlatpak(expectedParent, configRoot) {
		return fmt.Errorf("invalid Chrome Flatpak sandbox state root %q: XDG_CONFIG_HOME must be inside %q", configRoot, expectedParent)
	}
	if err := rejectPathSymlinks(configRoot); err != nil {
		return fmt.Errorf("invalid Chrome Flatpak sandbox state root: %w", err)
	}
	// Do not allow an existing symlink to redirect state outside the app. The
	// host init path creates missing directories after this check.
	if info, err := os.Lstat(configRoot); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("invalid Chrome Flatpak sandbox state root %q: must be a directory, not a symlink or file", configRoot)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("invalid Chrome Flatpak sandbox state root %q: %w", configRoot, err)
	}
	stateRoot := filepath.Join(configRoot, "tailscale-browser-ext")
	if err := rejectPathSymlinks(stateRoot); err != nil {
		return fmt.Errorf("invalid Chrome Flatpak sandbox state root: %w", err)
	}
	return nil
}

func pathWithinFlatpak(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && rel != ".." && !(len(rel) > 3 && rel[:3] == ".."+string(filepath.Separator))
}

// rejectPathSymlinks checks every existing component, including ancestors of
// a not-yet-created leaf. This prevents HOME/.var or an XDG subdirectory from
// redirecting the sandbox state outside the app data tree.
func rejectPathSymlinks(path string) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	rel := strings.TrimPrefix(path, current)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q contains symlink %q", path, current)
		}
	}
	return nil
}
