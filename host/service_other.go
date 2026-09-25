//go:build !darwin

package main

func installResidentService(string) error { return nil }
func uninstallResidentService() error     { return nil }
