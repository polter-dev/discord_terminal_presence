package detector

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestParseLinuxTTYNumberAfterCommand(t *testing.T) {
	tests := []struct {
		name string
		stat string
		want int32
	}{
		{name: "ordinary command", stat: "123 (codex) S 1 2 3 34817 0 0", want: 34817},
		{name: "spaces and closing parenthesis", stat: "456 (tool name ) worker) R 1 2 3 34818 0 0", want: 34818},
		{name: "no controlling tty", stat: "789 (daemon) S 1 2 3 0 0 0", want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseLinuxTTYNumber([]byte(test.stat))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("tty_nr = %d, want %d", got, test.want)
			}
		})
	}
	for _, malformed := range []string{"123 no-parens S 1 2 3 4", "123 no-opening) S 1 2 3 4", "123 (short) S 1", "123 (bad) S 1 2 3 nope"} {
		if _, err := parseLinuxTTYNumber([]byte(malformed)); err == nil {
			t.Fatalf("parseLinuxTTYNumber(%q) succeeded, want error", malformed)
		}
	}
}

func TestLinuxTTYResolverMapsDeviceAndDistinguishesNoTTY(t *testing.T) {
	stats := map[int32][]byte{
		1: []byte("1 (tool name) S 2 3 4 34817 0"),
		2: []byte("2 (daemon) S 1 1 1 0 0"),
		3: []byte("3 (missing) S 1 1 1 34818 0"),
	}
	resolver := &linuxTTYResolver{
		devices: map[uint64]string{34817: "/dev/pts/1"},
		readStat: func(pid int32) ([]byte, error) {
			stat, ok := stats[pid]
			if !ok {
				return nil, errors.New("process disappeared")
			}
			return stat, nil
		},
	}

	resolved, err := resolver.Resolve(1)
	if err != nil || resolved.Path != "/dev/pts/1" || resolved.NoTTY {
		t.Fatalf("mapped resolution = %#v, %v", resolved, err)
	}
	noTTY, err := resolver.Resolve(2)
	if err != nil || !noTTY.NoTTY {
		t.Fatalf("no-tty resolution = %#v, %v", noTTY, err)
	}
	if _, err := resolver.Resolve(3); err == nil {
		t.Fatal("device map miss succeeded, want error")
	}
	if _, err := resolver.Resolve(4); err == nil {
		t.Fatal("stat read failure succeeded, want error")
	}
}

func TestLinuxMountAtimeTrustMatrix(t *testing.T) {
	tests := []struct {
		name    string
		options string
		trusted bool
	}{
		{name: "strictatime", options: "rw,nosuid,strictatime", trusted: true},
		{name: "explicit atime", options: "rw,atime", trusted: true},
		{name: "relatime", options: "rw,nosuid,relatime", trusted: false},
		{name: "noatime", options: "rw,noatime", trusted: false},
		{name: "nodiratime ambiguity", options: "rw,strictatime,nodiratime", trusted: false},
		{name: "conflicting options", options: "rw,atime,relatime", trusted: false},
		{name: "missing policy", options: "rw,nosuid", trusted: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mounts, err := parseLinuxMounts([]byte("devpts /dev/pts devpts " + test.options + " 0 0\n"))
			if err != nil {
				t.Fatal(err)
			}
			if got := trustworthyLinuxAtime(mounts[0].options); got != test.trusted {
				t.Fatalf("trustworthyLinuxAtime(%q) = %v, want %v", test.options, got, test.trusted)
			}
		})
	}
}

func TestLinuxTTYAtimeGateRequiresCoveringTrustworthyDevpts(t *testing.T) {
	want := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	statCalls := 0
	stat := func(string) (time.Time, error) {
		statCalls++
		return want, nil
	}
	tests := []struct {
		name     string
		mounts   string
		path     string
		wantTime bool
	}{
		{name: "strict devpts", mounts: "devpts /dev/pts devpts rw,strictatime 0 0\n", path: "/dev/pts/7", wantTime: true},
		{name: "relatime devpts", mounts: "devpts /dev/pts devpts rw,relatime 0 0\n", path: "/dev/pts/7"},
		{name: "missing covering mount", mounts: "devpts /other devpts rw,strictatime 0 0\n", path: "/dev/pts/7"},
		{name: "non-devpts covering mount", mounts: "udev /dev devtmpfs rw,strictatime 0 0\n", path: "/dev/tty1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mounts, err := parseLinuxMounts([]byte(test.mounts))
			if err != nil {
				t.Fatal(err)
			}
			source := linuxTTYAtimeSource{mounts: mounts, stat: stat}
			got, err := source.Atime(test.path)
			if test.wantTime {
				if err != nil || !got.Equal(want) {
					t.Fatalf("Atime() = %s, %v; want %s", got, err, want)
				}
			} else if err == nil {
				t.Fatal("Atime() succeeded for untrusted mount")
			}
		})
	}
	if statCalls != 1 {
		t.Fatalf("stat called %d times, want only once for trusted atime", statCalls)
	}

	if _, err := parseLinuxMounts([]byte("malformed")); err == nil {
		t.Fatal("malformed mounts input succeeded, want error")
	}
}

