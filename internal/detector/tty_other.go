//go:build !darwin && !linux && !windows

package detector

import (
	"errors"
	"time"
)

type unknownTTYResolver struct{}

func newSystemTTYResolver() TTYResolver {
	return unknownTTYResolver{}
}

func (unknownTTYResolver) Resolve(int32) (TTYResolution, error) {
	return TTYResolution{}, errors.New("controlling tty resolution unsupported")
}

type systemTTYAtimeSource struct{}

func newSystemTTYAtimeSource() TTYAtimeSource {
	return systemTTYAtimeSource{}
}

func (systemTTYAtimeSource) Atime(string) (time.Time, error) {
	return time.Time{}, errors.New("tty atime unsupported")
}
