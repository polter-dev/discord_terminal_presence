package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// buildNestedInlineTOML returns a syntactically valid, quote-free TOML
// document with an inline table nested depth levels deep, well past
// maxTOMLNestingDepth.
func buildNestedInlineTOML(depth int) string {
	var open, close strings.Builder
	for i := 0; i < depth; i++ {
		open.WriteString("a={a=")
		close.WriteString("}")
	}
	return "x=" + open.String() + "0" + close.String() + "\n"
}

// TestHuntFourQuoteMultilineBypassesNestingGuard is the #574 reproduction.
// TOML 1.0 allows up to two extra quote characters as literal content
// immediately before the closing delimiter of a multi-line basic string, so
// `"""x""""` is the valid string `x"`. tomlNestingTooDeep used to leave
// stateMultilineBasic on the FIRST `"""` it saw and advance by exactly 2,
// landing back in stateDefault on the fourth quote, which opened a phantom
// basic string that swallowed the rest of the document, so a genuinely
// deep-nested document placed after that prefix was never counted and the
// guard returned false, handing the bytes to the O(n^2) toml.Decode path
// (#497).
func TestHuntFourQuoteMultilineBypassesNestingGuard(t *testing.T) {
	deep := buildNestedInlineTOML(maxTOMLNestingDepth + 50)

	if !tomlNestingTooDeep([]byte(deep)) {
		t.Fatalf("precondition failed: the quote-free deep document alone must already trip the guard")
	}

	bypass := "note = \"\"\"x\"\"\"\"\n" + deep
	if tomlNestingTooDeep([]byte(bypass)) != true {
		t.Fatalf("GUARD BYPASSED: a four-quote multiline-basic prefix disabled nesting detection for the document that follows it")
	}
}

// TestHuntFourQuoteMultilineLiteralBypassesNestingGuard is the same bypass
// via stateMultilineLiteral (`'''...''''`), named suspect in #574 alongside
// the multiline-basic case but not separately reproduced there.
func TestHuntFourQuoteMultilineLiteralBypassesNestingGuard(t *testing.T) {
	deep := buildNestedInlineTOML(maxTOMLNestingDepth + 50)

	bypass := "note = '''x''''\n" + deep
	if tomlNestingTooDeep([]byte(bypass)) != true {
		t.Fatalf("GUARD BYPASSED: a four-quote multiline-literal prefix disabled nesting detection for the document that follows it")
	}
}

// TestMultilineBasicTrailingQuoteRunsStillClose exercises every legal
// trailing-quote-run length (0, 1, and 2 extra quotes before the closing
// delimiter) so the fix does not just special-case the exact 4-quote
// reproduction. Depth counting must resume normally after the close in
// every case.
func TestMultilineBasicTrailingQuoteRunsStillClose(t *testing.T) {
	tests := []struct {
		name string
		doc  string
	}{
		{name: "no trailing quote", doc: `note = """x"""` + "\n"},
		{name: "one trailing quote", doc: `note = """x""""` + "\n"},
		{name: "two trailing quotes", doc: `note = """x"""""` + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := tt.doc + "y = {a={a={a=0}}}\n"
			if tomlNestingTooDeep([]byte(doc)) {
				t.Fatalf("shallow document after a closed multiline string must not trip the guard: %q", doc)
			}
			deepAfter := tt.doc + buildNestedInlineTOML(maxTOMLNestingDepth+50)
			if !tomlNestingTooDeep([]byte(deepAfter)) {
				t.Fatalf("depth counting must resume immediately after the multiline string closes: %q prefix", tt.name)
			}
		})
	}
}

// TestMultilineLiteralTrailingQuoteRunsStillClose is the literal-string
// counterpart to TestMultilineBasicTrailingQuoteRunsStillClose.
func TestMultilineLiteralTrailingQuoteRunsStillClose(t *testing.T) {
	tests := []struct {
		name string
		doc  string
	}{
		{name: "no trailing quote", doc: `note = '''x'''` + "\n"},
		{name: "one trailing quote", doc: `note = '''x''''` + "\n"},
		{name: "two trailing quotes", doc: `note = '''x'''''` + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := tt.doc + "y = {a={a={a=0}}}\n"
			if tomlNestingTooDeep([]byte(doc)) {
				t.Fatalf("shallow document after a closed multiline string must not trip the guard: %q", doc)
			}
			deepAfter := tt.doc + buildNestedInlineTOML(maxTOMLNestingDepth+50)
			if !tomlNestingTooDeep([]byte(deepAfter)) {
				t.Fatalf("depth counting must resume immediately after the multiline string closes: %q prefix", tt.name)
			}
		})
	}
}

