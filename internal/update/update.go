// Package update checks GitHub Releases for newer termp versions and selects
// the update command that matches the current installation.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	latestReleaseURL  = "https://api.github.com/repos/polter-dev/discord_terminal_presence/releases/latest"
	cacheLifetime     = 24 * time.Hour
	maxReleaseBody    = 1 << 20
	goEnvTimeout      = 500 * time.Millisecond
	brewPrefixTimeout = 500 * time.Millisecond
	cacheLockRetry    = 10 * time.Millisecond
	cacheLockTimeout  = 2 * time.Second

	BrewCommand         = "brew upgrade polter-dev/tap/termp"
	genericInstallerURL = "https://raw.githubusercontent.com/polter-dev/discord_terminal_presence/%s/install.sh"
	workerDownloadURL   = "https://termp.polter.sh/dl/update/%s/%s/%s"
)

var removeTemporaryInstaller = os.Remove

type goInstallPaths struct {
	goBin  string
	goPath string
}

var cachedGoInstallPaths = sync.OnceValue(func() goInstallPaths {
	ctx, cancel := context.WithTimeout(context.Background(), goEnvTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "env", "GOBIN", "GOPATH")
	cmd.WaitDelay = goEnvTimeout
	output, err := cmd.Output()
	return parseGoEnvPaths(output, err)
})

var cachedHomebrewPrefixes = sync.OnceValue(func() []string {
	prefixes := []string{"/opt/homebrew", "/usr/local", "/home/linuxbrew/.linuxbrew"}
	ctx, cancel := context.WithTimeout(context.Background(), brewPrefixTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "brew", "--prefix")
	cmd.WaitDelay = brewPrefixTimeout
	if output, err := cmd.Output(); err == nil {
		if prefix := strings.TrimSpace(string(output)); prefix != "" {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
})

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
	Env  []string
}

// CommandRunner runs an update process with the caller's standard streams.
// It is injectable so automatic updates can reuse PerformUpdate safely.
type CommandRunner interface {
	Run(context.Context, Command, io.Reader, io.Writer, io.Writer) error
}

// ExecRunner executes update commands on the local machine.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, command Command, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.Command(command.Name, command.Args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if len(command.Env) > 0 {
		cmd.Env = mergedEnvironment(os.Environ(), command.Env)
	}
	return runUpdateCommand(ctx, cmd)
}

func mergedEnvironment(base, overrides []string) []string {
	keys := make(map[string]struct{}, len(overrides))
	for _, entry := range overrides {
		if key, _, ok := strings.Cut(entry, "="); ok {
			keys[key] = struct{}{}
		}
	}
	merged := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := keys[key]; !overridden {
			merged = append(merged, entry)
		}
	}
	return append(merged, overrides...)
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
	if runtime.GOOS == "windows" {
		dir, err := os.UserCacheDir()
		if err != nil || dir == "" {
			return ""
		}
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
		Command: CommandForMethod(method, latest),
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
	var cached cacheEntry
	var fresh bool
	err := cacheTransaction(c.CachePath, func(entry cacheEntry) (cacheEntry, bool) {
		if !entry.CheckedAt.IsZero() && now.Before(entry.CheckedAt.Add(cacheLifetime)) {
			cached, fresh = entry, true
			return entry, false
		}
		entry.CheckedAt = now
		entry.Latest = ""
		return entry, true
	})
	if err != nil {
		return Result{}, false
	}
	// Another process may have refreshed between our first read and transaction.
	if fresh {
		return c.resultFor(current, cached.Latest)
	}

	// Record an attempt before the request. Failures are cached too, preventing
	// offline or rate-limited machines from retrying on every invocation.
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
		Command: CommandForMethod(method, latest),
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
	CheckedAt       time.Time               `json:"checked_at"`
	Latest          string                  `json:"latest_version,omitempty"`
	AutomaticUpdate *AutomaticUpdateAttempt `json:"automatic_update,omitempty"`
}

// AutomaticUpdateAttempt records the outcome of the last automatic install
// attempt so status can report failures after the daemon has started.
type AutomaticUpdateAttempt struct {
	AttemptedAt time.Time `json:"attempted_at"`
	Target      string    `json:"target_version"`
	Error       string    `json:"error,omitempty"`
	Skipped     bool      `json:"skipped,omitempty"`
}

// ReadAutomaticUpdateAttempt reads the last automatic install attempt from the
// update cache. Missing or malformed cache data is treated as no attempt.
func ReadAutomaticUpdateAttempt(path string) (AutomaticUpdateAttempt, bool) {
	entry, ok := readCache(path)
	if !ok || entry.AutomaticUpdate == nil || entry.AutomaticUpdate.AttemptedAt.IsZero() {
		return AutomaticUpdateAttempt{}, false
	}
	return *entry.AutomaticUpdate, true
}

