package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

const (
	defaultConfigDir  = ".config"
	defaultConfigFile = "config.toml"
	appConfigDir      = "termp"
)

// Display controls which activity fields are shown by default.
type Display struct {
	ToolName     bool `toml:"tool_name"`
	ElapsedTimer bool `toml:"elapsed_timer"`
	SmallImage   bool `toml:"small_image"`
	Collection   bool `toml:"collection"`
	Buttons      bool `toml:"buttons"`
}

// Privacy controls directory display. Directory display is off by default.
type Privacy struct {
	ShowDirectory         bool     `toml:"show_directory"`
	DirectoryAllowlist    []string `toml:"directory_allowlist"`
	DirectoryBasenameOnly bool     `toml:"directory_basename_only"`
}

// ToolOverride contains optional per-tool display/privacy settings.
type ToolOverride struct {
	Enabled               *bool             `toml:"enabled"`
	ToolName              *bool             `toml:"tool_name"`
	ElapsedTimer          *bool             `toml:"elapsed_timer"`
	SmallImage            *bool             `toml:"small_image"`
	ShowDirectory         *bool             `toml:"show_directory"`
	DirectoryAllowlist    []string          `toml:"directory_allowlist"`
	DirectoryBasenameOnly *bool             `toml:"directory_basename_only"`
	Buttons               []registry.Button `toml:"buttons"`
	buttonsSet            bool
	allowlistSet          bool
}

// Config is the loaded TOML configuration plus load metadata.
type Config struct {
	Enabled              bool                    `toml:"enabled"`
	ScanInterval         string                  `toml:"scan_interval"`
	Pin                  string                  `toml:"pin"`
	HeadlinerIdleTimeout string                  `toml:"headliner_idle_timeout"`
	ActivitySwitching    bool                    `toml:"activity_switching"`
	Display              Display                 `toml:"display"`
	Privacy              Privacy                 `toml:"privacy"`
	Tools                map[string]ToolOverride `toml:"tools"`
	CustomTools          []registry.CustomTool   `toml:"custom_tools"`
	Path                 string                  `toml:"-"`
	Warnings             []string                `toml:"-"`
}

type fileConfig struct {
	Enabled              bool                    `toml:"enabled"`
	ScanInterval         string                  `toml:"scan_interval"`
	Pin                  string                  `toml:"pin"`
	HeadlinerIdleTimeout string                  `toml:"headliner_idle_timeout"`
	ActivitySwitching    bool                    `toml:"activity_switching"`
	Display              Display                 `toml:"display"`
	Privacy              Privacy                 `toml:"privacy"`
	Tools                map[string]ToolOverride `toml:"tools"`
	CustomTools          []customTool            `toml:"custom_tools"`
}

type customTool struct {
	ID          string            `toml:"id"`
	DisplayName string            `toml:"display_name"`
	Match       customMatch       `toml:"match"`
	ImageKey    string            `toml:"image_key"`
	ImageURL    string            `toml:"image_url"`
	Priority    int               `toml:"priority"`
	Buttons     []registry.Button `toml:"buttons"`
}

type customMatch struct {
	Name  string `toml:"name"`
	Regex string `toml:"regex"`
}

// ResolvedTool is the effective config for one detected tool.
type ResolvedTool struct {
	Enabled               bool
	ToolName              bool
	ElapsedTimer          bool
	SmallImage            bool
	ButtonsEnabled        bool
	ShowDirectory         bool
	DirectoryAllowlist    []string
	DirectoryBasenameOnly bool
	Buttons               []registry.Button
}

// Default returns the privacy-first config defaults.
func Default() Config {
	return Config{
		Enabled:              true,
		ScanInterval:         "3s",
		HeadlinerIdleTimeout: "60s",
		ActivitySwitching:    true,
		Display: Display{
			ToolName:     true,
			ElapsedTimer: true,
			SmallImage:   true,
			Collection:   true,
			Buttons:      true,
		},
		Privacy: Privacy{
			ShowDirectory:         false,
			DirectoryBasenameOnly: true,
		},
		Tools: make(map[string]ToolOverride),
	}
}

