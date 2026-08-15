package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// launcherTaskXML renders a minimal enabled task whose action runs command,
// matching the shape schtasks /Query /XML returns.
func launcherTaskXML(command string) string {
	return `<Task><Actions><Exec><Command>` + command +
		`</Command></Exec></Actions><Settings><Enabled>true</Enabled></Settings></Task>`
}

func TestWindowsTaskExecPrefersLauncherWhenPresent(t *testing.T) {
	dir := t.TempDir()
	daemon := filepath.Join(dir, "termp.exe")
	launcher := filepath.Join(dir, "termpw.exe")
	for _, p := range []string{daemon, launcher} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	command, arguments, err := windowsTaskExec(daemon)
	if err != nil {
		t.Fatal(err)
	}
	if command != launcher {
		t.Fatalf("command = %q, want launcher %q", command, launcher)
	}
	if arguments != "" {
		t.Fatalf("arguments = %q, want empty (launcher takes none)", arguments)
	}
}

func TestWindowsTaskExecFallsBackToDaemonWhenLauncherMissing(t *testing.T) {
	dir := t.TempDir()
	daemon := filepath.Join(dir, "termp.exe")
	if err := os.WriteFile(daemon, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	command, arguments, err := windowsTaskExec(daemon)
	if err != nil {
		t.Fatal(err)
	}
	if command != daemon {
		t.Fatalf("command = %q, want daemon %q", command, daemon)
	}
	if arguments != "start --foreground" {
		t.Fatalf("arguments = %q, want \"start --foreground\"", arguments)
	}
}

func TestWindowsTaskExecBypassesScoopLauncherShim(t *testing.T) {
	root := t.TempDir()
	shimDir := filepath.Join(root, "shims")
	currentDir := filepath.Join(root, "apps", "termp", "current")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(currentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shimDaemon := filepath.Join(shimDir, "termp.exe")
	shimLauncher := filepath.Join(shimDir, "termpw.exe")
	realLauncher := filepath.Join(currentDir, "termpw.exe")
	for _, path := range []string{shimDaemon, shimLauncher, realLauncher} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	command, arguments, err := windowsTaskExec(shimDaemon)
	if err != nil {
		t.Fatal(err)
	}
	if command != realLauncher {
		t.Fatalf("command = %q, want real launcher %q (not Scoop shim %q)", command, realLauncher, shimLauncher)
	}
	if arguments != "" {
		t.Fatalf("arguments = %q, want empty (launcher takes none)", arguments)
	}
}

func TestWindowsTaskExecDoesNotFallBackToScoopConsoleShim(t *testing.T) {
	shimDir := filepath.Join(t.TempDir(), "shims")
	if err := os.MkdirAll(shimDir, 0o755); err != nil {
		t.Fatal(err)
	}
	shimDaemon := filepath.Join(shimDir, "termp.exe")
	shimLauncher := filepath.Join(shimDir, "termpw.exe")
	for _, path := range []string{shimDaemon, shimLauncher} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	command, arguments, err := windowsTaskExec(shimDaemon)
	if err == nil {
		t.Fatalf("windowsTaskExec(%q) = (%q, %q, nil), want missing real-launcher error", shimDaemon, command, arguments)
	}
}

func TestOwnsWindowsTaskCommandAcceptsRealScoopLauncher(t *testing.T) {
	executable := `C:\Users\me\scoop\shims\termp.exe`
	realLauncher := `C:\Users\me\scoop\apps\termp\current\termpw.exe`
	if !ownsWindowsTaskCommand(realLauncher, executable) {
		t.Fatalf("real Scoop launcher %q not owned by invocation %q", realLauncher, executable)
	}
}

// TestBuildWindowsTaskXMLLauncherExecOmitsArguments records the exact decoded
// <Exec> the launcher task ships with (the deliverable artifact) and proves the
// empty-argument launcher form emits no <Arguments> element.
func TestBuildWindowsTaskXMLLauncherExecOmitsArguments(t *testing.T) {
	const launcher = `C:\Program Files\termp\termpw.exe`
	data, err := BuildWindowsTaskXML(launcher, "", `DOMAIN\user`)
	if err != nil {
		t.Fatal(err)
	}
	text := decodeUTF16XML(t, data)
	wantExec := `<Exec><Command>C:\Program Files\termp\termpw.exe</Command></Exec>`
	if !strings.Contains(text, wantExec) {
		t.Fatalf("decoded task XML missing launcher Exec %q:\n%s", wantExec, text)
	}
	if strings.Contains(text, "<Arguments>") {
		t.Fatalf("launcher task XML unexpectedly contains <Arguments>:\n%s", text)
	}
	// Surface the exact decoded Exec for the hand-back / PR body.
	start := strings.Index(text, "<Exec>")
	end := strings.Index(text, "</Exec>") + len("</Exec>")
	t.Logf("decoded launcher <Exec>: %s", text[start:end])
}

// TestWindowsStatusAcceptsLauncherTask is the ownership-trap guard: once the
// task points at the sibling launcher, Status must still recognize the task as
// ours, or install/uninstall/enable/disable lock out every existing Windows user
// behind --force (issue #473).
func TestWindowsStatusAcceptsLauncherTask(t *testing.T) {
	query := "schtasks /Query /TN " + TaskName + " /XML"
	runner := &recordingRunner{
		fail: map[string]error{},
		out:  map[string]string{query: launcherTaskXML(`C:\Program Files\termp\termpw.exe`)},
	}
	manager := Manager{GOOS: "windows", Runner: runner, Executable: `C:\Program Files\termp\termp.exe`}
	state := manager.Status()
	if state.ForeignTask {
		t.Fatalf("launcher task reported as foreign: %+v", state)
	}
	if !state.Installed {
		t.Fatalf("launcher task not reported installed: %+v", state)
	}
	if state.Message != "" {
		t.Fatalf("unexpected Message for owned launcher task: %q", state.Message)
	}
}

// TestWindowsStatusRejectsForeignLauncherTask confirms the launcher acceptance
// is scoped to the sibling in our own install dir, not any termpw.exe anywhere.
func TestWindowsStatusRejectsForeignLauncherTask(t *testing.T) {
	query := "schtasks /Query /TN " + TaskName + " /XML"
	runner := &recordingRunner{
		fail: map[string]error{},
		out:  map[string]string{query: launcherTaskXML(`C:\Other\place\termpw.exe`)},
	}
	manager := Manager{GOOS: "windows", Runner: runner, Executable: `C:\Program Files\termp\termp.exe`}
	state := manager.Status()
	if !state.ForeignTask {
		t.Fatalf("foreign launcher task not reported foreign: %+v", state)
	}
}

// TestWindowsInstallPersistsStableInvocationPath is the issue #502 guard: the
// scheduled-task action must record the invocation path as resolved for install
// (here a Scoop-style `current` junction), never a junction-followed versioned
// directory. If canonicalization leaks back into the write path, a real junction
// resolves `...\current\...` to `...\<version>\...`, which `scoop update` later
// deletes and autostart dies. The launcher probe must likewise stay on the
// stable `current` directory.
func TestWindowsInstallPersistsStableInvocationPath(t *testing.T) {
	base := t.TempDir()
	versionDir := filepath.Join(base, "apps", "termp", "1.2.3")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"termp.exe", "termpw.exe"} {
		if err := os.WriteFile(filepath.Join(versionDir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	currentDir := filepath.Join(base, "apps", "termp", "current")
	if err := os.Symlink(versionDir, currentDir); err != nil {
		// Creating a directory symlink can require privilege (e.g. Windows
		// without Developer Mode). The behavior is still exercised on the other
		// build platforms; skip rather than fail where symlinks are unavailable.
		t.Skipf("directory symlink unavailable: %v", err)
	}

	stableDaemon := filepath.Join(currentDir, "termp.exe")
	stableLauncher := filepath.Join(currentDir, "termpw.exe")

	runner := &windowsInstallRunner{}
	manager := Manager{GOOS: "windows", Runner: runner, Executable: stableDaemon}
	if _, err := manager.Install(stableDaemon, false); err != nil {
		t.Fatal(err)
	}

	xmlText := decodeUTF16XML(t, runner.xmlData)
	wantCommand := "<Command>" + stableLauncher + "</Command>"
	if !strings.Contains(xmlText, wantCommand) {
		t.Fatalf("task XML missing stable launcher command %q:\n%s", wantCommand, xmlText)
	}
	if strings.Contains(xmlText, versionDir) {
		t.Fatalf("task XML baked the junction-followed versioned path %q:\n%s", versionDir, xmlText)
	}
}

func TestOwnsWindowsTaskCommand(t *testing.T) {
	const exe = `C:\Program Files\termp\termp.exe`
	tests := []struct {
		name        string
		taskCommand string
		want        bool
	}{
		{name: "same daemon", taskCommand: `C:\Program Files\termp\termp.exe`, want: true},
		{name: "sibling launcher", taskCommand: `C:\Program Files\termp\termpw.exe`, want: true},
		{name: "sibling launcher quoted/percent", taskCommand: `"C:/Program Files/termp/./termpw.exe"`, want: true},
		{name: "foreign daemon", taskCommand: `C:\Other\termp\termp.exe`, want: false},
		{name: "foreign launcher", taskCommand: `C:\Other\termp\termpw.exe`, want: false},
		{name: "empty", taskCommand: ``, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ownsWindowsTaskCommand(tt.taskCommand, exe); got != tt.want {
				t.Fatalf("ownsWindowsTaskCommand(%q, %q) = %t, want %t", tt.taskCommand, exe, got, tt.want)
			}
		})
	}
}
