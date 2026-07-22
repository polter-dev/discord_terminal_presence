package main

import (
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/config"
	"github.com/polter-dev/discord_terminal_presence/internal/detector"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

func TestApplyConfigChangeReloadsCustomTools(t *testing.T) {
	current, err := newDetectionRuntime(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := current.registry.Match("reload-only-tool"); ok {
		t.Fatal("unexpected initial custom-tool match")
	}

	nextCfg := config.Default()
	nextCfg.CustomTools = []registry.CustomTool{{
		ID:          "reload-only",
		DisplayName: "Reload only",
		Match:       registry.CustomMatch{Name: "reload-only-tool"},
		ImageKey:    "reload-only",
	}}
	next, change, err := applyConfigChange(current, nextCfg)
	if err != nil {
		t.Fatal(err)
	}
	if !change.registry || !change.detector {
		t.Fatalf("change = %+v, want registry and detector reload", change)
	}
	tool, ok := next.registry.Match("reload-only-tool")
	if !ok || tool.ID != "reload-only" {
		t.Fatalf("reloaded registry match = %#v, %t", tool, ok)
	}
}

func TestApplyConfigChangeReloadsPin(t *testing.T) {
	current, err := newDetectionRuntime(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	nextCfg := config.Default()
	nextCfg.Pin = "claude-code"
	next, change, err := applyConfigChange(current, nextCfg)
	if err != nil {
		t.Fatal(err)
	}
	if change.registry || !change.detector {
		t.Fatalf("change = %+v, want detector-only reload", change)
	}

	base := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	selection := detector.NewSelector(next.registry, next.detectorConfig, nil).Select([]detector.Process{
		{Pid: 1, Name: "claude", CreateTime: base},
		{Pid: 2, Name: "codex", CreateTime: base.Add(time.Minute)},
	})
	if selection.None || selection.Tool.ID != "claude-code" {
		t.Fatalf("pinned selection = %#v, want claude-code", selection)
	}
}

func TestApplyConfigChangeUpdatesScanInterval(t *testing.T) {
	current, err := newDetectionRuntime(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	nextCfg := config.Default()
	nextCfg.ScanInterval = "17ms"
	next, change, err := applyConfigChange(current, nextCfg)
	if err != nil {
		t.Fatal(err)
	}
	if !change.detector || !change.timing || change.registry {
		t.Fatalf("change = %+v, want detector timing reload", change)
	}
	if got := next.detectorConfig.ScanInterval; got != 17*time.Millisecond {
		t.Fatalf("scan interval = %s, want 17ms", got)
	}
}

func TestApplyConfigChangeKeepsDisplayReloadCheap(t *testing.T) {
	current, err := newDetectionRuntime(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	nextCfg := config.Default()
	nextCfg.Privacy.ShowDirectory = true
	next, change, err := applyConfigChange(current, nextCfg)
	if err != nil {
		t.Fatal(err)
	}
	if change.detector || change.registry || change.timing {
		t.Fatalf("display-only change = %+v", change)
	}
	if next.registry != current.registry {
		t.Fatal("display-only reload rebuilt registry")
	}
}

func TestApplyConfigChangeRejectsInvalidRegistryTransaction(t *testing.T) {
	current, err := newDetectionRuntime(config.Default())
	if err != nil {
		t.Fatal(err)
	}
	nextCfg := config.Default()
	nextCfg.Pin = "claude-code"
	nextCfg.CustomTools = []registry.CustomTool{{
		ID:          "broken",
		DisplayName: "Broken",
		Match:       registry.CustomMatch{Regex: "["},
		ImageKey:    "broken",
	}}
	next, change, err := applyConfigChange(current, nextCfg)
	if err == nil {
		t.Fatal("invalid custom matcher was accepted")
	}
	if next.registry != current.registry || next.config.Pin != current.config.Pin {
		t.Fatal("failed transaction did not preserve last-good runtime")
	}
	if change != (configChange{}) {
		t.Fatalf("failed change = %+v, want zero", change)
	}
}
