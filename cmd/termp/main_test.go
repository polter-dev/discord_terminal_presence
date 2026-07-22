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
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/presence"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
	"github.com/polter-dev/discord_terminal_presence/internal/service"
	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
)

type failingReleaseSource struct {
	calls int
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
	command updatepkg.Command
	calls   int
	err     error
}

func (r *recordingUpdateRunner) Run(_ context.Context, command updatepkg.Command, _ io.Reader, _, _ io.Writer) error {
	r.command = command
	r.calls++
	return r.err
}

var expectedCommands = []string{
	"install", "uninstall", "disable", "enable", "start", "stop", "status",
	"settings", "watch", "version", "setup", "config", "completion",
	"update",
}

type fileInfoWithSys struct {
	os.FileInfo
	sys any
}

func (i fileInfoWithSys) Sys() any { return i.sys }

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
	info, err := os.Stat(filepath.Dir(want))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("PID directory mode = %o, want 700", got)
	}
}

func TestWritePIDUses0600AndRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "termp.pid")
	if err := writePID(path, 99999998); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("PID file mode = %o, want 600", got)
	}
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
	if _, err := readPID(directoryPath); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("readPID(directory) error = %v, want regular-file rejection", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	foreignUID := uint32(os.Geteuid() + 1)
	foreignInfo := fileInfoWithSys{FileInfo: info, sys: &syscall.Stat_t{Uid: foreignUID}}
	if err := validatePIDFileInfo(foreignInfo, path); err == nil || !strings.Contains(err.Error(), "not current uid") {
		t.Fatalf("foreign owner check error = %v, want owner rejection", err)
	}
}

func TestFormatInstallSuccessShowsCTAForFreshInstall(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "termp", "config.toml")
	got := formatInstallSuccess("/usr/local/bin/termp", configPath, nil, 80)

	for _, want := range []string{"TERMP INSTALLED", "NEXT STEP - RUN THIS NOW:", "termp setup", "Nothing shows on your Discord profile until you do."} {
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

	got := formatInstallSuccess("/usr/local/bin/termp", configPath, nil, 80)
	want := "installed: /usr/local/bin/termp\nruns: termp start\nundo: termp uninstall\n"
	if got != want {
		t.Fatalf("configured install output = %q, want %q", got, want)
	}
	if strings.Contains(got, "NEXT STEP") || strings.Contains(got, "termp setup") {
		t.Fatalf("configured install unexpectedly included CTA:\n%s", got)
	}
}

func TestInstallCTAHasNoANSIWithoutColorSupport(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()

	tests := []struct {
		name     string
		terminal bool
		noColor  bool
	}{
		{name: "non-TTY stdout", terminal: false, noColor: false},
		{name: "NO_COLOR", terminal: true, noColor: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			renderer := newInstallRenderer(output, tt.terminal, tt.noColor)
			got := renderInstallCTA(renderer, 80)
			if strings.Contains(got, "\x1b") {
				t.Fatalf("output contains ANSI escape bytes: %q", got)
			}
		})
	}
}

func TestInstallCTAUsesColorWhenSupported(t *testing.T) {
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()

	renderer := lipgloss.NewRenderer(output)
	renderer.SetColorProfile(termenv.ANSI256)
	got := renderInstallCTA(renderer, 80)
	if !strings.Contains(got, "\x1b") {
		t.Fatalf("color-capable output contains no ANSI styling: %q", got)
	}
}

func TestInstallCTALinesNeverExceed80Columns(t *testing.T) {
	for _, width := range []int{20, 40, 80, 120} {
		got := renderInstallCTA(nil, width)
		maxWidth := min(width, maxInstallCTAWidth)
		for lineNumber, line := range strings.Split(got, "\n") {
			if lineWidth := lipgloss.Width(line); lineWidth > maxWidth {
				t.Fatalf("width %d line %d is %d columns: %q", width, lineNumber+1, lineWidth, line)
			}
		}
	}
}

func TestFormatVersionIncludesBuildAndPlatform(t *testing.T) {
	oldVersion, oldCommit, oldDate := version, commit, date
	t.Cleanup(func() {
		version, commit, date = oldVersion, oldCommit, oldDate
	})
	version, commit, date = "1.2.3", "abc123", "2026-07-05"

	got := formatVersion()
	for _, want := range []string{
		"termp 1.2.3 (abc123, 2026-07-05)",
		"go " + runtime.Version(),
		runtime.GOOS + "/" + runtime.GOARCH,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatVersion() = %q, want substring %q", got, want)
		}
	}
}

