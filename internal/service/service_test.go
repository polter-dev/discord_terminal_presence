package service

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

type recordingRunner struct {
	calls []string
	fail  map[string]error
	out   map[string]string
}

type blockingContextRunner struct {
	contextCalls int
}

type windowsInstallRunner struct {
	calls   [][]string
	xmlPath string
	xmlData []byte
	created bool
}

type foreignThenInstalledRunner struct {
	*windowsInstallRunner
	foreignXML string
	firstQuery string
}

type scriptedRunnerResult struct {
	out string
	err error
}

type scriptedRunner struct {
	calls   []string
	results map[string]scriptedRunnerResult
}

type sequenceRunner struct {
	calls   []string
	results map[string][]scriptedRunnerResult
}

type windowsRollbackRunner struct {
	calls       []string
	taskXML     []byte
	running     bool
	runFailures int
}

type simulatedExitError struct {
	code int
}

func (e simulatedExitError) Error() string {
	return fmt.Sprintf("exit status %d", e.code)
}

func (e simulatedExitError) ExitCode() int {
	return e.code
}

func (r *scriptedRunner) Run(name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	result := r.results[call]
	return []byte(result.out), result.err
}

func (r *sequenceRunner) Run(name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	results := r.results[call]
	if len(results) == 0 {
		return nil, nil
	}
	result := results[0]
	r.results[call] = results[1:]
	return []byte(result.out), result.err
}

func (r *windowsRollbackRunner) Run(name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if name != "schtasks" || len(args) == 0 {
		return nil, nil
	}
	switch args[0] {
	case "/Query":
		if hasArg(args, "/XML") {
			if r.taskXML == nil {
				return []byte("ERROR: The specified task name does not exist.\n"), simulatedExitError{code: 1}
			}
			return append([]byte(nil), r.taskXML...), nil
		}
		if hasArg(args, "/V") {
			result := "1"
			if r.running {
				result = "0x41301"
			}
			return []byte(`"COMPUTER","` + TaskName + `","N/A","Ready","Interactive","N/A","` + result + `"` + "\r\n"), nil
		}
		if r.taskXML == nil {
			return nil, simulatedExitError{code: 1}
		}
		return []byte(`"` + TaskName + `","N/A","Ready"` + "\r\n"), nil
	case "/Create":
		for i := 0; i < len(args)-1; i++ {
			if args[i] != "/XML" {
				continue
			}
			data, err := os.ReadFile(args[i+1])
			if err != nil {
				return nil, err
			}
			r.taskXML = append([]byte(nil), data...)
			r.running = false
			return nil, nil
		}
	case "/Run":
		if r.runFailures > 0 {
			r.runFailures--
			return []byte("ERROR: The task could not be started.\n"), simulatedExitError{code: 1}
		}
		r.running = true
		return nil, nil
	case "/End":
		r.running = false
		return nil, nil
	case "/Delete":
		r.taskXML = nil
		r.running = false
		return nil, nil
	}
	return nil, nil
}

func (*blockingContextRunner) Run(string, ...string) ([]byte, error) {
	panic("StatusContext used Runner.Run instead of Runner.RunContext")
}

func (r *blockingContextRunner) RunContext(ctx context.Context, _ string, _ ...string) ([]byte, error) {
	r.contextCalls++
	<-ctx.Done()
	return nil, ctx.Err()
}

func (r *recordingRunner) Run(name string, args ...string) ([]byte, error) {
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if err := r.fail[call]; err != nil {
		return []byte(r.out[call]), err
	}
	if call == "schtasks /Query /TN "+TaskName+" /FO CSV /NH" && r.out[call] == "" {
		xmlQuery := "schtasks /Query /TN " + TaskName + " /XML"
		if strings.HasPrefix(strings.TrimSpace(r.out[xmlQuery]), "<") {
			return []byte(`"` + TaskName + `","N/A","Ready"` + "\n"), nil
		}
		return nil, simulatedExitError{code: 1}
	}
	if call == "schtasks /Query /FO CSV /NH" && r.out[call] == "" {
		xmlQuery := "schtasks /Query /TN " + TaskName + " /XML"
		if strings.HasPrefix(strings.TrimSpace(r.out[xmlQuery]), "<") {
			return []byte(`"` + TaskName + `","N/A","Ready"` + "\n"), nil
		}
		return nil, nil
	}
	if name == "schtasks" && len(args) > 0 && args[0] == "/Delete" {
		query := "schtasks /Query /TN " + TaskName + " /XML"
		r.fail[query] = errors.New("exit status 1")
		r.out[query] = "ERROR: The specified task name does not exist in the system.\n"
		r.out["schtasks /Query /FO CSV /NH"] = ""
	}
	return []byte(r.out[call]), nil
}

func (r *windowsInstallRunner) Run(name string, args ...string) ([]byte, error) {
	call := append([]string{name}, args...)
	r.calls = append(r.calls, call)
	if name == "schtasks" && len(args) > 0 && args[0] == "/Create" {
		r.created = true
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "/XML" {
				r.xmlPath = args[i+1]
				data, err := os.ReadFile(r.xmlPath)
				if err != nil {
					return nil, err
				}
				r.xmlData = data
				break
			}
		}
	}
	if name == "schtasks" && len(args) > 0 && args[0] == "/Query" {
		if hasArg(args, "/FO") {
			if !r.created {
				if hasArg(args, "/TN") {
					return nil, simulatedExitError{code: 1}
				}
				return nil, nil
			}
			return []byte(`"` + TaskName + `","N/A","Ready"` + "\n"), nil
		}
		if !r.created {
			return []byte("ERROR: The specified task name does not exist in the system.\n"), errors.New("exit status 1")
		}
		return []byte(`<Task><Actions><Exec><Command>C:\Program Files &amp; Tools\&lt;termp&gt;\termp.exe</Command></Exec></Actions><Settings><Enabled>true</Enabled></Settings></Task>`), nil
	}
	return nil, nil
}

func (r *foreignThenInstalledRunner) Run(name string, args ...string) ([]byte, error) {
	if name == "schtasks" && len(args) > 0 && args[0] == "/Query" && hasArg(args, "/FO") && !r.created {
		call := append([]string{name}, args...)
		r.calls = append(r.calls, call)
		return []byte(`"` + TaskName + `","N/A","Ready"` + "\n"), nil
	}
	if name == "schtasks" && len(args) > 0 && args[0] == "/Query" && hasArg(args, "/XML") && !r.created {
		call := append([]string{name}, args...)
		r.calls = append(r.calls, call)
		if r.firstQuery == "" {
			r.firstQuery = strings.Join(call, " ")
		}
		return []byte(r.foreignXML), nil
	}
	return r.windowsInstallRunner.Run(name, args...)
}

func fakeHome(t *testing.T) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	return home
}

func requireGOOS(t *testing.T, goos string) {
	t.Helper()
	if runtime.GOOS != goos {
		t.Skip(goos + "-only")
	}
}

func requireSymlink(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "target"), filepath.Join(dir, "link")); err != nil {
		t.Skipf("symlinks unavailable on this account: %v", err)
	}
}

func TestLaunchAgentPathUsesHomeAndLabel(t *testing.T) {
	requireGOOS(t, "darwin")
	home := fakeHome(t)
	path, err := launchAgentPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if path != want {
		t.Fatalf("launchAgentPath() = %q, want %q", path, want)
	}
}

func TestSystemdUnitPathUsesHome(t *testing.T) {
	requireGOOS(t, "linux")
	home := fakeHome(t)
	path, err := systemdUnitPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".config", "systemd", "user", ServiceName)
	if path != want {
		t.Fatalf("systemdUnitPath() = %q, want %q", path, want)
	}
}

func TestSystemdUnitPathUsesXDGConfigHome(t *testing.T) {
	fakeHome(t)
	configHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path, err := systemdUnitPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(configHome, "systemd", "user", ServiceName)
	if path != want {
		t.Fatalf("systemdUnitPath() = %q, want %q", path, want)
	}
}

func TestUnstableExecutablePath(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(repo, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module github.com/polter-dev/discord_terminal_presence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !isTermpSourceTree(repo) {
		t.Fatal("termp checkout was not recognized as a source tree")
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{name: "git worktree", path: filepath.Join(repo, "build", "termp"), want: true},
		{name: "os temp", path: filepath.Join(os.TempDir(), "termp"), want: true},
		{name: "tmp", path: "/tmp/build/termp", want: true},
		{name: "private tmp", path: "/private/tmp/build/termp", want: true},
		{name: "private var folders", path: "/private/var/folders/ab/cache/termp", want: true},
		{name: "homebrew cellar", path: "/opt/homebrew/Cellar/termp/1.2.3/bin/termp", want: false},
		{name: "homebrew caskroom", path: "/opt/homebrew/Caskroom/termp/1.2.3/termp", want: false},
		{name: "usr local", path: "/usr/local/bin/termp", want: false},
		{name: "local bin", path: filepath.Join(string(filepath.Separator), "Users", "alice", ".local", "bin", "termp"), want: false},
		{name: "similar tmp prefix", path: "/tmp-stable/termp", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUnstableExecutablePath(tt.path); got != tt.want {
				t.Fatalf("isUnstableExecutablePath(%q) = %t, want %t", tt.path, got, tt.want)
			}
		})
	}
}

func TestHomebrewCheckoutAncestorIsNotTermpSourceTree(t *testing.T) {
	prefix := filepath.Join(t.TempDir(), "homebrew")
	if err := os.MkdirAll(filepath.Join(prefix, "Cellar", "termp", "1.2.3", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prefix, "go.mod"), []byte("module github.com/Homebrew/brew\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if isTermpSourceTree(prefix) {
		t.Fatal("Homebrew prefix was mistaken for the termp source tree")
	}
}

func TestValidateInstallExecutableResolvesNestedSymlinkAndHonorsForce(t *testing.T) {
	requireSymlink(t)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/polter-dev/discord_terminal_presence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	buildDir := filepath.Join(dir, "build")
	if err := os.MkdirAll(buildDir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(buildDir, "termp-real")
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	link1 := filepath.Join(dir, "termp-link-1")
	link2 := filepath.Join(dir, "termp-link-2")
	if err := os.Symlink(target, link1); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(link1, link2); err != nil {
		t.Fatal(err)
	}

	if _, err := ValidateInstallExecutable(link2, false); err == nil {
		t.Fatal("ValidateInstallExecutable() error = nil for unstable resolved path")
	} else {
		for _, want := range []string{"unstable executable path", "~/.local/bin", "/usr/local/bin", "--force"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("ValidateInstallExecutable() error missing %q: %v", want, err)
			}
		}
	}

	got, err := ValidateInstallExecutable(link2, true)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(link2)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("ValidateInstallExecutable(force) = %q, want stable invocation path %q", got, want)
	}
}

