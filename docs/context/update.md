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

Install detection resolves executable symlinks, then recognizes Homebrew, Scoop, Linux
system packages, Go, or generic ownership. Scoop detection accepts the exact user or
global `shims/termp.exe` layout and `apps/termp/<one segment>/termp.exe`, including
`current`, resolved version directories, and roots relocated by `SCOOP` or
`SCOOP_GLOBAL`. If Windows cannot resolve Scoop's `current` junction, detection checks
the unresolved path against those same anchored roots; other Scoop-like paths and
resolution failures remain generic.
A resolved `/usr/bin/termp` on Linux is package-managed. Bounded `dpkg-query --search`
and `rpm --query --file` checks identify
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
is unsupported. Scoop exposes `scoop update termp` guidance but is rejected by
`PerformUpdate`, preserving package-manager ownership. Interactive Linux system-package
updates download the matching exact-tag,
architecture-specific GitHub release `.deb` or `.rpm` and `checksums.txt` into private
temporary files, require exactly one valid matching SHA-256 entry, and only then invoke
`sudo apt install -y <file>` or `sudo dnf install -y <file>` as discrete argv. Temporary
files are removed on every return path. A missing tool or TTY, ambiguous package type,
download error, empty artifact, missing/invalid checksum, checksum mismatch, or install
error fails closed and leaves the existing exact-tag manual package instructions as the
interactive fallback. Scoop and Linux package updates never use the generic installer or
write a shadow binary under `/usr/local/bin`.

Automatic updates remain non-interactive: all system-package methods short-circuit before
`PerformUpdate`, record a managed-package skip, and invoke no download, `sudo`, apt, or dnf
command. On Unix, interactive update commands remain in the caller's foreground process
group so `sudo` and the generic installer can read a password from the controlling
terminal. Non-interactive automatic update commands run in a separate process group so
context cancellation kills the complete command tree; their existing preflights prevent
any path that could prompt. Failed interactive Homebrew and Go updates display the retry command produced
by `UpdateCommandForMethod`; generic failures tell users to resolve the reported error and
retry `termp update`, avoiding a fallback installer invocation that could drift `BINDIR`.

Homebrew-owned installs delegate to `brew upgrade polter-dev/tap/termp` without `--cask`;
Homebrew resolves that fully qualified token to the Cask published by GoReleaser.

**Depends on / used by:** Standard library only. `cmd/termp` uses it for status, version,
interactive updates, notices, and opt-in daemon-start automation.

**Open questions / TODO:** None.