// RecordAutomaticUpdateAttempt replaces the last automatic install attempt
// while retaining the release-check metadata stored in the same cache.
func RecordAutomaticUpdateAttempt(path, target string, attemptedAt time.Time, updateErr error) error {
	attempt := &AutomaticUpdateAttempt{
		AttemptedAt: attemptedAt,
		Target:      target,
	}
	if updateErr != nil {
		attempt.Error = updateErr.Error()
		var skip interface {
			AutomaticUpdateSkipped() bool
		}
		if errors.As(updateErr, &skip) {
			attempt.Skipped = skip.AutomaticUpdateSkipped()
		}
	}
	return cacheTransaction(path, func(entry cacheEntry) (cacheEntry, bool) {
		entry.AutomaticUpdate = attempt
		return entry, true
	})
}

func readFreshCache(path string, now time.Time) (cacheEntry, bool) {
	entry, ok := readCache(path)
	if !ok || entry.CheckedAt.IsZero() {
		return cacheEntry{}, false
	}
	if !now.Before(entry.CheckedAt.Add(cacheLifetime)) {
		return cacheEntry{}, false
	}
	return entry, true
}

func readCache(path string) (cacheEntry, bool) {
	if path == "" {
		return cacheEntry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cacheEntry{}, false
	}
	var entry cacheEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return cacheEntry{}, false
	}
	return entry, true
}

func writeCache(path string, entry cacheEntry) error {
	return cacheTransaction(path, func(previous cacheEntry) (cacheEntry, bool) {
		if entry.AutomaticUpdate == nil {
			entry.AutomaticUpdate = previous.AutomaticUpdate
		}
		return entry, true
	})
}

func cacheTransaction(path string, update func(cacheEntry) (cacheEntry, bool)) error {
	if path == "" {
		return nil
	}
	deadline := time.Now().Add(cacheLockTimeout)
	var releaseLock func()
	for {
		if release, ok := acquireCacheLock(path, time.Now()); ok {
			releaseLock = release
			break
		}
		if !time.Now().Before(deadline) {
			return errors.New("update cache lock unavailable")
		}
		time.Sleep(cacheLockRetry)
	}
	defer releaseLock()

	entry, _ := readCache(path)
	entry, write := update(entry)
	if !write {
		return nil
	}
	return writeCacheFile(path, entry)
}