// TestLoadPathRejectsFourQuoteBypassDeepDocument is the end-to-end
// regression: LoadPath must reject the bypassing document via the fast
// nesting guard rather than falling into the O(n^2) decoder. It uses a
// depth chosen to keep the (fixed) guard's rejection fast and the
// (hypothetically unfixed) decode path slow enough to be obviously wrong
// without making the test itself slow.
func TestLoadPathRejectsFourQuoteBypassDeepDocument(t *testing.T) {
	deep := buildNestedInlineTOML(maxTOMLNestingDepth + 50)
	bypass := "note = \"\"\"x\"\"\"\"\n" + deep

	path := filepath.Join(t.TempDir(), "config.toml")
	writeConfig(t, path, bypass)
	_, err := LoadPathUnsettled(path)
	if err == nil {
		t.Fatal("LoadPathUnsettled() = nil error, want errConfigNestingTooDeep")
	}
	if err.Error() != errConfigNestingTooDeep.Error() {
		t.Fatalf("LoadPathUnsettled() error = %v, want %v", err, errConfigNestingTooDeep)
	}
}

// FuzzTomlNestingTooDeep hunts for more of the same quote/comment/escape
// state-machine class of bug: any input on which tomlNestingTooDeep must
// never panic, never loop forever (the fuzzer's own deadline enforces
// this), and must agree with a straightforward bracket-counting reference
// scanner once real TOML string/comment lexing is accounted for. Rather
// than reimplement a second lexer (which would just relocate the same bug
// class), this checks the two properties #574 actually needs: the function
// terminates and does not go out of bounds, which go test -fuzz already
// verifies via crashes; the seed corpus below encodes the known bypass
// shape so a regression is instantly caught.
func FuzzTomlNestingTooDeep(f *testing.F) {
	f.Add([]byte(`enabled = true`))
	f.Add([]byte(`note = """x""""` + "\n" + buildNestedInlineTOML(maxTOMLNestingDepth+10)))
	f.Add([]byte(`note = '''x''''` + "\n" + buildNestedInlineTOML(maxTOMLNestingDepth+10)))
	f.Add([]byte(`note = """x"""""` + "\n"))
	f.Add([]byte(`note = '''x'''''` + "\n"))
	f.Add([]byte(`a = "\\"` + "\n"))
	f.Add([]byte(`# {{{` + "\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		// Must terminate and must not panic (index out of range, etc).
		// tomlNestingTooDeep's own result is checked for agreement with
		// toml.Decode-ability where feasible: if the guard says false, the
		// document must not actually be pathologically deep according to a
		// deliberately naive reference scan that also handles the same
		// quote-run rule directly, so a divergence here means a bypass.
		got := tomlNestingTooDeep(data)
		want := referenceNestingTooDeep(data)
		if got != want {
			t.Fatalf("tomlNestingTooDeep(%q) = %v, reference scanner = %v", data, got, want)
		}
	})
}

// referenceNestingTooDeep is an independent, more naive implementation of
// the same quote-run rule used only by the fuzz target as a cross-check
// against tomlNestingTooDeep. It intentionally uses a different code shape
// (recursion-free run counting via strings.IndexByte-style scanning is
// avoided in favor of a byte-by-byte loop) so the two implementations are
// unlikely to share the same mistake.
func referenceNestingTooDeep(data []byte) bool {
	const (
		stateDefault = iota
		stateComment
		stateBasicString
		stateLiteralString
		stateMultilineBasic
		stateMultilineLiteral
	)
	state := stateDefault
	depth := 0
	n := len(data)
	i := 0
	for i < n {
		c := data[i]
		switch state {
		case stateDefault:
			switch c {
			case '#':
				state = stateComment
				i++
			case '"':
				if i+2 < n && data[i+1] == '"' && data[i+2] == '"' {
					state = stateMultilineBasic
					i += 3
				} else {
					state = stateBasicString
					i++
				}
			case '\'':
				if i+2 < n && data[i+1] == '\'' && data[i+2] == '\'' {
					state = stateMultilineLiteral
					i += 3
				} else {
					state = stateLiteralString
					i++
				}
			case '{', '[':
				depth++
				if depth > maxTOMLNestingDepth {
					return true
				}
				i++
			case '}', ']':
				if depth > 0 {
					depth--
				}
				i++
			default:
				i++
			}
		case stateComment:
			if c == '\n' {
				state = stateDefault
			}
			i++
		case stateBasicString:
			if c == '\\' {
				i += 2
				continue
			}
			if c == '"' {
				state = stateDefault
			}
			i++
		case stateLiteralString:
			if c == '\'' {
				state = stateDefault
			}
			i++
		case stateMultilineBasic:
			if c == '\\' {
				i += 2
				continue
			}
			if c == '"' {
				run := 0
				for i+run < n && data[i+run] == '"' {
					run++
				}
				if run >= 3 {
					state = stateDefault
				}
				i += run
				continue
			}
			i++
		case stateMultilineLiteral:
			if c == '\'' {
				run := 0
				for i+run < n && data[i+run] == '\'' {
					run++
				}
				if run >= 3 {
					state = stateDefault
				}
				i += run
				continue
			}
			i++
		}
	}
	return false
}
