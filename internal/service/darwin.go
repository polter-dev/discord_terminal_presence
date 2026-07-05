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
	_ = s.unload(path)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return State{Supported: true, Path: path}, err
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
		return fmt.Errorf("launchctl load failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (s darwinService) unload(path string) error {
	domain := "gui/" + currentUID()
	if out, err := s.runner.Run("launchctl", "bootout", domain, path); err == nil {
		_ = out
		return nil
	}
	out, err := s.runner.Run("launchctl", "unload", "-w", path)
	if err != nil {
		return fmt.Errorf("launchctl unload failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
