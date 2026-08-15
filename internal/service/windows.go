package service

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

type windowsService struct {
	runner     Runner
	executable string
}

func (s windowsService) Install(exe string, force bool) (State, error) {
	return s.install(exe, true, force)
}

func (s windowsService) install(exe string, launch, force bool) (State, error) {
	status := s.Status()
	if status.ForeignTask && !force {
		return status, foreignTaskError(status.Message)
	}
	// Persist the stable invocation path exactly as it was resolved for install
	// (a Scoop `current` junction or shim, a Homebrew/hand-placed path, etc.).
	// Deliberately do NOT junction-follow here: canonicalizing through Scoop's
	// `apps\termp\current` junction would bake a versioned `apps\termp\<version>`
	// directory into the task, which `scoop update` later deletes, silently
	// killing autostart (issue #502). Junction/symlink resolution stays confined
	// to the ownership-identity comparison (ownsWindowsTaskCommand ->
	// sameWindowsExecutable), which must still recognize the task after an update.
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

	command, arguments, err := windowsTaskExec(exe)
	if err != nil {
		return State{Supported: true, Path: TaskName}, err
	}
	taskXML, err := BuildWindowsTaskXML(command, arguments, username)
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
	if launch {
		if err := s.runTask(); err != nil {
			return State{Supported: true, Installed: true, Path: TaskName}, err
		}
	}
	return s.Status(), nil
}

