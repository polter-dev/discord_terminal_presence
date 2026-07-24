package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"
	"unicode/utf16"
)

type windowsService struct {
	runner Runner
}

func (s windowsService) Install(exe string) (State, error) {
	username := ""
	if current, err := user.Current(); err == nil && current != nil {
		username = strings.TrimSpace(current.Username)
	}
	if username == "" {
		envUsername := strings.TrimSpace(os.Getenv("USERNAME"))
		if domain := strings.TrimSpace(os.Getenv("USERDOMAIN")); domain != "" && envUsername != "" {
			username = domain + `\` + envUsername
		} else {
			username = envUsername
		}
	}
	if username == "" {
		return State{Supported: true, Path: TaskName}, fmt.Errorf("cannot resolve current user for scheduled task")
	}

	taskXML, err := BuildWindowsTaskXML(exe, username)
	if err != nil {
		return State{Supported: true, Path: TaskName}, err
	}
	xmlFile, err := os.CreateTemp("", "termp-autostart-*.xml")
	if err != nil {
		return State{Supported: true, Path: TaskName}, fmt.Errorf("create scheduled task XML temp file: %w", err)
	}
	xmlPath := xmlFile.Name()
	defer os.Remove(xmlPath)
	if err := xmlFile.Chmod(0o600); err != nil {
		_ = xmlFile.Close()
		return State{Supported: true, Path: TaskName}, fmt.Errorf("restrict scheduled task XML temp file: %w", err)
	}
	if _, err := xmlFile.Write(taskXML); err != nil {
		_ = xmlFile.Close()
		return State{Supported: true, Path: TaskName}, fmt.Errorf("write scheduled task XML temp file: %w", err)
	}
	if err := xmlFile.Close(); err != nil {
		return State{Supported: true, Path: TaskName}, fmt.Errorf("close scheduled task XML temp file: %w", err)
	}

	if out, err := s.runner.Run(
		"schtasks",
		"/Create",
		"/TN", TaskName,
		"/XML", xmlPath,
		"/F",
	); err != nil {
		return State{Supported: true, Path: TaskName}, fmt.Errorf("schtasks create failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := s.runTask(); err != nil {
		return State{Supported: true, Installed: true, Path: TaskName}, err
	}
	return s.Status(), nil
}

func BuildWindowsTaskXML(exe, username string) ([]byte, error) {
	const description = "Terminal Presence autostart"
	esc := func(s string) string {
		var out bytes.Buffer
		_ = xml.EscapeText(&out, []byte(s))
		return out.String()
	}
	content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Description>%s</Description></RegistrationInfo>
  <Triggers><LogonTrigger><Enabled>true</Enabled><UserId>%s</UserId></LogonTrigger></Triggers>
  <Principals><Principal id="Author">
    <UserId>%s</UserId>
    <LogonType>InteractiveToken</LogonType>
    <RunLevel>LeastPrivilege</RunLevel>
  </Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <StartWhenAvailable>true</StartWhenAvailable>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author">
    <Exec><Command>%s</Command><Arguments>start</Arguments></Exec>
  </Actions>
</Task>
`, esc(description), esc(username), esc(username), esc(exe))

	encoded := utf16.Encode([]rune(content))
	data := make([]byte, 2+len(encoded)*2)
	data[0], data[1] = 0xff, 0xfe
	for i, unit := range encoded {
		binary.LittleEndian.PutUint16(data[2+i*2:], unit)
	}
	return data, nil
}

func (s windowsService) Uninstall() (State, error) {
	// A task definition can be deleted while an instance launched from it keeps
	// running. Ending first makes uninstall stop the daemon as well. /End is
	// intentionally best-effort because an idle or already-removed task is a
	// normal uninstall state.
	_, _ = s.runner.Run("schtasks", "/End", "/TN", TaskName)
	if out, err := s.runner.Run("schtasks", "/Delete", "/TN", TaskName, "/F"); err != nil {
		if isTaskNotFound(out, err) {
			return State{Supported: true, Path: TaskName, Loaded: "false", Enabled: "false"}, nil
		}
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
	if err := s.runTask(); err != nil {
		return State{Supported: true, Installed: true, Path: TaskName}, err
	}
	status = s.Status()
	if status.Message != "" {
		return status, fmt.Errorf("%s", status.Message)
	}
	return status, nil
}

func (s windowsService) Status() State {
	return s.StatusContext(context.Background())
}

func (s windowsService) StatusContext(ctx context.Context) State {
	state := State{Supported: true, Path: TaskName, Loaded: "unknown", Enabled: "unknown"}
	out, err := runStatusCommand(ctx, s.runner, "schtasks", "/Query", "/TN", TaskName, "/XML")
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
	var task struct {
		XMLName  xml.Name `xml:"Task"`
		Settings struct {
			Enabled *bool `xml:"Enabled"`
		} `xml:"Settings"`
	}
	if err := unmarshalTaskXML(out, &task); err != nil {
		state.Message = fmt.Sprintf("schtasks query returned invalid XML: %v", err)
		return state
	}
	// Task Scheduler's schema defaults Settings/Enabled to true when the
	// element is omitted.
	state.Enabled = "true"
	if task.Settings.Enabled != nil {
		state.Enabled = fmt.Sprintf("%t", *task.Settings.Enabled)
	}
	// The XML query gives the registered task definition, not a locale-stable
	// runtime status. For autostart, a registered enabled logon task is loaded
	// in the cross-platform sense: the OS scheduler has it and will launch it.
	state.Loaded = state.Enabled
	return state
}

func unmarshalTaskXML(data []byte, value any) error {
	if len(data) >= 2 {
		var byteOrder binary.ByteOrder
		switch {
		case data[0] == 0xff && data[1] == 0xfe:
			byteOrder = binary.LittleEndian
			data = data[2:]
		case data[0] == 0xfe && data[1] == 0xff:
			byteOrder = binary.BigEndian
			data = data[2:]
		case data[0] == '<' && data[1] == 0:
			byteOrder = binary.LittleEndian
		case data[0] == 0 && data[1] == '<':
			byteOrder = binary.BigEndian
		}
		if byteOrder != nil {
			codeUnits := make([]uint16, len(data)/2)
			for i := range codeUnits {
				codeUnits[i] = byteOrder.Uint16(data[i*2:])
			}
			data = []byte(string(utf16.Decode(codeUnits)))
		}
	}

	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.CharsetReader = func(label string, input io.Reader) (io.Reader, error) {
		if strings.EqualFold(label, "utf-16") {
			return input, nil
		}
		return nil, fmt.Errorf("unsupported XML encoding %q", label)
	}
	return decoder.Decode(value)
}

func (s windowsService) runTask() error {
	out, err := s.runner.Run("schtasks", "/Run", "/TN", TaskName)
	if err == nil || isTaskNotFound(out, err) || isTaskAlreadyRunning(out, err) {
		return nil
	}
	return fmt.Errorf("schtasks run failed: %w: %s", err, strings.TrimSpace(string(out)))
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

func isTaskAlreadyRunning(out []byte, err error) bool {
	text := string(out)
	if err != nil {
		text += "\n" + err.Error()
	}
	return containsAnyFold(text, "already running", "instance of this task is currently running")
}
