package service

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	Label       = "dev.termp.daemon"
	ServiceName = "termp.service"
	TaskName    = "termp"
)

// Runner executes service-manager commands. Tests replace it so launchctl and
// systemctl are never invoked.
type Runner interface {
	Run(name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

type Manager struct {
	GOOS   string
	Runner Runner
}

type State struct {
	Supported bool
	Installed bool
	Loaded    string
	Enabled   string
	Path      string
	Message   string
}

func NewManager() Manager {
	return Manager{GOOS: runtime.GOOS, Runner: ExecRunner{}}
}

func ResolveExecutable() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return resolved, nil
}

func ValidateInstallExecutable(exe string, force bool) (string, error) {
	exe, err := filepath.Abs(exe)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	if force || !isUnstableExecutablePath(resolved) {
		return resolved, nil
	}
	return "", fmt.Errorf(
		"refusing to install autostart from unstable executable path %q; move the binary to a stable location such as ~/.local/bin or /usr/local/bin, then re-run `termp install` (or use --force to install this path anyway)",
		resolved,
	)
}

func isUnstableExecutablePath(exe string) bool {
	for _, root := range []string{os.TempDir(), "/tmp", "/private/tmp", "/private/var/folders"} {
		if pathWithin(exe, root) {
			return true
		}
	}

	for dir := filepath.Dir(exe); ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
	}
}

func pathWithin(path, root string) bool {
	path, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func (m Manager) Install(exe string) (State, error) {
	switch m.GOOS {
	case "darwin":
		return darwinService{runner: m.runner()}.Install(exe)
	case "linux":
		return linuxService{runner: m.runner()}.Install(exe)
	case "windows":
		return windowsService{runner: m.runner()}.Install(exe)
	default:
		return State{Supported: false, Message: fmt.Sprintf("auto-start not supported on %s yet", m.GOOS)}, ErrUnsupported
	}
}

func (m Manager) Uninstall() (State, error) {
	switch m.GOOS {
	case "darwin":
		return darwinService{runner: m.runner()}.Uninstall()
	case "linux":
		return linuxService{runner: m.runner()}.Uninstall()
	case "windows":
		return windowsService{runner: m.runner()}.Uninstall()
	default:
		return State{Supported: false, Message: fmt.Sprintf("auto-start not supported on %s yet", m.GOOS)}, ErrUnsupported
	}
}

func (m Manager) Disable() (State, error) {
	switch m.GOOS {
	case "darwin":
		return darwinService{runner: m.runner()}.Disable()
	case "linux":
		return linuxService{runner: m.runner()}.Disable()
	case "windows":
		return windowsService{runner: m.runner()}.Disable()
	default:
		return State{Supported: false, Message: fmt.Sprintf("auto-start not supported on %s yet", m.GOOS)}, ErrUnsupported
	}
}

func (m Manager) Enable() (State, error) {
	switch m.GOOS {
	case "darwin":
		return darwinService{runner: m.runner()}.Enable()
	case "linux":
		return linuxService{runner: m.runner()}.Enable()
	case "windows":
		return windowsService{runner: m.runner()}.Enable()
	default:
		return State{Supported: false, Message: fmt.Sprintf("auto-start not supported on %s yet", m.GOOS)}, ErrUnsupported
	}
}

func (m Manager) Status() State {
	switch m.GOOS {
	case "darwin":
		return darwinService{runner: m.runner()}.Status()
	case "linux":
		return linuxService{runner: m.runner()}.Status()
	case "windows":
		return windowsService{runner: m.runner()}.Status()
	default:
		return State{Supported: false, Message: fmt.Sprintf("auto-start not supported on %s yet", m.GOOS)}
	}
}

func (m Manager) runner() Runner {
	if m.Runner != nil {
		return m.Runner
	}
	return ExecRunner{}
}

var ErrUnsupported = errors.New("auto-start unsupported")

func launchAgentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
}

func launchAgentLogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Logs", "termp.log"), nil
}

func systemdUnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user", ServiceName), nil
}

func BuildLaunchAgentPlist(exe, logPath string) ([]byte, error) {
	var b bytes.Buffer
	esc := func(s string) string {
		var out bytes.Buffer
		_ = xml.EscapeText(&out, []byte(s))
		return out.String()
	}
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>start</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, esc(Label), esc(exe), esc(logPath), esc(logPath))
	return b.Bytes(), nil
}

func BuildSystemdUnit(exe string) []byte {
	return []byte(fmt.Sprintf(`[Unit]
Description=termp Discord Rich Presence daemon

[Service]
ExecStart=%s start
Restart=on-failure

[Install]
WantedBy=default.target
`, systemdEscapeExecArg(exe)))
}

func systemdEscapeExecArg(arg string) string {
	if arg == "" {
		return `""`
	}
	if !strings.ContainsAny(arg, " \t\n\"'\\") {
		return arg
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(arg) + `"`
}
