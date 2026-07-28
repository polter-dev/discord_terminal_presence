package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

func withConfigHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	home = canonicalTestPath(t, home)
	configHome := filepath.Join(canonicalTestPath(t, root), "xdg")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("APPDATA", configHome)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return filepath.Join(configHome, appConfigDir, defaultConfigFile)
}

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(canonical)
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		t.Fatal(err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		t.Fatal(err)
	}
	if _, err := tmp.WriteString(content); err != nil {
		_ = tmp.Close()
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		t.Fatal(err)
	}
}

// nonAtomicWriter simulates an editor doing a non-atomic save: it truncates
// the destination in place, signals truncated once that has happened, then
// (after an optional delay) writes the final content into the same file
// descriptor and closes it. This reproduces the transient-empty-file window
// that a truncate-then-write save produces, which os.Rename-based atomic
// writes (writeConfig above) never do.
//
// write runs the save on its own goroutine (so the test goroutine can
// synchronize on truncated while the save is still in flight) and reports
// any I/O error over done rather than calling t.Fatal itself: a t.Fatal from
// a non-test goroutine only Goexits that goroutine, it does not reliably
// fail the test, which would let a real write failure hide behind an
// otherwise-passing assertion. Callers must call wait(t) to join and surface
// that error on the test goroutine.
type nonAtomicWriter struct {
	truncated chan struct{}
	done      chan error
}

func newNonAtomicWriter(t *testing.T) *nonAtomicWriter {
	t.Helper()
	return &nonAtomicWriter{truncated: make(chan struct{}), done: make(chan error, 1)}
}

func (w *nonAtomicWriter) write(path, content string, delayBeforeContent time.Duration) {
	go func() {
		w.done <- w.doWrite(path, content, delayBeforeContent)
	}()
}

func (w *nonAtomicWriter) writeChunked(path, prefix, suffix string, delayBeforeSuffix time.Duration) {
	go func() {
		w.done <- w.doWriteChunked(path, prefix, suffix, delayBeforeSuffix)
	}()
}

func (w *nonAtomicWriter) doWrite(path, content string, delayBeforeContent time.Duration) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	close(w.truncated)
	if delayBeforeContent > 0 {
		time.Sleep(delayBeforeContent)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (w *nonAtomicWriter) doWriteChunked(path, prefix, suffix string, delayBeforeSuffix time.Duration) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(prefix)); err != nil {
		_ = f.Close()
		return err
	}
	close(w.truncated)
	if delayBeforeSuffix > 0 {
		time.Sleep(delayBeforeSuffix)
	}
	if _, err := f.Write([]byte(suffix)); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// wait joins the write goroutine and fails the test (on the test goroutine)
// if the simulated save itself errored.
func (w *nonAtomicWriter) wait(t *testing.T) {
	t.Helper()
	if err := <-w.done; err != nil {
		t.Fatalf("non-atomic writer failed: %v", err)
	}
}

type scheduledConfigWrite struct {
	ready <-chan struct{}
	done  <-chan error
}

func startScheduledConfigWrite(write func(ready chan<- struct{}) error) scheduledConfigWrite {
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- write(ready)
	}()
	return scheduledConfigWrite{ready: ready, done: done}
}

func waitScheduledConfigWrite(t *testing.T, write scheduledConfigWrite) {
	t.Helper()
	if err := <-write.done; err != nil {
		t.Fatalf("scheduled config write failed: %v", err)
	}
}

func waitScheduledConfigReady(t *testing.T, write scheduledConfigWrite) {
	t.Helper()
	select {
	case <-write.ready:
	case err := <-write.done:
		t.Fatalf("scheduled config write failed before ready: %v", err)
	}
}

func assertManagerEnabledFalse(t *testing.T, manager *Manager, stage string) {
	t.Helper()
	cfg, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() %s = error %v", stage, err)
	}
	if cfg.Enabled {
		t.Fatalf("enabled observed true %s", stage)
	}
}

func writeChunks(f *os.File, data []byte, sizes []int, pauses []time.Duration) error {
	offset := 0
	for i, size := range sizes {
		if offset >= len(data) {
			break
		}
		end := offset + size
		if end > len(data) {
			end = len(data)
		}
		if _, err := f.Write(data[offset:end]); err != nil {
			return err
		}
		offset = end
		if offset < len(data) && i < len(pauses) {
			time.Sleep(pauses[i])
		}
	}
	if offset < len(data) {
		_, err := f.Write(data[offset:])
		return err
	}
	return nil
}

func boolPtr(v bool) *bool {
	return &v
}

func TestDefaultPathForOSBranches(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	configHome := filepath.Join(root, "xdg-config")
	appData := filepath.Join(root, "AppData", "Roaming")
	resolver := func(goos string) pathResolver {
		return pathResolver{
			goos: goos,
			getenv: func(key string) string {
				if key == "XDG_CONFIG_HOME" {
					return configHome
				}
				return ""
			},
			userHomeDir:   func() (string, error) { return home, nil },
			userConfigDir: func() (string, error) { return appData, nil },
			stat:          os.Stat,
			copyFile:      copyFileBestEffort,
		}
	}

	tests := []struct {
		name string
		goos string
		want string
	}{
		{
			name: "windows uses app data",
			goos: "windows",
			want: filepath.Join(appData, appConfigDir, defaultConfigFile),
		},
		{
			name: "linux honors xdg",
			goos: "linux",
			want: filepath.Join(configHome, appConfigDir, defaultConfigFile),
		},
		{
			name: "darwin honors xdg",
			goos: "darwin",
			want: filepath.Join(configHome, appConfigDir, defaultConfigFile),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := defaultPathFor(resolver(tt.goos)); got != tt.want {
				t.Fatalf("defaultPathFor(%q) = %q, want %q", tt.goos, got, tt.want)
			}
		})
	}
}

func TestDefaultPathForUnixHomeFallbackUnchanged(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	resolver := pathResolver{
		goos:          "linux",
		getenv:        func(string) string { return "" },
		userHomeDir:   func() (string, error) { return home, nil },
		userConfigDir: func() (string, error) { return t.TempDir(), nil },
		stat:          os.Stat,
		copyFile:      copyFileBestEffort,
	}

	got := defaultPathFor(resolver)
	want := filepath.Join(home, defaultConfigDir, appConfigDir, defaultConfigFile)
	if got != want {
		t.Fatalf("defaultPathFor(linux) = %q, want %q", got, want)
	}
}

func TestDefaultPathWindowsMigration(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	appData := filepath.Join(root, "AppData", "Roaming")
	native := filepath.Join(appData, appConfigDir, defaultConfigFile)
	legacy := filepath.Join(home, defaultConfigDir, appConfigDir, defaultConfigFile)
	base := pathResolver{
		goos:          "windows",
		getenv:        func(string) string { return "" },
		userHomeDir:   func() (string, error) { return home, nil },
		userConfigDir: func() (string, error) { return appData, nil },
		stat:          os.Stat,
		copyFile:      copyFileBestEffort,
	}

	t.Run("new path present ignores legacy", func(t *testing.T) {
		writeConfig(t, native, "enabled = true\n")
		writeConfig(t, legacy, "enabled = false\n")

		path := defaultPathFor(base)
		cfg, err := LoadPath(path)
		if err != nil {
			t.Fatal(err)
		}
		if path != native || !cfg.Enabled {
			t.Fatalf("path = %q enabled = %t, want native enabled", path, cfg.Enabled)
		}
	})

	t.Run("legacy only is read and copied", func(t *testing.T) {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		appData := filepath.Join(root, "AppData", "Roaming")
		native := filepath.Join(appData, appConfigDir, defaultConfigFile)
		legacy := filepath.Join(home, defaultConfigDir, appConfigDir, defaultConfigFile)
		writeConfig(t, legacy, "enabled = false\n")
		resolver := base
		resolver.userHomeDir = func() (string, error) { return home, nil }
		resolver.userConfigDir = func() (string, error) { return appData, nil }

		path := defaultPathFor(resolver)
		cfg, err := LoadPath(path)
		if err != nil {
			t.Fatal(err)
		}
		if path != native || cfg.Enabled {
			t.Fatalf("path = %q enabled = %t, want migrated native disabled config", path, cfg.Enabled)
		}
		if _, err := os.Stat(native); err != nil {
			t.Fatalf("migrated config missing: %v", err)
		}
		data, err := os.ReadFile(native)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "enabled = false\n" {
			t.Fatalf("migrated config = %q, want full legacy contents", data)
		}
	})

	t.Run("neither present uses native default", func(t *testing.T) {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		appData := filepath.Join(root, "AppData", "Roaming")
		native := filepath.Join(appData, appConfigDir, defaultConfigFile)
		resolver := base
		resolver.userHomeDir = func() (string, error) { return home, nil }
		resolver.userConfigDir = func() (string, error) { return appData, nil }

		path := defaultPathFor(resolver)
		cfg, err := LoadPath(path)
		if err != nil {
			t.Fatal(err)
		}
		if path != native || !cfg.Enabled {
			t.Fatalf("path = %q enabled = %t, want native default config", path, cfg.Enabled)
		}
	})

	t.Run("copy failure still reads legacy", func(t *testing.T) {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		appData := filepath.Join(root, "AppData", "Roaming")
		native := filepath.Join(appData, appConfigDir, defaultConfigFile)
		legacy := filepath.Join(home, defaultConfigDir, appConfigDir, defaultConfigFile)
		writeConfig(t, legacy, "enabled = false\n")
		resolver := base
		resolver.userHomeDir = func() (string, error) { return home, nil }
		resolver.userConfigDir = func() (string, error) { return appData, nil }
		resolver.copyFile = func(string, string) error { return fmt.Errorf("copy failed") }

		path := defaultPathFor(resolver)
		cfg, err := LoadPath(path)
		if err != nil {
			t.Fatal(err)
		}
		if path != legacy || cfg.Enabled {
			t.Fatalf("path = %q enabled = %t, want legacy disabled config", path, cfg.Enabled)
		}
		if _, err := os.Stat(native); !os.IsNotExist(err) {
			t.Fatalf("native config err = %v, want not exist", err)
		}
	})
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	path := withConfigHome(t)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Path != path {
		t.Fatalf("path = %q, want %q", cfg.Path, path)
	}
	if !cfg.Enabled || !cfg.StartAtLogin || !cfg.UpdateCheck || cfg.AutoUpdate || cfg.ScanInterval != "3s" {
		t.Fatalf("unexpected global defaults: %#v", cfg)
	}
	if cfg.IdleClearTimeout != "20m" || cfg.DetailsFormat != "Using {tool}" {
		t.Fatalf("unexpected polish defaults: %#v", cfg)
	}
	if !reflect.DeepEqual(cfg.FallbackMessages, []string{"Working on something", "In the terminal"}) {
		t.Fatalf("fallback_messages default = %#v", cfg.FallbackMessages)
	}
	if cfg.FeedbackURL != DefaultFeedbackURL {
		t.Fatalf("feedback_url default = %q, want %q", cfg.FeedbackURL, DefaultFeedbackURL)
	}
	if cfg.Pin != "" || cfg.HeadlinerIdleTimeout != "60s" || !cfg.ActivitySwitching {
		t.Fatalf("unexpected headliner defaults: %#v", cfg)
	}
	if !cfg.Display.ToolName || !cfg.Display.ElapsedTimer || !cfg.Display.SmallImage || !cfg.Display.Collection || !cfg.Display.Buttons {
		t.Fatalf("display defaults not enabled: %#v", cfg.Display)
	}
	if cfg.Privacy.ShowDirectory {
		t.Fatal("show_directory default should be false")
	}
	if !cfg.Privacy.DirectoryBasenameOnly {
		t.Fatal("directory_basename_only default should be true")
	}
	if !cfg.CTA.Enabled || cfg.CTA.Label != "What is this?" || cfg.CTA.URL != "https://termp.polter.sh/" {
		t.Fatalf("unexpected CTA defaults: %#v", cfg.CTA)
	}
}