func TestValidateInstallExecutableMissingPathErrorAndForceBypass(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "missing-parent", "termp")
	absolutePath, err := filepath.Abs(exe)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ValidateInstallExecutable(exe, false); err == nil {
		t.Fatal("ValidateInstallExecutable() error = nil for missing parent")
	} else {
		// Windows can render the path in 8.3 short form (e.g. RUNNER~1), so
		// assert on the actionable wrapper text and the target leaf rather than
		// the full long-form absolute path.
		for _, want := range []string{"resolve executable symlinks", "termp"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("ValidateInstallExecutable() error missing %q: %v", want, err)
			}
		}
		_ = absolutePath
	}

	got, err := ValidateInstallExecutable(exe, true)
	if err != nil {
		t.Fatalf("ValidateInstallExecutable(force) error = %v", err)
	}
	if got != absolutePath {
		t.Fatalf("ValidateInstallExecutable(force) = %q, want %q", got, absolutePath)
	}
}

func TestBuildLaunchAgentPlist(t *testing.T) {
	content, err := BuildLaunchAgentPlist("/opt/Term Presence/termp")
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	for _, want := range []string{
		"<string>" + Label + "</string>",
		"<string>/opt/Term Presence/termp</string>",
		"<string>start</string>",
		"<string>--foreground</string>",
		"<string>--internal-daemon-log</string>",
		"<key>RunAtLoad</key>\n\t<true/>",
		"<key>KeepAlive</key>\n\t<true/>",
		"<key>StandardOutPath</key>\n\t<string>/dev/null</string>",
		"<key>StandardErrorPath</key>\n\t<string>/dev/null</string>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plist missing %q:\n%s", want, text)
		}
	}
}

func TestBuildSystemdUnit(t *testing.T) {
	unit, err := BuildSystemdUnit("/opt/%u Term Presence/termp")
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	for _, want := range []string{
		"[Unit]",
		"Description=termp Discord Rich Presence daemon",
		"[Service]",
		`ExecStart="/opt/%%u Term Presence/termp" start --foreground`,
		"Restart=on-failure",
		"[Install]",
		"WantedBy=default.target",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("unit missing %q:\n%s", want, text)
		}
	}
}

func TestBuildSystemdUnitRejectsLineBreaks(t *testing.T) {
	for _, lineBreak := range []string{"\r", "\n"} {
		t.Run(fmt.Sprintf("%q", lineBreak), func(t *testing.T) {
			if _, err := BuildSystemdUnit("/opt/termp" + lineBreak + "injected"); err == nil {
				t.Fatalf("BuildSystemdUnit accepted executable path containing %q", lineBreak)
			}
		})
	}
}

