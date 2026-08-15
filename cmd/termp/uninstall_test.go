package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/polter-dev/discord_terminal_presence/internal/completioninstall"
	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
)

func TestUninstallAllUsesIsolatedPathsAndStopsBeforeDeleting(t *testing.T) {
	sandbox := t.TempDir()
	home := filepath.Join(sandbox, "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(home) == filepath.Clean(os.Getenv("HOME")) {
		t.Fatal("test HOME unexpectedly matches the real HOME")
	}

	targets := []uninstallRemovalTarget{
		{label: "config", path: filepath.Join(home, ".config", "termp"), directory: true},
		{label: "state", path: filepath.Join(home, ".local", "state", "termp"), directory: true},
		{label: "log", path: filepath.Join(home, "Library", "Logs", "termp.log")},
	}
	for _, target := range targets {
		path := target.path
		if target.directory {
			path = filepath.Join(path, "owned")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("termp"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	resolveHome := func() (string, error) { return home, nil }
	for _, shell := range []string{"bash", "zsh", "fish"} {
		if _, err := completioninstall.Install(shell, "# completion\n", resolveHome); err != nil {
			t.Fatal(err)
		}
	}

	outside := filepath.Join(sandbox, "outside.txt")
	if err := os.WriteFile(outside, []byte("owner data"), 0o600); err != nil {
		t.Fatal(err)
	}

	var calls []string
	var output bytes.Buffer
	stopCalls := 0
	deps := uninstallAllDeps{
		stopDaemon: func() (int, bool, error) {
			calls = append(calls, "stop")
			stopCalls++
			if stopCalls <= 2 {
				for _, target := range targets {
					if _, err := os.Stat(target.path); err != nil {
						t.Fatalf("target changed before stop validation: %s: %v", target.path, err)
					}
				}
			}
			return 42, true, nil
		},
		removeAutostart: func(force, stopAfter bool) error {
			calls = append(calls, "autostart")
			if force || stopAfter {
				t.Fatalf("removeAutostart(%t, %t), want false, false", force, stopAfter)
			}
			return nil
		},
		removeCompletion: func(homeDir completioninstall.HomeDirFunc) ([]string, error) {
			calls = append(calls, "completion")
			return completioninstall.UninstallAll(homeDir)
		},
		homeDir:       resolveHome,
		targets:       func() ([]uninstallRemovalTarget, error) { return targets, nil },
		confirm:       func(string) (bool, error) { t.Fatal("confirmation called with --yes"); return false, nil },
		detectInstall: func() updatepkg.InstallMethod { return updatepkg.InstallGeneric },
		genericBinDir: func() (string, error) { return filepath.Join(sandbox, "bin"), nil },
		goos:          "darwin",
		stdout:        &output,
		removeFile: func(path string) error {
			calls = append(calls, "file:"+path)
			return os.Remove(path)
		},
		removeAll: func(path string) error {
			calls = append(calls, "dir:"+path)
			return os.RemoveAll(path)
		},
	}

	if err := uninstallAllWithDeps(false, true, deps); err != nil {
		t.Fatalf("uninstallAllWithDeps() error = %v", err)
	}
	if len(calls) < 4 || !reflect.DeepEqual(calls[:4], []string{"stop", "autostart", "stop", "completion"}) {
		t.Fatalf("initial calls = %#v, want stop, autostart, stop, completion", calls)
	}
	assertUninstallSandboxRemoved(t, targets, resolveHome)
	if data, err := os.ReadFile(outside); err != nil || string(data) != "owner data" {
		t.Fatalf("outside sentinel changed: %q, %v", data, err)
	}

	calls = nil
	if err := uninstallAllWithDeps(false, true, deps); err != nil {
		t.Fatalf("idempotent uninstallAllWithDeps() error = %v", err)
	}
	if len(calls) < 4 || !reflect.DeepEqual(calls[:4], []string{"stop", "autostart", "stop", "completion"}) {
		t.Fatalf("idempotent initial calls = %#v, want stop, autostart, stop, completion", calls)
	}
	wantBinaryCommand := "sudo rm " + shellQuote(filepath.Join(sandbox, "bin", "termp"))
	if !strings.Contains(output.String(), wantBinaryCommand) {
		t.Fatalf("output missing resolved binary removal command:\n%s", output.String())
	}
}

func TestUninstallAllStopFailureLeavesEverythingUntouched(t *testing.T) {
	root := t.TempDir()
	target := uninstallRemovalTarget{label: "config", path: filepath.Join(root, "termp"), directory: true}
	file := filepath.Join(target.path, "config.toml")
	if err := os.MkdirAll(target.path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	mutated := false
	deps := uninstallAllDeps{
		stopDaemon:       func() (int, bool, error) { return 0, false, errors.New("identity validation failed") },
		removeAutostart:  func(bool, bool) error { mutated = true; return nil },
		removeCompletion: func(completioninstall.HomeDirFunc) ([]string, error) { mutated = true; return nil, nil },
		homeDir:          func() (string, error) { return filepath.Join(root, "home"), nil },
		targets:          func() ([]uninstallRemovalTarget, error) { return []uninstallRemovalTarget{target}, nil },
		confirm:          func(string) (bool, error) { return true, nil },
		detectInstall:    func() updatepkg.InstallMethod { return updatepkg.InstallHomebrew },
		genericBinDir:    func() (string, error) { return "", errors.New("unexpected") },
		goos:             "darwin",
		stdout:           &bytes.Buffer{},
		removeFile:       func(string) error { mutated = true; return nil },
		removeAll:        func(string) error { mutated = true; return nil },
	}
	err := uninstallAllWithDeps(false, false, deps)
	if err == nil || !strings.Contains(err.Error(), "stop daemon before uninstalling") {
		t.Fatalf("uninstallAllWithDeps() error = %v, want stop prerequisite error", err)
	}
	if mutated {
		t.Fatal("destructive dependency ran after stop validation failed")
	}
	if data, readErr := os.ReadFile(file); readErr != nil || string(data) != "keep" {
		t.Fatalf("config changed after stop failure: %q, %v", data, readErr)
	}
}

func TestUninstallAllCancellationDoesNotStopOrDelete(t *testing.T) {
	called := false
	var output bytes.Buffer
	deps := uninstallAllDeps{
		stopDaemon:       func() (int, bool, error) { called = true; return 0, false, nil },
		removeAutostart:  func(bool, bool) error { called = true; return nil },
		removeCompletion: func(completioninstall.HomeDirFunc) ([]string, error) { called = true; return nil, nil },
		homeDir:          func() (string, error) { return t.TempDir(), nil },
		targets:          func() ([]uninstallRemovalTarget, error) { return nil, nil },
		confirm:          func(plan string) (bool, error) { return false, nil },
		detectInstall:    func() updatepkg.InstallMethod { return updatepkg.InstallHomebrew },
		genericBinDir:    func() (string, error) { return "", errors.New("unexpected") },
		goos:             "darwin",
		stdout:           &output,
		removeFile:       func(string) error { called = true; return nil },
		removeAll:        func(string) error { called = true; return nil },
	}
	if err := uninstallAllWithDeps(false, false, deps); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("canceled uninstall mutated state")
	}
	if !strings.Contains(output.String(), "Uninstall cancelled.") {
		t.Fatalf("cancellation output missing:\n%s", output.String())
	}
}

func TestTopLevelUninstallPointsToFullRemoval(t *testing.T) {
	handlers := map[string]autostartActionHandler{
		"uninstall": func(args []string) (bool, error) {
			if len(args) != 0 {
				t.Fatalf("uninstall args = %#v, want none", args)
			}
			return false, nil
		},
	}
	output, err := captureStdout(t, func() error {
		return dispatchCommandWithAutostartHandlers("uninstall", nil, handlers)
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "To remove everything, run: termp uninstall --all") {
		t.Fatalf("top-level uninstall output missing full-removal guidance:\n%s", output)
	}
}

func TestTopLevelUninstallUsesParsedAllFlag(t *testing.T) {
	for _, spelling := range []string{"-all", "-all=true", "--all=TRUE", "--all=1"} {
		t.Run(spelling, func(t *testing.T) {
			handlers := map[string]autostartActionHandler{
				"uninstall": func(args []string) (bool, error) {
					options, err := parseUninstallOptions(args)
					if err != nil {
						return false, err
					}
					if !options.all {
						t.Fatal("flag parser did not set all")
					}
					return options.all, nil
				},
			}
			output, err := captureStdout(t, func() error {
				return dispatchCommandWithAutostartHandlers("uninstall", []string{spelling}, handlers)
			})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(output, "To remove everything") {
				t.Fatalf("top-level uninstall output contains full-removal guidance after %s:\n%s", spelling, output)
			}
		})
	}
}

func TestUninstallBinaryCommand(t *testing.T) {
	binDir := filepath.Join(string(filepath.Separator), "custom install", "bin")
	tests := []struct {
		name       string
		method     updatepkg.InstallMethod
		goos       string
		rpmManager string
		want       string
	}{
		{name: "generic", method: updatepkg.InstallGeneric, goos: "linux", want: "sudo rm " + shellQuote(filepath.Join(binDir, "termp"))},
		{name: "go", method: updatepkg.InstallGo, goos: "darwin", want: "rm " + shellQuote(filepath.Join(binDir, "termp"))},
		{name: "homebrew", method: updatepkg.InstallHomebrew, goos: "darwin", want: "brew uninstall --cask termp"},
		{name: "scoop", method: updatepkg.InstallScoop, goos: "windows", want: "scoop uninstall termp"},
		{name: "debian", method: updatepkg.InstallDebian, goos: "linux", want: "sudo apt remove termp"},
		{name: "rpm dnf", method: updatepkg.InstallRPM, goos: "linux", rpmManager: "dnf", want: "sudo dnf remove termp"},
		{name: "rpm zypper", method: updatepkg.InstallRPM, goos: "linux", rpmManager: "zypper", want: "sudo zypper remove termp"},
		{name: "rpm yum", method: updatepkg.InstallRPM, goos: "linux", rpmManager: "yum", want: "sudo yum remove termp"},
		{name: "rpm plain", method: updatepkg.InstallRPM, goos: "linux", rpmManager: "rpm", want: "sudo rpm -e termp"},
		{
			name:   "rpm none",
			method: updatepkg.InstallRPM,
			goos:   "linux",
			want:   "remove termp with your RPM package manager (sudo dnf, sudo zypper, sudo yum, or sudo rpm)",
		},
		{
			name:       "ambiguous system package",
			method:     updatepkg.InstallSystemPackage,
			goos:       "linux",
			rpmManager: "zypper",
			want:       "sudo apt remove termp (Debian/Ubuntu) or sudo zypper remove termp (RPM-based Linux)",
		},
		{name: "windows", method: updatepkg.InstallGeneric, goos: "windows", want: "del " + windowsCommandQuote(filepath.Join(binDir, "termp.exe")) + " " + windowsCommandQuote(filepath.Join(binDir, "termpw.exe"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := uninstallBinaryCommand(tt.method, tt.goos, tt.rpmManager, func() (string, error) { return binDir, nil })
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("uninstallBinaryCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRemoveUninstallTargetRejectsBroadDirectory(t *testing.T) {
	tests := []struct {
		name       string
		target     uninstallRemovalTarget
		wantErrMsg string
	}{
		{
			name:       "volume root",
			target:     uninstallRemovalTarget{label: "config", path: filepath.Clean(string(filepath.Separator)), directory: true},
			wantErrMsg: "unsafe",
		},
		{
			name:       "relative directory",
			target:     uninstallRemovalTarget{label: "config", path: "termp", directory: true},
			wantErrMsg: "relative",
		},
		{
			name:       "relative file",
			target:     uninstallRemovalTarget{label: "config", path: filepath.Join("termp", "config.toml")},
			wantErrMsg: "relative",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			err := removeUninstallTarget(
				tt.target,
				func(string) error { called = true; return nil },
				func(string) error { called = true; return nil },
			)
			if err == nil || !strings.Contains(err.Error(), tt.wantErrMsg) {
				t.Fatalf("removeUninstallTarget() error = %v, want %s path error", err, tt.wantErrMsg)
			}
			if called {
				t.Fatal("removal function called for refused path")
			}
		})
	}
}

func assertUninstallSandboxRemoved(t *testing.T, targets []uninstallRemovalTarget, homeDir completioninstall.HomeDirFunc) {
	t.Helper()
	for _, target := range targets {
		if _, err := os.Stat(target.path); !os.IsNotExist(err) {
			t.Fatalf("target still exists after uninstall: %s: %v", target.path, err)
		}
	}
	for _, shell := range []string{"bash", "zsh", "fish"} {
		path, err := completioninstall.TargetPath(shell, homeDir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("completion still exists after uninstall: %s: %v", path, err)
		}
	}
}