// TestLoadThenSaveRejectsChangingNonAtomicTruncationWindow is the #438
// regression. Setup and settings both load the user's whole config and later
// save that whole document. A transient empty read must not seed that write
// with defaults and durably erase the user's opt-out and unrelated settings.
func TestLoadThenSaveRejectsChangingNonAtomicTruncationWindow(t *testing.T) {
	const contents = `enabled = false
pin = "codex-cli"

[[custom_tools]]
id = "mine"
display_name = "Mine"
match = { name = "mine" }
image_key = "mine"
`
	for _, stall := range []time.Duration{
		50 * time.Millisecond,
		250 * time.Millisecond,
		400 * time.Millisecond,
		time.Second,
	} {
		t.Run(stall.String(), func(t *testing.T) {
			path := withConfigHome(t)
			writeConfig(t, path, contents)

			writer := newNonAtomicWriter(t)
			writer.write(path, contents, stall)
			<-writer.truncated

			cfg, err := LoadPath(path)
			if err != nil {
				t.Fatalf("LoadPath() during %v non-atomic save = %v", stall, err)
			}
			if err := Save(cfg, path); err != nil {
				t.Fatalf("Save() after protected load = %v", err)
			}
			writer.wait(t)

			onDisk, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read config after whole-document save: %v", err)
			}
			if !bytes.Contains(onDisk, []byte("enabled = false")) {
				t.Fatalf("%v stall: on-disk config lost enabled=false:\n%s", stall, onDisk)
			}
			if !bytes.Contains(onDisk, []byte(`pin = "codex-cli"`)) {
				t.Fatalf("%v stall: on-disk config lost pin:\n%s", stall, onDisk)
			}

			persisted, err := LoadPath(path)
			if err != nil {
				t.Fatalf("LoadPath() after whole-document save = %v", err)
			}
			if persisted.Enabled {
				t.Fatalf("%v stall: whole-document save durably changed enabled=false to true", stall)
			}
			if persisted.Pin != "codex-cli" {
				t.Fatalf("%v stall: whole-document save changed pin to %q, want codex-cli", stall, persisted.Pin)
			}
			if len(persisted.CustomTools) != 1 || persisted.CustomTools[0].ID != "mine" {
				t.Fatalf("%v stall: whole-document save changed custom tools to %#v, want mine", stall, persisted.CustomTools)
			}
		})
	}
}

func TestLoadPathBlankAndNonBlankLatencyPolicy(t *testing.T) {
	t.Run("deliberate blank waits through loosening horizon", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		writeConfig(t, path, " \n\t")

		start := time.Now()
		cfg, err := LoadPath(path)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("LoadPath() deliberate blank = %v", err)
		}
		if !cfg.Enabled || cfg.Pin != "" {
			t.Fatalf("LoadPath() deliberate blank = %#v, want defaults", cfg)
		}
		if elapsed < enabledLooseningHorizon {
			t.Fatalf("deliberate blank returned after %v, want at least %v", elapsed, enabledLooseningHorizon)
		}
		t.Logf("deliberate blank update load latency: %v", elapsed)
	})

	t.Run("normal config keeps settle latency", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		writeConfig(t, path, `pin = "vim"`)

		start := time.Now()
		cfg, err := LoadPath(path)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("LoadPath() normal config = %v", err)
		}
		if !cfg.Enabled || cfg.Pin != "vim" {
			t.Fatalf("LoadPath() normal config = %#v, want enabled default and pin", cfg)
		}
		if elapsed >= enabledLooseningHorizon {
			t.Fatalf("normal config returned after %v, want less than horizon %v", elapsed, enabledLooseningHorizon)
		}
		t.Logf("normal nonblank update load latency: %v", elapsed)
	})

	t.Run("read-only blank does not pay update horizon", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.toml")
		writeConfig(t, path, "")

		start := time.Now()
		cfg, err := LoadPathReadOnly(path)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("LoadPathReadOnly() blank = %v", err)
		}
		if !cfg.Enabled {
			t.Fatalf("LoadPathReadOnly() blank = %#v, want defaults", cfg)
		}
		if elapsed >= enabledLooseningHorizon {
			t.Fatalf("read-only blank returned after %v, want less than update horizon %v", elapsed, enabledLooseningHorizon)
		}
		t.Logf("read-only blank load latency: %v", elapsed)
	})
}

func TestLoadPathNeverSettlingWriterIsBounded(t *testing.T) {
	tests := []struct {
		name string
		load func(string, func(string) fileSnapshot, func() time.Time, func(time.Duration)) (Config, error)
	}{
		{
			name: "read-only returns latest snapshot",
			load: loadPathReadOnlyWith,
		},
		{
			name: "whole-document returns busy error",
			load: loadPathWith,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(0, 0)
			sleep := func(delay time.Duration) {
				now = now.Add(delay)
			}
			revision := 0
			snapshot := func(string) fileSnapshot {
				revision++
				if revision > 100 {
					t.Fatalf("snapshot reader called %d times; settle loop exceeded its bound", revision)
				}
				return fileSnapshot{
					exists: true,
					data:   []byte(fmt.Sprintf("pin = %q\n", fmt.Sprintf("revision-%d", revision))),
				}
			}

			start := now
			cfg, err := tt.load("config.toml", snapshot, func() time.Time { return now }, sleep)
			elapsed := now.Sub(start)
			if elapsed > standaloneLoadSettleTimeout {
				t.Fatalf("%s returned after %v virtual time, want no more than %v",
					tt.name, elapsed, standaloneLoadSettleTimeout)
			}
			if tt.name == "read-only returns latest snapshot" {
				if err != nil {
					t.Fatalf("LoadPathReadOnly() error = %v, want latest readable snapshot", err)
				}
				if cfg.Pin == "" {
					t.Fatalf("LoadPathReadOnly() pin = %q, want latest readable snapshot", cfg.Pin)
				}
				return
			}
			if !errors.Is(err, ErrConfigBeingWritten) {
				t.Fatalf("LoadPath() error = %v, want ErrConfigBeingWritten", err)
			}
		})
	}
}

func TestManagerStartupConfigPolicy(t *testing.T) {
	t.Run("absent file uses enabled defaults", func(t *testing.T) {
		path := withConfigHome(t)
		manager := NewManagerPath(path)

		cfg, err := manager.Current()
		if err != nil {
			t.Fatalf("Current() error = %v, want nil", err)
		}
		if !cfg.Enabled {
			t.Fatal("missing config disabled presence, want enabled first-run default")
		}
	})

	t.Run("deliberately blank file resolves to enabled defaults", func(t *testing.T) {
		path := withConfigHome(t)
		writeConfig(t, path, "")
		manager := NewManagerPath(path)

		assertManagerEnabledFalse(t, manager, "while constructor blank is pending")
		select {
		case reload := <-manager.Reloads():
			if reload.Err != nil || !reload.Config.Enabled || reload.Config.ScanInterval != Default().ScanInterval {
				t.Fatalf("deliberate constructor blank reload = %#v, want enabled defaults", reload)
			}
		case <-time.After(enabledLooseningHorizon + time.Second):
			t.Fatal("timed out waiting for deliberate constructor blank to load defaults")
		}
	})

	tests := []struct {
		name    string
		content string
	}{
		{
			name:    "syntax error",
			content: "enabled = false\nthis line is a syntax error\n",
		},
		{
			name:    "validation error",
			content: "enabled = true\nscan_interval = \"not-a-duration\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := withConfigHome(t)
			writeConfig(t, path, tt.content)
			manager := NewManagerPath(path)

			cfg, err := manager.Current()
			if err == nil {
				t.Fatal("Current() error = nil, want invalid-config error")
			}
			if cfg.Enabled {
				t.Fatal("invalid existing config left presence enabled")
			}
			if cfg.Path != path {
				t.Fatalf("fallback path = %q, want %q", cfg.Path, path)
			}

			writeConfig(t, path, "enabled = true\n")
			if err := manager.Reload(); err != nil {
				t.Fatalf("Reload() after repair = %v", err)
			}
			cfg, err = manager.Current()
			if err != nil {
				t.Fatalf("Current() after repair = %v", err)
			}
			if !cfg.Enabled {
				t.Fatal("valid reload did not restore enabled presence")
			}
		})
	}
}

// TestManagerStartupTruncatedPastSettleBudgetFailClosesAndKeepsGuardArmed is
// the #440 regression. A stable-for-the-budget empty file is ambiguous at
// construction time, so it must not seed enabled=true and bypass the later
// false-to-true guard.
func TestManagerStartupTruncatedPastSettleBudgetFailClosesAndKeepsGuardArmed(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, "enabled = false\nscan_interval = \"9s\"\n")

	writer := newNonAtomicWriter(t)
	writer.write(path, "scan_interval = \"5s\"\n", reloadSettleInterval*time.Duration(reloadSettleAttempts+5))
	<-writer.truncated

	manager := NewManagerPath(path)
	assertManagerEnabledFalse(t, manager, "after construction from ambiguous blank")
	writer.wait(t)

	start := time.Now()
	if err := manager.Reload(); err != nil {
		t.Fatalf("Reload() of defaulted final content = %v", err)
	}
	assertManagerEnabledFalse(t, manager, "while constructor-seeded guard is pending")
	select {
	case reload := <-manager.Reloads():
		t.Fatalf("guarded defaulted read published before the horizon: %#v", reload)
	default:
	}

	select {
	case reload := <-manager.Reloads():
		if reload.Err != nil || !reload.Config.Enabled || reload.Config.ScanInterval != "5s" {
			t.Fatalf("post-horizon reload = %#v, want enabled defaults with scan interval 5s", reload)
		}
		if elapsed := time.Since(start); elapsed < enabledLooseningHorizon {
			t.Fatalf("defaulted read applied after %v, want at least the %v loosening horizon", elapsed, enabledLooseningHorizon)
		}
	case <-time.After(enabledLooseningHorizon + time.Second):
		t.Fatal("timed out waiting for guarded defaulted read to apply")
	}
}

func TestInitFileWritesAnnotatedLoadableConfig(t *testing.T) {
	path := withConfigHome(t)
	if err := InitFile(path, false); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"enabled = true",
		"start_at_login = true",
		"update_check = true",
		"auto_update = false",
		"scan_interval = \"3s\"",
		"idle_clear_timeout = \"20m\"",
		"Clear presence after this much terminal inactivity",
		"headliner_idle_timeout = \"60s\"",
		"activity_switching = true",
		"details_format = \"Using {tool}\"",
		"fallback_messages = [\"Working on something\", \"In the terminal\"]",
		"[ui]",
		"accent_color = \"purple\"",
		"[display]",
		"[privacy]",
		"[cta]",
		"# [[custom_tools]]",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("annotated config missing %q:\n%s", want, content)
		}
	}
	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ScanInterval != Default().ScanInterval || cfg.CTA.Label != Default().CTA.Label {
		t.Fatalf("loaded config = %#v, want defaults", cfg)
	}
}

func TestFallbackMessagesLoadAndRoundTrip(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []string
	}{
		{name: "absent uses default", content: `enabled = true`, want: []string{"Working on something", "In the terminal"}},
		{name: "empty uses default", content: `fallback_messages = []`, want: []string{"Working on something", "In the terminal"}},
		{name: "custom preserved", content: `fallback_messages = ["Shipping code", "Reviewing changes"]`, want: []string{"Shipping code", "Reviewing changes"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			writeConfig(t, path, tt.content)
			cfg, err := LoadPath(path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cfg.FallbackMessages, tt.want) {
				t.Fatalf("loaded fallback_messages = %#v, want %#v", cfg.FallbackMessages, tt.want)
			}

			savedPath := filepath.Join(t.TempDir(), "saved.toml")
			if err := Save(cfg, savedPath); err != nil {
				t.Fatal(err)
			}
			roundTripped, err := LoadPath(savedPath)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(roundTripped.FallbackMessages, tt.want) {
				t.Fatalf("round-tripped fallback_messages = %#v, want %#v", roundTripped.FallbackMessages, tt.want)
			}
		})
	}
}

func TestInitFileRefusesExistingWithoutForce(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `enabled = false`)
	if err := InitFile(path, false); err == nil {
		t.Fatal("InitFile without force should refuse existing config")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `enabled = false` {
		t.Fatalf("existing config was overwritten: %q", data)
	}
}

