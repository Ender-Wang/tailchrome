//go:build !darwin

package main

import (
	"errors"
	"io"
)

func runDaemon() error { return errors.New("resident daemon is currently supported only on macOS") }

func runNativeBridge(io.Reader, io.Writer) (bool, error) { return false, nil }
