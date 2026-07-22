package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type recordingRunner struct {
	calls []string
	fail  map[string]error
	out   map[string]string
}

func (r *recordingRunner) Run(name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if err := r.fail[call]; err != nil {
		return []byte(r.out[call]), err
	}
	return []byte(r.out[call]), nil
}

func fakeHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return home
}

func TestLaunchAgentPathsUseHomeAndLabel(t *testing.T) {
	home := fakeHome(t)
	path, err := launchAgentPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if path != want {
		t.Fatalf("launchAgentPath() = %q, want %q", path, want)
	}
	logPath, err := launchAgentLogPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, "Library", "Logs", "termp.log"); logPath != want {
		t.Fatalf("launchAgentLogPath() = %q, want %q", logPath, want)
	}
}

func TestSystemdUnitPathUsesHome(t *testing.T) {
	home := fakeHome(t)
	path, err := systemdUnitPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "systemd", "user", ServiceName)
	if path != want {
		t.Fatalf("systemdUnitPath() = %q, want %q", path, want)
	}
}

func TestUnstableExecutablePath(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repo, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "git worktree", path: filepath.Join(repo, "build", "termp"), want: true},
		{name: "os temp", path: filepath.Join(os.TempDir(), "termp"), want: true},
		{name: "tmp", path: "/tmp/build/termp", want: true},
		{name: "private tmp", path: "/private/tmp/build/termp", want: true},
		{name: "private var folders", path: "/private/var/folders/ab/cache/termp", want: true},
		{name: "usr local", path: "/usr/local/bin/termp", want: false},
		{name: "local bin", path: filepath.Join(string(filepath.Separator), "Users", "alice", ".local", "bin", "termp"), want: false},
		{name: "similar tmp prefix", path: "/tmp-stable/termp", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUnstableExecutablePath(tt.path); got != tt.want {
				t.Fatalf("isUnstableExecutablePath(%q) = %t, want %t", tt.path, got, tt.want)
			}
		})
	}
}

func TestValidateInstallExecutableResolvesNestedSymlinkAndHonorsForce(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "termp-real")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	link1 := filepath.Join(dir, "termp-link-1")
	link2 := filepath.Join(dir, "termp-link-2")
	if err := os.Symlink(target, link1); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(link1, link2); err != nil {
		t.Fatal(err)
	}

	if _, err := ValidateInstallExecutable(link2, false); err == nil {
		t.Fatal("ValidateInstallExecutable() error = nil for unstable resolved path")
	} else {
		for _, want := range []string{"unstable executable path", "~/.local/bin", "/usr/local/bin", "--force"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("ValidateInstallExecutable() error missing %q: %v", want, err)
			}
		}
	}

	got, err := ValidateInstallExecutable(link2, true)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ValidateInstallExecutable(force) = %q, want resolved target %q", got, want)
	}
}

func TestBuildLaunchAgentPlist(t *testing.T) {
	content, err := BuildLaunchAgentPlist("/opt/Term Presence/termp", "/tmp/termp.log")
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{
		"<string>" + Label + "</string>",
		"<string>/opt/Term Presence/termp</string>",
		"<string>start</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>KeepAlive</key>\n\t<true/>",
		"<key>StandardOutPath</key>",
		"<key>StandardErrorPath</key>",
		"<string>/tmp/termp.log</string>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plist missing %q:\n%s", want, text)
		}
	}
}

func TestBuildSystemdUnit(t *testing.T) {
	unit, err := BuildSystemdUnit("/opt/%u Term Presence/termp")
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	for _, want := range []string{
		"[Unit]",
		"Description=termp Discord Rich Presence daemon",
		"[Service]",
		`ExecStart="/opt/%%u Term Presence/termp" start`,
		"Restart=on-failure",
		"[Install]",
		"WantedBy=default.target",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("unit missing %q:\n%s", want, text)
		}
	}
}

func TestBuildSystemdUnitRejectsLineBreaks(t *testing.T) {
	for _, lineBreak := range []string{"\r", "\n"} {
		t.Run(fmt.Sprintf("%q", lineBreak), func(t *testing.T) {
			if _, err := BuildSystemdUnit("/opt/termp" + lineBreak + "injected"); err == nil {
				t.Fatalf("BuildSystemdUnit accepted executable path containing %q", lineBreak)
			}
		})
	}
}