func TestServiceDefinitionExecutableParsers(t *testing.T) {
	t.Run("systemd unit", func(t *testing.T) {
		unit := []byte(`[Unit]
Description=fixture
[Service]
ExecStart="/opt/%%u Term Presence/termp" start --foreground
`)
		got, err := systemdUnitExecutable(unit)
		if err != nil {
			t.Fatal(err)
		}
		if want := "/opt/%u Term Presence/termp"; got != want {
			t.Fatalf("systemdUnitExecutable() = %q, want %q", got, want)
		}
	})

	t.Run("launchd plist", func(t *testing.T) {
		plist := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<plist><dict>
<key>Label</key><string>fixture</string>
<key>ProgramArguments</key>
<array><string>/opt/Term &amp; Presence/termp</string><string>start</string></array>
</dict></plist>`)
		got, err := launchAgentExecutable(plist)
		if err != nil {
			t.Fatal(err)
		}
		if want := "/opt/Term & Presence/termp"; got != want {
			t.Fatalf("launchAgentExecutable() = %q, want %q", got, want)
		}
	})
}

func TestSameUnixExecutableResolvesSymlinksAndCleansPaths(t *testing.T) {
	requireSymlink(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "bin", "termp")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "termp")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if !sameUnixExecutable(filepath.Join(dir, "bin", "..", "bin", "termp"), link) {
		t.Fatalf("sameUnixExecutable() = false for equivalent clean and symlink paths")
	}
	if sameUnixExecutable(target, filepath.Join(dir, "other", "termp")) {
		t.Fatalf("sameUnixExecutable() = true for different paths")
	}
}

func TestDarwinInstallWritesPlistWithoutRealLaunchctl(t *testing.T) {
	requireGOOS(t, "darwin")
	home := fakeHome(t)
	runner := &recordingRunner{
		fail: map[string]error{
			"launchctl bootout gui/" + userID() + " " + filepath.Join(home, "Library", "LaunchAgents", Label+".plist"): errors.New("not loaded"),
		},
		out: map[string]string{},
	}
	manager := Manager{GOOS: "darwin", Runner: runner}
	state, err := manager.Install("/bin/termp", false)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed {
		t.Fatal("state.Installed = false, want true")
	}
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "<string>/bin/termp</string>") ||
		!strings.Contains(text, "<string>start</string>") ||
		!strings.Contains(text, "<string>--foreground</string>") {
		t.Fatalf("plist missing foreground start invocation:\n%s", text)
	}
	if len(runner.calls) == 0 {
		t.Fatal("runner was not called")
	}
}

func TestDarwinInstallRollsBackDefinitionWhenLoadFails(t *testing.T) {
	tests := []struct {
		name            string
		original        []byte
		initiallyLoaded bool
		wantLoadCalls   int
	}{
		{name: "removes new definition", wantLoadCalls: 1},
		{name: "restores loaded definition", original: []byte("old launch agent"), initiallyLoaded: true, wantLoadCalls: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := fakeHome(t)
			path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
			if tt.original != nil {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, tt.original, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			printCall := "launchctl print gui/" + userID() + "/" + Label
			bootoutCall := "launchctl bootout gui/" + userID() + " " + path
			bootstrapCall := "launchctl bootstrap gui/" + userID() + " " + path
			loadCall := "launchctl load -w " + path
			results := map[string][]scriptedRunnerResult{
				bootoutCall: {
					{},
					{},
				},
				bootstrapCall: {
					{out: "Bootstrap failed: Operation not permitted\n", err: errors.New("exit status 5")},
				},
				loadCall: {
					{out: "Load failed: Operation not permitted\n", err: errors.New("exit status 5")},
				},
			}
			if tt.initiallyLoaded {
				results[printCall] = []scriptedRunnerResult{{out: "service data\n"}}
				results[bootstrapCall] = append(results[bootstrapCall], scriptedRunnerResult{})
			}
			runner := &sequenceRunner{results: results}

			_, err := (Manager{GOOS: "darwin", Runner: runner}).Install("/new/termp", true)
			if err == nil || !strings.Contains(err.Error(), "launchctl load failed") {
				t.Fatalf("Install() error = %v, want launchctl load failure", err)
			}
			got, readErr := os.ReadFile(path)
			if tt.original == nil {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("failed install left launch agent definition: %v", readErr)
				}
			} else {
				if readErr != nil {
					t.Fatal(readErr)
				}
				if string(got) != string(tt.original) {
					t.Fatalf("failed install left definition %q, want %q", got, tt.original)
				}
			}
			if got := countCall(runner.calls, bootstrapCall); got != tt.wantLoadCalls {
				t.Fatalf("bootstrap calls = %d, want %d; calls: %#v", got, tt.wantLoadCalls, runner.calls)
			}
		})
	}
}

func TestDarwinInstallDoesNotOverwritePlistOnUnloadFailure(t *testing.T) {
	requireGOOS(t, "darwin")
	home := fakeHome(t)
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte("old launch agent")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}
	bootout := "launchctl bootout gui/" + userID() + " " + path
	unload := "launchctl unload -w " + path
	runner := &recordingRunner{
		fail: map[string]error{
			bootout: errors.New("exit status 1"),
			unload:  errors.New("exit status 1"),
		},
		out: map[string]string{
			bootout: "Boot-out failed: Operation not permitted\n",
			unload:  "Unload failed: Operation not permitted\n",
		},
	}

	state, err := (Manager{GOOS: "darwin", Runner: runner}).Install("/new/termp", false)
	if err == nil || !strings.Contains(err.Error(), "Operation not permitted") {
		t.Fatalf("Install() error = %v, want unload permission failure", err)
	}
	if !state.Supported || state.Path != path {
		t.Fatalf("Install() state = %+v, want supported service path %q", state, path)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("plist overwritten after unload failure: got %q, want %q", got, original)
	}
	if hasCall(runner.calls, "launchctl bootstrap gui/"+userID()+" "+path) || hasCall(runner.calls, "launchctl load -w "+path) {
		t.Fatalf("load attempted after unload failure: %#v", runner.calls)
	}
}

func TestDarwinInstallReplacesPlistWhenAlreadyUnloaded(t *testing.T) {
	requireGOOS(t, "darwin")
	home := fakeHome(t)
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old launch agent"), 0o644); err != nil {
		t.Fatal(err)
	}
	bootout := "launchctl bootout gui/" + userID() + " " + path
	runner := &recordingRunner{
		fail: map[string]error{bootout: errors.New("exit status 3")},
		out:  map[string]string{bootout: "Boot-out failed: 3: No such process\n"},
	}

	state, err := (Manager{GOOS: "darwin", Runner: runner}).Install("/new/termp", false)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed {
		t.Fatalf("Install() state = %+v, want installed", state)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.Contains(string(got), "<string>/new/termp</string>") ||
		!strings.Contains(string(got), "<string>--foreground</string>") {
		t.Fatalf("plist was not replaced after benign absent result:\n%s", got)
	}
	if hasCall(runner.calls, "launchctl unload -w "+path) {
		t.Fatalf("legacy unload called after bootout proved service absent: %#v", runner.calls)
	}
}

func TestDarwinDisableAndEnableToggleLaunchAgentWithoutRemovingPlist(t *testing.T) {
	requireGOOS(t, "darwin")
	home := fakeHome(t)
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}
	manager := Manager{GOOS: "darwin", Runner: runner}

	if _, err := manager.Disable(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("plist should remain after Disable: %v", err)
	}
	if !hasCall(runner.calls, "launchctl bootout gui/"+userID()+" "+path) {
		t.Fatalf("Disable calls = %#v, want launchctl bootout", runner.calls)
	}

	runner.calls = nil
	if _, err := manager.Enable(); err != nil {
		t.Fatal(err)
	}
	if !hasCall(runner.calls, "launchctl bootstrap gui/"+userID()+" "+path) {
		t.Fatalf("Enable calls = %#v, want launchctl bootstrap", runner.calls)
	}
}

func TestDarwinDisableAndEnableAreIdempotent(t *testing.T) {
	home := fakeHome(t)
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		fail map[string]error
		out  map[string]string
		run  func(Manager) (State, error)
	}{
		{
			name: "disable already unloaded",
			fail: map[string]error{
				"launchctl bootout gui/" + userID() + " " + path: errors.New("not loaded"),
				"launchctl unload -w " + path:                    errors.New("not loaded"),
			},
			out: map[string]string{
				"launchctl unload -w " + path: "Could not find specified service\n",
			},
			run: func(m Manager) (State, error) { return m.Disable() },
		},
		{
			name: "enable already loaded",
			fail: map[string]error{
				"launchctl bootstrap gui/" + userID() + " " + path: errors.New("already loaded"),
				"launchctl load -w " + path:                        errors.New("already loaded"),
			},
			out: map[string]string{
				"launchctl load -w " + path: "service already loaded\n",
			},
			run: func(m Manager) (State, error) { return m.Enable() },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{fail: tt.fail, out: tt.out}
			manager := Manager{GOOS: "darwin", Runner: runner}
			if _, err := tt.run(manager); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDarwinDisableEnableMissingPlistReturnStatusWithoutLaunchctl(t *testing.T) {
	requireGOOS(t, "darwin")
	fakeHome(t)
	runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}
	manager := Manager{GOOS: "darwin", Runner: runner}

	state, err := manager.Disable()
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed {
		t.Fatalf("Disable state = %+v, want not installed", state)
	}
	state, err = manager.Enable()
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed {
		t.Fatalf("Enable state = %+v, want not installed", state)
	}
	for _, call := range runner.calls {
		if strings.Contains(call, "bootout") || strings.Contains(call, "bootstrap") || strings.Contains(call, "load") || strings.Contains(call, "unload") {
			t.Fatalf("unexpected launchctl toggle call for missing plist: %#v", runner.calls)
		}
	}
}

func TestDarwinStatusMapsLaunchctlErrors(t *testing.T) {
	requireGOOS(t, "darwin")
	home := fakeHome(t)
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	call := "launchctl print gui/" + userID() + "/" + Label

	tests := []struct {
		name       string
		output     string
		wantLoaded string
	}{
		{
			name:       "service not found",
			output:     "Could not find service \"" + Label + "\" in domain for user gui: " + userID() + "\n",
			wantLoaded: "false",
		},
		{
			name:       "execution error",
			output:     "Operation not permitted\n",
			wantLoaded: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{
				fail: map[string]error{call: errors.New("exit status 1")},
				out:  map[string]string{call: tt.output},
			}
			state := (Manager{GOOS: "darwin", Runner: runner}).Status()
			if !state.Installed || state.Loaded != tt.wantLoaded || state.Enabled != "n/a" {
				t.Fatalf("Status() = %+v, want installed=true loaded=%q enabled=n/a", state, tt.wantLoaded)
			}
		})
	}
}

func TestDarwinUninstallKeepsPlistOnUnloadFailure(t *testing.T) {
	requireGOOS(t, "darwin")
	home := fakeHome(t)
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	bootout := "launchctl bootout gui/" + userID() + " " + path
	unload := "launchctl unload -w " + path
	runner := &recordingRunner{
		fail: map[string]error{
			bootout: errors.New("exit status 1"),
			unload:  errors.New("exit status 1"),
		},
		out: map[string]string{
			bootout: "Boot-out failed: Operation not permitted\n",
			unload:  "Unload failed: Operation not permitted\n",
		},
	}

	state, err := (Manager{GOOS: "darwin", Runner: runner}).Uninstall(false)
	if err == nil || !strings.Contains(err.Error(), "Operation not permitted") {
		t.Fatalf("Uninstall() error = %v, want permission failure", err)
	}
	if !state.Installed || state.Path != path {
		t.Fatalf("Uninstall() state = %+v, want installed definition at %q", state, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("plist removed after unload failure: %v", err)
	}
}

func TestDarwinUninstallRemovesPlistWhenAlreadyUnloaded(t *testing.T) {
	requireGOOS(t, "darwin")
	home := fakeHome(t)
	path := filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("plist"), 0o644); err != nil {
		t.Fatal(err)
	}
	bootout := "launchctl bootout gui/" + userID() + " " + path
	runner := &recordingRunner{
		fail: map[string]error{bootout: errors.New("exit status 3")},
		out:  map[string]string{bootout: "Boot-out failed: 3: No such process\n"},
	}

	state, err := (Manager{GOOS: "darwin", Runner: runner}).Uninstall(false)
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed {
		t.Fatalf("Uninstall() state = %+v, want not installed", state)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plist still exists after benign unload failure: %v", err)
	}
	if hasCall(runner.calls, "launchctl unload -w "+path) {
		t.Fatalf("legacy unload called after bootout proved service absent: %#v", runner.calls)
	}
}

func TestDarwinUninstallAbsentIsNoOp(t *testing.T) {
	fakeHome(t)
	runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}

	state, err := (Manager{GOOS: "darwin", Runner: runner}).Uninstall(false)
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed {
		t.Fatalf("Uninstall() state = %+v, want not installed", state)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("Uninstall() calls = %#v, want no launchctl calls", runner.calls)
	}
}

func TestLinuxInstallWritesUnitWithoutRealSystemctl(t *testing.T) {
	requireGOOS(t, "linux")
	home := fakeHome(t)
	runner := &recordingRunner{
		fail: map[string]error{},
		out: map[string]string{
			"systemctl --user is-enabled " + ServiceName: "enabled\n",
			"systemctl --user is-active " + ServiceName:  "active\n",
		},
	}
	manager := Manager{GOOS: "linux", Runner: runner}
	state, err := manager.Install("/bin/termp", false)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed || state.Enabled != "enabled" || state.Loaded != "active" {
		t.Fatalf("state = %+v, want installed enabled active", state)
	}
	path := filepath.Join(home, ".config", "systemd", "user", ServiceName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "ExecStart=/bin/termp start --foreground") || !strings.Contains(text, "Restart=on-failure") {
		t.Fatalf("unit missing executable/restart:\n%s", text)
	}
}

func TestLinuxInstallRollsBackDefinitionWhenActivationFails(t *testing.T) {
	tests := []struct {
		name           string
		original       []byte
		activationCall string
		wantDisable    bool
	}{
		{name: "removes new definition after reload failure", activationCall: "systemctl --user daemon-reload"},
		{name: "restores disabled definition after enable failure", original: []byte("old systemd unit"), activationCall: "systemctl --user enable --now " + ServiceName, wantDisable: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := fakeHome(t)
			path := filepath.Join(home, ".config", "systemd", "user", ServiceName)
			if tt.original != nil {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, tt.original, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			reloadCall := "systemctl --user daemon-reload"
			results := map[string][]scriptedRunnerResult{
				"systemctl --user is-enabled " + ServiceName: {{out: "disabled\n"}},
				"systemctl --user is-active " + ServiceName:  {{out: "inactive\n"}},
				reloadCall:        {{}, {}},
				tt.activationCall: {{out: "activation failed\n", err: errors.New("exit status 1")}},
			}
			if tt.activationCall == reloadCall {
				results[reloadCall] = []scriptedRunnerResult{
					{out: "reload failed\n", err: errors.New("exit status 1")},
					{},
				}
			}
			runner := &sequenceRunner{results: results}

			_, err := (Manager{GOOS: "linux", Runner: runner}).Install("/new/termp", true)
			if err == nil {
				t.Fatal("Install() error = nil, want activation failure")
			}
			got, readErr := os.ReadFile(path)
			if tt.original == nil {
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("failed install left systemd unit: %v", readErr)
				}
			} else {
				if readErr != nil {
					t.Fatal(readErr)
				}
				if string(got) != string(tt.original) {
					t.Fatalf("failed install left definition %q, want %q", got, tt.original)
				}
			}
			disableCall := "systemctl --user disable --now " + ServiceName
			if got := hasCall(runner.calls, disableCall); got != tt.wantDisable {
				t.Fatalf("disable rollback called = %t, want %t; calls: %#v", got, tt.wantDisable, runner.calls)
			}
		})
	}
}

func TestUnixMutationsRefuseForeignDefinitions(t *testing.T) {
	platforms := []struct {
		name       string
		goos       string
		definition func(exe string) ([]byte, error)
		path       func() (string, error)
	}{
		{
			name: "linux",
			goos: "linux",
			definition: func(exe string) ([]byte, error) {
				return BuildSystemdUnit(exe)
			},
			path: systemdUnitPath,
		},
		{
			name: "darwin",
			goos: "darwin",
			definition: func(exe string) ([]byte, error) {
				return BuildLaunchAgentPlist(exe)
			},
			path: launchAgentPath,
		},
	}
	actions := []struct {
		name string
		run  func(Manager, string) (State, error)
	}{
		{name: "install", run: func(m Manager, exe string) (State, error) {
			return m.Install(exe, false)
		}},
		{name: "install definition", run: func(m Manager, exe string) (State, error) {
			return m.InstallDefinition(exe, false)
		}},
		{name: "uninstall", run: func(m Manager, _ string) (State, error) {
			return m.Uninstall(false)
		}},
		{name: "disable", run: func(m Manager, _ string) (State, error) {
			return m.Disable()
		}},
		{name: "enable", run: func(m Manager, _ string) (State, error) {
			return m.Enable()
		}},
	}

	for _, platform := range platforms {
		for _, action := range actions {
			t.Run(platform.name+"/"+action.name, func(t *testing.T) {
				fakeHome(t)
				dir := t.TempDir()
				current := filepath.Join(dir, "current", "termp")
				foreign := filepath.Join(dir, "foreign", "termp")
				for _, exe := range []string{current, foreign} {
					if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
						t.Fatal(err)
					}
				}
				path, err := platform.path()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				original, err := platform.definition(foreign)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, original, 0o644); err != nil {
					t.Fatal(err)
				}
				runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}
				manager := Manager{
					GOOS:       platform.goos,
					Runner:     runner,
					Executable: current,
				}

				state, err := action.run(manager, current)
				if err == nil || !strings.Contains(err.Error(), "different installation") ||
					!strings.Contains(err.Error(), "--force") {
					t.Fatalf("%s() error = %v, want ownership refusal", action.name, err)
				}
				if !state.ForeignTask || state.Installed {
					t.Fatalf("%s() state = %+v, want foreign definition", action.name, state)
				}
				got, readErr := os.ReadFile(path)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if string(got) != string(original) {
					t.Fatalf("%s() changed foreign definition", action.name)
				}
				for _, call := range runner.calls {
					if strings.Contains(call, " daemon-reload") ||
						strings.Contains(call, " enable") ||
						strings.Contains(call, " disable") ||
						strings.Contains(call, " bootstrap") ||
						strings.Contains(call, " bootout") ||
						strings.Contains(call, " load ") ||
						strings.Contains(call, " unload ") {
						t.Fatalf("%s() made mutation call %q for foreign definition", action.name, call)
					}
				}
			})
		}
	}
}

func TestUnixUnreadableDefinitionsRequireForce(t *testing.T) {
	platforms := []struct {
		name string
		goos string
		path func() (string, error)
	}{
		{name: "linux", goos: "linux", path: systemdUnitPath},
		{name: "darwin", goos: "darwin", path: launchAgentPath},
	}

	for _, platform := range platforms {
		t.Run(platform.name+"/without force", func(t *testing.T) {
			fakeHome(t)
			path, err := platform.path()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("existing definition"), 0o600); err != nil {
				t.Fatal(err)
			}
			makeUnreadable(t, path)

			runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}
			manager := Manager{GOOS: platform.goos, Runner: runner, Executable: "/opt/termp"}
			state, err := manager.InstallDefinition("/opt/termp", false)
			if err == nil ||
				!strings.Contains(err.Error(), path) ||
				!strings.Contains(err.Error(), "ownership could not be verified") ||
				!strings.Contains(err.Error(), "--force") {
				t.Fatalf("InstallDefinition() error = %v, want actionable ownership refusal for %s", err, path)
			}
			if !state.ForeignTask {
				t.Fatalf("InstallDefinition() state = %+v, want ownership-unknown definition", state)
			}
			if len(runner.calls) != 0 {
				t.Fatalf("InstallDefinition() calls = %#v, want no runner invocation", runner.calls)
			}
		})

		t.Run(platform.name+"/with force", func(t *testing.T) {
			fakeHome(t)
			path, err := platform.path()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("existing definition"), 0o600); err != nil {
				t.Fatal(err)
			}
			makeUnreadable(t, path)

			runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}
			manager := Manager{GOOS: platform.goos, Runner: runner, Executable: "/opt/termp"}
			state, err := manager.Uninstall(true)
			if err != nil {
				t.Fatalf("Uninstall(force) error = %v, want mutation to proceed", err)
			}
			if state.Installed || state.ForeignTask {
				t.Fatalf("Uninstall(force) state = %+v, want absent definition", state)
			}
			if len(runner.calls) == 0 {
				t.Fatal("Uninstall(force) did not invoke the service runner")
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("Uninstall(force) left definition behind: %v", err)
			}
		})
	}
}

func TestUnixReadableOwnedAndUnparseableDefinitionsProceed(t *testing.T) {
	platforms := []struct {
		name        string
		goos        string
		definition  func(string) ([]byte, error)
		executable  func([]byte) (string, error)
		unparseable []byte
		path        func() (string, error)
	}{
		{
			name:        "linux",
			goos:        "linux",
			definition:  BuildSystemdUnit,
			executable:  systemdUnitExecutable,
			unparseable: []byte("[Service]\n"),
			path:        systemdUnitPath,
		},
		{
			name: "darwin",
			goos: "darwin",
			definition: func(exe string) ([]byte, error) {
				return BuildLaunchAgentPlist(exe)
			},
			executable:  launchAgentExecutable,
			unparseable: []byte("not a plist"),
			path:        launchAgentPath,
		},
	}

	for _, platform := range platforms {
		for _, fixture := range []struct {
			name       string
			definition func(string) ([]byte, error)
		}{
			{
				name: "unparseable",
				definition: func(string) ([]byte, error) {
					return platform.unparseable, nil
				},
			},
			{name: "owned", definition: platform.definition},
		} {
			t.Run(platform.name+"/"+fixture.name, func(t *testing.T) {
				fakeHome(t)
				path, err := platform.path()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				current := filepath.Join(t.TempDir(), "termp")
				original, err := fixture.definition(current)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, original, 0o644); err != nil {
					t.Fatal(err)
				}
				runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}
				manager := Manager{GOOS: platform.goos, Runner: runner, Executable: current}

				state, err := manager.InstallDefinition(current, false)
				if err != nil {
					t.Fatalf("InstallDefinition() error = %v, want mutation to proceed", err)
				}
				if !state.Installed || state.ForeignTask {
					t.Fatalf("InstallDefinition() state = %+v, want owned definition", state)
				}
				installed, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				target, err := platform.executable(installed)
				if err != nil {
					t.Fatal(err)
				}
				if target != current {
					t.Fatalf("InstallDefinition() target = %q, want %q", target, current)
				}
			})
		}
	}
}

func makeUnreadable(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("restore permissions for %s: %v", path, err)
		}
	})
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("test account can read mode-000 files")
	}
}

func TestUnixForceMutatesForeignDefinitions(t *testing.T) {
	platforms := []struct {
		name       string
		goos       string
		definition func(exe string) ([]byte, error)
		executable func([]byte) (string, error)
		path       func() (string, error)
	}{
		{
			name:       "linux",
			goos:       "linux",
			definition: BuildSystemdUnit,
			executable: systemdUnitExecutable,
			path:       systemdUnitPath,
		},
		{
			name: "darwin",
			goos: "darwin",
			definition: func(exe string) ([]byte, error) {
				return BuildLaunchAgentPlist(exe)
			},
			executable: launchAgentExecutable,
			path:       launchAgentPath,
		},
	}

	for _, platform := range platforms {
		t.Run(platform.name, func(t *testing.T) {
			fakeHome(t)
			dir := t.TempDir()
			current := filepath.Join(dir, "current", "termp")
			foreign := filepath.Join(dir, "foreign", "termp")
			for _, exe := range []string{current, foreign} {
				if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			path, err := platform.path()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			foreignDefinition, err := platform.definition(foreign)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, foreignDefinition, 0o644); err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}
			manager := Manager{
				GOOS:       platform.goos,
				Runner:     runner,
				Executable: current,
			}

			state, err := manager.Install(current, true)
			if err != nil {
				t.Fatal(err)
			}
			if !state.Installed || state.ForeignTask {
				t.Fatalf("forced Install() state = %+v, want owned definition", state)
			}
			installed, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			target, err := platform.executable(installed)
			if err != nil {
				t.Fatal(err)
			}
			if target != current {
				t.Fatalf("forced Install() target = %q, want %q", target, current)
			}

			if err := os.WriteFile(path, foreignDefinition, 0o644); err != nil {
				t.Fatal(err)
			}
			state, err = manager.Uninstall(true)
			if err != nil {
				t.Fatal(err)
			}
			if state.Installed || state.ForeignTask {
				t.Fatalf("forced Uninstall() state = %+v, want absent definition", state)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("forced Uninstall() left definition behind: %v", err)
			}
		})
	}
}

func TestLinuxDisableAndEnableToggleUserService(t *testing.T) {
	requireGOOS(t, "linux")
	home := fakeHome(t)
	path := filepath.Join(home, ".config", "systemd", "user", ServiceName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[Unit]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{
		fail: map[string]error{},
		out: map[string]string{
			"systemctl --user is-enabled " + ServiceName: "disabled\n",
			"systemctl --user is-active " + ServiceName:  "inactive\n",
		},
	}
	manager := Manager{GOOS: "linux", Runner: runner}

	if _, err := manager.Disable(); err != nil {
		t.Fatal(err)
	}
	if !hasCall(runner.calls, "systemctl --user disable --now "+ServiceName) {
		t.Fatalf("Disable calls = %#v, want systemctl disable --now", runner.calls)
	}

	runner.calls = nil
	if _, err := manager.Enable(); err != nil {
		t.Fatal(err)
	}
	if !hasCall(runner.calls, "systemctl --user enable --now "+ServiceName) {
		t.Fatalf("Enable calls = %#v, want systemctl enable --now", runner.calls)
	}
}

func TestLinuxStatusParsesDocumentedStatesDespiteNonzeroExit(t *testing.T) {
	fakeHome(t)
	enabledCall := "systemctl --user is-enabled " + ServiceName
	activeCall := "systemctl --user is-active " + ServiceName

	tests := []struct {
		name        string
		enabledOut  string
		activeOut   string
		wantEnabled string
		wantLoaded  string
	}{
		{
			name:        "disabled and inactive",
			enabledOut:  "disabled\n",
			activeOut:   "inactive\n",
			wantEnabled: "disabled",
			wantLoaded:  "inactive",
		},
		{
			name:        "masked and failed",
			enabledOut:  "masked\n",
			activeOut:   "failed\n",
			wantEnabled: "masked",
			wantLoaded:  "failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{
				fail: map[string]error{
					enabledCall: errors.New("exit status 1"),
					activeCall:  errors.New("exit status 3"),
				},
				out: map[string]string{
					enabledCall: tt.enabledOut,
					activeCall:  tt.activeOut,
				},
			}
			state := (Manager{GOOS: "linux", Runner: runner}).Status()
			if state.Enabled != tt.wantEnabled || state.Loaded != tt.wantLoaded {
				t.Fatalf("Status() = %+v, want enabled=%q loaded=%q", state, tt.wantEnabled, tt.wantLoaded)
			}
		})
	}
}

func TestLinuxStatusUsesUnknownForMissingOrUnrecognizedOutput(t *testing.T) {
	fakeHome(t)
	enabledCall := "systemctl --user is-enabled " + ServiceName
	activeCall := "systemctl --user is-active " + ServiceName

	tests := []struct {
		name       string
		enabledOut string
		activeOut  string
	}{
		{name: "transport failure", enabledOut: "", activeOut: ""},
		{name: "unrecognized output", enabledOut: "enabled-runtime\n", activeOut: "mystery\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{
				fail: map[string]error{
					enabledCall: errors.New("command failed"),
					activeCall:  errors.New("command failed"),
				},
				out: map[string]string{
					enabledCall: tt.enabledOut,
					activeCall:  tt.activeOut,
				},
			}
			state := (Manager{GOOS: "linux", Runner: runner}).Status()
			if state.Enabled != "unknown" || state.Loaded != "unknown" {
				t.Fatalf("Status() = %+v, want unknown states", state)
			}
		})
	}
}

func TestStatusContextBoundsHungServiceCommands(t *testing.T) {
	fakeHome(t)
	runner := &blockingContextRunner{}
	const budget = 40 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()

	started := time.Now()
	state := (Manager{GOOS: "linux", Runner: runner}).StatusContext(ctx)
	elapsed := time.Since(started)

	if elapsed < budget/2 || elapsed > 250*time.Millisecond {
		t.Fatalf("StatusContext() elapsed = %v, want approximately %v budget", elapsed, budget)
	}
	if state.Loaded != "unknown" || state.Enabled != "unknown" {
		t.Fatalf("StatusContext() = %+v, want unknown timed-out states", state)
	}
	if runner.contextCalls != 2 {
		t.Fatalf("RunContext calls = %d, want both bounded Linux status queries", runner.contextCalls)
	}
}

const (
	windowsEnabledTaskXML  = `<Task><Actions><Exec><Command>C:\termp.exe</Command></Exec></Actions><Settings><Enabled>true</Enabled></Settings></Task>`
	windowsDisabledTaskXML = `<Task><Actions><Exec><Command>C:\termp.exe</Command></Exec></Actions><Settings><Enabled>false</Enabled></Settings></Task>`
)

func TestWindowsInstallCreatesAndRunsLogonTaskWithoutRealSchtasks(t *testing.T) {
	runner := &windowsInstallRunner{}
	manager := Manager{GOOS: "windows", Runner: runner}
	state, err := manager.Install(`C:\Program Files & Tools\<termp>\termp.exe`, false)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed || state.Enabled != "true" || state.Loaded != "true" {
		t.Fatalf("state = %+v, want installed enabled true loaded true", state)
	}
	if len(runner.calls) < 1 {
		t.Fatal("runner was not called")
	}
	create := runner.calls[1]
	for _, want := range []string{"schtasks", "/Create", "/TN", TaskName, "/XML", "/F"} {
		if !hasArg(create, want) {
			t.Fatalf("create call missing %q:\n%#v", want, create)
		}
	}
	for _, absent := range []string{"/SC", "ONLOGON", "/TR", "/RU", "/IT", "/RL", "LIMITED"} {
		if hasArg(create, absent) {
			t.Fatalf("create call unexpectedly contains %q:\n%#v", absent, create)
		}
	}
	xmlText := decodeUTF16XML(t, runner.xmlData)
	for _, want := range []string{
		"<LogonTrigger>",
		"InteractiveToken",
		"LeastPrivilege",
		`<Command>C:\Program Files &amp; Tools\&lt;termp&gt;\termp.exe</Command>`,
		"<Arguments>start --foreground</Arguments>",
	} {
		if !strings.Contains(xmlText, want) {
			t.Fatalf("task XML missing %q:\n%s", want, xmlText)
		}
	}
	if strings.Contains(xmlText, `C:\Program Files & Tools\<termp>\termp.exe`) {
		t.Fatalf("task XML contains unescaped executable path:\n%s", xmlText)
	}
	if runner.xmlPath == "" {
		t.Fatal("create call did not include /XML path")
	}
	if _, err := os.Stat(runner.xmlPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp XML file still exists after Install: %v", err)
	}
	if !hasArgCall(runner.calls, "schtasks", "/Run", "/TN", TaskName) {
		t.Fatalf("Install calls = %#v, want immediate schtasks run", runner.calls)
	}
}

func TestWindowsInstallRollsBackTaskWhenRunFails(t *testing.T) {
	tests := []struct {
		name             string
		original         []byte
		initiallyRunning bool
		wantRunCalls     int
	}{
		{name: "removes new task", wantRunCalls: 1},
		{
			name:             "restores running task",
			original:         []byte(windowsEnabledTaskXML),
			initiallyRunning: true,
			wantRunCalls:     2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &windowsRollbackRunner{
				taskXML:     append([]byte(nil), tt.original...),
				running:     tt.initiallyRunning,
				runFailures: 1,
			}

			_, err := (Manager{GOOS: "windows", Runner: runner}).Install(`C:\termp.exe`, false)
			if err == nil || !strings.Contains(err.Error(), "schtasks run failed") {
				t.Fatalf("Install() error = %v, want schtasks run failure", err)
			}
			if string(runner.taskXML) != string(tt.original) {
				t.Fatalf("task definition after failed install = %q, want %q", runner.taskXML, tt.original)
			}
			if runner.running != tt.initiallyRunning {
				t.Fatalf("task running after failed install = %t, want %t", runner.running, tt.initiallyRunning)
			}
			runCall := "schtasks /Run /TN " + TaskName
			if got := countCall(runner.calls, runCall); got != tt.wantRunCalls {
				t.Fatalf("run calls = %d, want %d; calls: %#v", got, tt.wantRunCalls, runner.calls)
			}
		})
	}
}

func TestWindowsInstallDefinitionDoesNotRunTask(t *testing.T) {
	runner := &windowsInstallRunner{}
	manager := Manager{GOOS: "windows", Runner: runner}
	if _, err := manager.InstallDefinition(`C:\Program Files\termp\termp.exe`, false); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) < 2 || !hasArg(runner.calls[1], "/Create") ||
		!hasArg(runner.calls[1], "/TN") || !hasArg(runner.calls[1], TaskName) ||
		!hasArg(runner.calls[1], "/XML") || !hasArg(runner.calls[1], "/F") {
		t.Fatalf("InstallDefinition calls = %#v, want task definition reconciliation", runner.calls)
	}
	if hasArgCall(runner.calls, "schtasks", "/Run", "/TN", TaskName) {
		t.Fatalf("InstallDefinition calls = %#v, must not launch duplicate daemon", runner.calls)
	}
}

func TestWindowsInstallDefinitionReconcilesOwnedTaskWithoutLaunching(t *testing.T) {
	query := "schtasks /Query /TN " + TaskName + " /XML"
	runner := &recordingRunner{
		fail: map[string]error{},
		out:  map[string]string{query: windowsEnabledTaskXML},
	}

	state, err := (Manager{GOOS: "windows", Runner: runner}).
		InstallDefinition(`C:\termp.exe`, false)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Installed || state.ForeignTask {
		t.Fatalf("InstallDefinition state = %+v, want owned installed task", state)
	}
	if !slicesContainsPrefix(runner.calls, "schtasks /Create /TN "+TaskName+" ") {
		t.Fatalf("InstallDefinition calls = %#v, want existing definition rewritten", runner.calls)
	}
	if hasCall(runner.calls, "schtasks /Run /TN "+TaskName) {
		t.Fatalf("InstallDefinition calls = %#v, must not launch a duplicate daemon", runner.calls)
	}
}

func TestBuildWindowsTaskXMLWritesUTF16WithBOM(t *testing.T) {
	data, err := BuildWindowsTaskXML(`C:\termp.exe`, "start --foreground", `DOMAIN\user`)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) < 2 || data[0] != 0xff || data[1] != 0xfe {
		t.Fatalf("task XML missing UTF-16 little-endian BOM: % x", data[:min(len(data), 4)])
	}
	text := decodeUTF16XML(t, data)
	if !strings.HasPrefix(text, `<?xml version="1.0" encoding="UTF-16"?>`) {
		t.Fatalf("task XML declaration = %q", text[:min(len(text), 50)])
	}
}

func TestBuildWindowsTaskXMLEscapesInterpolatedValues(t *testing.T) {
	data, err := BuildWindowsTaskXML(`C:\A&B\<termp>\termp.exe`, "start --foreground", `DOMAIN\a&b<user>`)
	if err != nil {
		t.Fatal(err)
	}
	text := decodeUTF16XML(t, data)
	for _, want := range []string{
		`<Command>C:\A&amp;B\&lt;termp&gt;\termp.exe</Command>`,
		`<UserId>DOMAIN\a&amp;b&lt;user&gt;</UserId>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("task XML missing escaped value %q:\n%s", want, text)
		}
	}
	for _, raw := range []string{`C:\A&B\<termp>\termp.exe`, `DOMAIN\a&b<user>`} {
		if strings.Contains(text, raw) {
			t.Fatalf("task XML contains raw value %q:\n%s", raw, text)
		}
	}
}

