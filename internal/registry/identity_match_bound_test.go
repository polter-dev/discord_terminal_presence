package registry

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestProcessMatchIdentityBoundsInterpreterEntrypoint verifies that a
// structured interpreter entrypoint is capped before it becomes a matching
// identity, while the detector's raw Argv can remain intact for other users.
func TestProcessMatchIdentityBoundsInterpreterEntrypoint(t *testing.T) {
	entrypoint := strings.Repeat("a", 2*1024*1024)
	process := ProcessInfo{Argv: []string{"node", entrypoint}}
	identities, _ := processMatchIdentity(process)
	if len(identities) != 1 {
		t.Fatalf("processMatchIdentity returned %d identities, want 1", len(identities))
	}
	if got := identities[0]; got != entrypoint[:MaxIdentityFieldBytes] {
		t.Fatalf("interpreter entrypoint has %d bytes, want the exact %d-byte prefix", len(got), MaxIdentityFieldBytes)
	}
	if got := process.Argv[1]; got != entrypoint {
		t.Fatalf("processMatchIdentity mutated raw Argv entrypoint to %d bytes", len(got))
	}
}

// TestProcessMatchIdentityBoundsSubcommand verifies that the immediate
// subcommand is capped before exclude regex matching.
func TestProcessMatchIdentityBoundsSubcommand(t *testing.T) {
	subcommand := strings.Repeat("b", 2*1024*1024)
	process := ProcessInfo{Argv: []string{"codex", subcommand}}
	_, got := processMatchIdentity(process)
	if got != subcommand[:MaxIdentityFieldBytes] {
		t.Fatalf("subcommand has %d bytes, want the exact %d-byte prefix", len(got), MaxIdentityFieldBytes)
	}
	if got := process.Argv[1]; got != subcommand {
		t.Fatalf("processMatchIdentity mutated raw Argv subcommand to %d bytes", len(got))
	}
}

// TestBoundIdentityFieldPreservesUTF8AtByteBoundary verifies both sides of
// the boundary: a complete rune ending at byte 4096 is retained, and a rune
// straddling byte 4096 is removed rather than emitted as invalid UTF-8.
func TestBoundIdentityFieldPreservesUTF8AtByteBoundary(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "complete rune ends at boundary",
			input: strings.Repeat("a", MaxIdentityFieldBytes-3) + "☃" + "tail",
			want:  strings.Repeat("a", MaxIdentityFieldBytes-3) + "☃",
		},
		{
			name:  "partial rune crosses boundary",
			input: strings.Repeat("a", MaxIdentityFieldBytes-1) + "☃" + "tail",
			want:  strings.Repeat("a", MaxIdentityFieldBytes-1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BoundIdentityField(tt.input)
			if got != tt.want {
				t.Fatalf("BoundIdentityField returned %d bytes, want %d-byte valid prefix", len(got), len(tt.want))
			}
			if !utf8.ValidString(got) {
				t.Fatalf("BoundIdentityField returned invalid UTF-8: %q", got)
			}
		})
	}
}

// TestBoundedInterpreterIdentitiesPreserveLegitimateMatches proves that
// realistic node and Python entrypoints remain exact and still resolve to
// their existing catalog tools after the argv matching boundary is applied.
func TestBoundedInterpreterIdentitiesPreserveLegitimateMatches(t *testing.T) {
	reg, err := New()
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		process        ProcessInfo
		wantEntrypoint string
		wantSubcommand string
		wantTool       string
	}{
		{
			name: "node script",
			process: ProcessInfo{Argv: []string{
				"node", "/usr/local/lib/node_modules/@openai/codex/bin/codex.js", "exec",
			}},
			wantEntrypoint: "/usr/local/lib/node_modules/@openai/codex/bin/codex.js",
			wantSubcommand: "exec",
			wantTool:       "codex-cli",
		},
		{
			name: "python module",
			process: ProcessInfo{Argv: []string{
				"python3.12", "-m", "aider", "--model",
			}},
			wantEntrypoint: "aider",
			wantSubcommand: "--model",
			wantTool:       "aider",
		},
		{
			name: "python script",
			process: ProcessInfo{Argv: []string{
				"python", "/usr/local/bin/glances", "--help",
			}},
			wantEntrypoint: "/usr/local/bin/glances",
			wantSubcommand: "--help",
			wantTool:       "glances",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			identities, subcommand := processMatchIdentity(tt.process)
			if len(identities) != 1 || identities[0] != tt.wantEntrypoint {
				t.Fatalf("process identities = %#v, want exact entrypoint %q", identities, tt.wantEntrypoint)
			}
			if subcommand != tt.wantSubcommand {
				t.Fatalf("subcommand = %q, want %q", subcommand, tt.wantSubcommand)
			}
			tool, ok := reg.MatchProcess(tt.process)
			if !ok || tool.ID != tt.wantTool {
				t.Fatalf("MatchProcess(%#v) = (%q, %t), want tool %q", tt.process, tool.ID, ok, tt.wantTool)
			}
		})
	}
}

// BenchmarkStructuredArgvMatchCost compares capped and multi-megabyte raw
// argv values. Both cases exercise only the bounded 4096-byte matcher surface,
// so matching cost should remain independent of the source argv size.
func BenchmarkStructuredArgvMatchCost(b *testing.B) {
	entrypointRegistry, err := newFromTools([]Tool{{
		ID:    "entrypoint-regex",
		Match: MatchSpec{Regex: `z$`},
	}})
	if err != nil {
		b.Fatal(err)
	}
	subcommandRegistry, err := newFromTools([]Tool{{
		ID:      "subcommand-exclude",
		Match:   MatchSpec{Name: "codex"},
		Exclude: `z$`,
	}})
	if err != nil {
		b.Fatal(err)
	}

	for _, size := range []int{MaxIdentityFieldBytes, 2 * 1024 * 1024} {
		b.Run("entrypoint/argv_bytes="+benchmarkSizeLabel(size), func(b *testing.B) {
			process := ProcessInfo{Argv: []string{"node", strings.Repeat("a", size)}}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if tool, ok := entrypointRegistry.MatchProcess(process); ok {
					b.Fatalf("unexpected entrypoint match for tool %q", tool.ID)
				}
			}
		})
		b.Run("subcommand/argv_bytes="+benchmarkSizeLabel(size), func(b *testing.B) {
			process := ProcessInfo{Name: "codex", Argv: []string{"codex", strings.Repeat("a", size)}}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if tool, ok := subcommandRegistry.MatchProcess(process); !ok || tool.ID != "subcommand-exclude" {
					b.Fatalf("MatchProcess returned (%q, %t), want subcommand-exclude", tool.ID, ok)
				}
			}
		})
	}
}

func benchmarkSizeLabel(size int) string {
	if size == 2*1024*1024 {
		return "2MiB"
	}
	return "4KiB"
}
