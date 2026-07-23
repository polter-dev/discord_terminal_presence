package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
	usagepkg "github.com/polter-dev/discord_terminal_presence/internal/usage"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = old })

	runErr := fn()
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = old
	return string(out), runErr
}

func TestParseRootMatrix(t *testing.T) {
	oldVerbose := verbose
	t.Cleanup(func() { verbose = oldVerbose })

	tests := []struct {
		name        string
		args        []string
		wantCommand string
		wantArgs    []string
		wantVersion bool
		wantErr     error
		wantAnyErr  bool
	}{
		{name: "short verbose", args: []string{"-v", "status"}, wantCommand: "status"},
		{name: "command arguments remain untouched", args: []string{"watch", "--once", "extra"}, wantCommand: "watch", wantArgs: []string{"--once", "extra"}},
		{name: "version wins without command", args: []string{"--version"}, wantVersion: true},
		{name: "missing command", wantErr: flag.ErrHelp},
		{name: "help", args: []string{"--help"}, wantErr: flag.ErrHelp},
		{name: "unknown global flag", args: []string{"--bogus"}, wantAnyErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verbose = false
			command, args, showVersion, err := parseRoot(tt.args)
			if tt.wantAnyErr {
				if err == nil {
					t.Fatal("parseRoot() error = nil")
				}
				return
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("parseRoot() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if command != tt.wantCommand || showVersion != tt.wantVersion || strings.Join(args, "\x00") != strings.Join(tt.wantArgs, "\x00") {
				t.Fatalf("parseRoot() = %q, %#v, %t; want %q, %#v, %t", command, args, showVersion, tt.wantCommand, tt.wantArgs, tt.wantVersion)
			}
		})
	}
}

func TestRootHelpRequested(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want bool
	}{
		{args: nil, want: false},
		{args: []string{"start", "--help"}, want: true},
		{args: []string{"-h"}, want: true},
		{args: []string{"help"}, want: false},
	} {
		if got := rootHelpRequested(tt.args); got != tt.want {
			t.Errorf("rootHelpRequested(%q) = %t, want %t", tt.args, got, tt.want)
		}
	}
}

func TestParseStartOptions(t *testing.T) {
	for _, tt := range []struct {
		name           string
		args           []string
		defaultVerbose bool
		want           startOptions
	}{
		{name: "foreground default", want: startOptions{}},
		{name: "long detach", args: []string{"--detach"}, want: startOptions{detach: true}},
		{name: "short detach and verbose", args: []string{"-d", "-v"}, want: startOptions{detach: true, verbose: true}},
		{name: "inherits root verbose", defaultVerbose: true, want: startOptions{verbose: true}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStartOptions(tt.args, tt.defaultVerbose, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("parseStartOptions() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestStartHelpExplainsDetachAndAutostart(t *testing.T) {
	var output bytes.Buffer
	_, err := parseStartOptions([]string{"--help"}, false, &output)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseStartOptions() error = %v, want %v", err, flag.ErrHelp)
	}
	for _, want := range []string{"--detach", "current daemon lifetime", "autostart install"} {
		if !strings.Contains(output.String(), want) {
			t.Errorf("start help missing %q:\n%s", want, output.String())
		}
	}
}

func TestDetachedChildConstructionAndMarker(t *testing.T) {
	if got := detachedChildArgs(false); strings.Join(got, " ") != "start --internal-detached-child" {
		t.Fatalf("detachedChildArgs(false) = %q", got)
	}
	if got := detachedChildArgs(true); strings.Join(got, " ") != "start --internal-detached-child --verbose" {
		t.Fatalf("detachedChildArgs(true) = %q", got)
	}

	options, err := parseStartOptions(detachedChildArgs(false)[1:], false, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !options.detachedChild || options.detach {
		t.Fatalf("child options = %#v, want marker without detach", options)
	}
}

func TestCommandsRejectInvalidArgumentsBeforeSideEffects(t *testing.T) {
	tests := []struct {
		name string
		call func([]string) error
		args []string
	}{
		{name: "start", call: start, args: []string{"--unknown"}},
		{name: "stop", call: stop, args: []string{"--unknown"}},
		{name: "status", call: status, args: []string{"--unknown"}},
		{name: "settings", call: settings, args: []string{"--unknown"}},
		{name: "watch", call: watch, args: []string{"--unknown"}},
		{name: "setup", call: setup, args: []string{"--unknown"}},
		{name: "version", call: versionCommand, args: []string{"--unknown"}},
		{name: "update extra positional", call: updateCommand, args: []string{"extra"}},
		{name: "completion missing shell", call: completion, args: nil},
		{name: "completion extra shell", call: completion, args: []string{"bash", "zsh"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(tt.args); err == nil {
				t.Fatal("error = nil, want argument error")
			}
		})
	}
}

func TestConfigCommandInitAndForce(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path := config.DefaultPath()

	out, err := captureStdout(t, func() error { return configCommand([]string{"init"}) })
	if err != nil {
		t.Fatal(err)
	}
	if want := "Wrote default config: " + path + "\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config was not created: %v", err)
	}
	if err := configCommand([]string{"init"}); err == nil {
		t.Fatal("second init succeeded without --force")
	}
	out, err = captureStdout(t, func() error { return configCommand([]string{"init", "--force"}) })
	if err != nil {
		t.Fatalf("forced init: %v", err)
	}
	for _, want := range []string{
		"Reset config to defaults: " + path,
		"Run \"termp setup\" to configure interactively.",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("forced init output missing %q: %q", want, out)
		}
	}
	// Bare `config` (no action) is a help request; an unknown action is a real
	// usage error, not flag.ErrHelp.
	if err := configCommand(nil); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("configCommand(nil) error = %v, want flag.ErrHelp", err)
	}
	if err := configCommand([]string{"unknown"}); err == nil || errors.Is(err, flag.ErrHelp) {
		t.Fatalf("configCommand([unknown]) error = %v, want a non-help usage error", err)
	}
}