func TestBuildWindowsTaskXMLRestartsConservativelyOnFailure(t *testing.T) {
	data, err := BuildWindowsTaskXML(`C:\termp.exe`, "start --foreground", `DOMAIN\user`)
	if err != nil {
		t.Fatal(err)
	}
	text := decodeUTF16XML(t, data)
	for _, want := range []string{
		`<RestartOnFailure>`,
		`<Interval>PT1M</Interval>`,
		`<Count>3</Count>`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("task XML missing restart setting %q:\n%s", want, text)
		}
	}
}

func TestWindowsDisableAndEnableToggleTaskWithoutRealSchtasks(t *testing.T) {
	runner := &recordingRunner{
		fail: map[string]error{},
		out: map[string]string{
			"schtasks /Query /TN " + TaskName + " /XML": windowsEnabledTaskXML,
		},
	}
	manager := Manager{GOOS: "windows", Runner: runner}

	if _, err := manager.Disable(); err != nil {
		t.Fatal(err)
	}
	if !hasCall(runner.calls, "schtasks /Change /TN "+TaskName+" /DISABLE") {
		t.Fatalf("Disable calls = %#v, want schtasks disable", runner.calls)
	}
	if !hasCall(runner.calls, "schtasks /End /TN "+TaskName) {
		t.Fatalf("Disable calls = %#v, want schtasks end", runner.calls)
	}

	runner.calls = nil
	if _, err := manager.Enable(); err != nil {
		t.Fatal(err)
	}
	if !hasCall(runner.calls, "schtasks /Change /TN "+TaskName+" /ENABLE") {
		t.Fatalf("Enable calls = %#v, want schtasks enable", runner.calls)
	}
	if !hasCall(runner.calls, "schtasks /Run /TN "+TaskName) {
		t.Fatalf("Enable calls = %#v, want immediate schtasks run", runner.calls)
	}
}

