package detector

import (
	"testing"

	"github.com/polter-dev/discord_terminal_presence/internal/registry"
)

// TestNoMatchKeyPinsEveryMatcherInput pins noMatchKey (#672): identities that
// share a key must get the same registry.MatchProcess answer, and forms whose
// answers differ must get different keys, so a weaker key fails here.
func TestNoMatchKeyPinsEveryMatcherInput(t *testing.T) {
	reg := testRegistry(t)
	py := func(argv ...string) Process {
		return Process{Name: "python3", Argv: append([]string{"python3"}, argv...)}
	}
	claude := func(argv ...string) Process {
		return Process{Name: "claude", Argv: append([]string{"claude"}, argv...)}
	}
	cmd := func(cmdline string) Process { return Process{Name: "python3", Cmdline: cmdline} }

	// Pairs the matcher answers differently (claude-code vs excluded "daemon").
	differ := [][2]Process{
		{py("-m", "claude"), py("-m", "claude", "daemon")},          // argv 3 vs 4
		{py("-m", "claude", "chat"), py("-m", "claude", "daemon")},  // subcommand at argv[3]
		{py("-m", "claude"), py("-m", "claude", "daemon", "x")},     // argv 3 vs 5
		{claude(), claude("daemon")},                                // argv 1 vs 2
		{claude("a"), claude("daemon")},                             // subcommand at argv[1]
		{cmd("python3 -m claude"), cmd("python3 -m claude daemon")}, // empty argv, Cmdline only
		{cmd("claude"), cmd("claude daemon")},                       // empty argv, Cmdline only
	}
	all := []Process{
		py("-m", "claude", "daemon", "x", "y"),
		claude("daemon", "a"),
		claude("daemon", "a", "b"),
		claude("daemon", "a", "b", "c"),
	}
	match := func(p Process) string {
		tool, ok := reg.MatchProcess(registry.ProcessInfo{Name: p.Name, Exe: p.Exe, Cmdline: p.Cmdline, Argv0: p.Argv0, Argv: p.Argv})
		if !ok {
			return "(none)"
		}
		return tool.ID
	}
	for _, pair := range differ {
		a, b := pair[0], pair[1]
		if match(a) == match(b) {
			t.Fatalf("table error: %q/%q and %q/%q both match %s", a.Argv, a.Cmdline, b.Argv, b.Cmdline, match(a))
		}
		if newNoMatchKey(a) == newNoMatchKey(b) {
			t.Errorf("%q/%q (%s) and %q/%q (%s) share a noMatchKey", a.Argv, a.Cmdline, match(a), b.Argv, b.Cmdline, match(b))
		}
		all = append(all, a, b)
	}
	for i, a := range all {
		for _, b := range all[i+1:] {
			if newNoMatchKey(a) == newNoMatchKey(b) && match(a) != match(b) {
				t.Errorf("share a key but match differently: %q/%q -> %s, %q/%q -> %s", a.Argv, a.Cmdline, match(a), b.Argv, b.Cmdline, match(b))
			}
		}
	}
}
