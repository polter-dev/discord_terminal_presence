//go:build !windows

package main

import (
	"runtime"
	"strings"
	"testing"
)

func TestConnectCommandReportsPlatformUnsupported(t *testing.T) {
	err := connectCommand(nil)
	if err == nil {
		t.Fatal("connectCommand() succeeded on an unsupported platform")
	}
	want := "not yet supported on " + runtime.GOOS
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("connectCommand() error = %q, want %q", err, want)
	}
}
