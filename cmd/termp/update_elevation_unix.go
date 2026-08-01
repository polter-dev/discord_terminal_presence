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

type automaticUpdateInstallDirError struct {
	err error
}

func (e automaticUpdateInstallDirError) Error() string {
	return e.err.Error()
}

func (e automaticUpdateInstallDirError) Unwrap() error {
	return e.err
}

func (automaticUpdateInstallDirError) AutomaticUpdateSkipped() bool {
	return true
}

func genericAutomaticUpdatePreflight() error {
	destination, err := genericUpdateInstallDir()
	if err != nil {
		return automaticUpdateInstallDirError{err: err}
	}
	err = genericInstallDirAccess(destination, unix.W_OK)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.ENOENT) {
		// The destination directory does not exist. install.sh's own
		// "[ ! -d "$bindir" ]" guard fails closed on this without ever
		// reaching its sudo branch, so it is safe to let the updater run
		// and report/persist that failure itself.
		return nil
	}

	// Every other access(2) failure — EACCES (the common case) and any other
	// errno, notably EROFS on a read-only mount — must skip. install.sh
	// decides whether to invoke sudo purely from `[ -w "$bindir" ]`, which
	// shares access(2) semantics with this check: on any of these errnos the
	// directory really is unwritable, install.sh's write probe would also
	// fail, and it would escalate with sudo for a non-interactive,
	// unattended automatic update. The automatic updater must never invoke
	// sudo (issue #495), so failing open here for anything but "destination
	// does not exist" is not safe and this used to do exactly that.
	return automaticUpdateElevationError{destination: destination}
}
