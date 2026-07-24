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
	"github.com/polter-dev/discord_terminal_presence/internal/service"
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

func TestHelpCommandDispatchReturnsSuccess(t *testing.T) {
	out, err := captureStdout(t, func() error {
		return dispatchCommand("help", nil)
	})
	if err != nil {
		t.Fatalf("dispatchCommand(help) = %v, want success", err)
	}
	if !strings.Contains(out, "Terminal Presence (termp)") {
		t.Fatalf("help output missing usage:\n%s", out)
	}
}

func TestUnknownCommandNamesOffendingToken(t *testing.T) {
	err := dispatchCommand("boguscmd", nil)
	if !errors.Is(err, errUnknownCommand) {
		t.Fatalf("dispatchCommand(boguscmd) = %v, want unknown-command error", err)
	}
	var stderr bytes.Buffer
	if !printDispatchUsageError(err, &stderr) {
		t.Fatal("printDispatchUsageError() = false, want true")
	}
	out := stderr.String()
	if !strings.Contains(out, `unknown command "boguscmd"`) {
		t.Fatalf("unknown-command output missing token:\n%s", out)
	}
	if !strings.Contains(out, "Usage:") {
		t.Fatalf("unknown-command output missing usage:\n%s", out)
	}
}

type fakeAutostartManager struct {
	statusState    service.State
	uninstallState service.State
	disableState   service.State
}

func (m fakeAutostartManager) Install(string) (service.State, error) {
	return service.State{}, nil
}

func (m fakeAutostartManager) Uninstall() (service.State, error) {
	return m.uninstallState, nil
}

func (m fakeAutostartManager) Disable() (service.State, error) {
	return m.disableState, nil
}

func (m fakeAutostartManager) Enable() (service.State, error) {
	return service.State{}, nil
}

func (m fakeAutostartManager) Status() service.State {
	return m.statusState
}

func withFakeAutostartManager(t *testing.T, manager fakeAutostartManager) {
	t.Helper()
	old := newAutostartManager
	newAutostartManager = func() autostartManager { return manager }
	t.Cleanup(func() { newAutostartManager = old })
}

func TestAutostartUninstallNotInstalledMessage(t *testing.T) {
	withFakeAutostartManager(t, fakeAutostartManager{
		statusState:    service.State{Supported: true, Path: service.TaskName},
		uninstallState: service.State{Supported: true, Path: service.TaskName},
	})

	out, err := captureStdout(t, func() error {
		return uninstall(nil)
	})
	if err != nil {
		t.Fatalf("uninstall() = %v, want success", err)
	}
	if got, want := strings.TrimSpace(out), "autostart not installed (nothing to remove)"; got != want {
		t.Fatalf("uninstall output = %q, want %q", got, want)
	}
}

func TestAutostartUninstallRemovedMessage(t *testing.T) {
	withFakeAutostartManager(t, fakeAutostartManager{
		statusState:    service.State{Supported: true, Installed: true, Path: service.TaskName},
		uninstallState: service.State{Supported: true, Path: service.TaskName},
	})

	out, err := captureStdout(t, func() error {
		return uninstall(nil)
	})
	if err != nil {
		t.Fatalf("uninstall() = %v, want success", err)
	}
	if got, want := strings.TrimSpace(out), `removed autostart: \Terminal Presence\termp (binary was not removed)`; got != want {
		t.Fatalf("uninstall output = %q, want %q", got, want)
	}
}

func TestAutostartDisableNotInstalledMessage(t *testing.T) {
	withFakeAutostartManager(t, fakeAutostartManager{
		disableState: service.State{Supported: true, Path: service.TaskName},
	})

	out, err := captureStdout(t, func() error {
		return disable(nil)
	})
	if err != nil {
		t.Fatalf("disable() = %v, want success", err)
	}
	got := strings.TrimSpace(out)
	if !strings.Contains(got, "autostart not installed (nothing to disable)") {
		t.Fatalf("disable output missing not-installed message: %q", got)
	}
	if strings.Contains(got, "termp stop") {
		t.Fatalf("disable output points at stop: %q", got)
	}
	if !strings.Contains(got, "termp autostart install") {
		t.Fatalf("disable output missing install hint: %q", got)
	}
}

func TestParseStartOptions(t *testing.T) {
	for _, tt := range []struct {
		name           string
		args           []string
		defaultVerbose bool
		want           startOptions
		wantBackground bool
	}{
		{name: "background default", want: startOptions{}, wantBackground: true},
		{name: "long foreground", args: []string{"--foreground"}, want: startOptions{foreground: true}},
		{name: "short foreground", args: []string{"-f"}, want: startOptions{foreground: true}},
		{name: "long detach compatibility", args: []string{"--detach"}, want: startOptions{detach: true}, wantBackground: true},
		{name: "short detach and verbose", args: []string{"-d", "-v"}, want: startOptions{detach: true, verbose: true}, wantBackground: true},
		{name: "detach does not conflict with foreground", args: []string{"--foreground", "--detach"}, want: startOptions{detach: true, foreground: true}},
		{name: "inherits root verbose", defaultVerbose: true, want: startOptions{verbose: true}, wantBackground: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseStartOptions(tt.args, tt.defaultVerbose, io.Discard)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("parseStartOptions() = %#v, want %#v", got, tt.want)
			}
			if background := !got.foreground; background != tt.wantBackground {
				t.Fatalf("background = %t, want %t", background, tt.wantBackground)
			}
		})
	}
}

func TestStartHelpExplainsBackgroundDefaultForegroundAndAutostart(t *testing.T) {
	var output bytes.Buffer
	_, err := parseStartOptions([]string{"--help"}, false, &output)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseStartOptions() error = %v, want %v", err, flag.ErrHelp)
	}
	for _, want := range []string{"--foreground", "--detach", "background (default)", "current daemon lifetime", "autostart install"} {
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
	withTermpConfigHome(t)
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
	withTermpConfigHome(t)
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
	withTermpConfigHome(t)
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
			flags:  []string{"--verbose", "--version", "--help", "--force", "--once", "--foreground -f", "--detach -d"},
		},
		{
			shell: "zsh",
			header: "#compdef termp\n" +
				"# termp zsh completion.\n" +
				"# Enable in the current session: source <(termp completion zsh)\n" +
				"# Or install permanently: termp completion zsh > ${fpath[1]}/_termp\n",
			marker: "_termp()",
			flags:  []string{"--verbose", "--version", "--help", "--force", "--once", "--foreground -f", "--detach -d"},
		},
		{
			shell: "fish",
			header: "# termp fish completion.\n" +
				"# Enable in the current session: termp completion fish | source\n" +
				"# Or install permanently: termp completion fish > ~/.config/fish/completions/termp.fish\n",
			marker: "complete -c termp",
			flags:  []string{"-l verbose", "-l version", "-l help", "-l force", "-l once", "-s f -l foreground", "-s d -l detach"},
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
