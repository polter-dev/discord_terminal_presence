//go:build linux

package update

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

type packageRunner struct {
	artifact      []byte
	checksum      string
	commands      []Command
	temporary     []string
	privateCreate bool
}

type panicPackageRunner struct{}

func (panicPackageRunner) Run(_ context.Context, _ Command, _ io.Reader, _, _ io.Writer) error {
	panic("simulated runner panic")
}

func (r *packageRunner) Run(_ context.Context, command Command, _ io.Reader, _, _ io.Writer) error {
	r.commands = append(r.commands, command)
	if command.Name != "curl" {
		return nil
	}
	output := command.Args[len(command.Args)-1]
	r.temporary = append(r.temporary, output)
	info, err := os.Stat(output)
	if err != nil {
		return err
	}
	r.privateCreate = r.privateCreate || info.Mode().Perm() == 0o600
	if strings.HasSuffix(command.Args[1], "/checksums.txt") {
		return os.WriteFile(output, []byte(r.checksum), 0o600)
	}
	return os.WriteFile(output, r.artifact, 0o600)
}

func TestPerformSystemPackageUpdateVerifiesAndInstalls(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	artifact := []byte("signed package contents")
	digest := sha256.Sum256(artifact)
	assetName := fmt.Sprintf("termp_2.3.4_linux_%s", runtime.GOARCH)

	for _, tt := range []struct {
		name       string
		method     InstallMethod
		extension  string
		rpmManager string
		wantArgs   []string
	}{
		{name: "debian", method: InstallDebian, extension: ".deb", wantArgs: []string{"apt", "install", "-y"}},
		{name: "rpm dnf", method: InstallRPM, extension: ".rpm", rpmManager: "dnf", wantArgs: []string{"dnf", "install", "-y"}},
		{name: "rpm zypper", method: InstallRPM, extension: ".rpm", rpmManager: "zypper", wantArgs: []string{"zypper", "--non-interactive", "--no-gpg-checks", "install"}},
		{name: "rpm yum", method: InstallRPM, extension: ".rpm", rpmManager: "yum", wantArgs: []string{"yum", "install", "-y"}},
		{name: "rpm plain", method: InstallRPM, extension: ".rpm", rpmManager: "rpm", wantArgs: []string{"rpm", "-U"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pinRPMManager(t, tt.rpmManager)
			runner := &packageRunner{
				artifact: artifact,
				checksum: fmt.Sprintf("%x  %s%s\n", digest, assetName, tt.extension),
			}
			if err := PerformUpdate(context.Background(), tt.method, "v2.3.4", runner, nil, io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			if len(runner.commands) != 3 {
				t.Fatalf("commands = %#v, want two downloads and one install", runner.commands)
			}
			install := runner.commands[2]
			want := Command{Name: "sudo", Args: append(append([]string{}, tt.wantArgs...), runner.temporary[0])}
			if !reflect.DeepEqual(install, want) {
				t.Fatalf("install command = %#v, want %#v", install, want)
			}
			if !runner.privateCreate {
				t.Fatal("temporary files were not created with mode 0600")
			}
			for _, command := range runner.commands {
				if strings.Contains(command.Name+"\x00"+strings.Join(command.Args, "\x00"), "/usr/local/bin") {
					t.Fatalf("package update referenced /usr/local/bin: %#v", command)
				}
			}
			for _, path := range runner.temporary {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("temporary file %q was not removed: %v", path, err)
				}
			}
		})
	}
}

func TestPerformSystemPackageUpdateFailsClosed(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	artifact := []byte("package contents")
	assetName := fmt.Sprintf("termp_2.3.4_linux_%s.deb", runtime.GOARCH)
	goodDigest := sha256.Sum256(artifact)

	for _, tt := range []struct {
		name     string
		artifact []byte
		checksum string
		want     string
	}{
		{
			name:     "mismatch",
			artifact: artifact,
			checksum: fmt.Sprintf("%064x  %s\n", 1, assetName),
			want:     "checksum mismatch",
		},
		{
			name:     "missing checksum",
			artifact: artifact,
			checksum: fmt.Sprintf("%x  another-file.deb\n", goodDigest),
			want:     "expected exactly one checksum",
		},
		{
			name:     "empty download",
			artifact: nil,
			checksum: fmt.Sprintf("%x  %s\n", goodDigest, assetName),
			want:     "is empty",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &packageRunner{artifact: tt.artifact, checksum: tt.checksum}
			err := PerformUpdate(context.Background(), InstallDebian, "v2.3.4", runner, nil, io.Discard, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("PerformUpdate() error = %v, want %q", err, tt.want)
			}
			for _, command := range runner.commands {
				if command.Name == "sudo" {
					t.Fatalf("unverified package reached install command: %#v", command)
				}
			}
			for _, path := range runner.temporary {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("temporary file %q was not removed: %v", path, err)
				}
			}
		})
	}
}

func TestPerformSystemPackageUpdateRejectsUnsafeTagAndAmbiguousMethod(t *testing.T) {
	for _, tt := range []struct {
		method InstallMethod
		tag    string
	}{
		{method: InstallDebian, tag: "v2.3.4;id"},
		{method: InstallSystemPackage, tag: "v2.3.4"},
	} {
		runner := &packageRunner{}
		if err := PerformUpdate(context.Background(), tt.method, tt.tag, runner, nil, io.Discard, io.Discard); err == nil {
			t.Fatalf("PerformUpdate(%q, %q) error = nil", tt.method, tt.tag)
		}
		if len(runner.commands) != 0 {
			t.Fatalf("PerformUpdate(%q, %q) ran commands: %#v", tt.method, tt.tag, runner.commands)
		}
	}
}

func TestPackageTemporaryFilesUseConfiguredTempDirectory(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	artifact := []byte("package")
	digest := sha256.Sum256(artifact)
	runner := &packageRunner{
		artifact: artifact,
		checksum: fmt.Sprintf("%x  termp_2.3.4_linux_%s.deb\n", digest, runtime.GOARCH),
	}
	if err := PerformUpdate(context.Background(), InstallDebian, "v2.3.4", runner, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, path := range runner.temporary {
		if filepath.Dir(path) != tempDir {
			t.Fatalf("temporary path = %q, want directory %q", path, tempDir)
		}
	}
}

func TestPerformSystemPackageUpdateCleansUpAfterPanic(t *testing.T) {
	tempDir := t.TempDir()
	t.Setenv("TMPDIR", tempDir)
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("PerformUpdate() did not propagate runner panic")
			}
		}()
		_ = PerformUpdate(context.Background(), InstallDebian, "v2.3.4", panicPackageRunner{}, nil, io.Discard, io.Discard)
	}()
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("temporary files survived panic: %v", entries)
	}
}

func TestPerformSystemPackageUpdateWithoutRPMManagerInstallsNothing(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	pinRPMManager(t, "")
	runner := &packageRunner{}
	err := PerformUpdate(context.Background(), InstallRPM, "v2.3.4", runner, nil, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "no RPM package manager available") {
		t.Fatalf("PerformUpdate(InstallRPM) error = %v, want missing-manager failure", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("PerformUpdate(InstallRPM) ran commands without a package manager: %#v", runner.commands)
	}
	guidance := GuidanceForMethod(InstallRPM, "v2.3.4").Text
	if guidance == "" || strings.Contains(guidance, "sudo dnf install") {
		t.Fatalf("guidance without a detected manager = %q, want a manager-agnostic fallback", guidance)
	}
}