// DefaultPath returns the XDG-aware config path.
func DefaultPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, appConfigDir, defaultConfigFile)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(appConfigDir, defaultConfigFile)
	}
	return filepath.Join(home, defaultConfigDir, appConfigDir, defaultConfigFile)
}

// Load reads the default config path. A missing file returns defaults.
func Load() (Config, error) {
	return LoadPath(DefaultPath())
}

// Save writes cfg to path as TOML. The write is atomic within the destination directory.
func Save(cfg Config, path string) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(saveDocument(cfg)); err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

// LoadPath reads a TOML config from path. A missing file returns defaults.
func LoadPath(path string) (Config, error) {
	cfg := Default()
	cfg.Path = path

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cloneConfig(cfg), nil
	}
	if err != nil {
		return cfg, err
	}

	raw := fileConfig{
		Enabled:              cfg.Enabled,
		ScanInterval:         cfg.ScanInterval,
		Pin:                  cfg.Pin,
		HeadlinerIdleTimeout: cfg.HeadlinerIdleTimeout,
		ActivitySwitching:    cfg.ActivitySwitching,
		Display:              cfg.Display,
		Privacy:              cfg.Privacy,
		Tools:                cfg.Tools,
	}
	meta, err := toml.Decode(string(data), &raw)
	if err != nil {
		return DefaultWithPath(path), err
	}
	cfg.Enabled = raw.Enabled
	cfg.ScanInterval = raw.ScanInterval
	cfg.Pin = raw.Pin
	cfg.HeadlinerIdleTimeout = raw.HeadlinerIdleTimeout
	cfg.ActivitySwitching = raw.ActivitySwitching
	cfg.Display = raw.Display
	cfg.Privacy = raw.Privacy
	cfg.Tools = raw.Tools
	cfg.CustomTools = convertCustomTools(raw.CustomTools)
	cfg.Path = path
	cfg.Warnings = unknownKeyWarnings(meta.Undecoded())
	markDefinedFields(&cfg, meta)
	if err := validate(&cfg); err != nil {
		return DefaultWithPath(path), err
	}
	return cloneConfig(cfg), nil
}

func convertCustomTools(raw []customTool) []registry.CustomTool {
	if len(raw) == 0 {
		return nil
	}
	out := make([]registry.CustomTool, 0, len(raw))
	for _, tool := range raw {
		out = append(out, registry.CustomTool{
			ID:          tool.ID,
			DisplayName: tool.DisplayName,
			Match: registry.CustomMatch{
				Name:  tool.Match.Name,
				Regex: tool.Match.Regex,
			},
			ImageKey: tool.ImageKey,
			ImageURL: tool.ImageURL,
			Priority: tool.Priority,
			Buttons:  append([]registry.Button(nil), tool.Buttons...),
		})
	}
	return out
}

func DefaultWithPath(path string) Config {
	cfg := Default()
	cfg.Path = path
	return cfg
}

// ScanIntervalDuration parses ScanInterval, falling back to 3s for invalid values.
func (c Config) ScanIntervalDuration() time.Duration {
	d, err := time.ParseDuration(c.ScanInterval)
	if err != nil || d <= 0 {
		return 3 * time.Second
	}
	return d
}

// HeadlinerIdleTimeoutDuration parses HeadlinerIdleTimeout, falling back to 60s for invalid values.
func (c Config) HeadlinerIdleTimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.HeadlinerIdleTimeout)
	if err != nil || d <= 0 {
		return time.Minute
	}
	return d
}

