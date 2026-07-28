# update (package `internal/update`)

**Purpose:** Performs privacy-preserving release checks, records check and automatic
install outcomes, detects install ownership, and runs the matching updater without
replacing the running process in place.

**Public surface:** `ReleaseSource`, `GitHubReleaseSource`, `Checker`, and `Result` own
release lookup and caching. `DefaultCachePath` resolves update state. `InstallMethod`,
`DetectInstallMethod`, `IsSystemPackageInstall`, `GuidanceForMethod`,
`UpdateCommandForMethod`, `GenericInstallDir`, and `PerformUpdate` select/run an
install-aware exact-tag updater. `AutomaticUpdateAttempt`,
`ReadAutomaticUpdateAttempt`, and `RecordAutomaticUpdateAttempt` persist automation
status.

**Key files:** `internal/update/update.go` contains anonymous GitHub lookup, semver,
cache/lock handling, attempt persistence, updater commands, install detection, and generic
installer download. `internal/update/exec_*.go` run platform commands.

**Invariants / gotchas:** `NO_UPDATE_CHECK` is presence-based; invalid/`dev` versions,
opt-out, unusable cache paths, and passive lookup errors fail closed to no result. Failed
checks are cached for 24 hours and a short-lived lock prevents concurrent checks. Lock
files older than 30 seconds are reclaimed so a crashed process cannot suppress checks for
the cache lifetime. Release requests send only `termp/<version>` as User-Agent.

Install detection resolves executable symlinks, then recognizes Homebrew, Linux system
packages, Go, or generic ownership. A resolved `/usr/bin/termp` on Linux is
package-managed. Bounded `dpkg-query --search` and `rpm --query --file` checks identify
Debian versus RPM ownership; tool presence is the fallback, and ambiguous hosts retain a
distinct system-package method whose guidance names both release-asset installation paths. Homebrew detection
uses standard roots plus a once-cached, 500ms-bounded `brew --prefix`, and requires the
resolved executable to match the `termp` Cellar or Caskroom layout. Go locations include
a once-cached, 500ms-bounded `go env GOBIN GOPATH`, the `GOPATH` environment variable,
and `~/go/bin`. Other resolution uncertainty is generic.

Automatic attempt state shares the update cache and records time, target, error, and a
distinct skipped flag. All cache read-modify-write transactions share a cross-process lock,
so check-cache writes preserve concurrent automatic-attempt records. Status reports
failed/skipped attempts; recording a later success leaves no reportable error and clears that warning.
All updater commands validate and pin the exact semver tag. Generic updates download the
tagged installer, pass a tagged archive URL, and explicitly set `BINDIR` to the resolved
running executable's directory so a vanished custom environment does not create a
second binary. The stored generic installer pipeline remains exact-tagged and
injection-free, but user-facing notices deliberately print `termp update` instead of
that pipeline so the resolved install directory is preserved. Windows generic self-update
is unsupported. System-package methods expose exact-tag commands that download the
matching GitHub release `.deb` or `.rpm` and install the local file with apt or dnf; they
are rejected by `PerformUpdate`, preserving package-manager ownership. Failed interactive
Homebrew and Go updates display the retry command produced
by `UpdateCommandForMethod`; generic failures tell users to resolve the reported error and
retry `termp update`, avoiding a fallback installer invocation that could drift `BINDIR`.

Homebrew-owned installs delegate to `brew upgrade polter-dev/tap/termp` without `--cask`;
Homebrew resolves that fully qualified token to the Cask published by GoReleaser.

**Depends on / used by:** Standard library only. `cmd/termp` uses it for status, version,
interactive updates, notices, and opt-in daemon-start automation.

**Open questions / TODO:** None.
