package service

import (
	"fmt"
	"strings"
)

type windowsService struct {
	runner Runner
}

func (s windowsService) Install(exe string) (State, error) {
	if out, err := s.runner.Run(
		"schtasks",
		"/Create",
		"/TN", TaskName,
		"/TR", `"`+exe+`" start`,
		"/SC", "ONLOGON",
		"/RL", "LIMITED",
		"/F",
	); err != nil {
		return State{Supported: true, Path: TaskName}, fmt.Errorf("schtasks create failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return s.Status(), nil
}

func (s windowsService) Uninstall() (State, error) {
	if out, err := s.runner.Run("schtasks", "/Delete", "/TN", TaskName, "/F"); err != nil {
		return State{Supported: true, Path: TaskName}, fmt.Errorf("schtasks delete failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return s.Status(), nil
}

func (s windowsService) Disable() (State, error) {
	if status := s.Status(); !status.Installed {
		return status, nil
	}
	if out, err := s.runner.Run("schtasks", "/Change", "/TN", TaskName, "/DISABLE"); err != nil {
		return State{Supported: true, Installed: true, Path: TaskName}, fmt.Errorf("schtasks disable failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	_, _ = s.runner.Run("schtasks", "/End", "/TN", TaskName)
	return s.Status(), nil
}

func (s windowsService) Enable() (State, error) {
	if status := s.Status(); !status.Installed {
		return status, nil
	}
	if out, err := s.runner.Run("schtasks", "/Change", "/TN", TaskName, "/ENABLE"); err != nil {
		return State{Supported: true, Installed: true, Path: TaskName}, fmt.Errorf("schtasks enable failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return s.Status(), nil
}

func (s windowsService) Status() State {
	state := State{Supported: true, Path: TaskName, Loaded: "unknown", Enabled: "unknown"}
	if out, err := s.runner.Run("schtasks", "/Query", "/TN", TaskName); err == nil {
		_ = out
		state.Installed = true
		state.Loaded = "true"
		state.Enabled = "true"
	} else {
		state.Installed = false
		state.Loaded = "false"
		state.Enabled = "false"
	}
	return state
}