// Resolve computes the effective settings for a detected tool.
func (c Config) Resolve(tool registry.Tool) ResolvedTool {
	resolved := ResolvedTool{
		Enabled:               c.Enabled,
		ToolName:              c.Display.ToolName,
		ElapsedTimer:          c.Display.ElapsedTimer,
		SmallImage:            c.Display.SmallImage,
		ButtonsEnabled:        c.Display.Buttons,
		ShowDirectory:         c.Privacy.ShowDirectory,
		DirectoryAllowlist:    append([]string(nil), c.Privacy.DirectoryAllowlist...),
		DirectoryBasenameOnly: c.Privacy.DirectoryBasenameOnly,
		Buttons:               append([]registry.Button(nil), tool.Buttons...),
	}
	if !c.Enabled {
		resolved.Enabled = false
		return resolved
	}

	override, ok := c.Tools[tool.ID]
	if !ok {
		return resolved
	}
	if override.Enabled != nil {
		resolved.Enabled = *override.Enabled
		if !resolved.Enabled {
			return resolved
		}
	}
	if override.ToolName != nil {
		resolved.ToolName = *override.ToolName
	}
	if override.ElapsedTimer != nil {
		resolved.ElapsedTimer = *override.ElapsedTimer
	}
	if override.SmallImage != nil {
		resolved.SmallImage = *override.SmallImage
	}
	if override.ShowDirectory != nil {
		resolved.ShowDirectory = *override.ShowDirectory
	}
	if override.allowlistSet {
		resolved.DirectoryAllowlist = append([]string(nil), override.DirectoryAllowlist...)
	}
	if override.DirectoryBasenameOnly != nil {
		resolved.DirectoryBasenameOnly = *override.DirectoryBasenameOnly
	}
	if override.buttonsSet {
		resolved.Buttons = append([]registry.Button(nil), override.Buttons...)
	}
	return resolved
}

// DirectoryAllowed reports whether path may be displayed under the effective privacy rules.
func (r ResolvedTool) DirectoryAllowed(path string) bool {
	if !r.Enabled || !r.ShowDirectory || path == "" {
		return false
	}
	if len(r.DirectoryAllowlist) == 0 {
		return true
	}
	cleanPath := filepath.Clean(path)
	for _, allowed := range r.DirectoryAllowlist {
		if pathHasPrefix(cleanPath, allowed) {
			return true
		}
	}
	return false
}

// DisplayDirectory returns the directory string allowed for presence state.
func (r ResolvedTool) DisplayDirectory(path string) (string, bool) {
	if !r.DirectoryAllowed(path) {
		return "", false
	}
	if r.DirectoryBasenameOnly {
		return filepath.Base(filepath.Clean(path)), true
	}
	return filepath.Clean(path), true
}

func validate(cfg *Config) error {
	cfg.Privacy.DirectoryAllowlist = expandPaths(cfg.Privacy.DirectoryAllowlist)
	for id, override := range cfg.Tools {
		override.DirectoryAllowlist = expandPaths(override.DirectoryAllowlist)
		cfg.Tools[id] = override
	}

	for i, customTool := range cfg.CustomTools {
		if strings.TrimSpace(customTool.ID) == "" {
			return fmt.Errorf("custom_tools[%d]: id is required", i)
		}
		if strings.TrimSpace(customTool.DisplayName) == "" {
			return fmt.Errorf("custom_tools[%d]: display_name is required", i)
		}
		if strings.TrimSpace(customTool.Match.Name) == "" && strings.TrimSpace(customTool.Match.Regex) == "" {
			return fmt.Errorf("custom_tools[%d]: match is required", i)
		}
		if strings.TrimSpace(customTool.ImageKey) == "" && strings.TrimSpace(customTool.ImageURL) == "" {
			return fmt.Errorf("custom_tools[%d]: image_key or image_url is required", i)
		}
	}
	return nil
}

func saveDocument(cfg Config) map[string]any {
	doc := map[string]any{
		"enabled":                cfg.Enabled,
		"scan_interval":          cfg.ScanInterval,
		"pin":                    cfg.Pin,
		"headliner_idle_timeout": cfg.HeadlinerIdleTimeout,
		"activity_switching":     cfg.ActivitySwitching,
		"display":                saveDisplay(cfg.Display),
		"privacy":                savePrivacy(cfg.Privacy),
		"tools":                  saveTools(cfg.Tools),
		"custom_tools":           saveCustomTools(cfg.CustomTools),
	}
	return doc
}

func saveDisplay(display Display) map[string]any {
	return map[string]any{
		"tool_name":     display.ToolName,
		"elapsed_timer": display.ElapsedTimer,
		"small_image":   display.SmallImage,
		"collection":    display.Collection,
		"buttons":       display.Buttons,
	}
}

