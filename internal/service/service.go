package service

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io/fs"
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
	GOOS       string
	Runner     Runner
	Executable string
}

type State struct {
	Supported   bool
	Installed   bool
	Loaded      string
	Enabled     string
	Path        string
	Message     string
	ForeignTask bool
}

func NewManager() Manager {
	exe, _ := ResolveExecutable()
	return Manager{GOOS: runtime.GOOS, Runner: ExecRunner{}, Executable: exe}
}

func ResolveExecutable() (string, error) {
	if invocationPath, err := exec.LookPath(os.Args[0]); err == nil {
		absolutePath, err := filepath.Abs(invocationPath)
		if err != nil {
			return "", fmt.Errorf("resolve executable path %q: %w", invocationPath, err)
		}
		return absolutePath, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate the running termp executable: %w", err)
	}
	absolutePath, err := filepath.Abs(exe)
	if err != nil {
		return "", fmt.Errorf("resolve executable path %q: %w", exe, err)
	}
	return absolutePath, nil
}

func ValidateInstallExecutable(exe string, force bool) (string, error) {
	invocationPath, err := filepath.Abs(exe)
	if err != nil {
		return "", fmt.Errorf("resolve executable path %q: %w", exe, err)
	}
	if force {
		return invocationPath, nil
	}
	// EvalSymlinks only feeds the unstable-path heuristic below. Treat its two
	// failure modes differently (issue #472):
	//
	//   - The path is genuinely absent. That is worth failing on, because the
	//     scheduled task or launch agent would point at nothing, but the raw
	//     error is useless to a user: on Windows a missing directory component
	//     surfaces as a bare syscall.Errno rendered as "The system cannot find
	//     the path specified." with no path attached at all. Name the path and
	//     the next command instead.
	//   - Resolution failed for any other reason (an unreadable parent
	//     directory, a reparse point that cannot be followed, a flaky network
	//     share). Those say nothing about whether the path is stable, so fall
	//     back to judging the unresolved absolute path rather than aborting an
	//     install that would otherwise have worked. /tmp and source-tree
	//     installs are still caught, because both are visible without
	//     resolution.
	candidate := invocationPath
	resolved, err := filepath.EvalSymlinks(invocationPath)
	switch {
	case err == nil:
		candidate = resolved
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf(
			"cannot install autostart from %q: %w; that file, or one of its parent directories, does not exist. Run `where termp` on Windows (`which termp` elsewhere) to see the path your shell actually resolves, re-run `termp autostart install` from that path, or pass --force to register this path unchecked",
			invocationPath, err,
		)
	}
	if !isUnstableExecutablePath(candidate) {
		return invocationPath, nil
	}
	// The temp-root half of this guard only started firing on Windows with
	// issue #542, so the suggested destination has to be one a Windows user can
	// actually use rather than a pair of Unix bin directories.
	stableLocation := "~/.local/bin or /usr/local/bin"
	if runtime.GOOS == "windows" {
		stableLocation = `%LOCALAPPDATA%\Programs\termp`
	}
	return "", fmt.Errorf(
		"refusing to install autostart from unstable executable path %q; move the binary to a stable location such as %s, then re-run `termp install` (or use --force to install this path anyway)",
		candidate, stableLocation,
	)
}

