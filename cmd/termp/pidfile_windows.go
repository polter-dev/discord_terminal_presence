//go:build windows

package main

import "os"

func createPIDFile(path string) (*os.File, error) {
	flags := os.O_CREATE | os.O_WRONLY | os.O_TRUNC | os.O_EXCL
	return os.OpenFile(path, flags, 0o600)
}

func openPIDFile(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY, 0)
}

func requireCurrentUserOwner(_ os.FileInfo, _ string) error {
	// Windows identifies owners with SIDs rather than Unix UIDs. The shared
	// validation still requires PID files to be regular files; skip UID equality.
	return nil
}
