package service

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// This file is Windows-only by filename, so the `Test (windows-latest)` CI job
// is what actually executes it. Issue #472 was reported from real Windows
// hardware, where `autostart install` printed exactly
// `termp: The system cannot find the path specified.` - one sentence, no
// operation, no path. The reason that sentence is so bare is Windows-specific
// (a raw syscall.Errno with nothing attached), so the guarantees below can only
// be proven here.
//
// None of these tests assert on the operating system's own error text. That
// text is locale-dependent, and the reporter's machine happened to be
// English-only; asserting on it would pin an accident. They assert on termp's
// own added context and on numeric errno classification.

// TestWindowsPathNotFoundErrnoIsClassifiedAsNotExist pins the single fact the
// whole fix rests on: errno 3 (ERROR_PATH_NOT_FOUND), the errno the reporter
// actually hit because a DIRECTORY component was missing, is classified by the
// standard library as fs.ErrNotExist. ValidateInstallExecutable branches on
// exactly that classification, so if this ever stopped holding, a missing path
// would silently fall through to the "unresolvable, keep going" branch and the
// install would register a path pointing at nothing.
func TestWindowsPathNotFoundErrnoIsClassifiedAsNotExist(t *testing.T) {
	for name, errno := range map[string]syscall.Errno{
		"ERROR_FILE_NOT_FOUND": syscall.ERROR_FILE_NOT_FOUND,
		"ERROR_PATH_NOT_FOUND": syscall.ERROR_PATH_NOT_FOUND,
	} {
		if !errors.Is(errno, fs.ErrNotExist) {
			t.Errorf("errors.Is(%s (%d), fs.ErrNotExist) = false; the missing-path branch of ValidateInstallExecutable would be skipped", name, uintptr(errno))
		}
	}
}

// TestWindowsMissingDirectoryComponentProducesActionableError is the direct
// regression test for the reported failure. On Windows, filepath.EvalSymlinks
// normalises components through syscall.FindFirstFile, which returns a bare
// errno; the user saw its rendering and nothing else. The claim here is that
// whatever the OS says, termp's message names the operation, the full path it
// was asked to install from, and the specific directory that is missing.
func TestWindowsMissingDirectoryComponentProducesActionableError(t *testing.T) {
	root := t.TempDir()
	missingDir := filepath.Join(root, "no-such-dir")
	exe := filepath.Join(missingDir, "nested", "termp.exe")

	invocationPath, err := filepath.Abs(exe)
	if err != nil {
		t.Fatal(err)
	}

	// Establish that this fixture really reproduces the reported condition:
	// resolution fails, and it fails as a not-exist error rather than anything
	// else. Without this the test could pass for the wrong reason.
	resolveErr := func() error {
		_, err := filepath.EvalSymlinks(invocationPath)
		return err
	}()
	if resolveErr == nil {
		t.Fatalf("EvalSymlinks(%q) succeeded; the fixture does not reproduce the reported condition", invocationPath)
	}
	if !errors.Is(resolveErr, fs.ErrNotExist) {
		t.Fatalf("EvalSymlinks(%q) error = %v, want an fs.ErrNotExist classification", invocationPath, resolveErr)
	}
	var errno syscall.Errno
	if errors.As(resolveErr, &errno) {
		t.Logf("Windows errno for a missing directory component: %d", uintptr(errno))
	}

	_, err = ValidateInstallExecutable(exe, false)
	if err == nil {
		t.Fatal("ValidateInstallExecutable() error = nil, want a refusal for a missing directory component")
	}
	got := err.Error()

	// %q escapes the backslashes in a Windows path, so assert on the quoted
	// rendering the message actually produces.
	for _, want := range []string{
		"cannot install autostart from",
		fmt.Sprintf("%q", invocationPath),
		fmt.Sprintf("the directory %q does not exist", missingDir),
		"where termp",
		"--force",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ValidateInstallExecutable() error missing %q: %v", want, err)
		}
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ValidateInstallExecutable() error = %v, want the underlying cause still unwrappable to fs.ErrNotExist", err)
	}
}

// TestWindowsForceBypassesMissingPathResolution states on Windows the intent
// the refusal message advertises: --force registers the path unchecked. The
// reported bug had the resolution running before the force check, so the
// advice "retry with --force" was false on exactly the platform where the
// error was least readable.
func TestWindowsForceBypassesMissingPathResolution(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "no-such-dir", "termp.exe")

	want, err := filepath.Abs(exe)
	if err != nil {
		t.Fatal(err)
	}

	got, err := ValidateInstallExecutable(exe, true)
	if err != nil {
		t.Fatalf("ValidateInstallExecutable(force) error = %v, want --force to bypass path resolution", err)
	}
	if got != want {
		t.Fatalf("ValidateInstallExecutable(force) = %q, want the absolute invocation path %q", got, want)
	}
}
