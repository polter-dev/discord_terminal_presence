# registry (package `internal/registry`)

**Purpose:** Owns built-in and custom terminal-tool definitions, resolves Discord image
values, and matches a process identity to the highest-priority tool.

**Public surface:** `Tool`, `CustomTool`, `MatchSpec`, `CustomMatch`, `ProcessInfo`, and
`Button` define registry data. `New` loads embedded built-ins and merges runtime tools;
`NewWithCustom` converts config-facing tools. `Registry.Tools` returns safe copies.
`MatchProcess` performs identity matching; `Match` is the name-only compatibility wrapper.

**Key files:** `internal/registry/catalog.json` is the embedded built-in catalog.
`internal/registry/registry.go` constructs registries, compiles match/exclude regexes,
extracts process identity, resolves icons, and chooses by priority then catalog order.

**Invariants / gotchas:** Matching never scans arbitrary later arguments. Its identity
surface is process name, argv0, executable path, and—only for recognized language-runtime
wrappers—the script/package entrypoint, including `python -m <package>`. Catalog name and
regex rules run only on those surfaces. Exclusions run on the same identity surfaces plus
only the immediate subcommand. Python and PyPy wrappers accept tightly anchored numeric
version suffixes such as `python3.12`; interpreter-like prefixes such as `pythonish-tool`
are not wrappers.

Structured argv from the detector is authoritative. If unavailable, the registry parses
the command line into argv as a fallback. Generic shell interpreters are rejected, and
the catalog intentionally avoids ubiquitous names such as shells, `ssh`, `node`,
`python`, and plain `git`. Wrapper patterns must stay narrow enough to identify known
tool entrypoints without turning a runtime process into a false positive.

Built-ins are embedded and custom entries with an existing ID replace that built-in.
Regexes are compiled once and case-insensitively. Image resolution prefers explicit URL,
key, then configured icon source, with the self-hosted generic mark as fallback; flagship
tools use explicit self-hosted URLs.

`NewWithCustom` validates custom-tool match and exclusion regex syntax plus Discord-facing
IDs, display names, resolved image keys/URLs, and buttons before converting entries into
runtime tools. Config loading calls the same validation boundary, so a malformed
`match.regex` or `exclude` is reported with its custom-tool index and the existing config
fail-closed behavior disables presence instead of allowing registry construction to fail
later (#477). Registry construction still compiles and retains the validated regexes once
for matching. Display names contain 2–128 runes so they can safely populate Discord image
tooltip text; this surfaces invalid custom configuration before publication. Resolved
images are bounded to Discord's limit and resolved URLs must be absolute HTTP(S). Button
labels are non-empty and at most 32 characters, each URL is absolute HTTP(S), and no
activity receives more than two buttons.
Display names, resolved `image_key`, and button labels are also rejected outright if they
contain a terminal escape sequence, C0/C1 control character, or Unicode bidi formatting
control (via `firstDisallowedRune`, built on the same rune class `terminaltext.Sanitize`
strips at terminal-rendering boundaries) — this gives the user an actionable config-load
error naming the offending codepoint and its rune position (e.g. "found U+200F at position
4") instead of letting raw control bytes reach the Discord IPC payload (#419). Rejecting
bidi formatting controls (not just C0/C1) is a deliberate anti-spoofing choice, and does
not reject legitimate non-Latin display names — combining marks, joiners (ZWJ, Persian
ZWNJ), and multi-codepoint emoji sequences are untouched; only the small set of explicit
bidi override/embedding/mark codepoints is rejected. `ValidateHTTPURL` is the shared
URL-scheme boundary, used for `image_url` and every button URL — it requires an absolute
HTTP(S) URL *and*, since #444, also runs `firstDisallowedRune` over the whole URL string.
Before #444 it was only `url.ParseRequestURI` plus a host/scheme check: that rejects raw
ASCII control bytes as an incidental side effect of URI parsing, but accepts a C1 control
(e.g. U+0085 NEL) or a Unicode bidi override (e.g. U+202E RIGHT-TO-LEFT OVERRIDE) outright,
and — because `normalizeActivity` in `internal/presence` deliberately never sanitizes
URLs, to avoid corrupting them — that rune reached the Discord wire unchanged in both an
image URL and a button URL, letting the visible link text read differently from the
address it actually resolves to. `ValidateHTTPURL` is the *only* defence for this class of
URL field, so it must reject the same rune classes the text fields reject, not merely
validate the URL's shape and length.

Do not replace identity matching with `gopsutil.Terminal()` filtering: it is not
implemented on Darwin and would remove all macOS presence. Short exact catalog names
such as `lf`, `mc`, `task`, `spt`, and `dust` remain product ambiguities.

**Depends on / used by:** Consumes config-facing custom tools. Used by `internal/detector`,
presence metadata, usage pruning, settings, status, and watch.

**Open questions / TODO:** Catalog priorities and ambiguous short names may need product
review as usage data accumulates.