func writeCacheFile(path string, entry cacheEntry) error {
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

// GenericCommand returns a command that fetches the installer from the exact
// release tag being installed. Release tags have already been validated as
// semantic versions, so they cannot add shell syntax to this command.
func GenericCommand(tag string) string {
	if !validReleaseTag(tag) {
		return ""
	}
	return fmt.Sprintf(
		`tmp=$(mktemp) && trap 'rm -f "$tmp"' EXIT HUP INT TERM && curl -fsSL `+genericInstallerURL+` -o "$tmp" && test -s "$tmp" && TERMP_DOWNLOAD_CHANNEL=update VERSION=%s sh "$tmp"`,
		tag,
		tag,
	)
}

// GoCommand returns a go install command pinned to the validated release tag.
func GoCommand(tag string) string {
	if !validReleaseTag(tag) {
		return ""
	}
	return "go install github.com/polter-dev/discord_terminal_presence/cmd/termp@" + tag
}

func validReleaseTag(tag string) bool {
	if tag == "" || tag != strings.TrimSpace(tag) {
		return false
	}
	_, ok := parseVersion(tag)
	return ok
}

func updateArchiveURL(goos, goarch, tag string) (string, error) {
	switch goos {
	case "darwin", "linux":
	default:
		return "", fmt.Errorf("unsupported update OS %q", goos)
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return "", fmt.Errorf("unsupported update architecture %q", goarch)
	}
	return fmt.Sprintf(workerDownloadURL, goos, goarch, tag), nil
}

// CommandForMethod returns the supported update command for an install method.
func CommandForMethod(method InstallMethod, tag string) string {
	switch method {
	case InstallHomebrew:
		return BrewCommand
	case InstallGo:
		return GoCommand(tag)
	default:
		return GenericCommand(tag)
	}
}

// UpdateCommandForMethod centralizes executable construction for updates.
func UpdateCommandForMethod(method InstallMethod, tag string) (Command, error) {
	switch method {
	case InstallHomebrew:
		return Command{Name: "brew", Args: []string{"upgrade", "polter-dev/tap/termp"}}, nil
	case InstallGo:
		if !validReleaseTag(tag) {
			return Command{}, fmt.Errorf("invalid release tag %q", tag)
		}
		return Command{Name: "go", Args: []string{"install", "github.com/polter-dev/discord_terminal_presence/cmd/termp@" + tag}}, nil
	default:
		command := GenericCommand(tag)
		if command == "" {
			return Command{}, fmt.Errorf("invalid release tag %q", tag)
		}
		return Command{Name: "sh", Args: []string{"-c", command}}, nil
	}
}

// PerformUpdate executes the install-aware updater with streamed I/O. Generic
// updates use the release-tagged installer. This function is intentionally
// separate from release checking for reuse by opt-in automation.
func PerformUpdate(ctx context.Context, method InstallMethod, tag string, runner CommandRunner, stdin io.Reader, stdout, stderr io.Writer) error {
	if runner == nil {
		runner = ExecRunner{}
	}
	if method == InstallGeneric {
		return performGenericUpdate(ctx, tag, runner, stdin, stdout, stderr)
	}
	command, err := UpdateCommandForMethod(method, tag)
	if err != nil {
		return err
	}
	if err := runner.Run(ctx, command, stdin, stdout, stderr); err != nil {
		return fmt.Errorf("run %s: %w", CommandForMethod(method, tag), err)
	}
	return nil
}

func performGenericUpdate(ctx context.Context, tag string, runner CommandRunner, stdin io.Reader, stdout, stderr io.Writer) error {
	if !validReleaseTag(tag) {
		return fmt.Errorf("invalid release tag %q", tag)
	}
	if err := genericUpdatePlatformError(runtime.GOOS, tag); err != nil {
		return err
	}
	archiveURL, err := updateArchiveURL(runtime.GOOS, runtime.GOARCH, tag)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "termp-install-*.sh")
	if err != nil {
		return fmt.Errorf("create temporary installer: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("create temporary installer: %w", err)
	}
	defer func() {
		if err := removeTemporaryInstaller(tmpPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("termp update: remove temporary installer %s: %v", tmpPath, err)
		}
	}()

	url := fmt.Sprintf(genericInstallerURL, tag)
	download := Command{Name: "curl", Args: []string{"-fsSL", url, "-o", tmpPath}}
	if err := runner.Run(ctx, download, nil, stdout, stderr); err != nil {
		return fmt.Errorf("download installer from %s: %w", url, err)
	}
	info, err := os.Stat(tmpPath)
	if err != nil {
		return fmt.Errorf("inspect downloaded installer: %w", err)
	}
	if info.Size() == 0 {
		return errors.New("downloaded installer is empty")
	}

	install := Command{
		Name: "sh",
		Args: []string{tmpPath},
		Env: []string{
			"VERSION=" + tag,
			"TERMP_DOWNLOAD_URL=" + archiveURL,
		},
	}
	if err := runner.Run(ctx, install, stdin, stdout, stderr); err != nil {
		return fmt.Errorf("run %s: %w", GenericCommand(tag), err)
	}
	return nil
}

func genericUpdatePlatformError(goos, tag string) error {
	if goos == "windows" {
		return fmt.Errorf("generic self-update is not supported on Windows; update with %q or install the release archive manually", GoCommand(tag))
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
	goPaths := cachedGoInstallPaths()
	goPath := strings.Join(nonEmptyStrings(goPaths.goPath, os.Getenv("GOPATH")), string(os.PathListSeparator))
	return detectInstall(executable, filepath.EvalSymlinks, goPaths.goBin, goPath, home, cachedHomebrewPrefixes()...)
}

func parseGoEnvPaths(output []byte, err error) goInstallPaths {
	if err != nil {
		return goInstallPaths{}
	}
	lines := strings.Split(strings.ReplaceAll(string(output), "\r\n", "\n"), "\n")
	if len(lines) < 2 {
		return goInstallPaths{}
	}
	return goInstallPaths{
		goBin:  strings.TrimSpace(lines[0]),
		goPath: strings.TrimSpace(lines[1]),
	}
}

func nonEmptyStrings(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

func detectInstall(executable string, evalSymlinks func(string) (string, error), goBin, goPath, home string, homebrewPrefixes ...string) InstallMethod {
	resolved, err := evalSymlinks(executable)
	if err != nil {
		return InstallGeneric
	}
	return detectResolvedInstall(resolved, goBin, goPath, home, homebrewPrefixes...)
}

func detectResolvedInstall(executable, goBin, goPath, home string, homebrewPrefixes ...string) InstallMethod {
	if isHomebrewInstall(executable, homebrewPrefixes) {
		return InstallHomebrew
	}

	goBins := make(map[string]struct{})
	if goBin = strings.TrimSpace(goBin); goBin != "" {
		goBins[filepath.Clean(goBin)] = struct{}{}
	}
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

func isHomebrewInstall(executable string, prefixes []string) bool {
	for _, prefix := range prefixes {
		rel, err := filepath.Rel(filepath.Clean(prefix), filepath.Clean(executable))
		if err != nil {
			continue
		}
		parts := strings.Split(filepath.ToSlash(rel), "/")
		if len(parts) == 5 && parts[0] == "Cellar" && parts[1] == "termp" &&
			parts[2] != "" && parts[3] == "bin" && parts[4] == "termp" {
			return true
		}
		if len(parts) == 4 && parts[0] == "Caskroom" && parts[1] == "termp" &&
			parts[2] != "" && parts[3] == "termp" {
			return true
		}
	}
	return false
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
