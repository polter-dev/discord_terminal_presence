package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

func withConfigHome(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	if err := os.MkdirAll(os.Getenv("HOME"), 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(os.Getenv("XDG_CONFIG_HOME"), appConfigDir, defaultConfigFile)
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func boolPtr(v bool) *bool {
	return &v
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
	if !cfg.Enabled || cfg.ScanInterval != "3s" {
		t.Fatalf("unexpected global defaults: %#v", cfg)
	}
	if cfg.IdleClearTimeout != "0" || cfg.DetailsFormat != "Using {tool}" {
		t.Fatalf("unexpected polish defaults: %#v", cfg)
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
	if !cfg.CTA.Enabled || cfg.CTA.Label != "Get Termp (WIP)" || cfg.CTA.URL != "https://termp.example" {
		t.Fatalf("unexpected CTA defaults: %#v", cfg.CTA)
	}
}

func TestLoadValidConfig(t *testing.T) {
	path := withConfigHome(t)
	writeConfig(t, path, `
enabled = true
scan_interval = "5s"
idle_clear_timeout = "8h"
pin = "codex-cli"
headliner_idle_timeout = "45s"
activity_switching = false
details_format = "Working in {tool}"

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
	if cfg.ScanInterval != "5s" || cfg.Display.ToolName {
		t.Fatalf("unexpected loaded values: %#v", cfg)
	}
	if cfg.IdleClearTimeout != "8h" || cfg.DetailsFormat != "Working in {tool}" {
		t.Fatalf("unexpected polish values: %#v", cfg)
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
	if got := cfg.Privacy.DirectoryAllowlist[0]; got != filepath.Join(os.Getenv("HOME"), "dev") {
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

func TestDurationFallbacks(t *testing.T) {
	cfg := Default()
	cfg.ScanInterval = "bad"
	cfg.HeadlinerIdleTimeout = "also-bad"

	if got := cfg.ScanIntervalDuration(); got != 3*time.Second {
		t.Fatalf("scan interval duration = %v, want 3s", got)
	}
	if got := cfg.IdleClearTimeoutDuration(); got != 0 {
		t.Fatalf("idle clear timeout = %v, want disabled", got)
	}
	if got := cfg.HeadlinerIdleTimeoutDuration(); got != time.Minute {
		t.Fatalf("headliner idle timeout = %v, want 1m", got)
	}

	cfg.IdleClearTimeout = "10m"
	if got := cfg.IdleClearTimeoutDuration(); got != 10*time.Minute {
		t.Fatalf("idle clear timeout = %v, want 10m", got)
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

	inside := filepath.Join(os.Getenv("HOME"), "work", "client")
	if !resolved.DirectoryAllowed(inside) {
		t.Fatalf("expected %q to be allowed", inside)
	}
	if got, ok := resolved.DisplayDirectory(inside); !ok || got != "client" {
		t.Fatalf("display directory = %q, %t; want client, true", got, ok)
	}
	outside := filepath.Join(os.Getenv("HOME"), "other")
	if resolved.DirectoryAllowed(outside) {
		t.Fatalf("expected %q to be denied", outside)
	}

	defaultResolved := Default().Resolve(registry.Tool{ID: "codex-cli"})
	if defaultResolved.DirectoryAllowed(inside) {
		t.Fatal("default show_directory=false should deny directory display")
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
	if !strings.Contains(err.Error(), "image_key or image_url") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := withConfigHome(t)
	cfg := Default()
	cfg.Enabled = false
	cfg.ScanInterval = "9s"
	cfg.IdleClearTimeout = "6h"
	cfg.Pin = "codex-cli"
	cfg.HeadlinerIdleTimeout = "2m"
	cfg.ActivitySwitching = false
	cfg.DetailsFormat = "{tool} in {dir}"
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
		ImageURL:    "https://example.test/mine.png",
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
	if loaded.Enabled || loaded.ScanInterval != "9s" || loaded.Pin != "codex-cli" {
		t.Fatalf("globals did not round-trip: %#v", loaded)
	}
	if loaded.IdleClearTimeout != "6h" || loaded.DetailsFormat != "{tool} in {dir}" {
		t.Fatalf("polish settings did not round-trip: %#v", loaded)
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
	wantAllow := filepath.Join(os.Getenv("HOME"), "dev")
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
}
