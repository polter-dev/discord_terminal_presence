// Package completioninstall installs generated shell completion scripts.
package completioninstall

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// HomeDirFunc resolves the home directory used for an installation.
type HomeDirFunc func() (string, error)

// DetectShell returns a supported shell name from a SHELL environment value.
// Bash is the conservative fallback when SHELL is empty or unsupported.
func DetectShell(shellEnv string) string {
	switch filepath.Base(shellEnv) {
	case "bash":
		return "bash"
	case "zsh":
		return "zsh"
	case "fish":
		return "fish"
	default:
		return "bash"
	}
}

// TargetPath returns the completion file that Install and Uninstall manage.
func TargetPath(shell string, homeDir HomeDirFunc) (string, error) {
	if homeDir == nil {
		return "", errors.New("resolve home directory: resolver is nil")
	}
	home, err := homeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if home == "" {
		return "", errors.New("resolve home directory: empty path")
	}

	switch shell {
	case "bash":
		return filepath.Join(home, ".local", "share", "bash-completion", "completions", "termp"), nil
	case "zsh":
		return filepath.Join(home, ".zsh", "completions", "_termp"), nil
	case "fish":
		return filepath.Join(home, ".config", "fish", "completions", "termp.fish"), nil
	default:
		return "", fmt.Errorf("unsupported shell %q (expected bash, zsh, or fish)", shell)
	}
}

// Install writes script to the shell's autoload location, replacing any
// previous termp completion file. It returns the exact path modified.
func Install(shell, script string, homeDir HomeDirFunc) ([]string, error) {
	path, err := TargetPath(shell, homeDir)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create completion directory: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("refuse to replace non-regular completion file %s", path)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect completion file %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary completion file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("set completion file permissions: %w", err)
	}
	if _, err := tmp.WriteString(script); err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("write completion file %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("write completion file %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return nil, fmt.Errorf("replace completion file %s: %w", path, err)
	}
	return []string{path}, nil
}

// Uninstall removes exactly the completion file managed by Install. If it is
// already absent, Uninstall succeeds and reports no modified paths.
func Uninstall(shell string, homeDir HomeDirFunc) ([]string, error) {
	path, err := TargetPath(shell, homeDir)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("remove completion file %s: %w", path, err)
	}
	return []string{path}, nil
}

// UninstallAll removes the completion files managed by Install for every
// supported shell. Files that are already absent are ignored.
func UninstallAll(homeDir HomeDirFunc) ([]string, error) {
	var removed []string
	for _, shell := range [...]string{"bash", "zsh", "fish"} {
		paths, err := Uninstall(shell, homeDir)
		if err != nil {
			return removed, err
		}
		removed = append(removed, paths...)
	}
	return removed, nil
}

// Note returns shell-specific activation guidance. No shell rc file is edited.
func Note(shell string) string {
	if shell == "zsh" {
		return "If needed, add `fpath=(~/.zsh/completions $fpath)` to ~/.zshrc before compinit."
	}
	return ""
}
