# usage (package `internal/usage`)

**Purpose:** Stores bounded, local-only per-tool usage so settings can rank the pin
picker by the user's common tools.

**Public surface:** `StatePath` resolves the platform/XDG state file. `New`, `Load`, and
`Save` manage a concurrency-safe `Store`. `Record` increments usage, `Rank` orders IDs by
count/recency/ID, and `Prune` applies registry-aware retention.

**Key files:** `internal/usage/usage.go` contains state paths, bounded loading, atomic
save, counting, pruning, ranking, and the hard cap. `replace_windows.go` provides
retry-bounded `MoveFileEx` replacement; `replace_other.go` uses rename.

**Invariants / gotchas:** The store holds at most 1,024 entries, keeping the most recent
then most-used entries when capped. Loads reject files over 1 MiB. `Record` saturates
`Count` at `math.MaxInt` instead of overflowing. A missing file loads empty with a nil
error. A corrupt, oversize, or otherwise unreadable file also loads an empty store, but
`Load` now returns the underlying error instead of nil (#567): absent and corrupt are not
the same outcome, and a caller must not treat a non-nil error as license to save the
returned empty store back over the real file. `cmd/termp/main.go`'s daemon startup does
exactly that: a load error disables `usage.Save` for the rest of the run.

`Prune` removes an ID only when it is absent from the complete built-in-plus-custom
registry and its `LastSeen` is older than 90 days. Process-scan absence is never a pruning
signal. An empty registry is treated as unavailable and never retention-prunes (the hard
cap still applies).

Save creates parents and atomically replaces through a temporary file. Windows uses
`MoveFileEx` and bounded retries for transient sharing/access failures.

**Depends on / used by:** Standard library only. Daemon startup/reload records and prunes
against the successfully constructed registry; settings reads ranking.

**Open questions / TODO:** `Seconds` remains in the JSON schema but is not accumulated.
