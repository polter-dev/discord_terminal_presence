//go:build !windows

package update

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
)

func runUpdateCommand(ctx context.Context, cmd *exec.Cmd, interactive bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	configureUpdateCommand(cmd, interactive)
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
		pid := cmd.Process.Pid
		if !interactive {
			pid = -pid
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			_ = cmd.Process.Kill()
		}
		<-wait
		return ctx.Err()
	}
}

func configureUpdateCommand(cmd *exec.Cmd, interactive bool) {
	if interactive {
		cmd.SysProcAttr = nil
		return
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}
