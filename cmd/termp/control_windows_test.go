//go:build windows

package main

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestWindowsControlPipeRoundTripValidatesServerProcess(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stop, err := startControlServer(ctx, os.Getpid(), func(_ context.Context, request controlRequest) controlResponse {
		return controlResponse{Status: request.Command}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		stop()
	}()

	requestCtx, requestCancel := context.WithTimeout(context.Background(), time.Second)
	defer requestCancel()
	response, err := sendControlRequest(requestCtx, os.Getpid(), controlRequest{Command: "connected"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != "connected" {
		t.Fatalf("response = %+v", response)
	}
}