func savePrivacy(privacy Privacy) map[string]any {
	return map[string]any{
		"show_directory":          privacy.ShowDirectory,
		"directory_allowlist":     append([]string(nil), privacy.DirectoryAllowlist...),
		"directory_basename_only": privacy.DirectoryBasenameOnly,
	}
}

func saveTools(tools map[string]ToolOverride) map[string]any {
	out := make(map[string]any, len(tools))
	for id, override := range tools {
		entry := make(map[string]any)
		if override.Enabled != nil {
			entry["enabled"] = *override.Enabled
		}
		if override.ToolName != nil {
			entry["tool_name"] = *override.ToolName
		}
		if override.ElapsedTimer != nil {
			entry["elapsed_timer"] = *override.ElapsedTimer
		}
		if override.SmallImage != nil {
			entry["small_image"] = *override.SmallImage
		}
		if override.ShowDirectory != nil {
			entry["show_directory"] = *override.ShowDirectory
		}
		if override.allowlistSet || len(override.DirectoryAllowlist) > 0 {
			entry["directory_allowlist"] = append([]string(nil), override.DirectoryAllowlist...)
		}
		if override.DirectoryBasenameOnly != nil {
			entry["directory_basename_only"] = *override.DirectoryBasenameOnly
		}
		if override.buttonsSet || len(override.Buttons) > 0 {
			entry["buttons"] = saveButtons(override.Buttons)
		}
		out[id] = entry
	}
	return out
}

func saveCustomTools(tools []registry.CustomTool) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		entry := map[string]any{
			"id":           tool.ID,
			"display_name": tool.DisplayName,
			"match": map[string]any{
				"name":  tool.Match.Name,
				"regex": tool.Match.Regex,
			},
			"image_key": tool.ImageKey,
			"image_url": tool.ImageURL,
			"priority":  tool.Priority,
			"buttons":   saveButtons(tool.Buttons),
		}
		out = append(out, entry)
	}
	return out
}

func saveButtons(buttons []registry.Button) []map[string]string {
	out := make([]map[string]string, 0, len(buttons))
	for _, button := range buttons {
		out = append(out, map[string]string{
			"label": button.Label,
			"url":   button.URL,
		})
	}
	return out
}

func unknownKeyWarnings(keys []toml.Key) []string {
	if len(keys) == 0 {
		return nil
	}
	warnings := make([]string, 0, len(keys))
	for _, key := range keys {
		warnings = append(warnings, "unknown config key: "+key.String())
	}
	return warnings
}

func markDefinedFields(cfg *Config, meta toml.MetaData) {
	for id, override := range cfg.Tools {
		if meta.IsDefined("tools", id, "buttons") {
			override.buttonsSet = true
		}
		if meta.IsDefined("tools", id, "directory_allowlist") {
			override.allowlistSet = true
		}
		cfg.Tools[id] = override
	}
}

func expandPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if expanded := expandHome(path); expanded != "" {
			out = append(out, filepath.Clean(expanded))
		}
	}
	return out
}

func expandHome(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			if path == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func pathHasPrefix(path, prefix string) bool {
	prefix = filepath.Clean(prefix)
	if path == prefix {
		return true
	}
	rel, err := filepath.Rel(prefix, path)
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func cloneConfig(cfg Config) Config {
	cfg.Privacy.DirectoryAllowlist = append([]string(nil), cfg.Privacy.DirectoryAllowlist...)
	cfg.Warnings = append([]string(nil), cfg.Warnings...)
	if cfg.Tools != nil {
		tools := make(map[string]ToolOverride, len(cfg.Tools))
		for id, override := range cfg.Tools {
			override.DirectoryAllowlist = append([]string(nil), override.DirectoryAllowlist...)
			override.Buttons = append([]registry.Button(nil), override.Buttons...)
			tools[id] = override
		}
		cfg.Tools = tools
	}
	cfg.CustomTools = append([]registry.CustomTool(nil), cfg.CustomTools...)
	for i := range cfg.CustomTools {
		cfg.CustomTools[i].Buttons = append([]registry.Button(nil), cfg.CustomTools[i].Buttons...)
	}
	return cfg
}
