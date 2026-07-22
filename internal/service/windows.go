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
	status := s.Status()
	if status.Message != "" {
		return status, fmt.Errorf("%s", status.Message)
	}
	if !status.Installed {
		return status, nil
	}
	if out, err := s.runner.Run("schtasks", "/Change", "/TN", TaskName, "/DISABLE"); err != nil {
		if isTaskNotFound(out, err) {
			return State{Supported: true, Path: TaskName, Loaded: "false", Enabled: "false"}, nil
		}
		return State{Supported: true, Installed: true, Path: TaskName}, fmt.Errorf("schtasks disable failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	_, _ = s.runner.Run("schtasks", "/End", "/TN", TaskName)
	status = s.Status()
	if status.Message != "" {
		return status, fmt.Errorf("%s", status.Message)
	}
	return status, nil
}

func (s windowsService) Enable() (State, error) {
	status := s.Status()
	if status.Message != "" {
		return status, fmt.Errorf("%s", status.Message)
	}
	if !status.Installed {
		return status, nil
	}
	if out, err := s.runner.Run("schtasks", "/Change", "/TN", TaskName, "/ENABLE"); err != nil {
		if isTaskNotFound(out, err) {
			return State{Supported: true, Path: TaskName, Loaded: "false", Enabled: "false"}, nil
		}
		return State{Supported: true, Installed: true, Path: TaskName}, fmt.Errorf("schtasks enable failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	status = s.Status()
	if status.Message != "" {
		return status, fmt.Errorf("%s", status.Message)
	}
	return status, nil
}

func (s windowsService) Status() State {
	state := State{Supported: true, Path: TaskName, Loaded: "unknown", Enabled: "unknown"}
	out, err := s.runner.Run("schtasks", "/Query", "/TN", TaskName, "/FO", "LIST", "/V")
	if err != nil {
		if isTaskNotFound(out, err) {
			state.Loaded = "false"
			state.Enabled = "false"
			return state
		}
		state.Installed = true
		state.Message = fmt.Sprintf("schtasks query failed: %v: %s", err, strings.TrimSpace(string(out)))
		return state
	}
	state.Installed = true
	state.Loaded = "true"
	state.Enabled = "true"
	if strings.EqualFold(windowsTaskStatus(out), "Disabled") {
		state.Loaded = "false"
		state.Enabled = "false"
	}
	return state
}

// isTaskNotFound best-effort matches the English schtasks absence messages.
// Localized Windows output may not match and will be reported as an operational
// query failure instead of being mistaken for a task that is not installed.
func isTaskNotFound(out []byte, err error) bool {
	text := string(out)
	if err != nil {
		text += "\n" + err.Error()
	}
	for _, line := range strings.Split(text, "\n") {
		line = strings.ToLower(strings.TrimSpace(line))
		if !strings.HasPrefix(line, "error:") {
			continue
		}
		if strings.Contains(line, "cannot find") || strings.Contains(line, "does not exist") {
			return true
		}
	}
	return false
}

func windowsTaskStatus(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "Status") {
			continue
		}
		return strings.TrimSpace(value)
	}
	return ""
}
