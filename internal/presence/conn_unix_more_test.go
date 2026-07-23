//go:build !windows

package presence

import (
	"errors"
	"fmt"
	"io/fs"
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
			got, err := validateSocketCandidateWithLstat(path, euid, lstat)
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
	if _, err := validateSocketCandidateWithLstat(path, euid, lstat); err != nil {
		t.Fatal(err)
	}
}
