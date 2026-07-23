//go:build !windows

package presence

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func dialDiscordIPC() (net.Conn, error) {
	envNames := []string{"XDG_RUNTIME_DIR", "TMPDIR", "TMP", "TEMP"}
	baseDirs := make([]string, 0, len(envNames)+1)
	for _, name := range envNames {
		if dir := os.Getenv(name); dir != "" {
			baseDirs = append(baseDirs, dir)
		}
	}
	baseDirs = append(baseDirs, "/tmp")

	var failures strings.Builder
	seen := make(map[string]struct{})
	tryCandidates := func(paths []string) net.Conn {
		for _, path := range paths {
			path = filepath.Clean(path)
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}

			conn, err := dialDiscordIPCSocket(path)
			if err == nil {
				return conn
			}
			fmt.Fprintf(&failures, "  %s: %v\n", path, err)
		}
		return nil
	}

	if conn := tryCandidates(discordIPCOverrideCandidates(os.Getenv("DISCORD_IPC_PATH"), os.Lstat)); conn != nil {
		return conn, nil
	}
	for _, dir := range discordIPCCandidateDirs(baseDirs) {
		paths := make([]string, 0, 10)
		for i := 0; i <= 9; i++ {
			paths = append(paths, filepath.Join(dir, fmt.Sprintf("discord-ipc-%d", i)))
		}
		if conn := tryCandidates(paths); conn != nil {
			return conn, nil
		}
	}
	if conn := tryCandidates(discordIPCGlobCandidates(baseDirs)); conn != nil {
		return conn, nil
	}

	return nil, fmt.Errorf("presence: no Discord IPC socket accepted a connection:\n%s", failures.String())
}

func dialDiscordIPCSocket(path string) (net.Conn, error) {
	before, err := validateSocketCandidate(path, os.Geteuid())
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return nil, err
	}
	if err := validateConnectedSocket(conn, path, before, os.Geteuid()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func discordIPCOverrideCandidates(value string, lstat func(string) (os.FileInfo, error)) []string {
	if value == "" {
		return nil
	}
	value = filepath.Clean(value)
	info, err := lstat(value)
	if err != nil || !info.IsDir() {
		return []string{value}
	}
	paths := make([]string, 0, 10)
	for i := 0; i <= 9; i++ {
		paths = append(paths, filepath.Join(value, fmt.Sprintf("discord-ipc-%d", i)))
	}
	return paths
}

func discordIPCCandidateDirs(baseDirs []string) []string {
	nestedDirs := []string{
		"snap.discord",
		filepath.Join("app", "com.discordapp.Discord"),
		filepath.Join("app", "com.discordapp.DiscordCanary"),
		filepath.Join("app", "com.discordapp.DiscordPTB"),
	}
	dirs := make([]string, 0, len(baseDirs)*(len(nestedDirs)+1))
	seen := make(map[string]struct{}, cap(dirs))
	add := func(dir string) {
		dir = filepath.Clean(dir)
		if _, ok := seen[dir]; !ok {
			dirs = append(dirs, dir)
			seen[dir] = struct{}{}
		}
	}
	for _, baseDir := range baseDirs {
		baseDir = filepath.Clean(baseDir)
		add(baseDir)
		for _, nestedDir := range nestedDirs {
			add(filepath.Join(baseDir, nestedDir))
		}
	}
	return dirs
}

func discordIPCGlobCandidates(baseDirs []string) []string {
	var paths []string
	seen := make(map[string]struct{})
	for _, baseDir := range baseDirs {
		matches, err := filepath.Glob(filepath.Join(baseDir, "*", "discord-ipc-*"))
		if err != nil {
			continue
		}
		for _, path := range matches {
			path = filepath.Clean(path)
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			paths = append(paths, path)
		}
	}
	return paths
}

func validateSocketCandidate(path string, euid int) (os.FileInfo, error) {
	return validateSocketCandidateWithLstat(path, euid, os.Lstat)
}

func validateSocketCandidateWithLstat(path string, euid int, lstat func(string) (os.FileInfo, error)) (os.FileInfo, error) {
	dir := filepath.Dir(path)
	dirInfo, err := lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("inspect socket directory: %w", err)
	}
	if !dirInfo.IsDir() {
		return nil, fmt.Errorf("socket directory is not a directory")
	}
	if dirInfo.Mode().Perm()&0002 != 0 {
		// Discord commonly places its socket directly in the sticky global /tmp.
		// The socket ownership check below keeps that compatible fallback safe.
		if filepath.Clean(dir) != "/tmp" || dirInfo.Mode()&os.ModeSticky == 0 {
			return nil, fmt.Errorf("socket directory is world-writable")
		}
	}

	info, err := lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return nil, fmt.Errorf("candidate is not a Unix socket")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("cannot determine socket owner")
	}
	if int(stat.Uid) != euid {
		return nil, fmt.Errorf("socket owner UID %d does not match effective UID %d", stat.Uid, euid)
	}
	return info, nil
}

func validateConnectedSocket(conn net.Conn, path string, before os.FileInfo, euid int) error {
	after, err := validateSocketCandidate(path, euid)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) {
		return fmt.Errorf("socket changed while connecting")
	}
	return validatePeerCredentials(conn, euid)
}