func TestInitFileRefusesSymlink(t *testing.T) {
	path := withConfigHome(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.toml")
	const original = "target contents\n"
	if err := os.WriteFile(target, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlinks unavailable on this account: %v", err)
	}

	err := InitFile(path, true)
	if err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("InitFile() error = %v, want non-regular file error", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Fatalf("symlink target was overwritten: %q", data)
	}
}

func TestInitFileRefusesNonRegularFile(t *testing.T) {
	path := withConfigHome(t)
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}

	err := InitFile(path, true)
	if err == nil || !strings.Contains(err.Error(), "non-regular") {
		t.Fatalf("InitFile() error = %v, want non-regular file error", err)
	}
}

func TestInitFileForceOverwrites(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `enabled = false`)
	if err := InitFile(path, true); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled {
		t.Fatal("force init should overwrite with default enabled=true")
	}
}

func TestLoadValidConfig(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `
enabled = true
start_at_login = false
update_check = false
auto_update = true
scan_interval = "5s"
idle_clear_timeout = "8h"
pin = "codex-cli"
headliner_idle_timeout = "45s"
activity_switching = false
details_format = "Working in {tool}"
feedback_url = "https://example.test/feedback"

[display]
tool_name = false
elapsed_timer = true
small_image = false
collection = false
buttons = true

[privacy]
show_directory = true
directory_allowlist = ["~/dev"]
directory_basename_only = false

[cta]
enabled = false
label = "Preview termp"
url = "https://example.test/dead-cta"

[tools.claude-code]
enabled = true
tool_name = true
show_directory = false
buttons = [{ label = "Claude", url = "https://example.test/claude" }]

[[custom_tools]]
id = "mine"
display_name = "Mine"
match = { name = "mine" }
image_url = "https://example.test/mine.png"
priority = 5
`)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StartAtLogin || cfg.UpdateCheck || !cfg.AutoUpdate || cfg.ScanInterval != "5s" || cfg.Display.ToolName {
		t.Fatalf("unexpected loaded values: %#v", cfg)
	}
	if cfg.IdleClearTimeout != "8h" || cfg.DetailsFormat != "Working in {tool}" {
		t.Fatalf("unexpected polish values: %#v", cfg)
	}
	if cfg.FeedbackURL != "https://example.test/feedback" {
		t.Fatalf("feedback_url = %q", cfg.FeedbackURL)
	}
	if cfg.Pin != "codex-cli" || cfg.HeadlinerIdleTimeout != "45s" || cfg.ActivitySwitching {
		t.Fatalf("unexpected headliner values: %#v", cfg)
	}
	if cfg.Display.Collection {
		t.Fatalf("collection should be false: %#v", cfg.Display)
	}
	if cfg.CTA.Enabled || cfg.CTA.Label != "Preview termp" || cfg.CTA.URL != "https://example.test/dead-cta" {
		t.Fatalf("CTA not loaded: %#v", cfg.CTA)
	}
	if got := cfg.Privacy.DirectoryAllowlist[0]; got != filepath.Join(canonicalTestPath(t, os.Getenv("HOME")), "dev") {
		t.Fatalf("allowlist = %q", got)
	}
	override := cfg.Tools["claude-code"]
	if override.ToolName == nil || !*override.ToolName || override.ShowDirectory == nil || *override.ShowDirectory {
		t.Fatalf("unexpected override: %#v", override)
	}
	if len(override.Buttons) != 1 || override.Buttons[0].Label != "Claude" {
		t.Fatalf("buttons not loaded: %#v", override.Buttons)
	}
	if len(cfg.CustomTools) != 1 || cfg.CustomTools[0].ID != "mine" || cfg.CustomTools[0].Match.Name != "mine" {
		t.Fatalf("custom tool not loaded: %#v", cfg.CustomTools)
	}
}

func TestCustomToolIconSlugLoadsAndResolves(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `
[[custom_tools]]
id = "slug-tool"
display_name = "Slug Tool"
match = { name = "slug-tool" }
icon_slug = "lazygit"
icon_source = "simpleicons"
priority = 11

[[custom_tools]]
id = "url-wins"
display_name = "URL Wins"
match = { name = "url-wins" }
image_url = "https://example.test/url-wins.png"
icon_slug = "ignored-slug"
icon_source = "simpleicons"
`)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.CustomTools) != 2 {
		t.Fatalf("custom tools = %#v", cfg.CustomTools)
	}
	slugTool := cfg.CustomTools[0]
	if slugTool.IconSlug != "lazygit" || slugTool.IconSource != "simpleicons" {
		t.Fatalf("slug fields not loaded: %#v", slugTool)
	}

	reg, err := registry.NewWithCustom(cfg.CustomTools...)
	if err != nil {
		t.Fatal(err)
	}
	resolvedSlug, ok := reg.Match("slug-tool")
	if !ok {
		t.Fatal("slug-tool did not match")
	}
	if !strings.Contains(resolvedSlug.ImageURL, "cdn.simpleicons.org/lazygit") {
		t.Fatalf("resolved image URL = %q, want Simple Icons CDN slug URL", resolvedSlug.ImageURL)
	}

	resolvedURL, ok := reg.Match("url-wins")
	if !ok {
		t.Fatal("url-wins did not match")
	}
	if resolvedURL.ImageURL != "https://example.test/url-wins.png" {
		t.Fatalf("image_url precedence failed: %q", resolvedURL.ImageURL)
	}
}

func TestDurationFallbacks(t *testing.T) {
	cfg := Default()
	cfg.ScanInterval = "bad"
	cfg.HeadlinerIdleTimeout = "also-bad"

	if got := cfg.ScanIntervalDuration(); got != 3*time.Second {
		t.Fatalf("scan interval duration = %v, want 3s", got)
	}
	if got := cfg.IdleClearTimeoutDuration(); got != 20*time.Minute {
		t.Fatalf("idle clear timeout = %v, want 20m", got)
	}
	if got := cfg.HeadlinerIdleTimeoutDuration(); got != time.Minute {
		t.Fatalf("headliner idle timeout = %v, want 1m", got)
	}

	cfg.IdleClearTimeout = "10m"
	if got := cfg.IdleClearTimeoutDuration(); got != 10*time.Minute {
		t.Fatalf("idle clear timeout = %v, want 10m", got)
	}

	cfg.IdleClearTimeout = "0"
	if got := cfg.IdleClearTimeoutDuration(); got != 0 {
		t.Fatalf("idle clear timeout = %v, want disabled", got)
	}
}

func TestLoadRejectsMalformedDurations(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "scan interval garbage", key: "scan_interval", value: "garbage"},
		{name: "scan interval zero", key: "scan_interval", value: "0"},
		{name: "idle clear garbage", key: "idle_clear_timeout", value: "garbage"},
		{name: "idle clear negative", key: "idle_clear_timeout", value: "-1s"},
		{name: "headliner timeout garbage", key: "headliner_idle_timeout", value: "garbage"},
		{name: "headliner timeout zero", key: "headliner_idle_timeout", value: "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := withConfigHome(t)
			writeConfig(t, path, fmt.Sprintf("%s = %q", tt.key, tt.value))

			_, err := Load()
			if err == nil {
				t.Fatalf("Load() accepted %s = %q", tt.key, tt.value)
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("Load() error = %v, want field name %q", err, tt.key)
			}
		})
	}
}

func TestManagerKeepsLastGoodOnInvalidDurationReload(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `
scan_interval = "7s"
idle_clear_timeout = "10m"
headliner_idle_timeout = "45s"
`)
	manager := NewManagerPath(path)
	if _, err := manager.Current(); err != nil {
		t.Fatal(err)
	}

	writeConfig(t, path, `
scan_interval = "7s"
idle_clear_timeout = "not-a-duration"
headliner_idle_timeout = "45s"
`)
	if err := manager.Reload(); err == nil {
		t.Fatal("expected invalid duration reload error")
	}
	cfg, err := manager.Current()
	if err == nil {
		t.Fatal("expected LastError after invalid duration reload")
	}
	if cfg.ScanInterval != "7s" || cfg.IdleClearTimeout != "10m" || cfg.HeadlinerIdleTimeout != "45s" {
		t.Fatalf("last-good durations not preserved: scan=%q idle=%q headliner=%q",
			cfg.ScanInterval, cfg.IdleClearTimeout, cfg.HeadlinerIdleTimeout)
	}
}

func TestResolutionOrder(t *testing.T) {
	tool := registry.Tool{
		ID:      "claude-code",
		Buttons: []registry.Button{{Label: "Default", URL: "https://example.test/default"}},
	}
	cfg := Default()
	cfg.Display.ToolName = false
	cfg.Privacy.ShowDirectory = false
	cfg.Tools["claude-code"] = ToolOverride{
		ToolName:      boolPtr(true),
		SmallImage:    boolPtr(false),
		ShowDirectory: boolPtr(true),
		Buttons:       []registry.Button{{Label: "Override", URL: "https://example.test/override"}},
		buttonsSet:    true,
	}

	resolved := cfg.Resolve(tool)
	if !resolved.Enabled || !resolved.ToolName {
		t.Fatalf("per-tool tool_name should win: %#v", resolved)
	}
	if !resolved.ElapsedTimer {
		t.Fatal("unset per-tool elapsed_timer should fall through to default true")
	}
	if resolved.SmallImage {
		t.Fatal("per-tool small_image=false should win")
	}
	if !resolved.ShowDirectory {
		t.Fatal("per-tool show_directory=true should win")
	}
	if len(resolved.Buttons) != 1 || resolved.Buttons[0].Label != "Override" {
		t.Fatalf("per-tool buttons should override registry defaults: %#v", resolved.Buttons)
	}

	cfg.Tools["claude-code"] = ToolOverride{Enabled: boolPtr(false)}
	if cfg.Resolve(tool).Enabled {
		t.Fatal("tool enabled=false should disable display")
	}

	cfg = Default()
	cfg.Enabled = false
	if cfg.Resolve(tool).Enabled {
		t.Fatal("global enabled=false should short-circuit")
	}
}

func TestPrivacyDirectoryRules(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `
[privacy]
show_directory = true
directory_allowlist = ["~/work"]
directory_basename_only = true
`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	resolved := cfg.Resolve(registry.Tool{ID: "codex-cli"})

	home := canonicalTestPath(t, os.Getenv("HOME"))
	inside := filepath.Join(home, "work", "client")
	if !resolved.DirectoryAllowed(inside) {
		t.Fatalf("expected %q to be allowed", inside)
	}
	outside := filepath.Join(home, "other")
	if resolved.DirectoryAllowed(outside) {
		t.Fatalf("expected %q to be denied", outside)
	}

	defaultResolved := Default().Resolve(registry.Tool{ID: "codex-cli"})
	if defaultResolved.DirectoryAllowed(inside) {
		t.Fatal("default show_directory=false should deny directory display")
	}
}

func TestResolvedToolDoesNotExposeDirectoryFormatting(t *testing.T) {
	if _, ok := reflect.TypeOf(ResolvedTool{}).MethodByName("DisplayDirectory"); ok {
		t.Fatal("ResolvedTool.DisplayDirectory must not expose an independent directory privacy boundary")
	}
}

func TestUnknownKeyWarns(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `
enabled = true
future_key = true
`)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "future_key") {
		t.Fatalf("warnings = %#v", cfg.Warnings)
	}
}

func TestLoadValidAccentColor(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "short hex", in: "#0af", want: "#0af"},
		{name: "long hex", in: "#abcdef", want: "#abcdef"},
		{name: "mixed-case long hex", in: "#12AbEF", want: "#12AbEF"},
		{name: "empty uses default palette", in: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := withConfigHome(t)
			writeConfig(t, path, fmt.Sprintf(`
[ui]
accent_color = %q
`, tt.in))

			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.UI.AccentColor != tt.want {
				t.Fatalf("ui.accent_color = %q, want %q", cfg.UI.AccentColor, tt.want)
			}
			if len(cfg.Warnings) != 0 {
				t.Fatalf("warnings = %#v, want none", cfg.Warnings)
			}
		})
	}
}

