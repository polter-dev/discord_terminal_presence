package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type linuxService struct {
	runner     Runner
	executable string
}

func (s linuxService) Install(exe string, force bool) (State, error) {
	return s.install(exe, true, force)
}

func (s linuxService) install(exe string, launch, force bool) (State, error) {
	status := s.Status()
	if status.ForeignTask && !force {
		return status, foreignTaskError(status.Message)
	}
	path, err := systemdUnitPath()
	if err != nil {
		return State{Supported: true}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return State{Supported: true, Path: path}, err
	}
	previous, err := snapshotDefinition(path)
	if err != nil {
		return State{Supported: true, Path: path}, fmt.Errorf("snapshot systemd unit definition: %w", err)
	}
	unit, err := BuildSystemdUnit(exe)
	if err != nil {
		return State{Supported: true, Path: path}, err
	}
	if err := os.WriteFile(path, unit, 0o644); err != nil {
		return State{Supported: true, Path: path}, err
	}
	if out, err := s.runner.Run("systemctl", "--user", "daemon-reload"); err != nil {
		activationErr := fmt.Errorf("systemctl daemon-reload failed: %w: %s", err, strings.TrimSpace(string(out)))
		rollbackErr := s.rollbackInstall(path, previous, status, false)
		return State{Supported: true, Installed: previous.exists, Path: path}, errors.Join(activationErr, rollbackErr)
	}
	enableArgs := []string{"--user", "enable"}
	if launch {
		enableArgs = append(enableArgs, "--now")
	}
	enableArgs = append(enableArgs, ServiceName)
	if out, err := s.runner.Run("systemctl", enableArgs...); err != nil {
		activationErr := fmt.Errorf("systemctl enable failed: %w: %s", err, strings.TrimSpace(string(out)))
		rollbackErr := s.rollbackInstall(path, previous, status, true)
		return State{Supported: true, Installed: previous.exists, Path: path}, errors.Join(activationErr, rollbackErr)
	}
	return s.Status(), nil
}

func (s linuxService) rollbackInstall(path string, previous definitionSnapshot, prior State, activationAttempted bool) error {
	var rollbackErrs []error
	stopped := true
	if activationAttempted {
		if out, err := s.runner.Run("systemctl", "--user", "disable", "--now", ServiceName); err != nil {
			stopped = false
			rollbackErrs = append(rollbackErrs, fmt.Errorf("stop failed systemd service during rollback: %w: %s", err, strings.TrimSpace(string(out))))
		}
	}
	restored := true
	if err := restoreDefinition(path, previous); err != nil {
		restored = false
		rollbackErrs = append(rollbackErrs, fmt.Errorf("restore previous systemd unit definition: %w", err))
	}
	if restored {
		if out, err := s.runner.Run("systemctl", "--user", "daemon-reload"); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("reload restored systemd unit: %w: %s", err, strings.TrimSpace(string(out))))
			restored = false
		}
	}
	if !activationAttempted || !previous.exists {
		return errors.Join(rollbackErrs...)
	}
	// Enablement and activation are independent dimensions. An unknown reading of
	// one of them is reported and left alone (fail closed), but it must not discard
	// the other one when that one is positively known.
	if prior.Enabled == "unknown" || prior.Loaded == "unknown" {
		rollbackErrs = append(rollbackErrs, fmt.Errorf(
			"the previous autostart activation state for %s could not be determined and may need checking; run `systemctl --user status %s` to check it",
			ServiceName,
			ServiceName,
		))
	}
	// Do not restore prior activation while the failed replacement may still be
	// running or while the previous definition is not back on disk.
	if !restored || !stopped {
		return errors.Join(rollbackErrs...)
	}
	if prior.Enabled == "enabled" {
		if out, err := s.runner.Run("systemctl", "--user", "enable", ServiceName); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore previous systemd enablement: %w: %s", err, strings.TrimSpace(string(out))))
		}
	}
	if prior.Loaded == "active" {
		if out, err := s.runner.Run("systemctl", "--user", "start", ServiceName); err != nil {
			rollbackErrs = append(rollbackErrs, fmt.Errorf("restore previous systemd activation: %w: %s", err, strings.TrimSpace(string(out))))
		}
	}
	return errors.Join(rollbackErrs...)
}

