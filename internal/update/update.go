// Package update checks GitHub Releases for newer termp versions and selects
// the update command that matches the current installation.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	latestReleaseURL = "https://api.github.com/repos/polter-dev/discord_terminal_presence/releases/latest"
	cacheLifetime    = 24 * time.Hour
	maxReleaseBody   = 1 << 20

	BrewCommand    = "brew upgrade --cask polter-dev/tap/termp"
	GoCommand      = "go install github.com/polter-dev/discord_terminal_presence/cmd/termp@latest"
	GenericCommand = "curl -fsSL https://raw.githubusercontent.com/polter-dev/discord_terminal_presence/main/install.sh | sh"
)

// InstallMethod identifies how the running binary was installed.
type InstallMethod string

const (
	InstallGeneric  InstallMethod = "generic"
	InstallHomebrew InstallMethod = "homebrew"
	InstallGo       InstallMethod = "go"
)

// ReleaseSource looks up the latest published release. Implementations must not
// attach user, machine, installation, usage, or configuration identifiers.
type ReleaseSource interface {
	Latest(context.Context, string) (string, error)
}

// GitHubReleaseSource reads the anonymous GitHub latest-release endpoint.
type GitHubReleaseSource struct {
	Client   *http.Client
	Endpoint string
}

// Latest returns the latest release's tag_name.
func (s GitHubReleaseSource) Latest(ctx context.Context, version string) (string, error) {
	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = latestReleaseURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "termp/"+version)

	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxReleaseBody))
		return "", fmt.Errorf("latest release returned HTTP %d", resp.StatusCode)
	}

	var payload struct {
		TagName string `json:"tag_name"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxReleaseBody))
	if err := decoder.Decode(&payload); err != nil {
		return "", err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return "", err
	}
	payload.TagName = strings.TrimSpace(payload.TagName)
	if payload.TagName == "" {
		return "", errors.New("latest release has no tag_name")
	}
	return payload.TagName, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("latest release response contains extra JSON")
		}
		return err
	}
	return nil
}

// Result describes an available update and the command appropriate for this
// installation.
type Result struct {
	Current string
	Latest  string
	Method  InstallMethod
	Command string
}

// Command describes an update process without requiring callers to parse a
// shell command string. Shell is used only for the generic curl pipeline.
type Command struct {
	Name string
	Args []string
}

// CommandRunner runs an update process with the caller's standard streams.
// It is injectable so automatic updates can reuse PerformUpdate safely.
type CommandRunner interface {
	Run(context.Context, Command, io.Reader, io.Writer, io.Writer) error
}

// ExecRunner executes update commands on the local machine.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, command Command, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// Checker limits release lookups to one per process and one per cacheLifetime.
// Errors intentionally collapse to "no update" so callers can fail silently.
type Checker struct {
	Source        ReleaseSource
	CachePath     string
	Now           func() time.Time
	DetectInstall func() InstallMethod

	once      sync.Once
	result    Result
	available bool
}

// NewChecker constructs a checker with production defaults.
func NewChecker(source ReleaseSource, cachePath string) *Checker {
	if source == nil {
		source = GitHubReleaseSource{}
	}
	return &Checker{
		Source:        source,
		CachePath:     cachePath,
		Now:           time.Now,
		DetectInstall: DetectInstallMethod,
	}
}

// DefaultCachePath returns the XDG-aware update cache location.
func DefaultCachePath() string {
	if dir := os.Getenv("XDG_CACHE_HOME"); dir != "" {
		return filepath.Join(dir, "termp", "update-check.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".cache", "termp", "update-check.json")
}

// Check reports a newer release when checks are enabled. NO_UPDATE_CHECK takes
// precedence over the config value, even when set to an empty string.
func (c *Checker) Check(ctx context.Context, current string, configEnabled bool) (Result, bool) {
	if !configEnabled || updateCheckDisabledByEnv() || isDevVersion(current) {
		return Result{}, false
	}
	if _, ok := parseVersion(current); !ok {
		return Result{}, false
	}

	c.once.Do(func() {
		c.result, c.available = c.check(ctx, current)
	})
	return c.result, c.available
}

// CachedCheck reports an update using only a fresh cache entry. It never calls
// the release source, making it suitable for latency-sensitive command alerts.
func (c *Checker) CachedCheck(current string, configEnabled bool) (Result, bool) {
	if !configEnabled || updateCheckDisabledByEnv() || isDevVersion(current) {
		return Result{}, false
	}
	if _, ok := parseVersion(current); !ok {
		return Result{}, false
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	cached, ok := readFreshCache(c.CachePath, now)
	if !ok || cached.Latest == "" {
		return Result{}, false
	}
	return c.resultFor(current, cached.Latest)
}

// Latest fetches the latest release and returns errors to an explicit caller.
// Successful results refresh the same cache used by passive checks and alerts.
func (c *Checker) Latest(ctx context.Context, current string) (Result, error) {
	if _, ok := parseVersion(current); !ok {
		return Result{}, fmt.Errorf("cannot update unversioned build %q", current)
	}
	latest, err := c.Source.Latest(ctx, current)
	if err != nil {
		return Result{}, fmt.Errorf("check latest release: %w", err)
	}
	if _, ok := parseVersion(latest); !ok {
		return Result{}, fmt.Errorf("latest release has invalid version %q", latest)
	}
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	_ = writeCache(c.CachePath, cacheEntry{CheckedAt: now, Latest: latest})
	method := InstallGeneric
	if c.DetectInstall != nil {
		method = c.DetectInstall()
	}
	return Result{
		Current: current,
		Latest:  latest,
		Method:  method,
		Command: CommandForMethod(method),
	}, nil
}

func (c *Checker) check(ctx context.Context, current string) (Result, bool) {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}

	if cached, ok := readFreshCache(c.CachePath, now); ok {
		return c.resultFor(current, cached.Latest)
	}
	if c.CachePath == "" {
		return Result{}, false
	}
	releaseLock, ok := acquireCacheLock(c.CachePath, now)
	if !ok {
		return Result{}, false
	}
	defer releaseLock()
	// Another process may have refreshed between our first read and lock.
	if cached, ok := readFreshCache(c.CachePath, now); ok {
		return c.resultFor(current, cached.Latest)
	}

	// Record an attempt before the request. Failures are cached too, preventing
	// offline or rate-limited machines from retrying on every invocation.
	if err := writeCache(c.CachePath, cacheEntry{CheckedAt: now}); err != nil {
		return Result{}, false
	}
	latest, err := c.Source.Latest(ctx, current)
	if err != nil {
		return Result{}, false
	}
	if _, ok := parseVersion(latest); !ok {
		return Result{}, false
	}
	_ = writeCache(c.CachePath, cacheEntry{CheckedAt: now, Latest: latest})
	return c.resultFor(current, latest)
}

func acquireCacheLock(cachePath string, now time.Time) (func(), bool) {
	dir := filepath.Dir(cachePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, false
	}
	lockPath := cachePath + ".lock"
	for attempt := 0; attempt < 2; attempt++ {
		lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_ = lock.Close()
			return func() { _ = os.Remove(lockPath) }, true
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, false
		}
		info, statErr := os.Stat(lockPath)
		if statErr != nil || now.Before(info.ModTime().Add(cacheLifetime)) {
			return nil, false
		}
		if err := os.Remove(lockPath); err != nil {
			return nil, false
		}
	}
	return nil, false
}

func (c *Checker) resultFor(current, latest string) (Result, bool) {
	if !IsNewer(current, latest) {
		return Result{}, false
	}
	method := InstallGeneric
	if c.DetectInstall != nil {
		method = c.DetectInstall()
	}
	return Result{
		Current: current,
		Latest:  latest,
		Method:  method,
		Command: CommandForMethod(method),
	}, true
}

func updateCheckDisabledByEnv() bool {
	_, disabled := os.LookupEnv("NO_UPDATE_CHECK")
	return disabled
}

func isDevVersion(version string) bool {
	return strings.EqualFold(strings.TrimSpace(version), "dev")
}

type cacheEntry struct {
	CheckedAt time.Time `json:"checked_at"`
	Latest    string    `json:"latest_version,omitempty"`
}

func readFreshCache(path string, now time.Time) (cacheEntry, bool) {
	if path == "" {
		return cacheEntry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheEntry{}, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil || entry.CheckedAt.IsZero() {
		return cacheEntry{}, false
	}
	if !now.Before(entry.CheckedAt.Add(cacheLifetime)) {
		return cacheEntry{}, false
	}
	return entry, true
}

func writeCache(path string, entry cacheEntry) error {
	if path == "" {
		return nil
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
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
	return os.Rename(tmpPath, path)
}

// CommandForMethod returns the supported update command for an install method.
func CommandForMethod(method InstallMethod) string {
	switch method {
	case InstallHomebrew:
		return BrewCommand
	case InstallGo:
		return GoCommand
	default:
		return GenericCommand
	}
}

// UpdateCommandForMethod centralizes executable construction for updates.
// The generic command retains the existing installer pipeline trust model;
// issue #84 will harden that path separately.
func UpdateCommandForMethod(method InstallMethod) Command {
	switch method {
	case InstallHomebrew:
		return Command{Name: "brew", Args: []string{"upgrade", "--cask", "polter-dev/tap/termp"}}
	case InstallGo:
		return Command{Name: "go", Args: []string{"install", "github.com/polter-dev/discord_terminal_presence/cmd/termp@latest"}}
	default:
		return Command{Name: "sh", Args: []string{"-c", GenericCommand}}
	}
}

// PerformUpdate executes the install-aware updater with streamed I/O. It is
// intentionally separate from release checking for reuse by opt-in automation.
func PerformUpdate(ctx context.Context, method InstallMethod, runner CommandRunner, stdin io.Reader, stdout, stderr io.Writer) error {
	if runner == nil {
		runner = ExecRunner{}
	}
	command := UpdateCommandForMethod(method)
	if err := runner.Run(ctx, command, stdin, stdout, stderr); err != nil {
		return fmt.Errorf("run %s: %w", CommandForMethod(method), err)
	}
	return nil
}

// DetectInstallMethod resolves the running executable before examining its
// location. Any resolution uncertainty falls back to the generic installer.
func DetectInstallMethod() InstallMethod {
	executable, err := os.Executable()
	if err != nil {
		return InstallGeneric
	}
	home, _ := os.UserHomeDir()
	return detectInstall(executable, filepath.EvalSymlinks, os.Getenv("GOPATH"), home)
}

func detectInstall(executable string, evalSymlinks func(string) (string, error), goPath, home string) InstallMethod {
	resolved, err := evalSymlinks(executable)
	if err != nil {
		return InstallGeneric
	}
	return detectResolvedInstall(resolved, goPath, home)
}

func detectResolvedInstall(executable, goPath, home string) InstallMethod {
	clean := filepath.ToSlash(filepath.Clean(executable))
	if strings.Contains(clean, "/Cellar/") || strings.Contains(clean, "/Caskroom/") {
		return InstallHomebrew
	}

	goBins := make(map[string]struct{})
	for _, root := range filepath.SplitList(goPath) {
		if root = strings.TrimSpace(root); root != "" {
			goBins[filepath.Clean(filepath.Join(root, "bin"))] = struct{}{}
		}
	}
	if home != "" {
		goBins[filepath.Clean(filepath.Join(home, "go", "bin"))] = struct{}{}
	}
	for bin := range goBins {
		if pathWithin(executable, bin) {
			return InstallGo
		}
	}
	return InstallGeneric
}

func pathWithin(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// IsNewer reports whether latest has greater semantic-version precedence than
// current. Build metadata is ignored, and dev/invalid versions never update.
func IsNewer(current, latest string) bool {
	if isDevVersion(current) {
		return false
	}
	currentVersion, currentOK := parseVersion(current)
	latestVersion, latestOK := parseVersion(latest)
	if !currentOK || !latestOK {
		return false
	}
	return compareVersion(latestVersion, currentVersion) > 0
}

type semanticVersion struct {
	core       [3]string
	prerelease []string
}

func parseVersion(value string) (semanticVersion, bool) {
	value = strings.TrimSpace(value)
	if len(value) > 0 && (value[0] == 'v' || value[0] == 'V') {
		value = value[1:]
	}
	if plus := strings.IndexByte(value, '+'); plus >= 0 {
		if !validBuildMetadata(value[plus+1:]) {
			return semanticVersion{}, false
		}
		value = value[:plus]
	}
	coreText, prereleaseText, hasPrerelease := strings.Cut(value, "-")
	parts := strings.Split(coreText, ".")
	if len(parts) != 3 {
		return semanticVersion{}, false
	}
	var parsed semanticVersion
	for i, part := range parts {
		if !validNumericIdentifier(part) {
			return semanticVersion{}, false
		}
		parsed.core[i] = part
	}
	if !hasPrerelease {
		return parsed, true
	}
	if prereleaseText == "" {
		return semanticVersion{}, false
	}
	for _, identifier := range strings.Split(prereleaseText, ".") {
		if !validPrereleaseIdentifier(identifier) {
			return semanticVersion{}, false
		}
		parsed.prerelease = append(parsed.prerelease, identifier)
	}
	return parsed, true
}

func validBuildMetadata(value string) bool {
	if value == "" {
		return false
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		for _, char := range identifier {
			if (char < '0' || char > '9') && (char < 'A' || char > 'Z') &&
				(char < 'a' || char > 'z') && char != '-' {
				return false
			}
		}
	}
	return true
}

func validNumericIdentifier(value string) bool {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validPrereleaseIdentifier(value string) bool {
	if value == "" {
		return false
	}
	numeric := true
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'A' || char > 'Z') &&
			(char < 'a' || char > 'z') && char != '-' {
			return false
		}
		if char < '0' || char > '9' {
			numeric = false
		}
	}
	return !numeric || len(value) == 1 || value[0] != '0'
}

func compareVersion(left, right semanticVersion) int {
	for i := range left.core {
		if compared := compareNumeric(left.core[i], right.core[i]); compared != 0 {
			return compared
		}
	}
	if len(left.prerelease) == 0 && len(right.prerelease) == 0 {
		return 0
	}
	if len(left.prerelease) == 0 {
		return 1
	}
	if len(right.prerelease) == 0 {
		return -1
	}
	for i := 0; i < min(len(left.prerelease), len(right.prerelease)); i++ {
		leftID, rightID := left.prerelease[i], right.prerelease[i]
		leftNumeric := isNumeric(leftID)
		rightNumeric := isNumeric(rightID)
		switch {
		case leftNumeric && rightNumeric:
			if compared := compareNumeric(leftID, rightID); compared != 0 {
				return compared
			}
		case leftNumeric:
			return -1
		case rightNumeric:
			return 1
		case leftID < rightID:
			return -1
		case leftID > rightID:
			return 1
		}
	}
	switch {
	case len(left.prerelease) < len(right.prerelease):
		return -1
	case len(left.prerelease) > len(right.prerelease):
		return 1
	default:
		return 0
	}
}

func compareNumeric(left, right string) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func isNumeric(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return value != ""
}
