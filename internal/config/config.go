package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/BurntSushi/toml"
	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

const (
	defaultConfigDir  = ".config"
	defaultConfigFile = "config.toml"
	appConfigDir      = "termp"
	maxConfigFileSize = 1 << 20
	// DefaultFeedbackURL deep-links to the live feedback form via the page's only stable anchor, the Turnstile container.
	DefaultFeedbackURL = "https://termp.polter.sh/#feedback-turnstile"
)

const BuiltInFallbackMessage = "Working on something"

var defaultFallbackMessages = []string{BuiltInFallbackMessage, "In the terminal"}

// ErrConfigBeingWritten reports that a whole-document load could not obtain
// a settled snapshot without risking a destructive writeback from a guess.
var ErrConfigBeingWritten = errors.New("config is being written right now; try again")

// DefaultAccentColor preserves the original adaptive purple TUI palette.
const DefaultAccentColor = "purple"

const maxActivityTextLength = 128

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
	FallbackMessages     []string                `toml:"fallback_messages"`
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
	FallbackMessages     []string                `toml:"fallback_messages"`
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
		FallbackMessages:     append([]string(nil), defaultFallbackMessages...),
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
	sourceInfo, err := os.Stat(from)
	if err != nil {
		return err
	}
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
	tmp, err := os.CreateTemp(dir, filepath.Base(to)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(sourceInfo.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, to)
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
idle_clear_timeout = %q       # Clear presence after this much terminal inactivity; "0" disables idle clear.
#                              On Windows, a system-wide input timer means injected input can prevent idle clear.
pin = %q                    # Prefer this tool ID as the headliner when it is running.
headliner_idle_timeout = %q # How long the current headliner must be idle before switching.
activity_switching = %t     # Let recent activity switch the headliner after the idle timeout.
details_format = %q # Set a custom template to override the default card cascade; supports {tool} and {dir}.
fallback_messages = %s # Random fallback details; one is chosen once per daemon session.
feedback_url = %q # URL opened by the settings feedback action.

[ui]
accent_color = %q          # TUI accent: purple, blue, green, orange, pink, red, or #RRGGBB.

[display]
tool_name = %t              # Show the tool display name in Discord details.
elapsed_timer = %t          # Show Discord's elapsed timer for the session.
small_image = %t            # Show an optional small image for another running tool.
collection = %t             # Show other running tools on the card.
buttons = %t                # Show Discord activity buttons when available.

[privacy]
show_directory = %t         # Show the working directory on Discord. Off by default.
# directory_allowlist = ["~/projects"] # Optional path prefixes allowed when show_directory is true.
#                              Unset (the default) means no restriction. A key that is present must
#                              have at least one non-blank entry: an empty or blank-only list is rejected.
directory_basename_only = %t # Show only the final directory name; false shows at most the last two segments.

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
		cfg.ActivitySwitching, cfg.DetailsFormat, tomlStringArray(cfg.FallbackMessages), cfg.FeedbackURL, cfg.UI.AccentColor,
		cfg.Display.ToolName, cfg.Display.ElapsedTimer, cfg.Display.SmallImage, cfg.Display.Collection, cfg.Display.Buttons,
		cfg.Privacy.ShowDirectory, cfg.Privacy.DirectoryBasenameOnly,
		cfg.CTA.Enabled, cfg.CTA.Label, cfg.CTA.URL)
}

func tomlStringArray(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, strconv.Quote(value))
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// InitFile writes the annotated default config, refusing to overwrite unless force is true.
func InitFile(path string, force bool) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	var (
		existingMode os.FileMode
		replacing    bool
	)
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refuse to replace non-regular config file %s", path)
		}
		if !force {
			return fmt.Errorf("config already exists: %s", path)
		}
		existingMode = info.Mode().Perm()
		replacing = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect config file %s: %w", path, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary config file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if replacing {
		if err := tmp.Chmod(existingMode); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("set config file permissions: %w", err)
		}
	} else {
		if err := tmp.Chmod(0o600); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("set config file permissions: %w", err)
		}
	}
	if _, err := tmp.WriteString(AnnotatedSample()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config file %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config file %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace config file %s: %w", path, err)
	}
	return nil
}

