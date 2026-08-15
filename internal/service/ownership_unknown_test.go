package service

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnixUnparseableDefinitionsRequireForce covers #556 item 1: an
// unparseable existing definition used to skip the ownership check entirely
// (parseErr != nil short-circuited the whole condition to false), so
// Installed stayed true and ForeignTask stayed false. That made the
// definition read as owned even though its target could never be
// established. It must instead be treated the same as an unreadable
// definition, which already required --force before this fix.
func TestUnixUnparseableDefinitionsRequireForce(t *testing.T) {
	platforms := []struct {
		name        string
		goos        string
		unparseable []byte
		path        func() (string, error)
	}{
		{name: "linux", goos: "linux", unparseable: []byte("[Service]\n"), path: systemdUnitPath},
		{name: "darwin", goos: "darwin", unparseable: []byte("not a plist"), path: launchAgentPath},
	}

	for _, platform := range platforms {
		t.Run(platform.name+"/without force", func(t *testing.T) {
			fakeHome(t)
			path, err := platform.path()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, platform.unparseable, 0o644); err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}
			current := filepath.Join(t.TempDir(), "termp")
			manager := Manager{GOOS: platform.goos, Runner: runner, Executable: current}

			state, err := manager.InstallDefinition(current, false)
			if err == nil ||
				!strings.Contains(err.Error(), path) ||
				!strings.Contains(err.Error(), "ownership could not be verified") ||
				!strings.Contains(err.Error(), "--force") {
				t.Fatalf("InstallDefinition() error = %v, want actionable ownership refusal for %s", err, path)
			}
			if !state.ForeignTask || state.Installed {
				t.Fatalf("InstallDefinition() state = %+v, want ownership-unknown definition", state)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("InstallDefinition() calls = %#v, want no runner invocation", runner.calls)
			}
			installed, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(installed) != string(platform.unparseable) {
				t.Fatalf("InstallDefinition() without force rewrote the definition: got %q, want unchanged %q", installed, platform.unparseable)
			}
		})

		t.Run(platform.name+"/with force", func(t *testing.T) {
			fakeHome(t)
			path, err := platform.path()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, platform.unparseable, 0o644); err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}
			current := filepath.Join(t.TempDir(), "termp")
			manager := Manager{GOOS: platform.goos, Runner: runner, Executable: current}

			state, err := manager.InstallDefinition(current, true)
			if err != nil {
				t.Fatalf("InstallDefinition(force) error = %v, want mutation to proceed", err)
			}
			if !state.Installed || state.ForeignTask {
				t.Fatalf("InstallDefinition(force) state = %+v, want owned definition", state)
			}
		})
	}
}

// TestUnixEmptyExecutableCannotVerifyOwnership covers #557 on Unix: an
// unresolved running executable (NewManager stores "" when ResolveExecutable
// fails) used to make isForeignUnixExecutable return false for any
// definition, because both empty-string guard clauses treated "no current
// executable to compare against" as "not foreign." That let a second,
// genuinely foreign installation's definition be read as owned and mutated
// without --force. An unresolved current executable must instead be an
// unverifiable-ownership state, matching an unparseable or unreadable
// definition.
func TestUnixEmptyExecutableCannotVerifyOwnership(t *testing.T) {
	platforms := []struct {
		name       string
		goos       string
		definition func(string) ([]byte, error)
		path       func() (string, error)
	}{
		{name: "linux", goos: "linux", definition: BuildSystemdUnit, path: systemdUnitPath},
		{name: "darwin", goos: "darwin", definition: func(exe string) ([]byte, error) { return BuildLaunchAgentPlist(exe) }, path: launchAgentPath},
	}

	for _, platform := range platforms {
		t.Run(platform.name, func(t *testing.T) {
			fakeHome(t)
			path, err := platform.path()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			// A genuinely foreign definition, targeting some other install.
			definition, err := platform.definition("/opt/somebody-else/termp")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, definition, 0o644); err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}
			// Executable deliberately left empty, simulating a failed
			// ResolveExecutable.
			manager := Manager{GOOS: platform.goos, Runner: runner}

			state, err := manager.Uninstall(false)
			if err == nil || !strings.Contains(err.Error(), "--force") {
				t.Fatalf("Uninstall() error = %v, want actionable ownership refusal with an unresolved executable", err)
			}
			if !state.ForeignTask || state.Installed {
				t.Fatalf("Uninstall() state = %+v, want ownership-unknown definition", state)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("Uninstall() calls = %#v, want no runner invocation without --force", runner.calls)
			}
			if _, statErr := os.Stat(path); statErr != nil {
				t.Fatalf("Uninstall() without force removed the definition: %v", statErr)
			}
		})
	}
}

