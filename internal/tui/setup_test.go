package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
)

func TestApplySetupReconcilesAutostartState(t *testing.T) {
	tests := []struct {
		name           string
		original       bool
		desired        bool
		installed      bool
		statusErr      error
		wantInstall    int
		wantUninstall  int
		wantExecutable int
		wantAutostart  string
	}{
		{
			name: "unchanged enabled installs when missing", original: true, desired: true,
			wantInstall: 1, wantExecutable: 1, wantAutostart: "installed",
		},
		{
			name: "unchanged enabled reapplies installed service", original: true, desired: true, installed: true,
			wantInstall: 1, wantExecutable: 1, wantAutostart: "installed",
		},
		{
			name: "changed enabled installs when missing", original: false, desired: true,
			wantInstall: 1, wantExecutable: 1, wantAutostart: "installed",
		},
		{
			name: "changed enabled reapplies installed service", original: false, desired: true, installed: true,
			wantInstall: 1, wantExecutable: 1, wantAutostart: "installed",
		},
		{
			name: "unchanged disabled removes installed service", original: false, desired: false, installed: true,
			wantUninstall: 1, wantAutostart: "removed",
		},
		{
			name: "changed disabled removes installed service", original: true, desired: false, installed: true,
			wantUninstall: 1, wantAutostart: "removed",
		},
		{
			name: "changed disabled is already disabled", original: true, desired: false,
			wantAutostart: "already disabled",
		},
		{
			name: "status failure installs desired enabled service", original: true, desired: true,
			statusErr: errors.New("status failed"), wantInstall: 1, wantExecutable: 1, wantAutostart: "installed",
		},
		{
			name: "status failure uninstalls desired disabled service", original: false, desired: false,
			statusErr: errors.New("status failed"), wantUninstall: 1, wantAutostart: "removed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := config.Default()
			original.StartAtLogin = tt.original
			desired := original
			desired.StartAtLogin = tt.desired
			installCalls, uninstallCalls, executableCalls := 0, 0, 0

			result := applySetup(
				original,
				desired,
				func(config.Config) (string, error) { return "/tmp/config.toml", nil },
				func(exe string) error {
					installCalls++
					if exe != "/usr/local/bin/termp" {
						t.Fatalf("install executable = %q", exe)
					}
					return nil
				},
				func() error {
					uninstallCalls++
					return nil
				},
				func() (string, error) {
					executableCalls++
					return "/usr/local/bin/termp", nil
				},
				func() (bool, error) { return tt.installed, tt.statusErr },
			)

			if result.err != nil {
				t.Fatalf("applySetup() error = %v", result.err)
			}
			if installCalls != tt.wantInstall || uninstallCalls != tt.wantUninstall || executableCalls != tt.wantExecutable {
				t.Fatalf("calls install/uninstall/executable = %d/%d/%d, want %d/%d/%d",
					installCalls, uninstallCalls, executableCalls,
					tt.wantInstall, tt.wantUninstall, tt.wantExecutable)
			}
			if result.autostart != tt.wantAutostart {
				t.Fatalf("autostart = %q, want %q", result.autostart, tt.wantAutostart)
			}
		})
	}
}

func TestApplySetupNilStatusInstallsDesiredEnabledService(t *testing.T) {
	cfg := config.Default()
	installCalls := 0
	result := applySetup(
		cfg,
		cfg,
		nil,
		func(string) error {
			installCalls++
			return nil
		},
		nil,
		func() (string, error) { return "/usr/local/bin/termp", nil },
		nil,
	)

	if result.err != nil || installCalls != 1 || result.autostart != "installed" {
		t.Fatalf("result = %#v, install calls = %d", result, installCalls)
	}
}

func TestApplySetupFailedInstallRestoresKnownAbsentService(t *testing.T) {
	original := config.Default()
	original.StartAtLogin = false
	desired := original
	desired.StartAtLogin = true
	persisted := original
	uninstallCalls := 0
	// The install writes its definition and then fails, so the compensating
	// uninstall has something real to clean up.
	definitionPresent := false

	result := applySetup(
		original,
		desired,
		func(next config.Config) (string, error) {
			persisted = next
			return "/tmp/config.toml", nil
		},
		func(string) error {
			definitionPresent = true
			return errors.New("install failed")
		},
		func() error {
			uninstallCalls++
			definitionPresent = false
			return nil
		},
		func() (string, error) { return "/usr/local/bin/termp", nil },
		func() (bool, error) { return definitionPresent, nil },
	)

	if result.err == nil || !strings.Contains(result.err.Error(), "install failed") {
		t.Fatalf("applySetup() error = %v, want install failure", result.err)
	}
	if uninstallCalls != 1 {
		t.Fatalf("uninstall calls = %d, want service rollback", uninstallCalls)
	}
	if persisted.StartAtLogin {
		t.Fatal("failed install did not restore original config")
	}
}