func TestDarwinInstallWritesPlistWithoutRealLaunchctl(t *testing.T) {
	home := fakeHome(t)
	runner := &recordingRunner{
		fail: map[string]error{
			"launchctl bootout gui/" + userID() + " " + filepath.Join(home, "Library", "LaunchAgents", Label+".plist"): errors.New("not loaded"),
		},
		out: map[string]string{},
	}
	manager := Manager{GOOS: "darwin", Runner: runner}
	state, err := manager.Install("/bin/termp")
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed {
		t.Fatal("state.Installed = false, want true")
	}
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "<string>/bin/termp</string>") || !strings.Contains(text, "<string>start</string>") {
		t.Fatalf("plist missing executable/start:\n%s", text)
	}
	if len(runner.calls) == 0 {
		t.Fatal("runner was not called")
	}
}

func TestDarwinInstallDoesNotOverwritePlistOnUnloadFailure(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("old launch agent")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	bootout := "launchctl bootout gui/" + userID() + " " + path
	unload := "launchctl unload -w " + path
	runner := &recordingRunner{
		fail: map[string]error{
			bootout: errors.New("exit status 1"),
			unload:  errors.New("exit status 1"),
		},
		out: map[string]string{
			bootout: "Boot-out failed: Operation not permitted\n",
			unload:  "Unload failed: Operation not permitted\n",
		},
	}

	state, err := (Manager{GOOS: "darwin", Runner: runner}).Install("/new/termp")
	if err == nil || !strings.Contains(err.Error(), "Operation not permitted") {
		t.Fatalf("Install() error = %v, want unload permission failure", err)
	}
	if !state.Supported || state.Path != path {
		t.Fatalf("Install() state = %+v, want supported service path %q", state, path)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("plist overwritten after unload failure: got %q, want %q", got, original)
	}
	if hasCall(runner.calls, "launchctl bootstrap gui/"+userID()+" "+path) || hasCall(runner.calls, "launchctl load -w "+path) {
		t.Fatalf("load attempted after unload failure: %#v", runner.calls)
	}
}

func TestDarwinInstallReplacesPlistWhenAlreadyUnloaded(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old launch agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	bootout := "launchctl bootout gui/" + userID() + " " + path
	runner := &recordingRunner{
		fail: map[string]error{bootout: errors.New("exit status 3")},
		out:  map[string]string{bootout: "Boot-out failed: 3: No such process\n"},
	}

	state, err := (Manager{GOOS: "darwin", Runner: runner}).Install("/new/termp")
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed {
		t.Fatalf("Install() state = %+v, want installed", state)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(got), "<string>/new/termp</string>") {
		t.Fatalf("plist was not replaced after benign absent result:\n%s", got)
	}
	if hasCall(runner.calls, "launchctl unload -w "+path) {
		t.Fatalf("legacy unload called after bootout proved service absent: %#v", runner.calls)
	}
}

func TestDarwinDisableAndEnableToggleLaunchAgentWithoutRemovingPlist(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}
	manager := Manager{GOOS: "darwin", Runner: runner}

	if _, err := manager.Disable(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("plist should remain after Disable: %v", err)
	}
	if !hasCall(runner.calls, "launchctl bootout gui/"+userID()+" "+path) {
		t.Fatalf("Disable calls = %#v, want launchctl bootout", runner.calls)
	}

	runner.calls = nil
	if _, err := manager.Enable(); err != nil {
		t.Fatal(err)
	}
	if !hasCall(runner.calls, "launchctl bootstrap gui/"+userID()+" "+path) {
		t.Fatalf("Enable calls = %#v, want launchctl bootstrap", runner.calls)
	}
}

