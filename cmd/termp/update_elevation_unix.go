//go:build darwin || linux

package main

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const defaultGenericInstallDir = "/usr/local/bin"

var genericInstallDirAccess = unix.Access

type automaticUpdateElevationError struct {
	destination string
}

func (e automaticUpdateElevationError) Error() string {
	return fmt.Sprintf("install destination %q is not writable; automatic update needs elevated permissions", e.destination)
}

func (automaticUpdateElevationError) AutomaticUpdateSkipped() bool {
	return true
}

func genericAutomaticUpdatePreflight() error {
	destination := os.Getenv("BINDIR")
	if destination == "" {
		destination = defaultGenericInstallDir
	}
	if err := genericInstallDirAccess(destination, unix.W_OK); err == nil {
		return nil
	} else if errors.Is(err, unix.EACCES) {
		return automaticUpdateElevationError{destination: destination}
	}

	// Preserve the fail-open contract when writability cannot be determined:
	// the installer will report and persist any resulting execution failure.
	return nil
}
