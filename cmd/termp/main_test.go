package main

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/presence"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
	"github.com/polter-dev/discord_terminal_presence/internal/service"
	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
)

var expectedCommands = []string{
	"install", "uninstall", "disable", "enable", "start", "stop", "status",
	"settings", "watch", "version", "setup", "config", "completion",
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
	commands := []string{updatepkg.BrewCommand, updatepkg.GoCommand, updatepkg.GenericCommand}
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
	for _, command := range []string{updatepkg.BrewCommand, updatepkg.GoCommand, updatepkg.GenericCommand} {
		for _, width := range []int{20, 40, 80} {
			wrapped := strings.Join(wrapShellCommand(command, width), "\n")
			if got := strings.ReplaceAll(wrapped, "\\\n", ""); got != command {
				t.Fatalf("width %d unwrapped command = %q, want %q", width, got, command)
			}
		}
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