func TestLinuxUngatedAtimeNeverReachesSelector(t *testing.T) {
	base := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	mounts, err := parseLinuxMounts([]byte("devpts /dev/pts devpts rw,relatime 0 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	statCalled := false
	atime := linuxTTYAtimeSource{
		mounts: mounts,
		stat: func(string) (time.Time, error) {
			statCalled = true
			return base.Add(-24 * time.Hour), nil
		},
	}
	enricher := presenceEnricher(
		fakeTTYResolver{resolutions: map[int32]TTYResolution{1: {Path: "/dev/pts/7"}}},
		fakeTmuxSnapshot{},
		atime,
	)
	process := enricher.Enrich(Process{Pid: 1, Name: "claude", CreateTime: base.Add(-time.Hour)})
	if process.TTY.AtimeKnown || statCalled {
		t.Fatalf("untrusted atime leaked through enrichment: tty=%#v statCalled=%v", process.TTY, statCalled)
	}
	selector := NewSelector(testRegistry(t), Config{IdleClearTimeout: 20 * time.Minute, ActivitySwitching: true}, &fakeClock{now: base})
	if detection := selector.Select([]Process{process}); detection.None {
		t.Fatal("unknown atime must fail open and keep the tool visible")
	}
}

func TestLinuxAtimeKnownGateMatrix(t *testing.T) {
	base := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		mounts    string
		mountErr  error
		wantKnown bool
	}{
		{name: "strictatime", mounts: "devpts /dev/pts devpts rw,strictatime 0 0\n", wantKnown: true},
		{name: "relatime", mounts: "devpts /dev/pts devpts rw,relatime 0 0\n"},
		{name: "noatime", mounts: "devpts /dev/pts devpts rw,noatime 0 0\n"},
		{name: "missing policy", mounts: "devpts /dev/pts devpts rw,nosuid 0 0\n"},
		{name: "mount parse failure", mountErr: errors.New("malformed mounts")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mounts, err := parseLinuxMounts([]byte(test.mounts))
			if err != nil {
				t.Fatal(err)
			}
			source := linuxTTYAtimeSource{
				mounts:   mounts,
				mountErr: test.mountErr,
				stat: func(string) (time.Time, error) {
					return base, nil
				},
			}
			enricher := presenceEnricher(
				fakeTTYResolver{resolutions: map[int32]TTYResolution{1: {Path: "/dev/pts/7"}}},
				fakeTmuxSnapshot{},
				source,
			)
			process := enricher.Enrich(Process{Pid: 1})
			if process.TTY.AtimeKnown != test.wantKnown {
				t.Fatalf("AtimeKnown = %v, want %v", process.TTY.AtimeKnown, test.wantKnown)
			}
		})
	}
}

func TestLinuxMountParsingEscapesAndLongestCoveringMount(t *testing.T) {
	mounts, err := parseLinuxMounts([]byte("devpts /dev/pts devpts rw,strictatime 0 0\ndevpts /dev/pts/special\\040pane devpts rw,strictatime 0 0\n"))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := coveringDevptsMount("/dev/pts/special pane/7", mounts)
	if !ok || got.path != "/dev/pts/special pane" {
		t.Fatalf("coveringDevptsMount() = %#v, %t", got, ok)
	}
	for _, value := range []string{`bad\`, `bad\xyz`} {
		if _, err := unescapeLinuxMountField(value); err == nil {
			t.Fatalf("unescapeLinuxMountField(%q) succeeded", value)
		}
	}
	if pathWithinMount("/dev/pts-other/1", "/dev/pts") {
		t.Fatal("similar mount prefix counted as within mount")
	}
	if !pathWithinMount("/anything", string(filepath.Separator)) {
		t.Fatal("root mount did not cover absolute path")
	}
}