// Load reads the default config path for a caller that may write the loaded
// document back. A missing file returns defaults immediately; an existing
// blank must remain blank through the enabled-loosening horizon before it is
// accepted as defaults.
func Load() (Config, error) {
	return LoadPath(DefaultPath())
}

// LoadReadOnly reads a settled snapshot of the default config path without
// extending an ambiguous blank through the update horizon.
func LoadReadOnly() (Config, error) {
	return LoadPathReadOnly(DefaultPath())
}

// LoadUnsettled reads the default config path once without settle protection.
// Callers should use Load unless they deliberately need a point-in-time read.
func LoadUnsettled() (Config, error) {
	return LoadPathUnsettled(DefaultPath())
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

// LoadPath reads a settled TOML config for a caller that may write the loaded
// document back. An existing blank file is ambiguous after the normal settle
// budget: it may be a deliberate reset or a writer stalled after truncation.
// This load waits through the enabled-loosening horizon so a completed write
// is returned instead of allowing defaults to overwrite it.
func LoadPath(path string) (Config, error) {
	return loadPathWith(path, snapshotConfigFile, time.Now, time.Sleep)
}

func loadPathWith(
	path string,
	snapshot func(string) fileSnapshot,
	now func() time.Time,
	sleep func(time.Duration),
) (Config, error) {
	snap, err := settledConfigSnapshotForLoadWith(path, snapshot, now, sleep)
	if err != nil {
		return invalidFallbackWithPath(path), err
	}
	return loadSnapshot(path, snap)
}

// LoadPathReadOnly reads a settled TOML config without extending an ambiguous
// blank through the update horizon. It is for callers that cannot write the
// loaded document back over path.
func LoadPathReadOnly(path string) (Config, error) {
	return loadPathReadOnlyWith(path, snapshotConfigFile, time.Now, time.Sleep)
}

func loadPathReadOnlyWith(
	path string,
	snapshot func(string) fileSnapshot,
	now func() time.Time,
	sleep func(time.Duration),
) (Config, error) {
	snap, _ := boundedSettledConfigSnapshotWith(path, snapshot, now, sleep, fileSnapshot{})
	return loadSnapshot(path, snap)
}

// LoadPathUnsettled reads a TOML config from path once without settle
// protection. Callers should use LoadPath unless they deliberately need a
// point-in-time read.
func LoadPathUnsettled(path string) (Config, error) {
	return loadSnapshot(path, snapshotConfigFile(path))
}

// fileSnapshot is a point-in-time read of a config file, used by protected and
// explicitly unsettled loads so both paths decode identically.
type fileSnapshot struct {
	exists bool
	data   []byte
	err    error
}

// snapshotConfigFile reads path once, applying the same existence and size
// rules as LoadPath. It never runs TOML decoding.
func snapshotConfigFile(path string) fileSnapshot {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileSnapshot{exists: false}
	}
	if err != nil {
		return fileSnapshot{exists: true, err: err}
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fileSnapshot{exists: true, err: err}
	}
	if info.Size() > maxConfigFileSize {
		return fileSnapshot{exists: true, err: fmt.Errorf("config file exceeds maximum size of %d bytes", maxConfigFileSize)}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConfigFileSize+1))
	if err != nil {
		return fileSnapshot{exists: true, err: err}
	}
	if len(data) > maxConfigFileSize {
		return fileSnapshot{exists: true, err: fmt.Errorf("config file exceeds maximum size of %d bytes", maxConfigFileSize)}
	}
	return fileSnapshot{exists: true, data: data}
}

// snapshotsEqual reports whether two snapshots observed the same file state.
func snapshotsEqual(a, b fileSnapshot) bool {
	if a.exists != b.exists {
		return false
	}
	if (a.err == nil) != (b.err == nil) {
		return false
	}
	if a.err != nil {
		return a.err.Error() == b.err.Error()
	}
	return bytes.Equal(a.data, b.data)
}

