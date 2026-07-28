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

`NewWithCustom` validates Discord-facing custom-tool IDs, display names, resolved image
keys/URLs, and buttons before converting them into runtime tools. Display names contain
2–128 runes so they can safely populate Discord image tooltip text; this surfaces invalid
custom configuration before publication. Resolved images are bounded to Discord's limit
and resolved URLs must be absolute HTTP(S). Button labels are non-empty and at most 32
characters, each URL is absolute HTTP(S), and no activity receives more than two buttons.
`ValidateHTTPURL` is the shared URL-scheme boundary.

Do not replace identity matching with `gopsutil.Terminal()` filtering: it is not
implemented on Darwin and would remove all macOS presence. Short exact catalog names
such as `lf`, `mc`, `task`, `spt`, and `dust` remain product ambiguities.

**Depends on / used by:** Consumes config-facing custom tools. Used by `internal/detector`,
presence metadata, usage pruning, settings, status, and watch.

**Open questions / TODO:** Catalog priorities and ambiguous short names may need product
review as usage data accumulates.
