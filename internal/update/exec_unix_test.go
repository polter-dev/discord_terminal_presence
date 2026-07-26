//go:build !windows

package update

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestExecRunnerCancellationKillsInstallerProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stdoutReader, stdoutWriter := io.Pipe()
	result := make(chan error, 1)
	go func() {
		result <- (ExecRunner{}).Run(ctx, Command{
			Name: "sh",
			Args: []string{"-c", "sleep 30 & child=$!; echo $child; wait"},
		}, nil, stdoutWriter, io.Discard)
		_ = stdoutWriter.Close()
	}()

	line, err := bufio.NewReader(stdoutReader).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(line[:len(line)-1])
	if err != nil {
		t.Fatalf("parse child PID %q: %v", line, err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecRunner cancellation error = %v, want context.Canceled", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for processExists(childPID) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(childPID) {
		t.Fatalf("installer descendant pid %d survived cancellation", childPID)
	}
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
