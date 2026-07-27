//go:build !windows

package main

import (
	"context"
	"errors"
	"runtime"
)

const connectSupported = false

// TODO(#237): Implement and live-test the daemon control transport separately
// on macOS and Linux against a real Discord IPC socket.
func startControlServer(context.Context, int, controlHandler) (func(), error) {
	return func() {}, nil
}

func sendControlRequest(context.Context, int, controlRequest) (controlResponse, error) {
	return controlResponse{}, errors.New("daemon control transport is not implemented on " + runtime.GOOS)
}