func TestLoadUnsetAccentColorUsesDefaultPalette(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `enabled = true`)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.UI.AccentColor != DefaultAccentColor {
		t.Fatalf("ui.accent_color = %q, want default %q", cfg.UI.AccentColor, DefaultAccentColor)
	}
	if len(cfg.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", cfg.Warnings)
	}
}

func TestInvalidAccentColorWarnsAndUsesDefault(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{name: "unsupported name", value: "cyan"},
		{name: "too short hex", value: "#12"},
		{name: "non-hex digits", value: "#gggggg"},
		{name: "four-digit hex", value: "#1234"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := withConfigHome(t)
			writeConfig(t, path, fmt.Sprintf(`
scan_interval = "7s"

[ui]
accent_color = %q
`, tt.value))

			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.UI.AccentColor != DefaultAccentColor {
				t.Fatalf("ui.accent_color = %q, want default %q", cfg.UI.AccentColor, DefaultAccentColor)
			}
			if cfg.ScanInterval != "7s" {
				t.Fatalf("valid config was not retained: scan_interval = %q", cfg.ScanInterval)
			}
			if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "ui.accent_color") || !strings.Contains(cfg.Warnings[0], tt.value) {
				t.Fatalf("warnings = %#v, want invalid accent warning for %q", cfg.Warnings, tt.value)
			}
		})
	}
}

func TestManagerKeepsLastGoodOnMalformedReload(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `scan_interval = "7s"`)
	manager := NewManagerPath(path)
	cfg, err := manager.Current()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ScanInterval != "7s" {
		t.Fatalf("scan interval = %q", cfg.ScanInterval)
	}

	writeConfig(t, path, `scan_interval = "broken" =`)
	if err := manager.Reload(); err == nil {
		t.Fatal("expected malformed reload error")
	}
	cfg, err = manager.Current()
	if err == nil {
		t.Fatal("expected LastError after malformed reload")
	}
	if cfg.ScanInterval != "7s" {
		t.Fatalf("last-good scan interval = %q, want 7s", cfg.ScanInterval)
	}
}

func TestManagerChangesDeliversSingleReload(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `scan_interval = "7s"`)
	manager := NewManagerPath(path)

	writeConfig(t, path, `scan_interval = "8s"`)
	if err := manager.Reload(); err != nil {
		t.Fatal(err)
	}

	select {
	case reload := <-manager.Reloads():
		if reload.Err != nil || reload.Config.ScanInterval != "8s" {
			t.Fatalf("notified reload = %#v, want scan interval 8s", reload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for config reload notification")
	}
}

func TestManagerChangesCoalescesBurstyReloadsToNewest(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `scan_interval = "7s"`)
	manager := NewManagerPath(path)

	writeConfig(t, path, `scan_interval = "8s"`)
	if err := manager.Reload(); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, path, `scan_interval = "9s"`)
	if err := manager.Reload(); err != nil {
		t.Fatal(err)
	}

	select {
	case reload := <-manager.Reloads():
		if reload.Err != nil || reload.Config.ScanInterval != "9s" {
			t.Fatalf("notified reload = %#v, want newest value 9s", reload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for coalesced config reload notification")
	}

	current, err := manager.Current()
	if err != nil {
		t.Fatal(err)
	}
	if current.ScanInterval != "9s" {
		t.Fatalf("current scan interval = %q, want 9s", current.ScanInterval)
	}

	writeConfig(t, path, `scan_interval = "10s"`)
	if err := manager.Reload(); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, path, `scan_interval = "broken" =`)
	if err := manager.Reload(); err == nil {
		t.Fatal("expected malformed reload error")
	}
	select {
	case reload := <-manager.Reloads():
		if reload.Err == nil {
			t.Fatalf("coalesced reload = %#v, want newest failure", reload)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for coalesced failure")
	}
}

func TestManagerWatchReportsMalformedReload(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `scan_interval = "7s"`)
	manager := NewManagerPath(path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Watch(ctx); err != nil {
		t.Fatal(err)
	}

	writeConfig(t, path, `scan_interval = "broken" =`)
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()

waitForMalformedReload:
	for {
		select {
		case reload := <-manager.Reloads():
			if reload.Err == nil {
				continue
			}
			if !strings.Contains(reload.Err.Error(), "expected") {
				t.Fatalf("reload failure = %v, want TOML parse error", reload.Err)
			}
			break waitForMalformedReload
		case <-timeout.C:
			t.Fatal("timed out waiting for malformed reload failure")
		}
	}
	current, err := manager.Current()
	if err == nil {
		t.Fatal("Current() error = nil after malformed watched reload")
	}
	if current.ScanInterval != "7s" {
		t.Fatalf("last-good scan interval = %q, want 7s", current.ScanInterval)
	}
}

func TestManagerWatcherErrorDoesNotInvalidateConfig(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `scan_interval = "7s"`)
	manager := NewManagerPath(path)

	manager.reportWatchFailure(errors.New("watch backend failed"))

	select {
	case watchErr := <-manager.WatchErrors():
		if watchErr == nil || !strings.Contains(watchErr.Error(), "watch backend failed") {
			t.Fatalf("watcher error = %v, want backend failure", watchErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for watcher error")
	}
	current, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() error = %v, want valid config after watcher error", err)
	}
	if current.ScanInterval != "7s" {
		t.Fatalf("current scan interval = %q, want 7s", current.ScanInterval)
	}
	select {
	case reload := <-manager.Reloads():
		t.Fatalf("watcher error leaked into reload stream: %#v", reload)
	default:
	}
	t.Logf("watcher error left Current valid with scan_interval=%s", current.ScanInterval)
}

func TestManagerConcurrentCurrentDuringReloadKeepsLastGood(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `scan_interval = "7s"`)
	manager := NewManagerPath(path)

	var wg sync.WaitGroup
	start := make(chan struct{})
	stop := make(chan struct{})
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
					cfg, _ := manager.Current()
					if cfg.ScanInterval != "7s" && cfg.ScanInterval != "9s" {
						errs <- fmt.Errorf("Current() scan_interval = %q, want last-good value", cfg.ScanInterval)
						return
					}
				}
			}
		}()
	}
	close(start)

	writeConfig(t, path, `scan_interval = "broken" =`)
	if err := manager.Reload(); err == nil {
		t.Fatal("expected malformed reload error")
	}
	cfg, err := manager.Current()
	if err == nil {
		t.Fatal("expected LastError after malformed reload")
	}
	if cfg.ScanInterval != "7s" {
		t.Fatalf("last-good scan interval after malformed reload = %q, want 7s", cfg.ScanInterval)
	}

	writeConfig(t, path, `scan_interval = "9s"`)
	if err := manager.Reload(); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	cfg, err = manager.Current()
	if err != nil {
		t.Fatalf("Current() err after valid reload = %v", err)
	}
	if cfg.ScanInterval != "9s" {
		t.Fatalf("scan interval after valid reload = %q, want 9s", cfg.ScanInterval)
	}
}

func TestCustomToolMissingRequiredFieldRejected(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `
[[custom_tools]]
id = "missing-image"
display_name = "Missing Image"
match = { name = "missing-image" }
`)
	_, err := Load()
	if err == nil {
		t.Fatal("expected missing image validation error")
	}
	if !strings.Contains(err.Error(), "image_key, image_url, or icon_slug") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFeedbackURLValidationRejectedAtLoad(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "local file",
			value: "file:///Applications/Malicious.app",
			want:  "feedback_url must be a valid absolute http/https URL",
		},
		{
			name:  "over length",
			value: "https://example.test/" + strings.Repeat("x", registry.MaxButtonURLLength),
			want:  "feedback_url must be at most 512 characters",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := withConfigHome(t)
			writeConfig(t, path, fmt.Sprintf("feedback_url = %q\n", tt.value))
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadPathRejectsOversizedConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxConfigFileSize + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	_, err = LoadPath(path)
	if err == nil || !strings.Contains(err.Error(), "config file exceeds maximum size") {
		t.Fatalf("LoadPath() error = %v, want maximum-size error", err)
	}
}