func TestDarwinDisableAndEnableAreIdempotent(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		fail map[string]error
		out  map[string]string
		run  func(Manager) (State, error)
	}{
		{
			name: "disable already unloaded",
			fail: map[string]error{
				"launchctl bootout gui/" + userID() + " " + path: errors.New("not loaded"),
				"launchctl unload -w " + path:                    errors.New("not loaded"),
			},
			out: map[string]string{
				"launchctl unload -w " + path: "Could not find specified service\n",
			},
			run: func(m Manager) (State, error) { return m.Disable() },
		},
		{
			name: "enable already loaded",
			fail: map[string]error{
				"launchctl bootstrap gui/" + userID() + " " + path: errors.New("already loaded"),
				"launchctl load -w " + path:                        errors.New("already loaded"),
			},
			out: map[string]string{
				"launchctl load -w " + path: "service already loaded\n",
			},
			run: func(m Manager) (State, error) { return m.Enable() },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{fail: tt.fail, out: tt.out}
			manager := Manager{GOOS: "darwin", Runner: runner}
			if _, err := tt.run(manager); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDarwinDisableEnableMissingPlistReturnStatusWithoutLaunchctl(t *testing.T) {
	fakeHome(t)
	runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}
	manager := Manager{GOOS: "darwin", Runner: runner}

	state, err := manager.Disable()
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed {
		t.Fatalf("Disable state = %+v, want not installed", state)
	}
	state, err = manager.Enable()
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed {
		t.Fatalf("Enable state = %+v, want not installed", state)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "bootout") || strings.Contains(call, "bootstrap") || strings.Contains(call, "load") || strings.Contains(call, "unload") {
			t.Fatalf("unexpected launchctl toggle call for missing plist: %#v", runner.calls)
		}
	}
}

func TestDarwinStatusMapsLaunchctlErrors(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	call := "launchctl print gui/" + userID() + "/" + Label

	tests := []struct {
		name       string
		output     string
		wantLoaded string
	}{
		{
			name:       "service not found",
			output:     "Could not find service \"" + Label + "\" in domain for user gui: " + userID() + "\n",
			wantLoaded: "false",
		},
		{
			name:       "execution error",
			output:     "Operation not permitted\n",
			wantLoaded: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{
				fail: map[string]error{call: errors.New("exit status 1")},
				out:  map[string]string{call: tt.output},
			}
			state := (Manager{GOOS: "darwin", Runner: runner}).Status()
			if !state.Installed || state.Loaded != tt.wantLoaded || state.Enabled != "n/a" {
				t.Fatalf("Status() = %+v, want installed=true loaded=%q enabled=n/a", state, tt.wantLoaded)
			}
		})
	}
}

func TestDarwinUninstallKeepsPlistOnUnloadFailure(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	bootout := "launchctl bootout gui/" + userID() + " " + path
	unload := "launchctl unload -w " + path
	runner := &recordingRunner{
		fail: map[string]error{
			bootout: errors.New("exit status 1"),
			unload:  errors.New("exit status 1"),
		},
		out: map[string]string{
			bootout: "Boot-out failed: Operation not permitted\n",
			unload:  "Unload failed: Operation not permitted\n",
		},
	}

	state, err := (Manager{GOOS: "darwin", Runner: runner}).Uninstall()
	if err == nil || !strings.Contains(err.Error(), "Operation not permitted") {
		t.Fatalf("Uninstall() error = %v, want permission failure", err)
	}
	if !state.Installed || state.Path != path {
		t.Fatalf("Uninstall() state = %+v, want installed definition at %q", state, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("plist removed after unload failure: %v", err)
	}
}

func TestDarwinUninstallRemovesPlistWhenAlreadyUnloaded(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	bootout := "launchctl bootout gui/" + userID() + " " + path
	runner := &recordingRunner{
		fail: map[string]error{bootout: errors.New("exit status 3")},
		out:  map[string]string{bootout: "Boot-out failed: 3: No such process\n"},
	}

	state, err := (Manager{GOOS: "darwin", Runner: runner}).Uninstall()
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed {
		t.Fatalf("Uninstall() state = %+v, want not installed", state)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plist still exists after benign unload failure: %v", err)
	}
	if hasCall(runner.calls, "launchctl unload -w "+path) {
		t.Fatalf("legacy unload called after bootout proved service absent: %#v", runner.calls)
	}
}

func TestDarwinUninstallAbsentIsNoOp(t *testing.T) {
	fakeHome(t)
	runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}

	state, err := (Manager{GOOS: "darwin", Runner: runner}).Uninstall()
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed {
		t.Fatalf("Uninstall() state = %+v, want not installed", state)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("Uninstall() calls = %#v, want no launchctl calls", runner.calls)
	}
}

