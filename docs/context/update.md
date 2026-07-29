# update (package `internal/update`)

**Purpose:** Performs privacy-preserving release checks, records check and automatic
install outcomes, detects install ownership, and runs the matching updater without
replacing the running process in place.

**Public surface:** `ReleaseSource`, `GitHubReleaseSource`, `Checker`, and `Result` own
release lookup and caching. `Checker.Check` (memoized, one lookup per process),
`Checker.Refresh` (same lookup without that memoization, for long-lived callers), and
`Checker.CachedCheck` (cache-only, never networked) are its three entry points, and
`CacheLifetime` exposes how long a recorded check suppresses the next lookup.
`DefaultCachePath` resolves update state. `InstallMethod`,
`DetectInstallMethod`, `IsSystemPackageInstall`, `GuidanceForMethod`,
`UpdateCommandForMethod`, `GenericInstallDir`, and `PerformUpdate` select/run an
install-aware exact-tag updater. `AutomaticUpdateAttempt`,
`ReadAutomaticUpdateAttempt`, `RecordAutomaticUpdateAttempt`, and
`ClearAutomaticUpdateAttempt` persist and retire automation status.

**Key files:** `internal/update/update.go` contains anonymous GitHub lookup, semver,
cache/lock handling, attempt persistence, updater commands, install detection, and generic
installer download. `internal/update/exec_*.go` run platform commands.

