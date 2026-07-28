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
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	latestReleaseURL    = "https://api.github.com/repos/polter-dev/discord_terminal_presence/releases/latest"
	cacheLifetime       = 24 * time.Hour
	maxReleaseBody      = 1 << 20
	goEnvTimeout        = 500 * time.Millisecond
	brewPrefixTimeout   = 500 * time.Millisecond
	packageQueryTimeout = 500 * time.Millisecond
	cacheLockRetry      = 10 * time.Millisecond
	cacheLockTimeout    = 2 * time.Second
	curlConnectTimeout  = "10"
	curlMaxTime         = "300"
	// Cache transactions only perform local JSON I/O; 30s leaves ample margin
	// for a legitimate holder while recovering promptly after a process crash.
	cacheLockStaleAfter = 30 * time.Second

	BrewCommand         = "brew upgrade polter-dev/tap/termp"
	ScoopCommand        = "scoop update termp"
	genericInstallerURL = "https://raw.githubusercontent.com/polter-dev/discord_terminal_presence/%s/install.sh"
	packageDownloadURL  = "https://github.com/polter-dev/discord_terminal_presence/releases/download/%s/%s"
	workerDownloadURL   = "https://termp.polter.sh/dl/update/%s/%s/%s"
	latestReleasePage   = "https://github.com/polter-dev/discord_terminal_presence/releases/latest"
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
	InstallGeneric       InstallMethod = "generic"
	InstallHomebrew      InstallMethod = "homebrew"
	InstallScoop         InstallMethod = "scoop"
	InstallGo            InstallMethod = "go"
	InstallDebian        InstallMethod = "debian"
	InstallRPM           InstallMethod = "rpm"
	InstallSystemPackage InstallMethod = "system-package"
)

// IsSystemPackageInstall reports whether method is owned by a package manager
// that must be updated outside termp.
func IsSystemPackageInstall(method InstallMethod) bool {
	switch method {
	case InstallScoop, InstallDebian, InstallRPM, InstallSystemPackage:
		return true
	default:
		return false
	}
}

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

// Result describes an available update and the guidance appropriate for this
// installation.
type Result struct {
	Current  string
	Latest   string
	Method   InstallMethod
	Guidance Guidance
}