func TestLinuxInstallWritesUnitWithoutRealSystemctl(t *testing.T) {
	home := fakeHome(t)
	runner := &recordingRunner{
		fail: map[string]error{},
		out: map[string]string{
			"systemctl --user is-enabled " + ServiceName: "enabled\n",
			"systemctl --user is-active " + ServiceName:  "active\n",
		},
	}
	manager := Manager{GOOS: "linux", Runner: runner}
	state, err := manager.Install("/bin/termp")
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed || state.Enabled != "enabled" || state.Loaded != "active" {
		t.Fatalf("state = %+v, want installed enabled active", state)
	}
	path := filepath.Join(home, ".config", "systemd", "user", ServiceName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "ExecStart=/bin/termp start") || !strings.Contains(text, "Restart=on-failure") {
		t.Fatalf("unit missing executable/restart:\n%s", text)
	}
}

func TestLinuxDisableAndEnableToggleUserService(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, ".config", "systemd", "user", ServiceName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{
		fail: map[string]error{},
		out: map[string]string{
			"systemctl --user is-enabled " + ServiceName: "disabled\n",
			"systemctl --user is-active " + ServiceName:  "inactive\n",
		},
	}
	manager := Manager{GOOS: "linux", Runner: runner}

	if _, err := manager.Disable(); err != nil {
		t.Fatal(err)
	}
	if !hasCall(runner.calls, "systemctl --user disable --now "+ServiceName) {
		t.Fatalf("Disable calls = %#v, want systemctl disable --now", runner.calls)
	}

	runner.calls = nil
	if _, err := manager.Enable(); err != nil {
		t.Fatal(err)
	}
	if !hasCall(runner.calls, "systemctl --user enable --now "+ServiceName) {
		t.Fatalf("Enable calls = %#v, want systemctl enable --now", runner.calls)
	}
}

func TestLinuxStatusParsesDocumentedStatesDespiteNonzeroExit(t *testing.T) {
	fakeHome(t)
	enabledCall := "systemctl --user is-enabled " + ServiceName
	activeCall := "systemctl --user is-active " + ServiceName

	tests := []struct {
		name        string
		enabledOut  string
		activeOut   string
		wantEnabled string
		wantLoaded  string
	}{
		{
			name:        "disabled and inactive",
			enabledOut:  "disabled\n",
			activeOut:   "inactive\n",
			wantEnabled: "disabled",
			wantLoaded:  "inactive",
		},
		{
			name:        "masked and failed",
			enabledOut:  "masked\n",
			activeOut:   "failed\n",
			wantEnabled: "masked",
			wantLoaded:  "failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{
				fail: map[string]error{
					enabledCall: errors.New("exit status 1"),
					activeCall:  errors.New("exit status 3"),
				},
				out: map[string]string{
					enabledCall: tt.enabledOut,
					activeCall:  tt.activeOut,
				},
			}
			state := (Manager{GOOS: "linux", Runner: runner}).Status()
			if state.Enabled != tt.wantEnabled || state.Loaded != tt.wantLoaded {
				t.Fatalf("Status() = %+v, want enabled=%q loaded=%q", state, tt.wantEnabled, tt.wantLoaded)
			}
		})
	}
}

func TestLinuxStatusUsesUnknownForMissingOrUnrecognizedOutput(t *testing.T) {
	fakeHome(t)
	enabledCall := "systemctl --user is-enabled " + ServiceName
	activeCall := "systemctl --user is-active " + ServiceName

	tests := []struct {
		name       string
		enabledOut string
		activeOut  string
	}{
		{name: "transport failure", enabledOut: "", activeOut: ""},
		{name: "unrecognized output", enabledOut: "enabled-runtime\n", activeOut: "mystery\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{
				fail: map[string]error{
					enabledCall: errors.New("command failed"),
					activeCall:  errors.New("command failed"),
				},
				out: map[string]string{
					enabledCall: tt.enabledOut,
					activeCall:  tt.activeOut,
				},
			}
			state := (Manager{GOOS: "linux", Runner: runner}).Status()
			if state.Enabled != "unknown" || state.Loaded != "unknown" {
				t.Fatalf("Status() = %+v, want unknown states", state)
			}
		})
	}
}