func TestToolButtonValidationRejectedAtLoad(t *testing.T) {
	tests := []struct {
		name    string
		buttons string
		want    string
	}{
		{
			name:    "over-length label",
			buttons: `[{ label = "` + strings.Repeat("X", 33) + `", url = "https://example.test" }]`,
			want:    "tools.claude-code: buttons[0].label must be at most 32 characters",
		},
		{
			name: "more than two buttons",
			buttons: `[
				{ label = "One", url = "https://example.test/one" },
				{ label = "Two", url = "https://example.test/two" },
				{ label = "Three", url = "https://example.test/three" },
			]`,
			want: "tools.claude-code: buttons must contain at most 2 entries",
		},
		{
			name:    "malformed URL",
			buttons: `[{ label = "Broken", url = "not-a-valid-url-at-all" }]`,
			want:    "tools.claude-code: buttons[0].url must be a valid absolute http/https URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := withConfigHome(t)
			writeConfig(t, path, "[tools.claude-code]\nbuttons = "+tt.buttons+"\n")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCustomToolDiscordFieldValidationRejectedAtLoad(t *testing.T) {
	tests := []struct {
		name  string
		field string
		want  string
	}{
		{
			name:  "id length",
			field: `id = "` + strings.Repeat("i", registry.MaxToolIDLength+1) + `"`,
			want:  "custom_tools[0]: id must be at most 64 characters",
		},
		{
			name:  "display name length",
			field: `display_name = "` + strings.Repeat("n", registry.MaxDisplayNameLength+1) + `"`,
			want:  "custom_tools[0]: display_name must be at most 128 characters",
		},
		{
			name:  "image URL shape",
			field: `image_url = "not-an-absolute-url"`,
			want:  "custom_tools[0]: image_url must be a valid absolute http/https URL",
		},
		{
			name:  "image key length",
			field: `image_key = "` + strings.Repeat("k", registry.MaxImageValueLength+1) + `"`,
			want:  "custom_tools[0]: image_key must be at most 256 characters",
		},
		{
			name:  "resolved icon URL shape",
			field: "icon_slug = \"file:///tmp/icon.png\"\nicon_source = \"url\"",
			want:  "custom_tools[0]: resolved image_url must be a valid absolute http/https URL",
		},
		{
			name:  "button label length",
			field: `buttons = [{ label = "` + strings.Repeat("X", 33) + `", url = "https://example.test" }]`,
			want:  "custom_tools[0]: buttons[0].label must be at most 32 characters",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := `id = "custom"`
			displayName := `display_name = "Custom"`
			image := `image_url = "https://example.test/custom.png"`
			buttons := ""
			switch tt.name {
			case "id length":
				id = tt.field
			case "display name length":
				displayName = tt.field
			case "image URL shape":
				image = tt.field
			case "image key length", "resolved icon URL shape":
				image = tt.field
			case "button label length":
				buttons = "\n" + tt.field
			}
			path := withConfigHome(t)
			writeConfig(t, path, "[[custom_tools]]\n"+id+"\n"+displayName+
				"\nmatch = { name = \"custom\" }\n"+image+buttons+"\n")
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCustomToolTooShortDisplayNameRejectedAtLoad(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `[[custom_tools]]
id = "mine"
display_name = "x"
image_key = "mine"
[custom_tools.match]
name = "mine"
`)

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "custom_tools[0]: display_name must be at least 2 characters") {
		t.Fatalf("Load() error = %v, want display_name minimum-length error", err)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := withConfigHome(t)
	cfg := Default()
	cfg.Enabled = false
	cfg.StartAtLogin = false
	cfg.UpdateCheck = false
	cfg.AutoUpdate = true
	cfg.ScanInterval = "9s"
	cfg.IdleClearTimeout = "6h"
	cfg.Pin = "codex-cli"
	cfg.HeadlinerIdleTimeout = "2m"
	cfg.ActivitySwitching = false
	cfg.DetailsFormat = "{tool} in {dir}"
	cfg.FallbackMessages = []string{"Building quietly", "Pairing"}
	cfg.FeedbackURL = "https://example.test/feedback"
	cfg.Display.ToolName = false
	cfg.Display.Collection = false
	cfg.CTA.Enabled = false
	cfg.CTA.Label = "Preview termp"
	cfg.CTA.URL = "https://example.test/dead-cta"
	cfg.Privacy.ShowDirectory = true
	cfg.Privacy.DirectoryAllowlist = []string{"~/dev"}
	cfg.Privacy.DirectoryBasenameOnly = false
	cfg.Tools["claude-code"] = ToolOverride{
		Enabled:               boolPtr(true),
		ToolName:              boolPtr(false),
		ShowDirectory:         boolPtr(true),
		DirectoryBasenameOnly: boolPtr(true),
		Buttons:               []registry.Button{{Label: "Claude", URL: "https://example.test/claude"}},
		buttonsSet:            true,
	}
	cfg.CustomTools = []registry.CustomTool{{
		ID:          "mine",
		DisplayName: "Mine",
		Match:       registry.CustomMatch{Name: "mine"},
		IconSlug:    "lazygit",
		IconSource:  "simpleicons",
		Priority:    7,
		Buttons:     []registry.Button{{Label: "Mine", URL: "https://example.test/mine"}},
	}}

	if err := Save(cfg, path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Enabled || loaded.StartAtLogin || loaded.UpdateCheck || !loaded.AutoUpdate || loaded.ScanInterval != "9s" || loaded.Pin != "codex-cli" {
		t.Fatalf("globals did not round-trip: %#v", loaded)
	}
	if loaded.IdleClearTimeout != "6h" || loaded.DetailsFormat != "{tool} in {dir}" {
		t.Fatalf("polish settings did not round-trip: %#v", loaded)
	}
	if !reflect.DeepEqual(loaded.FallbackMessages, cfg.FallbackMessages) {
		t.Fatalf("fallback_messages = %#v, want %#v", loaded.FallbackMessages, cfg.FallbackMessages)
	}
	if loaded.FeedbackURL != "https://example.test/feedback" {
		t.Fatalf("feedback_url did not round-trip: %#v", loaded)
	}
	if loaded.ActivitySwitching || loaded.HeadlinerIdleTimeout != "2m" {
		t.Fatalf("headliner did not round-trip: %#v", loaded)
	}
	if loaded.Display.ToolName || loaded.Display.Collection {
		t.Fatalf("display did not round-trip: %#v", loaded.Display)
	}
	if loaded.CTA.Enabled || loaded.CTA.Label != "Preview termp" || loaded.CTA.URL != "https://example.test/dead-cta" {
		t.Fatalf("CTA did not round-trip: %#v", loaded.CTA)
	}
	if !loaded.Privacy.ShowDirectory || loaded.Privacy.DirectoryBasenameOnly {
		t.Fatalf("privacy did not round-trip: %#v", loaded.Privacy)
	}
	wantAllow := filepath.Join(canonicalTestPath(t, os.Getenv("HOME")), "dev")
	if len(loaded.Privacy.DirectoryAllowlist) != 1 || loaded.Privacy.DirectoryAllowlist[0] != wantAllow {
		t.Fatalf("allowlist = %#v, want %q", loaded.Privacy.DirectoryAllowlist, wantAllow)
	}
	override := loaded.Tools["claude-code"]
	if override.ToolName == nil || *override.ToolName || override.ShowDirectory == nil || !*override.ShowDirectory {
		t.Fatalf("override did not round-trip: %#v", override)
	}
	if len(override.Buttons) != 1 || override.Buttons[0].Label != "Claude" {
		t.Fatalf("override buttons = %#v", override.Buttons)
	}
	if len(loaded.CustomTools) != 1 || loaded.CustomTools[0].ID != "mine" || loaded.CustomTools[0].Priority != 7 {
		t.Fatalf("custom tools did not round-trip: %#v", loaded.CustomTools)
	}
	if loaded.CustomTools[0].IconSlug != "lazygit" || loaded.CustomTools[0].IconSource != "simpleicons" {
		t.Fatalf("custom tool slug fields did not round-trip: %#v", loaded.CustomTools[0])
	}
}

func TestCustomToolExcludeLoadSaveLoadRoundTripAndMatch(t *testing.T) {
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.toml")
	savedPath := filepath.Join(dir, "saved.toml")
	input := `
[[custom_tools]]
id = "mine"
display_name = "Mine"
match = { regex = "mine" }
exclude = "--helper"
image_url = "https://example.test/mine.png"
`
	if err := os.WriteFile(inputPath, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadPath(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.CustomTools) != 1 || loaded.CustomTools[0].Exclude != "--helper" {
		t.Fatalf("loaded custom tool exclude = %#v", loaded.CustomTools)
	}
	if err := Save(loaded, savedPath); err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadPath(savedPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.CustomTools) != 1 || reloaded.CustomTools[0].Exclude != "--helper" {
		t.Fatalf("reloaded custom tool exclude = %#v", reloaded.CustomTools)
	}

	reg, err := registry.NewWithCustom(reloaded.CustomTools...)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.MatchProcess(registry.ProcessInfo{Name: "mine", Cmdline: "mine --helper"}); ok {
		t.Fatal("custom exclude did not reject helper process")
	}
	if tool, ok := reg.MatchProcess(registry.ProcessInfo{Name: "mine", Cmdline: "mine --interactive"}); !ok || tool.ID != "mine" {
		t.Fatalf("interactive process match = (%#v, %t), want custom tool", tool, ok)
	}
}

// TestManagerReloadRejectsChangingNonAtomicTruncationWindow is the #410
// regression: a
// non-atomic save (truncate, then write the final content after a delay)
// produces a transient empty file that parses as valid TOML. A reload that
// fires during that window must not adopt the transient defaults as
// last-good. Because the provisional snapshot changes during the settle
// budget, this reload is a no-op; the write completion's fsnotify event
// triggers a later reload of the final content.
func TestManagerReloadRejectsChangingNonAtomicTruncationWindow(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `scan_interval = "7s"`)
	manager := NewManagerPath(path)

	writer := newNonAtomicWriter(t)
	defer writer.wait(t)
	writer.write(path, `scan_interval = "9s"`, 3*time.Millisecond)

	<-writer.truncated
	// Fire the reload right as fsnotify would for the truncation event, while
	// the file is still 0 bytes on disk.
	if err := manager.Reload(); err != nil {
		t.Fatalf("Reload during truncation window returned error: %v", err)
	}

	cfg, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if cfg.ScanInterval != "7s" {
		t.Fatalf("last-good scan interval = %q, want unchanged value 7s after provisional snapshot changed", cfg.ScanInterval)
	}
}

// TestManagerReloadDeliberatelyEmptiedConfigLoadsDefaults confirms that the
// extended guard delays, but does not reject, an intentional reset.
func TestManagerReloadDeliberatelyEmptiedConfigLoadsDefaults(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "enabled = false\nscan_interval = \"7s\"\n")
	manager := NewManagerPath(path)

	writeConfig(t, path, ``)
	if err := manager.Reload(); err != nil {
		t.Fatalf("Reload of a deliberately emptied config returned error: %v", err)
	}
	assertManagerEnabledFalse(t, manager, "while deliberate blank is pending")
	if manager.LastError() != nil {
		t.Fatalf("LastError() = %v while deliberate blank is pending", manager.LastError())
	}
	select {
	case reload := <-manager.Reloads():
		t.Fatalf("pending deliberate blank published a misleading reload: %#v", reload)
	default:
	}

	select {
	case reload := <-manager.Reloads():
		if reload.Err != nil || !reload.Config.Enabled || reload.Config.ScanInterval != Default().ScanInterval {
			t.Fatalf("deliberate blank reload = %#v, want enabled defaults", reload)
		}
	case <-time.After(enabledLooseningHorizon + time.Second):
		t.Fatal("timed out waiting for deliberate blank to load defaults")
	}
}

// TestManagerReloadPreservesEnabledFalseAcrossNonAtomicRewrite is the privacy
// variant of #410: enabled=false travels the same reload path as
// scan_interval, so a non-atomic rewrite of an unrelated field must not let
// the transient empty file flip enabled back to its true default.
func TestManagerReloadPreservesEnabledFalseAcrossNonAtomicRewrite(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, "enabled = false\nscan_interval = \"7s\"\n")
	manager := NewManagerPath(path)
	if cfg, err := manager.Current(); err != nil || cfg.Enabled {
		t.Fatalf("initial Current() = (%#v, %v), want enabled=false", cfg, err)
	}

	writer := newNonAtomicWriter(t)
	defer writer.wait(t)
	writer.write(path, "enabled = false\nscan_interval = \"9s\"\n", 3*time.Millisecond)

	<-writer.truncated
	if err := manager.Reload(); err != nil {
		t.Fatalf("Reload during truncation window returned error: %v", err)
	}

	cfg, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if cfg.Enabled {
		t.Fatal("enabled flipped to true after a non-atomic rewrite of an unrelated field; reload must not adopt the transient empty-file default")
	}
	if cfg.ScanInterval != "7s" {
		t.Fatalf("scan interval = %q, want unchanged last-good value 7s after provisional snapshot changed", cfg.ScanInterval)
	}
}

func TestManagerReloadPreservesEnabledFalseAcrossUnlinkRecreateWindow(t *testing.T) {
	const contents = "enabled = false\nscan_interval = \"9s\"\n"
	path := withConfigHome(t)
	writeConfig(t, path, contents)
	manager := NewManagerPath(path)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	writeDone := make(chan error, 1)
	go func() {
		time.Sleep(120 * time.Millisecond)
		writeDone <- os.WriteFile(path, []byte(contents), 0o600)
	}()
	defer func() {
		if err := <-writeDone; err != nil {
			t.Errorf("recreate config: %v", err)
		}
	}()

	if err := manager.Reload(); err != nil {
		t.Fatalf("Reload during unlink-recreate window returned error: %v", err)
	}

	cfg, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if cfg.Enabled {
		t.Fatal("enabled flipped to true after Reload observed the missing-file window of an unlink-recreate save")
	}
	if cfg.ScanInterval != "9s" {
		t.Fatalf("scan interval = %q, want unchanged last-good value 9s during unlink-recreate save", cfg.ScanInterval)
	}
}

func TestManagerReloadAcceptsStableConfigDeletion(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "enabled = false\nscan_interval = \"9s\"\n")
	manager := NewManagerPath(path)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reload(); err != nil {
		t.Fatalf("Reload after deliberate deletion returned error: %v", err)
	}

	assertManagerEnabledFalse(t, manager, "while deliberate deletion is pending")
	select {
	case reload := <-manager.Reloads():
		if reload.Err != nil || !reload.Config.Enabled || reload.Config.ScanInterval != Default().ScanInterval {
			t.Fatalf("deliberate deletion reload = %#v, want defaults", reload)
		}
	case <-time.After(enabledLooseningHorizon + time.Second):
		t.Fatal("timed out waiting for deliberate deletion to load defaults")
	}
}

func TestProvisionalConfigSnapshotMissingDependsOnAcceptedFile(t *testing.T) {
	missing := fileSnapshot{}
	if provisionalConfigSnapshot(missing, fileSnapshot{}) {
		t.Fatal("missing first-run candidate is provisional without a previously accepted file")
	}
	if !provisionalConfigSnapshot(missing, fileSnapshot{exists: true, data: []byte("enabled = false\n")}) {
		t.Fatal("missing candidate is not provisional after a file was previously accepted")
	}
}

func TestManagerReloadRejectsEmptySnapshotThatChangesDuringFullSettleBudget(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, "scan_interval = \"9s\"\nenabled = false\n")
	manager := NewManagerPath(path)

	writer := newNonAtomicWriter(t)
	defer writer.wait(t)
	writer.write(path, "scan_interval = \"9s\"\nenabled = false\n", 100*time.Millisecond)

	<-writer.truncated
	if err := manager.Reload(); err != nil {
		t.Fatalf("Reload during stalled truncation returned error: %v", err)
	}

	cfg, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if cfg.Enabled {
		t.Fatal("enabled flipped to true after the empty provisional snapshot changed during the settle budget")
	}
}

func TestManagerReloadRejectsPrefixSnapshotThatChangesDuringFullSettleBudget(t *testing.T) {
	const prefix = "scan_interval = \"9s\"\n"
	const suffix = "enabled = false\n"
	path := withConfigHome(t)
	writeConfig(t, path, prefix+suffix)
	manager := NewManagerPath(path)

	writer := newNonAtomicWriter(t)
	defer writer.wait(t)
	writer.writeChunked(path, prefix, suffix, 60*time.Millisecond)

	<-writer.truncated
	if err := manager.Reload(); err != nil {
		t.Fatalf("Reload during chunked write returned error: %v", err)
	}

	cfg, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if cfg.Enabled {
		t.Fatal("enabled flipped to true after the strict-prefix provisional snapshot changed during the settle budget")
	}
	if cfg.ScanInterval != "9s" {
		t.Fatalf("scan interval = %q, want final value 9s", cfg.ScanInterval)
	}
}

func TestManagerReloadAcceptsStableTrailingLineDeletion(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "scan_interval = \"9s\"\nenabled = false\n")
	manager := NewManagerPath(path)

	writeConfig(t, path, "scan_interval = \"9s\"\n")
	if err := manager.Reload(); err != nil {
		t.Fatalf("Reload after deliberate trailing-line deletion returned error: %v", err)
	}

	assertManagerEnabledFalse(t, manager, "while trailing-line deletion is pending")
	select {
	case reload := <-manager.Reloads():
		if reload.Err != nil || !reload.Config.Enabled || reload.Config.ScanInterval != "9s" {
			t.Fatalf("trailing-line deletion reload = %#v, want enabled=true and scan interval 9s", reload)
		}
	case <-time.After(enabledLooseningHorizon + time.Second):
		t.Fatal("timed out waiting for trailing-line deletion to load")
	}
}

func TestManagerReloadRetryRearmsForChangedLoosening(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "enabled = false\nscan_interval = \"9s\"\n")
	manager := NewManagerPath(path)

	writeConfig(t, path, "")
	if err := manager.Reload(); err != nil {
		t.Fatalf("Reload() for initial blank = %v", err)
	}
	assertManagerEnabledFalse(t, manager, "while initial blank is pending")

	// Change to a different loosening before the first retry fires. There is
	// deliberately no further Reload call: the retry that observes this new
	// snapshot must start a fresh horizon and arrange its own successor.
	time.Sleep(500 * time.Millisecond)
	writeConfig(t, path, "scan_interval = \"5s\"\n")

	select {
	case reload := <-manager.Reloads():
		if reload.Err != nil || !reload.Config.Enabled || reload.Config.ScanInterval != "5s" {
			t.Fatalf("changed loosening reload = %#v, want enabled=true and scan interval 5s", reload)
		}
	case <-time.After(2*enabledLooseningHorizon + 2*time.Second):
		t.Fatal("timed out waiting for retry to apply changed loosening without another Reload call")
	}
}

