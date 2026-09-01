package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
)

const testInstallerPID = 4242
const testInstallerStartTime = 987654

// noDaemonRunning is the daemonRunning stub for update tests that are not about
// the restart guidance. It keeps every one of them hermetic: without it those
// tests would call the real detector and read the host's PID file.
func noDaemonRunning() bool { return false }

func daemonIsRunning() bool { return true }

type fixedChecker struct {
	result updatepkg.Result
	err    error
}

func (c fixedChecker) Latest(context.Context, string) (updatepkg.Result, error) {
	return c.result, c.err
}

type okRunner struct{}

func (okRunner) Run(context.Context, updatepkg.Command, io.Reader, io.Writer, io.Writer) error {
	return nil
}

func runUpdateCapturingStdout(t *testing.T, daemonRunning func() bool) string {
	t.Helper()
	checker := fixedChecker{result: updatepkg.Result{
		Current: "0.1.3",
		Latest:  "0.1.4",
		Method:  updatepkg.InstallHomebrew,
	}}
	var stdout bytes.Buffer
	err := runUpdate(
		context.Background(),
		context.Background(),
		"0.1.3",
		checker,
		okRunner{},
		nil,
		&stdout,
		io.Discard,
		daemonRunning,
	)
	if err != nil {
		t.Fatalf("runUpdate() error = %v, want nil", err)
	}
	return stdout.String()
}

func TestUpdateSuccessNamesTheNewVersion(t *testing.T) {
	out := runUpdateCapturingStdout(t, noDaemonRunning)
	if !strings.Contains(out, "Updated termp from 0.1.3 to 0.1.4.") {
		t.Fatalf("update output = %q, want a completion line naming the new version", out)
	}
}

func TestUpdateGuidesARestartOnlyWhenADaemonIsRunning(t *testing.T) {
	running := runUpdateCapturingStdout(t, daemonIsRunning)
	for _, want := range []string{
		"still running the previous version",
		`"termp stop"`,
		`"termp start"`,
	} {
		if !strings.Contains(running, want) {
			t.Fatalf("update output with a daemon running = %q, want it to contain %q", running, want)
		}
	}

	stopped := runUpdateCapturingStdout(t, noDaemonRunning)
	if strings.Contains(stopped, "still running the previous version") {
		t.Fatalf("update output with no daemon = %q, want no restart guidance", stopped)
	}
	if strings.Contains(stopped, "termp stop") {
		t.Fatalf("update output with no daemon = %q, want no stop/start commands", stopped)
	}
}

// There is no `termp restart`. The guidance has to name commands that exist,
// so this pins that the printed commands are real subcommands (issue #584).
func TestUpdateRestartGuidanceNamesRealCommands(t *testing.T) {
	if knownCommand("restart") {
		t.Fatal("a `termp restart` command now exists; the update guidance should name it instead of stop plus start")
	}
	for _, command := range []string{"stop", "start"} {
		if !knownCommand(command) {
			t.Fatalf("update guidance names %q, which is not a known command", command)
		}
	}
	out := runUpdateCapturingStdout(t, daemonIsRunning)
	if strings.Contains(out, "termp restart") {
		t.Fatalf("update output = %q, want no reference to a nonexistent `termp restart`", out)
	}
}

// Windows cannot replace a running termp.exe, so the generic path there never
// performs an update at all: it prints guidance and returns. Claiming an update
// completed would be a lie, so the completion line must not appear.
func TestWindowsArchiveUpdatePrintsNoCompletionLine(t *testing.T) {
	checker := fixedChecker{result: updatepkg.Result{
		Current: "0.1.3",
		Latest:  "0.1.4",
		Method:  updatepkg.InstallGeneric,
	}}
	var stdout bytes.Buffer
	if err := runUpdateForPlatform(
		context.Background(),
		context.Background(),
		"0.1.3",
		checker,
		okRunner{},
		nil,
		&stdout,
		io.Discard,
		daemonIsRunning,
		"windows",
	); err != nil {
		t.Fatalf("runUpdateForPlatform() error = %v, want nil", err)
	}
	if got := stdout.String(); strings.Contains(got, "Updated termp from") {
		t.Fatalf("Windows archive output = %q, want no completion line for an update that did not happen", got)
	}
}

func TestUpToDateInstallPrintsNoRestartGuidance(t *testing.T) {
	checker := fixedChecker{result: updatepkg.Result{
		Current: "0.1.4",
		Latest:  "0.1.4",
		Method:  updatepkg.InstallHomebrew,
	}}
	var stdout bytes.Buffer
	if err := runUpdate(
		context.Background(),
		context.Background(),
		"0.1.4",
		checker,
		okRunner{},
		nil,
		&stdout,
		io.Discard,
		daemonIsRunning,
	); err != nil {
		t.Fatalf("runUpdate() error = %v, want nil", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "already on the latest version") {
		t.Fatalf("up-to-date output = %q, want the already-current line", out)
	}
	if strings.Contains(out, "still running the previous version") {
		t.Fatalf("up-to-date output = %q, want no restart guidance", out)
	}
}

func TestAutomaticUpdateWritesActionableNoticeToDaemonLog(t *testing.T) {
	_ = os.Unsetenv("NO_UPDATE_CHECK")
	t.Cleanup(func() { _ = os.Unsetenv("NO_UPDATE_CHECK") })

	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	var logs bytes.Buffer
	log.SetOutput(&logs)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	statePath := filepath.Join(t.TempDir(), "update.json")
	checker := updatepkg.NewChecker(&staticReleaseSource{latest: "0.1.4"}, statePath)
	checker.DetectInstall = func() updatepkg.InstallMethod { return updatepkg.InstallGo }
	cfg := config.Default()
	cfg.AutoUpdate = true
	runAutomaticUpdateWithStatePathForPlatform(context.Background(), cfg, "0.1.3", checker, &recordingUpdateRunner{}, statePath, "linux")

	for _, want := range []string{"Automatic update installed 0.1.4.", `"termp stop"`, `"termp start"`} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("automatic update log = %q, want it to contain %q", logs.String(), want)
		}
	}
}

