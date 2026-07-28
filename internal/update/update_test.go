package update

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	mu     sync.Mutex
	latest string
	err    error
	calls  int
}

type recordingRunner struct {
	command  Command
	commands []Command
	calls    int
	err      error
}

type skippedUpdateError struct{}

func (skippedUpdateError) Error() string { return "update skipped" }

func (skippedUpdateError) AutomaticUpdateSkipped() bool { return true }

func (r *recordingRunner) Run(_ context.Context, command Command, _ io.Reader, _, _ io.Writer) error {
	r.calls++
	r.command = command
	r.commands = append(r.commands, command)
	if r.err != nil {
		return r.err
	}
	if command.Name == "curl" {
		for i, arg := range command.Args {
			if arg == "-o" && i+1 < len(command.Args) {
				return os.WriteFile(command.Args[i+1], []byte("#!/bin/sh\n"), 0o600)
			}
		}
	}
	return nil
}

func (s *fakeSource) Latest(context.Context, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.latest, s.err
}

func (s *fakeSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func requireSymlink(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable on this account: %v", err)
	}
}

func TestIsNewerSemver(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "patch", current: "1.2.3", latest: "1.2.4", want: true},
		{name: "v prefix", current: "v1.2.3", latest: "v2.0.0", want: true},
		{name: "build metadata ignored", current: "1.2.3+abc123", latest: "1.2.3+def456", want: false},
		{name: "newer despite metadata", current: "1.2.3+abc123", latest: "1.2.4+def456", want: true},
		{name: "stable after prerelease", current: "1.2.3-rc.1", latest: "1.2.3", want: true},
		{name: "prerelease numeric", current: "1.2.3-rc.2", latest: "1.2.3-rc.10", want: true},
		{name: "older", current: "2.0.0", latest: "1.9.9", want: false},
		{name: "equal", current: "1.2.3", latest: "1.2.3", want: false},
		{name: "dev", current: "dev", latest: "99.0.0", want: false},
		{name: "invalid current", current: "main", latest: "1.0.0", want: false},
		{name: "invalid latest", current: "1.0.0", latest: "release", want: false},
		{name: "invalid leading zero", current: "1.0.0", latest: "1.01.0", want: false},
		{name: "invalid metadata", current: "1.0.0", latest: "1.0.1+", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNewer(tt.current, tt.latest); got != tt.want {
				t.Fatalf("IsNewer(%q, %q) = %t, want %t", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestGenericCommandPinsInstallerAndVersionToReleaseTag(t *testing.T) {
	const tag = "v1.2.3"
	command := GenericCommand(tag)
	if strings.Contains(command, "/main/") {
		t.Fatalf("generic command uses mutable main branch: %q", command)
	}
	if want := "raw.githubusercontent.com/polter-dev/discord_terminal_presence/" + tag + "/install.sh"; !strings.Contains(command, want) {
		t.Fatalf("generic command = %q, want tagged installer URL containing %q", command, want)
	}
	if !strings.Contains(command, "VERSION="+tag+" sh") {
		t.Fatalf("generic command = %q, want installer version pinned to %q", command, tag)
	}
}

func TestGoCommandPinsReleaseTag(t *testing.T) {
	const tag = "v1.2.3"
	want := "go install github.com/polter-dev/discord_terminal_presence/cmd/termp@" + tag
	if got := GoCommand(tag); got != want {
		t.Fatalf("GoCommand(%q) = %q, want %q", tag, got, want)
	}
	for _, invalid := range []string{"", "latest", "v1.2.3; id"} {
		if got := GoCommand(invalid); got != "" {
			t.Fatalf("GoCommand(%q) = %q, want empty command", invalid, got)
		}
	}
}

func TestGenericUpdateRejectsUnsafeReleaseTagsWithoutRunning(t *testing.T) {
	for _, tag := range []string{"", "latest", "v1.2.3; id", "../v1.2.3", "v1.2.3\nmain"} {
		t.Run(strings.ReplaceAll(tag, "/", "_"), func(t *testing.T) {
			runner := &recordingRunner{}
			err := PerformUpdate(context.Background(), InstallGeneric, tag, runner, nil, io.Discard, io.Discard)
			if err == nil {
				t.Fatalf("PerformUpdate() accepted unsafe release tag %q", tag)
			}
			if runner.calls != 0 {
				t.Fatalf("PerformUpdate() ran command for unsafe release tag %q", tag)
			}
		})
	}
}

func TestPerformGenericUpdateUsesResolvedReleaseTag(t *testing.T) {
	t.Setenv("BINDIR", "/wrong/install/directory")
	runner := &recordingRunner{}
	err := PerformUpdate(context.Background(), InstallGeneric, "v2.3.4", runner, nil, io.Discard, io.Discard)
	if runtime.GOOS == "windows" {
		if err == nil || !strings.Contains(err.Error(), "not supported on Windows") ||
			!strings.Contains(err.Error(), "go install") {
			t.Fatalf("Windows generic update error = %v, want supported-path guidance", err)
		}
		if runner.calls != 0 {
			t.Fatalf("unsupported Windows generic update invoked runner %d times", runner.calls)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 2 {
		t.Fatalf("runner calls = %d, want 2", runner.calls)
	}
	download, install := runner.commands[0], runner.commands[1]
	wantURL := "https://raw.githubusercontent.com/polter-dev/discord_terminal_presence/v2.3.4/install.sh"
	if download.Name != "curl" || len(download.Args) != 4 || download.Args[1] != wantURL || download.Args[2] != "-o" {
		t.Fatalf("download command = %#v", download)
	}
	wantEnv := []string{
		"VERSION=v2.3.4",
		"BINDIR=" + filepath.Dir(mustResolvedExecutable(t)),
		"TERMP_DOWNLOAD_URL=https://termp.polter.sh/dl/update/" + runtime.GOOS + "/" + runtime.GOARCH + "/v2.3.4",
	}
	if install.Name != "sh" || len(install.Args) != 1 || !reflect.DeepEqual(install.Env, wantEnv) {
		t.Fatalf("install command = %#v", install)
	}
	if install.Args[0] != download.Args[3] {
		t.Fatalf("installer path = %q, downloaded path = %q", install.Args[0], download.Args[3])
	}
	if _, err := os.Stat(download.Args[3]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary installer was not cleaned up: %v", err)
	}
}

func mustResolvedExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestGenericInstallDirUsesResolvedExecutableDirectory(t *testing.T) {
	got, err := genericInstallDir(
		func() (string, error) { return "/custom/bin/termp-link", nil },
		func(path string) (string, error) {
			if path != "/custom/bin/termp-link" {
				t.Fatalf("evalSymlinks(%q), want symlink path", path)
			}
			return "/srv/termp/bin/termp", nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Clean("/srv/termp/bin"); got != want {
		t.Fatalf("genericInstallDir() = %q, want %q", got, want)
	}
}

func TestUpdateArchiveURLMapsSupportedTargets(t *testing.T) {
	tests := []struct {
		goos   string
		goarch string
		want   string
	}{
		{goos: "darwin", goarch: "amd64", want: "https://termp.polter.sh/dl/update/darwin/amd64/v2.3.4"},
		{goos: "darwin", goarch: "arm64", want: "https://termp.polter.sh/dl/update/darwin/arm64/v2.3.4"},
		{goos: "linux", goarch: "amd64", want: "https://termp.polter.sh/dl/update/linux/amd64/v2.3.4"},
		{goos: "linux", goarch: "arm64", want: "https://termp.polter.sh/dl/update/linux/arm64/v2.3.4"},
	}
	for _, tt := range tests {
		t.Run(tt.goos+"/"+tt.goarch, func(t *testing.T) {
			got, err := updateArchiveURL(tt.goos, tt.goarch, "v2.3.4")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("updateArchiveURL(%q, %q, %q) = %q, want %q", tt.goos, tt.goarch, "v2.3.4", got, tt.want)
			}
		})
	}
}

func TestUpdateArchiveURLRejectsUnsupportedTargets(t *testing.T) {
	for _, target := range [][2]string{{"windows", "amd64"}, {"linux", "386"}} {
		if got, err := updateArchiveURL(target[0], target[1], "v2.3.4"); err == nil || got != "" {
			t.Fatalf("updateArchiveURL(%q, %q, %q) = (%q, %v), want error", target[0], target[1], "v2.3.4", got, err)
		}
	}
}

func TestHomebrewUpdateUsesQualifiedCommand(t *testing.T) {
	want := Command{Name: "brew", Args: []string{"upgrade", "polter-dev/tap/termp"}}
	got, err := UpdateCommandForMethod(InstallHomebrew, "v2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UpdateCommandForMethod(InstallHomebrew) = %#v, want %#v", got, want)
	}
	if BrewCommand != "brew upgrade polter-dev/tap/termp" {
		t.Fatalf("BrewCommand = %q, want fully qualified upgrade command", BrewCommand)
	}
}

func TestSystemPackageCommands(t *testing.T) {
	tests := []struct {
		method InstallMethod
		want   string
	}{
		{method: InstallDebian, want: DebianCommand},
		{method: InstallRPM, want: RPMCommand},
		{method: InstallSystemPackage, want: SystemPackageCommand},
	}
	for _, tt := range tests {
		t.Run(string(tt.method), func(t *testing.T) {
			if got := CommandForMethod(tt.method, "v2.3.4"); got != tt.want {
				t.Fatalf("CommandForMethod(%q) = %q, want %q", tt.method, got, tt.want)
			}
			if _, err := UpdateCommandForMethod(tt.method, "v2.3.4"); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("UpdateCommandForMethod(%q) error = %v, want package guidance", tt.method, err)
			}
			runner := &recordingRunner{}
			if err := PerformUpdate(context.Background(), tt.method, "v2.3.4", runner, nil, io.Discard, io.Discard); err == nil {
				t.Fatalf("PerformUpdate(%q) error = nil, want package guidance", tt.method)
			}
			if runner.calls != 0 {
				t.Fatalf("PerformUpdate(%q) ran %d commands, want none", tt.method, runner.calls)
			}
		})
	}
}

func TestPerformGenericUpdateReturnsDownloadFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("generic shell updater is unsupported on Windows")
	}
	runner := &recordingRunner{err: errors.New("simulated fetch failure")}
	err := PerformUpdate(context.Background(), InstallGeneric, "v2.3.4", runner, nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "simulated fetch failure") {
		t.Fatalf("PerformUpdate() error = %v, want fetch failure", err)
	}
	if runner.calls != 1 || runner.command.Name != "curl" {
		t.Fatalf("runner = (%d, %#v), want one curl call", runner.calls, runner.command)
	}
}

func TestPerformGenericUpdateLogsTemporaryInstallerCleanupFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("generic shell updater is unsupported on Windows")
	}
	originalRemove := removeTemporaryInstaller
	originalLogOutput := log.Writer()
	t.Cleanup(func() {
		removeTemporaryInstaller = originalRemove
		log.SetOutput(originalLogOutput)
	})

	removeTemporaryInstaller = func(path string) error {
		_ = os.Remove(path)
		return errors.New("simulated cleanup failure")
	}
	var logs bytes.Buffer
	log.SetOutput(&logs)

	if err := PerformUpdate(context.Background(), InstallGeneric, "v2.3.4", &recordingRunner{}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), "simulated cleanup failure") {
		t.Fatalf("cleanup failure was not logged: %q", logs.String())
	}
}