func TestManagerReloadEnabledGuardKnownWriterEscapes(t *testing.T) {
	const (
		initial = "scan_interval = \"9s\"\nenabled = false\n"
		final   = "scan_interval = \"5s\"\nenabled = false\n"
	)

	tests := []struct {
		name  string
		start func(string) scheduledConfigWrite
	}{
		{
			name: "divergent partial",
			start: func(path string) scheduledConfigWrite {
				return startScheduledConfigWrite(func(ready chan<- struct{}) error {
					f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
					if err != nil {
						return err
					}
					if _, err := f.WriteString("scan_interval = \"5s\"\n"); err != nil {
						_ = f.Close()
						return err
					}
					close(ready)
					time.Sleep(80 * time.Millisecond)
					if _, err := f.WriteString("enabled = false\n"); err != nil {
						_ = f.Close()
						return err
					}
					return f.Close()
				})
			},
		},
		{
			name: "chunked growing divergent",
			start: func(path string) scheduledConfigWrite {
				return startScheduledConfigWrite(func(ready chan<- struct{}) error {
					f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
					if err != nil {
						return err
					}
					if _, err := f.WriteString("scan_interval = \"5s\"\n"); err != nil {
						_ = f.Close()
						return err
					}
					close(ready)
					time.Sleep(55 * time.Millisecond)
					if _, err := f.WriteString("idle_clear_timeout = \"10m\"\n"); err != nil {
						_ = f.Close()
						return err
					}
					time.Sleep(55 * time.Millisecond)
					if _, err := f.WriteString("enabled = false\n"); err != nil {
						_ = f.Close()
						return err
					}
					return f.Close()
				})
			},
		},
		{
			name: "shrinking divergent",
			start: func(path string) scheduledConfigWrite {
				return startScheduledConfigWrite(func(ready chan<- struct{}) error {
					const partial = "scan_interval = \"5s\"\nidle_clear_timeout = \"10m\"\n"
					if err := os.WriteFile(path, []byte(partial), 0o600); err != nil {
						return err
					}
					close(ready)
					time.Sleep(55 * time.Millisecond)
					if err := os.Truncate(path, int64(len("scan_interval = \"5s\"\n"))); err != nil {
						return err
					}
					time.Sleep(55 * time.Millisecond)
					f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
					if err != nil {
						return err
					}
					if _, err := f.WriteString("enabled = false\n"); err != nil {
						_ = f.Close()
						return err
					}
					return f.Close()
				})
			},
		},
		{
			name: "empty beyond settle budget",
			start: func(path string) scheduledConfigWrite {
				return startScheduledConfigWrite(func(ready chan<- struct{}) error {
					f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
					if err != nil {
						return err
					}
					close(ready)
					time.Sleep(450 * time.Millisecond)
					if _, err := f.WriteString(final); err != nil {
						_ = f.Close()
						return err
					}
					return f.Close()
				})
			},
		},
		{
			name: "missing beyond settle budget",
			start: func(path string) scheduledConfigWrite {
				return startScheduledConfigWrite(func(ready chan<- struct{}) error {
					if err := os.Remove(path); err != nil {
						return err
					}
					close(ready)
					time.Sleep(450 * time.Millisecond)
					return os.WriteFile(path, []byte(final), 0o600)
				})
			},
		},
		{
			name: "prefix beyond settle budget",
			start: func(path string) scheduledConfigWrite {
				return startScheduledConfigWrite(func(ready chan<- struct{}) error {
					f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
					if err != nil {
						return err
					}
					if _, err := f.WriteString("scan_interval = \"9s\"\n"); err != nil {
						_ = f.Close()
						return err
					}
					close(ready)
					time.Sleep(450 * time.Millisecond)
					if _, err := f.WriteString("enabled = false\n"); err != nil {
						_ = f.Close()
						return err
					}
					return f.Close()
				})
			},
		},
		{
			name: "rename partial then append",
			start: func(path string) scheduledConfigWrite {
				return startScheduledConfigWrite(func(ready chan<- struct{}) error {
					tmp := path + ".partial"
					if err := os.WriteFile(tmp, []byte("scan_interval = \"5s\"\n"), 0o600); err != nil {
						return err
					}
					if err := os.Rename(tmp, path); err != nil {
						return err
					}
					close(ready)
					time.Sleep(80 * time.Millisecond)
					f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
					if err != nil {
						return err
					}
					if _, err := f.WriteString("enabled = false\n"); err != nil {
						_ = f.Close()
						return err
					}
					return f.Close()
				})
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			writeConfig(t, path, initial)
			manager := NewManagerPath(path)
			write := tt.start(path)
			waitScheduledConfigReady(t, write)

			if err := manager.Reload(); err != nil {
				t.Fatalf("Reload() during partial write = %v", err)
			}
			assertManagerEnabledFalse(t, manager, "after partial reload")
			waitScheduledConfigWrite(t, write)
			if err := manager.Reload(); err != nil {
				t.Fatalf("Reload() after writer completion = %v", err)
			}
			assertManagerEnabledFalse(t, manager, "after final reload")
		})
	}
}

func TestManagerReloadExplicitEnabledTrueHasNoExtendedDelay(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "enabled = false\nscan_interval = \"9s\"\n")
	manager := NewManagerPath(path)
	writeConfig(t, path, "enabled = true\nscan_interval = \"5s\"\n")

	start := time.Now()
	if err := manager.Reload(); err != nil {
		t.Fatal(err)
	}
	cfg, err := manager.Current()
	if err != nil || !cfg.Enabled || cfg.ScanInterval != "5s" {
		t.Fatalf("explicit true Current() = (%#v, %v)", cfg, err)
	}
	if elapsed := time.Since(start); elapsed >= enabledLooseningHorizon {
		t.Fatalf("explicit enabled=true took %v, want less than extended horizon %v", elapsed, enabledLooseningHorizon)
	}
}

func TestManagerReloadNormalEditKeepsEnabledFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "enabled = false\nscan_interval = \"9s\"\n")
	manager := NewManagerPath(path)
	writeConfig(t, path, "enabled = false\nscan_interval = \"5s\"\n")

	if err := manager.Reload(); err != nil {
		t.Fatal(err)
	}
	cfg, err := manager.Current()
	if err != nil || cfg.Enabled || cfg.ScanInterval != "5s" {
		t.Fatalf("normal enabled=false edit Current() = (%#v, %v)", cfg, err)
	}
}

// TestManagerReloadRandomWriterSchedules is the load-bearing property for
// #434. Every randomized schedule ends with an explicit enabled=false, and
// the invariant is that Manager never exposes enabled=true while getting
// there. The fixed seed set keeps CI reproducible while varying chunk sizes,
// pauses, and save strategy.
//
// Stalls longer than enabledLooseningHorizon are deliberately excluded. Such
// a valid key-omitting snapshot is indistinguishable from an intentional
// blank reset and is the documented residual of the time-bounded guard.
func TestManagerReloadRandomWriterSchedules(t *testing.T) {
	const schedulesPerSeed = 10
	seeds := []int64{434, 410, 425}

	for _, seed := range seeds {
		for schedule := 0; schedule < schedulesPerSeed; schedule++ {
			seed := seed
			schedule := schedule
			t.Run(fmt.Sprintf("seed_%d_schedule_%02d", seed, schedule), func(t *testing.T) {
				t.Parallel()
				rng := rand.New(rand.NewSource(seed*1000 + int64(schedule)))
				path := filepath.Join(t.TempDir(), "config.toml")
				writeConfig(t, path, "scan_interval = \"9s\"\nenabled = false\n")
				manager := NewManagerPath(path)
				var enabledObservations atomic.Int64
				stopObserver := make(chan struct{})
				observerDone := make(chan struct{})
				var stopObserverOnce sync.Once
				stopAndWaitForObserver := func() {
					stopObserverOnce.Do(func() {
						close(stopObserver)
						<-observerDone
					})
				}
				t.Cleanup(stopAndWaitForObserver)
				go func() {
					defer close(observerDone)
					for {
						select {
						case <-stopObserver:
							return
						default:
						}
						cfg, _ := manager.Current()
						if cfg.Enabled {
							enabledObservations.Add(1)
						}
					}
				}()

				scanSeconds := 4 + rng.Intn(5)
				firstLine := fmt.Sprintf("scan_interval = \"%ds\"\n", scanSeconds)
				suffix := []byte("idle_clear_timeout = \"10m\"\nenabled = false\n")
				var sizes []int
				var pauses []time.Duration
				for remaining := len(suffix); remaining > 0; {
					size := 1 + rng.Intn(12)
					sizes = append(sizes, size)
					pauses = append(pauses, time.Duration(3+rng.Intn(15))*time.Millisecond)
					remaining -= size
				}
				initialPause := time.Duration(45+rng.Intn(70)) * time.Millisecond

				var write scheduledConfigWrite
				switch rng.Intn(3) {
				case 0:
					write = startScheduledConfigWrite(func(ready chan<- struct{}) error {
						f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o600)
						if err != nil {
							return err
						}
						if _, err := f.WriteString(firstLine); err != nil {
							_ = f.Close()
							return err
						}
						close(ready)
						time.Sleep(initialPause)
						if err := writeChunks(f, suffix, sizes, pauses); err != nil {
							_ = f.Close()
							return err
						}
						return f.Close()
					})
				case 1:
					write = startScheduledConfigWrite(func(ready chan<- struct{}) error {
						if err := os.Remove(path); err != nil {
							return err
						}
						close(ready)
						// Missing snapshots use the existing ~300ms settle
						// budget, so exercise stalls beyond that budget while
						// remaining far below the extended horizon.
						time.Sleep(time.Duration(350+rng.Intn(150)) * time.Millisecond)
						f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
						if err != nil {
							return err
						}
						if _, err := f.WriteString(firstLine); err != nil {
							_ = f.Close()
							return err
						}
						if err := writeChunks(f, suffix, sizes, pauses); err != nil {
							_ = f.Close()
							return err
						}
						return f.Close()
					})
				default:
					write = startScheduledConfigWrite(func(ready chan<- struct{}) error {
						tmp := path + ".partial"
						if err := os.WriteFile(tmp, []byte(firstLine), 0o600); err != nil {
							return err
						}
						if err := os.Rename(tmp, path); err != nil {
							return err
						}
						close(ready)
						time.Sleep(initialPause)
						f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
						if err != nil {
							return err
						}
						if err := writeChunks(f, suffix, sizes, pauses); err != nil {
							_ = f.Close()
							return err
						}
						return f.Close()
					})
				}

				waitScheduledConfigReady(t, write)
				if err := manager.Reload(); err != nil {
					t.Fatalf("Reload() during randomized write = %v", err)
				}
				waitScheduledConfigWrite(t, write)
				if err := manager.Reload(); err != nil {
					t.Fatalf("Reload() after randomized write = %v", err)
				}
				stopAndWaitForObserver()
				if got := enabledObservations.Load(); got != 0 {
					t.Fatalf("Current() exposed enabled=true %d times during randomized write", got)
				}
			})
		}
	}
}

