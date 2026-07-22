package update

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	mu     sync.Mutex
	latest string
	err    error
	calls  int
}

type recordingRunner struct {
	command Command
	calls   int
}

func (r *recordingRunner) Run(_ context.Context, command Command, _ io.Reader, _, _ io.Writer) error {
	r.calls++
	r.command = command
	return nil
}

func (s *fakeSource) Latest(context.Context, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.latest, s.err
}

func (s *fakeSource) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func TestIsNewerSemver(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    bool
	}{
		{name: "patch", current: "1.2.3", latest: "1.2.4", want: true},
		{name: "v prefix", current: "v1.2.3", latest: "v2.0.0", want: true},
		{name: "build metadata ignored", current: "1.2.3+abc123", latest: "1.2.3+def456", want: false},
		{name: "newer despite metadata", current: "1.2.3+abc123", latest: "1.2.4+def456", want: true},
		{name: "stable after prerelease", current: "1.2.3-rc.1", latest: "1.2.3", want: true},
		{name: "prerelease numeric", current: "1.2.3-rc.2", latest: "1.2.3-rc.10", want: true},
		{name: "older", current: "2.0.0", latest: "1.9.9", want: false},
		{name: "equal", current: "1.2.3", latest: "1.2.3", want: false},
		{name: "dev", current: "dev", latest: "99.0.0", want: false},
		{name: "invalid current", current: "main", latest: "1.0.0", want: false},
		{name: "invalid latest", current: "1.0.0", latest: "release", want: false},
		{name: "invalid leading zero", current: "1.0.0", latest: "1.01.0", want: false},
		{name: "invalid metadata", current: "1.0.0", latest: "1.0.1+", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsNewer(tt.current, tt.latest); got != tt.want {
				t.Fatalf("IsNewer(%q, %q) = %t, want %t", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestGenericCommandPinsInstallerAndVersionToReleaseTag(t *testing.T) {
	const tag = "v1.2.3"
	command := GenericCommand(tag)
	if strings.Contains(command, "/main/") {
		t.Fatalf("generic command uses mutable main branch: %q", command)
	}
	if want := "raw.githubusercontent.com/polter-dev/discord_terminal_presence/" + tag + "/install.sh"; !strings.Contains(command, want) {
		t.Fatalf("generic command = %q, want tagged installer URL containing %q", command, want)
	}
	if !strings.Contains(command, "VERSION="+tag+" sh") {
		t.Fatalf("generic command = %q, want installer version pinned to %q", command, tag)
	}
}

func TestGenericUpdateRejectsUnsafeReleaseTagsWithoutRunning(t *testing.T) {
	for _, tag := range []string{"", "latest", "v1.2.3; id", "../v1.2.3", "v1.2.3\nmain"} {
		t.Run(strings.ReplaceAll(tag, "/", "_"), func(t *testing.T) {
			runner := &recordingRunner{}
			err := PerformUpdate(context.Background(), InstallGeneric, tag, runner, nil, io.Discard, io.Discard)
			if err == nil {
				t.Fatalf("PerformUpdate() accepted unsafe release tag %q", tag)
			}
			if runner.calls != 0 {
				t.Fatalf("PerformUpdate() ran command for unsafe release tag %q", tag)
			}
		})
	}
}

func TestPerformGenericUpdateUsesResolvedReleaseTag(t *testing.T) {
	runner := &recordingRunner{}
	if err := PerformUpdate(context.Background(), InstallGeneric, "v2.3.4", runner, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := Command{Name: "sh", Args: []string{"-c", GenericCommand("v2.3.4")}}
	if runner.calls != 1 || runner.command.Name != want.Name || strings.Join(runner.command.Args, "\x00") != strings.Join(want.Args, "\x00") {
		t.Fatalf("runner = (%d, %#v), want (1, %#v)", runner.calls, runner.command, want)
	}
}

func TestInstallMethodDetection(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "Users", "test")
	goPath := filepath.Join(string(filepath.Separator), "opt", "gopath")
	tests := []struct {
		name string
		path string
		want InstallMethod
	}{
		{name: "Homebrew Cellar", path: filepath.Join("/opt/homebrew/Cellar/termp/1.2.3/bin/termp"), want: InstallHomebrew},
		{name: "Homebrew Caskroom", path: filepath.Join("/usr/local/Caskroom/termp/1.2.3/termp"), want: InstallHomebrew},
		{name: "GOPATH bin", path: filepath.Join(goPath, "bin", "termp"), want: InstallGo},
		{name: "default home Go bin", path: filepath.Join(home, "go", "bin", "termp"), want: InstallGo},
		{name: "generic installer", path: filepath.Join("/usr/local/bin/termp"), want: InstallGeneric},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectInstall(tt.path, func(path string) (string, error) { return path, nil }, goPath, home)
			if got != tt.want {
				t.Fatalf("detectInstall(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestInstallDetectionResolvesSymlinkBeforeMatching(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "Caskroom", "termp", "1.2.3", "termp")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "bin", "termp")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if got := detectInstall(link, filepath.EvalSymlinks, "", root); got != InstallHomebrew {
		t.Fatalf("symlinked Homebrew install = %q, want %q", got, InstallHomebrew)
	}
}

func TestInstallDetectionFallsBackWhenResolutionFails(t *testing.T) {
	got := detectInstall("/opt/homebrew/Cellar/termp/1.2.3/termp", func(string) (string, error) {
		return "", errors.New("cannot resolve")
	}, "", "")
	if got != InstallGeneric {
		t.Fatalf("ambiguous install = %q, want generic", got)
	}
}

func TestCheckerUsesFreshCache(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "update.json")
	if err := writeCache(path, cacheEntry{CheckedAt: now.Add(-time.Hour), Latest: "v1.2.0"}); err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{latest: "v9.0.0"}
	checker := NewChecker(source, path)
	checker.Now = func() time.Time { return now }
	checker.DetectInstall = func() InstallMethod { return InstallGo }

	result, ok := checker.Check(context.Background(), "1.0.0+sha", true)
	if !ok || result.Latest != "v1.2.0" || result.Command != GoCommand {
		t.Fatalf("cached result = (%#v, %t)", result, ok)
	}
	if source.callCount() != 0 {
		t.Fatalf("fresh cache made %d source calls", source.callCount())
	}
}

func TestCachedCheckNeverUsesReleaseSource(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		checkedAt time.Time
		latest    string
		want      bool
	}{
		{name: "fresh newer", checkedAt: now.Add(-time.Hour), latest: "v1.1.0", want: true},
		{name: "fresh equal", checkedAt: now.Add(-time.Hour), latest: "v1.0.0"},
		{name: "stale newer", checkedAt: now.Add(-cacheLifetime), latest: "v1.1.0"},
		{name: "failed cached check", checkedAt: now.Add(-time.Hour), latest: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "update.json")
			if err := writeCache(path, cacheEntry{CheckedAt: tt.checkedAt, Latest: tt.latest}); err != nil {
				t.Fatal(err)
			}
			source := &fakeSource{err: errors.New("network must not be used")}
			checker := NewChecker(source, path)
			checker.Now = func() time.Time { return now }
			_, got := checker.CachedCheck("1.0.0", true)
			if got != tt.want {
				t.Fatalf("CachedCheck() available = %t, want %t", got, tt.want)
			}
			if source.callCount() != 0 {
				t.Fatalf("CachedCheck made %d source calls", source.callCount())
			}
		})
	}
}

func TestCheckerRefreshesExpiredCache(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "update.json")
	if err := writeCache(path, cacheEntry{CheckedAt: now.Add(-cacheLifetime), Latest: "v1.1.0"}); err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{latest: "v1.3.0"}
	checker := NewChecker(source, path)
	checker.Now = func() time.Time { return now }
	checker.DetectInstall = func() InstallMethod { return InstallHomebrew }

	result, ok := checker.Check(context.Background(), "1.2.0", true)
	if !ok || result.Latest != "v1.3.0" || result.Command != BrewCommand {
		t.Fatalf("refreshed result = (%#v, %t)", result, ok)
	}
	if source.callCount() != 1 {
		t.Fatalf("expired cache made %d source calls, want 1", source.callCount())
	}
	entry, fresh := readFreshCache(path, now)
	if !fresh || entry.Latest != "v1.3.0" {
		t.Fatalf("refreshed cache = (%#v, %t)", entry, fresh)
	}
}

func TestCheckerChecksAtMostOncePerProcess(t *testing.T) {
	source := &fakeSource{latest: "v2.0.0"}
	checker := NewChecker(source, filepath.Join(t.TempDir(), "update.json"))
	checker.DetectInstall = func() InstallMethod { return InstallGeneric }
	for range 2 {
		result, ok := checker.Check(context.Background(), "1.0.0", true)
		if !ok {
			t.Fatal("expected update")
		}
		if result.Command != GenericCommand("v2.0.0") {
			t.Fatalf("generic update command = %q, want %q", result.Command, GenericCommand("v2.0.0"))
		}
	}
	if source.callCount() != 1 {
		t.Fatalf("source calls = %d, want 1", source.callCount())
	}
}

func TestCheckerOptOutsAndDevSkipEntirely(t *testing.T) {
	tests := []struct {
		name          string
		version       string
		configEnabled bool
		envSet        bool
	}{
		{name: "config", version: "1.0.0", configEnabled: false},
		{name: "environment", version: "1.0.0", configEnabled: true, envSet: true},
		{name: "dev", version: "dev", configEnabled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.envSet {
				t.Setenv("NO_UPDATE_CHECK", "")
			} else {
				old, existed := os.LookupEnv("NO_UPDATE_CHECK")
				_ = os.Unsetenv("NO_UPDATE_CHECK")
				t.Cleanup(func() {
					if existed {
						_ = os.Setenv("NO_UPDATE_CHECK", old)
					} else {
						_ = os.Unsetenv("NO_UPDATE_CHECK")
					}
				})
			}
			source := &fakeSource{latest: "v9.0.0"}
			cachePath := filepath.Join(t.TempDir(), "update.json")
			checker := NewChecker(source, cachePath)
			if result, ok := checker.Check(context.Background(), tt.version, tt.configEnabled); ok || result != (Result{}) {
				t.Fatalf("opt-out result = (%#v, %t)", result, ok)
			}
			if source.callCount() != 0 {
				t.Fatalf("opt-out made %d source calls", source.callCount())
			}
			if _, err := os.Stat(cachePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("opt-out cache stat error = %v, want not exist", err)
			}
		})
	}
}

