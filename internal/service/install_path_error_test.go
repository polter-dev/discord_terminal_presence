package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The tests in this file pin the user-facing half of issue #472: what
// `autostart install` tells someone whose executable path cannot be resolved,
// and what `--force` is allowed to skip. They assert only on text termp itself
// adds. The operating system's own wording is deliberately never asserted on:
// it is locale-dependent, and on Windows it carries no path at all, which is
// the reason the wrapper exists.

// TestValidateInstallExecutableNamesTheMissingDirectory claims: when a
// directory component of the path is absent, the refusal names which directory
// is missing, not merely that something along the path is. The missing
// directory is the actionable part - it is what the user has to go look at.
func TestValidateInstallExecutableNamesTheMissingDirectory(t *testing.T) {
	root := t.TempDir()
	missingDir := filepath.Join(root, "no-such-dir")
	exe := filepath.Join(missingDir, "nested", "termp")

	invocationPath, err := filepath.Abs(exe)
	if err != nil {
		t.Fatal(err)
	}

	_, err = ValidateInstallExecutable(exe, false)
	if err == nil {
		t.Fatal("ValidateInstallExecutable() error = nil, want a refusal for a missing directory component")
	}
	got := err.Error()

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
	if strings.Contains(got, "that file does not exist") {
		t.Errorf("a missing directory component was reported as a missing file: %v", err)
	}
}

// TestValidateInstallExecutableNamesTheMissingFile claims the other half of the
// same distinction: when every directory on the path exists and only the file
// is absent, the message says so and does not blame a directory. Pointing a
// user at a directory that is right there would send them looking in the wrong
// place.
func TestValidateInstallExecutableNamesTheMissingFile(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "termp")

	invocationPath, err := filepath.Abs(exe)
	if err != nil {
		t.Fatal(err)
	}

	_, err = ValidateInstallExecutable(exe, false)
	if err == nil {
		t.Fatal("ValidateInstallExecutable() error = nil, want a refusal for a missing file")
	}
	got := err.Error()

	for _, want := range []string{
		"cannot install autostart from",
		fmt.Sprintf("%q", invocationPath),
		"that file does not exist",
		"where termp",
		"--force",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ValidateInstallExecutable() error missing %q: %v", want, err)
		}
	}
	if strings.Contains(got, "the directory") {
		t.Errorf("a missing file was reported as a missing directory: %v", err)
	}
}

// TestValidateInstallExecutableForceSkipsPathResolutionEntirely states the
// settled intent of --force, so a later change cannot quietly narrow it: force
// returns the absolute invocation path without resolving it, even when the path
// does not exist at all. Path resolution here exists only to feed the advisory
// unstable-path heuristic, so skipping it under an explicit --force gives up no
// safety property - it is exactly the "register this path unchecked" the
// refusal message offers. Registering a path that resolves to nothing is the
// user's stated choice; the install still fails loudly at the service manager
// if the OS itself objects.
func TestValidateInstallExecutableForceSkipsPathResolutionEntirely(t *testing.T) {
	root := t.TempDir()
	exe := filepath.Join(root, "no-such-dir", "termp")

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
	if _, statErr := os.Stat(got); statErr == nil {
		t.Fatalf("fixture %q unexpectedly exists; the test is no longer proving the bypass", got)
	}
}
