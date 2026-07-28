//go:build !windows

package presence

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeFileInfo struct {
	name string
	mode os.FileMode
	sys  any
}

func newIsolatedIPCSocket(t *testing.T) (outerDir, socketPath string, listener net.Listener) {
	t.Helper()

	outerDir, err := os.MkdirTemp("/tmp", "termp-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(outerDir) })

	socketDir := filepath.Join(outerDir, "s")
	if err := os.Mkdir(socketDir, 0o700); err != nil {
		t.Fatal(err)
	}
	socketPath = filepath.Join(socketDir, "discord-ipc-0")
	if filepath.Clean(filepath.Dir(socketPath)) == "/tmp" {
		t.Fatalf("IPC test socket %q must not be directly globbable from /tmp", socketPath)
	}
	const comfortableUnixSocketPathLimit = 90
	if len(socketPath) > comfortableUnixSocketPathLimit {
		t.Fatalf("IPC test socket path length = %d, want <= %d: %q", len(socketPath), comfortableUnixSocketPathLimit, socketPath)
	}

	listener, err = net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	info, err := os.Lstat(socketPath)
	if err != nil {
		t.Fatalf("IPC test socket did not bind: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("IPC test socket mode = %v, want Unix socket", info.Mode())
	}
	return outerDir, socketPath, listener
}

func (f fakeFileInfo) Name() string       { return f.name }
func (f fakeFileInfo) Size() int64        { return 0 }
func (f fakeFileInfo) Mode() os.FileMode  { return f.mode }
func (f fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeFileInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeFileInfo) Sys() any           { return f.sys }

func TestDiscordIPCCandidateDirs(t *testing.T) {
	runtimeDir := filepath.Join(string(filepath.Separator), "run", "user", "501")
	got := discordIPCCandidateDirs([]string{
		runtimeDir,
		filepath.Join(runtimeDir, "."),
		filepath.Join(runtimeDir, "snap.discord"),
	})
	want := []string{
		runtimeDir,
		filepath.Join(runtimeDir, "snap.discord"),
		filepath.Join(runtimeDir, "app", "com.discordapp.Discord"),
		filepath.Join(runtimeDir, "app", "com.discordapp.DiscordCanary"),
		filepath.Join(runtimeDir, "app", "com.discordapp.DiscordPTB"),
		filepath.Join(runtimeDir, "snap.discord", "snap.discord"),
		filepath.Join(runtimeDir, "snap.discord", "app", "com.discordapp.Discord"),
		filepath.Join(runtimeDir, "snap.discord", "app", "com.discordapp.DiscordCanary"),
		filepath.Join(runtimeDir, "snap.discord", "app", "com.discordapp.DiscordPTB"),
	}
	if len(got) != len(want) {
		t.Fatalf("candidate directories = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("candidate directory %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestDiscordIPCOverrideCandidates(t *testing.T) {
	socketPath := filepath.Join(string(filepath.Separator), "run", "user", "501", "custom-ipc")
	dirPath := filepath.Dir(socketPath)
	lstatCalls := 0
	lstat := func(path string) (os.FileInfo, error) {
		lstatCalls++
		switch path {
		case socketPath:
			return fakeFileInfo{name: "custom-ipc", mode: os.ModeSocket}, nil
		case dirPath:
			return fakeFileInfo{name: "501", mode: os.ModeDir | 0o700}, nil
		default:
			return nil, fs.ErrNotExist
		}
	}

	if got := discordIPCOverrideCandidates("", lstat); got != nil {
		t.Fatalf("unset override candidates = %v, want nil", got)
	}
	if lstatCalls != 0 {
		t.Fatalf("unset override called lstat %d times, want 0", lstatCalls)
	}
	if got := discordIPCOverrideCandidates("relative/discord-ipc-0", lstat); got != nil {
		t.Fatalf("relative override candidates = %v, want nil", got)
	}
	if lstatCalls != 0 {
		t.Fatalf("relative override called lstat %d times, want 0", lstatCalls)
	}

	gotFile := discordIPCOverrideCandidates(socketPath, lstat)
	if len(gotFile) != 1 || gotFile[0] != socketPath {
		t.Fatalf("socket override candidates = %v, want [%s]", gotFile, socketPath)
	}

	gotDir := discordIPCOverrideCandidates(dirPath, lstat)
	if len(gotDir) != 10 {
		t.Fatalf("directory override candidate count = %d, want 10", len(gotDir))
	}
	for i, got := range gotDir {
		want := filepath.Join(dirPath, fmt.Sprintf("discord-ipc-%d", i))
		if got != want {
			t.Errorf("directory override candidate %d = %q, want %q", i, got, want)
		}
	}
}

// TestDialDiscordIPCOverrideAuthoritative reproduces #409: DISCORD_IPC_PATH
// must be authoritative. If the override's own candidates fail, dialDiscordIPC
// must report failure naming the override rather than silently falling
// through to the default candidate search — which, on a real machine, could
// connect to a genuine running Discord instance and leak live presence.
func TestDialDiscordIPCOverrideAuthoritative(t *testing.T) {
	decoyDir, _, listener := newIsolatedIPCSocket(t)

	accepted := make(chan struct{}, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = conn.Close()
			accepted <- struct{}{}
		}
	}()

	// Point every fallback base dir at the decoy so a fallthrough to the
	// default candidate search would connect to it and prove the bug is
	// fixed, not merely untriggered.
	t.Setenv("XDG_RUNTIME_DIR", decoyDir)
	t.Setenv("TMPDIR", decoyDir)
	t.Setenv("TMP", decoyDir)
	t.Setenv("TEMP", decoyDir)
	t.Setenv("DISCORD_IPC_PATH", filepath.Join(decoyDir, "does-not-exist"))

	conn, err := dialDiscordIPC(context.Background())
	if conn != nil {
		_ = conn.Close()
		t.Fatal("dialDiscordIPC returned a connection, want override failure")
	}
	if !errors.Is(err, ErrDiscordIPCNotFound) {
		t.Fatalf("error = %v, want ErrDiscordIPCNotFound", err)
	}
	if !strings.Contains(err.Error(), "DISCORD_IPC_PATH=") {
		t.Fatalf("error = %v, want it to name the override", err)
	}

	select {
	case <-accepted:
		t.Fatal("fallback connected to the decoy socket despite an authoritative override")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestDialDiscordIPCRejectsRelativeOverrideAsError proves a non-absolute
// DISCORD_IPC_PATH is a hard error (not a logged "override ignored") and that
// the default candidate search is never consulted in that case either.
func TestDialDiscordIPCRejectsRelativeOverrideAsError(t *testing.T) {
	t.Setenv("DISCORD_IPC_PATH", "relative/discord-ipc-0")
	_, err := dialDiscordIPC(context.Background())
	if !errors.Is(err, ErrDiscordIPCOverrideInvalid) {
		t.Fatalf("error = %v, want ErrDiscordIPCOverrideInvalid", err)
	}
}

func TestStatusProbeReturnsPromptlyWhenContextCancelled(t *testing.T) {
	_, socketPath, listener := newIsolatedIPCSocket(t)
	t.Setenv("DISCORD_IPC_PATH", socketPath)

	handshakeRead := make(chan error, 1)
	releaseServer := make(chan struct{})
	defer close(releaseServer)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			handshakeRead <- err
			return
		}
		defer conn.Close()
		_, err = readFrame(conn)
		handshakeRead <- err
		<-releaseServer
	}()

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- StatusProbe(ctx, "app-id")
	}()
	if err := <-handshakeRead; err != nil {
		t.Fatalf("read handshake: %v", err)
	}

	started := time.Now()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("StatusProbe error = %v, want context canceled", err)
		}
		if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
			t.Fatalf("StatusProbe returned after %v, want prompt cancellation", elapsed)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("StatusProbe did not observe cancellation before status timeout %v", statusIOTimeout)
	}
}

func TestDiscordIPCGlobCandidatesFiltersSortsAndDedupes(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "new-package")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	var numericPaths []string
	for _, index := range []string{"10", "2", "0"} {
		path := filepath.Join(nested, "discord-ipc-"+index)
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		numericPaths = append(numericPaths, path)
	}
	for _, name := range []string{"discord-ipc-old", "discord-ipc-1x", "discord-ipc-", "unrelated"} {
		if err := os.WriteFile(filepath.Join(nested, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	deeper := filepath.Join(nested, "deeper")
	if err := os.Mkdir(deeper, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deeper, "discord-ipc-8"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	got := discordIPCGlobCandidates([]string{base, filepath.Join(base, ".")})
	want := []string{numericPaths[2], numericPaths[1], numericPaths[0]}
	if len(got) != len(want) {
		t.Fatalf("glob candidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("glob candidate %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestValidateSocketCandidateRealSymlinkedParentDirectory exercises the #417
// fix end to end against the real filesystem: real os.Lstat and real
// filepath.EvalSymlinks (no injected fakes), so this proves the production
// wiring in validateSocketCandidate resolves a symlinked parent directory,
// not just that the algorithm is correct in isolation. It also re-proves the
// anti-symlink security guarantee (a symlinked *socket* path is still
// refused) using the same real directories.
func TestValidateSocketCandidateRealSymlinkedParentDirectory(t *testing.T) {
	base, socketPath, _ := newIsolatedIPCSocket(t)
	realDir := filepath.Dir(socketPath)
	linkDir := filepath.Join(base, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}

	t.Run("accepts socket reached through a symlinked parent directory", func(t *testing.T) {
		viaSymlink := filepath.Join(linkDir, "discord-ipc-0")
		info, err := validateSocketCandidate(viaSymlink, os.Geteuid())
		if err != nil {
			t.Fatalf("validateSocketCandidate(%s): %v", viaSymlink, err)
		}
		if info == nil || info.Mode()&os.ModeSocket == 0 {
			t.Fatalf("info = %#v, want socket info", info)
		}
	})

	t.Run("still refuses a symlinked socket path inside a real parent", func(t *testing.T) {
		evilLink := filepath.Join(realDir, "discord-ipc-evil")
		if err := os.Symlink(socketPath, evilLink); err != nil {
			t.Fatal(err)
		}
		_, err := validateSocketCandidate(evilLink, os.Geteuid())
		if err == nil {
			t.Fatal("validateSocketCandidate accepted a symlinked socket path, want refusal")
		}
		if !strings.Contains(err.Error(), "not a Unix socket") {
			t.Fatalf("error = %v, want \"not a Unix socket\"", err)
		}
	})
}

func TestValidateSocketCandidateMatrix(t *testing.T) {
	const euid = 501
	dir := filepath.Join(string(filepath.Separator), "run", "user", "501")
	path := filepath.Join(dir, "discord-ipc-0")
	dirInfo := fakeFileInfo{name: "501", mode: os.ModeDir | 0o700}
	socketInfo := fakeFileInfo{name: "discord-ipc-0", mode: os.ModeSocket | 0o600, sys: &syscall.Stat_t{Uid: euid}}

	tests := []struct {
		name    string
		lookup  map[string]os.FileInfo
		lookupE map[string]error
		wantErr string
	}{
		{name: "valid", lookup: map[string]os.FileInfo{dir: dirInfo, path: socketInfo}},
		{name: "missing directory", lookupE: map[string]error{dir: fs.ErrNotExist}, wantErr: "inspect socket directory"},
		{name: "directory is file", lookup: map[string]os.FileInfo{dir: fakeFileInfo{mode: 0o600}}, wantErr: "not a directory"},
		{name: "world writable directory", lookup: map[string]os.FileInfo{dir: fakeFileInfo{mode: os.ModeDir | 0o702}}, wantErr: "world-writable"},
		{name: "missing socket", lookup: map[string]os.FileInfo{dir: dirInfo}, lookupE: map[string]error{path: fs.ErrNotExist}, wantErr: "inspect socket"},
		{name: "regular file", lookup: map[string]os.FileInfo{dir: dirInfo, path: fakeFileInfo{mode: 0o600}}, wantErr: "not a Unix socket"},
		{name: "unknown owner representation", lookup: map[string]os.FileInfo{dir: dirInfo, path: fakeFileInfo{mode: os.ModeSocket, sys: struct{}{}}}, wantErr: "cannot determine socket owner"},
		{name: "foreign owner", lookup: map[string]os.FileInfo{dir: dirInfo, path: fakeFileInfo{mode: os.ModeSocket, sys: &syscall.Stat_t{Uid: euid + 1}}}, wantErr: "does not match effective UID"},
	}
	identityEval := func(p string) (string, error) { return p, nil }
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lstat := func(name string) (os.FileInfo, error) {
				if err := tt.lookupE[name]; err != nil {
					return nil, err
				}
				if info := tt.lookup[name]; info != nil {
					return info, nil
				}
				return nil, fs.ErrNotExist
			}
			got, err := validateSocketCandidateWithLstat(path, euid, lstat, identityEval)
			if tt.wantErr == "" {
				if err != nil || got != socketInfo {
					t.Fatalf("result = %#v, %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidateSocketCandidateAllowsStickyGlobalTmp(t *testing.T) {
	const euid = 501
	path := "/tmp/discord-ipc-0"
	lstat := func(name string) (os.FileInfo, error) {
		switch name {
		case "/tmp":
			return fakeFileInfo{mode: os.ModeDir | os.ModeSticky | 0o777}, nil
		case path:
			return fakeFileInfo{mode: os.ModeSocket | 0o600, sys: &syscall.Stat_t{Uid: euid}}, nil
		default:
			return nil, errors.New("unexpected path")
		}
	}
	identityEval := func(p string) (string, error) { return p, nil }
	if _, err := validateSocketCandidateWithLstat(path, euid, lstat, identityEval); err != nil {
		t.Fatal(err)
	}
}

// TestValidateSocketCandidateResolvesSymlinkedTmp reproduces #417: macOS ships
// /tmp as a symlink to /private/tmp, so a candidate directly under /tmp must
// still validate once the parent directory is resolved, and the sticky-/tmp
// carve-out must compare against the resolved path rather than the literal
// string "/tmp".
func TestValidateSocketCandidateResolvesSymlinkedTmp(t *testing.T) {
	const euid = 501
	path := "/tmp/discord-ipc-0"
	resolvedPath := "/private/tmp/discord-ipc-0"
	lstat := func(name string) (os.FileInfo, error) {
		switch name {
		case "/private/tmp":
			return fakeFileInfo{mode: os.ModeDir | os.ModeSticky | 0o777}, nil
		case resolvedPath:
			return fakeFileInfo{mode: os.ModeSocket | 0o600, sys: &syscall.Stat_t{Uid: euid}}, nil
		default:
			return nil, fmt.Errorf("unexpected lstat(%s)", name)
		}
	}
	eval := func(p string) (string, error) {
		switch p {
		case "/tmp":
			return "/private/tmp", nil
		default:
			return "", fmt.Errorf("unexpected EvalSymlinks(%s)", p)
		}
	}
	info, err := validateSocketCandidateWithLstat(path, euid, lstat, eval)
	if err != nil {
		t.Fatalf("validateSocketCandidateWithLstat: %v", err)
	}
	if info == nil {
		t.Fatal("info = nil, want socket info")
	}
}

// TestValidateSocketCandidateStillRejectsSymlinkedSocket proves the anti-symlink
// guarantee on the socket file itself survives the #417 fix: resolving the
// *directory* must not cause the socket path's own final component to be
// followed through a symlink planted by another user.
func TestValidateSocketCandidateStillRejectsSymlinkedSocket(t *testing.T) {
	const euid = 501
	path := "/private/tmp/discord-ipc-0"
	lstat := func(name string) (os.FileInfo, error) {
		switch name {
		case "/private/tmp":
			return fakeFileInfo{mode: os.ModeDir | 0o700}, nil
		case path:
			// A symlink has neither the directory nor socket mode bit set.
			return fakeFileInfo{mode: os.ModeSymlink | 0o777}, nil
		default:
			return nil, fmt.Errorf("unexpected lstat(%s)", name)
		}
	}
	identityEval := func(p string) (string, error) { return p, nil }
	_, err := validateSocketCandidateWithLstat(path, euid, lstat, identityEval)
	if err == nil || !strings.Contains(err.Error(), "not a Unix socket") {
		t.Fatalf("error = %v, want \"not a Unix socket\"", err)
	}
}

// TestValidateSocketCandidatePropagatesSymlinkResolutionFailure ensures a
// directory that cannot be resolved (e.g. a broken symlink) surfaces as an
// error rather than silently falling through.
func TestValidateSocketCandidatePropagatesSymlinkResolutionFailure(t *testing.T) {
	resolveErr := errors.New("broken symlink")
	eval := func(string) (string, error) { return "", resolveErr }
	lstat := func(string) (os.FileInfo, error) {
		t.Fatal("lstat should not be called when symlink resolution fails")
		return nil, nil
	}
	_, err := validateSocketCandidateWithLstat("/tmp/discord-ipc-0", 501, lstat, eval)
	if !errors.Is(err, resolveErr) {
		t.Fatalf("error = %v, want wrapped %v", err, resolveErr)
	}
}
