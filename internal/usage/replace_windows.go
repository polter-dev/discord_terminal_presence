//go:build windows

package usage

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

const replaceRetryTimeout = 500 * time.Millisecond

func replaceFile(from, to string) error {
	deadline := time.Now().Add(replaceRetryTimeout)
	for {
		err := windows.Rename(from, to)
		if err == nil {
			return nil
		}
		if !retryableReplaceError(err) {
			return err
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func readFile(path string) ([]byte, error) {
	deadline := time.Now().Add(replaceRetryTimeout)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			return data, nil
		}
		if !retryableReplaceError(err) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func retryableReplaceError(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) ||
		errors.Is(err, windows.ERROR_ACCESS_DENIED)
}