func TestGenericUpdateSupportRejectsStandardWindows(t *testing.T) {
	err := genericUpdatePlatformError("windows", "v2.3.4")
	if err == nil || !strings.Contains(err.Error(), "not supported on Windows") ||
		!strings.Contains(err.Error(), "go install") {
		t.Fatalf("Windows generic update error = %v, want supported-path guidance", err)
	}
	for _, goos := range []string{"darwin", "linux"} {
		if err := genericUpdatePlatformError(goos, "v2.3.4"); err != nil {
			t.Fatalf("generic updater unexpectedly rejects %s: %v", goos, err)
		}
	}
}

func TestInstallerRejectsMalformedSemverTags(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is not used on Windows")
	}
	script := filepath.Join("..", "..", "install.sh")
	for _, tag := range []string{"v1.2.3foo", "1.2.3.4", "1.02.3", "1.2.3-01"} {
		t.Run(tag, func(t *testing.T) {
			cmd := exec.Command("sh", script)
			cmd.Env = append(os.Environ(), "VERSION="+tag, "BINDIR="+t.TempDir())
			output, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "invalid release tag") {
				t.Fatalf("install.sh VERSION=%q = %v, output %q; want invalid release tag", tag, err, output)
			}
		})
	}
}

func TestPerformGoUpdateUsesResolvedReleaseTag(t *testing.T) {
	runner := &recordingRunner{}
	if err := PerformUpdate(context.Background(), InstallGo, "v2.3.4", runner, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := Command{Name: "go", Args: []string{"install", "github.com/polter-dev/discord_terminal_presence/cmd/termp@v2.3.4"}}
	if runner.calls != 1 || runner.command.Name != want.Name || strings.Join(runner.command.Args, "\x00") != strings.Join(want.Args, "\x00") {
		t.Fatalf("runner = (%d, %#v), want (1, %#v)", runner.calls, runner.command, want)
	}
}

func TestGoUpdateRejectsInvalidReleaseTagsWithoutRunning(t *testing.T) {
	for _, tag := range []string{"", "latest", "v1.2.3; id"} {
		t.Run(tag, func(t *testing.T) {
			runner := &recordingRunner{}
			err := PerformUpdate(context.Background(), InstallGo, tag, runner, nil, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "invalid release tag") {
				t.Fatalf("PerformUpdate() error = %v, want invalid release tag", err)
			}
			if runner.calls != 0 {
				t.Fatalf("PerformUpdate() ran command for invalid release tag %q", tag)
			}
		})
	}
}

func TestInstallMethodDetection(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "Users", "test")
	goBin := filepath.Join(string(filepath.Separator), "Users", "test", "bin")
	goPath := filepath.Join(string(filepath.Separator), "opt", "gopath")
	homebrewPrefixes := []string{"/opt/homebrew", "/usr/local"}
	tests := []struct {
		name          string
		path          string
		goos          string
		systemPackage InstallMethod
		want          InstallMethod
	}{
		{name: "Homebrew Cellar", path: filepath.Join("/opt/homebrew/Cellar/termp/1.2.3/bin/termp"), goos: "darwin", want: InstallHomebrew},
		{name: "Homebrew Caskroom", path: filepath.Join("/usr/local/Caskroom/termp/1.2.3/termp"), goos: "darwin", want: InstallHomebrew},
		{name: "unrelated Cellar", path: filepath.Join("/home/alice/Cellar/archive/termp"), goos: "linux", want: InstallGeneric},
		{name: "wrong Cellar package", path: filepath.Join("/opt/homebrew/Cellar/archive/1.2.3/bin/termp"), goos: "darwin", want: InstallGeneric},
		{name: "wrong Caskroom layout", path: filepath.Join("/usr/local/Caskroom/termp/1.2.3/bin/termp"), goos: "darwin", want: InstallGeneric},
		{name: "GOBIN", path: filepath.Join(goBin, "termp"), goos: "linux", want: InstallGo},
		{name: "GOPATH bin", path: filepath.Join(goPath, "bin", "termp"), goos: "linux", want: InstallGo},
		{name: "default home Go bin", path: filepath.Join(home, "go", "bin", "termp"), goos: "linux", want: InstallGo},
		{name: "Debian package", path: filepath.Join("/usr/bin/termp"), goos: "linux", systemPackage: InstallDebian, want: InstallDebian},
		{name: "RPM package", path: filepath.Join("/usr/bin/termp"), goos: "linux", systemPackage: InstallRPM, want: InstallRPM},
		{name: "ambiguous system package", path: filepath.Join("/usr/bin/termp"), goos: "linux", systemPackage: InstallSystemPackage, want: InstallSystemPackage},
		{name: "non-Linux usr bin", path: filepath.Join("/usr/bin/termp"), goos: "darwin", systemPackage: InstallDebian, want: InstallGeneric},
		{name: "generic installer", path: filepath.Join("/usr/local/bin/termp"), goos: "linux", want: InstallGeneric},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			systemPackage := func(string) InstallMethod { return tt.systemPackage }
			got := detectInstall(tt.path, func(path string) (string, error) { return path, nil }, tt.goos, systemPackage, goBin, goPath, home, homebrewPrefixes...)
			if got != tt.want {
				t.Fatalf("detectInstall(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestParseGoEnvPaths(t *testing.T) {
	t.Run("custom GOBIN and GOPATH", func(t *testing.T) {
		goBin := filepath.Join(string(filepath.Separator), "custom", "bin")
		goPath := filepath.Join(string(filepath.Separator), "custom", "gopath")
		got := parseGoEnvPaths([]byte(goBin+"\n"+goPath+"\n"), nil)
		if got != (goInstallPaths{goBin: goBin, goPath: goPath}) {
			t.Fatalf("parseGoEnvPaths() = %#v", got)
		}
		if method := detectResolvedInstall(filepath.Join(goBin, "termp"), "linux", nil, got.goBin, got.goPath, ""); method != InstallGo {
			t.Fatalf("go env GOBIN install = %q, want %q", method, InstallGo)
		}
		if method := detectResolvedInstall(filepath.Join(goPath, "bin", "termp"), "linux", nil, got.goBin, got.goPath, ""); method != InstallGo {
			t.Fatalf("custom go env GOPATH install = %q, want %q", method, InstallGo)
		}
	})

	t.Run("absent toolchain falls back safely", func(t *testing.T) {
		got := parseGoEnvPaths(nil, exec.ErrNotFound)
		if got != (goInstallPaths{}) {
			t.Fatalf("parseGoEnvPaths() = %#v, want empty paths", got)
		}
		rawGoPath := filepath.Join(string(filepath.Separator), "fallback", "gopath")
		if method := detectResolvedInstall(filepath.Join(rawGoPath, "bin", "termp"), "linux", nil, got.goBin, rawGoPath, ""); method != InstallGo {
			t.Fatalf("raw GOPATH fallback install = %q, want %q", method, InstallGo)
		}
		if method := detectResolvedInstall(filepath.Join(string(filepath.Separator), "usr", "local", "bin", "termp"), "linux", nil, got.goBin, "", ""); method != InstallGeneric {
			t.Fatalf("unknown install without toolchain = %q, want %q", method, InstallGeneric)
		}
	})

	t.Run("empty output falls back safely", func(t *testing.T) {
		if got := parseGoEnvPaths(nil, nil); got != (goInstallPaths{}) {
			t.Fatalf("parseGoEnvPaths() = %#v, want empty paths", got)
		}
	})
}

func TestInstallDetectionResolvesSymlinkBeforeMatching(t *testing.T) {
	requireSymlink(t)

	root := t.TempDir()
	target := filepath.Join(root, "Caskroom", "termp", "1.2.3", "termp")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "bin", "termp")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := detectInstall(link, filepath.EvalSymlinks, runtime.GOOS, nil, "", "", root, resolvedRoot); got != InstallHomebrew {
		t.Fatalf("symlinked Homebrew install = %q, want %q", got, InstallHomebrew)
	}
}

func TestInstallDetectionResolvesSymlinkBeforeSystemPackageMatching(t *testing.T) {
	const link = "/usr/local/bin/termp"
	systemPackage := func(path string) InstallMethod {
		if path != "/usr/bin/termp" {
			t.Fatalf("system package lookup path = %q, want resolved path", path)
		}
		return InstallDebian
	}
	got := detectInstall(link, func(path string) (string, error) {
		if path != link {
			t.Fatalf("evalSymlinks(%q), want %q", path, link)
		}
		return "/usr/bin/termp", nil
	}, "linux", systemPackage, "", "", "")
	if got != InstallDebian {
		t.Fatalf("symlinked system package install = %q, want %q", got, InstallDebian)
	}
}

func TestInstallDetectionFallsBackWhenResolutionFails(t *testing.T) {
	got := detectInstall("/opt/homebrew/Cellar/termp/1.2.3/termp", func(string) (string, error) {
		return "", errors.New("cannot resolve")
	}, "darwin", nil, "", "", "")
	if got != InstallGeneric {
		t.Fatalf("ambiguous install = %q, want generic", got)
	}
}

func TestClassifySystemPackage(t *testing.T) {
	tests := []struct {
		name          string
		dpkgOwns      bool
		rpmOwns       bool
		dpkgAvailable bool
		rpmAvailable  bool
		want          InstallMethod
	}{
		{name: "dpkg owns file", dpkgOwns: true, rpmAvailable: true, want: InstallDebian},
		{name: "rpm owns file", rpmOwns: true, dpkgAvailable: true, want: InstallRPM},
		{name: "both databases own file", dpkgOwns: true, rpmOwns: true, want: InstallSystemPackage},
		{name: "only dpkg present", dpkgAvailable: true, want: InstallDebian},
		{name: "only rpm present", rpmAvailable: true, want: InstallRPM},
		{name: "both tools present", dpkgAvailable: true, rpmAvailable: true, want: InstallSystemPackage},
		{name: "no tools present", want: InstallSystemPackage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifySystemPackage(
				"/usr/bin/termp",
				func(string) bool { return tt.dpkgOwns },
				func(string) bool { return tt.rpmOwns },
				func() bool { return tt.dpkgAvailable },
				func() bool { return tt.rpmAvailable },
			)
			if got != tt.want {
				t.Fatalf("classifySystemPackage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCheckerUsesFreshCache(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "update.json")
	if err := writeCache(path, cacheEntry{CheckedAt: now.Add(-time.Hour), Latest: "v1.2.0"}); err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{latest: "v9.0.0"}
	checker := NewChecker(source, path)
	checker.Now = func() time.Time { return now }
	checker.DetectInstall = func() InstallMethod { return InstallGo }

	result, ok := checker.Check(context.Background(), "1.0.0+sha", true)
	if !ok || result.Latest != "v1.2.0" || result.Command != GoCommand("v1.2.0") {
		t.Fatalf("cached result = (%#v, %t)", result, ok)
	}
	if source.callCount() != 0 {
		t.Fatalf("fresh cache made %d source calls", source.callCount())
	}
}

func TestReleaseCacheWritesPreserveAutomaticUpdateAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	attemptedAt := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	if err := RecordAutomaticUpdateAttempt(path, "v1.2.0", attemptedAt, errors.New("permission denied")); err != nil {
		t.Fatal(err)
	}
	if err := writeCache(path, cacheEntry{CheckedAt: attemptedAt.Add(time.Hour), Latest: "v1.3.0"}); err != nil {
		t.Fatal(err)
	}

	attempt, ok := ReadAutomaticUpdateAttempt(path)
	if !ok || attempt.AttemptedAt != attemptedAt || attempt.Target != "v1.2.0" || attempt.Error != "permission denied" {
		t.Fatalf("automatic update attempt after cache write = (%+v, %t)", attempt, ok)
	}
}

func TestConcurrentCacheWritersPreserveAutomaticUpdateAttempt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	attemptedAt := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	start := make(chan struct{})
	errs := make(chan error, 2)

	go func() {
		<-start
		errs <- writeCache(path, cacheEntry{CheckedAt: attemptedAt.Add(time.Hour), Latest: strings.Repeat("v1.3.0", 1<<20)})
	}()
	go func() {
		<-start
		errs <- RecordAutomaticUpdateAttempt(path, "v1.2.0", attemptedAt, errors.New("permission denied"))
	}()
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	attempt, ok := ReadAutomaticUpdateAttempt(path)
	if !ok || attempt.AttemptedAt != attemptedAt || attempt.Target != "v1.2.0" || attempt.Error != "permission denied" {
		t.Fatalf("automatic update attempt after concurrent cache writes = (%+v, %t)", attempt, ok)
	}
}

func TestRecordAutomaticUpdateAttemptPreservesSkippedOutcome(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	attemptedAt := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	if err := RecordAutomaticUpdateAttempt(path, "v1.2.0", attemptedAt, fmt.Errorf("preflight: %w", skippedUpdateError{})); err != nil {
		t.Fatal(err)
	}
	attempt, ok := ReadAutomaticUpdateAttempt(path)
	if !ok || !attempt.Skipped || attempt.Error != "preflight: update skipped" {
		t.Fatalf("skipped automatic update attempt = (%+v, %t)", attempt, ok)
	}
}

func TestCachedCheckNeverUsesReleaseSource(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		checkedAt time.Time
		latest    string
		want      bool
	}{
		{name: "fresh newer", checkedAt: now.Add(-time.Hour), latest: "v1.1.0", want: true},
		{name: "fresh equal", checkedAt: now.Add(-time.Hour), latest: "v1.0.0"},
		{name: "stale newer", checkedAt: now.Add(-cacheLifetime), latest: "v1.1.0"},
		{name: "failed cached check", checkedAt: now.Add(-time.Hour), latest: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "update.json")
			if err := writeCache(path, cacheEntry{CheckedAt: tt.checkedAt, Latest: tt.latest}); err != nil {
				t.Fatal(err)
			}
			source := &fakeSource{err: errors.New("network must not be used")}
			checker := NewChecker(source, path)
			checker.Now = func() time.Time { return now }
			_, got := checker.CachedCheck("1.0.0", true)
			if got != tt.want {
				t.Fatalf("CachedCheck() available = %t, want %t", got, tt.want)
			}
			if source.callCount() != 0 {
				t.Fatalf("CachedCheck made %d source calls", source.callCount())
			}
		})
	}
}

func TestCheckerRefreshesExpiredCache(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "update.json")
	if err := writeCache(path, cacheEntry{CheckedAt: now.Add(-cacheLifetime), Latest: "v1.1.0"}); err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{latest: "v1.3.0"}
	checker := NewChecker(source, path)
	checker.Now = func() time.Time { return now }
	checker.DetectInstall = func() InstallMethod { return InstallHomebrew }

	result, ok := checker.Check(context.Background(), "1.2.0", true)
	if !ok || result.Latest != "v1.3.0" || result.Command != BrewCommand {
		t.Fatalf("refreshed result = (%#v, %t)", result, ok)
	}
	if source.callCount() != 1 {
		t.Fatalf("expired cache made %d source calls, want 1", source.callCount())
	}
	entry, fresh := readFreshCache(path, now)
	if !fresh || entry.Latest != "v1.3.0" {
		t.Fatalf("refreshed cache = (%#v, %t)", entry, fresh)
	}
}

