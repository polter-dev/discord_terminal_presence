package tui

import (
	"errors"
	"testing"

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
			name: "unchanged enabled is already enabled", original: true, desired: true, installed: true,
			wantAutostart: "already enabled",
		},
		{
			name: "changed enabled installs when missing", original: false, desired: true,
			wantInstall: 1, wantExecutable: 1, wantAutostart: "installed",
		},
		{
			name: "changed enabled does not reinstall", original: false, desired: true, installed: true,
			wantAutostart: "already enabled",
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
