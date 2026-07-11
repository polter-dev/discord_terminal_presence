package service

import (
	"errors"
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
	text := string(BuildSystemdUnit("/opt/Term Presence/termp"))
	for _, want := range []string{
		"[Unit]",
		"Description=termp Discord Rich Presence daemon",
		"[Service]",
		`ExecStart="/opt/Term Presence/termp" start`,
		"Restart=on-failure",
		"[Install]",
		"WantedBy=default.target",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("unit missing %q:\n%s", want, text)
		}
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

func TestWindowsInstallCreatesLogonTaskWithoutRealSchtasks(t *testing.T) {
	runner := &recordingRunner{
		fail: map[string]error{},
		out:  map[string]string{},
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
		out:  map[string]string{},
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
			"schtasks /Query /TN " + TaskName: errors.New("task not found"),
		},
		out: map[string]string{},
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

func TestUnsupportedOS(t *testing.T) {
	_, err := (Manager{GOOS: "plan9", Runner: &recordingRunner{}}).Install("/bin/termp")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Install unsupported error = %v, want ErrUnsupported", err)
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