func TestCheckerChecksAtMostOncePerProcess(t *testing.T) {
	source := &fakeSource{latest: "v2.0.0"}
	checker := NewChecker(source, filepath.Join(t.TempDir(), "update.json"))
	checker.DetectInstall = func() InstallMethod { return InstallGeneric }
	for range 2 {
		result, ok := checker.Check(context.Background(), "1.0.0", true)
		if !ok {
			t.Fatal("expected update")
		}
		if result.Command != GenericCommand("v2.0.0") {
			t.Fatalf("generic update command = %q, want %q", result.Command, GenericCommand("v2.0.0"))
		}
	}
	if source.callCount() != 1 {
		t.Fatalf("source calls = %d, want 1", source.callCount())
	}
}

func TestCheckerOptOutsAndDevSkipEntirely(t *testing.T) {
	tests := []struct {
		name          string
		version       string
		configEnabled bool
		envSet        bool
	}{
		{name: "config", version: "1.0.0", configEnabled: false},
		{name: "environment", version: "1.0.0", configEnabled: true, envSet: true},
		{name: "dev", version: "dev", configEnabled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envSet {
				t.Setenv("NO_UPDATE_CHECK", "")
			} else {
				old, existed := os.LookupEnv("NO_UPDATE_CHECK")
				_ = os.Unsetenv("NO_UPDATE_CHECK")
				t.Cleanup(func() {
					if existed {
						_ = os.Setenv("NO_UPDATE_CHECK", old)
					} else {
						_ = os.Unsetenv("NO_UPDATE_CHECK")
					}
				})
			}
			source := &fakeSource{latest: "v9.0.0"}
			cachePath := filepath.Join(t.TempDir(), "update.json")
			checker := NewChecker(source, cachePath)
			if result, ok := checker.Check(context.Background(), tt.version, tt.configEnabled); ok || result != (Result{}) {
				t.Fatalf("opt-out result = (%#v, %t)", result, ok)
			}
			if source.callCount() != 0 {
				t.Fatalf("opt-out made %d source calls", source.callCount())
			}
			if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("opt-out cache stat error = %v, want not exist", err)
			}
		})
	}
}