func TestCheckerFailureIsSilentAndCached(t *testing.T) {
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "update.json")
	source := &fakeSource{err: errors.New("offline")}
	checker := NewChecker(source, path)
	checker.Now = func() time.Time { return now }

	if result, ok := checker.Check(context.Background(), "1.0.0", true); ok || result != (Result{}) {
		t.Fatalf("failed lookup result = (%#v, %t)", result, ok)
	}
	entry, fresh := readFreshCache(path, now)
	if !fresh || entry.Latest != "" {
		t.Fatalf("failure cache = (%#v, %t)", entry, fresh)
	}
}

func TestCheckerHTTPFailuresStaySilent(t *testing.T) {
	tests := []struct {
		name      string
		transport roundTripFunc
	}{
		{name: "network error", transport: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		}},
		{name: "non-200", transport: func(*http.Request) (*http.Response, error) {
			return response(http.StatusTooManyRequests, `{"message":"rate limited"}`), nil
		}},
		{name: "malformed JSON", transport: func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, `{"tag_name":`), nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := GitHubReleaseSource{
				Client:   &http.Client{Transport: tt.transport},
				Endpoint: "https://offline.invalid/latest",
			}
			checker := NewChecker(source, filepath.Join(t.TempDir(), "update.json"))
			if result, ok := checker.Check(context.Background(), "1.0.0", true); ok || result != (Result{}) {
				t.Fatalf("failure result = (%#v, %t)", result, ok)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func TestGitHubReleaseSourceOfflineResponses(t *testing.T) {
	tests := []struct {
		name      string
		transport roundTripFunc
		want      string
		wantErr   bool
	}{
		{
			name: "success",
			transport: func(req *http.Request) (*http.Response, error) {
				if got := req.Header.Get("User-Agent"); got != "termp/1.2.3" {
					t.Fatalf("User-Agent = %q", got)
				}
				if req.Header.Get("Authorization") != "" {
					t.Fatal("anonymous request unexpectedly has Authorization")
				}
				return response(http.StatusOK, `{"tag_name":"v1.3.0"}`), nil
			},
			want: "v1.3.0",
		},
		{name: "network error", transport: func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		}, wantErr: true},
		{name: "non-200", transport: func(*http.Request) (*http.Response, error) {
			return response(http.StatusInternalServerError, `{"message":"nope"}`), nil
		}, wantErr: true},
		{name: "malformed JSON", transport: func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, `{"tag_name":`), nil
		}, wantErr: true},
		{name: "missing tag", transport: func(*http.Request) (*http.Response, error) {
			return response(http.StatusOK, `{}`), nil
		}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := GitHubReleaseSource{
				Client:   &http.Client{Transport: tt.transport},
				Endpoint: "https://offline.invalid/latest",
			}
			got, err := source.Latest(context.Background(), "1.2.3")
			if (err != nil) != tt.wantErr {
				t.Fatalf("Latest() error = %v, wantErr %t", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("Latest() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultCachePathUsesXDGCacheHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", root)
	want := filepath.Join(root, "termp", "update-check.json")
	if got := DefaultCachePath(); got != want {
		t.Fatalf("DefaultCachePath() = %q, want %q", got, want)
	}
}

func TestDefaultCachePathUsesXDGDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".cache", "termp", "update-check.json")
	if got := DefaultCachePath(); got != want {
		t.Fatalf("DefaultCachePath() = %q, want %q", got, want)
	}
}
