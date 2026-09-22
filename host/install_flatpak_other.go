//go:build !linux

package main

import "errors"

func installChromeFlatpak(_ string) error {
	return errors.New("Chrome Flatpak installation is supported only on Linux")
}

func uninstallChromeFlatpak() error {
	return errors.New("Chrome Flatpak installation is supported only on Linux")
}