func writeSucceededAttempt(t *testing.T, target string) string {
	t.Helper()
	statePath := filepath.Join(t.TempDir(), "update.json")
	if err := updatepkg.RecordAutomaticUpdateAttemptForProcess(statePath, target, time.Now(), nil, testInstallerPID, testInstallerStartTime); err != nil {
		t.Fatalf("RecordAutomaticUpdateAttemptForProcess() error = %v", err)
	}
	return statePath
}

func installingDaemonRecord() daemonPIDRecord {
	return daemonPIDRecord{PID: testInstallerPID, StartTime: testInstallerStartTime}
}

// The automatic path may have no attached terminal, so status carries the
// recorded notice in addition to the normal daemon log (#584).
func TestStatusReportsAnAutomaticUpdateAwaitingRestart(t *testing.T) {
	statePath := writeSucceededAttempt(t, "0.1.4")
	got := automaticUpdateStatus(statePath, true, "0.1.4", "linux", updatepkg.InstallGeneric, installingDaemonRecord())
	for _, want := range []string{"installed 0.1.4", "still on the previous version", `"termp stop"`, `"termp start"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("automaticUpdateStatus() = %q, want it to contain %q", got, want)
		}
	}
}

func TestStatusStaysQuietWhenNoDaemonIsRunning(t *testing.T) {
	statePath := writeSucceededAttempt(t, "0.1.4")
	if got := automaticUpdateStatus(statePath, true, "0.1.4", "linux", updatepkg.InstallGeneric, daemonPIDRecord{}); got != "" {
		t.Fatalf("automaticUpdateStatus() with no daemon = %q, want empty", got)
	}
}

// While this binary is still behind the recorded target the install has not
// been observed to land, so status must not claim it did.
func TestStatusStaysQuietWhileTheBinaryIsStillBehindTheTarget(t *testing.T) {
	statePath := writeSucceededAttempt(t, "0.1.4")
	if got := automaticUpdateStatus(statePath, true, "0.1.3", "linux", updatepkg.InstallGeneric, installingDaemonRecord()); got != "" {
		t.Fatalf("automaticUpdateStatus() before the install landed = %q, want empty", got)
	}
}

// The notice has to end by itself. A restarted daemon runs the new version, and
// its startup retirement clears the record that carries the notice.
func TestRestartedDaemonRetiresTheSucceededAttempt(t *testing.T) {
	statePath := writeSucceededAttempt(t, "0.1.4")
	retireStaleAutomaticUpdateAttempt(statePath, "0.1.4", "0.1.4")
	if _, ok := updatepkg.ReadAutomaticUpdateAttempt(statePath); ok {
		t.Fatal("a succeeded attempt the running version satisfies was not retired")
	}
	if got := automaticUpdateStatus(statePath, true, "0.1.4", "linux", updatepkg.InstallGeneric, installingDaemonRecord()); got != "" {
		t.Fatalf("automaticUpdateStatus() after the restart = %q, want empty", got)
	}
}

func TestStatusStaysQuietAfterAReplacementDaemonStarts(t *testing.T) {
	statePath := writeSucceededAttempt(t, "0.1.4")
	for _, replacement := range []daemonPIDRecord{
		{PID: testInstallerPID + 1, StartTime: testInstallerStartTime + 1},
		{PID: testInstallerPID, StartTime: testInstallerStartTime + 1},
	} {
		if got := automaticUpdateStatus(statePath, true, "0.1.4", "linux", updatepkg.InstallGeneric, replacement); got != "" {
			t.Fatalf("automaticUpdateStatus() for replacement %+v = %q, want empty", replacement, got)
		}
	}
}

// The daemon that installed the update is still on the old version, so its own
// ticker must not retire the record out from under the notice.
func TestInstallingDaemonKeepsTheSucceededAttempt(t *testing.T) {
	statePath := writeSucceededAttempt(t, "0.1.4")
	retireStaleAutomaticUpdateAttempt(statePath, "0.1.3", "0.1.4")
	if _, ok := updatepkg.ReadAutomaticUpdateAttempt(statePath); !ok {
		t.Fatal("the installing daemon retired its own succeeded attempt")
	}
}

// A later release must not erase the pending-restart record: the daemon has
// still not picked up the version that did install.
func TestASupersededSuccessIsKept(t *testing.T) {
	statePath := writeSucceededAttempt(t, "0.1.4")
	retireStaleAutomaticUpdateAttempt(statePath, "0.1.3", "0.1.5")
	if _, ok := updatepkg.ReadAutomaticUpdateAttempt(statePath); !ok {
		t.Fatal("a succeeded attempt was retired because a newer release exists")
	}
}

func TestSucceededAttemptDoesNotRenderAsAFailure(t *testing.T) {
	statePath := writeSucceededAttempt(t, "0.1.4")
	if got := automaticUpdateFailure(statePath, "0.1.3"); got != "" {
		t.Fatalf("automaticUpdateFailure() for a success = %q, want empty", got)
	}
}