**Invariants / gotchas:** `NO_UPDATE_CHECK` is presence-based; invalid/`dev` versions,
opt-out, unusable cache paths, and passive lookup errors fail closed to no result. That
gate lives in one place (`Checker.lookupPermitted`) and every passive entry point —
`Check`, `Refresh`, `CachedCheck` — runs it, so an opt-out cannot be honoured by one and
missed by another. `Refresh` differs from `Check` in exactly one respect: it skips the
`sync.Once`, so a process that outlives `CacheLifetime` (the daemon, issue #460) can keep
the shared cache fresh instead of sitting on a fired `Once` while the cache-only command
alert goes silent. It is not a cache bypass — a still-fresh entry from any process
short-circuits the lookup — and it deliberately does not publish into `Check`'s memoized
result, so a concurrent `Refresh` cannot change the single stable answer a short-lived CLI
run gets. Failed
checks are cached for 24 hours and a short-lived lock prevents concurrent checks. Caching
failures is what stops a repeating caller from becoming a retry loop against an
unreachable source. Lock
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
`LastKnownLatest` exposes the release version the last *successful* check wrote to that
cache (a failed check stores an empty version, so `""` means "no successful check on
file", never "no release exists"); it only reads what is already cached and never calls
out. `SameVersion` compares two versions by semver precedence, so a `v` prefix or build
metadata on one side only does not make them differ; an unparseable version is never the
same as anything, including itself. `cmd/termp` uses both to decide which recorded
attempts are stale — see [`cli.md`](cli.md).
All updater commands validate and pin the exact semver tag. Generic updates download the
tagged installer, pass a tagged archive URL, and explicitly set `BINDIR` to the resolved
running executable's directory so a vanished custom environment does not create a
second binary. The stored generic installer pipeline remains exact-tagged and
injection-free, but user-facing notices deliberately print `termp update` instead of
that pipeline so the resolved install directory is preserved. Windows generic self-update
is unsupported: it is a permanent platform limitation (Windows locks a running
executable), not a transient failure, so `runUpdate` detects a Windows generic install
before calling `PerformUpdate` and prints non-runnable `go install`/release-archive
guidance (`WindowsArchiveGuidance`) instead of attempting and then telling the user to
retry. Scoop exposes `scoop update termp` guidance but is rejected by
`PerformUpdate`, preserving package-manager ownership. Command-construction errors use
composable Go error phrasing while retaining proper-noun capitalization before callers
add user-facing context. Interactive Linux system-package updates download the matching
exact-tag,
architecture-specific GitHub release `.deb` or `.rpm` and `checksums.txt` into private
temporary files, require exactly one valid matching SHA-256 entry, and only then invoke
`sudo apt install -y <file>` (deb) or the detected RPM front-end (rpm) as discrete argv.
The RPM front-end is probed with `exec.LookPath` in preference order `dnf`, `zypper`,
`yum`, `rpm` (cached once per process, injectable for tests) and produces
`sudo dnf install -y <file>`, a version-adaptive zypper invocation (below),
`sudo yum install -y <file>`, or `sudo rpm -U <file>`; plain `rpm` is last because it does
not resolve dependencies. The zypper execution additionally probes `zypper --version`
once (cached the same way as front-end detection, injectable for tests) and uses the
newer, semantically scoped install-command option `--allow-unsigned-rpm` on zypper
>= 1.14; on 1.13, or any version that fails to parse, it falls back to the long-standing
global `--no-gpg-checks` option chosen in #394. Both flags exist because plain
`--non-interactive` alone aborts at the safe default when the unsigned release RPM
reaches signature verification; `--allow-unsigned-rpm` is not proposed as a replacement
for 1.13 because it is an unknown option there. CI's openSUSE leg runs zypper >= 1.14, so
the `--allow-unsigned-rpm` branch is now observed against the real unsigned package, but
the `--no-gpg-checks` fallback branch has no zypper-1.13 CI leg and remains reasoned,
not observed. Printed zypper guidance remains
interactive and omits both flags so the user can decide at the prompt. This keeps
openSUSE/SLES and RHEL/CentOS 7 working instead of assuming `dnf`. When no front-end is
present the update fails before any download and
the printed guidance names no specific tool. Guidance, `termp uninstall --all`
binary-removal text (`sudo <manager> remove termp`, `sudo rpm -e termp`), and the
executed argv all use the same detected manager, so printed instructions are always
runnable. Temporary files are removed on every return path. Before an executed Linux
package update touches the network, stdin must be an actual terminal according to the
operating system's fd-level TTY query; merely being a character device (for example
`/dev/null`) is not enough. A missing tool or TTY, ambiguous package type, download error,
empty artifact, missing/invalid checksum, checksum mismatch, or install error fails closed
and leaves the existing exact-tag manual package instructions as the interactive fallback.
Scoop and Linux package updates never use the generic installer or write a shadow binary
under `/usr/local/bin`.

Every updater-owned curl transfer, including the generic installer and both Linux package
downloads, allows 10 seconds to connect and 300 seconds total. For release metadata,
archives, and checksums, `install.sh` gives curl the same connection and transfer bounds
and gives wget a 10-second timeout with one attempt. Interactive update still uses an
intentionally unbounded context so a human can remain at a `sudo` password prompt without
reintroducing issue #382; the downloader flags bound network work independently.

Automatic updates remain non-interactive: all system-package methods short-circuit before
`PerformUpdate`, record a managed-package skip, and invoke no download, `sudo`, apt, or
RPM front-end command. On Unix, interactive update commands remain in the caller's foreground process
group so `sudo` and the generic installer can read a password from the controlling
terminal. Non-interactive automatic update commands run in a separate process group so
context cancellation kills the complete command tree; their existing preflights prevent
any path that could prompt. Failed interactive Homebrew and Go updates display the retry command produced
by `UpdateCommandForMethod`; non-Windows generic failures tell users to resolve the
reported error and retry `termp update`, avoiding a fallback installer invocation that
could drift `BINDIR`. A Windows generic install never reaches that retry path at all —
see above.

Homebrew-owned installs delegate to `brew upgrade polter-dev/tap/termp` without `--cask`;
Homebrew resolves that fully qualified token to the Cask published by GoReleaser.

**Depends on / used by:** Standard library only. `cmd/termp` uses it for status, version,
interactive updates, notices, and opt-in daemon-start automation.

**Open questions / TODO:** None.
