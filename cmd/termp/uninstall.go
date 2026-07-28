package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/polter-dev/discord_terminal_presence/internal/completioninstall"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/tui"
	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
	usagepkg "github.com/polter-dev/discord_terminal_presence/internal/usage"
)

type uninstallRemovalTarget struct {
	label     string
	path      string
	directory bool
}

type uninstallAllDeps struct {
	stopDaemon       func() (int, bool, error)
	removeAutostart  func(bool, bool) error
	removeCompletion func(completioninstall.HomeDirFunc) ([]string, error)
	homeDir          completioninstall.HomeDirFunc
	targets          func() ([]uninstallRemovalTarget, error)
	confirm          func(string) (bool, error)
	detectInstall    func() updatepkg.InstallMethod
	genericBinDir    func() (string, error)
	goos             string
	stdout           io.Writer
	removeFile       func(string) error
	removeAll        func(string) error
}

func uninstallAll(force, yes bool) error {
	deps := uninstallAllDeps{
		stopDaemon:       stopRunningDaemon,
		removeAutostart:  uninstallAutostart,
		removeCompletion: completioninstall.UninstallAll,
		homeDir:          os.UserHomeDir,
		targets:          defaultUninstallTargets,
		confirm:          confirmCompleteUninstall,
		detectInstall:    updatepkg.DetectInstallMethod,
		genericBinDir:    updatepkg.GenericInstallDir,
		goos:             runtime.GOOS,
		stdout:           os.Stdout,
		removeFile:       os.Remove,
		removeAll:        os.RemoveAll,
	}
	return uninstallAllWithDeps(force, yes, deps)
}

func uninstallAllWithDeps(force, yes bool, deps uninstallAllDeps) error {
	targets, err := deps.targets()
	if err != nil {
		return err
	}
	completionPaths, err := uninstallCompletionPaths(deps.homeDir)
	if err != nil {
		return err
	}
	method := deps.detectInstall()
	binaryCommand, err := uninstallBinaryCommand(method, deps.goos, deps.genericBinDir)
	if err != nil {
		return err
	}

	plan := formatUninstallPlan(completionPaths, targets, binaryCommand)
	fmt.Fprint(deps.stdout, plan)
	if !yes {
		confirmed, err := deps.confirm("Remove all termp data?")
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(deps.stdout, "Uninstall cancelled.")
			return nil
		}
	}

	stoppedPID, stopped, err := deps.stopDaemon()
	if err != nil {
		return fmt.Errorf("stop daemon before uninstalling: %w", err)
	}
	if stopped {
		fmt.Fprintf(deps.stdout, "Stopped daemon (pid %d).\n", stoppedPID)
	}

	if err := deps.removeAutostart(force, false); err != nil {
		return err
	}
	restartedPID, restarted, err := deps.stopDaemon()
	if err != nil {
		return fmt.Errorf("confirm daemon remained stopped after removing autostart: %w", err)
	}
	if restarted {
		fmt.Fprintf(deps.stdout, "Stopped relaunched daemon (pid %d).\n", restartedPID)
	}
	removedCompletions, err := deps.removeCompletion(deps.homeDir)
	if err != nil {
		return err
	}
	for _, path := range removedCompletions {
		fmt.Fprintf(deps.stdout, "Removed shell completion: %s\n", path)
	}

	var removalErrors []error
	for _, target := range targets {
		if err := removeUninstallTarget(target, deps.removeFile, deps.removeAll); err != nil {
			removalErrors = append(removalErrors, err)
			continue
		}
		fmt.Fprintf(deps.stdout, "Removed %s: %s\n", target.label, target.path)
	}
	if err := errors.Join(removalErrors...); err != nil {
		return err
	}

	fmt.Fprintln(deps.stdout, "All termp-created data was removed. The binary is still installed.")
	fmt.Fprintf(deps.stdout, "To remove the binary, run: %s\n", binaryCommand)
	return nil
}