// Guidance distinguishes a directly runnable command from explanatory update
// instructions that may contain multiple commands.
type Guidance struct {
	Text     string
	Runnable bool
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

// ExecRunner executes update commands on the local machine. Interactive
// runners remain in the caller's foreground process group so commands such as
// sudo can read from the controlling terminal. Non-interactive runners use a
// separate process group so cancellation can terminate the whole command tree.
type ExecRunner struct {
	Interactive bool
}

func (runner ExecRunner) Run(ctx context.Context, command Command, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.Command(command.Name, command.Args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if len(command.Env) > 0 {
		cmd.Env = mergedEnvironment(os.Environ(), command.Env)
	}
	return runUpdateCommand(ctx, cmd, runner.Interactive)
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
		Current:  current,
		Latest:   latest,
		Method:   method,
		Guidance: GuidanceForMethod(method, latest),
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
		if statErr != nil || now.Before(info.ModTime().Add(cacheLockStaleAfter)) {
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
		Current:  current,
		Latest:   latest,
		Method:   method,
		Guidance: GuidanceForMethod(method, latest),
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
		`tmp=$(mktemp) && trap 'rm -f "$tmp"' EXIT HUP INT TERM && curl --connect-timeout `+curlConnectTimeout+` --max-time `+curlMaxTime+` -fsSL `+genericInstallerURL+` -o "$tmp" && test -s "$tmp" && TERMP_DOWNLOAD_CHANNEL=update VERSION=%s sh "$tmp"`,
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

// GuidanceForMethod returns update guidance for an install method. Runnable is
// false when the text contains multiple commands or explanatory labels.
func GuidanceForMethod(method InstallMethod, tag string) Guidance {
	switch method {
	case InstallHomebrew:
		return Guidance{Text: BrewCommand, Runnable: true}
	case InstallScoop:
		return Guidance{Text: ScoopCommand}
	case InstallGo:
		return Guidance{Text: GoCommand(tag), Runnable: true}
	case InstallDebian, InstallRPM, InstallSystemPackage:
		return systemPackageGuidance(method, tag, runtime.GOARCH, DetectRPMManager())
	default:
		return Guidance{Text: GenericCommand(tag), Runnable: true}
	}
}

// rpmManagerCandidates lists RPM front-ends in preference order. dnf (Fedora,
// RHEL 8+), zypper (openSUSE/SLES, which upgrades in place from a local file),
// and yum (RHEL/CentOS 7) all resolve dependencies. Plain rpm is last because
// it installs without resolving them.
var rpmManagerCandidates = []string{"dnf", "zypper", "yum", "rpm"}

// rpmManagerFallback describes every supported front-end. It is only used when
// none of them is installed, where naming a single tool would be a guess.
const rpmManagerFallback = "sudo dnf, sudo zypper, sudo yum, or sudo rpm"

var cachedRPMManager = sync.OnceValue(func() string {
	return detectRPMManager(exec.LookPath)
})

// resolveRPMManager is a variable so tests can pin the detected front-end.
var resolveRPMManager = func() string { return cachedRPMManager() }

// DetectRPMManager returns the first available RPM front-end, or "" when none
// is installed. Callers must fall back to printed guidance on "" rather than
// assuming a manager that is not present.
func DetectRPMManager() string { return resolveRPMManager() }

func detectRPMManager(lookPath func(string) (string, error)) string {
	for _, name := range rpmManagerCandidates {
		if _, err := lookPath(name); err == nil {
			return name
		}
	}
	return ""
}

// rpmInstallArgs returns the argv following sudo that installs or upgrades the
// package at path with manager, or nil when manager is unknown or empty.
func rpmInstallArgs(manager, path string) []string {
	switch manager {
	case "dnf", "yum":
		return []string{manager, "install", "-y", path}
	case "zypper":
		// --non-interactive is a global option accepted by every zypper
		// release. zypper >= 1.14 supports the semantically scoped
		// install-command option --allow-unsigned-rpm, which covers exactly
		// the unsigned-commandline-rpm case; older releases (including
		// 1.13) do not recognize it, so those -- and any release whose
		// version cannot be determined -- fall back to the long-standing
		// global --no-gpg-checks option chosen in #394 (also global on
		// 1.13, so an unsigned release RPM does not abort at the safe
		// default). Plain --non-interactive alone always aborts there.
		if zypperSupportsAllowUnsignedRPM(resolveZypperVersion()) {
			return []string{manager, "--non-interactive", "install", "--allow-unsigned-rpm", path}
		}
		return []string{manager, "--non-interactive", "--no-gpg-checks", "install", path}
	case "rpm":
		return []string{manager, "-U", path}
	default:
		return nil
	}
}

var cachedZypperVersion = sync.OnceValue(func() string {
	return detectZypperVersion(runZypperVersionCommand)
})

// resolveZypperVersion is a variable so tests can pin the detected zypper
// version without invoking the real binary, the same pattern used for
// resolveRPMManager.
var resolveZypperVersion = func() string { return cachedZypperVersion() }

func runZypperVersionCommand() (string, error) {
	output, err := exec.Command("zypper", "--version").Output()
	return string(output), err
}

// detectZypperVersion extracts zypper's own version (e.g. "1.14.63") from
// `zypper --version` output such as "zypper 1.14.63". It anchors on the
// token immediately following a literal "zypper" field so a multi-line or
// multi-tool report (some builds also print a "libzypp N.N.N" line, whose
// own version numbering is unrelated to zypper's) cannot be mistaken for
// zypper's version. If that anchor is not found, it falls back to the first
// parseable dotted-version token in case the output format ever changes.
// It returns "" when the command fails or nothing parses, so callers fail
// closed to the older, universally supported flag.
func detectZypperVersion(runVersion func() (string, error)) string {
	output, err := runVersion()
	if err != nil {
		return ""
	}
	fields := strings.Fields(output)
	for i, field := range fields {
		if field != "zypper" || i+1 >= len(fields) {
			continue
		}
		if _, _, ok := parseZypperVersion(fields[i+1]); ok {
			return fields[i+1]
		}
	}
	for _, field := range fields {
		if _, _, ok := parseZypperVersion(field); ok {
			return field
		}
	}
	return ""
}

// zypperSupportsAllowUnsignedRPM reports whether version is new enough
// (>= 1.14) to accept the --allow-unsigned-rpm install option. An
// unparseable or empty version fails closed to false, preserving the
// global --no-gpg-checks flag that has worked since zypper 1.13.
func zypperSupportsAllowUnsignedRPM(version string) bool {
	major, minor, ok := parseZypperVersion(version)
	if !ok {
		return false
	}
	if major != 1 {
		return major > 1
	}
	return minor >= 14
}

func parseZypperVersion(version string) (major, minor int, ok bool) {
	version = strings.TrimSpace(version)
	if version == "" {
		return 0, 0, false
	}
	parts := strings.SplitN(version, ".", 3)
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// RPMInstallCommand renders the human-runnable install command for manager.
func RPMInstallCommand(manager, file string) string {
	switch manager {
	case "dnf", "yum", "zypper":
		return fmt.Sprintf("sudo %s install %s", manager, file)
	case "rpm":
		return fmt.Sprintf("sudo rpm -U %s", file)
	default:
		return fmt.Sprintf("install %s with your RPM package manager (%s)", file, rpmManagerFallback)
	}
}

// RPMRemoveCommand renders the human-runnable removal command for manager.
func RPMRemoveCommand(manager string) string {
	switch manager {
	case "dnf", "yum", "zypper":
		return fmt.Sprintf("sudo %s remove termp", manager)
	case "rpm":
		return "sudo rpm -e termp"
	default:
		return fmt.Sprintf("remove termp with your RPM package manager (%s)", rpmManagerFallback)
	}
}

// WindowsArchiveGuidance describes how to update a Windows archive (generic)
// install. Windows locks a running executable, so termp cannot self-replace
// it the way it does on other platforms; this is a permanent platform
// limitation, not a transient failure. The guidance is not Runnable because
// it presents two independent options rather than a single command.
func WindowsArchiveGuidance(tag string) Guidance {
	return Guidance{
		Text: fmt.Sprintf(
			"  %s\n\nOr download the new release archive from:\n  %s",
			GoCommand(tag),
			latestReleasePage,
		),
	}
}

func systemPackageGuidance(method InstallMethod, tag, goarch, rpmManager string) Guidance {
	if !validReleaseTag(tag) {
		return Guidance{}
	}
	switch goarch {
	case "amd64", "arm64":
	default:
		return Guidance{}
	}

	version := strings.TrimPrefix(strings.TrimPrefix(tag, "v"), "V")
	debianAsset := fmt.Sprintf("termp_%s_linux_%s.deb", version, goarch)
	rpmAsset := fmt.Sprintf("termp_%s_linux_%s.rpm", version, goarch)
	debian := fmt.Sprintf(
		"curl -fLO %s\nsudo apt install ./%s",
		fmt.Sprintf(packageDownloadURL, tag, debianAsset),
		debianAsset,
	)
	rpm := fmt.Sprintf(
		"curl -fLO %s\n%s",
		fmt.Sprintf(packageDownloadURL, tag, rpmAsset),
		RPMInstallCommand(rpmManager, "./"+rpmAsset),
	)

	switch method {
	case InstallDebian:
		return Guidance{Text: debian}
	case InstallRPM:
		return Guidance{Text: rpm}
	default:
		return Guidance{Text: "Debian/Ubuntu:\n" + debian + "\n\nRPM-based Linux:\n" + rpm}
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
	case InstallScoop:
		return Command{}, fmt.Errorf("update the Scoop installation with %q", ScoopCommand)
	case InstallDebian, InstallRPM, InstallSystemPackage:
		return Command{}, fmt.Errorf("system package installation must be updated manually:\n%s", GuidanceForMethod(method, tag).Text)
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
		runner = ExecRunner{Interactive: false}
	}
	if method == InstallGeneric {
		return performGenericUpdate(ctx, tag, runner, stdin, stdout, stderr)
	}
	if method == InstallScoop {
		_, err := UpdateCommandForMethod(method, tag)
		return err
	}
	if IsSystemPackageInstall(method) {
		return performSystemPackageUpdate(ctx, method, tag, runner, stdin, stdout, stderr)
	}
	command, err := UpdateCommandForMethod(method, tag)
	if err != nil {
		return err
	}
	if err := runner.Run(ctx, command, stdin, stdout, stderr); err != nil {
		return fmt.Errorf("run %s: %w", GuidanceForMethod(method, tag).Text, err)
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
	binDir, err := GenericInstallDir()
	if err != nil {
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
	download := curlDownloadCommand(url, tmpPath)
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
			"BINDIR=" + binDir,
			"TERMP_DOWNLOAD_URL=" + archiveURL,
		},
	}
	if err := runner.Run(ctx, install, stdin, stdout, stderr); err != nil {
		return fmt.Errorf("run %s: %w", GenericCommand(tag), err)
	}
	return nil
}

func curlDownloadCommand(url, destination string) Command {
	return Command{
		Name: "curl",
		Args: []string{
			"--connect-timeout", curlConnectTimeout,
			"--max-time", curlMaxTime,
			"-fsSL", url,
			"-o", destination,
		},
	}
}

// GenericInstallDir returns the directory containing the resolved running
// executable. Generic updates replace that binary instead of falling back to a
// potentially different default install directory.
func GenericInstallDir() (string, error) {
	return genericInstallDir(os.Executable, filepath.EvalSymlinks)
}

func genericInstallDir(executable func() (string, error), evalSymlinks func(string) (string, error)) (string, error) {
	path, err := executable()
	if err != nil {
		return "", fmt.Errorf("locate running executable: %w", err)
	}
	resolved, err := evalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve running executable %q: %w", path, err)
	}
	dir := filepath.Dir(resolved)
	if dir == "" || dir == "." {
		return "", fmt.Errorf("resolve install directory for %q", resolved)
	}
	return dir, nil
}

func genericUpdatePlatformError(goos, tag string) error {
	if goos == "windows" {
		return fmt.Errorf("generic self-update is not supported on Windows; update with %q or install the release archive manually", GoCommand(tag))
	}
	return nil
}

// DetectInstallMethod resolves the running executable before examining its
// location. On Windows, an unresolved Scoop app path is also checked because
// filepath.EvalSymlinks can reject Scoop's mid-path current junction.
func DetectInstallMethod() InstallMethod {
	executable, err := os.Executable()
	if err != nil {
		return InstallGeneric
	}
	home, _ := os.UserHomeDir()
	goPaths := cachedGoInstallPaths()
	goPath := strings.Join(nonEmptyStrings(goPaths.goPath, os.Getenv("GOPATH")), string(os.PathListSeparator))
	scoopRoots := scoopInstallRoots(home, os.Getenv("SCOOP"), os.Getenv("SCOOP_GLOBAL"), os.Getenv("ProgramData"))
	return detectInstall(executable, filepath.EvalSymlinks, runtime.GOOS, detectSystemPackage, goPaths.goBin, goPath, home, scoopRoots, cachedHomebrewPrefixes()...)
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

func detectInstall(
	executable string,
	evalSymlinks func(string) (string, error),
	goos string,
	systemPackage func(string) InstallMethod,
	goBin, goPath, home string,
	scoopRoots []string,
	homebrewPrefixes ...string,
) InstallMethod {
	resolved, err := evalSymlinks(executable)
	if err != nil {
		if goos == "windows" && isScoopInstall(executable, scoopRoots) {
			return InstallScoop
		}
		return InstallGeneric
	}
	return detectResolvedInstall(resolved, goos, systemPackage, goBin, goPath, home, scoopRoots, homebrewPrefixes...)
}

func detectResolvedInstall(
	executable, goos string,
	systemPackage func(string) InstallMethod,
	goBin, goPath, home string,
	scoopRoots []string,
	homebrewPrefixes ...string,
) InstallMethod {
	if isHomebrewInstall(executable, homebrewPrefixes) {
		return InstallHomebrew
	}
	if goos == "windows" && isScoopInstall(executable, scoopRoots) {
		return InstallScoop
	}
	if goos == "linux" && filepath.Clean(executable) == filepath.Join(string(filepath.Separator), "usr", "bin", "termp") {
		if systemPackage != nil {
			if method := systemPackage(executable); IsSystemPackageInstall(method) {
				return method
			}
		}
		return InstallSystemPackage
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

func scoopInstallRoots(home, scoop, scoopGlobal, programData string) []string {
	var roots []string
	if scoop = strings.TrimSpace(scoop); scoop != "" {
		roots = append(roots, scoop)
	} else if home != "" {
		roots = append(roots, filepath.Join(home, "scoop"))
	}
	if scoopGlobal = strings.TrimSpace(scoopGlobal); scoopGlobal != "" {
		roots = append(roots, scoopGlobal)
	} else if programData != "" {
		roots = append(roots, filepath.Join(programData, "scoop"))
	}
	return roots
}

func isScoopInstall(executable string, roots []string) bool {
	executable = filepath.Clean(executable)
	for _, root := range roots {
		if root = strings.TrimSpace(root); root == "" {
			continue
		}
		root = filepath.Clean(root)
		if strings.EqualFold(executable, filepath.Join(root, "shims", "termp.exe")) {
			return true
		}
		appVersionDir := filepath.Dir(executable)
		version := filepath.Base(appVersionDir)
		if version != "." && version != ".." && version != string(filepath.Separator) &&
			strings.EqualFold(filepath.Base(executable), "termp.exe") &&
			strings.EqualFold(filepath.Dir(appVersionDir), filepath.Join(root, "apps", "termp")) {
			return true
		}
	}
	return false
}

func detectSystemPackage(executable string) InstallMethod {
	ownsFile := func(name string, args ...string) bool {
		ctx, cancel := context.WithTimeout(context.Background(), packageQueryTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.WaitDelay = packageQueryTimeout
		return cmd.Run() == nil
	}
	hasTool := func(name string) bool {
		_, err := exec.LookPath(name)
		return err == nil
	}
	return classifySystemPackage(
		executable,
		func(path string) bool { return ownsFile("dpkg-query", "--search", path) },
		func(path string) bool { return ownsFile("rpm", "--query", "--file", path) },
		func() bool { return hasTool("dpkg-query") },
		func() bool { return hasTool("rpm") },
	)
}

func classifySystemPackage(
	executable string,
	dpkgOwns, rpmOwns func(string) bool,
	hasDPKG, hasRPM func() bool,
) InstallMethod {
	debianOwned := dpkgOwns(executable)
	rpmOwned := rpmOwns(executable)
	switch {
	case debianOwned && !rpmOwned:
		return InstallDebian
	case rpmOwned && !debianOwned:
		return InstallRPM
	case debianOwned || rpmOwned:
		return InstallSystemPackage
	}

	dpkgAvailable := hasDPKG()
	rpmAvailable := hasRPM()
	switch {
	case dpkgAvailable && !rpmAvailable:
		return InstallDebian
	case rpmAvailable && !dpkgAvailable:
		return InstallRPM
	default:
		return InstallSystemPackage
	}
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
