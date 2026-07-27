package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type darwinService struct {
	runner     Runner
	executable string
}

func (s darwinService) Install(exe string, force bool) (State, error) {
	return s.install(exe, true, force)
}

func (s darwinService) install(exe string, launch, force bool) (State, error) {
	status := s.Status()
	if status.ForeignTask && !force {
		return status, foreignTaskError(status.Message)
	}
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
	if launch {
		if err := s.unload(path); err != nil {
			return State{Supported: true, Path: path}, fmt.Errorf("cannot replace launch agent before unloading the existing job: %w", err)
		}
	}
	content, err := BuildLaunchAgentPlist(exe, logPath)
	if err != nil {
		return State{Supported: true, Path: path}, err
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return State{Supported: true, Path: path}, err
	}
	if launch {
		if err := s.load(path); err != nil {
			return State{Supported: true, Installed: true, Path: path}, err
		}
	}
	return s.Status(), nil
}

func (s darwinService) Uninstall(force bool) (State, error) {
	status := s.Status()
	if status.ForeignTask && !force {
		return status, foreignTaskError(status.Message)
	}
	path, err := launchAgentPath()
	if err != nil {
		return State{Supported: true}, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return status, nil
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
	status := s.Status()
	if status.ForeignTask {
		return status, foreignTaskError(status.Message)
	}
	path, err := launchAgentPath()
	if err != nil {
		return State{Supported: true}, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return status, nil
	} else if err != nil {
		return State{Supported: true, Path: path}, err
	}
	if err := s.unload(path); err != nil {
		return State{Supported: true, Installed: true, Path: path}, err
	}
	return s.Status(), nil
}

func (s darwinService) Enable() (State, error) {
	status := s.Status()
	if status.ForeignTask {
		return status, foreignTaskError(status.Message)
	}
	path, err := launchAgentPath()
	if err != nil {
		return State{Supported: true}, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return status, nil
	} else if err != nil {
		return State{Supported: true, Path: path}, err
	}
	if err := s.load(path); err != nil {
		return State{Supported: true, Installed: true, Path: path}, err
	}
	return s.Status(), nil
}

func (s darwinService) Status() State {
	return s.StatusContext(context.Background())
}

func (s darwinService) StatusContext(ctx context.Context) State {
	path, err := launchAgentPath()
	if err != nil {
		return State{Supported: true, Loaded: "unknown", Enabled: "unknown"}
	}
	state := State{Supported: true, Path: path, Loaded: "unknown", Enabled: "n/a"}
	if _, err := os.Stat(path); err == nil {
		state.Installed = true
		if definition, readErr := os.ReadFile(path); readErr == nil {
			if target, parseErr := launchAgentExecutable(definition); parseErr == nil &&
				isForeignUnixExecutable(target, s.executable) {
				state.Installed = false
				state.Loaded = "false"
				state.Enabled = "false"
				state.ForeignTask = true
				state.Message = fmt.Sprintf(
					"launchd plist %s belongs to a different installation: targets %q, running executable is %q",
					path, target, s.executable,
				)
				return state
			}
		}
	} else if os.IsNotExist(err) {
		state.Loaded = "false"
		return state
	}
	if out, err := runStatusCommand(ctx, s.runner, "launchctl", "print", "gui/"+currentUID()+"/"+Label); err == nil {
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

func launchAgentExecutable(plist []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(plist))
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				return "", fmt.Errorf("launchd plist has no ProgramArguments executable")
			}
			return "", fmt.Errorf("parse launchd plist: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "key" {
			continue
		}
		var key string
		if err := decoder.DecodeElement(&key, &start); err != nil {
			return "", fmt.Errorf("parse launchd plist key: %w", err)
		}
		if strings.TrimSpace(key) != "ProgramArguments" {
			continue
		}
		return launchAgentFirstProgramArgument(decoder)
	}
}

func launchAgentFirstProgramArgument(decoder *xml.Decoder) (string, error) {
	for {
		token, err := decoder.Token()
		if err != nil {
			return "", fmt.Errorf("parse launchd ProgramArguments: %w", err)
		}
		start, ok := token.(xml.StartElement)
		if !ok {
			continue
		}
		if start.Name.Local != "array" {
			return "", fmt.Errorf("launchd ProgramArguments is not an array")
		}
		for {
			token, err := decoder.Token()
			if err != nil {
				return "", fmt.Errorf("parse launchd ProgramArguments array: %w", err)
			}
			switch element := token.(type) {
			case xml.StartElement:
				if element.Name.Local != "string" {
					return "", fmt.Errorf("launchd ProgramArguments first value is not a string")
				}
				var executable string
				if err := decoder.DecodeElement(&executable, &element); err != nil {
					return "", fmt.Errorf("parse launchd executable: %w", err)
				}
				executable = strings.TrimSpace(executable)
				if executable == "" {
					return "", fmt.Errorf("launchd ProgramArguments executable is empty")
				}
				return executable, nil
			case xml.EndElement:
				if element.Name.Local == "array" {
					return "", fmt.Errorf("launchd ProgramArguments is empty")
				}
			}
		}
	}
}
