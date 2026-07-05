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

func TestUnsupportedOS(t *testing.T) {
	_, err := (Manager{GOOS: "plan9", Runner: &recordingRunner{}}).Install("/bin/termp")
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Install unsupported error = %v, want ErrUnsupported", err)
	}
}

func userID() string {
	return currentUID()
}
