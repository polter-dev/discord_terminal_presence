//go:build !windows

package presence

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const discordIPCDialBudget = 2 * time.Second

// Candidate-rejection sentinels. They exist so callers can classify a
// validateSocketCandidate failure with errors.Is instead of matching message
// bytes, which vary by platform and path.
var (
	errIPCCandidateNotSocket    = errors.New("candidate is not a Unix socket")
	errIPCCandidateForeignOwner = errors.New("socket owner UID does not match effective UID")
)

func dialDiscordIPC(ctx context.Context) (net.Conn, error) {
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
	deadline := time.Now().Add(discordIPCDialBudget)
	budgetExhausted := false
	endpointFound := false
	tryCandidates := func(paths []string) net.Conn {
		for _, path := range paths {
			if ctx.Err() != nil {
				return nil
			}
			path = filepath.Clean(path)
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}

			remaining := time.Until(deadline)
			if remaining <= 0 {
				fmt.Fprintf(&failures, "  discovery stopped after %s aggregate dial-time budget\n", discordIPCDialBudget)
				budgetExhausted = true
				return nil
			}
			timeout := min(500*time.Millisecond, remaining)
			dialCtx, cancel := context.WithTimeout(ctx, timeout)
			conn, exists, err := dialDiscordIPCSocket(dialCtx, path)
			cancel()
			if err == nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					_ = conn.Close()
					return nil
				}
				return conn
			}
			if exists {
				endpointFound = true
			}
			fmt.Fprintf(&failures, "  %s: %v\n", path, err)
		}
		return nil
	}

	override := os.Getenv("DISCORD_IPC_PATH")
	if override != "" {
		if !filepath.IsAbs(override) {
			return nil, fmt.Errorf("%w: DISCORD_IPC_PATH %q is not an absolute path", ErrDiscordIPCOverrideInvalid, override)
		}
		if conn := tryCandidates(discordIPCOverrideCandidates(override, os.Lstat)); conn != nil {
			return conn, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !endpointFound {
			return nil, fmt.Errorf("%w: DISCORD_IPC_PATH=%q override not connectable:\n%s", ErrDiscordIPCNotFound, override, failures.String())
		}
		return nil, fmt.Errorf("%w: DISCORD_IPC_PATH=%q override not connectable:\n%s", ErrDiscordIPCUnreachable, override, failures.String())
	}
	for _, dir := range discordIPCCandidateDirs(baseDirs) {
		if budgetExhausted {
			break
		}
		paths := make([]string, 0, 10)
		for i := 0; i <= 9; i++ {
			paths = append(paths, filepath.Join(dir, fmt.Sprintf("discord-ipc-%d", i)))
		}
		if conn := tryCandidates(paths); conn != nil {
			return conn, nil
		}
	}
	if !budgetExhausted {
		if conn := tryCandidates(discordIPCGlobCandidates(baseDirs)); conn != nil {
			return conn, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if !endpointFound {
		return nil, fmt.Errorf("%w:\n%s", ErrDiscordIPCNotFound, failures.String())
	}
	return nil, fmt.Errorf("%w:\n%s", ErrDiscordIPCUnreachable, failures.String())
}

// dialDiscordIPCSocket returns the connection, whether the candidate is
// evidence that a Discord IPC endpoint exists, and the failure if any. The
// boolean is what dialDiscordIPC aggregates into endpointFound, which picks
// ErrDiscordIPCUnreachable over ErrDiscordIPCNotFound and so decides whether
// `termp status` says "running but unreachable" or "not running". It must
// therefore claim no more than the probe actually established (issue #468;
// the same principle formatDiscordStatus records from the #423 review).
func dialDiscordIPCSocket(ctx context.Context, path string) (net.Conn, bool, error) {
	before, err := validateSocketCandidate(path, os.Geteuid())
	if err != nil {
		return nil, validationErrorProvesEndpoint(err), err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, dialErrorProvesEndpoint(err), err
	}
	if err := validateConnectedSocket(conn, path, before, os.Geteuid()); err != nil {
		// A completed connect proves a listener held this socket, whatever
		// the post-connect validation went on to reject. Always an endpoint.
		_ = conn.Close()
		return nil, true, err
	}
	return conn, true, nil
}

// validationErrorProvesEndpoint classifies a pre-dial validateSocketCandidate
// failure. The sub-cases are not equivalent and must not be blanket-mapped:
//
//   - absent path: nothing there. Not an endpoint.
//   - not a Unix socket (a regular file or directory named discord-ipc-N):
//     nothing at that path could be Discord, so it is not evidence Discord is
//     running. Not an endpoint (issue #468).
//   - socket owned by another UID: someone's Discord genuinely is running,
//     this user just cannot use it. Still an endpoint — "unreachable" is the
//     truthful answer, and downgrading it would mask the ownership gate
//     added in #450.
//   - anything else (directory resolution, lstat, world-writable parent,
//     undeterminable owner): establishes nothing either way. Kept as an
//     endpoint because that is the conservative reading — it never asserts
//     the absence the probe failed to observe, and the underlying failure is
//     reproduced verbatim in the aggregated error text.
func validationErrorProvesEndpoint(err error) bool {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return false
	case errors.Is(err, errIPCCandidateNotSocket):
		return false
	default:
		return true
	}
}

// dialErrorProvesEndpoint classifies a failed connect(2) to a path that
// already passed socket validation.
//
// ECONNREFUSED on a Unix socket is positive evidence in the other direction:
// the inode exists and no process holds a listening socket bound to it. That
// is the residue a crashed or killed Discord leaves behind, so it means
// Discord is not running rather than unreachable (issue #468).
//
// A path that vanished between the lstat and the connect is likewise nothing.
// Everything else — deadline exceeded, EACCES, or an unrecognised errno —
// keeps the endpoint claim, because none of them rule out a live listener.
func dialErrorProvesEndpoint(err error) bool {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return false
	case errors.Is(err, os.ErrNotExist):
		return false
	default:
		return true
	}
}