// TestManagerWatchIgnoresTransientEmptyDuringNonAtomicWrite drives the #410
// scenario through the real fsnotify-backed Watch() pipeline (the same path
// production and CI hit), rather than calling Reload() directly. It confirms
// last-good never regresses to defaults for any reload observed while a
// non-atomic save is in flight, and settles on the final content once the
// filesystem quiets down.
func TestManagerWatchIgnoresTransientEmptyDuringNonAtomicWrite(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `scan_interval = "7s"`)
	manager := NewManagerPath(path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := manager.Watch(ctx); err != nil {
		t.Fatal(err)
	}

	writer := newNonAtomicWriter(t)
	defer writer.wait(t)
	writer.write(path, `scan_interval = "9s"`, 3*time.Millisecond)

	deadline := time.After(2 * time.Second)
	sawFinal := false
	for !sawFinal {
		select {
		case reload := <-manager.Reloads():
			if reload.Err == nil && reload.Config.ScanInterval == Default().ScanInterval && Default().ScanInterval != "7s" {
				t.Fatalf("watch-driven reload adopted defaults as last-good: %#v", reload)
			}
			if reload.Err == nil && reload.Config.ScanInterval == "9s" {
				sawFinal = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for the fsnotify-driven reload to settle on the final content")
		}
	}

	current, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if current.ScanInterval != "9s" {
		t.Fatalf("last-good scan interval = %q, want settled value 9s", current.ScanInterval)
	}
}

// ---------------------------------------------------------------------------
// #447: the loosening horizon must guard every privacy-loosening transition,
// not just the global enabled flag.
// ---------------------------------------------------------------------------

// TestPrivacyPostureCoversAllPrivacyFields fails the day a field is added to
// Privacy without a conscious decision about whether the loosening-horizon
// posture snapshot (postureFor in config.go) needs to grow to cover it. That
// enumeration-of-one failure mode is what let #447 happen: the #434 guard
// covered exactly the enabled flag and nothing else.
func TestPrivacyPostureCoversAllPrivacyFields(t *testing.T) {
	// Every field of Privacy must be listed here. Add a new field to postureFor
	// (and to permissivenessLoosened's reasoning about it) before adding it here.
	covered := map[string]bool{
		"ShowDirectory":         true,
		"DirectoryAllowlist":    true,
		"DirectoryBasenameOnly": true,
	}
	typ := reflect.TypeOf(Privacy{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !covered[name] {
			t.Fatalf("Privacy field %q is not covered by the loosening-horizon posture snapshot; "+
				"add it to postureFor/permissivenessLoosened in config.go and to this test's covered list", name)
		}
	}
	if len(covered) != typ.NumField() {
		t.Fatalf("covered-field list is stale: expected %d Privacy fields, list declares %d", typ.NumField(), len(covered))
	}
}

// TestPrivacyPostureCoversToolOverridePrivacyFields is the ToolOverride
// counterpart. ToolOverride mixes privacy fields (Enabled, ShowDirectory,
// DirectoryAllowlist, DirectoryBasenameOnly) with display-only fields
// (ToolName, ElapsedTimer, SmallImage, Buttons) that do not affect what is
// disclosed. A new exported field must be explicitly classified into one of
// the two lists below, so it cannot silently join neither.
func TestPrivacyPostureCoversToolOverridePrivacyFields(t *testing.T) {
	displayOnly := map[string]bool{
		"ToolName":     true,
		"ElapsedTimer": true,
		"SmallImage":   true,
		"Buttons":      true,
	}
	privacyRelevant := map[string]bool{
		"Enabled":               true,
		"ShowDirectory":         true,
		"DirectoryAllowlist":    true,
		"DirectoryBasenameOnly": true,
	}
	typ := reflect.TypeOf(ToolOverride{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name := field.Name
		if displayOnly[name] {
			continue
		}
		if !privacyRelevant[name] {
			t.Fatalf("ToolOverride field %q is classified as neither display-only nor privacy-relevant; "+
				"classify it in TestPrivacyPostureCoversToolOverridePrivacyFields and, if privacy-relevant, "+
				"confirm postureFor/Config.Resolve cover it", name)
		}
	}
}

// assertPrivacyHoldsAcrossTruncationStall writes prefix immediately (leaving
// a genuinely truncated, non-empty, stable-looking file on disk), stalls for
// stall (which must exceed the ordinary ~300ms settle budget so the prefix
// itself would otherwise be accepted as final), then appends suffix to
// complete the original content. While the writer is stalled beyond the
// ordinary settle budget but inside the enabledLooseningHorizon, check must
// keep reporting the restrictive state on every observed reload.
func assertPrivacyHoldsAcrossTruncationStall(t *testing.T, path string, manager *Manager, prefix, suffix string, stall time.Duration, check func(Config) error) {
	t.Helper()
	writer := newNonAtomicWriter(t)
	defer writer.wait(t)
	writer.writeChunked(path, prefix, suffix, stall)
	<-writer.truncated

	deadline := time.Now().Add(stall - 50*time.Millisecond)
	if deadline.Before(time.Now().Add(reloadSettleInterval * (reloadSettleAttempts + 5))) {
		t.Fatalf("test stall %v too short to observe the ordinary settle budget elapse", stall)
	}
	observed := 0
	for time.Now().Before(deadline) {
		_ = manager.Reload()
		cfg, err := manager.Current()
		if err != nil {
			t.Fatalf("Current() during stall = %v", err)
		}
		if err := check(cfg); err != nil {
			t.Fatalf("%v (observed %d times before this failure)", err, observed)
		}
		observed++
		time.Sleep(20 * time.Millisecond)
	}
	if observed == 0 {
		t.Fatal("test never actually observed a reload during the stall window")
	}
}

// TestManagerReloadPreservesAllowlistAcrossTruncationStall is the #447
// reproduction: a writer truncates the config to a strict prefix that drops
// the directory_allowlist line, then stalls past the ordinary ~300ms settle
// budget. Before this fix, the guard only covered the enabled flag, so the
// truncated (unrestricted) allowlist was accepted as soon as it looked
// stable, deep inside the 3s horizon.
func TestManagerReloadPreservesAllowlistAcrossTruncationStall(t *testing.T) {
	const prefix = "enabled = true\n" +
		"[privacy]\n" +
		"show_directory = true\n"
	const suffix = "directory_allowlist = [\"/allowed/only\"]\n"

	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, prefix+suffix)
	manager := NewManagerPath(path)

	before, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() before truncation = %v", err)
	}
	if resolved := before.Resolve(registry.Tool{ID: "any"}); resolved.DirectoryAllowed("/home/secret") {
		t.Fatal("precondition failed: /home/secret should not be allowed before truncation")
	}

	assertPrivacyHoldsAcrossTruncationStall(t, path, manager, prefix, suffix, 900*time.Millisecond, func(cfg Config) error {
		if resolved := cfg.Resolve(registry.Tool{ID: "any"}); resolved.DirectoryAllowed("/home/secret") {
			return fmt.Errorf("allowlist lost to a stalled truncation before the loosening horizon elapsed: %#v", cfg.Privacy)
		}
		return nil
	})
}

// TestManagerReloadPreservesPerToolShowDirectoryAcrossTruncationStall covers
// a per-tool show_directory=false override (extra privacy for one tool) that
// is lost to a truncating, stalling writer while the global show_directory
// is true. Losing the override must not immediately re-expose that tool's
// directory.
func TestManagerReloadPreservesPerToolShowDirectoryAcrossTruncationStall(t *testing.T) {
	const prefix = "enabled = true\n" +
		"[privacy]\n" +
		"show_directory = true\n"
	const suffix = "[tools.claude-code]\n" +
		"show_directory = false\n"

	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, prefix+suffix)
	manager := NewManagerPath(path)

	assertPrivacyHoldsAcrossTruncationStall(t, path, manager, prefix, suffix, 900*time.Millisecond, func(cfg Config) error {
		resolved := cfg.Resolve(registry.Tool{ID: "claude-code"})
		if resolved.ShowDirectory {
			return fmt.Errorf("per-tool show_directory=false lost to a stalled truncation before the loosening horizon elapsed: %#v", cfg.Tools)
		}
		return nil
	})
}

// TestManagerReloadPreservesPerToolBasenameOnlyAcrossTruncationStall covers a
// per-tool directory_basename_only=true override (extra privacy for one
// tool, hiding nested path segments) that is lost to a truncating, stalling
// writer while the global directory_basename_only is false.
func TestManagerReloadPreservesPerToolBasenameOnlyAcrossTruncationStall(t *testing.T) {
	const prefix = "enabled = true\n" +
		"[privacy]\n" +
		"show_directory = true\n" +
		"directory_basename_only = false\n"
	const suffix = "[tools.claude-code]\n" +
		"directory_basename_only = true\n"

	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, prefix+suffix)
	manager := NewManagerPath(path)

	assertPrivacyHoldsAcrossTruncationStall(t, path, manager, prefix, suffix, 900*time.Millisecond, func(cfg Config) error {
		resolved := cfg.Resolve(registry.Tool{ID: "claude-code"})
		if !resolved.DirectoryBasenameOnly {
			return fmt.Errorf("per-tool directory_basename_only=true lost to a stalled truncation before the loosening horizon elapsed: %#v", cfg.Tools)
		}
		return nil
	})
}

