package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polter-dev/discord_terminal_presence/internal/completioninstall"
	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	updatepkg "github.com/polter-dev/discord_terminal_presence/internal/update"
	usagepkg "github.com/polter-dev/discord_terminal_presence/internal/usage"
)

func stubbedUninstallDeps(out *bytes.Buffer, targets func() ([]uninstallRemovalTarget, error)) uninstallAllDeps {
	return uninstallAllDeps{
		stopDaemon:       func() (int, bool, error) { return 0, false, nil },
		removeAutostart:  func(bool, bool) error { return nil },
		removeCompletion: func(completioninstall.HomeDirFunc) ([]string, error) { return nil, nil },
		homeDir:          os.UserHomeDir,
		targets:          targets,
		detectInstall:    func() updatepkg.InstallMethod { return updatepkg.InstallHomebrew },
		goos:             "darwin",
		stdout:           out,
		removeFile:       os.Remove,
		removeAll:        os.RemoveAll,
	}
}

// TestUninstallAllRemovesDaemonStateAndRuntimeDirectory reproduces issue
// #636: every file a stock account's daemon writes, at its production default
// path, must be gone after `uninstall --all`, including the daemon's
// discord.json and the <cache>/termp/run directory that held the PID file.
func TestUninstallAllRemovesDaemonStateAndRuntimeDirectory(t *testing.T) {
	root := t.TempDir()
	// A stock account: only HOME (and the Windows-native dirs) are set.
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("USERPROFILE", filepath.Join(root, "home"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "local"))
	t.Setenv("APPDATA", filepath.Join(root, "roaming"))
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_RUNTIME_DIR", "")

	if err := config.Save(config.Default(), config.DefaultPath()); err != nil {
		t.Fatal(err)
	}
	if err := usagepkg.Save(usagepkg.StatePath(), usagepkg.New()); err != nil {
		t.Fatal(err)
	}
	if err := detector.SaveEpisodeStore(detector.EpisodeStatePath(), detector.NewEpisodeStore()); err != nil {
		t.Fatal(err)
	}
	// What runDaemonDiscordStatePublisher writes on every daemon start.
	if err := writeDaemonDiscordState(daemonDiscordStatePath(), daemonDiscordState{PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	pidPath := pidFilePath()
	if err := ensurePIDDirectory(filepath.Dir(pidPath)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pidPath, []byte(`{"pid":1}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := uninstallAllWithDeps(false, true, stubbedUninstallDeps(&out, defaultUninstallTargets)); err != nil {
		t.Fatalf("uninstall --all error = %v", err)
	}

	var left []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if rel, _ := filepath.Rel(root, path); err == nil && strings.Contains(rel, "termp") {
			left = append(left, rel)
		}
		return nil
	})
	if len(left) > 0 {
		t.Fatalf("termp paths left after uninstall --all: %v\noutput:\n%s", left, out.String())
	}
}

// TestUninstallAllReportsKeptDirectory covers the reporting half of #636: a
// termp directory kept because it holds a file termp did not create must not
// be reported as removed.
func TestUninstallAllReportsKeptDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "termp")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("owner data"), 0o600); err != nil {
		t.Fatal(err)
	}
	targets := func() ([]uninstallRemovalTarget, error) {
		return []uninstallRemovalTarget{{label: "state", path: dir, directory: true}}, nil
	}

	var out bytes.Buffer
	if err := uninstallAllWithDeps(false, true, stubbedUninstallDeps(&out, targets)); err != nil {
		t.Fatalf("uninstall --all error = %v", err)
	}
	if strings.Contains(out.String(), "Removed state:") || !strings.Contains(out.String(), "Kept state: "+dir) {
		t.Fatalf("output must report the kept state directory, not a removal:\n%s", out.String())
	}
}