// BuildWindowsTaskXML renders the logon task definition that runs command with
// arguments. command is normally the companion launcher (termpw.exe), which
// takes no arguments; when the launcher is unavailable it falls back to the
// daemon itself with "start --foreground".
func BuildWindowsTaskXML(command, arguments, username string) ([]byte, error) {
	const description = "Terminal Presence autostart"
	esc := func(s string) string {
		var out bytes.Buffer
		_ = xml.EscapeText(&out, []byte(s))
		return out.String()
	}
	argumentsElement := ""
	if arguments != "" {
		argumentsElement = fmt.Sprintf("<Arguments>%s</Arguments>", esc(arguments))
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
    <RestartOnFailure><Interval>PT1M</Interval><Count>3</Count></RestartOnFailure>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Enabled>true</Enabled>
  </Settings>
  <Actions Context="Author">
    <Exec><Command>%s</Command>%s</Exec>
  </Actions>
</Task>
`, esc(description), esc(username), esc(username), esc(command), argumentsElement)

	encoded := utf16.Encode([]rune(content))
	data := make([]byte, 2+len(encoded)*2)
	data[0], data[1] = 0xff, 0xfe
	for i, unit := range encoded {
		binary.LittleEndian.PutUint16(data[2+i*2:], unit)
	}
	return data, nil
}

func (s windowsService) Uninstall(force bool) (State, error) {
	status := s.Status()
	if status.ForeignTask && !force {
		return status, foreignTaskError(status.Message)
	}
	if !status.Installed && !status.ForeignTask {
		return status, nil
	}
	// A task definition can be deleted while an instance launched from it keeps
	// running. Require /End to succeed before deleting the definition so
	// uninstall cannot orphan a task-owned daemon.
	if out, err := s.runner.Run("schtasks", "/End", "/TN", TaskName); err != nil {
		return status, fmt.Errorf("schtasks end failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := s.runner.Run("schtasks", "/Delete", "/TN", TaskName, "/F"); err != nil {
		if exists, queryErr := s.taskExists(context.Background()); queryErr == nil && !exists {
			return State{Supported: true, Path: TaskName, Loaded: "false", Enabled: "false"}, nil
		}
		return State{Supported: true, Path: TaskName}, fmt.Errorf("schtasks delete failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return s.Status(), nil
}

func (s windowsService) Disable() (State, error) {
	status := s.Status()
	if status.ForeignTask {
		return status, foreignTaskError(status.Message)
	}
	if status.Message != "" {
		return status, fmt.Errorf("%s", status.Message)
	}
	if !status.Installed {
		return status, nil
	}
	if out, err := s.runner.Run("schtasks", "/Change", "/TN", TaskName, "/DISABLE"); err != nil {
		if exists, queryErr := s.taskExists(context.Background()); queryErr == nil && !exists {
			return State{Supported: true, Path: TaskName, Loaded: "false", Enabled: "false"}, nil
		}
		return State{Supported: true, Installed: true, Path: TaskName}, fmt.Errorf("schtasks disable failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := s.runner.Run("schtasks", "/End", "/TN", TaskName); err != nil {
		return State{Supported: true, Installed: true, Path: TaskName, Enabled: "false"}, fmt.Errorf("schtasks end failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	status = s.Status()
	if status.ForeignTask {
		return status, foreignTaskError(status.Message)
	}
	if status.Message != "" {
		return status, fmt.Errorf("%s", status.Message)
	}
	return status, nil
}

func (s windowsService) Enable() (State, error) {
	status := s.Status()
	if status.ForeignTask {
		return status, foreignTaskError(status.Message)
	}
	if status.Message != "" {
		return status, fmt.Errorf("%s", status.Message)
	}
	if !status.Installed {
		return status, nil
	}
	if out, err := s.runner.Run("schtasks", "/Change", "/TN", TaskName, "/ENABLE"); err != nil {
		if exists, queryErr := s.taskExists(context.Background()); queryErr == nil && !exists {
			return State{Supported: true, Path: TaskName, Loaded: "false", Enabled: "false"}, nil
		}
		return State{Supported: true, Installed: true, Path: TaskName}, fmt.Errorf("schtasks enable failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := s.runTask(); err != nil {
		return State{Supported: true, Installed: true, Path: TaskName}, err
	}
	status = s.Status()
	if status.ForeignTask {
		return status, foreignTaskError(status.Message)
	}
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
	exists, err := s.taskExists(ctx)
	if err != nil {
		state.Installed = true
		state.Message = fmt.Sprintf("schtasks query failed: %v", err)
		return state
	}
	if !exists {
		state.Loaded = "false"
		state.Enabled = "false"
		return state
	}
	out, err := runStatusCommand(ctx, s.runner, "schtasks", "/Query", "/TN", TaskName, "/XML")
	if err != nil {
		state.Installed = true
		state.Message = fmt.Sprintf("schtasks query failed: %v: %s", err, strings.TrimSpace(string(out)))
		return state
	}
	state.Installed = true
	var task struct {
		XMLName xml.Name `xml:"Task"`
		Actions struct {
			Exec struct {
				Command string `xml:"Command"`
			} `xml:"Exec"`
		} `xml:"Actions"`
		Settings struct {
			Enabled *bool `xml:"Enabled"`
		} `xml:"Settings"`
	}
	if err := unmarshalTaskXML(out, &task); err != nil {
		state.Message = fmt.Sprintf("schtasks query returned invalid XML: %v", err)
		return state
	}
	if s.executable != "" && !ownsWindowsTaskCommand(task.Actions.Exec.Command, s.executable) {
		state.Installed = false
		state.Loaded = "false"
		state.Enabled = "false"
		state.ForeignTask = true
		state.Message = fmt.Sprintf(
			"scheduled task %s belongs to a different installation: targets %q, running executable is %q",
			TaskName, task.Actions.Exec.Command, s.executable,
		)
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

func foreignTaskError(message string) error {
	return fmt.Errorf("%s; re-run autostart install or uninstall with --force to take it over", message)
}

// windowsLauncherName is the fixed base name of the companion launcher shipped
// beside termp.exe in every Windows archive.
const windowsLauncherName = "termpw.exe"

// windowsTaskExec selects what the logon task runs. When the companion launcher
// sits beside the daemon (the normal shipped layout) the task runs it with no
// arguments, so no console window is ever created (issue #473). When the
// launcher is absent from a non-Scoop install — e.g. a hand-assembled install
// — it falls back to the daemon's own foreground path so autostart still works,
// at the cost of the window this fix removes. Scoop instead fails closed rather
// than registering one of its console shims.
func windowsTaskExec(exe string) (command, arguments string, err error) {
	if launcher := existingWindowsLauncher(exe); launcher != "" {
		return launcher, "", nil
	}
	if isScoopShimExecutable(exe) {
		return "", "", fmt.Errorf("cannot find the real Scoop autostart launcher at %s", filepath.Join(filepath.Dir(filepath.Dir(exe)), "apps", "termp", "current", windowsLauncherName))
	}
	return exe, "start --foreground", nil
}

func isScoopShimExecutable(exe string) bool {
	return strings.EqualFold(filepath.Base(exe), "termp.exe") &&
		strings.EqualFold(filepath.Base(filepath.Dir(exe)), "shims")
}

// existingWindowsLauncher returns the real launcher path when it exists on
// disk, using host-native path handling so the on-disk probe is correct on real
// Windows. A Scoop shim invocation is mapped to apps\termp\current first: the
// shims-directory sibling is another console shim, not the GUI launcher (#510).
func existingWindowsLauncher(exe string) string {
	if isScoopShimExecutable(exe) {
		launcher := filepath.Join(filepath.Dir(filepath.Dir(exe)), "apps", "termp", "current", windowsLauncherName)
		if info, err := os.Stat(launcher); err == nil && !info.IsDir() {
			return launcher
		}
		return ""
	}
	launcher := filepath.Join(filepath.Dir(exe), windowsLauncherName)
	if info, err := os.Stat(launcher); err == nil && !info.IsDir() {
		return launcher
	}
	return ""
}

// ownsWindowsTaskCommand reports whether a task whose action runs taskCommand
// belongs to this installation. It accepts both the daemon itself (tasks written
// before the launcher existed, and the fallback path) and the companion launcher
// that lives in the same directory as the daemon. Without this, once a task
// points at termpw.exe the Status ownership check would report our own task as
// foreign and lock install/uninstall/enable/disable behind --force (issue #473).
func ownsWindowsTaskCommand(taskCommand, executable string) bool {
	if sameWindowsExecutable(taskCommand, executable) {
		return true
	}
	for _, launcher := range windowsLauncherCandidates(executable) {
		if sameWindowsExecutable(taskCommand, launcher) {
			return true
		}
	}
	return false
}

// windowsLauncherCandidates returns the launcher identities owned by an
// invocation path. New Scoop tasks target the real launcher under the stable
// current junction; the shim sibling remains accepted so older tasks can be
// reconciled in place.
func windowsLauncherCandidates(executable string) []string {
	sibling := windowsLauncherSibling(executable)
	normalized := normalizeResolvedWindowsPath(executable)
	separator := strings.LastIndex(normalized, `\`)
	if separator < 0 || !strings.EqualFold(normalized[separator+1:], "termp.exe") {
		return []string{sibling}
	}
	dir := normalized[:separator]
	dirSeparator := strings.LastIndex(dir, `\`)
	if dirSeparator < 0 || !strings.EqualFold(dir[dirSeparator+1:], "shims") {
		return []string{sibling}
	}
	root := dir[:dirSeparator]
	return []string{root + `\apps\termp\current\` + windowsLauncherName, sibling}
}

// windowsLauncherSibling derives the launcher path that sits beside executable,
// as a Windows-style path, using string handling that is correct regardless of
// the host OS (the ownership check runs on every platform's Status path).
func windowsLauncherSibling(executable string) string {
	normalized := normalizeResolvedWindowsPath(executable)
	if normalized == "" {
		return ""
	}
	if idx := strings.LastIndex(normalized, `\`); idx >= 0 {
		return normalized[:idx] + `\` + windowsLauncherName
	}
	return windowsLauncherName
}

func sameWindowsExecutable(taskCommand, executable string) bool {
	taskCommand = normalizeWindowsPath(taskCommand)
	executable = normalizeResolvedWindowsPath(executable)
	if taskCommand == "" || executable == "" {
		return false
	}
	if same, determined := sameWindowsFileIdentity(taskCommand, executable); determined {
		return same
	}
	taskCanonical, taskErr := canonicalWindowsExecutable(taskCommand)
	exeCanonical, exeErr := canonicalWindowsExecutable(executable)
	if taskErr == nil && exeErr == nil {
		return strings.EqualFold(taskCanonical, exeCanonical)
	}
	return strings.EqualFold(taskCommand, executable)
}

func (s windowsService) taskExists(ctx context.Context) (bool, error) {
	// The targeted query is fast and its exit status is locale-independent:
	// success means the task exists and a normal schtasks exit means it does
	// not. Fall back to enumeration only when the command could not run
	// normally (for example, a transient runner failure).
	_, err := runStatusCommand(
		ctx, s.runner, "schtasks", "/Query", "/TN", TaskName, "/FO", "CSV", "/NH",
	)
	if err == nil {
		return true, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	var exitErr interface{ ExitCode() int }
	if errors.As(err, &exitErr) {
		return false, nil
	}

	out, err := runStatusCommand(ctx, s.runner, "schtasks", "/Query", "/FO", "CSV", "/NH")
	found, parseErr := windowsTaskCSVContains(out, TaskName)
	if parseErr != nil {
		return false, parseErr
	}
	if found {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return false, nil
}

func windowsTaskCSVContains(data []byte, taskName string) (bool, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	records, err := reader.ReadAll()
	if err != nil {
		return false, fmt.Errorf("parse schtasks CSV: %w", err)
	}
	for _, record := range records {
		if len(record) > 0 && strings.EqualFold(strings.TrimSpace(record[0]), taskName) {
			return true, nil
		}
	}
	return false, nil
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
	if err == nil {
		return nil
	}
	queryOut, queryErr := s.runner.Run("schtasks", "/Query", "/TN", TaskName, "/FO", "CSV", "/V", "/NH")
	if queryErr == nil && windowsTaskCSVIsRunning(queryOut) {
		return nil
	}
	if exists, queryErr := s.taskExists(context.Background()); queryErr == nil && !exists {
		return nil
	}
	return fmt.Errorf("schtasks run failed: %w: %s", err, strings.TrimSpace(string(out)))
}

// windowsLastRunResultColumn is the zero-based index of the "Last Run Result"
// field in the verbose CSV that `schtasks /Query /FO CSV /V /NH` emits. The
// column order is fixed and locale-independent (only the header names, which
// `/NH` omits, are localized), so the running sentinel is read from this one
// column positionally. Scanning every field instead lets a coincidental
// SCHED_S_TASK_RUNNING value in an unrelated numeric column (e.g. a duration or
// an embedded exit code) falsely report the task as running (issue #504).
const windowsLastRunResultColumn = 6

func windowsTaskCSVIsRunning(data []byte) bool {
	// Verbose CSV column names and status text are localized. The numeric Last
	// Run Result is not; SCHED_S_TASK_RUNNING is 0x41301 on every locale.
	const schedSTaskRunning = uint64(0x00041301)
	reader := csv.NewReader(bytes.NewReader(data))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	records, err := reader.ReadAll()
	if err != nil {
		return false
	}
	for _, record := range records {
		if len(record) <= windowsLastRunResultColumn {
			continue
		}
		value := strings.TrimSpace(record[windowsLastRunResultColumn])
		base := 10
		if strings.HasPrefix(strings.ToLower(value), "0x") {
			base = 0
		}
		if result, err := strconv.ParseUint(value, base, 32); err == nil && result == schedSTaskRunning {
			return true
		}
	}
	return false
}
