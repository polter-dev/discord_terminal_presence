//go:build !windows

package presence

import (
	"errors"
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