func TestSetupNonInteractiveWritesDefaults(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	out, err := captureStdout(t, func() error { return setup(nil) })
	if err != nil {
		t.Fatal(err)
	}
	path := config.DefaultPath()
	for _, want := range []string{"Wrote default config: " + path, "Non-interactive setup skipped autostart"} {
		if !strings.Contains(out, want) {
			t.Fatalf("setup output missing %q: %q", want, out)
		}
	}
	got, err := config.LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.Path != path {
		t.Fatalf("saved defaults = %+v", got)
	}
}

func TestSetupNonInteractivePreservesExistingConfig(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path := config.DefaultPath()
	enabled := false
	cfg := config.Default()
	cfg.Enabled = false
	cfg.AutoUpdate = true
	cfg.ScanInterval = "11s"
	cfg.Display.ToolName = false
	cfg.Privacy.DirectoryAllowlist = []string{"/work/private"}
	cfg.Tools = map[string]config.ToolOverride{"codex-cli": {Enabled: &enabled}}
	cfg.CustomTools = []registry.CustomTool{{
		ID: "custom", DisplayName: "Custom", Match: registry.CustomMatch{Name: "custom"}, ImageKey: "custom",
	}}
	if err := config.Save(cfg, path); err != nil {
		t.Fatal(err)
	}

	if _, err := captureStdout(t, func() error { return setup(nil) }); err != nil {
		t.Fatal(err)
	}
	got, err := config.LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled || !got.AutoUpdate || got.ScanInterval != "11s" || got.Display.ToolName ||
		len(got.Privacy.DirectoryAllowlist) != 1 || len(got.Tools) != 1 || len(got.CustomTools) != 1 || got.CustomTools[0].ID != "custom" {
		t.Fatalf("non-interactive setup changed existing config: %+v", got)
	}
}

func TestCompletionScriptRejectsUnknownShell(t *testing.T) {
	if _, err := completionScript("powershell"); err == nil || !strings.Contains(err.Error(), "unsupported shell") {
		t.Fatalf("error = %v, want unsupported shell", err)
	}
}

