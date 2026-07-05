package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type linuxService struct {
	runner Runner
}

func (s linuxService) Install(exe string) (State, error) {
	path, err := systemdUnitPath()
	if err != nil {
		return State{Supported: true}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return State{Supported: true, Path: path}, err
	}
	if err := os.WriteFile(path, BuildSystemdUnit(exe), 0o644); err != nil {
		return State{Supported: true, Path: path}, err
	}
	if out, err := s.runner.Run("systemctl", "--user", "daemon-reload"); err != nil {
		return State{Supported: true, Installed: true, Path: path}, fmt.Errorf("systemctl daemon-reload failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := s.runner.Run("systemctl", "--user", "enable", "--now", ServiceName); err != nil {
		return State{Supported: true, Installed: true, Path: path}, fmt.Errorf("systemctl enable failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return s.Status(), nil
}

func (s linuxService) Uninstall() (State, error) {
	path, err := systemdUnitPath()
	if err != nil {
		return State{Supported: true}, err
	}
	_, _ = s.runner.Run("systemctl", "--user", "disable", "--now", ServiceName)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return State{Supported: true, Path: path}, err
	}
	_, _ = s.runner.Run("systemctl", "--user", "daemon-reload")
	return s.Status(), nil
}

func (s linuxService) Status() State {
	path, err := systemdUnitPath()
	if err != nil {
		return State{Supported: true, Loaded: "unknown", Enabled: "unknown"}
	}
	state := State{Supported: true, Path: path, Loaded: "unknown", Enabled: "unknown"}
	if _, err := os.Stat(path); err == nil {
		state.Installed = true
	} else if os.IsNotExist(err) {
		state.Installed = false
	}
	if out, err := s.runner.Run("systemctl", "--user", "is-enabled", ServiceName); err == nil {
		state.Enabled = strings.TrimSpace(string(out))
	} else {
		state.Enabled = "unknown"
	}
	if out, err := s.runner.Run("systemctl", "--user", "is-active", ServiceName); err == nil {
		state.Loaded = strings.TrimSpace(string(out))
	} else {
		state.Loaded = "unknown"
	}
	return state
}
