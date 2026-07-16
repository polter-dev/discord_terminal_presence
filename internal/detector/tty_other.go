//go:build !darwin

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

func (systemTTYAtimeSource) Atime(string) (time.Time, error) {
	return time.Time{}, errors.New("tty atime unsupported")
}