func TestWindowsInstallCreatesLogonTaskWithoutRealSchtasks(t *testing.T) {
	runner := &recordingRunner{
		fail: map[string]error{},
		out: map[string]string{
			"schtasks /Query /TN " + TaskName + " /FO LIST /V": "Status: Ready\n",
		},
	}
	manager := Manager{GOOS: "windows", Runner: runner}
	state, err := manager.Install(`C:\Program Files\termp\termp.exe`)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed || state.Enabled != "true" || state.Loaded != "true" {
		t.Fatalf("state = %+v, want installed enabled true loaded true", state)
	}
	if len(runner.calls) < 1 {
		t.Fatal("runner was not called")
	}
	create := runner.calls[0]
	for _, want := range []string{
		"schtasks /Create",
		"/TN " + TaskName,
		`/TR "C:\Program Files\termp\termp.exe" start`,
		"/SC ONLOGON",
		"/RL LIMITED",
		"/F",
	} {
		if !strings.Contains(create, want) {
			t.Fatalf("create call missing %q:\n%s", want, create)
		}
	}
}

func TestWindowsDisableAndEnableToggleTaskWithoutRealSchtasks(t *testing.T) {
	runner := &recordingRunner{
		fail: map[string]error{},
		out: map[string]string{
			"schtasks /Query /TN " + TaskName + " /FO LIST /V": "Status: Ready\n",
		},
	}
	manager := Manager{GOOS: "windows", Runner: runner}

	if _, err := manager.Disable(); err != nil {
		t.Fatal(err)
	}
	if !hasCall(runner.calls, "schtasks /Change /TN "+TaskName+" /DISABLE") {
		t.Fatalf("Disable calls = %#v, want schtasks disable", runner.calls)
	}
	if !hasCall(runner.calls, "schtasks /End /TN "+TaskName) {
		t.Fatalf("Disable calls = %#v, want schtasks end", runner.calls)
	}

	runner.calls = nil
	if _, err := manager.Enable(); err != nil {
		t.Fatal(err)
	}
	if !hasCall(runner.calls, "schtasks /Change /TN "+TaskName+" /ENABLE") {
		t.Fatalf("Enable calls = %#v, want schtasks enable", runner.calls)
	}
}

func TestWindowsUninstallDeletesTaskWithoutRealSchtasks(t *testing.T) {
	runner := &recordingRunner{
		fail: map[string]error{
			"schtasks /Query /TN " + TaskName + " /FO LIST /V": errors.New("exit status 1"),
		},
		out: map[string]string{
			"schtasks /Query /TN " + TaskName + " /FO LIST /V": "ERROR: The system cannot find the file specified.\n",
		},
	}
	manager := Manager{GOOS: "windows", Runner: runner}
	state, err := manager.Uninstall()
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed || state.Enabled != "false" || state.Loaded != "false" {
		t.Fatalf("state = %+v, want not installed enabled false loaded false", state)
	}
	if len(runner.calls) < 1 {
		t.Fatal("runner was not called")
	}
	delete := runner.calls[0]
	for _, want := range []string{
		"schtasks /Delete",
		"/TN " + TaskName,
		"/F",
	} {
		if !strings.Contains(delete, want) {
			t.Fatalf("delete call missing %q:\n%s", want, delete)
		}
	}
}

