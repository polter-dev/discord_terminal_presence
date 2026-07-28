//go:build !windows

package update

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestExecRunnerProcessGroupMatchesInteractivity(t *testing.T) {
	for _, tt := range []struct {
		name        string
		interactive bool
		wantSetpgid bool
	}{
		{name: "interactive", interactive: true, wantSetpgid: false},
		{name: "automatic", interactive: false, wantSetpgid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command("true")
			configureUpdateCommand(cmd, tt.interactive)
			if tt.interactive {
				if cmd.SysProcAttr != nil {
					t.Fatalf("interactive command SysProcAttr = %#v, want nil", cmd.SysProcAttr)
				}
				return
			}
			if cmd.SysProcAttr == nil {
				t.Fatal("automatic command has nil SysProcAttr")
			}
			if got := cmd.SysProcAttr.Setpgid; got != tt.wantSetpgid {
				t.Fatalf("configured command Setpgid = %t, want %t", got, tt.wantSetpgid)
			}
		})
	}
}

func TestAutomaticExecRunnerCancellationKillsInstallerProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stdoutReader, stdoutWriter := io.Pipe()
	result := make(chan error, 1)
	go func() {
		result <- (ExecRunner{Interactive: false}).Run(ctx, Command{
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
