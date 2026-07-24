package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

const (
	defaultConfigDir  = ".config"
	defaultConfigFile = "config.toml"
	appConfigDir      = "termp"
	// DefaultFeedbackURL deep-links to the live feedback form via the page's only stable anchor, the Turnstile container.
	DefaultFeedbackURL = "https://termp.polter.sh/#feedback-turnstile"
)

// DefaultAccentColor preserves the original adaptive purple TUI palette.
const DefaultAccentColor = "purple"

var hexColorPattern = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// UI controls terminal interface appearance.
type UI struct {
	AccentColor string `toml:"accent_color"`
}

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

// CTA controls the prototype call-to-action presence button.
type CTA struct {
	Enabled bool   `toml:"enabled"`
	Label   string `toml:"label"`
	URL     string `toml:"url"`
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
	StartAtLogin         bool                    `toml:"start_at_login"`
	UpdateCheck          bool                    `toml:"update_check"`
	AutoUpdate           bool                    `toml:"auto_update"`
	ScanInterval         string                  `toml:"scan_interval"`
	IdleClearTimeout     string                  `toml:"idle_clear_timeout"`
	Pin                  string                  `toml:"pin"`
	HeadlinerIdleTimeout string                  `toml:"headliner_idle_timeout"`
	ActivitySwitching    bool                    `toml:"activity_switching"`
	DetailsFormat        string                  `toml:"details_format"`
	FeedbackURL          string                  `toml:"feedback_url"`
	UI                   UI                      `toml:"ui"`
	Display              Display                 `toml:"display"`
	Privacy              Privacy                 `toml:"privacy"`
	CTA                  CTA                     `toml:"cta"`
	Tools                map[string]ToolOverride `toml:"tools"`
	CustomTools          []registry.CustomTool   `toml:"custom_tools"`
	Path                 string                  `toml:"-"`
	Warnings             []string                `toml:"-"`
}

type pathResolver struct {
	goos          string
	getenv        func(string) string
	userHomeDir   func() (string, error)
	userConfigDir func() (string, error)
	stat          func(string) (os.FileInfo, error)
	copyFile      func(string, string) error
}

type fileConfig struct {
	Enabled              bool                    `toml:"enabled"`
	StartAtLogin         bool                    `toml:"start_at_login"`
	UpdateCheck          bool                    `toml:"update_check"`
	AutoUpdate           bool                    `toml:"auto_update"`
	ScanInterval         string                  `toml:"scan_interval"`
	IdleClearTimeout     string                  `toml:"idle_clear_timeout"`
	Pin                  string                  `toml:"pin"`
	HeadlinerIdleTimeout string                  `toml:"headliner_idle_timeout"`
	ActivitySwitching    bool                    `toml:"activity_switching"`
	DetailsFormat        string                  `toml:"details_format"`
	FeedbackURL          string                  `toml:"feedback_url"`
	UI                   UI                      `toml:"ui"`
	Display              Display                 `toml:"display"`
	Privacy              Privacy                 `toml:"privacy"`
	CTA                  CTA                     `toml:"cta"`
	Tools                map[string]ToolOverride `toml:"tools"`
	CustomTools          []customTool            `toml:"custom_tools"`
}

type customTool struct {
	ID          string      `toml:"id"`
	DisplayName string      `toml:"display_name"`
	Match       customMatch `toml:"match"`
	Exclude     string      `toml:"exclude"`
	ImageKey    string      `toml:"image_key"`
	ImageURL    string      `toml:"image_url"`
	IconSlug    string      `toml:"icon_slug"`
	// IconSource optionally selects "simpleicons" or "lobehub"; empty defaults in registry.
	IconSource string            `toml:"icon_source"`
	Priority   int               `toml:"priority"`
	Buttons    []registry.Button `toml:"buttons"`
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
		StartAtLogin:         true,
		UpdateCheck:          true,
		AutoUpdate:           false,
		ScanInterval:         "3s",
		IdleClearTimeout:     "20m",
		HeadlinerIdleTimeout: "60s",
		ActivitySwitching:    true,
		DetailsFormat:        "Using {tool}",
		FeedbackURL:          DefaultFeedbackURL,
		UI: UI{
			AccentColor: DefaultAccentColor,
		},
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
		CTA: CTA{
			Enabled: true,
			Label:   "What is this?",
			URL:     "https://termp.polter.sh/",
		},
		Tools: make(map[string]ToolOverride),
	}
}

// DefaultPath returns the XDG-aware config path.
func DefaultPath() string {
	return defaultPathFor(pathResolver{
		goos:          runtime.GOOS,
		getenv:        os.Getenv,
		userHomeDir:   os.UserHomeDir,
		userConfigDir: os.UserConfigDir,
		stat:          os.Stat,
		copyFile:      copyFileBestEffort,
	})
}

