//go:build darwin || linux

package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
	"golang.org/x/sys/unix"
)

func stubGenericInstallDirAccess(t *testing.T, err error) {
	t.Helper()
	original := genericInstallDirAccess
	genericInstallDirAccess = func(string, uint32) error {
		return err
	}
	t.Cleanup(func() {
		genericInstallDirAccess = original
	})
}

func automaticGenericUpdateTestInputs(t *testing.T) (config.Config, *updatepkg.Checker, string) {
	t.Helper()
	cfg := config.Default()
	cfg.AutoUpdate = true
	statePath := filepath.Join(t.TempDir(), "update-check.json")
	checker := updatepkg.NewChecker(&staticReleaseSource{latest: "v1.1.0"}, statePath)
	checker.DetectInstall = func() updatepkg.InstallMethod { return updatepkg.InstallGeneric }
	return cfg, checker, statePath
}

func TestAutomaticGenericUpdateSkipsNonWritableDestination(t *testing.T) {
	const destination = "/system/bin"
	t.Setenv("BINDIR", destination)
	stubGenericInstallDirAccess(t, unix.EACCES)
	cfg, checker, statePath := automaticGenericUpdateTestInputs(t)
	runner := &recordingUpdateRunner{}

	runAutomaticUpdateWithStatePath(context.Background(), cfg, "1.0.0", checker, runner, statePath)

	if runner.calls != 0 {
		t.Fatalf("non-writable destination ran updater %d times", runner.calls)
	}
	attempt, ok := updatepkg.ReadAutomaticUpdateAttempt(statePath)
	if !ok || !attempt.Skipped || attempt.Target != "v1.1.0" {
		t.Fatalf("recorded attempt = (%+v, %t), want skipped v1.1.0 attempt", attempt, ok)
	}
	for _, want := range []string{destination, "not writable", "elevated permissions"} {
		if !strings.Contains(attempt.Error, want) {
			t.Fatalf("recorded skip %q missing %q", attempt.Error, want)
		}
	}
	status := automaticUpdateFailure(statePath)
	for _, want := range []string{"skipped for v1.1.0", "elevated permissions", "run `termp update` manually"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status update reason %q missing %q", status, want)
		}
	}
}

func TestAutomaticGenericUpdateAttemptsWritableDestination(t *testing.T) {
	t.Setenv("BINDIR", t.TempDir())
	stubGenericInstallDirAccess(t, nil)
	cfg, checker, statePath := automaticGenericUpdateTestInputs(t)
	runner := &recordingUpdateRunner{}

	runAutomaticUpdateWithStatePath(context.Background(), cfg, "1.0.0", checker, runner, statePath)

	if runner.calls != 2 || runner.command.Name != "sh" {
		t.Fatalf("writable destination runner = (%d, %#v), want two calls ending in sh", runner.calls, runner.command)
	}
}

func TestAutomaticGenericUpdatePreflightFailsOpen(t *testing.T) {
	t.Setenv("BINDIR", filepath.Join(t.TempDir(), "missing"))
	stubGenericInstallDirAccess(t, unix.ENOENT)
	cfg, checker, statePath := automaticGenericUpdateTestInputs(t)
	runner := &recordingUpdateRunner{}

	runAutomaticUpdateWithStatePath(context.Background(), cfg, "1.0.0", checker, runner, statePath)

	if runner.calls != 2 {
		t.Fatalf("indeterminate preflight ran updater %d times, want 2", runner.calls)
	}
}

func TestInteractiveGenericUpdateDoesNotRunElevationPreflight(t *testing.T) {
	t.Setenv("BINDIR", "/system/bin")
	stubGenericInstallDirAccess(t, unix.EACCES)
	checker := stubLatestChecker{result: updatepkg.Result{
		Current: "1.0.0",
		Latest:  "v1.1.0",
		Method:  updatepkg.InstallGeneric,
	}}
	runner := &recordingUpdateRunner{}

	if err := runUpdate(context.Background(), context.Background(), "1.0.0", checker, runner, strings.NewReader("password\n"), io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if runner.calls != 2 || runner.command.Name != "sh" {
		t.Fatalf("interactive update runner = (%d, %#v), want two calls ending in sh", runner.calls, runner.command)
	}
}