func TestWindowsStatusParsesTaskState(t *testing.T) {
	tests := []struct {
		name          string
		queryOut      string
		queryErr      error
		wantInstalled bool
		wantLoaded    string
		wantEnabled   string
		wantMessage   bool
	}{
		{
			name:          "ready task is enabled",
			queryOut:      "TaskName: termp\nStatus: Ready\n",
			wantInstalled: true,
			wantLoaded:    "true",
			wantEnabled:   "true",
		},
		{
			name:          "disabled task is not enabled",
			queryOut:      "TaskName: termp\nStatus: Disabled\n",
			wantInstalled: true,
			wantLoaded:    "false",
			wantEnabled:   "false",
		},
		{
			name:          "absent task is not installed",
			queryOut:      "ERROR: The specified task name does not exist in the system.\n",
			queryErr:      errors.New("exit status 1"),
			wantInstalled: false,
			wantLoaded:    "false",
			wantEnabled:   "false",
		},
		{
			name:          "query failure is not clean absence",
			queryOut:      "ERROR: Access is denied.\n",
			queryErr:      errors.New("exit status 1"),
			wantInstalled: true,
			wantLoaded:    "unknown",
			wantEnabled:   "unknown",
			wantMessage:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := "schtasks /Query /TN " + TaskName + " /FO LIST /V"
			runner := &recordingRunner{
				fail: map[string]error{},
				out:  map[string]string{query: tt.queryOut},
			}
			if tt.queryErr != nil {
				runner.fail[query] = tt.queryErr
			}

			state := (Manager{GOOS: "windows", Runner: runner}).Status()
			if state.Installed != tt.wantInstalled || state.Loaded != tt.wantLoaded || state.Enabled != tt.wantEnabled {
				t.Fatalf("Status() = %+v, want installed=%t loaded=%q enabled=%q", state, tt.wantInstalled, tt.wantLoaded, tt.wantEnabled)
			}
			if (state.Message != "") != tt.wantMessage {
				t.Fatalf("Status().Message = %q, wantMessage=%t", state.Message, tt.wantMessage)
			}
			if tt.wantMessage && !strings.Contains(state.Message, "Access is denied") {
				t.Fatalf("Status().Message = %q, want schtasks output", state.Message)
			}
		})
	}
}

func TestWindowsDisableAndEnableReturnQueryFailures(t *testing.T) {
	query := "schtasks /Query /TN " + TaskName + " /FO LIST /V"
	tests := []struct {
		name string
		run  func(Manager) (State, error)
	}{
		{name: "disable", run: Manager.Disable},
		{name: "enable", run: Manager.Enable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{
				fail: map[string]error{query: errors.New("exit status 1")},
				out:  map[string]string{query: "ERROR: Access is denied.\n"},
			}
			state, err := tt.run(Manager{GOOS: "windows", Runner: runner})
			if err == nil || !strings.Contains(err.Error(), "Access is denied") {
				t.Fatalf("%s() error = %v, want query failure", tt.name, err)
			}
			if state.Message == "" || state.Loaded != "unknown" || state.Enabled != "unknown" {
				t.Fatalf("%s() state = %+v, want visible ambiguous query state", tt.name, state)
			}
			if len(runner.calls) != 1 || runner.calls[0] != query {
				t.Fatalf("%s() calls = %#v, want only query", tt.name, runner.calls)
			}
		})
	}
}

func TestUnsupportedOS(t *testing.T) {
	manager := Manager{GOOS: "plan9", Runner: &recordingRunner{}}
	tests := []struct {
		name string
		call func() (State, error)
	}{
		{name: "install", call: func() (State, error) { return manager.Install("/bin/termp") }},
		{name: "uninstall", call: manager.Uninstall},
		{name: "disable", call: manager.Disable},
		{name: "enable", call: manager.Enable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, err := tt.call()
			if !errors.Is(err, ErrUnsupported) || state.Supported || !strings.Contains(state.Message, "plan9") {
				t.Fatalf("state, error = %+v, %v; want unsupported plan9", state, err)
			}
		})
	}
	state := manager.Status()
	if state.Supported || !strings.Contains(state.Message, "plan9") {
		t.Fatalf("Status() = %+v, want unsupported plan9", state)
	}
}

func TestServiceUnitEscapingEdges(t *testing.T) {
	plist, err := BuildLaunchAgentPlist(`/opt/a&b/<termp>`, `/tmp/a&b.log`)
	if err != nil {
		t.Fatal(err)
	}
	text := string(plist)
	for _, escaped := range []string{`/opt/a&amp;b/&lt;termp&gt;`, `/tmp/a&amp;b.log`} {
		if !strings.Contains(text, escaped) {
			t.Fatalf("plist missing escaped value %q:\n%s", escaped, text)
		}
	}

	tests := []struct {
		arg  string
		want string
	}{
		{arg: "", want: `""`},
		{arg: "/usr/local/bin/termp", want: "/usr/local/bin/termp"},
		{arg: `/opt/a b/termp`, want: `"/opt/a b/termp"`},
		{arg: `C:\Program Files\"termp"`, want: `"C:\\Program Files\\\"termp\""`},
	}
	for _, tt := range tests {
		if got := systemdEscapeExecArg(tt.arg); got != tt.want {
			t.Errorf("systemdEscapeExecArg(%q) = %q, want %q", tt.arg, got, tt.want)
		}
	}
}

