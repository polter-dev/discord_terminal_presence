package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveKnownFilesFromDirectoryPreservesUnknownFiles reproduces issue
// #562: full uninstall's directory targets are termp's own namespace
// directories, but the old os.RemoveAll deleted everything inside them,
// including a file the user placed there themselves (a hand-kept note, a
// backup config copy, and so on). Only files removeKnownFilesFromDirectory
// recognizes as termp-owned should go; anything else, and therefore the
// directory itself, must survive.
func TestRemoveKnownFilesFromDirectoryPreservesUnknownFiles(t *testing.T) {
	dir := t.TempDir()
	known := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(known, []byte("termp config"), 0o600); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(dir, "not-created-by-termp.txt")
	if err := os.WriteFile(unknown, []byte("owner data"), 0o600); err != nil {
		t.Fatal(err)
	}

	target := uninstallRemovalTarget{label: "config", path: dir, directory: true}
	if kept, err := removeKnownFilesFromDirectory(target, os.Remove, os.RemoveAll); err != nil || !kept {
		t.Fatalf("removeKnownFilesFromDirectory() = %t, %v; want kept, nil", kept, err)
	}

	if _, err := os.Stat(known); !os.IsNotExist(err) {
		t.Fatalf("known termp file still exists: %v", err)
	}
	data, err := os.ReadFile(unknown)
	if err != nil || string(data) != "owner data" {
		t.Fatalf("unknown file after removal = %q, %v; want preserved owner data", data, err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("directory holding an unknown file was removed: %v", err)
	}
}

// TestRemoveKnownFilesFromDirectoryRemovesEmptiedDirectory covers the
// complementary case: once every entry in a termp-owned directory is
// recognized and removed, the directory itself is still fully removed, so
// full uninstall's "everything termp created is gone" promise holds.
func TestRemoveKnownFilesFromDirectoryRemovesEmptiedDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "usage.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}

	target := uninstallRemovalTarget{label: "state", path: dir, directory: true}
	if kept, err := removeKnownFilesFromDirectory(target, os.Remove, os.RemoveAll); err != nil || kept {
		t.Fatalf("removeKnownFilesFromDirectory() = %t, %v; want removed, nil", kept, err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("emptied termp directory still exists: %v", err)
	}
}

// TestRemoveKnownFilesFromDirectoryRecognizesTempWritePrefix covers the
// atomic-write temp files config/usage/episode/update leave behind under a
// crash before their final rename (`<name>.tmp-<random>`): these are
// termp's own transient files, not user data, and must be recognized too.
func TestRemoveKnownFilesFromDirectoryRecognizesTempWritePrefix(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.toml.tmp-12345"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	target := uninstallRemovalTarget{label: "config", path: dir, directory: true}
	if _, err := removeKnownFilesFromDirectory(target, os.Remove, os.RemoveAll); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("directory holding only a recognized temp-write file still exists: %v", err)
	}
}

// TestRemoveKnownFilesFromDirectoryKeepsUnknownFileInRunDirectory covers the
// PID directory recursion added for #636: a user's file inside run/ keeps
// both run/ and its parent, while termp's PID file still goes.
func TestRemoveKnownFilesFromDirectoryKeepsUnknownFileInRunDirectory(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "run")
	if err := os.Mkdir(run, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"termp.pid": "{}", "notes.txt": "owner data"} {
		if err := os.WriteFile(filepath.Join(run, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	target := uninstallRemovalTarget{label: "runtime directory", path: dir, directory: true}
	if kept, err := removeKnownFilesFromDirectory(target, os.Remove, os.RemoveAll); err != nil || !kept {
		t.Fatalf("removeKnownFilesFromDirectory() = %t, %v; want kept, nil", kept, err)
	}
	if _, err := os.Stat(filepath.Join(run, "termp.pid")); !os.IsNotExist(err) {
		t.Fatalf("PID file still exists: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(run, "notes.txt")); err != nil || string(data) != "owner data" {
		t.Fatalf("unknown file in run/ = %q, %v; want preserved owner data", data, err)
	}
}

// TestIsKnownUninstallFile pins the exact-match and temp-write-prefix rules
// isKnownUninstallFile applies.
func TestIsKnownUninstallFile(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "config.toml", want: true},
		{name: "usage.json", want: true},
		{name: "presence.json", want: true},
		{name: "update-check.json", want: true},
		{name: "discord.json", want: true},
		{name: "update-check.json.lock", want: true},
		{name: "termp.log", want: true},
		{name: "termp.log.lock", want: true},
		{name: "termp.log.1", want: true},
		{name: "config.toml.tmp-98765", want: true},
		{name: "config.tomlbackup", want: false},
		{name: "not-created-by-termp.txt", want: false},
		{name: "termp.log.4", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isKnownUninstallFile(tt.name); got != tt.want {
				t.Fatalf("isKnownUninstallFile(%q) = %t, want %t", tt.name, got, tt.want)
			}
		})
	}
}
