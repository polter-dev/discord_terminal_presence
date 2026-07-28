package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

type watchTestLister struct {
	processes []detector.Process
}

func (l watchTestLister) List() ([]detector.Process, error) {
	return l.processes, nil
}

type watchTestReconfigurer struct {
	calls int
	err   error
}

func (r *watchTestReconfigurer) Reconfigure(context.Context, *registry.Registry, detector.Config) error {
	r.calls++
	return r.err
}

type watchActivityUpdate struct {
	config    config.Config
	detection detector.Detection
}

func TestWatchConfigChangeMakesCustomToolDetectable(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	cfg := config.Default()
	cfg.ScanInterval = "1ms"
	applied, err := newDetectionRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	det, err := detector.New(applied.registry, watchTestLister{processes: []detector.Process{{Owned: true,
		Pid:  1,
		Name: "watch-reload-tool",
	}}}, applied.detectorConfig)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloads := make(chan config.ReloadResult)
	watchErrs := make(chan error)
	updates := make(chan watchActivityUpdate, 4)
	detections := det.RunReadOnly(ctx)
	go bridgeWatchActivityUpdates(ctx, reloads, watchErrs, det, applied, detections, func(cfg config.Config, detection detector.Detection) {
		updates <- watchActivityUpdate{config: cfg, detection: detection}
	}, func(string) {})

	select {
	case initial := <-updates:
		if !initial.detection.None {
			t.Fatalf("initial detection = %#v, want none", initial.detection)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial watch detection")
	}

	nextCfg := cfg
	nextCfg.CustomTools = []registry.CustomTool{{
		ID:          "watch-reload",
		DisplayName: "Watch Reload",
		Match:       registry.CustomMatch{Name: "watch-reload-tool"},
		ImageKey:    "watch-reload",
	}}
	reloads <- config.ReloadResult{Config: nextCfg}

	select {
	case reloaded := <-updates:
		if reloaded.detection.None || reloaded.detection.Tool.ID != "watch-reload" {
			t.Fatalf("reloaded detection = %#v, want watch-reload", reloaded.detection)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for custom tool detection after watch reload")
	}
}

func TestWatchDisplayOnlyChangeRerendersWithoutReconfigure(t *testing.T) {
	cfg := config.Default()
	applied, err := newDetectionRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloads := make(chan config.ReloadResult)
	watchErrs := make(chan error)
	detections := make(chan detector.Detection)
	updates := make(chan watchActivityUpdate, 2)
	reconfigurer := &watchTestReconfigurer{}
	go bridgeWatchActivityUpdates(ctx, reloads, watchErrs, reconfigurer, applied, detections, func(cfg config.Config, detection detector.Detection) {
		updates <- watchActivityUpdate{config: cfg, detection: detection}
	}, func(string) {})

	detection := detector.Detection{Tool: registry.Tool{ID: "claude-code", DisplayName: "Claude Code"}}
	detections <- detection
	<-updates

	nextCfg := cfg
	nextCfg.Privacy.ShowDirectory = true
	reloads <- config.ReloadResult{Config: nextCfg}
	select {
	case update := <-updates:
		if !update.config.Privacy.ShowDirectory || update.detection.Tool.ID != detection.Tool.ID {
			t.Fatalf("display-only update = %#v", update)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for display-only rerender")
	}
	if reconfigurer.calls != 0 {
		t.Fatalf("Reconfigure calls = %d, want 0 for display-only change", reconfigurer.calls)
	}
}

func TestWatchRejectsInvalidConfigAndKeepsSessionRunning(t *testing.T) {
	cfg := config.Default()
	applied, err := newDetectionRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloads := make(chan config.ReloadResult)
	watchErrs := make(chan error)
	detections := make(chan detector.Detection)
	updates := make(chan watchActivityUpdate, 2)
	warnings := make(chan string, 1)
	reconfigurer := &watchTestReconfigurer{err: errors.New("Reconfigure must not be called")}
	go bridgeWatchActivityUpdates(ctx, reloads, watchErrs, reconfigurer, applied, detections, func(cfg config.Config, detection detector.Detection) {
		updates <- watchActivityUpdate{config: cfg, detection: detection}
	}, func(warning string) {
		warnings <- warning
	})

	invalid := cfg
	invalid.Privacy.ShowDirectory = true
	invalid.CustomTools = []registry.CustomTool{{
		ID:          "broken",
		DisplayName: "Broken",
		Match:       registry.CustomMatch{Regex: "["},
		ImageKey:    "broken",
	}}
	reloads <- config.ReloadResult{Config: invalid}
	select {
	case warning := <-warnings:
		if want := "config reload failed; keeping last-good behavior"; !strings.Contains(warning, want) {
			t.Fatalf("watch warning = %q, want %q", warning, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for rejected-config warning")
	}

	detection := detector.Detection{Tool: registry.Tool{ID: "codex-cli", DisplayName: "Codex CLI"}}
	detections <- detection
	select {
	case update := <-updates:
		if update.config.Privacy.ShowDirectory {
			t.Fatal("invalid config replaced last-good watch config")
		}
		if update.detection.Tool.ID != detection.Tool.ID {
			t.Fatalf("post-rejection detection = %#v", update.detection)
		}
	case <-time.After(time.Second):
		t.Fatal("watch session stopped after invalid config")
	}
	if reconfigurer.calls != 0 {
		t.Fatalf("Reconfigure calls = %d, want 0 for rejected config", reconfigurer.calls)
	}
}

func TestWatchLabelsWatcherErrorWithoutTreatingItAsReloadFailure(t *testing.T) {
	cfg := config.Default()
	applied, err := newDetectionRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloads := make(chan config.ReloadResult)
	watchErrs := make(chan error)
	detections := make(chan detector.Detection)
	warnings := make(chan string, 1)
	go bridgeWatchActivityUpdates(ctx, reloads, watchErrs, &watchTestReconfigurer{}, applied, detections, func(config.Config, detector.Detection) {}, func(warning string) {
		warnings <- warning
	})

	watchErrs <- errors.New("watch backend failed")
	select {
	case warning := <-warnings:
		if !strings.Contains(warning, "config watcher error; continuing with current config") {
			t.Fatalf("watch warning = %q, want watcher-specific message", warning)
		}
		if strings.Contains(warning, "reload failed") {
			t.Fatalf("watch warning mislabeled as reload failure: %q", warning)
		}
		t.Logf("watcher warning: %s", warning)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for watcher warning")
	}
}