func TestUpdateNoticeHasNoANSIWithoutColorSupport(t *testing.T) {
	result := updatepkg.Result{Current: "1.0.0", Latest: "1.1.0", Command: updatepkg.BrewCommand}
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
	result := updatepkg.Result{Current: "1.0.0", Latest: "1.1.0", Command: updatepkg.BrewCommand}
	if got := formatUpdateNotice(result, renderer, 80); !strings.Contains(got, "\x1b") {
		t.Fatalf("color update notice contains no ANSI: %q", got)
	}
}

func TestUpdateNoticeLinesStayWithinOutputWidth(t *testing.T) {
	commands := []string{updatepkg.BrewCommand, updatepkg.GoCommand, updatepkg.GenericCommand("v12.34.56")}
	for _, width := range []int{20, 40, 80, 120} {
		for _, command := range commands {
			result := updatepkg.Result{Current: "1.0.0+abc123", Latest: "v12.34.56+def456", Command: command}
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

func TestWrappedUpdateCommandsRemainCopyPasteable(t *testing.T) {
	for _, command := range []string{updatepkg.BrewCommand, updatepkg.GoCommand, updatepkg.GenericCommand("v1.1.0")} {
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

func TestAutomaticUpdateRunsInstallAwareUpdater(t *testing.T) {
	for _, tt := range []struct {
		name   string
		method updatepkg.InstallMethod
		want   updatepkg.Command
	}{
		{name: "generic", method: updatepkg.InstallGeneric, want: updatepkg.Command{Name: "sh", Args: []string{"-c", updatepkg.GenericCommand("v1.1.0")}}},
		{name: "homebrew", method: updatepkg.InstallHomebrew, want: updatepkg.Command{Name: "brew", Args: []string{"upgrade", "--cask", "polter-dev/tap/termp"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_ = os.Unsetenv("NO_UPDATE_CHECK")
			source := &staticReleaseSource{latest: "v1.1.0"}
			checker := updatepkg.NewChecker(source, filepath.Join(t.TempDir(), "update-check.json"))
			checker.DetectInstall = func() updatepkg.InstallMethod { return tt.method }
			runner := &recordingUpdateRunner{}
			cfg := config.Default()
			cfg.AutoUpdate = true
			runAutomaticUpdate(context.Background(), cfg, "1.0.0", checker, runner)
			if source.calls != 1 || runner.calls != 1 || !reflect.DeepEqual(runner.command, tt.want) {
				t.Fatalf("source calls = %d, runner = (%d, %#v), want (1, %#v)", source.calls, runner.calls, runner.command, tt.want)
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
			cfg := config.Default()
			cfg.AutoUpdate = true
			runAutomaticUpdate(context.Background(), cfg, "1.0.0", checker, tt.runner)
			if tt.name == "check" && tt.runner.calls != 0 {
				t.Fatal("failed check ran updater")
			}
			if tt.name == "update" && tt.runner.calls != 1 {
				t.Fatal("failed updater was not invoked")
			}
		})
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
		{method: updatepkg.InstallHomebrew, want: updatepkg.Command{Name: "brew", Args: []string{"upgrade", "--cask", "polter-dev/tap/termp"}}},
		{method: updatepkg.InstallGo, want: updatepkg.Command{Name: "go", Args: []string{"install", "github.com/polter-dev/discord_terminal_presence/cmd/termp@latest"}}},
		{method: updatepkg.InstallGeneric, want: updatepkg.Command{Name: "sh", Args: []string{"-c", updatepkg.GenericCommand("v1.1.0")}}},
	}
	for _, tt := range tests {
		t.Run(string(tt.method), func(t *testing.T) {
			runner := &recordingUpdateRunner{}
			checker := stubLatestChecker{result: updatepkg.Result{Current: "1.0.0", Latest: "v1.1.0", Method: tt.method}}
			if err := runUpdate(context.Background(), context.Background(), "1.0.0", checker, runner, nil, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			if runner.calls != 1 || runner.command.Name != tt.want.Name || strings.Join(runner.command.Args, "\x00") != strings.Join(tt.want.Args, "\x00") {
				t.Fatalf("runner = (%d, %#v), want (1, %#v)", runner.calls, runner.command, tt.want)
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
	activity := buildActivity(cfg, detectionWithButtons(nil))
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
	}))
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
	activity := buildActivity(cfg, detectionWithButtons(nil))
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
	}))
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