func TestWindowsUninstallDeletesTaskWithoutRealSchtasks(t *testing.T) {
	runner := &recordingRunner{
		fail: map[string]error{},
		out: map[string]string{
			"schtasks /Query /TN " + TaskName + " /XML": windowsEnabledTaskXML,
		},
	}
	manager := Manager{GOOS: "windows", Runner: runner}
	state, err := manager.Uninstall(false)
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed || state.Enabled != "false" || state.Loaded != "false" {
		t.Fatalf("state = %+v, want not installed enabled false loaded false", state)
	}
	if len(runner.calls) < 4 {
		t.Fatalf("Uninstall calls = %#v, want query, end, then delete", runner.calls)
	}
	if want := "schtasks /End /TN " + TaskName; runner.calls[2] != want {
		t.Fatalf("third Uninstall call = %q, want %q", runner.calls[2], want)
	}
	delete := runner.calls[3]
	for _, want := range []string{
		"schtasks /Delete",
		"/TN " + TaskName,
		"/F",
	} {
		if !strings.Contains(delete, want) {
			t.Fatalf("delete call missing %q:\n%s", want, delete)
		}
	}
}

func TestWindowsDisableSurfacesEndFailure(t *testing.T) {
	queryCall := "schtasks /Query /TN " + TaskName + " /XML"
	endCall := "schtasks /End /TN " + TaskName
	runner := &recordingRunner{
		fail: map[string]error{endCall: errors.New("exit status 1")},
		out: map[string]string{
			queryCall: windowsEnabledTaskXML,
			endCall:   "ERROR: The task could not be stopped.",
		},
	}

	_, err := (Manager{GOOS: "windows", Runner: runner}).Disable()
	if err == nil || !strings.Contains(err.Error(), "schtasks end failed") {
		t.Fatalf("Disable() error = %v, want schtasks end failure", err)
	}
}