func defaultPathFor(resolver pathResolver) string {
	if resolver.goos == "windows" {
		return defaultWindowsPathFor(resolver)
	}
	if xdg := resolver.getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, appConfigDir, defaultConfigFile)
	}
	home, err := resolver.userHomeDir()
	if err != nil || home == "" {
		return filepath.Join(appConfigDir, defaultConfigFile)
	}
	return filepath.Join(home, defaultConfigDir, appConfigDir, defaultConfigFile)
}

func defaultWindowsPathFor(resolver pathResolver) string {
	native := filepath.Join(appConfigDir, defaultConfigFile)
	if configDir, err := resolver.userConfigDir(); err == nil && configDir != "" {
		native = filepath.Join(configDir, appConfigDir, defaultConfigFile)
	}
	home, err := resolver.userHomeDir()
	if err != nil || home == "" {
		return native
	}
	legacy := filepath.Join(home, defaultConfigDir, appConfigDir, defaultConfigFile)
	return migrateLegacyPath(native, legacy, resolver)
}

func migrateLegacyPath(native, legacy string, resolver pathResolver) string {
	// Prefer the native Windows path, but copy an existing legacy file forward
	// before using it; if migration fails, keep reading the legacy file.
	if _, err := resolver.stat(native); err == nil {
		return native
	}
	if _, err := resolver.stat(legacy); err != nil {
		return native
	}
	if err := resolver.copyFile(legacy, native); err != nil {
		return legacy
	}
	return native
}

func copyFileBestEffort(from, to string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	dir := filepath.Dir(to)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(to, data, 0o644)
}

// AnnotatedSample returns a fully-commented config file containing every default key.
func AnnotatedSample() string {
	cfg := Default()
	return fmt.Sprintf(`# termp config
# This file is hot-reloaded by the daemon.

enabled = %t                # Master switch. When false, no Discord presence is shown.
start_at_login = %t         # Start termp automatically when you log in.
update_check = %t           # Check GitHub Releases for updates; NO_UPDATE_CHECK also disables this.
auto_update = %t            # Silently install newer releases on daemon start. Off by default.
scan_interval = %q        # How often termp scans local processes.
idle_clear_timeout = %q       # Clear presence after this much CPU-idle time; "0" disables idle clear.
pin = %q                    # Prefer this tool ID as the headliner when it is running.
headliner_idle_timeout = %q # How long the current headliner must be idle before switching.
activity_switching = %t     # Let recent activity switch the headliner after the idle timeout.
details_format = %q # Details text; {tool} expands to the display name.
feedback_url = %q # URL opened by the settings feedback action.

[ui]
accent_color = %q          # TUI accent: purple, blue, green, orange, pink, red, or #RRGGBB.

[display]
tool_name = %t              # Show the tool display name in Discord details.
elapsed_timer = %t          # Show Discord's elapsed timer for the session.
small_image = %t            # Show an optional small image for another running tool.
collection = %t             # Show other running tools in the state line when no directory is shown.
buttons = %t                # Show Discord activity buttons when available.

[privacy]
show_directory = %t         # Show the working directory on Discord. Off by default.
directory_allowlist = []    # Optional path prefixes allowed when show_directory is true.
directory_basename_only = %t # Show only the final directory name instead of the full path.

[cta]
enabled = %t                # Show the "What is this?" button when fewer than two tool buttons exist.
label = %q       # Label for the CTA button.
url = %q       # URL for the CTA button.

# [[custom_tools]]
# id = "lazygit"            # Stable tool ID.
# display_name = "lazygit"  # Name shown in Discord.
# match = { name = "lazygit" } # Match by executable name; regex is also supported.
# exclude = "--helper"       # Optional regex rejecting helper processes by path or command line.
# image_url = "https://example.com/lazygit.png" # Logo URL used by Discord.
# priority = 10              # Higher priority wins when multiple tools match.
`, cfg.Enabled, cfg.StartAtLogin, cfg.UpdateCheck, cfg.AutoUpdate, cfg.ScanInterval, cfg.IdleClearTimeout, cfg.Pin, cfg.HeadlinerIdleTimeout,
		cfg.ActivitySwitching, cfg.DetailsFormat, cfg.FeedbackURL, cfg.UI.AccentColor,
		cfg.Display.ToolName, cfg.Display.ElapsedTimer, cfg.Display.SmallImage, cfg.Display.Collection, cfg.Display.Buttons,
		cfg.Privacy.ShowDirectory, cfg.Privacy.DirectoryBasenameOnly,
		cfg.CTA.Enabled, cfg.CTA.Label, cfg.CTA.URL)
}

