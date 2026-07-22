package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type darwinService struct {
	runner Runner
}

func (s darwinService) Install(exe string) (State, error) {
	path, err := launchAgentPath()
	if err != nil {
		return State{Supported: true}, err
	}
	logPath, err := launchAgentLogPath()
	if err != nil {
		return State{Supported: true, Path: path}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return State{Supported: true, Path: path}, err
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return State{Supported: true, Path: path}, err
	}
	_ = s.unload(path)
	content, err := BuildLaunchAgentPlist(exe, logPath)
	if err != nil {
		return State{Supported: true, Path: path}, err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return State{Supported: true, Path: path}, err
	}
	if err := s.load(path); err != nil {
		return State{Supported: true, Installed: true, Path: path}, err
	}
	return s.Status(), nil
}

func (s darwinService) Uninstall() (State, error) {
	path, err := launchAgentPath()
	if err != nil {
		return State{Supported: true}, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return s.Status(), nil
	} else if err != nil {
		return State{Supported: true, Path: path}, err
	}
	if err := s.unload(path); err != nil {
		return State{Supported: true, Installed: true, Path: path}, fmt.Errorf("%w; service definition kept at %s so uninstall can be retried", err, path)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return State{Supported: true, Path: path}, err
	}
	return s.Status(), nil
}

func (s darwinService) Disable() (State, error) {
	path, err := launchAgentPath()
	if err != nil {
		return State{Supported: true}, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return s.Status(), nil
	} else if err != nil {
		return State{Supported: true, Path: path}, err
	}
	if err := s.unload(path); err != nil {
		return State{Supported: true, Installed: true, Path: path}, err
	}
	return s.Status(), nil
}

func (s darwinService) Enable() (State, error) {
	path, err := launchAgentPath()
	if err != nil {
		return State{Supported: true}, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return s.Status(), nil
	} else if err != nil {
		return State{Supported: true, Path: path}, err
	}
	if err := s.load(path); err != nil {
		return State{Supported: true, Installed: true, Path: path}, err
	}
	return s.Status(), nil
}

func (s darwinService) Status() State {
	path, err := launchAgentPath()
	if err != nil {
		return State{Supported: true, Loaded: "unknown", Enabled: "unknown"}
	}
	state := State{Supported: true, Path: path, Loaded: "unknown", Enabled: "n/a"}
	if _, err := os.Stat(path); err == nil {
		state.Installed = true
	} else if os.IsNotExist(err) {
		state.Loaded = "false"
		return state
	}
	if out, err := s.runner.Run("launchctl", "print", "gui/"+currentUID()+"/"+Label); err == nil {
		state.Loaded = "true"
		_ = out
	} else if isLaunchctlServiceNotFoundError(out) {
		state.Loaded = "false"
	} else {
		state.Loaded = "unknown"
	}
	return state
}

func (s darwinService) load(path string) error {
	domain := "gui/" + currentUID()
	if out, err := s.runner.Run("launchctl", "bootstrap", domain, path); err == nil {
		_ = out
		return nil
	}
	out, err := s.runner.Run("launchctl", "load", "-w", path)
	if err != nil {
		if isBenignLaunchctlLoadError(out) {
			return nil
		}
		return fmt.Errorf("launchctl load failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s darwinService) unload(path string) error {
	domain := "gui/" + currentUID()
	bootoutOut, bootoutErr := s.runner.Run("launchctl", "bootout", domain, path)
	if bootoutErr == nil || isBenignLaunchctlUnloadError(bootoutOut) {
		return nil
	}
	out, err := s.runner.Run("launchctl", "unload", "-w", path)
	if err != nil {
		if isBenignLaunchctlUnloadError(out) {
			return nil
		}
		return fmt.Errorf(
			"launchctl bootout failed: %v: %s; launchctl unload failed: %w: %s",
			bootoutErr,
			strings.TrimSpace(string(bootoutOut)),
			err,
			strings.TrimSpace(string(out)),
		)
	}
	return nil
}

func isBenignLaunchctlLoadError(out []byte) bool {
	return containsAnyFold(string(out),
		"already loaded",
		"service already exists",
	)
}

func isBenignLaunchctlUnloadError(out []byte) bool {
	return containsAnyFold(string(out),
		"could not find specified service",
		"no such process",
		"not found",
		"not loaded",
		"service is not loaded",
	)
}

func isLaunchctlServiceNotFoundError(out []byte) bool {
	return containsAnyFold(string(out),
		"could not find service",
		"could not find specified service",
		"no such service",
		"service not found",
	)
}

func containsAnyFold(text string, needles ...string) bool {
	text = strings.ToLower(text)
	for _, needle := range needles {
		if strings.Contains(text, strings.ToLower(needle)) {
			return true
		}
	}
	return false
}