func TestCompletionScriptsExplainInstallationAndCoverCLI(t *testing.T) {
	tests := []struct {
		shell  string
		header string
		marker string
		flags  []string
	}{
		{
			shell: "bash",
			header: "# termp bash completion.\n" +
				"# Enable in the current session: source <(termp completion bash)\n" +
				"# Or install permanently: termp completion bash > ~/.local/share/bash-completion/completions/termp\n",
			marker: "_termp_complete()",
			flags:  []string{"--verbose", "--version", "--help", "--force", "--once"},
		},
		{
			shell: "zsh",
			header: "#compdef termp\n" +
				"# termp zsh completion.\n" +
				"# Enable in the current session: source <(termp completion zsh)\n" +
				"# Or install permanently: termp completion zsh > ${fpath[1]}/_termp\n",
			marker: "_termp()",
			flags:  []string{"--verbose", "--version", "--help", "--force", "--once"},
		},
		{
			shell: "fish",
			header: "# termp fish completion.\n" +
				"# Enable in the current session: termp completion fish | source\n" +
				"# Or install permanently: termp completion fish > ~/.config/fish/completions/termp.fish\n",
			marker: "complete -c termp",
			flags:  []string{"-l verbose", "-l version", "-l help", "-l force", "-l once"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.shell, func(t *testing.T) {
			script, err := completionScript(tt.shell)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(script, tt.header) {
				t.Fatalf("completion header = %q, want prefix %q", script[:min(len(script), len(tt.header))], tt.header)
			}
			if !strings.Contains(script, tt.marker) {
				t.Fatalf("completion script missing function marker %q", tt.marker)
			}
			for _, command := range expectedCommands {
				if !strings.Contains(script, command) {
					t.Errorf("completion script missing command %q", command)
				}
			}
			for _, want := range append(tt.flags, "init", "bash", "zsh", "fish") {
				if !strings.Contains(script, want) {
					t.Errorf("completion script missing %q", want)
				}
			}
		})
	}
}

func TestPIDHelpersRejectInvalidContentAndUnsafeDirectory(t *testing.T) {
	for _, content := range []string{"", "abc", "0", "-1"} {
		t.Run("content_"+strings.ReplaceAll(content, "-", "negative"), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "termp.pid")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readPID(path); err == nil {
				t.Fatalf("readPID(%q) succeeded", content)
			}
		})
	}
	if err := writePID(filepath.Join(t.TempDir(), "termp.pid"), 0); err == nil {
		t.Fatal("writePID accepted zero")
	}

	dir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensurePIDDirectory(dir); err == nil {
		t.Fatal("ensurePIDDirectory accepted a regular file")
	}
}

func TestPIDProcessAndRemovalHelpers(t *testing.T) {
	if processAlive(0) || processLooksLikeTermp(-1) {
		t.Fatal("invalid PID reported alive or termp-like")
	}
	if !processAlive(os.Getpid()) {
		t.Fatal("current test process reported dead")
	}
	if processLooksLikeTermp(99999999) {
		t.Fatal("nonexistent process matched termp executable")
	}
	path := filepath.Join(t.TempDir(), "termp.pid")
	if err := os.WriteFile(path, []byte("1"), 0o600); err != nil {
		t.Fatal(err)
	}
	pid, info, err := readPIDRecord(path)
	if err != nil {
		t.Fatal(err)
	}
	if removed, err := removePIDIfOwned(path, pid, info); err != nil || !removed {
		t.Fatalf("removePIDIfOwned() = %t, %v; want true, nil", removed, err)
	}
	if removed, err := removePIDIfOwned(path, pid, info); err != nil || removed {
		t.Fatalf("second removePIDIfOwned() = %t, %v; want false, nil", removed, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed PID stat error = %v", err)
	}
}

func TestActivityHelpers(t *testing.T) {
	tools := []registry.Tool{{ID: "one"}, {ID: "two"}}
	if got := otherToolIDs(nil); got != "none" {
		t.Fatalf("otherToolIDs(nil) = %q", got)
	}
	if got := otherToolIDs(tools); got != "one,two" {
		t.Fatalf("otherToolIDs() = %q", got)
	}

	store := usagepkg.New()
	recordUsage(store, detectionWithButtons(nil), timeForTest())
	if ranks := store.Rank(); len(ranks) != 1 || ranks[0] != "test-tool" {
		t.Fatalf("usage ranks = %#v", ranks)
	}
}

func timeForTest() time.Time {
	return time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
}

func TestOutputTextEdges(t *testing.T) {
	if got := strings.Join(wrapOutputText("abcdefghij", 4), "|"); got != "abcd|efgh|ij" {
		t.Fatalf("wrapped long word = %q", got)
	}
	if got := strings.Join(wrapShellCommand("abcdefghij", 4), "|"); got != "abc\\|def\\|ghi\\|j" {
		t.Fatalf("wrapped shell token = %q", got)
	}
	var buf bytes.Buffer
	usage(&buf)
	if !strings.Contains(buf.String(), "--verbose") {
		t.Fatal("usage omitted global flags")
	}
}