func TestCheckerFailureIsSilentAndCached(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "update.json")
	source := &fakeSource{err: errors.New("offline")}
	checker := NewChecker(source, path)
	checker.Now = func() time.Time { return now }

	if result, ok := checker.Check(context.Background(), "1.0.0", true); ok || result != (Result{}) {
		t.Fatalf("failed lookup result = (%#v, %t)", result, ok)
	}
	entry, fresh := readFreshCache(path, now)
	if !fresh || entry.Latest != "" {
		t.Fatalf("failure cache = (%#v, %t)", entry, fresh)
	}
}

func TestCheckerHTTPFailuresStaySilent(t *testing.T) {
	tests := []struct {
		name      string
		transport roundTripFunc
	}{
		{name: "network error", transport: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		}},
		{name: "non-200", transport: func(*http.Request) (*http.Response, error) {
			return response(http.StatusTooManyRequests, `{"message":"rate limited"}`), nil
		}},
		{name: "malformed JSON", transport: func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, `{"tag_name":`), nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := GitHubReleaseSource{
				Client:   &http.Client{Transport: tt.transport},
				Endpoint: "https://offline.invalid/latest",
			}
			checker := NewChecker(source, filepath.Join(t.TempDir(), "update.json"))
			if result, ok := checker.Check(context.Background(), "1.0.0", true); ok || result != (Result{}) {
				t.Fatalf("failure result = (%#v, %t)", result, ok)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestGitHubReleaseSourceOfflineResponses(t *testing.T) {
	tests := []struct {
		name      string
		transport roundTripFunc
		want      string
		wantErr   bool
	}{
		{
			name: "success",
			transport: func(req *http.Request) (*http.Response, error) {
				if got := req.Header.Get("User-Agent"); got != "termp/1.2.3" {
					t.Fatalf("User-Agent = %q", got)
				}
				if req.Header.Get("Authorization") != "" {
					t.Fatal("anonymous request unexpectedly has Authorization")
				}
				return response(http.StatusOK, `{"tag_name":"v1.3.0"}`), nil
			},
			want: "v1.3.0",
		},
		{name: "network error", transport: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		}, wantErr: true},
		{name: "non-200", transport: func(*http.Request) (*http.Response, error) {
			return response(http.StatusInternalServerError, `{"message":"nope"}`), nil
		}, wantErr: true},
		{name: "malformed JSON", transport: func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, `{"tag_name":`), nil
		}, wantErr: true},
		{name: "missing tag", transport: func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, `{}`), nil
		}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := GitHubReleaseSource{
				Client:   &http.Client{Transport: tt.transport},
				Endpoint: "https://offline.invalid/latest",
			}
			got, err := source.Latest(context.Background(), "1.2.3")
			if (err != nil) != tt.wantErr {
				t.Fatalf("Latest() error = %v, wantErr %t", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("Latest() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultCachePathUsesXDGCacheHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)
	want := filepath.Join(root, "termp", "update-check.json")
	if got := DefaultCachePath(); got != want {
		t.Fatalf("DefaultCachePath() = %q, want %q", got, want)
	}
}

func TestDefaultCachePathUsesXDGDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", home)
	var want string
	if runtime.GOOS == "windows" {
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			t.Fatal(err)
		}
		want = filepath.Join(cacheDir, "termp", "update-check.json")
	} else {
		want = filepath.Join(home, ".cache", "termp", "update-check.json")
	}
	if got := DefaultCachePath(); got != want {
		t.Fatalf("DefaultCachePath() = %q, want %q", got, want)
	}
}