// TestWindowsEmptyExecutableCannotVerifyOwnership covers #557 on Windows:
// StatusContext guarded the ownership comparison with `s.executable != ""`,
// so an unresolved running executable skipped the comparison entirely and
// read a foreign task as owned, installed, and enabled.
func TestWindowsEmptyExecutableCannotVerifyOwnership(t *testing.T) {
	query := "schtasks /Query /TN " + TaskName + " /XML"
	runner := &recordingRunner{
		fail: map[string]error{},
		// windowsEnabledTaskXML targets C:\termp.exe, a different executable
		// from whatever this (empty) Manager.Executable would be.
		out: map[string]string{query: `<Task><Actions><Exec><Command>C:\other\termp.exe</Command></Exec></Actions><Settings><Enabled>true</Enabled></Settings></Task>`},
	}
	manager := Manager{GOOS: "windows", Runner: runner}

	state := manager.Status()
	if state.Installed || !state.ForeignTask {
		t.Fatalf("Status() = %+v, want unverified ownership with an unresolved executable", state)
	}

	_, err := manager.Uninstall(false)
	if err == nil || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("Uninstall() error = %v, want actionable ownership refusal with an unresolved executable", err)
	}
	if hasCallForOwnershipTest(runner.calls, "schtasks /Delete /TN "+TaskName+" /F") {
		t.Fatalf("Uninstall() calls = %#v, want no delete without --force", runner.calls)
	}
}

func hasCallForOwnershipTest(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

// TestWindowsUnverifiableStatusRefusesDestructiveMutationsButAllowsForce
// covers #556 items 2 and 3: a failed schtasks query used to be coerced into
// "installed and owned" (Installed=true, ForeignTask left false), which let
// Uninstall delete the task and install rewrite it without --force while
// Disable/Enable separately refused on any non-empty Message. Ownership that
// could not be verified now consistently requires --force for every
// mutation, install included, and forced calls still take the definition
// over.
func TestWindowsUnverifiableStatusRefusesDestructiveMutationsButAllowsForce(t *testing.T) {
	newFailingRunner := func() *recordingRunner {
		targetedQuery := "schtasks /Query /TN " + TaskName + " /FO CSV /NH"
		listQuery := "schtasks /Query /FO CSV /NH"
		return &recordingRunner{
			fail: map[string]error{
				targetedQuery: errors.New("start schtasks: access denied"),
				listQuery:     errors.New("exit status 1"),
			},
			out: map[string]string{listQuery: "ERROR: Access is denied.\n"},
		}
	}

	t.Run("uninstall without force is refused", func(t *testing.T) {
		runner := newFailingRunner()
		_, err := (Manager{GOOS: "windows", Runner: runner}).Uninstall(false)
		if err == nil || !strings.Contains(err.Error(), "--force") {
			t.Fatalf("Uninstall() error = %v, want actionable ownership refusal", err)
		}
		if hasCallForOwnershipTest(runner.calls, "schtasks /Delete /TN "+TaskName+" /F") ||
			hasCallForOwnershipTest(runner.calls, "schtasks /End /TN "+TaskName) {
			t.Fatalf("Uninstall() calls = %#v, want no destructive schtasks call without --force", runner.calls)
		}
	})

	t.Run("uninstall with force still proceeds", func(t *testing.T) {
		runner := newFailingRunner()
		if _, err := (Manager{GOOS: "windows", Runner: runner}).Uninstall(true); err != nil {
			t.Fatalf("Uninstall(force) error = %v, want mutation to proceed despite unverified ownership", err)
		}
		if !hasCallForOwnershipTest(runner.calls, "schtasks /Delete /TN "+TaskName+" /F") {
			t.Fatalf("Uninstall(force) calls = %#v, want delete despite unverified ownership", runner.calls)
		}
	})

	t.Run("install without force is refused, install with force still proceeds", func(t *testing.T) {
		// Install uses the same existing ForeignTask+--force gate as every
		// other mutation (windows.go:31); this fix does not add a new,
		// unconditional abort to install. --force still takes an
		// unverifiable definition over, matching TestWindowsForceTakesOverForeignTask.
		runner := newFailingRunner()
		_, err := (Manager{GOOS: "windows", Runner: runner}).Install(`C:\termp.exe`, false)
		if err == nil || !strings.Contains(err.Error(), "--force") {
			t.Fatalf("Install() error = %v, want actionable ownership refusal", err)
		}
		if hasCallForOwnershipTest(runner.calls, "schtasks /Create /TN "+TaskName+" ") {
			t.Fatalf("Install() calls = %#v, want no create without --force", runner.calls)
		}

		runner = newFailingRunner()
		if _, err := (Manager{GOOS: "windows", Runner: runner}).Install(`C:\termp.exe`, true); err != nil {
			t.Fatalf("Install(force) error = %v, want install to proceed despite unverifiable prior status", err)
		}
	})

	t.Run("disable is refused the same way as uninstall", func(t *testing.T) {
		runner := newFailingRunner()
		_, err := (Manager{GOOS: "windows", Runner: runner}).Disable()
		if err == nil || !strings.Contains(err.Error(), "--force") {
			t.Fatalf("Disable() error = %v, want the same actionable ownership refusal as Uninstall", err)
		}
	})
}