func isUnstableExecutablePath(exe string) bool {
	for _, root := range unstableExecutableRoots() {
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

// unstableExecutableRoots lists the directories a binary should not be
// installed from. The temp root is compared in both spellings on purpose
// (issue #542). os.TempDir() returns the TMPDIR/TMP value verbatim, while the
// path being judged has usually been through filepath.EvalSymlinks, and those
// two can name the same directory differently: a symlinked TMPDIR on Unix, or
// on Windows a TMP holding an 8.3 short path (C:\Users\RUNNER~1\...) where
// EvalSymlinks returns the long form (C:\Users\runneradmin\...). Comparing only
// the raw string made filepath.Rel walk out of the root, so the guard silently
// never fired on such a machine and a temp-directory binary was judged stable.
// Resolving the root can itself fail (the directory may be gone or unreadable);
// that is not worth failing an install over, so the raw string alone is checked
// in that case, which is exactly the behavior that shipped before this fix. The
// remaining roots are Unix-only literals and are matched as written.
func unstableExecutableRoots() []string {
	tempDir := os.TempDir()
	roots := []string{tempDir, "/tmp", "/private/tmp", "/private/var/folders"}
	if resolvedTempDir, err := filepath.EvalSymlinks(tempDir); err == nil && resolvedTempDir != tempDir {
		roots = append(roots, resolvedTempDir)
	}
	return roots
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

func (m Manager) Install(exe string, force bool) (State, error) {
	return m.install(exe, true, force)
}

// InstallDefinition reconciles the autostart definition without launching a
// second copy of an already-running daemon.
func (m Manager) InstallDefinition(exe string, force bool) (State, error) {
	return m.install(exe, false, force)
}

func (m Manager) install(exe string, launch, force bool) (State, error) {
	switch m.GOOS {
	case "darwin":
		return darwinService{runner: m.runner(), executable: exe}.install(exe, launch, force)
	case "linux":
		return linuxService{runner: m.runner(), executable: exe}.install(exe, launch, force)
	case "windows":
		return windowsService{runner: m.runner(), executable: exe}.install(exe, launch, force)
	default:
		return State{Supported: false, Message: fmt.Sprintf("auto-start not supported on %s yet", m.GOOS)}, ErrUnsupported
	}
}

func (m Manager) Uninstall(force bool) (State, error) {
	switch m.GOOS {
	case "darwin":
		return darwinService{runner: m.runner(), executable: m.Executable}.Uninstall(force)
	case "linux":
		return linuxService{runner: m.runner(), executable: m.Executable}.Uninstall(force)
	case "windows":
		return windowsService{runner: m.runner(), executable: m.Executable}.Uninstall(force)
	default:
		return State{Supported: false, Message: fmt.Sprintf("auto-start not supported on %s yet", m.GOOS)}, ErrUnsupported
	}
}

func (m Manager) Disable() (State, error) {
	switch m.GOOS {
	case "darwin":
		return darwinService{runner: m.runner(), executable: m.Executable}.Disable()
	case "linux":
		return linuxService{runner: m.runner(), executable: m.Executable}.Disable()
	case "windows":
		return windowsService{runner: m.runner(), executable: m.Executable}.Disable()
	default:
		return State{Supported: false, Message: fmt.Sprintf("auto-start not supported on %s yet", m.GOOS)}, ErrUnsupported
	}
}

func (m Manager) Enable() (State, error) {
	switch m.GOOS {
	case "darwin":
		return darwinService{runner: m.runner(), executable: m.Executable}.Enable()
	case "linux":
		return linuxService{runner: m.runner(), executable: m.Executable}.Enable()
	case "windows":
		return windowsService{runner: m.runner(), executable: m.Executable}.Enable()
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
		return darwinService{runner: m.runner(), executable: m.Executable}.StatusContext(ctx)
	case "linux":
		return linuxService{runner: m.runner(), executable: m.Executable}.StatusContext(ctx)
	case "windows":
		return windowsService{runner: m.runner(), executable: m.Executable}.StatusContext(ctx)
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

func BuildLaunchAgentPlist(exe string) ([]byte, error) {
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
		<string>--foreground</string>
		<string>--internal-daemon-log</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>/dev/null</string>
	<key>StandardErrorPath</key>
	<string>/dev/null</string>
</dict>
</plist>
`, esc(Label), esc(exe))
	return b.Bytes(), nil
}

func BuildSystemdUnit(exe string) ([]byte, error) {
	if strings.ContainsAny(exe, "\r\n") {
		return nil, errors.New("systemd executable path contains a line break")
	}
	return []byte(fmt.Sprintf(`[Unit]
Description=termp Discord Rich Presence daemon

[Service]
ExecStart=%s start --foreground
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

func sameUnixExecutable(definitionExecutable, currentExecutable string) bool {
	definitionExecutable = filepath.Clean(strings.TrimSpace(definitionExecutable))
	currentExecutable = filepath.Clean(strings.TrimSpace(currentExecutable))
	if definitionExecutable == "." || currentExecutable == "." {
		return false
	}
	if definitionExecutable == currentExecutable {
		return true
	}

	definitionResolved, definitionErr := filepath.EvalSymlinks(definitionExecutable)
	currentResolved, currentErr := filepath.EvalSymlinks(currentExecutable)
	if definitionErr == nil && currentErr == nil {
		return filepath.Clean(definitionResolved) == filepath.Clean(currentResolved)
	}

	definitionInfo, definitionStatErr := os.Stat(definitionExecutable)
	currentInfo, currentStatErr := os.Stat(currentExecutable)
	return definitionStatErr == nil && currentStatErr == nil &&
		os.SameFile(definitionInfo, currentInfo)
}

func isForeignUnixExecutable(definitionExecutable, currentExecutable string) bool {
	if definitionExecutable == "" || currentExecutable == "" ||
		!filepath.IsAbs(definitionExecutable) || !filepath.IsAbs(currentExecutable) {
		return false
	}
	return !sameUnixExecutable(definitionExecutable, currentExecutable)
}