const (
	// reloadSettleInterval is the gap between consecutive reads while waiting
	// for a config file to stop changing. It is small enough that a genuinely
	// finished write (the common case) only adds one interval of latency.
	reloadSettleInterval = 15 * time.Millisecond
	// reloadSettleAttempts bounds how long a reload will wait for a file to
	// stabilize before giving up on this reload attempt (~reloadSettleInterval
	// * reloadSettleAttempts, currently ~300ms). A subsequent fsnotify event
	// once the write finishes triggers another attempt.
	reloadSettleAttempts = 20
	// standaloneLoadSettleTimeout bounds loads that cannot retain last-good
	// state. It permits another chance beyond the ordinary ~300ms settle
	// budget without making read-only commands wait for the 3s loosening
	// horizon when another process keeps rewriting the file.
	standaloneLoadSettleTimeout = 500 * time.Millisecond
)

// provisionalConfigSnapshot reports whether candidate could be an incomplete
// non-atomic rewrite: a previously accepted file that is now missing, an
// existing empty file, or a strict prefix of the last successfully accepted
// file content.
func provisionalConfigSnapshot(candidate, accepted fileSnapshot) bool {
	if !candidate.exists {
		return accepted.exists && accepted.err == nil
	}
	if candidate.err != nil {
		return false
	}
	if len(candidate.data) == 0 {
		return true
	}
	return accepted.exists &&
		accepted.err == nil &&
		len(candidate.data) < len(accepted.data) &&
		bytes.HasPrefix(accepted.data, candidate.data)
}

// settledConfigSnapshot waits for two consecutive reads of path to agree
// before returning. A provisional snapshot must instead remain byte-identical
// across the full settle budget before it can be returned. This defends
// against non-atomic saves that pause after truncation or after writing a
// valid prefix. It reports ok=false when a provisional snapshot changes, or
// when no candidate satisfies its stability requirement within the budget;
// the caller should treat that as "no new information yet" rather than as
// either a success or a failure.
func settledConfigSnapshot(path string, accepted fileSnapshot) (fileSnapshot, bool) {
	return settledConfigSnapshotUntil(path, accepted, time.Time{})
}

// settledConfigSnapshotUntil applies the normal settle rules but also stops at
// deadline when it is non-zero. On an unsettled result it returns the newest
// snapshot observed so read-only callers can make bounded forward progress.
func settledConfigSnapshotUntil(path string, accepted fileSnapshot, deadline time.Time) (fileSnapshot, bool) {
	return settledConfigSnapshotUntilWith(
		path, accepted, deadline, snapshotConfigFile, time.Now, time.Sleep,
	)
}

func settledConfigSnapshotUntilWith(
	path string,
	accepted fileSnapshot,
	deadline time.Time,
	snapshot func(string) fileSnapshot,
	now func() time.Time,
	sleep func(time.Duration),
) (fileSnapshot, bool) {
	candidate := snapshot(path)
	// A caller with no previously accepted file cannot distinguish a genuine
	// first run from an unlink/recreate window. Missing-first-run defaults are
	// intentionally immediate, so do not pay even one poll interval here.
	if !candidate.exists && !accepted.exists {
		return candidate, true
	}
	stableReads := 0
	for i := 0; i < reloadSettleAttempts; i++ {
		delay := reloadSettleInterval
		if !deadline.IsZero() {
			remaining := deadline.Sub(now())
			if remaining <= 0 {
				return candidate, false
			}
			delay = min(delay, remaining)
		}
		sleep(delay)
		next := snapshot(path)
		if snapshotsEqual(candidate, next) {
			stableReads++
			if !provisionalConfigSnapshot(candidate, accepted) || stableReads == reloadSettleAttempts {
				return candidate, true
			}
			continue
		}
		if provisionalConfigSnapshot(candidate, accepted) {
			return next, false
		}
		candidate = next
		stableReads = 0
	}
	return candidate, false
}

