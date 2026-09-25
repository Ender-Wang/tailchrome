package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func launchAgentPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", daemonServiceLabel+".plist")
}

func launchAgentPlist(binaryPath, logPath string) []byte {
	escape := func(value string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "\"", "&quot;", "'", "&apos;").Replace(value)
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>daemon</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, daemonServiceLabel, escape(binaryPath), escape(logPath), escape(logPath)))
}

func installResidentService(binaryPath string) error {
	resolved, err := validateInvokingBinaryPath(binaryPath)
	if err != nil {
		return err
	}
	logPath := filepath.Join(daemonDataDir(), "helper.log")
	plist := launchAgentPlist(resolved, logPath)
	path := launchAgentPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	if err := ensureDaemonDataDir(); err != nil {
		return err
	}
	if err := atomicWriteFile(path, plist, 0644, "LaunchAgent"); err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = exec.Command("/bin/launchctl", "bootout", domain+"/"+daemonServiceLabel).Run()
	if output, err := exec.Command("/bin/launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func uninstallResidentService() error {
	domain := "gui/" + strconv.Itoa(os.Getuid())
	_ = exec.Command("/bin/launchctl", "bootout", domain+"/"+daemonServiceLabel).Run()
	var errs []error
	paths := []string{
		launchAgentPath(),
		daemonSocketPath(),
		daemonTokenPath(),
		daemonStatePath(),
		externalProxyConfigPath(),
		filepath.Join(daemonDataDir(), "helper.log"),
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("remove %s: %w", path, err))
		}
	}
	return errors.Join(errs...)
}
