package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/presence"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
	"github.com/polter-dev/discord_terminal_presence/internal/service"
	"github.com/polter-dev/discord_terminal_presence/internal/tui"
	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
)

const fixtureProcessStartTime = uint64(1)

func useFixtureProcessStartTime(t *testing.T) {
	t.Helper()
	oldLookup := lookupProcessStartTime
	lookupProcessStartTime = func(int) (uint64, error) {
		return fixtureProcessStartTime, nil
	}
	t.Cleanup(func() {
		lookupProcessStartTime = oldLookup
	})
}

type failingReleaseSource struct {
	calls int
}

type fakeSetupServiceManager struct {
	installedExe   string
	definitionExe  string
	uninstallCalls int
	installed      bool
}

func (m *fakeSetupServiceManager) Install(exe string, _ bool) (service.State, error) {
	m.installedExe = exe
	return service.State{Supported: true, Installed: true}, nil
}

func (m *fakeSetupServiceManager) InstallDefinition(exe string, _ bool) (service.State, error) {
	m.definitionExe = exe
	return service.State{Supported: true, Installed: true}, nil
}

func (m *fakeSetupServiceManager) Uninstall(_ bool) (service.State, error) {
	m.uninstallCalls++
	return service.State{Supported: true}, nil
}

func (m *fakeSetupServiceManager) Status() service.State {
	return service.State{Supported: true, Installed: m.installed}
}

func TestNewSetupModelWiresServiceUninstall(t *testing.T) {
	cfg := config.Default()
	cfg.StartAtLogin = true
	manager := &fakeSetupServiceManager{installed: true}
	var saved config.Config
	model := newSetupModel(cfg, func(next config.Config) (string, error) {
		saved = next
		return "/tmp/config.toml", nil
	}, manager, func() (string, error) {
		t.Fatal("executable resolution should not run while disabling autostart")
		return "", nil
	})

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = updated.(tui.SetupModel)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.SetupModel)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.SetupModel)
	if cmd == nil || manager.uninstallCalls != 0 {
		t.Fatal("setup confirmation should return a command without uninstalling inline")
	}
	updated, _ = model.Update(cmd())
	model = updated.(tui.SetupModel)

	if manager.uninstallCalls != 1 || manager.installedExe != "" {
		t.Fatalf("service calls = uninstall:%d install:%q, want 1/empty", manager.uninstallCalls, manager.installedExe)
	}
	if saved.StartAtLogin || model.SetupConfig().StartAtLogin || !model.Applied() {
		t.Fatalf("setup result = saved:%t model:%t applied:%t", saved.StartAtLogin, model.SetupConfig().StartAtLogin, model.Applied())
	}
}

func TestNewSetupModelWiresCompletionInstallWithTempHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("SHELL", "/usr/local/bin/fish")
	manager := &fakeSetupServiceManager{}
	cfg := config.Default()
	cfg.StartAtLogin = false
	model := newSetupModel(cfg, nil, manager, nil)

	for range 3 {
		updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
		model = updated.(tui.SetupModel)
	}
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = updated.(tui.SetupModel)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.SetupModel)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(tui.SetupModel)
	if cmd == nil {
		t.Fatal("completion confirmation did not return an apply command")
	}
	updated, _ = model.Update(cmd())
	model = updated.(tui.SetupModel)

	path := filepath.Join(home, ".config", "fish", "completions", "termp.fish")
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want, err := completionScript("fish")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("installed completion differs from `termp completion fish` output")
	}
	if !strings.Contains(model.View(), "Completion: installed: "+path) {
		t.Fatalf("setup summary does not show installed path:\n%s", model.View())
	}
}

func TestSetupReconcilesDefinitionWithoutLaunchingWhenDaemonRunning(t *testing.T) {
	manager := &fakeSetupServiceManager{}
	if err := installSetupAutostart(manager, `C:\bin\termp.exe`, true); err != nil {
		t.Fatal(err)
	}
	if manager.definitionExe != `C:\bin\termp.exe` || manager.installedExe != "" {
		t.Fatalf("definition/install calls = %q/%q, want definition only", manager.definitionExe, manager.installedExe)
	}
}

func (s *failingReleaseSource) Latest(context.Context, string) (string, error) {
	s.calls++
	return "", errors.New("network must not be used")
}

type staticReleaseSource struct {
	latest string
	calls  int
}

func (s *staticReleaseSource) Latest(context.Context, string) (string, error) {
	s.calls++
	return s.latest, nil
}

type stubLatestChecker struct {
	result updatepkg.Result
	err    error
}

func (c stubLatestChecker) Latest(context.Context, string) (updatepkg.Result, error) {
	return c.result, c.err
}

type recordingUpdateRunner struct {
	command  updatepkg.Command
	commands []updatepkg.Command
	calls    int
	err      error
}

func (r *recordingUpdateRunner) Run(_ context.Context, command updatepkg.Command, _ io.Reader, _, _ io.Writer) error {
	r.command = command
	r.commands = append(r.commands, command)
	r.calls++
	if r.err != nil {
		return r.err
	}
	if command.Name == "curl" {
		for i, arg := range command.Args {
			if arg == "-o" && i+1 < len(command.Args) {
				return os.WriteFile(command.Args[i+1], []byte("#!/bin/sh\n"), 0o600)
			}
		}
	}
	return nil
}

var expectedCommands = commandNames()

type fileInfoWithSys struct {
	os.FileInfo
	sys any
}

func (i fileInfoWithSys) Sys() any { return i.sys }

func requireSymlink(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable on this account: %v", err)
	}
}

func withTermpConfigHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	configHome := filepath.Join(root, "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("APPDATA", configHome)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return configHome
}

func TestPIDFilePathUsesPrivateUserCacheDirectory(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", filepath.Join(t.TempDir(), "cache"))

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(cacheDir, "termp", "run", "termp.pid")
	if got := pidFilePath(); got != want {
		t.Fatalf("pidFilePath() = %q, want %q", got, want)
	}
	if want == filepath.Join(os.TempDir(), "termp.pid") {
		t.Fatal("PID path still uses the shared temporary file")
	}
	if err := writePID(want, 99999999); err != nil {
		t.Fatal(err)
	}
	assertPIDDirectoryMode(t, filepath.Dir(want))
}

func TestWritePIDUses0600AndRefusesSymlink(t *testing.T) {
	requireSymlink(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "termp.pid")
	if err := writePID(path, 99999998); err != nil {
		t.Fatal(err)
	}
	assertPIDFileMode(t, path)
	if err := writePID(path, 99999997); err != nil {
		t.Fatalf("replace stale PID file: %v", err)
	}
	if pid, err := readPID(path); err != nil || pid != 99999997 {
		t.Fatalf("replaced readPID() = %d, %v; want 99999997, nil", pid, err)
	}

	victim := filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}
	if err := writePID(path, 1234); err == nil {
		t.Fatal("writePID followed or replaced a symlink")
	}
	data, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != "unchanged" {
		t.Fatalf("symlink target was modified: %q", got)
	}
	if _, err := readPID(path); err == nil {
		t.Fatal("readPID followed a symlink")
	}
}

func TestReadPIDRequiresRegularFileOwnedByCurrentUser(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "termp.pid")
	if err := os.WriteFile(path, []byte("1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if pid, err := readPID(path); err != nil || pid != 1234 {
		t.Fatalf("readPID() = %d, %v; want 1234, nil", pid, err)
	}

	directoryPath := filepath.Join(dir, "directory.pid")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readPID(directoryPath); err == nil ||
		(!strings.Contains(err.Error(), "regular file") &&
			!(runtime.GOOS == "windows" && strings.Contains(strings.ToLower(err.Error()), "access is denied"))) {
		t.Fatalf("readPID(directory) error = %v, want unusable-PID-file rejection", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	foreignUID := uint32(os.Geteuid() + 1)
	if foreignInfo, ok := foreignOwnerFileInfo(info, foreignUID); ok {
		if err := validatePIDFileInfo(foreignInfo, path); err == nil || !strings.Contains(err.Error(), "not current uid") {
			t.Fatalf("foreign owner check error = %v, want owner rejection", err)
		}
	}
}

func TestRemovePIDIfOwnedPreservesNewOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termp.pid")
	originalInfo, err := writePIDOwned(path, 1234)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := writePID(path, 5678); err != nil {
		t.Fatal(err)
	}

	removed, err := removePIDIfOwned(path, 1234, originalInfo)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("cleanup removed a PID file owned by a newer daemon")
	}
	if pid, err := readPID(path); err != nil || pid != 5678 {
		t.Fatalf("new owner PID = %d, %v; want 5678, nil", pid, err)
	}
}