// settledConfigSnapshotForRead retries changing snapshots until it has a
// settled result or the standalone bound expires. Unlike Manager.Reload, a
// standalone read has no last-good state to retain, so after the bound it
// returns the most recent snapshot and lets the read-only caller carry on.
func settledConfigSnapshotForRead(path string) fileSnapshot {
	snap, _ := boundedSettledConfigSnapshotWith(
		path, snapshotConfigFile, time.Now, time.Sleep, fileSnapshot{},
	)
	return snap
}

// boundedSettledConfigSnapshotWith settles a snapshot within the standalone
// bound. knownAccepted lets a caller that has already observed this file
// exist during the current load thread that fact through to the settle
// primitive: without it, a missing file always looks like a genuine first
// run (the "no accepted state" fast path), which is exactly how #448 let a
// file deleted mid-horizon be mistaken for one that never existed. Pass a
// zero fileSnapshot{} when there is no such prior knowledge (first read of a
// call, or manager construction), preserving the fast first-run path.
func boundedSettledConfigSnapshotWith(
	path string,
	snapshot func(string) fileSnapshot,
	now func() time.Time,
	sleep func(time.Duration),
	knownAccepted fileSnapshot,
) (fileSnapshot, bool) {
	deadline := now().Add(standaloneLoadSettleTimeout)
	for {
		snap, ok := settledConfigSnapshotUntilWith(
			path, knownAccepted, deadline, snapshot, now, sleep,
		)
		if ok {
			return snap, true
		}
		if !now().Before(deadline) {
			return snap, false
		}
	}
}

// settledConfigSnapshotForLoad extends the normal settled read only for an
// existing blank snapshot. Whole-document editors must not seed a later Save
// with defaults from a truncate-then-write window. A continuously changing
// file fails with ErrConfigBeingWritten rather than seeding a later Save from
// the latest untrusted snapshot.
func settledConfigSnapshotForLoadWith(
	path string,
	snapshot func(string) fileSnapshot,
	now func() time.Time,
	sleep func(time.Duration),
) (fileSnapshot, error) {
	started := now()
	snap, ok := boundedSettledConfigSnapshotWith(path, snapshot, now, sleep, fileSnapshot{})
	if !ok {
		return fileSnapshot{}, ErrConfigBeingWritten
	}
	if !ambiguousBlankConfigSnapshot(snap) {
		return snap, nil
	}

	// The file was observed to exist (blank) at least once during this load.
	// Remember that so a later disappearance cannot take the missing-file
	// fast path meant for a genuine first run (#448 route A).
	existedDuringLoad := fileSnapshot{exists: true}

	for {
		remaining := enabledLooseningHorizon - now().Sub(started)
		if remaining <= 0 {
			return snap, nil
		}
		delay := min(reloadSettleInterval, remaining)
		sleep(delay)

		next := snapshot(path)
		if ambiguousBlankConfigSnapshot(next) {
			snap = next
			continue
		}
		settled, ok := boundedSettledConfigSnapshotWith(path, snapshot, now, sleep, existedDuringLoad)
		if !ok {
			return fileSnapshot{}, ErrConfigBeingWritten
		}
		// Leaving the horizon loop must not hand back a result that is
		// itself still ambiguous: a settle that lands back on blank (#448
		// route B, a brief content flicker inside a stalled truncation), or
		// one that lands on "missing" for a file we know existed moments ago
		// (#448 route A) is not new information. Keep the horizon running
		// instead of returning defaults before it elapses.
		if ambiguousBlankConfigSnapshot(settled) || !settled.exists {
			snap = settled
			continue
		}
		return settled, nil
	}
}

func ambiguousBlankConfigSnapshot(snap fileSnapshot) bool {
	return snap.exists && snap.err == nil && len(bytes.TrimSpace(snap.data)) == 0
}

// loadSnapshot decodes an already-read snapshot into a Config, applying the
// same defaulting, validation, and fail-closed rules as LoadPath.
func loadSnapshot(path string, snap fileSnapshot) (Config, error) {
	cfg, _, err := loadSnapshotWithMetadata(path, snap)
	return cfg, err
}

