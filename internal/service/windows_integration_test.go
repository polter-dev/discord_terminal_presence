//go:build windows && integration

package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestWindowsSchtasksIntegration(t *testing.T) {
	manager := NewManager()
	if manager.GOOS != "windows" {
		t.Fatalf("NewManager().GOOS = %q, want windows", manager.GOOS)
	}

	exe := buildWindowsIntegrationHelper(t)
	manager.Executable = exe
	aliases := []struct {
		name string
		path string
	}{
		{name: "junction", path: junctionExecutable(t, exe)},
	}
	if short := shortWindowsPath(t, exe); short != "" &&
		!strings.EqualFold(normalizeWindowsPath(short), normalizeWindowsPath(exe)) {
		aliases = append(aliases, struct {
			name string
			path string
		}{name: "8.3 short path", path: short})
	} else {
		t.Log("8.3 short-name alias unavailable; verify on hardware with 8.3 names enabled")
	}
	if substituted := substitutedWindowsExecutable(t, exe); substituted != "" {
		aliases = append(aliases, struct {
			name string
			path string
		}{name: "substituted drive", path: substituted})
	}
	for _, alias := range aliases {
		t.Run(alias.name, func(t *testing.T) {
			testWindowsSchtasksLifecycle(t, manager, alias.path)
		})
	}
}

func testWindowsSchtasksLifecycle(t *testing.T, manager Manager, installExe string) {
	t.Helper()
	t.Cleanup(func() {
		// The task normally exits promptly, but explicitly end it so Windows
		// releases the helper binary before TempDir cleanup removes it.
		_, _ = manager.Runner.Run("schtasks", "/End", "/TN", TaskName)
		if _, err := manager.Uninstall(false); err != nil {
			t.Errorf("cleanup Uninstall() error = %v", err)
		}
	})

	// Remove a task left behind by an interrupted earlier run before asserting
	// the complete lifecycle from a known state.
	state, err := manager.Uninstall(false)
	if err != nil {
		t.Fatalf("initial Uninstall() error = %v", err)
	}
	assertWindowsIntegrationState(t, "initial uninstall", state, false, "false", "false")

	state, err = manager.Install(installExe, false)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	assertWindowsIntegrationState(t, "install", state, true, "true", "unknown", "true")

	// Query separately so the test directly covers the /XML status path. The
	// task may not launch a process on a headless CI runner, so Loaded may stay
	// unknown and is not used as proof that installation succeeded.
	state = manager.Status()
	assertWindowsIntegrationState(t, "status", state, true, "true", "unknown", "true")

	// The helper sleeps long enough for this second run request to exercise the
	// already-running result on interactive hardware. Headless CI may not launch
	// the task, but still covers the remainder of the scheduler lifecycle.
	state, err = manager.Enable()
	if err != nil {
		t.Fatalf("Enable() while running error = %v", err)
	}
	assertWindowsIntegrationState(t, "enable while running", state, true, "true", "unknown", "true")

	state, err = manager.Disable()
	if err != nil {
		t.Fatalf("Disable() error = %v", err)
	}
	assertWindowsIntegrationState(t, "disable", state, true, "false", "unknown", "false")

	state, err = manager.Enable()
	if err != nil {
		t.Fatalf("Enable() error = %v", err)
	}
	assertWindowsIntegrationState(t, "enable", state, true, "true", "unknown", "true")

	state, err = manager.Uninstall(false)
	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	assertWindowsIntegrationState(t, "uninstall", state, false, "false", "false")
}

func buildWindowsIntegrationHelper(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Program Files termp")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create helper directory: %v", err)
	}
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte("package main\nimport \"time\"\nfunc main() { time.Sleep(30 * time.Second) }\n"), 0o600); err != nil {
		t.Fatalf("write helper source: %v", err)
	}
	exe := filepath.Join(dir, "termp-integration-helper.exe")
	// These fixtures cover path identity through junction, short-name, and
	// substituted-drive aliases. Supply the normal sibling launcher so they do
	// not incidentally exercise the separately tested missing-launcher fallback.
	for _, outputPath := range []string{exe, filepath.Join(dir, windowsLauncherName)} {
		if output, err := exec.Command("go", "build", "-o", outputPath, source).CombinedOutput(); err != nil {
			t.Fatalf("build helper executable %s: %v\n%s", outputPath, err, output)
		}
	}
	return exe
}

func junctionExecutable(t *testing.T, exe string) string {
	t.Helper()
	junction := filepath.Join(t.TempDir(), "helper-junction")
	if output, err := exec.Command("cmd", "/c", "mklink", "/j", junction, filepath.Dir(exe)).CombinedOutput(); err != nil {
		t.Fatalf("create executable junction: %v\n%s", err, output)
	}
	return filepath.Join(junction, filepath.Base(exe))
}

func shortWindowsPath(t *testing.T, exe string) string {
	t.Helper()
	pointer, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		t.Logf("encode path for 8.3 resolution: %v", err)
		return ""
	}
	buffer := make([]uint16, 260)
	for {
		length, err := windows.GetShortPathName(pointer, &buffer[0], uint32(len(buffer)))
		if err != nil {
			t.Logf("resolve 8.3 short path: %v", err)
			return ""
		}
		if length < uint32(len(buffer)) {
			return windows.UTF16ToString(buffer[:length])
		}
		buffer = make([]uint16, length+1)
	}
}

func substitutedWindowsExecutable(t *testing.T, exe string) string {
	t.Helper()
	const drive = `T:`
	if _, err := os.Stat(drive + `\`); err == nil {
		t.Logf("substituted-drive alias skipped because %s is already in use", drive)
		return ""
	}
	if output, err := exec.Command("subst", drive, filepath.Dir(exe)).CombinedOutput(); err != nil {
		t.Logf("create substituted drive: %v: %s", err, output)
		return ""
	}
	t.Cleanup(func() {
		if output, err := exec.Command("subst", drive, "/D").CombinedOutput(); err != nil {
			t.Errorf("remove substituted drive: %v: %s", err, output)
		}
	})
	return drive + `\` + filepath.Base(exe)
}

func assertWindowsIntegrationState(
	t *testing.T,
	step string,
	state State,
	installed bool,
	enabled string,
	allowedLoaded ...string,
) {
	t.Helper()

	if !state.Supported {
		t.Fatalf("%s: Supported = false", step)
	}
	if state.Path != TaskName {
		t.Fatalf("%s: Path = %q, want %q", step, state.Path, TaskName)
	}
	if state.Message != "" {
		t.Fatalf("%s: Message = %q, want empty", step, state.Message)
	}
	if state.Installed != installed {
		t.Fatalf("%s: Installed = %t, want %t", step, state.Installed, installed)
	}
	if state.Enabled != enabled {
		t.Fatalf("%s: Enabled = %q, want %q", step, state.Enabled, enabled)
	}
	for _, allowed := range allowedLoaded {
		if state.Loaded == allowed {
			return
		}
	}
	t.Fatalf("%s: Loaded = %q, want one of %v", step, state.Loaded, allowedLoaded)
}