func uninstallCompletionPaths(homeDir completioninstall.HomeDirFunc) ([]string, error) {
	paths := make([]string, 0, 3)
	for _, shell := range []string{"bash", "zsh", "fish"} {
		path, err := completioninstall.TargetPath(shell, homeDir)
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func defaultUninstallTargets() ([]uninstallRemovalTarget, error) {
	logPath, err := detachedLogPath()
	if err != nil {
		return nil, err
	}
	candidates := []uninstallRemovalTarget{
		{label: "config", path: parentPath(config.DefaultPath()), directory: true},
		{label: "state", path: parentPath(usagepkg.StatePath()), directory: true},
		{label: "presence state", path: parentPath(detector.EpisodeStatePath()), directory: true},
		{label: "update cache", path: parentPath(updatepkg.DefaultCachePath()), directory: true},
		{label: "log", path: logPath},
		{label: "runtime state", path: pidFilePath()},
	}
	targets := make([]uninstallRemovalTarget, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, target := range candidates {
		if target.path == "" {
			continue
		}
		target.path = filepath.Clean(target.path)
		if _, ok := seen[target.path]; ok {
			continue
		}
		seen[target.path] = struct{}{}
		targets = append(targets, target)
	}
	return targets, nil
}

func parentPath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Dir(path)
}

func formatUninstallPlan(completionPaths []string, targets []uninstallRemovalTarget, binaryCommand string) string {
	var b strings.Builder
	b.WriteString("This will stop the termp daemon and remove:\n")
	b.WriteString("  start-at-login\n")
	for _, path := range completionPaths {
		fmt.Fprintf(&b, "  shell completion: %s\n", path)
	}
	for _, target := range targets {
		fmt.Fprintf(&b, "  %s: %s\n", target.label, target.path)
	}
	b.WriteString("The binary will not be deleted by this command.\n")
	fmt.Fprintf(&b, "Afterward, remove it with: %s\n", binaryCommand)
	return b.String()
}

func removeUninstallTarget(target uninstallRemovalTarget, removeFile, removeAll func(string) error) error {
	if err := validateUninstallTarget(target); err != nil {
		return err
	}
	var err error
	if target.directory {
		err = removeAll(target.path)
	} else {
		err = removeFile(target.path)
	}
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("remove %s %s: %w", target.label, target.path, err)
}

func validateUninstallTarget(target uninstallRemovalTarget) error {
	path := filepath.Clean(target.path)
	if target.path == "" || path == "." || path == filepath.VolumeName(path)+string(filepath.Separator) {
		return fmt.Errorf("refuse unsafe %s removal path %q", target.label, target.path)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("refuse relative %s removal path %q", target.label, target.path)
	}
	if target.directory && filepath.Base(path) != "termp" {
		return fmt.Errorf("refuse unsafe %s directory %q", target.label, target.path)
	}
	return nil
}

func uninstallBinaryCommand(method updatepkg.InstallMethod, goos string, genericBinDir func() (string, error)) (string, error) {
	switch method {
	case updatepkg.InstallHomebrew:
		return "brew uninstall --cask termp", nil
	case updatepkg.InstallDebian:
		return "sudo apt remove termp", nil
	case updatepkg.InstallRPM:
		return "sudo dnf remove termp", nil
	case updatepkg.InstallSystemPackage:
		return "sudo apt remove termp (Debian/Ubuntu) or sudo dnf remove termp (RPM-based Linux)", nil
	}

	binDir, err := genericBinDir()
	if err != nil {
		return "", err
	}
	name := "termp"
	if goos == "windows" {
		name += ".exe"
	}
	path := filepath.Join(binDir, name)
	if goos == "windows" {
		return "del " + windowsCommandQuote(path), nil
	}
	if method == updatepkg.InstallGo {
		return "rm " + shellQuote(path), nil
	}
	return "sudo rm " + shellQuote(path), nil
}

func shellQuote(value string) string {
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("/._-:", r) {
			continue
		}
		return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
	}
	return value
}

func windowsCommandQuote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

type uninstallConfirmModel struct {
	dialog    tui.ConfirmDialog
	done      bool
	confirmed bool
}

func (m uninstallConfirmModel) Init() tea.Cmd {
	return nil
}

func (m uninstallConfirmModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c", "esc":
		m.done = true
		return m, tea.Quit
	}
	var selected bool
	m.dialog, selected = m.dialog.Update(key)
	if selected {
		m.done = true
		m.confirmed = m.dialog.Highlighted() == tui.ConfirmYes
		return m, tea.Quit
	}
	return m, nil
}

func (m uninstallConfirmModel) View() string {
	if m.done {
		return ""
	}
	return m.dialog.View()
}

func confirmCompleteUninstall(prompt string) (bool, error) {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return false, errors.New("confirmation requires an interactive terminal; re-run with --yes")
	}
	model := uninstallConfirmModel{dialog: tui.NewConfirmDialog(prompt, tui.ConfirmNo)}
	final, err := tea.NewProgram(model).Run()
	if err != nil {
		return false, fmt.Errorf("run uninstall confirmation: %w", err)
	}
	result, ok := final.(uninstallConfirmModel)
	if !ok {
		return false, errors.New("uninstall confirmation ended unexpectedly")
	}
	return result.confirmed, nil
}