// loadSnapshotWithMetadata also reports whether enabled was explicitly
// defined. The manager needs that distinction to tell an intentional
// enabled=true from the default produced by an absent key.
func loadSnapshotWithMetadata(path string, snap fileSnapshot) (Config, bool, error) {
	cfg := Default()
	cfg.Path = path

	if snap.err != nil {
		return invalidFallbackWithPath(path), false, snap.err
	}
	if !snap.exists {
		return cloneConfig(cfg), false, nil
	}
	data := snap.data

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
		FallbackMessages:     append([]string(nil), cfg.FallbackMessages...),
		FeedbackURL:          cfg.FeedbackURL,
		UI:                   cfg.UI,
		Display:              cfg.Display,
		Privacy:              cfg.Privacy,
		CTA:                  cfg.CTA,
		Tools:                cfg.Tools,
	}
	meta, err := toml.Decode(string(data), &raw)
	if err != nil {
		return invalidFallbackWithPath(path), false, err
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
	cfg.FallbackMessages = append([]string(nil), raw.FallbackMessages...)
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
	privacyAllowlistDefined := meta.IsDefined("privacy", "directory_allowlist")
	if err := validate(&cfg, privacyAllowlistDefined); err != nil {
		return invalidFallbackWithPath(path), false, err
	}
	return cloneConfig(cfg), meta.IsDefined("enabled"), nil
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

func invalidFallbackWithPath(path string) Config {
	cfg := DefaultWithPath(path)
	cfg.Enabled = false
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
// It does not format path for display.
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

// privacyPosture is a comparable summary of everything that affects what a
// resolved tool may disclose, computed from the SAME Config.Resolve path
// presence mapping uses. It deliberately does not enumerate Config fields by
// name beyond this one place: TestPrivacyPostureCoversAllPrivacyFields and
// TestPrivacyPostureCoversToolOverridePrivacyFields (config_test.go) use
// reflection to fail the build the day a new field is added to Privacy or
// ToolOverride without a conscious decision about whether postureFor below
// needs to grow with it. That is the actual defect this bug family (#410 ->
// #425 -> #434 -> #435 -> #438 -> #440 -> #447) keeps regenerating: a rule
// bound to an enumeration of one.
type privacyPosture struct {
	enabled               bool
	showDirectory         bool
	directoryBasenameOnly bool // true is MORE private: basename only
	allowlistRestricted   bool // true means the allowlist actively narrows disclosure
}

func postureFor(r ResolvedTool) privacyPosture {
	return privacyPosture{
		enabled:               r.Enabled,
		showDirectory:         r.ShowDirectory,
		directoryBasenameOnly: r.DirectoryBasenameOnly,
		allowlistRestricted:   len(r.DirectoryAllowlist) > 0,
	}
}

// postureLoosened reports whether next could disclose something prev did
// not, for one resolved tool.
func postureLoosened(prev, next privacyPosture) bool {
	if !prev.enabled && next.enabled {
		return true
	}
	if !prev.showDirectory && next.showDirectory {
		return true
	}
	if prev.directoryBasenameOnly && !next.directoryBasenameOnly {
		return true
	}
	if prev.allowlistRestricted && !next.allowlistRestricted {
		return true
	}
	return false
}

// unionToolIDs returns every tool ID overridden in either config plus "" for
// the tool-agnostic (global) posture.
func unionToolIDs(a, b Config) []string {
	seen := map[string]struct{}{"": {}}
	for id := range a.Tools {
		seen[id] = struct{}{}
	}
	for id := range b.Tools {
		seen[id] = struct{}{}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	return ids
}

// permissivenessLoosened reports whether next could disclose more than prev
// for any tool (including the tool-agnostic global posture), by resolving
// both configs through Config.Resolve exactly as presence mapping would. A
// new privacy field is covered automatically the moment it participates in
// Resolve/ResolvedTool and postureFor, without a hand-written per-field
// comparison at every call site.
//
// The top-level enabled flag already has its own explicit-vs-defaulted
// tracking (enabledDefined, checked by the caller): an explicit
// `enabled = true` must apply immediately even while other tools resolve as
// newly-enabled purely as a downstream consequence of that same explicit
// flip (every tool is forced disabled while the global flag is off). When
// the global flag itself changes, skipEnabledDimension excludes the enabled
// posture dimension from this generic comparison so that already-vetted,
// explicit transition cannot be redundantly re-gated here; every other
// privacy dimension is still compared normally.
func permissivenessLoosened(prev, next Config, skipEnabledDimension bool) bool {
	// Config.Resolve returns early when the global flag is off, BEFORE
	// applying any per-tool override. If prev were resolved at its real,
	// disabled value, a per-tool tightening (e.g. an explicit
	// `[tools.vim] enabled = false`) would never be visible in prev's
	// posture at all, and neutralizing the enabled dimension on top would
	// then blind the guard completely for the disabled->enabled transition
	// -- exactly the gap an independent reviewer found in round 3. Resolve
	// prev with Enabled forced true so its per-tool overrides are actually
	// applied and can be compared; this only affects the posture snapshot
	// used for comparison here, not the real prev.Enabled used elsewhere.
	prevForPosture := prev
	if skipEnabledDimension {
		prevForPosture.Enabled = true
	}
	for _, id := range unionToolIDs(prev, next) {
		tool := registry.Tool{ID: id}
		p := postureFor(prevForPosture.Resolve(tool))
		n := postureFor(next.Resolve(tool))
		if skipEnabledDimension && id == "" {
			// The tool-agnostic global enabled dimension is already vetted
			// by the caller's own enabledDefined check when the global flag
			// itself changes; neutralize only this one dimension, for only
			// the global posture, so it cannot be redundantly re-gated here.
			// A per-tool id's enabled dimension is NOT neutralized: it is
			// the only place a dropped per-tool opt-out would ever show up
			// while the global flag is simultaneously turning on.
			p.enabled = n.enabled
		}
		if postureLoosened(p, n) {
			return true
		}
	}
	return false
}

func validate(cfg *Config, privacyAllowlistDefined bool) error {
	if err := validateDuration("scan_interval", cfg.ScanInterval, false); err != nil {
		return err
	}
	if err := validateDuration("idle_clear_timeout", cfg.IdleClearTimeout, true); err != nil {
		return err
	}
	if err := validateDuration("headliner_idle_timeout", cfg.HeadlinerIdleTimeout, false); err != nil {
		return err
	}
	if utf8.RuneCountInString(cfg.DetailsFormat) > maxActivityTextLength {
		return fmt.Errorf("details_format must be at most %d characters", maxActivityTextLength)
	}
	if err := ValidateFeedbackURL(cfg.FeedbackURL); err != nil {
		return err
	}
	if cfg.CTA.Enabled {
		if err := registry.ValidateButtons([]registry.Button{{Label: cfg.CTA.Label, URL: cfg.CTA.URL}}); err != nil {
			return fmt.Errorf("cta: %w", err)
		}
	}

	fallbackMessages := cfg.FallbackMessages[:0]
	for i, message := range cfg.FallbackMessages {
		if utf8.RuneCountInString(message) > maxActivityTextLength {
			return fmt.Errorf("fallback_messages[%d] must be at most %d characters", i, maxActivityTextLength)
		}
		if strings.TrimSpace(message) != "" {
			fallbackMessages = append(fallbackMessages, message)
		}
	}
	cfg.FallbackMessages = fallbackMessages
	if len(cfg.FallbackMessages) == 0 {
		cfg.FallbackMessages = append([]string(nil), defaultFallbackMessages...)
	}

	if !validAccentColor(cfg.UI.AccentColor) {
		cfg.Warnings = append(cfg.Warnings, fmt.Sprintf(
			"invalid config value: ui.accent_color %q; using %q",
			cfg.UI.AccentColor,
			DefaultAccentColor,
		))
		cfg.UI.AccentColor = DefaultAccentColor
	}

	// #449: a blank allowlist entry (e.g. `[""]`) used to be silently dropped
	// by expandPaths, leaving a zero-length allowlist that DirectoryAllowed
	// treats as "no restriction configured" -- the exact opposite of what a
	// user writing an allowlist entry intends. No generated config has ever
	// contained a blank entry, so this is always a typo: reject it rather
	// than guessing. An ABSENT key is unaffected and still means "no
	// restriction configured".
	if err := validateAllowlistEntries("privacy", cfg.Privacy.DirectoryAllowlist); err != nil {
		return err
	}
	// A present-but-empty `directory_allowlist = []` at the TOP LEVEL is
	// different: `termp config init`'s own AnnotatedSample emitted exactly
	// this for every existing user before #449, so rejecting it outright
	// would silently disable presence on upgrade for every one of them (the
	// lead caught this in review). Warn instead of erroring: the config
	// still loads and still means "no restriction configured" (identical to
	// an absent key), but the ambiguity is no longer silent. A present-but-
	// empty PER-TOOL override is unaffected by this warning and stays valid
	// with no message: it is the documented way to opt one tool out of a
	// restrictive global allowlist (docs/product/config-schema.md).
	if privacyAllowlistDefined && len(cfg.Privacy.DirectoryAllowlist) == 0 {
		cfg.Warnings = append(cfg.Warnings, "privacy.directory_allowlist is present but empty, which allows every directory; remove the key to make that explicit, or add at least one path to restrict it")
	}
	cfg.Privacy.DirectoryAllowlist = expandPaths(cfg.Privacy.DirectoryAllowlist)
	for id, override := range cfg.Tools {
		// A blank override ID is rejected for the same reason custom tools
		// reject one: it matches no tool. It also matters structurally here,
		// because "" is the sentinel permissivenessLoosened uses for the
		// tool-agnostic global posture, and "" is a legal TOML map key — so
		// a [tools.""] section would collide with that sentinel. Rejecting
		// the key removes the collision by construction rather than relying
		// on the guard to tolerate it. No generated config has ever emitted
		// this section, so there is no upgrade exposure.
		if strings.TrimSpace(id) == "" {
			return fmt.Errorf("tools: override id must not be blank")
		}
		if err := registry.ValidateButtons(override.Buttons); err != nil {
			return fmt.Errorf("tools.%s: %w", id, err)
		}
		if err := validateAllowlistEntries(fmt.Sprintf("tools.%s", id), override.DirectoryAllowlist); err != nil {
			return err
		}
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
		if err := registry.ValidateCustomTool(customTool); err != nil {
			return fmt.Errorf("custom_tools[%d]: %w", i, err)
		}
	}
	return nil
}

// ValidateFeedbackURL bounds feedback targets and restricts them to absolute HTTP(S) URLs.
func ValidateFeedbackURL(value string) error {
	if utf8.RuneCountInString(value) > registry.MaxButtonURLLength {
		return fmt.Errorf("feedback_url must be at most %d characters", registry.MaxButtonURLLength)
	}
	if err := registry.ValidateHTTPURL(value); err != nil {
		return fmt.Errorf("feedback_url must be a valid absolute http/https URL")
	}
	return nil
}

func validateDuration(name, value string, allowZero bool) error {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s: invalid duration %q: %w", name, value, err)
	}
	if duration < 0 || (!allowZero && duration == 0) {
		requirement := "greater than zero"
		if allowZero {
			requirement = "zero or greater"
		}
		return fmt.Errorf("%s: duration must be %s, got %q", name, requirement, value)
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
		"fallback_messages":      append([]string(nil), cfg.FallbackMessages...),
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
			entry["directory_allowlist"] = append(make([]string, 0, len(override.DirectoryAllowlist)), override.DirectoryAllowlist...)
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

// validateAllowlistEntries rejects blank or whitespace-only allowlist
// entries. Silently dropping them (the old expandPaths behavior) can turn a
// restrictive, user-authored allowlist into an empty one, which
// DirectoryAllowed treats as allow-everything. See #449.
func validateAllowlistEntries(context string, entries []string) error {
	for i, entry := range entries {
		if strings.TrimSpace(entry) == "" {
			return fmt.Errorf("%s.directory_allowlist[%d]: entry must not be blank", context, i)
		}
	}
	return nil
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
	cfg.FallbackMessages = append([]string(nil), cfg.FallbackMessages...)
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