func TestWindowsUninstallDoesNotDeleteAfterEndFailure(t *testing.T) {
	queryCall := "schtasks /Query /TN " + TaskName + " /XML"
	endCall := "schtasks /End /TN " + TaskName
	runner := &recordingRunner{
		fail: map[string]error{endCall: errors.New("exit status 1")},
		out: map[string]string{
			queryCall: windowsEnabledTaskXML,
			endCall:   "ERROR: The task could not be stopped.",
		},
	}

	_, err := (Manager{GOOS: "windows", Runner: runner}).Uninstall(false)
	if err == nil || !strings.Contains(err.Error(), "schtasks end failed") {
		t.Fatalf("Uninstall() error = %v, want schtasks end failure", err)
	}
	if hasCall(runner.calls, "schtasks /Delete /TN "+TaskName+" /F") {
		t.Fatalf("Uninstall calls = %#v, must not delete after /End failure", runner.calls)
	}
}

func TestWindowsUninstallTreatsMissingTaskAsSuccess(t *testing.T) {
	deleteCall := "schtasks /Delete /TN " + TaskName + " /F"
	runner := &recordingRunner{
		fail: map[string]error{
			"schtasks /Query /TN " + TaskName + " /XML": errors.New("exit status 1"),
			deleteCall: errors.New("exit status 1"),
		},
		out: map[string]string{
			"schtasks /Query /TN " + TaskName + " /XML": "ERROR: The specified task name does not exist in the system.\n",
			deleteCall: "ERROR: The specified task name does not exist in the system.\n",
		},
	}

	state, err := (Manager{GOOS: "windows", Runner: runner}).Uninstall(false)
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed || state.Loaded != "false" || state.Enabled != "false" {
		t.Fatalf("Uninstall() = %+v, want clean absent state", state)
	}
	if len(runner.calls) != 1 || runner.calls[0] != "schtasks /Query /TN "+TaskName+" /FO CSV /NH" {
		t.Fatalf("Uninstall calls = %#v, want only targeted absent-state query", runner.calls)
	}
}

func TestWindowsRunToleratesBenignRaces(t *testing.T) {
	tests := []struct {
		name     string
		out      string
		queryOut string
		listOut  string
	}{
		{
			name: "already running with German output",
			out:  "FEHLER: Eine Instanz dieser Aufgabe wird bereits ausgeführt.\n",
			// Realistic German verbose row: SCHED_S_TASK_RUNNING sits in the
			// locale-independent "Letztes Ausführungsergebnis" (Last Run Result)
			// column at the fixed verbose index, not an abbreviated column.
			queryOut: `"COMPUTER","` + TaskName + `","Nicht zutreffend","Wird ausgeführt","Nur interaktiv","28.07.2026 08:59:00","0x41301","DOMAIN\User","C:\Program Files\termp\termpw.exe"` + "\r\n",
			listOut:  `"` + TaskName + `","Nicht zutreffend","Wird ausgeführt"` + "\r\n",
		},
		{
			name:    "task removed with Japanese output",
			out:     "エラー: 指定されたファイルが見つかりません。\n",
			listOut: "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runCall := "schtasks /Run /TN " + TaskName
			verboseCall := "schtasks /Query /TN " + TaskName + " /FO CSV /V /NH"
			listCall := "schtasks /Query /FO CSV /NH"
			runner := &recordingRunner{
				fail: map[string]error{runCall: errors.New("exit status 1")},
				out: map[string]string{
					runCall:     tt.out,
					verboseCall: tt.queryOut,
					listCall:    tt.listOut,
				},
			}
			if err := (windowsService{runner: runner}).runTask(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestWindowsTaskCSVContainsIgnoresLocalizedColumns(t *testing.T) {
	japanese := []byte(
		`"\Microsoft\Windows\UpdateOrchestrator\Schedule Scan","次回の実行時刻なし","準備完了"` + "\r\n" +
			`"` + TaskName + `","次回の実行時刻なし","実行中"` + "\r\n",
	)
	found, err := windowsTaskCSVContains(japanese, TaskName)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatalf("windowsTaskCSVContains() = false, want task found in Japanese sample")
	}

	found, err = windowsTaskCSVContains([]byte(`"\Andere Aufgabe","Nicht zutreffend","Bereit"`+"\r\n"), TaskName)
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("windowsTaskCSVContains() = true, want absent task in German sample")
	}
}

func TestWindowsTaskCSVContainsToleratesUnrelatedErrorRows(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "schtasks", "list-with-error.csv"))
	if err != nil {
		t.Fatal(err)
	}
	found, err := windowsTaskCSVContains(fixture, TaskName)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("windowsTaskCSVContains() = false, want task after unrelated error row")
	}
}

func TestWindowsTaskCSVIsRunningToleratesUndoubledCommandQuotes(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "schtasks", "verbose-running-undoubled-quotes.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if !windowsTaskCSVIsRunning(fixture) {
		t.Fatal("windowsTaskCSVIsRunning() = false, want 0x41301 from realistic verbose row")
	}
}

// TestWindowsTaskCSVIsRunningChecksOnlyLastRunResultColumn is the issue #504
// guard: the running sentinel (SCHED_S_TASK_RUNNING = 0x41301 = 267521) must be
// read from the fixed "Last Run Result" verbose column only. A coincidental
// 267521/0x41301 in any other column (a duration, an embedded exit code, a
// timestamp fragment) must not be mistaken for a running task.
func TestWindowsTaskCSVIsRunningChecksOnlyLastRunResultColumn(t *testing.T) {
	// verboseRow renders a representative /V /FO CSV /NH record. lastResult is
	// the Last Run Result column (index 6); tail fills columns after the
	// task-to-run, where decoy values are planted.
	verboseRow := func(lastResult string, tail ...string) string {
		fields := []string{
			"WORKSTATION",
			TaskName,
			"7/28/2026 9:00:00 AM",
			"Ready",
			"Interactive only",
			"7/28/2026 8:59:00 AM",
			lastResult,
			`DOMAIN\User`,
			`C:\Program Files\termp\termpw.exe`,
		}
		fields = append(fields, tail...)
		quoted := make([]string, len(fields))
		for i, f := range fields {
			quoted[i] = `"` + f + `"`
		}
		return strings.Join(quoted, ",") + "\r\n"
	}

	tests := []struct {
		name string
		data string
		want bool
	}{
		{name: "running hex sentinel in last-result column", data: verboseRow("0x41301"), want: true},
		// 0x41301 == 267009 decimal (the value schtasks prints on some locales).
		{name: "running decimal sentinel in last-result column", data: verboseRow("267009"), want: true},
		// Real sentinel value planted in an unrelated column: the old whole-row
		// scan false-positived here; the positional check must not.
		{name: "success with real hex sentinel decoy in a later column", data: verboseRow("0x0", "0x41301"), want: false},
		{name: "success with real decimal sentinel decoy in a later column", data: verboseRow("0x0", "267009", "72:00:00"), want: false},
		// The decoy called out in issue #504 (267521) planted in another column.
		{name: "success with issue-reported decoy in a later column", data: verboseRow("0x0", "267521"), want: false},
		{name: "success without any decoy", data: verboseRow("0x0"), want: false},
		{name: "short row cannot reach last-result column", data: `"WORKSTATION","` + TaskName + `","0x41301"` + "\r\n", want: false},
		{name: "empty output", data: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := windowsTaskCSVIsRunning([]byte(tt.data)); got != tt.want {
				t.Fatalf("windowsTaskCSVIsRunning() = %t, want %t\ninput: %q", got, tt.want, tt.data)
			}
		})
	}
}

func TestWindowsTaskExistsUsesTargetedQueryFastPath(t *testing.T) {
	targeted := "schtasks /Query /TN " + TaskName + " /FO CSV /NH"
	runner := &scriptedRunner{results: map[string]scriptedRunnerResult{
		targeted: {out: `"` + TaskName + `","N/A","Ready"`},
	}}

	found, err := (windowsService{runner: runner}).taskExists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("taskExists() = false, want targeted-query success to mean present")
	}
	if len(runner.calls) != 1 || runner.calls[0] != targeted {
		t.Fatalf("taskExists() calls = %#v, want only targeted query", runner.calls)
	}
}

func TestWindowsTaskExistsUsesParsedFallbackDespiteExitError(t *testing.T) {
	targeted := "schtasks /Query /TN " + TaskName + " /FO CSV /NH"
	list := "schtasks /Query /FO CSV /NH"
	fixture, err := os.ReadFile(filepath.Join("testdata", "schtasks", "list-with-error.csv"))
	if err != nil {
		t.Fatal(err)
	}
	runner := &scriptedRunner{results: map[string]scriptedRunnerResult{
		targeted: {err: errors.New("start schtasks: transient failure")},
		list: {
			out: string(fixture),
			err: errors.New("exit status 1"),
		},
	}}

	found, err := (windowsService{runner: runner}).taskExists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("taskExists() = false, want parsed task row despite listing exit error")
	}
	if len(runner.calls) != 2 || runner.calls[0] != targeted || runner.calls[1] != list {
		t.Fatalf("taskExists() calls = %#v, want targeted query then fallback listing", runner.calls)
	}
}

func TestWindowsTaskExistsTreatsTargetedCommandExitAsAbsent(t *testing.T) {
	targeted := "schtasks /Query /TN " + TaskName + " /FO CSV /NH"
	runner := &scriptedRunner{results: map[string]scriptedRunnerResult{
		targeted: {err: simulatedExitError{code: 1}},
	}}

	found, err := (windowsService{runner: runner}).taskExists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("taskExists() = true, want targeted-query command exit to mean absent")
	}
	if len(runner.calls) != 1 || runner.calls[0] != targeted {
		t.Fatalf("taskExists() calls = %#v, want no full-list fallback", runner.calls)
	}
}

