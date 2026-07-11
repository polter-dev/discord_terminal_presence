package main

import (
	"bytes"
	"log"
	"runtime"
	"strings"
	"testing"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/presence"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

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
	commands := []string{"start", "stop", "status", "install", "uninstall", "disable", "enable", "settings", "watch", "version", "setup", "config", "completion"}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			script, err := completionScript(shell)
			if err != nil {
				t.Fatal(err)
			}
			for _, command := range commands {
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
	want := []presence.Button{{Label: "What is this?", URL: "https://termp.example"}}
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
