package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestValidateInstallExecutableThroughScoopCurrentJunction is the issue #472
// regression test. A plain Scoop install runs termp from
// `scoop\apps\termp\current\termp.exe`, where `current` is a junction Scoop's
// link_current creates with `New-Item -ItemType Junction` and then marks
// `attrib +R /L`. filepath.EvalSymlinks rejects that path with "The system
// cannot find the path specified." although the file is there, so
// `termp autostart install` refused every Scoop user while `termp setup`, which
// skips this validation, worked on the same machine.
func TestValidateInstallExecutableThroughScoopCurrentJunction(t *testing.T) {
	root := t.TempDir()
	versionDir := filepath.Join(root, "scoop", "apps", "termp", "0.1.4")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(versionDir, "termp.exe"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(root, "scoop", "apps", "termp", "current")
	// The same two commands Scoop's link_current runs.
	newJunction := fmt.Sprintf("New-Item -Path '%s' -ItemType Junction -Value '%s' | Out-Null", current, versionDir)
	if out, err := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", newJunction).CombinedOutput(); err != nil {
		t.Fatalf("create junction: %v\n%s", err, out)
	}
	if out, err := exec.Command("attrib", current, "+R", "/L").CombinedOutput(); err != nil {
		t.Fatalf("attrib +R /L: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("attrib", current, "-R", "/L").Run() })

	// Keep the temp-root half of the unstable-path guard off this fixture, the
	// way a real Scoop root under the user profile is not inside %TEMP%.
	elsewhere := filepath.Join(root, "elsewhere")
	if err := os.Mkdir(elsewhere, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMP", elsewhere)
	t.Setenv("TEMP", elsewhere)

	exe := filepath.Join(current, "termp.exe")
	if _, err := os.Stat(exe); err != nil {
		t.Fatalf("fixture: os.Stat(%q) = %v, want the file to exist through the junction", exe, err)
	}
	_, evalErr := filepath.EvalSymlinks(exe)
	t.Logf("filepath.EvalSymlinks(%q) error = %v", exe, evalErr)

	got, err := ValidateInstallExecutable(exe, false)
	if err != nil {
		t.Fatalf("ValidateInstallExecutable(%q) error = %v, want nil for an existing Scoop install", exe, err)
	}
	if got != exe {
		t.Fatalf("ValidateInstallExecutable(%q) = %q, want the unresolved current path", exe, got)
	}
}
