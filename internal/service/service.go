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

func (m Manager) Install(exe string) (State, error) {
	switch m.GOOS {
	case "darwin":
		return darwinService{runner: m.runner()}.Install(exe)
	case "linux":
		return linuxService{runner: m.runner()}.Install(exe)
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