// InitFile writes the annotated default config, refusing to overwrite unless force is true.
func InitFile(path string, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("config already exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, []byte(AnnotatedSample()), 0o644)
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
		StartAtLogin:         cfg.StartAtLogin,
		UpdateCheck:          cfg.UpdateCheck,
		AutoUpdate:           cfg.AutoUpdate,
		ScanInterval:         cfg.ScanInterval,
		IdleClearTimeout:     cfg.IdleClearTimeout,
		Pin:                  cfg.Pin,
		HeadlinerIdleTimeout: cfg.HeadlinerIdleTimeout,
		ActivitySwitching:    cfg.ActivitySwitching,
		DetailsFormat:        cfg.DetailsFormat,
		FeedbackURL:          cfg.FeedbackURL,
		UI:                   cfg.UI,
		Display:              cfg.Display,
		Privacy:              cfg.Privacy,
		CTA:                  cfg.CTA,
		Tools:                cfg.Tools,
	}
	meta, err := toml.Decode(string(data), &raw)
	if err != nil {
		return DefaultWithPath(path), err
	}
	cfg.Enabled = raw.Enabled
	cfg.StartAtLogin = raw.StartAtLogin
	cfg.UpdateCheck = raw.UpdateCheck
	cfg.AutoUpdate = raw.AutoUpdate
	cfg.ScanInterval = raw.ScanInterval
	cfg.IdleClearTimeout = raw.IdleClearTimeout
	cfg.Pin = raw.Pin
	cfg.HeadlinerIdleTimeout = raw.HeadlinerIdleTimeout
	cfg.ActivitySwitching = raw.ActivitySwitching
	cfg.DetailsFormat = raw.DetailsFormat
	cfg.FeedbackURL = raw.FeedbackURL
	cfg.UI = raw.UI
	cfg.Display = raw.Display
	cfg.Privacy = raw.Privacy
	cfg.CTA = raw.CTA
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
			Exclude:    tool.Exclude,
			ImageKey:   tool.ImageKey,
			ImageURL:   tool.ImageURL,
			IconSlug:   tool.IconSlug,
			IconSource: tool.IconSource,
			Priority:   tool.Priority,
			Buttons:    append([]registry.Button(nil), tool.Buttons...),
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

// IdleClearTimeoutDuration parses IdleClearTimeout; invalid or non-positive values disable idle clear.
func (c Config) IdleClearTimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.IdleClearTimeout)
	if err != nil || d <= 0 {
		return 0
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
	cleanPath := canonicalPrivacyPath(path)
	for _, allowed := range r.DirectoryAllowlist {
		if pathHasPrefix(cleanPath, canonicalPrivacyPath(allowed)) {
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
	if !validAccentColor(cfg.UI.AccentColor) {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf(
			"invalid config value: ui.accent_color %q; using %q",
			cfg.UI.AccentColor,
			DefaultAccentColor,
		))
		cfg.UI.AccentColor = DefaultAccentColor
	}

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
		if strings.TrimSpace(customTool.ImageKey) == "" && strings.TrimSpace(customTool.ImageURL) == "" && strings.TrimSpace(customTool.IconSlug) == "" {
			return fmt.Errorf("custom_tools[%d]: image_key, image_url, or icon_slug is required", i)
		}
	}
	return nil
}

func saveDocument(cfg Config) map[string]any {
	doc := map[string]any{
		"enabled":                cfg.Enabled,
		"start_at_login":         cfg.StartAtLogin,
		"update_check":           cfg.UpdateCheck,
		"auto_update":            cfg.AutoUpdate,
		"scan_interval":          cfg.ScanInterval,
		"idle_clear_timeout":     cfg.IdleClearTimeout,
		"pin":                    cfg.Pin,
		"headliner_idle_timeout": cfg.HeadlinerIdleTimeout,
		"activity_switching":     cfg.ActivitySwitching,
		"details_format":         cfg.DetailsFormat,
		"feedback_url":           cfg.FeedbackURL,
		"ui":                     saveUI(cfg.UI),
		"display":                saveDisplay(cfg.Display),
		"privacy":                savePrivacy(cfg.Privacy),
		"cta":                    saveCTA(cfg.CTA),
		"tools":                  saveTools(cfg.Tools),
		"custom_tools":           saveCustomTools(cfg.CustomTools),
	}
	return doc
}

func saveUI(ui UI) map[string]any {
	return map[string]any{
		"accent_color": ui.AccentColor,
	}
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

func saveCTA(cta CTA) map[string]any {
	return map[string]any{
		"enabled": cta.Enabled,
		"label":   cta.Label,
		"url":     cta.URL,
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
			"exclude":     tool.Exclude,
			"image_key":   tool.ImageKey,
			"image_url":   tool.ImageURL,
			"icon_slug":   tool.IconSlug,
			"icon_source": tool.IconSource,
			"priority":    tool.Priority,
			"buttons":     saveButtons(tool.Buttons),
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

func validAccentColor(value string) bool {
	switch strings.ToLower(value) {
	case "", "purple", "blue", "green", "orange", "pink", "red":
		return true
	default:
		return hexColorPattern.MatchString(value)
	}
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

func canonicalPrivacyPath(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS != "windows" {
		return path
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = filepath.Clean(resolved)
	}
	return strings.ToLower(path)
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