// TestManagerReloadPreservesPerToolOptOutAcrossTruncationStall is the #447
// per-tool reproduction: a `[tools.<id>] enabled = false` opt-out is dropped
// by a stalling truncation and must not silently re-enable that tool.
func TestManagerReloadPreservesPerToolOptOutAcrossTruncationStall(t *testing.T) {
	const prefix = "enabled = true\n"
	const suffix = "[tools.claude-code]\n" +
		"enabled = false\n"

	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, prefix+suffix)
	manager := NewManagerPath(path)

	assertPrivacyHoldsAcrossTruncationStall(t, path, manager, prefix, suffix, 900*time.Millisecond, func(cfg Config) error {
		resolved := cfg.Resolve(registry.Tool{ID: "claude-code"})
		if resolved.Enabled {
			return fmt.Errorf("per-tool opt-out lost to a stalled truncation before the loosening horizon elapsed: %#v", cfg.Tools)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// #448: the blank-config horizon for whole-document loads must not exit early.
// ---------------------------------------------------------------------------

// TestLoadPathHorizonSurvivesFileDeletionMidHorizon is #448 route A, driven
// by an injected snapshot sequence and virtual clock (not real timing, so it
// cannot be flaky in CI): the file reads as blank, then "disappears" 700ms
// (virtual) into the load. Before the fix, boundedSettledConfigSnapshotWith
// always assumed no file had ever existed, so the missing-file fast path
// fired and handed back defaults at ~700ms, deep inside the 3s horizon.
func TestLoadPathHorizonSurvivesFileDeletionMidHorizon(t *testing.T) {
	start := time.Unix(0, 0)
	current := start
	now := func() time.Time { return current }
	sleep := func(d time.Duration) { current = current.Add(d) }

	const deleteAt = 700 * time.Millisecond
	snapshot := func(string) fileSnapshot {
		if current.Sub(start) < deleteAt {
			return fileSnapshot{exists: true, data: []byte("  \n")}
		}
		return fileSnapshot{exists: false}
	}

	snap, err := settledConfigSnapshotForLoadWith("config.toml", snapshot, now, sleep)
	elapsed := current.Sub(start)
	if err != nil {
		t.Fatalf("settledConfigSnapshotForLoadWith() error = %v", err)
	}
	if elapsed < enabledLooseningHorizon {
		t.Fatalf("returned after %v virtual time with snap=%#v; want at least the %v horizon before accepting a disappearance as a deliberate deletion", elapsed, snap, enabledLooseningHorizon)
	}
	if snap.exists {
		t.Fatalf("snap = %#v, want missing (the deletion is stable at the point the horizon elapses)", snap)
	}
}

// TestLoadPathHorizonSurvivesContentFlickerMidHorizon is #448 route B, also
// driven by an injected snapshot sequence and virtual clock rather than real
// timing (a clock-driven flicker test would be flaky in CI -- exactly the
// mistake #441's first bounded-settle test made). The file reads as blank,
// then for one settle-interval tick shows real content (a writer's single
// exposed poll), then reverts to blank and stalls there for the rest of the
// load. Before the fix, that one non-blank poll caused the horizon loop to
// exit and re-settle once, landing back on blank and returning defaults
// immediately instead of continuing the horizon.
func TestLoadPathHorizonSurvivesContentFlickerMidHorizon(t *testing.T) {
	start := time.Unix(0, 0)
	current := start
	now := func() time.Time { return current }
	sleep := func(d time.Duration) { current = current.Add(d) }

	const flickerAt = 933 * time.Millisecond
	snapshot := func(string) fileSnapshot {
		elapsed := current.Sub(start)
		if elapsed >= flickerAt && elapsed < flickerAt+reloadSettleInterval {
			return fileSnapshot{exists: true, data: []byte("pin = \"flicker\"\n")}
		}
		return fileSnapshot{exists: true, data: []byte("  \n")}
	}

	snap, err := settledConfigSnapshotForLoadWith("config.toml", snapshot, now, sleep)
	elapsed := current.Sub(start)
	if err != nil {
		t.Fatalf("settledConfigSnapshotForLoadWith() error = %v", err)
	}
	if elapsed < enabledLooseningHorizon {
		t.Fatalf("flicker returned after %v virtual time with snap=%#v; want at least the %v horizon", elapsed, snap, enabledLooseningHorizon)
	}
	if !ambiguousBlankConfigSnapshot(snap) {
		t.Fatalf("snap = %#v, want ambiguous-blank (the flicker never produced a genuinely settled non-blank result)", snap)
	}
}

// ---------------------------------------------------------------------------
// #449: a blank directory_allowlist entry must not silently mean allow-all.
// ---------------------------------------------------------------------------

func TestDirectoryAllowlistBlankEntriesRejected(t *testing.T) {
	cases := []string{
		`directory_allowlist = [""]`,
		`directory_allowlist = [" "]`,
		`directory_allowlist = ["", ""]`,
		`directory_allowlist = ["/allowed", " "]`,
	}
	for _, entriesLine := range cases {
		t.Run(entriesLine, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			content := "enabled = true\n[privacy]\nshow_directory = true\n" + entriesLine + "\n"
			writeConfig(t, path, content)

			cfg, err := LoadPath(path)
			if err == nil {
				t.Fatalf("LoadPath() with blank allowlist entry = nil error, want a validation error; resolved allowlist = %#v", cfg.Privacy.DirectoryAllowlist)
			}
			if cfg.Enabled {
				t.Fatalf("invalid config left presence enabled: %#v", cfg)
			}
		})
	}
}

// TestDirectoryAllowlistPresentButEmptyWarnsAndAllowsAll covers the lead's
// review finding on the original #449 fix: `termp config init`'s own
// AnnotatedSample has always emitted a top-level `directory_allowlist = []`
// for every existing user. Rejecting that outright at load would silently
// disable presence on upgrade for every one of them (an invalid config loads
// with presence off per the #395 policy) -- exactly the kind of silent
// failure this issue family exists to eliminate, just moved to a new place.
// A present-but-empty top-level allowlist must instead load successfully,
// keep meaning "no restriction configured" (same as an absent key), and
// surface a Config.Warnings entry so the ambiguity is visible, not silent.
func TestDirectoryAllowlistPresentButEmptyWarnsAndAllowsAll(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "enabled = true\n[privacy]\nshow_directory = true\ndirectory_allowlist = []\n")

	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatalf("LoadPath() with present-but-empty top-level allowlist = %v, want success", err)
	}
	if !cfg.Enabled {
		t.Fatal("present-but-empty top-level allowlist disabled presence; want it to load as no-restriction, same as an absent key")
	}
	resolved := cfg.Resolve(registry.Tool{ID: "any"})
	if !resolved.DirectoryAllowed("/anywhere/at/all") {
		t.Fatal("present-but-empty top-level allowlist should mean no restriction, same as an absent key")
	}
	found := false
	for _, w := range cfg.Warnings {
		if strings.Contains(w, "directory_allowlist") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no warning surfaced for a present-but-empty top-level allowlist; warnings = %#v", cfg.Warnings)
	}
}

// TestDirectoryAllowlistLegacyAnnotatedSampleStillLoads is the lead's
// requested regression: a byte-for-byte legacy config as `termp config init`
// generated it before #449 (including the trailing comment) must still load
// with presence enabled and a warning, not fail closed.
func TestDirectoryAllowlistLegacyAnnotatedSampleStillLoads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	legacy := "enabled = true\n" +
		"[privacy]\n" +
		"show_directory = false         # Show the working directory on Discord. Off by default.\n" +
		"directory_allowlist = []    # Optional path prefixes allowed when show_directory is true.\n" +
		"directory_basename_only = true # Show only the final directory name; false shows at most the last two segments.\n"
	writeConfig(t, path, legacy)

	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatalf("LoadPath() legacy config init output = %v, want success (this must not break every user who ever ran `termp config init`)", err)
	}
	if !cfg.Enabled {
		t.Fatal("legacy config from `termp config init` lost presence on load; every existing user would silently stop publishing")
	}
	found := false
	for _, w := range cfg.Warnings {
		if strings.Contains(w, "directory_allowlist") {
			found = true
		}
	}
	if !found {
		t.Fatalf("legacy config loaded but with no warning about the empty allowlist; warnings = %#v", cfg.Warnings)
	}
}

// TestManagerReloadStillGatesGlobalAllowlistBecomingPresentButEmpty confirms
// that downgrading the present-but-empty top-level allowlist from a hard
// validation error to a warning (per the lead's review of the original #449
// fix) did not accidentally route around #447's loosening horizon. The
// resolved allowlist becomes empty (unrestricted) either way; #447's guard
// operates on the resolved posture in Config.Resolve/permissivenessLoosened,
// entirely independent of whether validate() treats the source form as an
// error or a warning, so this transition must still be held pending rather
// than applied on the very next Reload.
func TestManagerReloadStillGatesGlobalAllowlistBecomingPresentButEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "enabled = true\n[privacy]\nshow_directory = true\ndirectory_allowlist = [\"/allowed/only\"]\n")
	manager := NewManagerPath(path)

	if resolved := mustCurrent(t, manager).Resolve(registry.Tool{ID: "any"}); resolved.DirectoryAllowed("/home/secret") {
		t.Fatal("precondition failed: /home/secret should not be allowed before the edit")
	}

	writeConfig(t, path, "enabled = true\n[privacy]\nshow_directory = true\ndirectory_allowlist = []\n")
	if err := manager.Reload(); err != nil {
		t.Fatalf("Reload() = %v", err)
	}

	resolved := mustCurrent(t, manager).Resolve(registry.Tool{ID: "any"})
	if resolved.DirectoryAllowed("/home/secret") {
		t.Fatal("an explicit present-but-empty allowlist bypassed the loosening horizon on the very next reload; #449's warning downgrade must not affect #447's gate")
	}
}

func mustCurrent(t *testing.T, manager *Manager) Config {
	t.Helper()
	cfg, err := manager.Current()
	if err != nil {
		t.Fatalf("Current() = %v", err)
	}
	return cfg
}

func TestDirectoryAllowlistAbsentMeansNoRestriction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "enabled = true\n[privacy]\nshow_directory = true\n")

	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatalf("LoadPath() with absent allowlist = %v, want success", err)
	}
	resolved := cfg.Resolve(registry.Tool{ID: "any"})
	if !resolved.DirectoryAllowed("/anywhere/at/all") {
		t.Fatal("an absent directory_allowlist must still mean no restriction")
	}
}

// TestDirectoryAllowlistToolOverrideEmptyAllowed confirms the #449 fix does
// not regress the documented per-tool behavior (docs/product/config-schema.md):
// an explicit, present-but-empty per-tool allowlist deliberately opts that
// tool out of a restrictive global allowlist, and remains valid.
func TestDirectoryAllowlistToolOverrideEmptyAllowed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := "enabled = true\n" +
		"[privacy]\n" +
		"show_directory = true\n" +
		"directory_allowlist = [\"/restricted\"]\n" +
		"[tools.claude-code]\n" +
		"directory_allowlist = []\n"
	writeConfig(t, path, content)

	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatalf("LoadPath() with empty per-tool allowlist override = %v, want success", err)
	}
	resolved := cfg.Resolve(registry.Tool{ID: "claude-code"})
	if !resolved.DirectoryAllowed("/anywhere/at/all") {
		t.Fatal("an explicit empty per-tool allowlist should opt that tool out of the global restriction")
	}
	other := cfg.Resolve(registry.Tool{ID: "other-tool"})
	if other.DirectoryAllowed("/anywhere/at/all") {
		t.Fatal("the global allowlist should still restrict tools without an override")
	}
}

// TestDirectoryAllowlistToolOverrideBlankEntryRejected confirms blank entries
// are rejected at the tool-override level too, not just the top level.
func TestDirectoryAllowlistToolOverrideBlankEntryRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	content := "enabled = true\n" +
		"[privacy]\n" +
		"show_directory = true\n" +
		"[tools.claude-code]\n" +
		"directory_allowlist = [\"\"]\n"
	writeConfig(t, path, content)

	if _, err := LoadPath(path); err == nil {
		t.Fatal("LoadPath() with blank per-tool allowlist entry = nil error, want a validation error")
	}
}

// TestSaveDoesNotRewriteAllowlistMeaning is the #449 round-trip guard: a
// valid, non-blank allowlist must come back byte-for-byte equivalent (as a
// resolved value) after Save, and Save must never be reached with a config
// whose allowlist entries were silently altered by loading.
func TestSaveDoesNotRewriteAllowlistMeaning(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, "enabled = true\n[privacy]\nshow_directory = true\ndirectory_allowlist = [\"/allowed/only\"]\n")

	cfg, err := LoadPath(path)
	if err != nil {
		t.Fatalf("LoadPath() = %v", err)
	}
	if err := Save(cfg, path); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(onDisk, []byte("directory_allowlist = []")) {
		t.Fatalf("Save() rewrote a non-blank allowlist to an empty one:\n%s", onDisk)
	}
	if !bytes.Contains(onDisk, []byte("/allowed/only")) {
		t.Fatalf("Save() lost the allowlist entry:\n%s", onDisk)
	}
}