// A refused install writes nothing, so there is nothing to compensate for. Setup used
// to run the compensating uninstall anyway, watch it get refused for the same reason,
// and print that refusal a second time as a failed restore of state it never changed.
func TestApplySetupRefusedInstallSkipsCompensationAndReportsOnce(t *testing.T) {
	const refusal = "launchd plist /Users/x/Library/LaunchAgents/sh.polter.termp.plist " +
		"belongs to a different installation: targets \"/opt/other/termp\", running " +
		"executable is \"/usr/local/bin/termp\"; re-run autostart install or uninstall " +
		"with --force to take it over"
	original := config.Default()
	original.StartAtLogin = false
	desired := original
	desired.StartAtLogin = true
	persisted := original
	uninstallCalls := 0

	result := applySetup(
		original,
		desired,
		func(next config.Config) (string, error) {
			persisted = next
			return "/tmp/config.toml", nil
		},
		// A foreign definition is refused before anything is written, and Status
		// reports it as not installed both before and after.
		func(string) error { return errors.New(refusal) },
		func() error {
			uninstallCalls++
			return errors.New(refusal)
		},
		func() (string, error) { return "/usr/local/bin/termp", nil },
		func() (bool, error) { return false, nil },
	)

	if result.err == nil {
		t.Fatal("applySetup() error = nil, want the install refusal")
	}
	if uninstallCalls != 0 {
		t.Fatalf("uninstall calls = %d, want 0: nothing was written to clean up", uninstallCalls)
	}
	if strings.Contains(result.err.Error(), "restore previous autostart state") {
		t.Fatalf("applySetup() error = %q, want no failed-restore claim", result.err)
	}
	if got := strings.Count(result.err.Error(), refusal); got != 1 {
		t.Fatalf("refusal reported %d times, want 1:\n%v", got, result.err)
	}
	if persisted.StartAtLogin {
		t.Fatal("failed install did not restore original config")
	}
}

func TestSetupCompletionChoiceIsExplicitDefaultOffAndInstallsOnConfirm(t *testing.T) {
	const completionPath = "/tmp/home/.config/fish/completions/termp.fish"
	installCalls := 0
	cfg := config.Default()
	model := NewSetupModel(cfg, nil, nil, nil, nil).WithCompletion(
		"fish",
		completionPath,
		"",
		func() ([]string, error) {
			installCalls++
			return []string{completionPath}, nil
		},
	)

	if model.completionChoice < 0 || model.choices[model.completionChoice].value {
		t.Fatal("completion choice should exist and default to Off")
	}
	wantPrompt := "Install shell completion for fish? (writes " + completionPath + ")"
	if !strings.Contains(model.View(), wantPrompt) {
		t.Fatalf("setup view does not show exact completion destination %q:\n%s", wantPrompt, model.View())
	}

	model.cursor = model.completionChoice
	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeySpace})
	model = updated.(SetupModel)
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(SetupModel)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(SetupModel)
	if cmd == nil || installCalls != 0 {
		t.Fatal("confirmation should return a command without installing inline")
	}
	updated, _ = model.Update(cmd())
	model = updated.(SetupModel)

	if installCalls != 1 || !model.Applied() {
		t.Fatalf("completion install calls = %d, applied = %t; want 1/true", installCalls, model.Applied())
	}
	if !reflect.DeepEqual(model.SetupConfig(), cfg) || model.Err() != nil {
		t.Fatalf("full-success model config/error = %#v/%v, want %#v/nil", model.SetupConfig(), model.Err(), cfg)
	}
	if !strings.Contains(model.View(), "Completion: installed: "+completionPath) {
		t.Fatalf("setup summary does not report modified completion path:\n%s", model.View())
	}
}