func TestWindowsStatusContextDoesNotTreatTimeoutAsAbsence(t *testing.T) {
	runner := &blockingContextRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	state := (Manager{GOOS: "windows", Runner: runner}).StatusContext(ctx)
	if !state.Installed || state.Loaded != "unknown" || state.Enabled != "unknown" {
		t.Fatalf("StatusContext() = %+v, want ambiguous installed state after timeout", state)
	}
	if !strings.Contains(state.Message, context.DeadlineExceeded.Error()) {
		t.Fatalf("StatusContext().Message = %q, want deadline error", state.Message)
	}
	if runner.contextCalls != 1 {
		t.Fatalf("RunContext calls = %d, want only targeted query before timeout", runner.contextCalls)
	}
}

func TestWindowsStatusParsesTaskState(t *testing.T) {
	tests := []struct {
		name          string
		queryOut      string
		queryErr      error
		wantInstalled bool
		wantLoaded    string
		wantEnabled   string
		wantMessage   bool
	}{
		{
			name:          "ready task is enabled",
			queryOut:      windowsEnabledTaskXML,
			wantInstalled: true,
			wantLoaded:    "true",
			wantEnabled:   "true",
		},
		{
			name:          "disabled task is not enabled",
			queryOut:      windowsDisabledTaskXML,
			wantInstalled: true,
			wantLoaded:    "false",
			wantEnabled:   "false",
		},
		{
			name:          "missing enabled field defaults true",
			queryOut:      `<Task><Settings /></Task>`,
			wantInstalled: true,
			wantLoaded:    "true",
			wantEnabled:   "true",
		},
		{
			name:          "absent task is not installed",
			queryOut:      "ERROR: The specified task name does not exist in the system.\n",
			queryErr:      errors.New("exit status 1"),
			wantInstalled: false,
			wantLoaded:    "false",
			wantEnabled:   "false",
		},
		{
			name:          "query failure is not clean absence",
			queryOut:      "ERROR: Access is denied.\n",
			queryErr:      errors.New("exit status 1"),
			wantInstalled: true,
			wantLoaded:    "unknown",
			wantEnabled:   "unknown",
			wantMessage:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			query := "schtasks /Query /TN " + TaskName + " /XML"
			targetedQuery := "schtasks /Query /TN " + TaskName + " /FO CSV /NH"
			listQuery := "schtasks /Query /FO CSV /NH"
			runner := &recordingRunner{
				fail: map[string]error{},
				out:  map[string]string{query: tt.queryOut},
			}
			if tt.queryErr != nil {
				if tt.wantMessage {
					runner.fail[targetedQuery] = errors.New("start schtasks: access denied")
					runner.fail[listQuery] = tt.queryErr
					runner.out[listQuery] = tt.queryOut
				} else {
					runner.fail[query] = tt.queryErr
				}
			}

			state := (Manager{GOOS: "windows", Runner: runner}).Status()
			if state.Installed != tt.wantInstalled || state.Loaded != tt.wantLoaded || state.Enabled != tt.wantEnabled {
				t.Fatalf("Status() = %+v, want installed=%t loaded=%q enabled=%q", state, tt.wantInstalled, tt.wantLoaded, tt.wantEnabled)
			}
			if (state.Message != "") != tt.wantMessage {
				t.Fatalf("Status().Message = %q, wantMessage=%t", state.Message, tt.wantMessage)
			}
			if tt.wantMessage && !strings.Contains(state.Message, "Access is denied") {
				t.Fatalf("Status().Message = %q, want schtasks output", state.Message)
			}
		})
	}
}

func TestWindowsStatusVerifiesTaskExecutable(t *testing.T) {
	query := "schtasks /Query /TN " + TaskName + " /XML"
	tests := []struct {
		name          string
		executable    string
		wantInstalled bool
		wantMessage   string
	}{
		{
			name:          "same executable",
			executable:    `c:\TERMP.exe`,
			wantInstalled: true,
		},
		{
			name:        "different executable",
			executable:  `C:\staged\termp.exe`,
			wantMessage: `belongs to a different installation`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := &recordingRunner{
				fail: map[string]error{},
				out:  map[string]string{query: windowsEnabledTaskXML},
			}
			manager := Manager{GOOS: "windows", Runner: runner, Executable: tt.executable}
			state := manager.Status()
			if state.Installed != tt.wantInstalled {
				t.Fatalf("Status().Installed = %t, want %t; state=%+v", state.Installed, tt.wantInstalled, state)
			}
			if !strings.Contains(state.Message, tt.wantMessage) {
				t.Fatalf("Status().Message = %q, want substring %q", state.Message, tt.wantMessage)
			}
		})
	}
}

func TestSameWindowsExecutableNormalizesPathSyntaxAndEnvironment(t *testing.T) {
	t.Setenv("ProgramFiles", `C:\Program Files`)
	tests := []struct {
		name       string
		task       string
		executable string
		want       bool
	}{
		{
			name:       "case quotes separators and dot segments",
			task:       `"c:/PROGRAM FILES/termp/./termp.exe"`,
			executable: `C:\Program Files\termp\termp.exe`,
			want:       true,
		},
		{
			name:       "percent environment variable",
			task:       `%PROGRAMFILES%\termp\termp.exe`,
			executable: `C:\Program Files\termp\termp.exe`,
			want:       true,
		},
		{
			name:       "different executable",
			task:       `C:\Program Files\termp\termp.exe`,
			executable: `C:\Program Files\other\termp.exe`,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sameWindowsExecutable(tt.task, tt.executable); got != tt.want {
				t.Fatalf("sameWindowsExecutable(%q, %q) = %t, want %t", tt.task, tt.executable, got, tt.want)
			}
		})
	}
}

func TestNormalizeResolvedWindowsPathDoesNotExpandEnvironment(t *testing.T) {
	t.Setenv("TEMP", `C:\Windows\Temp`)
	const resolved = `C:\dir%TEMP%name\termp.exe`
	got := normalizeResolvedWindowsPath(resolved)
	if got != resolved {
		t.Fatalf("normalizeResolvedWindowsPath(%q) = %q, want literal percent path unchanged", resolved, got)
	}
}

func TestNormalizeWindowsPathClampsAtDriveRoot(t *testing.T) {
	const input = `C:\a\b\..\..\..\..\x.exe`
	if got, want := normalizeWindowsPath(input), `C:\x.exe`; got != want {
		t.Fatalf("normalizeWindowsPath(%q) = %q, want %q", input, got, want)
	}
}

func TestWindowsInstallReconcilesTaskForSameExecutable(t *testing.T) {
	query := "schtasks /Query /TN " + TaskName + " /XML"
	runner := &recordingRunner{
		fail: map[string]error{},
		out:  map[string]string{query: windowsEnabledTaskXML},
	}

	if _, err := (Manager{GOOS: "windows", Runner: runner}).Install(`C:\termp.exe`, false); err != nil {
		t.Fatal(err)
	}
	if !slicesContainsPrefix(runner.calls, "schtasks /Create /TN "+TaskName+" ") {
		t.Fatalf("Install calls = %#v, want existing same-executable task reconciled", runner.calls)
	}
}

func TestWindowsUnrelatedStatusMessageDoesNotBlockMutations(t *testing.T) {
	query := "schtasks /Query /TN " + TaskName + " /XML"

	t.Run("install", func(t *testing.T) {
		runner := &recordingRunner{
			fail: map[string]error{},
			out:  map[string]string{query: "<not valid XML"},
		}
		if _, err := (Manager{GOOS: "windows", Runner: runner}).Install(`C:\termp.exe`, false); err != nil {
			t.Fatal(err)
		}
		if !slicesContainsPrefix(runner.calls, "schtasks /Create /TN "+TaskName+" ") {
			t.Fatalf("Install calls = %#v, want create despite unrelated status message", runner.calls)
		}
	})

	t.Run("uninstall", func(t *testing.T) {
		runner := &recordingRunner{
			fail: map[string]error{},
			out:  map[string]string{query: "<not valid XML"},
		}
		if _, err := (Manager{GOOS: "windows", Runner: runner}).Uninstall(false); err != nil {
			t.Fatal(err)
		}
		if !hasCall(runner.calls, "schtasks /Delete /TN "+TaskName+" /F") {
			t.Fatalf("Uninstall calls = %#v, want delete despite unrelated status message", runner.calls)
		}
	})
}

func TestWindowsMutationsRefuseForeignTask(t *testing.T) {
	query := "schtasks /Query /TN " + TaskName + " /XML"
	t.Run("install", func(t *testing.T) {
		runner := &recordingRunner{
			fail: map[string]error{},
			out:  map[string]string{query: windowsEnabledTaskXML},
		}
		_, err := (Manager{GOOS: "windows", Runner: runner}).Install(`C:\other\termp.exe`, false)
		if err == nil || !strings.Contains(err.Error(), "different installation") || !strings.Contains(err.Error(), "--force") {
			t.Fatalf("Install error = %v, want ownership refusal", err)
		}
		if len(runner.calls) != 2 || runner.calls[0] != "schtasks /Query /TN "+TaskName+" /FO CSV /NH" || runner.calls[1] != query {
			t.Fatalf("Install calls = %#v, want presence and ownership queries only", runner.calls)
		}
	})

	for _, action := range []struct {
		name string
		run  func(Manager) (State, error)
	}{
		{name: "install definition", run: func(manager Manager) (State, error) {
			return manager.InstallDefinition(`C:\other\termp.exe`, false)
		}},
		{name: "uninstall", run: func(manager Manager) (State, error) {
			return manager.Uninstall(false)
		}},
		{name: "disable", run: Manager.Disable},
		{name: "enable", run: Manager.Enable},
	} {
		t.Run(action.name, func(t *testing.T) {
			runner := &recordingRunner{
				fail: map[string]error{},
				out:  map[string]string{query: windowsEnabledTaskXML},
			}
			_, err := action.run(Manager{
				GOOS:       "windows",
				Runner:     runner,
				Executable: `C:\other\termp.exe`,
			})
			if err == nil || !strings.Contains(err.Error(), "different installation") || !strings.Contains(err.Error(), "--force") {
				t.Fatalf("%s error = %v, want ownership refusal", action.name, err)
			}
			if len(runner.calls) != 2 || runner.calls[0] != "schtasks /Query /TN "+TaskName+" /FO CSV /NH" || runner.calls[1] != query {
				t.Fatalf("%s calls = %#v, want presence and ownership queries only", action.name, runner.calls)
			}
		})
	}
}

func TestWindowsForceTakesOverForeignTask(t *testing.T) {
	query := "schtasks /Query /TN " + TaskName + " /XML"
	runner := &windowsInstallRunner{}
	runner.created = false

	foreignRunner := &foreignThenInstalledRunner{
		windowsInstallRunner: runner,
		foreignXML:           windowsEnabledTaskXML,
	}
	state, err := (Manager{GOOS: "windows", Runner: foreignRunner}).Install(`C:\Program Files & Tools\<termp>\termp.exe`, true)
	if err != nil {
		t.Fatal(err)
	}
	if !runner.created {
		t.Fatalf("Install with force did not rewrite foreign task; calls = %#v", runner.calls)
	}
	if !state.Installed || state.ForeignTask {
		t.Fatalf("Install with force state = %+v, want owned installed task", state)
	}
	if foreignRunner.firstQuery != query {
		t.Fatalf("first query = %q, want %q", foreignRunner.firstQuery, query)
	}
}

