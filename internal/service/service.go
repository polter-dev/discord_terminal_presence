package service

import (
	"bytes"
	"context"
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
	TaskName    = `\Terminal Presence\termp`
)

// Runner executes service-manager commands. Tests replace it so launchctl and
// systemctl are never invoked.
type Runner interface {
	Run(name string, args ...string) ([]byte, error)
}

type contextRunner interface {
	RunContext(ctx context.Context, name string, args ...string) ([]byte, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(name string, args ...string) ([]byte, error) {
	return exec.Command(name, args...).CombinedOutput()
}

func (ExecRunner) RunContext(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
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
	if invocationPath, err := exec.LookPath(os.Args[0]); err == nil {
		return filepath.Abs(invocationPath)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Abs(exe)
}

func ValidateInstallExecutable(exe string, force bool) (string, error) {
	invocationPath, err := filepath.Abs(exe)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(invocationPath)
	if err != nil {
		return "", err
	}
	if force || !isUnstableExecutablePath(resolved) {
		return invocationPath, nil
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
		if isTermpSourceTree(dir) {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
	}
}

func isTermpSourceTree(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err != nil {
		return false
	}
	goMod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(goMod), "\n") {
		if strings.TrimSpace(line) == "module github.com/polter-dev/discord_terminal_presence" {
			return true
		}
	}
	return false
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
	return m.StatusContext(context.Background())
}

// StatusContext reports service state while bounding service-manager queries
// to the supplied context. Install, enable, disable, and uninstall continue to
// use their existing unbounded command path.
func (m Manager) StatusContext(ctx context.Context) State {
	switch m.GOOS {
	case "darwin":
		return darwinService{runner: m.runner()}.StatusContext(ctx)
	case "linux":
		return linuxService{runner: m.runner()}.StatusContext(ctx)
	case "windows":
		return windowsService{runner: m.runner()}.StatusContext(ctx)
	default:
		return State{Supported: false, Message: fmt.Sprintf("auto-start not supported on %s yet", m.GOOS)}
	}
}

func runStatusCommand(ctx context.Context, runner Runner, name string, args ...string) ([]byte, error) {
	if runner, ok := runner.(contextRunner); ok {
		return runner.RunContext(ctx, name, args...)
	}

	type result struct {
		out []byte
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		out, err := runner.Run(name, args...)
		resultCh <- result{out: out, err: err}
	}()

	select {
	case result := <-resultCh:
		return result.out, result.err
	case <-ctx.Done():
		return nil, ctx.Err()
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
	if configHome := os.Getenv("XDG_CONFIG_HOME"); configHome != "" {
		return filepath.Join(configHome, "systemd", "user", ServiceName), nil
	}
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

func BuildSystemdUnit(exe string) ([]byte, error) {
	if strings.ContainsAny(exe, "\r\n") {
		return nil, errors.New("systemd executable path contains a line break")
	}
	return []byte(fmt.Sprintf(`[Unit]
Description=termp Discord Rich Presence daemon

[Service]
ExecStart=%s start
Restart=on-failure

[Install]
WantedBy=default.target
`, systemdEscapeExecArg(exe))), nil
}

func systemdEscapeExecArg(arg string) string {
	arg = strings.ReplaceAll(arg, "%", "%%")
	if arg == "" {
		return `""`
	}
	if !strings.ContainsAny(arg, " \t\n\"'\\") {
		return arg
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(arg) + `"`
}