func TestSetupCompletionFailureReportsPartialSuccessAndAdoptsPersistedConfig(t *testing.T) {
	original := config.Default()
	var persisted config.Config
	saveCalls := 0
	model := NewSetupModel(
		original,
		func(cfg config.Config) (string, error) {
			saveCalls++
			persisted = cfg
			return "/tmp/config.toml", nil
		},
		nil,
		nil,
		nil,
	).WithCompletion(
		"fish",
		"/tmp/home/.config/fish/completions/termp.fish",
		"",
		func() ([]string, error) {
			return nil, errors.New("permission denied")
		},
	)
	model.choices[1].value = !original.AutoUpdate
	model.choices[model.completionChoice].value = true

	updated, cmd := model.startApply()
	model = updated.(SetupModel)
	updated, _ = model.Update(cmd())
	model = updated.(SetupModel)

	if saveCalls != 1 {
		t.Fatalf("save calls = %d, want 1", saveCalls)
	}
	if !reflect.DeepEqual(model.SetupConfig(), persisted) || model.SetupConfig().AutoUpdate == original.AutoUpdate {
		t.Fatalf("model config = %#v, persisted = %#v, original = %#v", model.SetupConfig(), persisted, original)
	}
	if !model.Applied() || model.Err() != nil {
		t.Fatalf("partial-success applied/error = %t/%v, want true/nil", model.Applied(), model.Err())
	}
	view := model.View()
	for _, want := range []string{
		"Setup applied.",
		"Config: /tmp/config.toml",
		"Completion: failed: install shell completion: permission denied",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("partial-success summary missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "setup failed:") {
		t.Fatalf("partial-success summary reports total failure:\n%s", view)
	}
}

func TestSetupApplyFailureStillReportsTotalFailure(t *testing.T) {
	original := config.Default()
	completionCalls := 0
	model := NewSetupModel(
		original,
		func(config.Config) (string, error) {
			return "", errors.New("save failed")
		},
		nil,
		nil,
		nil,
	).WithCompletion(
		"fish",
		"/tmp/home/.config/fish/completions/termp.fish",
		"",
		func() ([]string, error) {
			completionCalls++
			return nil, nil
		},
	)
	model.choices[1].value = !original.AutoUpdate
	model.choices[model.completionChoice].value = true

	updated, cmd := model.startApply()
	model = updated.(SetupModel)
	updated, _ = model.Update(cmd())
	model = updated.(SetupModel)

	if completionCalls != 0 {
		t.Fatalf("completion calls = %d, want 0", completionCalls)
	}
	if model.Applied() || model.Err() == nil {
		t.Fatalf("total-failure applied/error = %t/%v, want false/non-nil", model.Applied(), model.Err())
	}
	if !reflect.DeepEqual(model.SetupConfig(), original) {
		t.Fatalf("total-failure model config = %#v, want original %#v", model.SetupConfig(), original)
	}
	view := model.View()
	if !strings.Contains(view, "setup failed: save failed") || strings.Contains(view, "Setup applied.") {
		t.Fatalf("total-failure view does not report only failure:\n%s", view)
	}
}

func TestSetupCompletionChoiceSkipsInstallByDefault(t *testing.T) {
	installCalls := 0
	model := NewSetupModel(config.Default(), nil, nil, nil, nil).WithCompletion(
		"bash",
		"/tmp/home/.local/share/bash-completion/completions/termp",
		"",
		func() ([]string, error) {
			installCalls++
			return nil, nil
		},
	)

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(SetupModel)
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(SetupModel)
	updated, _ = model.Update(cmd())
	model = updated.(SetupModel)

	if installCalls != 0 || !strings.Contains(model.View(), "Completion: not installed") {
		t.Fatalf("default-off completion state = calls:%d\n%s", installCalls, model.View())
	}
}

func TestSetupViewSanitizesExternallyDerivedOutput(t *testing.T) {
	const (
		osc52 = "\x1b]52;c;Y2xpcGJvYXJk\x07"
		erase = "\x1b[2J"
	)
	model := NewSetupModel(config.Default(), nil, nil, nil, nil).WithCompletion(
		"fi"+osc52+"sh",
		"/tmp/"+erase+"completion",
		"",
		func() ([]string, error) { return nil, nil },
	)

	view := model.View()
	for _, control := range []string{osc52, erase} {
		if strings.Contains(view, control) {
			t.Fatalf("setup table emitted terminal control sequence %q:\n%s", control, view)
		}
	}
	if !strings.Contains(view, "Install shell completion for fish? (writes /tmp/completion)") {
		t.Fatalf("setup table missing sanitized completion choice:\n%s", view)
	}

	model.err = errors.New("install " + osc52 + "failed")
	view = model.View()
	if strings.Contains(view, osc52) || !strings.Contains(view, "setup failed: install failed") {
		t.Fatalf("setup error was not sanitized:\n%s", view)
	}

	model.step = 2
	model.path = "/tmp/" + osc52 + "config.toml"
	model.autostart = "in" + erase + "stalled"
	model.completion = "installed: /tmp/completion\n" + osc52 + "restart shell"
	view = model.View()
	for _, control := range []string{osc52, erase} {
		if strings.Contains(view, control) {
			t.Fatalf("setup summary emitted terminal control sequence %q:\n%s", control, view)
		}
	}
	for _, want := range []string{
		"Config: /tmp/config.toml",
		"Autostart: installed",
		"Completion: installed: /tmp/completionrestart shell",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("setup summary missing sanitized output %q:\n%s", want, view)
		}
	}
}
