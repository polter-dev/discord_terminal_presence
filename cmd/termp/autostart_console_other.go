//go:build !windows

package main

func setAutostartConsoleTitle(string) error { return nil }

func freeAutostartConsole() error { return nil }
