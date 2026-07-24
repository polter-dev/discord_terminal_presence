//go:build !windows

package main

import (
	"context"
	"strconv"
)

func shutdownEventName(pid int) string {
	return `Local\TermpDaemonShutdown-` + strconv.Itoa(pid)
}

func installShutdownSignal(cancel context.CancelFunc) func() {
	return func() {}
}
