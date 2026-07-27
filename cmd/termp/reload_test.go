package main

import (
	"strings"
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

func TestDisabledFeaturedToolFallsBackBeforeSelection(t *testing.T) {
	cfg := config.Default()
	cfg.Pin = "claude-code"
	cfg.Privacy.ShowDirectory = true
	current, err := newDetectionRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	selector := detector.NewSelector(current.registry, current.detectorConfig, nil)
	processes := []detector.Process{
		{Pid: 1, Name: "claude", Cwd: "/work/claude-project"},
		{Pid: 2, Name: "codex", Cwd: "/work/codex-project"},
	}
	if initial := selector.Select(processes); initial.None || initial.Tool.ID != "claude-code" {
		t.Fatalf("initial selection = %#v, want pinned claude-code", initial)
	}

	disabled := false
	disabledCfg := cfg
	disabledCfg.Tools = map[string]config.ToolOverride{
		"claude-code": {Enabled: &disabled},
	}
	next, change, err := applyConfigChange(current, disabledCfg)
	if err != nil {
		t.Fatal(err)
	}
	if !change.detector || change.registry {
		t.Fatalf("disable change = %+v, want detector-only reload", change)
	}
	selector.Reconfigure(next.registry, next.detectorConfig)
	fallback := selector.Select(processes)
	if fallback.None || fallback.Tool.ID != "codex-cli" {
		t.Fatalf("fallback selection = %#v, want codex-cli", fallback)
	}
	if fallback.Cwd != "/work/codex-project" {
		t.Fatalf("fallback cwd = %q, want codex process cwd", fallback.Cwd)
	}
	if fallback.StartedAt.IsZero() {
		t.Fatal("fallback start time is zero")
	}
	activity := buildActivity(disabledCfg, fallback, "Fixed fallback")
	if activity == nil {
		t.Fatal("fallback activity = nil, want codex activity")
	}
	if activity.Name != "Codex CLI" || !strings.Contains(activity.Details, "codex-project") {
		t.Fatalf("fallback activity = %#v, want Codex CLI in codex-project", activity)
	}
	if activity.StartTimestamp == nil || !activity.StartTimestamp.Equal(fallback.StartedAt) {
		t.Fatalf("fallback timestamp = %v, want %v", activity.StartTimestamp, fallback.StartedAt)
	}

	reenabled := true
	reenabledCfg := cfg
	reenabledCfg.Tools = map[string]config.ToolOverride{
		"claude-code": {Enabled: &reenabled},
	}
	restored, change, err := applyConfigChange(next, reenabledCfg)
	if err != nil {
		t.Fatal(err)
	}
	if !change.detector || change.registry {
		t.Fatalf("re-enable change = %+v, want detector-only reload", change)
	}
	selector.Reconfigure(restored.registry, restored.detectorConfig)
	if selection := selector.Select(processes); selection.None || selection.Tool.ID != "claude-code" {
		t.Fatalf("re-enabled selection = %#v, want pinned claude-code", selection)
	}
}

func TestBuildActivityGlobalDisabledClearsPresence(t *testing.T) {
	cfg := config.Default()
	cfg.Enabled = false
	detection := detector.Detection{
		Tool:      registry.Tool{ID: "claude-code", DisplayName: "Claude Code"},
		Cwd:       "/work/claude-project",
		StartedAt: time.Now(),
	}
	if activity := buildActivity(cfg, detection, "Fixed fallback"); activity != nil {
		t.Fatalf("activity = %#v, want nil when globally disabled", activity)
	}
}

func TestAllRunningToolsDisabledClearsPresence(t *testing.T) {
	cfg := config.Default()
	disabled := false
	cfg.Tools = map[string]config.ToolOverride{
		"claude-code": {Enabled: &disabled},
		"codex-cli":   {Enabled: &disabled},
	}
	runtime, err := newDetectionRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	detection := detector.NewSelector(runtime.registry, runtime.detectorConfig, nil).Select([]detector.Process{
		{Pid: 1, Name: "claude", Cwd: "/work/claude-project"},
		{Pid: 2, Name: "codex", Cwd: "/work/codex-project"},
	})
	if !detection.None {
		t.Fatalf("selection = %#v, want none when every running tool is disabled", detection)
	}
	if activity := buildActivity(cfg, detection, "Fixed fallback"); activity != nil {
		t.Fatalf("activity = %#v, want nil", activity)
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