func discordIPCOverrideCandidates(value string, lstat func(string) (os.FileInfo, error)) []string {
	if value == "" || !filepath.IsAbs(value) {
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
	type candidate struct {
		path  string
		index int
	}
	var candidates []candidate
	seen := make(map[string]struct{})
	for _, baseDir := range baseDirs {
		matches, err := filepath.Glob(filepath.Join(baseDir, "*", "discord-ipc-*"))
		if err != nil {
			continue
		}
		for _, path := range matches {
			path = filepath.Clean(path)
			name := filepath.Base(path)
			indexText := strings.TrimPrefix(name, "discord-ipc-")
			if indexText == "" || strings.IndexFunc(indexText, func(r rune) bool {
				return r < '0' || r > '9'
			}) != -1 {
				continue
			}
			index, err := strconv.Atoi(indexText)
			if err != nil {
				continue
			}
			if _, ok := seen[path]; ok {
				continue
			}
			seen[path] = struct{}{}
			candidates = append(candidates, candidate{path: path, index: index})
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].index != candidates[j].index {
			return candidates[i].index < candidates[j].index
		}
		return candidates[i].path < candidates[j].path
	})
	paths := make([]string, len(candidates))
	for i, candidate := range candidates {
		paths[i] = candidate.path
	}
	return paths
}

func validateSocketCandidate(path string, euid int) (os.FileInfo, error) {
	return validateSocketCandidateWithLstat(path, euid, os.Lstat, filepath.EvalSymlinks)
}

// validateSocketCandidateWithLstat validates the socket at path. The parent
// directory is resolved through evalSymlinks first (macOS ships /tmp as a
// symlink to /private/tmp, so a literal lstat of the parent directory would
// see a symlink, not a directory, and every /tmp candidate would be rejected
// as an error rather than treated as merely absent). The socket path itself
// is still lstat'd (never stat'd) within the resolved directory so a
// symlinked socket planted by another user is still refused.
func validateSocketCandidateWithLstat(path string, euid int, lstat func(string) (os.FileInfo, error), evalSymlinks func(string) (string, error)) (os.FileInfo, error) {
	dir := filepath.Dir(path)
	resolvedDir, err := evalSymlinks(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve socket directory: %w", err)
	}
	dirInfo, err := lstat(resolvedDir)
	if err != nil {
		return nil, fmt.Errorf("inspect socket directory: %w", err)
	}
	if !dirInfo.IsDir() {
		return nil, fmt.Errorf("socket directory is not a directory")
	}
	if dirInfo.Mode().Perm()&0002 != 0 {
		// Discord commonly places its socket directly in the sticky global /tmp.
		// The socket ownership check below keeps that compatible fallback safe.
		// Compare against /tmp's resolved path too, since /tmp itself may be a
		// symlink (macOS: /tmp -> /private/tmp) and resolvedDir above is already
		// resolved.
		resolvedStickyTmp, stickyErr := evalSymlinks("/tmp")
		if stickyErr != nil {
			resolvedStickyTmp = "/tmp"
		}
		if filepath.Clean(resolvedDir) != filepath.Clean(resolvedStickyTmp) || dirInfo.Mode()&os.ModeSticky == 0 {
			return nil, fmt.Errorf("socket directory is world-writable")
		}
	}

	resolvedPath := filepath.Join(resolvedDir, filepath.Base(path))
	info, err := lstat(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("inspect socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return nil, errIPCCandidateNotSocket
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return nil, fmt.Errorf("cannot determine socket owner")
	}
	if int(stat.Uid) != euid {
		return nil, fmt.Errorf("%w: socket owner UID %d, effective UID %d", errIPCCandidateForeignOwner, stat.Uid, euid)
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