func (s linuxService) Uninstall(force bool) (State, error) {
	status := s.Status()
	if status.ForeignTask && !force {
		return status, foreignTaskError(status.Message)
	}
	path, err := systemdUnitPath()
	if err != nil {
		return State{Supported: true}, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return status, nil
	} else if err != nil {
		return State{Supported: true, Path: path}, err
	}
	if out, err := s.runner.Run("systemctl", "--user", "disable", "--now", ServiceName); err != nil && !isBenignSystemctlDisableError(out) {
		return State{Supported: true, Installed: true, Path: path}, fmt.Errorf("systemctl disable failed; service definition kept at %s so uninstall can be retried: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return State{Supported: true, Path: path}, err
	}
	if out, err := s.runner.Run("systemctl", "--user", "daemon-reload"); err != nil {
		return State{Supported: true, Path: path}, fmt.Errorf("systemctl daemon-reload failed after removing %s; retry `systemctl --user daemon-reload`: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	return s.Status(), nil
}

func isBenignSystemctlDisableError(out []byte) bool {
	text := string(out)
	if containsAnyFold(text, "failed to connect to bus") {
		return false
	}
	return containsAnyFold(text,
		"unit "+ServiceName+" not loaded",
		"unit "+ServiceName+" not found",
		"unit "+ServiceName+" could not be found",
		"unit "+ServiceName+" does not exist",
		"unit file "+ServiceName+" does not exist",
		"no such process",
	)
}

func (s linuxService) Disable() (State, error) {
	status := s.Status()
	if status.ForeignTask {
		return status, foreignTaskError(status.Message)
	}
	path, err := systemdUnitPath()
	if err != nil {
		return State{Supported: true}, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return status, nil
	} else if err != nil {
		return State{Supported: true, Path: path}, err
	}
	if out, err := s.runner.Run("systemctl", "--user", "disable", "--now", ServiceName); err != nil {
		return State{Supported: true, Installed: true, Path: path}, fmt.Errorf("systemctl disable failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return s.Status(), nil
}

func (s linuxService) Enable() (State, error) {
	status := s.Status()
	if status.ForeignTask {
		return status, foreignTaskError(status.Message)
	}
	path, err := systemdUnitPath()
	if err != nil {
		return State{Supported: true}, err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return status, nil
	} else if err != nil {
		return State{Supported: true, Path: path}, err
	}
	if out, err := s.runner.Run("systemctl", "--user", "enable", "--now", ServiceName); err != nil {
		return State{Supported: true, Installed: true, Path: path}, fmt.Errorf("systemctl enable failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return s.Status(), nil
}

func (s linuxService) Status() State {
	return s.StatusContext(context.Background())
}

func (s linuxService) StatusContext(ctx context.Context) State {
	path, err := systemdUnitPath()
	if err != nil {
		return State{Supported: true, Loaded: "unknown", Enabled: "unknown"}
	}
	state := State{Supported: true, Path: path, Loaded: "unknown", Enabled: "unknown"}
	if _, err := os.Stat(path); err == nil {
		state.Installed = true
		definition, readErr := os.ReadFile(path)
		if readErr != nil {
			state.Installed = false
			state.Loaded = "false"
			state.Enabled = "false"
			state.ForeignTask = true
			state.Message = fmt.Sprintf(
				"systemd unit %s ownership could not be verified because the definition could not be read: %v",
				path, readErr,
			)
			return state
		}
		target, parseErr := systemdUnitExecutable(definition)
		switch {
		case parseErr != nil:
			state.Installed = false
			state.Loaded = "false"
			state.Enabled = "false"
			state.ForeignTask = true
			state.Message = fmt.Sprintf(
				"systemd unit %s ownership could not be verified because it could not be parsed: %v",
				path, parseErr,
			)
			return state
		case s.executable == "":
			state.Installed = false
			state.Loaded = "false"
			state.Enabled = "false"
			state.ForeignTask = true
			state.Message = fmt.Sprintf(
				"systemd unit %s ownership could not be verified because the running termp executable could not be resolved",
				path,
			)
			return state
		case isForeignUnixExecutable(target, s.executable):
			state.Installed = false
			state.Loaded = "false"
			state.Enabled = "false"
			state.ForeignTask = true
			state.Message = fmt.Sprintf(
				"systemd unit %s belongs to a different installation: targets %q, running executable is %q",
				path, target, s.executable,
			)
			return state
		}
	} else if os.IsNotExist(err) {
		state.Installed = false
	}
	out, _ := runStatusCommand(ctx, s.runner, "systemctl", "--user", "is-enabled", ServiceName)
	state.Enabled = systemctlState(out, "enabled", "disabled", "static", "masked", "linked")
	out, _ = runStatusCommand(ctx, s.runner, "systemctl", "--user", "is-active", ServiceName)
	state.Loaded = systemctlState(out, "active", "inactive", "failed", "activating", "deactivating")
	return state
}

func systemctlState(out []byte, documented ...string) string {
	state := strings.TrimSpace(string(out))
	for _, candidate := range documented {
		if state == candidate {
			return state
		}
	}
	return "unknown"
}

func systemdUnitExecutable(unit []byte) (string, error) {
	section := ""
	lines := strings.Split(strings.ReplaceAll(string(unit), "\r\n", "\n"), "\n")
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		if section != "Service" {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found || strings.TrimSpace(key) != "ExecStart" || strings.TrimSpace(value) == "" {
			continue
		}
		executable, err := systemdFirstWord(strings.TrimSpace(value))
		if err != nil {
			return "", fmt.Errorf("parse systemd ExecStart: %w", err)
		}
		for len(executable) > 0 && strings.ContainsRune("-@:+!|", rune(executable[0])) {
			executable = executable[1:]
		}
		return decodeSystemdPercentEscapes(executable)
	}
	return "", fmt.Errorf("systemd unit has no ExecStart executable")
}

func systemdFirstWord(value string) (string, error) {
	var word strings.Builder
	var quote byte
	for index := 0; index < len(value); index++ {
		character := value[index]
		if quote == 0 && (character == ' ' || character == '\t') {
			if word.Len() > 0 {
				return word.String(), nil
			}
			continue
		}
		if character == '\'' || character == '"' {
			if quote == 0 {
				quote = character
				continue
			}
			if quote == character {
				quote = 0
				continue
			}
		}
		if character == '\\' {
			index++
			if index >= len(value) {
				return "", fmt.Errorf("trailing backslash")
			}
			if !strings.ContainsRune(`\'"`, rune(value[index])) {
				return "", fmt.Errorf("unsupported escape sequence \\%c", value[index])
			}
			word.WriteByte(value[index])
			continue
		}
		word.WriteByte(character)
	}
	if quote != 0 {
		return "", fmt.Errorf("unterminated quote")
	}
	if word.Len() == 0 {
		return "", fmt.Errorf("empty command")
	}
	return word.String(), nil
}

func decodeSystemdPercentEscapes(value string) (string, error) {
	var decoded strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '%' {
			decoded.WriteByte(value[index])
			continue
		}
		if index+1 >= len(value) || value[index+1] != '%' {
			return "", fmt.Errorf("systemd executable uses a dynamic specifier")
		}
		decoded.WriteByte('%')
		index++
	}
	return decoded.String(), nil
}