func TestLinuxUninstallIsIdempotent(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, ".config", "systemd", "user", ServiceName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}
	manager := Manager{GOOS: "linux", Runner: runner}
	for i := 0; i < 2; i++ {
		state, err := manager.Uninstall()
		if err != nil {
			t.Fatal(err)
		}
		if state.Installed {
			t.Fatalf("uninstall %d state = %+v", i+1, state)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit still exists: %v", err)
	}
}

func TestLinuxUninstallKeepsUnitOnDisableFailure(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, ".config", "systemd", "user", ServiceName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	disable := "systemctl --user disable --now " + ServiceName
	runner := &recordingRunner{
		fail: map[string]error{disable: errors.New("exit status 1")},
		out:  map[string]string{disable: "Failed to connect to bus: No such process\n"},
	}

	state, err := (Manager{GOOS: "linux", Runner: runner}).Uninstall()
	if err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Fatalf("Uninstall() error = %v, want bus failure", err)
	}
	if !state.Installed || state.Path != path {
		t.Fatalf("Uninstall() state = %+v, want installed definition at %q", state, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unit removed after disable failure: %v", err)
	}
	if hasCall(runner.calls, "systemctl --user daemon-reload") {
		t.Fatalf("daemon-reload called after disable failure: %#v", runner.calls)
	}
}

func TestLinuxUninstallRemovesUnitWhenAlreadyDisabled(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, ".config", "systemd", "user", ServiceName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	disable := "systemctl --user disable --now " + ServiceName
	runner := &recordingRunner{
		fail: map[string]error{disable: errors.New("exit status 1")},
		out:  map[string]string{disable: "Failed to disable unit: Unit file " + ServiceName + " does not exist.\n"},
	}

	state, err := (Manager{GOOS: "linux", Runner: runner}).Uninstall()
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed {
		t.Fatalf("Uninstall() state = %+v, want not installed", state)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit still exists after benign disable failure: %v", err)
	}
	if !hasCall(runner.calls, "systemctl --user daemon-reload") {
		t.Fatalf("Uninstall() calls = %#v, want daemon-reload", runner.calls)
	}
}

func TestLinuxUninstallReportsDaemonReloadFailure(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, ".config", "systemd", "user", ServiceName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	reload := "systemctl --user daemon-reload"
	runner := &recordingRunner{
		fail: map[string]error{reload: errors.New("exit status 1")},
		out:  map[string]string{reload: "Failed to connect to bus: Permission denied\n"},
	}

	_, err := (Manager{GOOS: "linux", Runner: runner}).Uninstall()
	if err == nil {
		t.Fatal("Uninstall() error = nil, want daemon-reload failure")
	}
	for _, want := range []string{"daemon-reload", "Permission denied", "retry"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Uninstall() error missing %q: %v", want, err)
		}
	}
}

func TestLinuxUninstallAbsentIsNoOp(t *testing.T) {
	fakeHome(t)
	disable := "systemctl --user disable --now " + ServiceName
	reload := "systemctl --user daemon-reload"
	runner := &recordingRunner{
		fail: map[string]error{
			disable: errors.New("must not run"),
			reload:  errors.New("must not run"),
		},
		out: map[string]string{},
	}

	state, err := (Manager{GOOS: "linux", Runner: runner}).Uninstall()
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed {
		t.Fatalf("Uninstall() state = %+v, want not installed", state)
	}
	if hasCall(runner.calls, disable) || hasCall(runner.calls, reload) {
		t.Fatalf("Uninstall() calls = %#v, want no disable or reload", runner.calls)
	}
}

func userID() string {
	return currentUID()
}

func hasCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}
