//go:build windows

package update

import (
	"context"
	"os/exec"
)

func runUpdateCommand(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
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
		_ = cmd.Process.Kill()
		<-wait
		return ctx.Err()
	}
}
