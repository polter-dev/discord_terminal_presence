# update (package `internal/update`)

**Purpose:** Performs privacy-preserving release checks, records check and automatic
install outcomes, detects install ownership, and runs the matching updater without
replacing the running process in place.

**Public surface:** `ReleaseSource`, `GitHubReleaseSource`, `Checker`, and `Result` own
release lookup and caching. `DefaultCachePath` resolves update state. `InstallMethod`,
`DetectInstallMethod`, `CommandForMethod`, `UpdateCommandForMethod`, and `PerformUpdate`
select/run an exact-tag updater. `AutomaticUpdateAttempt`,
`ReadAutomaticUpdateAttempt`, and `RecordAutomaticUpdateAttempt` persist automation
status.

**Key files:** `internal/update/update.go` contains anonymous GitHub lookup, semver,
cache/lock handling, attempt persistence, updater commands, install detection, and generic
installer download. `internal/update/exec_*.go` run platform commands.

**Invariants / gotchas:** `NO_UPDATE_CHECK` is presence-based; invalid/`dev` versions,
opt-out, unusable cache paths, and passive lookup errors fail closed to no result. Failed
checks are cached for 24 hours and a short-lived lock prevents concurrent checks. Release
requests send only `termp/<version>` as User-Agent.

Install detection resolves executable symlinks, then recognizes Homebrew, Go, or generic
ownership. Go locations include a once-cached, 500ms-bounded `go env GOBIN GOPATH`,
the `GOPATH` environment variable, and `~/go/bin`. Resolution uncertainty is generic.

Automatic attempt state shares the update cache and records time, target, error, and a
distinct skipped flag. Check-cache writes preserve it. Status reports failed/skipped
attempts; recording a later success leaves no reportable error and clears that warning.
All updater commands validate and pin the exact semver tag. Generic updates download the
tagged installer and pass a tagged archive URL; Windows generic self-update is unsupported.

Homebrew-owned installs currently delegate to
`brew upgrade polter-dev/tap/termp` without `--cask`, while `.goreleaser.yaml` publishes
a Homebrew Cask. This known contradiction is tracked in #303 pending an empirical test
release and owner decision; do not resolve it in this ledger.

**Depends on / used by:** Standard library only. `cmd/termp` uses it for status, version,
interactive updates, notices, and opt-in daemon-start automation.

**Open questions / TODO:** Resolve the Formula/Cask update contract in #303 after the
test release.
