# CLI (package `cmd/termp`)

**Purpose:** Owns command dispatch, daemon lifecycle, status/control output, setup
wiring, update policy, installation guidance, and TUI startup.

**Public surface:** The `termp` binary exposes start/stop/status, autostart,
settings/watch/setup, config, completion, version, update, and full-uninstall commands.
Windows also exposes `connect`. `install.sh` is the canonical generic release installer.

**Key files:** `cmd/termp/main.go` owns dispatch, daemon operation, status, setup, and
usage/config wiring. `cmd/termp/connect.go` and `control_*` own daemon control.
`cmd/termp/update.go` owns manual notices, the cached command alert, the daemon's
update-check cache refresh, and opt-in automatic updates.
`cmd/termp/configload.go` owns the slow-load stderr notice and the pre-dispatch
update-alert eligibility/dedup gate (#442). `spawn_*`,
`pidfile_*`, and `shutdown_*` contain platform lifecycle behavior. `install.sh` installs
tag-pinned archives. `.github/workflows/release.yml` and `.goreleaser.yaml` own releases,
including gated Homebrew Cask and Scoop manifest publication;
`.github/workflows/verify-release-secrets.yml` provides the manual publishing-token
pre-flight check. `.github/workflows/ci.yml` owns cross-platform tests and real
package-layout integration probes.

**Invariants / gotchas:** Start treats the validated PID file as final arbiter, also
recognizes a fresh same-user/same-executable `discord.json` publisher, and waits for
bounded child readiness. Stop targets both a valid PID owner and a distinct valid
publisher. Windows publishes readiness only after shutdown primitives exist. Autostart
disable/uninstall stop the tracked daemon and report partial failure if it survives.

Connect never starts a daemon. It prefers the validated publisher, then the PID owner.
Windows uses a current-user PID-addressed named pipe and verifies the server process.
`--force` reconnects; ordinary already-connected calls are successful no-ops. Response
and readiness share a bound. Non-Windows transport remains explicitly unsupported, so
help and shell completions hide `connect` there and direct invocation reports that the
command is not yet supported on the platform.
Status trusts a fresh connected publisher instead of probing Discord and exposes
concurrent PID/publisher faults. Internal command errors are phrased to compose after the
CLI adds its user-facing prefix while retaining proper-noun capitalization.
`formatDiscordStatus`'s unmatched-error fallback reports `unknown (<err>)` rather than
asserting the specific "Discord is running but unreachable" diagnosis it has not
established; only the sentinel errors that actually mean that (`ErrDiscordIPCUnreachable`)
render that text. A malformed `DISCORD_IPC_PATH` override (`ErrDiscordIPCOverrideInvalid`,
`internal/presence`) gets its own `misconfigured (DISCORD_IPC_PATH override is invalid)`
line rather than falling into either the unreachable or the generic unknown case.

`termp status` reports Discord *connection* health (`Discord: connected`) and the last
activity *publication* result (`Published:` — present only when the most recent publish
attempt was rejected) as two separate facts: classic Discord IPC can permanently reject a
payload (code 4000) while the connection stays healthy, so a rejection was previously
invisible — `status` said "connected" while nothing reached the user's profile (issue
#404). `presence.Writer` reports this through a new `WithPublicationState(func(error))`
option (`internal/presence/writer.go`), fired with the rejection error on a permanent
per-payload rejection and with `nil` once either a later publish succeeds or presence is
cleared entirely (nothing left to reject) — either is what clears a reported rejection,
never merely the passage of time. The daemon persists it in `daemon.json`'s
`publication_ok`/`publication_error`/`publication_at` fields (mirroring `config_ok`) via
`runDaemonDiscordStatePublisher`'s `publication` publisher, and `statusPublicationHealth`
(cmd/termp/main.go) only trusts a fresh record from the currently-running daemon's PID,
matching the existing `statusConfigHealth` pattern.

Plain legacy PID records remain readable for stale-file cleanup, but a true legacy
record without a process start time never authorizes signaling. New records explicitly
mark an unavailable start time and fall back to executable identity so a live daemon is
not orphaned when process metadata cannot be read; status and connect's PID-file fallback
honor that marker. Publisher/Discord-state records still require their recorded process
start time. Verified records use process start time to detect PID reuse during shutdown
polling.

Setup rewrites enabled service definitions but does not relaunch an already-running
daemon. Config/autostart success survives a completion-only failure and the summary
reports that partial outcome. Completion removal attempts every shell; details live in
[`completioninstall.md`](completioninstall.md).

Plain `termp uninstall` remains the start-at-login removal alias and points users to
`termp uninstall --all`. Full uninstall confirms unless `--yes` is supplied, stops and
validates the daemon through the same process-image-aware stop path before deleting
anything, then removes autostart, completions, config, state/cache, and the platform log.
The confirmation requires stdin and stdout to pass the operating system's fd-level TTY
query, so character devices such as `/dev/null` cannot enter the Bubble Tea prompt and
stall on EOF. It never deletes the running executable. It reuses install ownership
detection and the resolved generic install directory to print exact Homebrew, Scoop, apt,
detected RPM front-end (`dnf`/`zypper`/`yum`, or `rpm -e`), Go, generic Unix, or Windows
binary-removal guidance. Destructive tests inject paths beneath an asserted temporary
home. Full uninstall also removes the daemon log's rotation lock and three retained
generations.

Automatic updates are fail-open, asynchronous, and non-interactive. Unix generic
installs preflight the resolved running executable's directory and record a skipped
reason when it is not writable. Generic Windows installs record an unsupported-platform
skip; Go and Homebrew installs remain eligible. Scoop- and Debian/RPM-owned installs
record a managed-package skip without invoking an updater. Automatic Unix commands use
a separate process group so timeout cancellation terminates their full process tree.
Interactive `termp update` commands stay in the foreground process group, allowing
`sudo` in both generic and deb/rpm updates to read from the controlling terminal.
Attempts are visible in
`termp status` under `Updates > Automatic`, gated by `automaticUpdateStatus`/
`automaticUpdateFailure` (cmd/termp/main.go). The recorded failure/skip is retired the
moment it is no longer true, not only when some future automatic attempt happens to
succeed (issue #418): it is suppressed once the running version is no longer older than
the recorded target — covering a later automatic success, a manual `termp update`, a
package-manager upgrade, or any other way the user reached that version — and it renders
nothing at all while `auto_update` is disabled, since the section describes automatic-
update behavior that is not currently running. `runAutomaticUpdateWithStatePathForPlatform`
(cmd/termp/update.go) additionally erases the stale record outright rather than leaving
the stale JSON sitting in the cache indefinitely.

**Stale automatic-attempt clearing (#418, #458).** `retireStaleAutomaticUpdateAttempt`
(cmd/termp/update.go) owns the rule and runs on every daemon startup where
`update_check` is on, *before* the `auto_update` branch — a record recorded while
automatic updates were enabled and then stranded by turning them off (often *because*
they failed) is exactly the case that has to clear (#458 cause 1, gate moved in #459).
A recorded **failure** is stale when either:

- the running version already satisfies the target (`!IsNewer(current, target)`), the
  original #418 rule; or
- the release source is not offering the target
  (`!SameVersion(target, latest) && latest != ""`), added for #458 cause 2.

The second rule exists because the first alone cannot retire a target that is bogus:
the reporter's cache held `target_version: "1.1.0"`, which is not a real release and
sorts newer than every shipped version, so `!IsNewer` was never satisfied and the record
was immortal.

`latest` is the version this run's check reported, falling back to
`updatepkg.LastKnownLatest(statePath)` when the check produced no result. **That fallback
is load-bearing, not a nicety:** `Checker.Check` returns `ok == false` whenever the
running version is already current, so a rule written only against the check's own
result never executes in the exact state the reporter's machine was in (latest release
== running version). A `latest` of `""` means no successful check is on file — an
unreachable release source is not evidence a target is gone — and no clearing conclusion
is drawn from it.

Deliberate asymmetry: this errs toward **preserving**. A failure for the version still
being offered is kept, because that is a real failure `termp status` is the place to
learn about; only a target the source is demonstrably not offering is deleted. Nothing
is inferred from silence. Successful attempt records (`Error == ""`) are never touched —
`clearStaleAutomaticUpdateAttempt` returns before consulting the staleness predicate.
Clearing stays best-effort throughout: read and write failures are logged via `debugf`
and never propagate, so nothing here can delay or fail daemon startup.

Interactive
`termp update` on a Scoop install prints the available-version header and
`To update: scoop update termp` guidance without a system-package preamble, an
`Updating...` line, stderr output, or an installer command. For a known Debian/RPM-owned
install it downloads the exact-tag, architecture-specific release package and
`checksums.txt` into private temporary files, verifies SHA-256 fail-closed,
and then runs `sudo apt install -y <file>` or the detected RPM front-end command
(`dnf`/`zypper`/`yum`/`rpm`, probed in that order). It never uses
the generic installer or writes to `/usr/local/bin`. The interactive package path requires
stdin to pass a real fd-level TTY query rather than accepting any character device. When
sudo, the package manager, or a TTY is unavailable, package ownership is ambiguous, or
download/checksum/install fails, the command reports the reason and prints the existing
exact-tag GitHub release download and local apt or detected-RPM-front-end instructions.
Automatic/background updates never enter this path and continue to record a
managed-package skip with zero commands. A failed executable update
prints the exact retry command for Homebrew and Go, while a non-Windows generic failure
says to resolve the reported error and retry `termp update`. A Windows generic (archive)
install is a permanent platform limitation rather than a transient failure — Windows locks
a running executable — so `runUpdate` never attempts `PerformUpdate` for it: it skips the
misleading `Updating termp from X to Y...` line and prints non-runnable `To update:`
guidance (tag-pinned `go install ...` plus the GitHub releases/latest URL) with no retry
line and exit code 0, matching the Scoop path's exit-0 convention (issue #389). Update
notices label multi-step system-package instructions `To update:` rather than `Run:`.
They print `termp update` for generic installs, preserving the resolved executable
directory; Homebrew and Go notices continue to print their direct package commands under
`Run:`.

Updater and installer curl downloads allow 10 seconds to connect and 300 seconds total;
the installer's wget fallback uses a 10-second timeout and one attempt. These per-transfer
bounds do not add a deadline to interactive `termp update`: its context remains
deliberately unbounded so `sudo` can wait indefinitely for a human password prompt,
preserving the issue #382 contract.

Homebrew updates run `brew upgrade polter-dev/tap/termp` without `--cask`. Homebrew
resolves the fully qualified token to the GoReleaser-published Cask; the orphaned
hand-written Formula draft was removed under #303.
Homebrew Cask installs, Scoop's post-install hook, and deb/rpm postinstall scripts use
the same prominent, fixed-width ASCII setup-box treatment; the generic installer uses
an adaptive-width counterpart. Homebrew prints Cask caveats unindented at column 0, so
the box renders intact, but `Cask::Installer#install` prints them before fetching,
staging, installing artifacts, and running the quarantine hook. The Homebrew box
therefore tells users to run setup after Homebrew finishes; the post-install surfaces
retain their installed/run-now wording. For a single-cask install, Homebrew's final
message collector does not reprint caveats, so the
binary's config-missing first-run card remains the Homebrew user's only setup reminder
at invocation time. GoReleaser's caveats templating trims authored padding blank lines;
Homebrew instead emits one trailing blank line through the installer's caveats heredoc.
The deb/rpm package script always exits successfully so guidance cannot break an
install.

CI runs Staticcheck 2025.1.1 in its own pinned job. It also snapshot-builds and installs
the real deb and rpm artifacts in digest-pinned
`debian:stable`, `fedora:latest`, and `opensuse/leap:latest` containers. Before the
detection probe runs, the job plants a disabled stub for the *other* package tool (`rpm`
on the debian job, `dpkg-query` on the fedora/openSUSE jobs) into `/usr/sbin`, never
`/usr/local/bin`. Without that
stub, `classifySystemPackage`'s tool-presence fallback alone reproduces the expected
`debian`/`rpm` answer in these images (each image ships exactly one of the two tools),
so the assertion could pass even if live ownership detection were completely broken.
With both tools appearing present, the fallback always resolves to `system-package`, so
a `debian`/`rpm` result from the package-owned `/usr/bin/termp` probe can only come from
a real, successful `dpkg-query`/`rpm` ownership query. `DetectInstallMethod` only calls
into ownership detection when the executable path is exactly `/usr/bin/termp`, so the
job's negative control has to run there too: it purges the package (removing both the
ownership record and the file), places the probe at `/usr/bin/termp` by hand, and asserts
detection now reports `system-package` for that same path once neither tool owns it. It
then reinstalls the real package and re-checks the positive `debian`/`rpm` result before
restoring the packaged binary, so ownership is genuinely back in place for the update
exercise below. The packaged CLI then uses a local TLS release
stub with outbound networking disabled; the job asserts a redirected update refuses
before `curl` and leaves `/usr/local/bin` unchanged. Because the containers run as root,
this does not cover an interactive sudo password prompt (#382), which needs a pty and a
human. It also cannot cover #364 shadowing on a *successful* update: the update here is
always refused, so the `/usr/local/bin` before/after snapshot only proves a refused
update writes nothing — it compares empty to empty, not a real shadowing binary against
the package-managed one.

Every rpm-method leg sets a matrix `rpm_frontend` and asserts it is non-empty before
proceeding, so a typo'd or omitted matrix key fails loudly instead of silently skipping
the front-end exercise below and passing having installed nothing (#406). The fedora leg
(`rpm_frontend: dnf`) and the openSUSE leg (`rpm_frontend: zypper`) each remove the RPM
and install the real unsigned snapshot artifact with their claimed front-end, then
re-run the package-ownership assertion and additionally assert `DetectRPMManager()`
(printed by the probe binary's `rpm-manager` argument) equals the claimed front-end, so
the leg proves the manager termp would actually pick, not just the broader install
method (#405, #406). The fedora leg runs `dnf install -y`, matching the updater argv.
`opensuse/leap:latest` ships zypper >= 1.14, so the openSUSE leg's primary install runs
`zypper --non-interactive install --allow-unsigned-rpm`, the exact argv `rpmInstallArgs`
selects on that platform (#407) — this is now the observed path, not a reasoned one. It
then also reinstalls with the older global `zypper --non-interactive --no-gpg-checks`
flag as a secondary check, confirming a current zypper still accepts it; that is
regression coverage for the fallback flag's continued acceptance, not a substitute for
real zypper 1.13 coverage, which does not exist (see `update.md`'s open gap). A
`yum`/CentOS 7 leg was considered but skipped as lower priority; the `yum)` case arm in
the script is unreachable scaffolding until such a leg exists.

The installer labels `termp uninstall` as a login-only action rather than implying that
it removes the binary. It resolves one tag for archive/checksum, prefers
`https://termp.polter.sh/dl/curl/{os}/{arch}/{tag}`, and falls back to the tag-pinned
GitHub asset. Every fatal installer path prints the advertised fetch-and-pipe retry
command, with explicitly supplied `VERSION`, `BINDIR`, and `TERMP_DOWNLOAD_CHANNEL`
preserved immediately before `sh`; atomic staging prevents failed installs from leaving
a partial destination binary.
Tag runs create a draft release (`release.draft: true`) and attach the generated Cask and
Scoop manifest without writing the tap or bucket. Only `release.published` triggers jobs
that verify the release is public, download those exact generated files, and update the
tap and bucket with sha-comparison idempotency.
Before a tag is pushed, the manually dispatched **Verify release secrets** workflow
checks that each cross-repository publishing token is present, can see its configured
target, and has `permissions.push` access without writing to that repository. Its token
probes run in separate steps and report every invalid token in one run.

Config initialization safety is documented in [`config.md`](config.md), terminal
rendering in [`tui.md`](tui.md), update cache/detection in [`update.md`](update.md), and
usage retention in [`usage.md`](usage.md).

The macOS process-image verifier calls `proc_pidpath` through `SYS_PROC_INFO` because
`x/sys` exposes no libSystem wrapper for it; that single call carries a narrow Staticcheck
SA1019 suppression rather than disabling the check repository-wide.

Structured CLI output and verbose log messages sanitize
externally derived terminal text. Detection logs resolve the working directory through
the same effective privacy policy and path-reduction helper as presence output, cap
expanded paths at their final two components, and report `hidden` when directory display
is not allowed. Labelled status values, single-line log records, and the interactive
watch TUI's warning banner replace embedded line breaks with a visible ` ; ` separator
before sanitizing, preserving multi-step update commands without breaking status-column
alignment, gluing tokens, or permitting log-line injection. The substitution recognizes
every line/record-break character a terminal or log pipeline could honor (not just
CR/LF; see [`terminaltext.md`](terminaltext.md) for the exact list), collapses runs of
separators, and trims leading/trailing separators so a leading/trailing or blank-line
break in the source value never leaves dangling or doubled punctuation. Daemon,
interactive-watch, and `watch --once` config warnings share one logging helper that owns
both iteration and single-line sanitization. The
package-manager-unknown update guidance from `internal/update` is the concrete case that
motivated the collapsing: it joins Debian and RPM instructions with a blank line.
Per-tool enablement is passed into detector selection, and hot reloads that change it
trigger an immediate rescan; the global `enabled` switch still clears mapped presence.
Live watch loads the daemon's episode anchors for matching elapsed-time display but runs
the detector read-only, leaving `presence.json` persistence exclusively to the daemon.
It shares the daemon's config-change transaction and hot-reconfigures detector settings;
display-only changes re-render the last detection without forcing another scan.
When initial config loading fails, `watch --once` warns on stderr that built-in defaults
are active with presence disabled and points to `termp status`; interactive watch shows
the same warning inside the alternate-screen view so it remains visible without
corrupting the terminal. At daemon startup an invalid existing config is reported to the
invoking stderr and daemon log, the daemon stays running with presence off, and a valid
hot reload restores normal operation (with an absent-key `enabled` loosening subject to
config's extended guard). A failed hot reload keeps last-good behavior,
writes a sanitized daemon log line, and updates the live watch warning banner without
writing through the global logger from the watch goroutine. The existing daemon state
record carries config health, so `termp status` distinguishes startup failure (`off`)
from reload failure (`using last-good config`) while showing the error. A successful
reload clears that health error and the watch banner. A reload-introduced config warning
(for example an unknown key added by an edit) is logged to the daemon log through the
same `logConfigWarnings` helper startup warnings use (issue #416 comment), not just
surfaced the next time something re-loads the file for `termp status`.
Daemon and interactive-watch startup share `newWatchedConfigManager`, which constructs
the manager from a settled snapshot, installs or attempts the watcher, performs one
settled `Manager.Reload`, and only then returns the config used by warnings, automatic
updates, detection, and startup error rendering. An existing empty snapshot at
construction is held fail-closed and keeps config's `enabled` loosening guard armed. Thus
a non-atomic save that completes during startup is either read by a settled load/reload
or leaves a queued watcher event instead of stranding transient defaults for the process
lifetime (#435, #440).

Every direct CLI config read inherits config's settled-read protection. Read-only paths
such as update notices, version, pre-spawn error rendering, status, and `watch --once`
use `LoadReadOnly`, so stable existing files add one ~15ms poll interval and a missing
first-run config remains immediate. If another process keeps rewriting the file,
read-only commands render from the newest snapshot after config's 500ms standalone
bound instead of hanging. Setup/settings use the safe-by-default `Load` because they can
save the loaded whole document: an existing blank must persist through the three-second
loosening horizon before it can seed defaults, preventing a truncate stall beyond the
ordinary settle budget from durably erasing the user's opt-out and unrelated settings
(#438). A continuously changing file instead returns `ErrConfigBeingWritten`; both
commands propagate it before any save or TUI work, leaving the file byte-identical.
Their normal nonblank path still adds only the ordinary settle interval.

A genuinely blank config is deliberately ambiguous and the resulting wait is correct
(#438/#434), but a silent multi-second pause with no output looked like a hang: `setup`
and `settings` sat for the full ~3s horizon, and `status`/the `version` subcommand paid
config's ~300ms settle bound, with **no indication why** (#442). `loadConfigWithNotice`
(`cmd/termp/configload.go`) wraps a config load in a goroutine and prints one `checking
config…` line to stderr only if the load has not returned within
`checkingConfigNoticeDelay` (150ms) — normal existing configs and missing first-run files
resolve well under that and print nothing new; a blank or continuously-rewritten file
does. `main()`'s pre-dispatch update-alert check previously called `config.LoadReadOnly`
unconditionally before evaluating `eligibleForUpdateAlert`, so a command that is not
alert-eligible (`status`, non-interactive `settings`) or that loads config again itself
(`setup`, `settings`) paid the settle/horizon cost twice for the same file with no benefit.
`maybePrintCommandUpdateAlert` now checks eligibility (and a `commandsLoadConfigForOwnAlert`
skip-list for setup/settings) before loading anything, and setup/settings print the same
alert from their own already-loaded config instead of main() loading a second time.
`status`'s own load goes through the package var `readOnlyConfigLoader` (defaults to
`config.LoadReadOnly`) so tests can substitute a counting stub and assert "loads config
exactly once" on an observable rather than on timing.

**Update alert reachability (#457).** The one-line stderr alert ("A new version (X) is
available — run termp update", `printCommandUpdateAlert` in `cmd/termp/update.go`)
now follows a deny-list, not an allow-list: `eligibleForUpdateAlert` returns true for
every known command except `update`, `version`, and `status` (which run their own live
check and print the richer `Update available: X -> Y` block, so alerting would
double-print) and `completion` (whose stdout is eval'd at shell startup). Unknown
commands are also excluded so a typo does not pay a config load and a nag on top of the
error. Three things changed together, because any one alone would have been nearly
invisible:

1. Bare `termp` runs the watch TUI from `main()`'s `flag.ErrHelp` branch and returned
   before ever reaching the pre-dispatch alert, so the most human invocation of all was
   the only one that never mentioned a new release. It now calls
   `maybePrintCommandUpdateAlert("watch", …)` immediately before entering the TUI (same
   single `readOnlyConfigLoader` load explicit `termp watch` already paid).
2. The gate is now "is `os.Stderr` a TTY", not "is the command interactive". The alert is
   written to stderr, so a TTY there means a human is watching, while a redirected or
   piped stderr means something is capturing `2>&1` that an extra line could corrupt.
   This shows the alert in more human situations (`config`, `watch --once` with a
   terminal stderr) and is deliberately *stricter* than before for `install`/`uninstall`/
   `start`/`stop`, which used to print it even with stderr redirected.
3. The load-bearing part: `printCommandUpdateAlert` reads `CachedCheck`, which never
   touches the network, and the daemon used to refresh that cache only when
   `auto_update` was on — i.e. never for the exact population the alert exists for.
   `runAutomaticUpdateWithStatePathForPlatform` is now gated on `update_check` alone;
   after the check it returns before any preflight/install when `auto_update` is off.
   Automatic *installing* is unchanged and still requires `auto_update`.

Scope of the refresh, stated honestly: it happens once per daemon process, at daemon
start, because `internal/update`'s `Checker` performs at most one lookup per process and
caches for `cacheLifetime` (24h). A machine whose daemon keeps running past 24h without a
restart still has no periodic refresh; `termp version`, `termp status`, and `termp
update` remain the other refresh points. `update_check = false` and `NO_UPDATE_CHECK`
still suppress every network call and the alert itself, and a config load error still
suppresses the alert.

Cost note: broadening eligibility means commands that load config for their own work now
also pay `main()`'s pre-dispatch `LoadReadOnly` — one extra settled read, the same one
`start`/`install`/`stop` already paid. `setup`/`settings` remain on the
`commandsLoadConfigForOwnAlert` skip-list, so the expensive 3s-horizon load (#442) is
still paid only once.

If the daemon cannot start its config watcher at all — `config.EnsureConfigDir` or
`Manager.Watch` failing, for example because the config directory path is occupied by a
stray file — `startConfigWatchWithRetry` (cmd/termp/main.go) logs `config watch
disabled: ...` exactly like `termp watch` already did, then keeps retrying in the
background (`retryConfigWatch`, every `configWatchRetryInterval`, quietly after the first
failure) until the watch starts or the daemon exits. Previously this failure was silent
and permanent: the startup message promises presence recovers once the config is valid,
but with no watcher ever established, a user who fixed the problem stayed stuck at 0
reloads until they restarted the daemon (issue #416).

Detached daemons and the macOS launchd foreground daemon own their log file through the
same dependency-free rotating writer with a 1 MiB threshold and three retained
generations. The launch agent sends its inherited stdout/stderr to `/dev/null` and passes
an internal marker that makes the daemon open the log itself, so launchd never retains a
rotated inode. Linux autostart is not covered by file rotation because systemd sends its
output to journald. Rotation happens before a complete logger write, uses rename rather
than copy/truncate, and coordinates writers through a cross-process lock; a writer whose
open file was rotated reopens the current path before its next line. Daemon stderr is
rebound whenever the current generation changes so Go panic stacks remain in the bounded
log rather than following an orphaned inode. This keeps individual log records intact
and bounds normal verbose/crash-loop growth.
The banner, startup error, and reload error render through `SanitizeSingleLine`, matching
every other single-line render boundary in the CLI.

**Depends on / used by:** Composes every `internal/*` package and is the application
entry point. Release automation depends on GitHub Actions and GoReleaser.

**Open questions / TODO:** Implement and live-verify non-Windows daemon control transport.
