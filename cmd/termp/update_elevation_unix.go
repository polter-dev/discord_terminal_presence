//go:build darwin || linux

package main

import (
	"errors"
	"fmt"

	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
	"golang.org/x/sys/unix"
)

var genericInstallDirAccess = unix.Access
var genericUpdateInstallDir = updatepkg.GenericInstallDir

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
	destination, err := genericUpdateInstallDir()
	if err != nil {
		return err
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
