//go:build !linux

package update

import (
	"context"
	"errors"
	"io"
)

func performSystemPackageUpdate(
	context.Context,
	InstallMethod,
	string,
	CommandRunner,
	io.Reader,
	io.Writer,
	io.Writer,
) error {
	return errors.New("system package updates are only supported on Linux")
}
