//go:build !windows

package update

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
)

func runUpdateCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}

	wait := make(chan error, 1)
	go func() {
		wait <- cmd.Wait()
	}()

	select {
	case err := <-wait:
		return err
	case <-ctx.Done():
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			_ = cmd.Process.Kill()
		}
		<-wait
		return ctx.Err()
	}
}