func TestPIDFileMatchesOwnerRequiresPIDAndFileIdentity(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first.pid")
	second := filepath.Join(t.TempDir(), "second.pid")
	if err := os.WriteFile(first, []byte("1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("1234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstInfo, err := os.Stat(first)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	if !pidFileMatchesOwner(1234, 1234, firstInfo, firstInfo) {
		t.Fatal("matching PID and file identity were rejected")
	}
	if pidFileMatchesOwner(1234, 5678, firstInfo, firstInfo) {
		t.Fatal("different recorded PID was accepted")
	}
	if pidFileMatchesOwner(1234, 1234, firstInfo, secondInfo) {
		t.Fatal("different file identity was accepted")
	}
}

func TestStopDaemonWaitsForExitThenRemovesPIDFile(t *testing.T) {
	useFixtureProcessStartTime(t)
	path := filepath.Join(t.TempDir(), "termp.pid")
	if err := writePID(path, 1234); err != nil {
		t.Fatal(err)
	}
	aliveChecks := 0
	alive := func(pid int) bool {
		if pid != 1234 {
			t.Fatalf("alive PID = %d, want 1234", pid)
		}
		aliveChecks++
		return aliveChecks < 4
	}
	signalCalls := 0
	signal := func(pid int) error {
		signalCalls++
		if pid != 1234 {
			t.Fatalf("signal PID = %d, want 1234", pid)
		}
		return nil
	}
	var slept time.Duration
	sleep := func(delay time.Duration) { slept += delay }

	pid, err := stopDaemon(path, time.Second, 10*time.Millisecond, alive, func(int) bool { return true }, signal, sleep, false)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 1234 || signalCalls != 1 || slept != 10*time.Millisecond {
		t.Fatalf("stop result: pid=%d signals=%d slept=%s", pid, signalCalls, slept)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PID file remains after exit: %v", err)
	}
}

func TestStopDaemonRechecksIdentityImmediatelyBeforeSignal(t *testing.T) {
	useFixtureProcessStartTime(t)
	path := filepath.Join(t.TempDir(), "termp.pid")
	if err := writePID(path, 1234); err != nil {
		t.Fatal(err)
	}
	identityChecks := 0
	signaled := false
	_, err := stopDaemon(path, time.Second, time.Millisecond,
		func(int) bool { return true },
		func(int) bool {
			identityChecks++
			return identityChecks == 1
		},
		func(int) error {
			signaled = true
			return nil
		},
		func(time.Duration) {},
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "identity changed before signaling") {
		t.Fatalf("stopDaemon() error = %v, want changed-identity error", err)
	}
	if signaled {
		t.Fatal("daemon was signaled after its identity changed")
	}
}

func TestStopDaemonSucceedsWhenDaemonRemovesPIDFile(t *testing.T) {
	useFixtureProcessStartTime(t)
	path := filepath.Join(t.TempDir(), "termp.pid")
	if err := writePID(path, 1234); err != nil {
		t.Fatal(err)
	}
	alive := true
	pid, err := stopDaemon(path, time.Second, time.Millisecond, func(int) bool {
		return alive
	}, func(int) bool {
		return true
	}, func(pid int) error {
		if pid != 1234 {
			t.Fatalf("signal PID = %d, want 1234", pid)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		alive = false
		return nil
	}, func(time.Duration) {}, false)
	if err != nil || pid != 1234 {
		t.Fatalf("stopDaemon() = %d, %v; want 1234, nil", pid, err)
	}
}

func TestStopDaemonAndPublisherStopsOrphanNotNamedByPIDFile(t *testing.T) {
	useFixtureProcessStartTime(t)
	path := filepath.Join(t.TempDir(), "termp.pid")
	if err := writePID(path, 2222); err != nil {
		t.Fatal(err)
	}
	live := map[int]bool{1111: true, 2222: true}
	var signaled []int
	pid, err := stopDaemonAndPublisher(path, daemonPIDRecord{PID: 1111, StartTime: fixtureProcessStartTime}, time.Second, time.Millisecond,
		func(pid int) bool { return live[pid] },
		func(int) bool { return true },
		func(pid int) error {
			signaled = append(signaled, pid)
			live[pid] = false
			return nil
		},
		func(time.Duration) {},
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if pid != 1111 || !reflect.DeepEqual(signaled, []int{1111, 2222}) {
		t.Fatalf("stop result pid/signals = %d/%v, want 1111/[1111 2222]", pid, signaled)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PID file remains: %v", err)
	}
}

func TestStopDaemonAndPublisherAcceptsAutostartRelaunch(t *testing.T) {
	useFixtureProcessStartTime(t)
	path := filepath.Join(t.TempDir(), "termp.pid")
	if err := writePID(path, 1234); err != nil {
		t.Fatal(err)
	}
	live := map[int]bool{1234: true, 5678: false}
	pid, err := stopDaemonAndPublisher(path, daemonPIDRecord{}, time.Second, time.Millisecond,
		func(pid int) bool { return live[pid] },
		func(pid int) bool { return pid == 1234 || pid == 5678 },
		func(pid int) error {
			live[pid] = false
			live[5678] = true
			return writePID(path, 5678)
		},
		func(time.Duration) {},
		true,
	)
	if err != nil || pid != 1234 {
		t.Fatalf("stopDaemonAndPublisher() = %d, %v; want 1234, nil", pid, err)
	}

	out, err := captureStdout(t, func() error {
		printStopSuccess(pid, service.State{Installed: true, Loaded: "active"})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "stopped (pid 1234)") || !strings.Contains(out, "termp autostart disable") {
		t.Fatalf("stop output missing relaunch guidance: %q", out)
	}
	if relaunchedPID, readErr := readPID(path); readErr != nil || relaunchedPID != 5678 {
		t.Fatalf("relaunched PID file = %d, %v; want 5678, nil", relaunchedPID, readErr)
	}
}

func TestStopDaemonAndPublisherRejectsUnexpectedPIDFileTakeover(t *testing.T) {
	useFixtureProcessStartTime(t)
	path := filepath.Join(t.TempDir(), "termp.pid")
	if err := writePID(path, 1234); err != nil {
		t.Fatal(err)
	}
	live := map[int]bool{1234: true, 5678: false}
	_, err := stopDaemonAndPublisher(path, daemonPIDRecord{}, time.Second, time.Millisecond,
		func(pid int) bool { return live[pid] },
		func(pid int) bool { return pid == 1234 },
		func(pid int) error {
			live[pid] = false
			live[5678] = true
			return writePID(path, 5678)
		},
		func(time.Duration) {},
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "daemon exited, but PID file changed ownership and was not removed") {
		t.Fatalf("stopDaemonAndPublisher() error = %v, want changed-ownership error", err)
	}
}

func TestKnownDaemonPIDFindsLivePublisherNotNamedByPIDFile(t *testing.T) {
	useFixtureProcessStartTime(t)
	pidPath := filepath.Join(t.TempDir(), "termp.pid")
	if err := writePID(pidPath, 2222); err != nil {
		t.Fatal(err)
	}
	statePath := writeDaemonDiscordStateFixture(t, daemonDiscordState{
		Connected: true,
		PID:       1111,
		StartTime: fixtureProcessStartTime,
	})
	got := knownDaemonPID(pidPath, statePath,
		func(pid int) bool { return pid == 1111 },
		func(pid int) bool { return pid == 1111 },
	)
	if got != 1111 {
		t.Fatalf("knownDaemonPID() = %d, want orphaned publisher 1111", got)
	}
}

func TestConcurrentPIDInitializationDoesNotPublishEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "termp.pid")
	initializing := make(chan struct{})
	release := make(chan struct{})
	pausedDone := make(chan error, 1)
	go func() {
		_, err := writePIDOwnedWithHook(path, os.Getpid(), func() {
			close(initializing)
			<-release
		})
		pausedDone <- err
	}()
	<-initializing

	if _, err := writePIDOwned(path, os.Getpid()); err != nil {
		close(release)
		t.Fatalf("contending starter did not acquire unpublished PID file: %v", err)
	}
	close(release)
	if err := <-pausedDone; err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("paused starter error = %v, want live-owner rejection", err)
	}
	if pid, err := readPID(path); err != nil || pid != os.Getpid() {
		t.Fatalf("published PID = %d, %v; want %d, nil", pid, err, os.Getpid())
	}
}

func TestStopDaemonRemovesStalePIDFile(t *testing.T) {
	useFixtureProcessStartTime(t)
	path := filepath.Join(t.TempDir(), "termp.pid")
	if err := writePID(path, 1234); err != nil {
		t.Fatal(err)
	}
	_, err := stopDaemon(path, time.Second, time.Millisecond, func(int) bool { return false }, func(int) bool { return true }, func(int) error {
		t.Fatal("stale PID was signaled")
		return nil
	}, func(time.Duration) { t.Fatal("stale PID wait slept") }, false)
	if err == nil || !strings.Contains(err.Error(), "stale PID file removed") {
		t.Fatalf("stopDaemon() error = %v, want stale-file message", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("stale PID file remains: %v", statErr)
	}
}

func TestStopDaemonTimeoutKeepsPIDFile(t *testing.T) {
	useFixtureProcessStartTime(t)
	path := filepath.Join(t.TempDir(), "termp.pid")
	if err := writePID(path, 1234); err != nil {
		t.Fatal(err)
	}
	var slept time.Duration
	_, err := stopDaemon(path, 25*time.Millisecond, 10*time.Millisecond, func(int) bool { return true }, func(int) bool { return true }, func(int) error {
		return nil
	}, func(delay time.Duration) { slept += delay }, false)
	if err == nil || !strings.Contains(err.Error(), "PID file was not removed") {
		t.Fatalf("stopDaemon() error = %v, want retained-file timeout", err)
	}
	if slept != 25*time.Millisecond {
		t.Fatalf("slept %s, want bounded 25ms", slept)
	}
	if pid, readErr := readPID(path); readErr != nil || pid != 1234 {
		t.Fatalf("retained PID = %d, %v; want 1234, nil", pid, readErr)
	}
}

func TestStopDaemonSignalsLiveProcessWhenStartTimeUnavailable(t *testing.T) {
	oldLookup := lookupProcessStartTime
	lookupProcessStartTime = func(int) (uint64, error) {
		return 0, errors.New("start time unavailable")
	}
	t.Cleanup(func() {
		lookupProcessStartTime = oldLookup
	})

	path := filepath.Join(t.TempDir(), "termp.pid")
	if err := writePID(path, 1234); err != nil {
		t.Fatal(err)
	}
	record, _, err := readPIDIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	if record.StartTime != 0 || !record.StartTimeUnavailable {
		t.Fatalf("PID identity = %+v, want explicit unavailable start time", record)
	}

	signals := 0
	_, err = stopDaemon(
		path,
		time.Millisecond,
		time.Millisecond,
		func(int) bool { return true },
		func(int) bool { return true },
		func(pid int) error {
			signals++
			if pid != 1234 {
				t.Fatalf("signal PID = %d, want 1234", pid)
			}
			return nil
		},
		func(time.Duration) {},
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "PID file was not removed") {
		t.Fatalf("stopDaemon() error = %v, want retained-file timeout", err)
	}
	if signals != 1 {
		t.Fatalf("signal calls = %d, want 1", signals)
	}
	if pid, readErr := readPID(path); readErr != nil || pid != 1234 {
		t.Fatalf("retained PID = %d, %v; want 1234, nil", pid, readErr)
	}
}

func TestFormatInstallSuccessShowsCTAForFreshInstall(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "termp", "config.toml")
	got := formatInstallSuccess("/usr/local/bin/termp", configPath)

	for _, want := range []string{"termp install", "Autostart", "Config", "Next step", "termp setup", "Nothing shows on your Discord profile until you do."} {
		if !strings.Contains(got, want) {
			t.Fatalf("fresh install output missing %q:\n%s", want, got)
		}
	}
}

func TestFormatInstallSuccessSkipsCTAWhenConfigExists(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "termp", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("enabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := formatInstallSuccess("/usr/local/bin/termp", configPath)
	want := "termp install\n\nAutostart\n" +
		"  Installed         /usr/local/bin/termp\n" +
		"  Runs              termp start\n" +
		"  Remove autostart  termp autostart uninstall\n"
	if got != want {
		t.Fatalf("configured install output = %q, want %q", got, want)
	}
	if strings.Contains(got, "NEXT STEP") || strings.Contains(got, "termp setup") {
		t.Fatalf("configured install unexpectedly included CTA:\n%s", got)
	}
}

func TestFormatVersionGroupedAndAligned(t *testing.T) {
	info := versionInfo{
		version:   "1.2.3",
		commit:    "abc123",
		built:     "2026-07-05",
		goVersion: "go1.26.1",
		platform:  "darwin/arm64",
	}
	want := "termp\n" +
		"  Version   1.2.3\n" +
		"  Commit    abc123\n" +
		"  Built     2026-07-05\n" +
		"  Go        go1.26.1\n" +
		"  Platform  darwin/arm64\n"
	if got := formatVersion(info); got != want {
		t.Fatalf("formatVersion() =\n%q\nwant:\n%q", got, want)
	}
}

func TestResolveVersionInfoFallsBackToGoBuildInfo(t *testing.T) {
	calls := 0
	info := resolveVersionInfo("dev", "none", "unknown", func() (*debug.BuildInfo, bool) {
		calls++
		return &debug.BuildInfo{
			Main: debug.Module{Version: "v1.2.3"},
			Settings: []debug.BuildSetting{
				{Key: "vcs.revision", Value: "0123456789abcdef"},
				{Key: "vcs.time", Value: "2026-07-23T12:34:56Z"},
			},
		}, true
	})
	if calls != 1 {
		t.Fatalf("ReadBuildInfo calls = %d, want 1", calls)
	}
	if info.version != "v1.2.3" || info.commit != "0123456" ||
		info.built != "2026-07-23T12:34:56Z" || info.dateLabel != "Commit time" {
		t.Fatalf("fallback version info = %+v", info)
	}
	if got := formatVersion(info); !strings.Contains(got, "Commit time  2026-07-23T12:34:56Z") {
		t.Fatalf("fallback output does not label vcs.time as commit time:\n%s", got)
	}
}

func TestResolveVersionInfoPreservesStampedValues(t *testing.T) {
	calls := 0
	info := resolveVersionInfo("1.2.3", "release-sha", "2026-07-23", func() (*debug.BuildInfo, bool) {
		calls++
		return &debug.BuildInfo{Main: debug.Module{Version: "v9.9.9"}}, true
	})
	if calls != 0 {
		t.Fatalf("ReadBuildInfo calls = %d, want 0 for fully stamped build", calls)
	}
	if info.version != "1.2.3" || info.commit != "release-sha" ||
		info.built != "2026-07-23" || info.dateLabel != "Built" {
		t.Fatalf("stamped version info changed: %+v", info)
	}
}

func TestCompactVersionKeepsParseableFirstToken(t *testing.T) {
	info := versionInfo{
		version:   "1.2.3",
		commit:    "abc123",
		built:     "2026-07-05",
		goVersion: runtime.Version(),
		platform:  runtime.GOOS + "/" + runtime.GOARCH,
	}
	want := "termp 1.2.3 (abc123, 2026-07-05)\ngo " + runtime.Version() + "\n" + runtime.GOOS + "/" + runtime.GOARCH + "\n"
	if got := formatCompactVersion(info); got != want {
		t.Fatalf("formatCompactVersion() = %q, want %q", got, want)
	}
}

func TestFormatStatusLabelsWindowsScheduledTask(t *testing.T) {
	info := statusInfo{
		serviceSupported: true,
		serviceInstalled: true,
		servicePath:      `\Terminal Presence\termp`,
		servicePathLabel: autostartLocationLabel("windows"),
		configOK:         true,
	}
	got := formatStatus(info)
	if !strings.Contains(got, "  Task       \\Terminal Presence\\termp\n") {
		t.Fatalf("Windows status missing Task label:\n%s", got)
	}
	if strings.Contains(got, "  Path       \\Terminal Presence\\termp\n") {
		t.Fatalf("Windows task was mislabeled as a path:\n%s", got)
	}
	if got := autostartLocationLabel("linux"); got != "Path" {
		t.Fatalf("Linux autostart label = %q, want Path", got)
	}
	if got := autostartLocationLabel("darwin"); got != "Path" {
		t.Fatalf("Darwin autostart label = %q, want Path", got)
	}
}

func TestStatusReportsRunningForPIDRecordWithUnavailableStartTime(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "termp.pid")
	if err := os.WriteFile(pidPath, []byte(`{"pid":42,"start_time_unavailable":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	daemonPID := statusDaemonPID(
		pidPath,
		filepath.Join(t.TempDir(), "missing-discord.json"),
		time.Now(),
		func(pid int) bool { return pid == 42 },
		func(pid int) bool { return pid == 42 },
	)
	got := formatStatus(statusInfo{running: daemonPID > 0, configOK: true})
	if daemonPID != 42 || !strings.Contains(got, "  Running        yes\n") {
		t.Fatalf("status daemon pid = %d, output =\n%s\nwant pid 42 reported running", daemonPID, got)
	}
}

func TestFormatStatusGroupedAlignedAndComplete(t *testing.T) {
	homeDir := filepath.Join(string(filepath.Separator), "Users", "test")
	servicePath := filepath.Join(homeDir, "Library", "LaunchAgents", "dev.termp.plist")
	configPath := filepath.Join(homeDir, ".config", "termp", "config.toml")
	info := statusInfo{
		running:          false,
		discord:          "connected",
		detectedTool:     "claude-code",
		serviceSupported: true,
		serviceInstalled: true,
		serviceLoaded:    "false",
		serviceEnabled:   "n/a",
		servicePath:      servicePath,
		serviceMessage:   "ready",
		configPath:       configPath,
		configOK:         true,
		configWarnings:   []string{"unknown key ignored"},
		homeDir:          homeDir,
	}
	shortServicePath := "~" + strings.TrimPrefix(servicePath, homeDir)
	shortConfigPath := "~" + strings.TrimPrefix(configPath, homeDir)
	want := "termp status\n\n" +
		"Daemon\n" +
		"  Running        no\n" +
		"  Discord        connected\n" +
		"  Detected tool  claude-code\n\n" +
		"Autostart\n" +
		"  Supported  yes\n" +
		"  Installed  yes\n" +
		"  Loaded     no\n" +
		"  Enabled    —\n" +
		"  Path       " + shortServicePath + "\n" +
		"  Message    ready\n\n" +
		"Config\n" +
		"  Path     " + shortConfigPath + "\n" +
		"  Valid    yes\n" +
		"  Warning  unknown key ignored\n"
	if got := formatStatus(info); got != want {
		t.Fatalf("formatStatus() =\n%q\nwant:\n%q", got, want)
	}
}

func TestFormatStatusSanitizesExternallyDerivedText(t *testing.T) {
	got := formatStatus(statusInfo{
		detectedTool:   "safe\x1b]52;c;clipboard\x07\u200fevil",
		serviceMessage: "ready\x1b[31m",
		configOK:       true,
	})
	for _, unsafe := range []string{"\x1b", "\x07", "\u200f"} {
		if strings.Contains(got, unsafe) {
			t.Fatalf("formatStatus() retained unsafe terminal text %q:\n%s", unsafe, got)
		}
	}
	if !strings.Contains(got, "safeevil") || !strings.Contains(got, "ready") {
		t.Fatalf("formatStatus() lost safe text:\n%s", got)
	}
}

func TestRunStatusProbesFastPathUnchanged(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	wantService := service.State{
		Supported: true,
		Installed: true,
		Loaded:    "active",
		Enabled:   "enabled",
		Path:      "/tmp/termp.service",
	}
	got := runStatusProbes(ctx, statusProbeFuncs{
		discord: func(context.Context) error { return nil },
		service: func(context.Context) service.State { return wantService },
		tool: func(context.Context) (detector.Detection, error) {
			tool := registry.Tool{ID: "claude-code"}
			return detector.Detection{Tool: tool, Featured: detector.FeaturedTool{Tool: tool}}, nil
		},
	})

	if got.discord != "connected" || got.detectedTool != "claude-code" || !reflect.DeepEqual(got.service, wantService) {
		t.Fatalf("runStatusProbes() = %+v, want connected/claude-code and service %+v", got, wantService)
	}
}

func TestRunStatusProbesUsesFreshConnectedDaemonDiscordStateWithoutDirectProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	discordCalls := 0

	got := runStatusProbes(ctx, statusProbeFuncs{
		daemonRunning: true,
		daemonPID:     1234,
		now:           func() time.Time { return now },
		discordState: func(time.Time) (daemonDiscordState, bool) {
			return daemonDiscordState{
				Connected: true,
				UpdatedAt: now,
				PID:       1234,
			}, true
		},
		discord: func(context.Context) error {
			discordCalls++
			return presence.ErrDiscordIPCHandshakeTimeout
		},
		service: func(context.Context) service.State {
			return service.State{Supported: true, Loaded: "active", Enabled: "enabled"}
		},
		tool: func(context.Context) (detector.Detection, error) {
			return detector.Detection{None: true}, nil
		},
	})

	if discordCalls != 0 {
		t.Fatalf("direct Discord probe calls = %d, want 0", discordCalls)
	}
	if got.discord != "connected" {
		t.Fatalf("discord status = %q, want connected", got.discord)
	}
}

func TestRunStatusProbesFallsBackWhenFreshDaemonDiscordStateIsDisconnected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	discordCalls := 0

	got := runStatusProbes(ctx, statusProbeFuncs{
		daemonRunning: true,
		daemonPID:     1234,
		now:           func() time.Time { return now },
		discordState: func(time.Time) (daemonDiscordState, bool) {
			return daemonDiscordState{
				Connected: false,
				UpdatedAt: now,
				PID:       1234,
			}, true
		},
		discord: func(context.Context) error {
			discordCalls++
			return nil
		},
		service: func(context.Context) service.State {
			return service.State{Supported: true, Loaded: "active", Enabled: "enabled"}
		},
		tool: func(context.Context) (detector.Detection, error) {
			return detector.Detection{None: true}, nil
		},
	})

	if discordCalls != 1 {
		t.Fatalf("direct Discord probe calls = %d, want 1", discordCalls)
	}
	if got.discord != "connected" {
		t.Fatalf("discord status = %q, want direct connected result", got.discord)
	}
}

func TestRunStatusProbesFallsBackWhenDaemonStateMissingOrStale(t *testing.T) {
	tests := []struct {
		name  string
		state func(time.Time) (daemonDiscordState, bool)
	}{
		{
			name: "missing",
			state: func(time.Time) (daemonDiscordState, bool) {
				return daemonDiscordState{}, false
			},
		},
		{
			name: "stale",
			state: func(now time.Time) (daemonDiscordState, bool) {
				return readFreshDaemonDiscordState(writeDaemonDiscordStateFixture(t, daemonDiscordState{
					Connected: true,
					UpdatedAt: now.Add(-daemonDiscordStateStaleAfter - time.Nanosecond),
					PID:       1234,
				}), now, daemonDiscordStateStaleAfter)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
			discordCalls := 0

			got := runStatusProbes(ctx, statusProbeFuncs{
				daemonRunning: true,
				daemonPID:     1234,
				now:           func() time.Time { return now },
				discordState:  tt.state,
				discord: func(context.Context) error {
					discordCalls++
					return presence.ErrDiscordIPCHandshakeTimeout
				},
				service: func(context.Context) service.State {
					return service.State{Supported: true, Loaded: "active", Enabled: "enabled"}
				},
				tool: func(context.Context) (detector.Detection, error) {
					return detector.Detection{None: true}, nil
				},
			})

			if discordCalls != 1 {
				t.Fatalf("direct Discord probe calls = %d, want 1", discordCalls)
			}
			if got.discord != "not responding (Discord IPC handshake timed out)" {
				t.Fatalf("discord status = %q, want handshake-timeout mapping", got.discord)
			}
		})
	}
}

func TestRunStatusProbesIgnoresDaemonStateWhenDaemonNotRunning(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	discordCalls := 0

	got := runStatusProbes(ctx, statusProbeFuncs{
		daemonRunning: false,
		discordState: func(time.Time) (daemonDiscordState, bool) {
			return daemonDiscordState{
				Connected: true,
				UpdatedAt: time.Now(),
				PID:       1234,
			}, true
		},
		discord: func(context.Context) error {
			discordCalls++
			return presence.ErrDiscordIPCNotFound
		},
		service: func(context.Context) service.State {
			return service.State{Supported: true, Loaded: "inactive", Enabled: "enabled"}
		},
		tool: func(context.Context) (detector.Detection, error) {
			return detector.Detection{None: true}, nil
		},
	})

	if discordCalls != 1 {
		t.Fatalf("direct Discord probe calls = %d, want 1", discordCalls)
	}
	if got.discord != "not running (start Discord to show presence)" {
		t.Fatalf("discord status = %q, want direct not-running result", got.discord)
	}
}

func TestReadFreshDaemonDiscordStateAcceptsBoundaryAndRejectsOlder(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	boundaryPath := writeDaemonDiscordStateFixture(t, daemonDiscordState{
		Connected: true,
		UpdatedAt: now.Add(-daemonDiscordStateStaleAfter),
		PID:       1234,
	})
	if state, ok := readFreshDaemonDiscordState(boundaryPath, now, daemonDiscordStateStaleAfter); !ok || !state.Connected {
		t.Fatalf("boundary state = (%+v, %t), want fresh connected", state, ok)
	}

	stalePath := writeDaemonDiscordStateFixture(t, daemonDiscordState{
		Connected: true,
		UpdatedAt: now.Add(-daemonDiscordStateStaleAfter - time.Nanosecond),
		PID:       1234,
	})
	if state, ok := readFreshDaemonDiscordState(stalePath, now, daemonDiscordStateStaleAfter); ok {
		t.Fatalf("older state = (%+v, %t), want stale", state, ok)
	}
}

func TestDaemonDiscordStateConnectedTrustsConnectedPublisherDespitePIDMismatch(t *testing.T) {
	if !daemonDiscordStateConnected(42, daemonDiscordState{Connected: true, PID: 42}) {
		t.Fatal("matching connected daemon state was rejected")
	}
	if daemonDiscordStateConnected(42, daemonDiscordState{Connected: false, PID: 42}) {
		t.Fatal("disconnected daemon state was accepted")
	}
	if !daemonDiscordStateConnected(42, daemonDiscordState{Connected: true, PID: 43}) {
		t.Fatal("truthful state from orphaned publisher was rejected")
	}
}

func TestRunStatusProbesReportsPublisherPIDMismatchWithoutDirectProbe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	probeCalls := 0
	got := runStatusProbes(ctx, statusProbeFuncs{
		daemonRunning: true,
		daemonPID:     2222,
		discordState: func(time.Time) (daemonDiscordState, bool) {
			return daemonDiscordState{Connected: true, PID: 1111}, true
		},
		discord: func(context.Context) error {
			probeCalls++
			return presence.ErrDiscordIPCHandshakeTimeout
		},
		service: func(context.Context) service.State { return service.State{} },
		tool: func(context.Context) (detector.Detection, error) {
			return detector.Detection{None: true}, nil
		},
	})
	if probeCalls != 0 {
		t.Fatalf("direct Discord probe calls = %d, want 0", probeCalls)
	}
	if !strings.Contains(got.discord, "another termp daemon owns Discord") ||
		!strings.Contains(got.discord, "pid 1111") || !strings.Contains(got.discord, "names 2222") {
		t.Fatalf("discord status = %q, want truthful PID mismatch", got.discord)
	}
}

func TestDiscordConnectedFromStateOrProbeUsesFreshDaemonConnection(t *testing.T) {
	probes := 0
	connected := discordConnectedFromStateOrProbe(42, daemonDiscordState{Connected: true, PID: 42}, true, func() error {
		probes++
		return errors.New("handshake timed out")
	})
	if !connected {
		t.Fatal("fresh daemon connection was reported disconnected")
	}
	if probes != 0 {
		t.Fatalf("direct probe calls = %d, want 0", probes)
	}
}

func TestWriteDaemonDiscordStateUses0600(t *testing.T) {
	path := filepath.Join(t.TempDir(), "discord.json")
	state := daemonDiscordState{
		Connected: true,
		UpdatedAt: time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC),
		PID:       1234,
	}
	if err := writeDaemonDiscordState(path, state); err != nil {
		t.Fatal(err)
	}
	read, ok := readFreshDaemonDiscordState(path, state.UpdatedAt, daemonDiscordStateStaleAfter)
	if !ok || read != state {
		t.Fatalf("read state = (%+v, %t), want %+v, true", read, ok, state)
	}
	assertPIDFileMode(t, path)
}

func writeDaemonDiscordStateFixture(t *testing.T, state daemonDiscordState) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "discord.json")
	if err := writeDaemonDiscordState(path, state); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunStatusProbesDiscordStateMapping(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "endpoint absent",
			err:  presence.ErrDiscordIPCNotFound,
			want: "not running (start Discord to show presence)",
		},
		{
			name: "endpoint present connect failure",
			err:  presence.ErrDiscordIPCUnreachable,
			want: "connection failed (Discord is running but unreachable)",
		},
		{
			name: "handshake timeout",
			err:  presence.ErrDiscordIPCHandshakeTimeout,
			want: "not responding (Discord IPC handshake timed out)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			got := runStatusProbes(ctx, statusProbeFuncs{
				discord: func(context.Context) error { return tt.err },
				service: func(context.Context) service.State {
					return service.State{Supported: true, Loaded: "unknown", Enabled: "unknown"}
				},
				tool: func(context.Context) (detector.Detection, error) {
					return detector.Detection{None: true}, nil
				},
			})

			if got.discord != tt.want {
				t.Fatalf("discord status = %q, want %q", got.discord, tt.want)
			}
		})
	}
}

func TestRunStatusProbesHonorsOverallDeadline(t *testing.T) {
	const budget = 40 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	wait := func(ctx context.Context) {
		<-ctx.Done()
	}

	started := time.Now()
	got := runStatusProbes(ctx, statusProbeFuncs{
		discord: func(ctx context.Context) error {
			wait(ctx)
			return ctx.Err()
		},
		service: func(ctx context.Context) service.State {
			wait(ctx)
			return service.State{Supported: true, Loaded: "unknown", Enabled: "unknown"}
		},
		tool: func(ctx context.Context) (detector.Detection, error) {
			wait(ctx)
			return detector.Detection{}, ctx.Err()
		},
	})
	elapsed := time.Since(started)

	if elapsed < budget/2 || elapsed > 250*time.Millisecond {
		t.Fatalf("runStatusProbes() elapsed = %v, want approximately %v overall budget", elapsed, budget)
	}
	if got.discord != "unknown (probe timed out)" || got.detectedTool != "unknown (probe timed out)" {
		t.Fatalf("deadline results = %+v, want timed-out stages reported unknown", got)
	}
}

func TestRunStatusProbesDoesNotWaitForHungProbe(t *testing.T) {
	const budget = 40 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	release := make(chan struct{})

	started := time.Now()
	got := runStatusProbes(ctx, statusProbeFuncs{
		discord: func(context.Context) error {
			<-release
			return nil
		},
		service: func(context.Context) service.State {
			return service.State{Supported: true, Installed: true, Loaded: "active", Enabled: "enabled"}
		},
		tool: func(context.Context) (detector.Detection, error) {
			return detector.Detection{None: true}, nil
		},
	})
	close(release)

	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("runStatusProbes() waited %v for hung fake, budget was %v", elapsed, budget)
	}
	if got.discord != "unknown (probe timed out)" || got.detectedTool != "none" ||
		!got.service.Installed || got.service.Loaded != "active" {
		t.Fatalf("hung-probe results = %+v, want only Discord unavailable", got)
	}
}

func TestUpdateNoticeHasNoANSIWithoutColorSupport(t *testing.T) {
	result := updatepkg.Result{
		Current:  "1.0.0",
		Latest:   "1.1.0",
		Method:   updatepkg.InstallHomebrew,
		Guidance: updatepkg.Guidance{Text: updatepkg.BrewCommand, Runnable: true},
	}
	for _, renderer := range []*lipgloss.Renderer{nil, newInstallRenderer(os.Stdout, true, true)} {
		got := formatUpdateNotice(result, renderer, 80)
		if strings.Contains(got, "\x1b") {
			t.Fatalf("plain update notice contains ANSI: %q", got)
		}
	}
}

func TestUpdateNoticeUsesColorWhenSupported(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	renderer := lipgloss.NewRenderer(output)
	renderer.SetColorProfile(termenv.ANSI256)
	result := updatepkg.Result{
		Current:  "1.0.0",
		Latest:   "1.1.0",
		Method:   updatepkg.InstallHomebrew,
		Guidance: updatepkg.Guidance{Text: updatepkg.BrewCommand, Runnable: true},
	}
	if got := formatUpdateNotice(result, renderer, 80); !strings.Contains(got, "\x1b") {
		t.Fatalf("color update notice contains no ANSI: %q", got)
	}
}

func TestUpdateNoticeLinesStayWithinOutputWidth(t *testing.T) {
	methods := []struct {
		method  updatepkg.InstallMethod
		command string
	}{
		{method: updatepkg.InstallHomebrew, command: updatepkg.BrewCommand},
		{method: updatepkg.InstallGo, command: updatepkg.GoCommand("v12.34.56")},
		{method: updatepkg.InstallGeneric, command: updatepkg.GenericCommand("v12.34.56")},
	}
	for _, width := range []int{20, 40, 80, 120} {
		for _, tt := range methods {
			result := updatepkg.Result{
				Current:  "1.0.0+abc123",
				Latest:   "v12.34.56+def456",
				Method:   tt.method,
				Guidance: updatepkg.Guidance{Text: tt.command, Runnable: true},
			}
			got := formatUpdateNotice(result, nil, width)
			maxWidth := min(max(width, 20), maxInstallCTAWidth)
			for lineNumber, line := range strings.Split(got, "\n") {
				if lineWidth := lipgloss.Width(line); lineWidth > maxWidth {
					t.Fatalf("width %d line %d is %d columns: %q", width, lineNumber+1, lineWidth, line)
				}
			}
		}
	}
}

func TestUpdateNoticeUsesMethodSpecificGuidance(t *testing.T) {
	tests := []struct {
		name   string
		result updatepkg.Result
		want   string
	}{
		{
			name: "generic",
			result: updatepkg.Result{
				Current:  "v0.1.0",
				Latest:   "v0.1.1",
				Method:   updatepkg.InstallGeneric,
				Guidance: updatepkg.Guidance{Text: updatepkg.GenericCommand("v0.1.1"), Runnable: true},
			},
			want: "Update available: v0.1.0 -> v0.1.1\n\nRun:\n  termp update\n",
		},
		{
			name: "homebrew",
			result: updatepkg.Result{
				Current:  "v0.1.0",
				Latest:   "v0.1.1",
				Method:   updatepkg.InstallHomebrew,
				Guidance: updatepkg.Guidance{Text: updatepkg.BrewCommand, Runnable: true},
			},
			want: "Update available: v0.1.0 -> v0.1.1\n\nRun:\n  " + updatepkg.BrewCommand + "\n",
		},
		{
			name: "go",
			result: updatepkg.Result{
				Current:  "v0.1.0",
				Latest:   "v0.1.1",
				Method:   updatepkg.InstallGo,
				Guidance: updatepkg.Guidance{Text: updatepkg.GoCommand("v0.1.1"), Runnable: true},
			},
			want: "Update available: v0.1.0 -> v0.1.1\n\nRun:\n  " + updatepkg.GoCommand("v0.1.1") + "\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatUpdateNotice(tt.result, nil, maxInstallCTAWidth); got != tt.want {
				t.Fatalf("notice = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUpdateNoticeLabelsSystemPackageGuidanceAsNonRunnable(t *testing.T) {
	for _, method := range []updatepkg.InstallMethod{
		updatepkg.InstallDebian,
		updatepkg.InstallRPM,
		updatepkg.InstallSystemPackage,
	} {
		t.Run(string(method), func(t *testing.T) {
			guidance := updatepkg.GuidanceForMethod(method, "v0.1.1")
			result := updatepkg.Result{
				Current:  "v0.1.0",
				Latest:   "v0.1.1",
				Method:   method,
				Guidance: guidance,
			}
			got := formatUpdateNotice(result, nil, maxInstallCTAWidth)
			if !strings.Contains(got, "\nTo update:\n") || strings.Contains(got, "\nRun:\n") {
				t.Fatalf("package notice has incorrect label:\n%s", got)
			}
			unwrapped := strings.ReplaceAll(got, " \\\n  ", " ")
			unwrapped = strings.ReplaceAll(unwrapped, "\\\n  ", "")
			for _, command := range strings.Split(guidance.Text, "\n") {
				if command == "" || strings.HasSuffix(command, ":") {
					continue
				}
				if !strings.Contains(unwrapped, command) {
					t.Fatalf("package notice missing copy-pasteable command %q:\n%s", command, got)
				}
			}
		})
	}
}

func TestWrappedUpdateCommandsRemainCopyPasteable(t *testing.T) {
	for _, command := range []string{updatepkg.BrewCommand, updatepkg.GoCommand("v1.1.0"), updatepkg.GenericCommand("v1.1.0")} {
		for _, width := range []int{20, 40, 80} {
			wrapped := strings.Join(wrapShellCommand(command, width), "\n")
			if got := strings.ReplaceAll(wrapped, "\\\n", ""); got != command {
				t.Fatalf("width %d unwrapped command = %q, want %q", width, got, command)
			}
		}
	}
}

func TestCommandUpdateAlertUsesCacheWithoutNetwork(t *testing.T) {
	oldChecker, oldVersion := releaseChecker, version
	t.Cleanup(func() {
		releaseChecker, version = oldChecker, oldVersion
	})
	_ = os.Unsetenv("NO_UPDATE_CHECK")
	t.Cleanup(func() { _ = os.Unsetenv("NO_UPDATE_CHECK") })

	cachePath := filepath.Join(t.TempDir(), "update-check.json")
	data, err := json.Marshal(map[string]any{
		"checked_at":     time.Now(),
		"latest_version": "v1.2.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	source := &failingReleaseSource{}
	releaseChecker = updatepkg.NewChecker(source, cachePath)
	version = "1.0.0"
	cfg := config.Default()
	cfg.UpdateCheck = true

	var stderr bytes.Buffer
	printCommandUpdateAlert("start", nil, true, cfg, nil, &stderr)
	want := "A new version (v1.2.0) is available — run `termp update`\n"
	if got := stderr.String(); got != want {
		t.Fatalf("alert = %q, want %q", got, want)
	}
	if source.calls != 0 {
		t.Fatalf("cached alert made %d network calls", source.calls)
	}
}

func TestCommandUpdateAlertSuppressed(t *testing.T) {
	oldChecker, oldVersion := releaseChecker, version
	t.Cleanup(func() {
		releaseChecker, version = oldChecker, oldVersion
		_ = os.Unsetenv("NO_UPDATE_CHECK")
	})
	cachePath := filepath.Join(t.TempDir(), "update-check.json")
	data, err := json.Marshal(map[string]any{
		"checked_at":     time.Now(),
		"latest_version": "v2.0.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	releaseChecker = updatepkg.NewChecker(&failingReleaseSource{}, cachePath)

	tests := []struct {
		name    string
		command string
		args    []string
		enabled bool
		current string
		loadErr error
		envSet  bool
	}{
		{name: "update", command: "update", enabled: true, current: "1.0.0"},
		{name: "version", command: "version", enabled: true, current: "1.0.0"},
		{name: "status", command: "status", enabled: true, current: "1.0.0"},
		{name: "completion", command: "completion", enabled: true, current: "1.0.0"},
		{name: "config", command: "config", enabled: true, current: "1.0.0"},
		{name: "watch once", command: "watch", args: []string{"--once"}, enabled: true, current: "1.0.0"},
		{name: "disabled config", command: "start", enabled: false, current: "1.0.0"},
		{name: "automatic updates", command: "start", enabled: true, current: "1.0.0"},
		{name: "environment", command: "start", enabled: true, current: "1.0.0", envSet: true},
		{name: "dev build", command: "start", enabled: true, current: "dev"},
		{name: "config error", command: "start", enabled: true, current: "1.0.0", loadErr: errors.New("bad config")},
		{name: "unknown command", command: "nope", enabled: true, current: "1.0.0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Unsetenv("NO_UPDATE_CHECK")
			if tt.envSet {
				t.Setenv("NO_UPDATE_CHECK", "")
			}
			version = tt.current
			cfg := config.Default()
			cfg.UpdateCheck = tt.enabled
			cfg.AutoUpdate = tt.name == "automatic updates"
			var stderr bytes.Buffer
			printCommandUpdateAlert(tt.command, tt.args, true, cfg, tt.loadErr, &stderr)
			if got := stderr.String(); got != "" {
				t.Fatalf("suppressed alert = %q", got)
			}
		})
	}
}

func TestAutomaticUpdateDisabledDoesNothing(t *testing.T) {
	source := &failingReleaseSource{}
	checker := updatepkg.NewChecker(source, filepath.Join(t.TempDir(), "update-check.json"))
	runner := &recordingUpdateRunner{}
	runAutomaticUpdate(context.Background(), config.Default(), "1.0.0", checker, runner)
	if source.calls != 0 || runner.calls != 0 {
		t.Fatalf("disabled automatic update used source %d times and runner %d times", source.calls, runner.calls)
	}
}

func TestAutomaticGenericWindowsUpdateRecordsLimitation(t *testing.T) {
	_ = os.Unsetenv("NO_UPDATE_CHECK")
	statePath := filepath.Join(t.TempDir(), "update-check.json")
	source := &staticReleaseSource{latest: "v1.1.0"}
	checker := updatepkg.NewChecker(source, statePath)
	checker.DetectInstall = func() updatepkg.InstallMethod { return updatepkg.InstallGeneric }
	runner := &recordingUpdateRunner{}
	cfg := config.Default()
	cfg.AutoUpdate = true

	runAutomaticUpdateWithStatePathForPlatform(context.Background(), cfg, "1.0.0", checker, runner, statePath, "windows")

	if source.calls != 1 || runner.calls != 0 {
		t.Fatalf("generic Windows automatic update used source %d times and runner %d times, want 1 and 0", source.calls, runner.calls)
	}
	attempt, ok := updatepkg.ReadAutomaticUpdateAttempt(statePath)
	if !ok || !attempt.Skipped || attempt.Target != "v1.1.0" {
		t.Fatalf("recorded attempt = (%+v, %t), want skipped v1.1.0 attempt", attempt, ok)
	}
	for _, want := range []string{"generic automatic updates are not supported on Windows", "run `termp update`"} {
		if !strings.Contains(attempt.Error, want) {
			t.Fatalf("recorded skip %q missing %q", attempt.Error, want)
		}
	}
	status := automaticUpdateStatus(statePath, true, "windows", updatepkg.InstallGeneric)
	for _, want := range []string{"skipped for v1.1.0", "not supported on Windows", "run `termp update`"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status update reason %q missing %q", status, want)
		}
	}
}

func TestAutomaticUpdateStatusReportsGenericWindowsLimitationBeforeAttempt(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "missing-update-check.json")
	status := automaticUpdateStatus(statePath, true, "windows", updatepkg.InstallGeneric)
	for _, want := range []string{"skipped:", "not supported on Windows", "run `termp update`"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status update reason %q missing %q", status, want)
		}
	}
	rendered := formatStatus(statusInfo{updateFailure: status})
	if !strings.Contains(rendered, "Updates\n  Automatic  "+status+"\n") {
		t.Fatalf("status did not report generic Windows automatic-update limitation:\n%s", rendered)
	}
	if got := automaticUpdateStatus(statePath, false, "windows", updatepkg.InstallGeneric); got != "" {
		t.Fatalf("disabled automatic update status = %q, want empty", got)
	}
}

func TestAutomaticUpdatePlatformPreflightOnlyRejectsGenericWindows(t *testing.T) {
	for _, tt := range []struct {
		name   string
		goos   string
		method updatepkg.InstallMethod
		want   bool
	}{
		{name: "generic Windows", goos: "windows", method: updatepkg.InstallGeneric, want: true},
		{name: "Go Windows", goos: "windows", method: updatepkg.InstallGo},
		{name: "Homebrew Windows", goos: "windows", method: updatepkg.InstallHomebrew},
		{name: "generic Linux", goos: "linux", method: updatepkg.InstallGeneric},
		{name: "generic macOS", goos: "darwin", method: updatepkg.InstallGeneric},
		{name: "Debian package", goos: "linux", method: updatepkg.InstallDebian, want: true},
		{name: "RPM package", goos: "linux", method: updatepkg.InstallRPM, want: true},
		{name: "ambiguous system package", goos: "linux", method: updatepkg.InstallSystemPackage, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := automaticUpdatePlatformPreflight(tt.goos, tt.method) != nil; got != tt.want {
				t.Fatalf("automaticUpdatePlatformPreflight(%q, %q) rejected = %t, want %t", tt.goos, tt.method, got, tt.want)
			}
		})
	}
}

func TestAutomaticManagedWindowsUpdatesRemainEnabled(t *testing.T) {
	for _, method := range []updatepkg.InstallMethod{updatepkg.InstallGo, updatepkg.InstallHomebrew} {
		t.Run(string(method), func(t *testing.T) {
			_ = os.Unsetenv("NO_UPDATE_CHECK")
			statePath := filepath.Join(t.TempDir(), "update-check.json")
			checker := updatepkg.NewChecker(&staticReleaseSource{latest: "v1.1.0"}, statePath)
			checker.DetectInstall = func() updatepkg.InstallMethod { return method }
			runner := &recordingUpdateRunner{}
			cfg := config.Default()
			cfg.AutoUpdate = true

			runAutomaticUpdateWithStatePathForPlatform(context.Background(), cfg, "1.0.0", checker, runner, statePath, "windows")

			if runner.calls != 1 {
				t.Fatalf("%s Windows automatic update invoked runner %d times, want 1", method, runner.calls)
			}
			attempt, ok := updatepkg.ReadAutomaticUpdateAttempt(statePath)
			if !ok || attempt.Skipped || attempt.Error != "" {
				t.Fatalf("%s Windows automatic update attempt = (%+v, %t), want success", method, attempt, ok)
			}
		})
	}
}

func TestAutomaticUpdateRunsInstallAwareUpdater(t *testing.T) {
	for _, tt := range []struct {
		name   string
		method updatepkg.InstallMethod
		calls  int
		want   string
	}{
		{name: "generic", method: updatepkg.InstallGeneric, calls: 2, want: "sh"},
		{name: "homebrew", method: updatepkg.InstallHomebrew, calls: 1, want: "brew"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Unsetenv("NO_UPDATE_CHECK")
			if tt.method == updatepkg.InstallGeneric {
				t.Setenv("BINDIR", t.TempDir())
			}
			source := &staticReleaseSource{latest: "v1.1.0"}
			checker := updatepkg.NewChecker(source, filepath.Join(t.TempDir(), "update-check.json"))
			checker.DetectInstall = func() updatepkg.InstallMethod { return tt.method }
			runner := &recordingUpdateRunner{}
			cfg := config.Default()
			cfg.AutoUpdate = true
			runAutomaticUpdate(context.Background(), cfg, "1.0.0", checker, runner)
			if tt.method == updatepkg.InstallGeneric && runtime.GOOS == "windows" {
				if source.calls != 1 || runner.calls != 0 {
					t.Fatalf("source calls = %d, runner calls = %d, want (1, 0) for unsupported Windows generic update", source.calls, runner.calls)
				}
				return
			}
			if source.calls != 1 || runner.calls != tt.calls || runner.command.Name != tt.want {
				t.Fatalf("source calls = %d, runner = (%d, %#v), want (1, %d calls ending in %s)", source.calls, runner.calls, runner.command, tt.calls, tt.want)
			}
		})
	}
}

func TestAutomaticUpdateFailuresDoNotEscape(t *testing.T) {
	tests := []struct {
		name   string
		source updatepkg.ReleaseSource
		runner *recordingUpdateRunner
	}{
		{name: "check", source: &failingReleaseSource{}, runner: &recordingUpdateRunner{}},
		{name: "update", source: &staticReleaseSource{latest: "1.1.0"}, runner: &recordingUpdateRunner{err: errors.New("exec failed")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Unsetenv("NO_UPDATE_CHECK")
			checker := updatepkg.NewChecker(tt.source, filepath.Join(t.TempDir(), "update-check.json"))
			checker.DetectInstall = func() updatepkg.InstallMethod { return updatepkg.InstallGeneric }
			t.Setenv("BINDIR", t.TempDir())
			cfg := config.Default()
			cfg.AutoUpdate = true
			runAutomaticUpdate(context.Background(), cfg, "1.0.0", checker, tt.runner)
			if tt.name == "check" && tt.runner.calls != 0 {
				t.Fatal("failed check ran updater")
			}
			if tt.name == "update" {
				if runtime.GOOS == "windows" && tt.runner.calls != 0 {
					t.Fatalf("unsupported Windows generic update invoked runner %d times", tt.runner.calls)
				}
				if runtime.GOOS != "windows" && tt.runner.calls != 1 {
					t.Fatal("failed updater was not invoked")
				}
			}
		})
	}
}

func TestAutomaticUpdateFailureIsReportedAndLaterSuccessClearsIt(t *testing.T) {
	_ = os.Unsetenv("NO_UPDATE_CHECK")
	statePath := filepath.Join(t.TempDir(), "update-check.json")
	source := &staticReleaseSource{latest: "v1.1.0"}
	checker := updatepkg.NewChecker(source, statePath)
	checker.DetectInstall = func() updatepkg.InstallMethod { return updatepkg.InstallGo }
	cfg := config.Default()
	cfg.AutoUpdate = true

	runner := &recordingUpdateRunner{err: errors.New("permission denied")}
	runAutomaticUpdateWithStatePath(context.Background(), cfg, "1.0.0", checker, runner, statePath)

	failure := automaticUpdateFailure(statePath)
	for _, want := range []string{"failed for v1.1.0", "permission denied", "run `termp update` manually"} {
		if !strings.Contains(failure, want) {
			t.Fatalf("automatic update failure %q missing %q", failure, want)
		}
	}
	statusOutput := formatStatus(statusInfo{updateFailure: failure})
	if !strings.Contains(statusOutput, "Updates\n  Automatic  "+failure+"\n") {
		t.Fatalf("status did not report automatic update failure:\n%s", statusOutput)
	}

	runner.err = nil
	runAutomaticUpdateWithStatePath(context.Background(), cfg, "1.0.0", checker, runner, statePath)
	if failure := automaticUpdateFailure(statePath); failure != "" {
		t.Fatalf("successful automatic update left stale failure %q", failure)
	}
	attempt, ok := updatepkg.ReadAutomaticUpdateAttempt(statePath)
	if !ok || attempt.Target != "v1.1.0" || attempt.Error != "" {
		t.Fatalf("successful automatic update attempt = (%+v, %t), want cleared v1.1.0 attempt", attempt, ok)
	}
}

func TestInteractiveOnlyAlertsAreSuppressedForScriptStyleInvocation(t *testing.T) {
	for _, command := range []string{"settings", "setup", "watch"} {
		if eligibleForUpdateAlert(command, nil, false) {
			t.Fatalf("%s eligible without an interactive terminal", command)
		}
	}
}

func TestRunUpdateSelectsInstallMethodCommand(t *testing.T) {
	tests := []struct {
		method updatepkg.InstallMethod
		want   updatepkg.Command
	}{
		{method: updatepkg.InstallHomebrew, want: updatepkg.Command{Name: "brew", Args: []string{"upgrade", "polter-dev/tap/termp"}}},
		{method: updatepkg.InstallGo, want: updatepkg.Command{Name: "go", Args: []string{"install", "github.com/polter-dev/discord_terminal_presence/cmd/termp@v1.1.0"}}},
		{method: updatepkg.InstallGeneric, want: updatepkg.Command{Name: "sh", Args: []string{"-c", updatepkg.GenericCommand("v1.1.0")}}},
	}
	for _, tt := range tests {
		t.Run(string(tt.method), func(t *testing.T) {
			runner := &recordingUpdateRunner{}
			checker := stubLatestChecker{result: updatepkg.Result{Current: "1.0.0", Latest: "v1.1.0", Method: tt.method}}
			err := runUpdate(context.Background(), context.Background(), "1.0.0", checker, runner, nil, io.Discard, io.Discard)
			if tt.method == updatepkg.InstallGeneric && runtime.GOOS == "windows" {
				if err == nil || !strings.Contains(err.Error(), "not supported on Windows") ||
					!strings.Contains(err.Error(), "go install") {
					t.Fatalf("Windows generic update error = %v, want supported-path guidance", err)
				}
				if runner.calls != 0 {
					t.Fatalf("unsupported Windows generic update invoked runner %d times", runner.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			wantCalls := 1
			if tt.method == updatepkg.InstallGeneric {
				wantCalls = 2
				tt.want = updatepkg.Command{Name: "sh"}
			}
			if runner.calls != wantCalls || runner.command.Name != tt.want.Name ||
				tt.method != updatepkg.InstallGeneric && strings.Join(runner.command.Args, "\x00") != strings.Join(tt.want.Args, "\x00") {
				t.Fatalf("runner = (%d, %#v), want (%d, %#v)", runner.calls, runner.command, wantCalls, tt.want)
			}
		})
	}
}

func TestRunUpdateFallsBackToSystemPackageGuidance(t *testing.T) {
	tests := []struct {
		method updatepkg.InstallMethod
		want   string
	}{
		{method: updatepkg.InstallDebian, want: updatepkg.GuidanceForMethod(updatepkg.InstallDebian, "v1.1.0").Text},
		{method: updatepkg.InstallRPM, want: updatepkg.GuidanceForMethod(updatepkg.InstallRPM, "v1.1.0").Text},
		{method: updatepkg.InstallSystemPackage, want: updatepkg.GuidanceForMethod(updatepkg.InstallSystemPackage, "v1.1.0").Text},
	}
	for _, tt := range tests {
		t.Run(string(tt.method), func(t *testing.T) {
			checker := stubLatestChecker{result: updatepkg.Result{
				Current: "1.0.0",
				Latest:  "v1.1.0",
				Method:  tt.method,
			}}
			runner := &recordingUpdateRunner{err: errors.New("sudo unavailable")}
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if err := runUpdate(context.Background(), context.Background(), "1.0.0", checker, runner, nil, &stdout, &stderr); err != nil {
				t.Fatal(err)
			}
			wantCalls := 1
			if tt.method == updatepkg.InstallSystemPackage || runtime.GOOS != "linux" {
				wantCalls = 0
			}
			if runner.calls != wantCalls {
				t.Fatalf("package-managed fallback ran %d commands, want %d", runner.calls, wantCalls)
			}
			for _, want := range []string{"managed by your system package manager", "To update:", tt.want} {
				if !strings.Contains(stdout.String(), want) {
					t.Fatalf("update guidance %q missing %q", stdout.String(), want)
				}
			}
			if strings.Contains(stdout.String(), "\nRun:") {
				t.Fatalf("package guidance printed beneath Run label:\n%s", stdout.String())
			}
			if !strings.Contains(stderr.String(), "automatic package update unavailable") {
				t.Fatalf("package fallback error = %q, want clear reason", stderr.String())
			}
		})
	}
}

func TestAutomaticSystemPackageUpdateIsSkippedWithoutInstalling(t *testing.T) {
	for _, method := range []updatepkg.InstallMethod{
		updatepkg.InstallDebian,
		updatepkg.InstallRPM,
		updatepkg.InstallSystemPackage,
	} {
		t.Run(string(method), func(t *testing.T) {
			_ = os.Unsetenv("NO_UPDATE_CHECK")
			statePath := filepath.Join(t.TempDir(), "update-check.json")
			checker := updatepkg.NewChecker(&staticReleaseSource{latest: "v1.1.0"}, statePath)
			checker.DetectInstall = func() updatepkg.InstallMethod { return method }
			runner := &recordingUpdateRunner{}
			cfg := config.Default()
			cfg.AutoUpdate = true

			runAutomaticUpdateWithStatePath(context.Background(), cfg, "1.0.0", checker, runner, statePath)

			if runner.calls != 0 {
				t.Fatalf("package-managed automatic update ran %d commands, want none", runner.calls)
			}
			attempt, ok := updatepkg.ReadAutomaticUpdateAttempt(statePath)
			if !ok || !attempt.Skipped || !strings.Contains(attempt.Error, updatepkg.GuidanceForMethod(method, "v1.1.0").Text) {
				t.Fatalf("automatic attempt = (%+v, %t), want managed-package skip", attempt, ok)
			}
			status := automaticUpdateStatus(statePath, true, "linux", method)
			if !strings.Contains(status, "skipped for v1.1.0") || !strings.Contains(status, updatepkg.GuidanceForMethod(method, "v1.1.0").Text) {
				t.Fatalf("automatic package status = %q, want recorded release-package guidance", status)
			}
		})
	}
}

func TestRunUpdateFailurePrintsMethodRetryCommand(t *testing.T) {
	tests := []struct {
		method updatepkg.InstallMethod
		want   string
	}{
		{method: updatepkg.InstallHomebrew, want: updatepkg.BrewCommand},
		{method: updatepkg.InstallGo, want: updatepkg.GoCommand("v1.1.0")},
		{method: updatepkg.InstallGeneric, want: "update failed; resolve the error above, then retry: termp update"},
	}
	for _, tt := range tests {
		t.Run(string(tt.method), func(t *testing.T) {
			runner := &recordingUpdateRunner{err: errors.New("simulated update failure")}
			checker := stubLatestChecker{result: updatepkg.Result{
				Current: "1.0.0",
				Latest:  "v1.1.0",
				Method:  tt.method,
			}}
			var stderr bytes.Buffer
			err := runUpdate(
				context.Background(),
				context.Background(),
				"1.0.0",
				checker,
				runner,
				nil,
				io.Discard,
				&stderr,
			)
			if tt.method == updatepkg.InstallGeneric && runtime.GOOS == "windows" {
				if err == nil || !strings.Contains(err.Error(), "generic self-update is not supported on Windows") ||
					!strings.Contains(err.Error(), updatepkg.GoCommand("v1.1.0")) ||
					!strings.Contains(err.Error(), "install the release archive manually") {
					t.Fatalf("Windows generic update error = %v, want unsupported-method recovery guidance", err)
				}
				if runner.calls != 0 {
					t.Fatalf("unsupported Windows generic update invoked runner %d times", runner.calls)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "simulated update failure") {
				t.Fatalf("runUpdate() error = %v, want simulated failure", err)
			}
			want := "termp update: retry with: " + tt.want + "\n"
			if tt.method == updatepkg.InstallGeneric {
				want = "termp update: " + tt.want + "\n"
			}
			if got := stderr.String(); got != want {
				t.Fatalf("retry output = %q, want %q", got, want)
			}
		})
	}
}

func TestRunUpdateAlreadyLatest(t *testing.T) {
	runner := &recordingUpdateRunner{}
	checker := stubLatestChecker{result: updatepkg.Result{Current: "1.2.0", Latest: "v1.2.0", Method: updatepkg.InstallGo}}
	var stdout bytes.Buffer
	if err := runUpdate(context.Background(), context.Background(), "1.2.0", checker, runner, nil, &stdout, io.Discard); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "You're already on the latest version (v1.2.0).\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if runner.calls != 0 {
		t.Fatalf("already-latest ran updater %d times", runner.calls)
	}
}

func TestRunUpdateCheckFailureDoesNotRunUpdater(t *testing.T) {
	runner := &recordingUpdateRunner{}
	checker := stubLatestChecker{err: errors.New("offline")}
	err := runUpdate(context.Background(), context.Background(), "1.2.0", checker, runner, nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "unable to check for updates") || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("error = %v, want clear offline check failure", err)
	}
	if runner.calls != 0 {
		t.Fatalf("failed check ran updater %d times", runner.calls)
	}
}

func TestParseRootVersionFlag(t *testing.T) {
	oldVerbose := verbose
	t.Cleanup(func() { verbose = oldVerbose })

	_, _, showVersion, err := parseRoot([]string{"--version"})
	if err != nil {
		t.Fatal(err)
	}
	if !showVersion {
		t.Fatal("showVersion = false, want true")
	}
}

func TestParseRootVerboseFlag(t *testing.T) {
	oldVerbose := verbose
	t.Cleanup(func() { verbose = oldVerbose })

	command, args, showVersion, err := parseRoot([]string{"--verbose", "start", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if command != "start" {
		t.Fatalf("command = %q, want start", command)
	}
	if showVersion {
		t.Fatal("showVersion = true, want false")
	}
	if !verbose {
		t.Fatal("verbose = false, want true")
	}
	if len(args) != 1 || args[0] != "--dry-run" {
		t.Fatalf("args = %#v, want --dry-run", args)
	}
}

func TestSubcommandHelpReturnsSuccess(t *testing.T) {
	tests := make([]struct {
		name    string
		command string
		args    []string
	}, 0, len(commandHelp)+1)
	for _, command := range commandHelp {
		if command.name == "connect" && !connectSupported {
			continue
		}
		tests = append(tests, struct {
			name    string
			command string
			args    []string
		}{name: command.name, command: command.name, args: []string{"--help"}})
	}
	tests = append(tests, struct {
		name    string
		command string
		args    []string
	}{name: "config init", command: "config", args: []string{"init", "--help"}})

	oldStderr := os.Stderr
	stderr, err := os.CreateTemp(t.TempDir(), "help-output")
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = stderr
	t.Cleanup(func() {
		os.Stderr = oldStderr
		stderr.Close()
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := dispatchCommand(tt.command, tt.args); err != nil {
				t.Fatalf("dispatchCommand(%q, %q) = %v, want successful help", tt.command, tt.args, err)
			}
		})
	}

	for _, tt := range []struct {
		command string
		args    []string
	}{
		{command: "watch", args: []string{"--unknown"}},
		{command: "config", args: []string{"unknown"}},
	} {
		if err := dispatchCommand(tt.command, tt.args); err == nil {
			t.Fatalf("dispatchCommand(%q, %q) accepted invalid arguments", tt.command, tt.args)
		}
	}
}

func TestAutostartSubcommandMatchesLegacyDispatch(t *testing.T) {
	for _, action := range []string{"enable", "disable", "status", "install", "uninstall"} {
		t.Run(action, func(t *testing.T) {
			var calls [][]string
			handlers := map[string]autostartActionHandler{
				action: func(args []string) error {
					calls = append(calls, append([]string(nil), args...))
					return nil
				},
			}
			wantArgs := []string{"--sentinel"}
			if err := dispatchCommandWithAutostartHandlers(action, wantArgs, handlers); err != nil {
				t.Fatalf("legacy %s dispatch: %v", action, err)
			}
			if err := dispatchCommandWithAutostartHandlers("autostart", append([]string{action}, wantArgs...), handlers); err != nil {
				t.Fatalf("grouped %s dispatch: %v", action, err)
			}
			if len(calls) != 2 || !reflect.DeepEqual(calls[0], wantArgs) || !reflect.DeepEqual(calls[1], wantArgs) {
				t.Fatalf("handler calls = %#v, want two calls with %#v", calls, wantArgs)
			}
		})
	}
}

func TestUsageListsEveryCommandWithDescription(t *testing.T) {
	var buf bytes.Buffer
	usage(&buf)
	got := buf.String()

	if !strings.Contains(got, "Terminal Presence (termp)") {
		t.Fatalf("usage missing product name:\n%s", got)
	}
	for _, command := range expectedCommands {
		prefix := "  " + command + strings.Repeat(" ", 12-len(command))
		description := ""
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, prefix) {
				description = strings.TrimSpace(strings.TrimPrefix(line, prefix))
				break
			}
		}
		if description == "" {
			t.Fatalf("usage missing non-empty description for %q:\n%s", command, got)
		}
	}
	for lineNumber, line := range strings.Split(got, "\n") {
		if len(line) > 80 {
			t.Fatalf("usage line %d is %d columns: %q", lineNumber+1, len(line), line)
		}
	}
}

func TestConnectAdvertisingMatchesPlatformSupport(t *testing.T) {
	advertised := slices.Contains(commandNames(), "connect")
	if advertised != connectSupported {
		t.Fatalf("command list advertises connect = %t, connectSupported = %t", advertised, connectSupported)
	}

	var help bytes.Buffer
	usage(&help)
	if strings.Contains(help.String(), "\n  connect ") != connectSupported {
		t.Fatalf("help connect advertising does not match connectSupported = %t:\n%s", connectSupported, help.String())
	}

	for _, shell := range []string{"bash", "zsh", "fish"} {
		script, err := completionScript(shell)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(script, "connect") != connectSupported {
			t.Fatalf("%s completion connect advertising does not match connectSupported = %t:\n%s", shell, connectSupported, script)
		}
	}
}

func TestDebugfEmitsOnlyWhenVerbose(t *testing.T) {
	oldVerbose := verbose
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	oldPrefix := log.Prefix()
	t.Cleanup(func() {
		verbose = oldVerbose
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	})

	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")

	verbose = false
	debugf("hidden")
	if got := buf.String(); got != "" {
		t.Fatalf("debugf emitted while disabled: %q", got)
	}

	verbose = true
	debugf("hello %s", "world")
	if got := buf.String(); !strings.Contains(got, "hello world") {
		t.Fatalf("debugf output = %q, want hello world", got)
	}
}

func TestDebugfSanitizesTerminalText(t *testing.T) {
	oldVerbose := verbose
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	oldPrefix := log.Prefix()
	t.Cleanup(func() {
		verbose = oldVerbose
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	})

	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	verbose = true
	debugf("scan %s", "safe\x1b]52;c;clipboard\x07\u061cevil")

	if got := buf.String(); got != "scan safeevil\n" {
		t.Fatalf("debugf output = %q, want sanitized log line", got)
	}
}

func TestDebugDetectionDirectoryHonorsPrivacy(t *testing.T) {
	cfg := config.Default()
	detection := detector.Detection{
		Tool: registry.Tool{ID: "claude-code"},
		Cwd:  filepath.Join(string(filepath.Separator), "private", "client"),
	}
	if got := debugDetectionDirectory(cfg, detection); got != "hidden" {
		t.Fatalf("private debug directory = %q, want hidden", got)
	}

	cfg.Privacy.ShowDirectory = true
	cfg.Privacy.DirectoryBasenameOnly = true
	if got := debugDetectionDirectory(cfg, detection); got != "client" {
		t.Fatalf("allowed debug directory = %q, want basename client", got)
	}

	const homeDirectoryName = "privacy-user-home"
	detection.Cwd = filepath.Join(
		string(filepath.Separator),
		"Users",
		homeDirectoryName,
		"clients",
		"acme-secret-project",
	)
	cfg.Privacy.DirectoryBasenameOnly = false
	cfg.Privacy.DirectoryAllowlist = []string{filepath.Dir(detection.Cwd)}

	got := debugDetectionDirectory(cfg, detection)
	want := "clients/acme-secret-project"
	if got != want {
		t.Fatalf("allowed deep debug directory = %q, want final two components %q", got, want)
	}
	if strings.Contains(got, detection.Cwd) {
		t.Fatalf("debug directory contains full path: %q", got)
	}
	if strings.Contains(got, homeDirectoryName) {
		t.Fatalf("debug directory contains home directory name %q: %q", homeDirectoryName, got)
	}
}

func TestWatchOnceEmitsConfigWarnings(t *testing.T) {
	configHome := withTermpConfigHome(t)

	path := filepath.Join(configHome, "termp", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`
[ui]
accent_color = "cyan"
`), 0o644); err != nil {
		t.Fatal(err)
	}

	oldWriter := log.Writer()
	oldFlags := log.Flags()
	oldPrefix := log.Prefix()
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
	})

	var stderr bytes.Buffer
	log.SetOutput(&stderr)
	log.SetFlags(0)
	log.SetPrefix("")

	if _, err := captureStdout(t, func() error {
		return watch([]string{"--once"})
	}); err != nil {
		t.Fatal(err)
	}
	if got := stderr.String(); !strings.Contains(got, `invalid config value: ui.accent_color "cyan"; using "purple"`) {
		t.Fatalf("watch --once warnings = %q, want invalid accent warning", got)
	}
}

func TestWatchOnceReportsInvalidConfigFallback(t *testing.T) {
	configHome := withTermpConfigHome(t)

	path := filepath.Join(configHome, "termp", "config.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`enabled = "not-a-bool"`), 0o600); err != nil {
		t.Fatal(err)
	}

	oldWriter := log.Writer()
	oldFlags := log.Flags()
	oldPrefix := log.Prefix()
	oldVerbose := verbose
	t.Cleanup(func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
		log.SetPrefix(oldPrefix)
		verbose = oldVerbose
	})

	var stderr bytes.Buffer
	log.SetOutput(&stderr)
	log.SetFlags(0)
	log.SetPrefix("")
	verbose = false

	if _, err := captureStdout(t, func() error {
		return watch([]string{"--once"})
	}); err != nil {
		t.Fatal(err)
	}
	got := stderr.String()
	for _, want := range []string{
		"config load failed",
		"using built-in defaults",
		`termp status`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("watch --once stderr = %q, want %q", got, want)
		}
	}
}

func TestCompletionScriptsContainCommands(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			script, err := completionScript(shell)
			if err != nil {
				t.Fatal(err)
			}
			for _, command := range expectedCommands {
				if !strings.Contains(script, command) {
					t.Fatalf("%s completion missing %q:\n%s", shell, command, script)
				}
			}
		})
	}
}

func TestBuildActivityAddsCTAWhenToolHasNoButtons(t *testing.T) {
	cfg := config.Default()
	activity := buildActivity(cfg, detectionWithButtons(nil), "Fixed fallback")
	if activity == nil {
		t.Fatal("activity = nil, want activity")
	}
	if activity.Name != "Test Tool" {
		t.Fatalf("name = %q, want featured tool display name", activity.Name)
	}
	want := []presence.Button{{Label: "What is this?", URL: "https://termp.polter.sh/"}}
	if !equalButtons(activity.Buttons, want) {
		t.Fatalf("buttons = %#v, want %#v", activity.Buttons, want)
	}
}

func TestBuildActivityDoesNotExceedTwoButtons(t *testing.T) {
	cfg := config.Default()
	activity := buildActivity(cfg, detectionWithButtons([]registry.Button{
		{Label: "One", URL: "https://example.test/one"},
		{Label: "Two", URL: "https://example.test/two"},
	}), "Fixed fallback")
	if activity == nil {
		t.Fatal("activity = nil, want activity")
	}
	want := []presence.Button{
		{Label: "One", URL: "https://example.test/one"},
		{Label: "Two", URL: "https://example.test/two"},
	}
	if !equalButtons(activity.Buttons, want) {
		t.Fatalf("buttons = %#v, want %#v", activity.Buttons, want)
	}
}

func TestBuildActivitySkipsDisabledCTA(t *testing.T) {
	cfg := config.Default()
	cfg.CTA.Enabled = false
	activity := buildActivity(cfg, detectionWithButtons(nil), "Fixed fallback")
	if activity == nil {
		t.Fatal("activity = nil, want activity")
	}
	if len(activity.Buttons) != 0 {
		t.Fatalf("buttons = %#v, want none", activity.Buttons)
	}
}

func TestBuildActivitySkipsAllButtonsWhenDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.Display.Buttons = false
	activity := buildActivity(cfg, detectionWithButtons([]registry.Button{
		{Label: "One", URL: "https://example.test/one"},
	}), "Fixed fallback")
	if activity == nil {
		t.Fatal("activity = nil, want activity")
	}
	if len(activity.Buttons) != 0 {
		t.Fatalf("buttons = %#v, want none", activity.Buttons)
	}
}

func TestServiceWillRelaunch(t *testing.T) {
	tests := []struct {
		name  string
		state service.State
		want  bool
	}{
		{
			name:  "not installed",
			state: service.State{Installed: false},
			want:  false,
		},
		{
			name:  "loaded active",
			state: service.State{Installed: true, Loaded: "active"},
			want:  true,
		},
		{
			name:  "loaded inactive",
			state: service.State{Installed: true, Loaded: "inactive"},
			want:  false,
		},
		{
			name:  "loaded true but disabled",
			state: service.State{Installed: true, Loaded: "true", Enabled: "false"},
			want:  false,
		},
		{
			name:  "loaded unknown",
			state: service.State{Installed: true, Loaded: "unknown"},
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serviceWillRelaunch(tt.state); got != tt.want {
				t.Fatalf("serviceWillRelaunch(%+v) = %t, want %t", tt.state, got, tt.want)
			}
		})
	}
}

func TestPrintStopSuccessAutostartHint(t *testing.T) {
	tests := []struct {
		name     string
		state    service.State
		wantHint bool
	}{
		{
			name:     "autostart enabled",
			state:    service.State{Installed: true, Loaded: "active"},
			wantHint: true,
		},
		{
			name:  "autostart not enabled",
			state: service.State{Installed: true, Loaded: "inactive"},
		},
	}

	const hint = "Autostart is on — run \"termp autostart disable\" to pause it (or \"termp autostart uninstall\" to remove autostart, not the binary)."
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := captureStdout(t, func() error {
				printStopSuccess(1234, tt.state)
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "stopped (pid 1234)") {
				t.Fatalf("stop output missing PID: %q", out)
			}
			if got := strings.Contains(out, hint); got != tt.wantHint {
				t.Fatalf("stop output hint present = %t, want %t: %q", got, tt.wantHint, out)
			}
			if !tt.wantHint && (strings.Contains(out, "termp autostart disable") || strings.Contains(out, "termp autostart uninstall")) {
				t.Fatalf("stop output unexpectedly contains autostart commands: %q", out)
			}
		})
	}
}

func detectionWithButtons(buttons []registry.Button) detector.Detection {
	return detector.Detection{
		Tool: registry.Tool{
			ID:          "test-tool",
			DisplayName: "Test Tool",
			ImageKey:    "test-tool",
			Buttons:     buttons,
		},
	}
}

func equalButtons(a, b []presence.Button) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