func TestWindowsInstallDefinitionForceTakesOverWithoutLaunching(t *testing.T) {
	runner := &windowsInstallRunner{}
	foreignRunner := &foreignThenInstalledRunner{
		windowsInstallRunner: runner,
		foreignXML:           windowsEnabledTaskXML,
	}

	state, err := (Manager{GOOS: "windows", Runner: foreignRunner}).
		InstallDefinition(`C:\Program Files & Tools\<termp>\termp.exe`, true)
	if err != nil {
		t.Fatal(err)
	}
	if !runner.created || !state.Installed || state.ForeignTask {
		t.Fatalf("forced InstallDefinition state = %+v, calls = %#v; want rewritten owned task", state, runner.calls)
	}
	if hasArgCall(runner.calls, "schtasks", "/Run", "/TN", TaskName) {
		t.Fatalf("forced InstallDefinition calls = %#v, must not launch a duplicate daemon", runner.calls)
	}
}

func TestWindowsForceUninstallRemovesForeignTask(t *testing.T) {
	query := "schtasks /Query /TN " + TaskName + " /XML"
	runner := &recordingRunner{
		fail: map[string]error{},
		out:  map[string]string{query: windowsEnabledTaskXML},
	}
	state, err := (Manager{
		GOOS:       "windows",
		Runner:     runner,
		Executable: `C:\other\termp.exe`,
	}).Uninstall(true)
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed {
		t.Fatalf("Uninstall with force state = %+v, want absent task", state)
	}
	if !hasCall(runner.calls, "schtasks /Delete /TN "+TaskName+" /F") {
		t.Fatalf("Uninstall with force calls = %#v, want foreign task deleted", runner.calls)
	}
}

func TestWindowsDisableAndEnableReturnQueryFailures(t *testing.T) {
	tests := []struct {
		name string
		run  func(Manager) (State, error)
	}{
		{name: "disable", run: Manager.Disable},
		{name: "enable", run: Manager.Enable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			targetedQuery := "schtasks /Query /TN " + TaskName + " /FO CSV /NH"
			listQuery := "schtasks /Query /FO CSV /NH"
			runner := &recordingRunner{
				fail: map[string]error{
					targetedQuery: errors.New("start schtasks: access denied"),
					listQuery:     errors.New("exit status 1"),
				},
				out: map[string]string{listQuery: "ERROR: Access is denied.\n"},
			}
			state, err := tt.run(Manager{GOOS: "windows", Runner: runner})
			if err == nil || !strings.Contains(err.Error(), "Access is denied") {
				t.Fatalf("%s() error = %v, want query failure", tt.name, err)
			}
			if state.Message == "" || state.Loaded != "unknown" || state.Enabled != "unknown" {
				t.Fatalf("%s() state = %+v, want visible ambiguous query state", tt.name, state)
			}
			if len(runner.calls) != 2 || runner.calls[0] != targetedQuery || runner.calls[1] != listQuery {
				t.Fatalf("%s() calls = %#v, want targeted query then fallback listing", tt.name, runner.calls)
			}
		})
	}
}

func TestUnsupportedOS(t *testing.T) {
	manager := Manager{GOOS: "plan9", Runner: &recordingRunner{}}
	tests := []struct {
		name string
		call func() (State, error)
	}{
		{name: "install", call: func() (State, error) { return manager.Install("/bin/termp", false) }},
		{name: "uninstall", call: func() (State, error) { return manager.Uninstall(false) }},
		{name: "disable", call: manager.Disable},
		{name: "enable", call: manager.Enable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, err := tt.call()
			if !errors.Is(err, ErrUnsupported) || state.Supported || !strings.Contains(state.Message, "plan9") {
				t.Fatalf("state, error = %+v, %v; want unsupported plan9", state, err)
			}
		})
	}
	state := manager.Status()
	if state.Supported || !strings.Contains(state.Message, "plan9") {
		t.Fatalf("Status() = %+v, want unsupported plan9", state)
	}
}

func TestServiceUnitEscapingEdges(t *testing.T) {
	plist, err := BuildLaunchAgentPlist(`/opt/a&b/<termp>`)
	if err != nil {
		t.Fatal(err)
	}
	text := string(plist)
	for _, escaped := range []string{`/opt/a&amp;b/&lt;termp&gt;`} {
		if !strings.Contains(text, escaped) {
			t.Fatalf("plist missing escaped value %q:\n%s", escaped, text)
		}
	}

	tests := []struct {
		arg  string
		want string
	}{
		{arg: "", want: `""`},
		{arg: "/usr/local/bin/termp", want: "/usr/local/bin/termp"},
		{arg: `/opt/a b/termp`, want: `"/opt/a b/termp"`},
		{arg: `C:\Program Files\"termp"`, want: `"C:\\Program Files\\\"termp\""`},
	}
	for _, tt := range tests {
		if got := systemdEscapeExecArg(tt.arg); got != tt.want {
			t.Errorf("systemdEscapeExecArg(%q) = %q, want %q", tt.arg, got, tt.want)
		}
	}
}

func TestLinuxUninstallIsIdempotent(t *testing.T) {
	requireGOOS(t, "linux")
	home := fakeHome(t)
	path := filepath.Join(home, ".config", "systemd", "user", ServiceName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{fail: map[string]error{}, out: map[string]string{}}
	manager := Manager{GOOS: "linux", Runner: runner}
	for i := 0; i < 2; i++ {
		state, err := manager.Uninstall(false)
		if err != nil {
			t.Fatal(err)
		}
		if state.Installed {
			t.Fatalf("uninstall %d state = %+v", i+1, state)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit still exists: %v", err)
	}
}

func TestLinuxUninstallKeepsUnitOnDisableFailure(t *testing.T) {
	requireGOOS(t, "linux")
	home := fakeHome(t)
	path := filepath.Join(home, ".config", "systemd", "user", ServiceName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	disable := "systemctl --user disable --now " + ServiceName
	runner := &recordingRunner{
		fail: map[string]error{disable: errors.New("exit status 1")},
		out:  map[string]string{disable: "Failed to connect to bus: No such process\n"},
	}

	state, err := (Manager{GOOS: "linux", Runner: runner}).Uninstall(false)
	if err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Fatalf("Uninstall() error = %v, want bus failure", err)
	}
	if !state.Installed || state.Path != path {
		t.Fatalf("Uninstall() state = %+v, want installed definition at %q", state, path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("unit removed after disable failure: %v", err)
	}
	if hasCall(runner.calls, "systemctl --user daemon-reload") {
		t.Fatalf("daemon-reload called after disable failure: %#v", runner.calls)
	}
}

func TestLinuxUninstallRemovesUnitWhenAlreadyDisabled(t *testing.T) {
	requireGOOS(t, "linux")
	home := fakeHome(t)
	path := filepath.Join(home, ".config", "systemd", "user", ServiceName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	disable := "systemctl --user disable --now " + ServiceName
	runner := &recordingRunner{
		fail: map[string]error{disable: errors.New("exit status 1")},
		out:  map[string]string{disable: "Failed to disable unit: Unit file " + ServiceName + " does not exist.\n"},
	}

	state, err := (Manager{GOOS: "linux", Runner: runner}).Uninstall(false)
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed {
		t.Fatalf("Uninstall() state = %+v, want not installed", state)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unit still exists after benign disable failure: %v", err)
	}
	if !hasCall(runner.calls, "systemctl --user daemon-reload") {
		t.Fatalf("Uninstall() calls = %#v, want daemon-reload", runner.calls)
	}
}

func TestLinuxUninstallReportsDaemonReloadFailure(t *testing.T) {
	requireGOOS(t, "linux")
	home := fakeHome(t)
	path := filepath.Join(home, ".config", "systemd", "user", ServiceName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	reload := "systemctl --user daemon-reload"
	runner := &recordingRunner{
		fail: map[string]error{reload: errors.New("exit status 1")},
		out:  map[string]string{reload: "Failed to connect to bus: Permission denied\n"},
	}

	_, err := (Manager{GOOS: "linux", Runner: runner}).Uninstall(false)
	if err == nil {
		t.Fatal("Uninstall() error = nil, want daemon-reload failure")
	}
	for _, want := range []string{"daemon-reload", "Permission denied", "retry"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Uninstall() error missing %q: %v", want, err)
		}
	}
}

func TestLinuxUninstallAbsentIsNoOp(t *testing.T) {
	fakeHome(t)
	disable := "systemctl --user disable --now " + ServiceName
	reload := "systemctl --user daemon-reload"
	runner := &recordingRunner{
		fail: map[string]error{
			disable: errors.New("must not run"),
			reload:  errors.New("must not run"),
		},
		out: map[string]string{},
	}

	state, err := (Manager{GOOS: "linux", Runner: runner}).Uninstall(false)
	if err != nil {
		t.Fatal(err)
	}
	if state.Installed {
		t.Fatalf("Uninstall() state = %+v, want not installed", state)
	}
	if hasCall(runner.calls, disable) || hasCall(runner.calls, reload) {
		t.Fatalf("Uninstall() calls = %#v, want no disable or reload", runner.calls)
	}
}

func userID() string {
	return currentUID()
}

func hasCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func countCall(calls []string, want string) int {
	count := 0
	for _, call := range calls {
		if call == want {
			count++
		}
	}
	return count
}

func slicesContainsPrefix(calls []string, prefix string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}

func hasArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func hasArgCall(calls [][]string, want ...string) bool {
	for _, call := range calls {
		if len(call) != len(want) {
			continue
		}
		matches := true
		for i := range call {
			if call[i] != want[i] {
				matches = false
				break
			}
		}
		if matches {
			return true
		}
	}
	return false
}

func decodeUTF16XML(t *testing.T, data []byte) string {
	t.Helper()
	if len(data) < 2 || data[0] != 0xff || data[1] != 0xfe {
		t.Fatalf("data is not UTF-16 little-endian with BOM: % x", data[:min(len(data), 4)])
	}
	data = data[2:]
	if len(data)%2 != 0 {
		t.Fatalf("UTF-16 data has odd length: %d", len(data))
	}
	codeUnits := make([]uint16, len(data)/2)
	for i := range codeUnits {
		codeUnits[i] = binary.LittleEndian.Uint16(data[i*2:])
	}
	return string(utf16.Decode(codeUnits))
}
