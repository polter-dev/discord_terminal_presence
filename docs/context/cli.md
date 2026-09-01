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
bounded child readiness. `waitForDetachedStart` (`cmd/termp/spawn.go`) does not report
success the instant it first observes the detached child owning the PID file: once
confirmed, it keeps re-checking (in `detachedStartPollInterval` steps, via the new
`confirmDetachedStartStability` helper) for a `detachedStartStabilityWindow` (400ms,
bounded by whatever of the overall 2s `detachedStartTimeout` budget remains) that the
child is still alive and still owns the PID file before returning nil. This closes issue
#490: the PID file is written before `run()`'s own initialization
(`newDetectionRuntime`, `detector.New`, `presence.NewWriter`, or a panic during setup),
and `start()`'s deferred `removePIDIfOwned` removes that PID file the moment `run()`
returns an error — so without the stability window, a child that died milliseconds after
publishing its PID file was already invisible to the parent, which had already printed
`termp started in the background (pid N)` and exited 0. `start` stays intentionally
lightweight: it still does not wait for the presence loop, first detector scan, or
steady state — only the short stability window, so a healthy start's added latency stays
in the hundreds-of-milliseconds range. Verified by instrumenting a temporary build of
`run()` to sleep 120ms past the PID write and then return an error: before the fix this
printed the success line and exited 0 with the PID file gone a moment later; after the
fix `termp start` exits non-zero, prints no "started" message, and the daemon log records
the injected failure — the instrumentation was reverted before committing (issue #490).
On Windows the detached child is launched from the absolute `termp.exe` path beside the
running process image, never from the invocation path or a Scoop shim, and combines
`CREATE_NO_WINDOW` with the existing new-process-group and detached flags (#508, #510).
The window suppression matters at both layers: a hidden Scoop shim can otherwise launch
its own console-subsystem child without preserving the flag.
New PID and Discord-state records capture the daemon's own
normalized executable path; process validation compares the live image against that
recorded path alongside same-user ownership and start time. This preserves PID-reuse and
foreign-process refusal after an upgrade moves the invoking binary. A new `start` from a
different path refuses to launch a duplicate, names the old path, and directs the user to
`termp stop`; `status` recognizes that daemon and `stop` can signal it through the same
record-bound validation (#476). Legacy records without an executable path retain the
current-binary comparison. Stop targets both a valid PID owner and a distinct valid
publisher. Windows publishes readiness only after shutdown primitives exist. Autostart
disable/uninstall stop the tracked daemon and report partial failure if it survives.
The top-level stop command bounds autostart state discovery with `StatusContext` and
charges that probe plus daemon shutdown against the same five-second deadline, so a hung
service manager cannot postpone daemon signaling indefinitely or extend the command past
its lifecycle budget. The shutdown wait shares its remaining budget across a distinct
publisher and PID-file owner instead of granting each target a fresh timeout (#529). A
prompt probe still drives relaunch-aware PID handling and the successful-stop autostart
hint (#515). A probe that fails or times out leaves the platform "unknown" sentinel, which
is neither "will relaunch" nor "will not": for an installed service that is not explicitly
disabled, stop tolerates a relaunched PID-file owner instead of reporting the
changed-ownership bookkeeping error, and then says so in one line rather than claiming a
clean stop (#534). Known loaded and disabled states are unchanged.

Connect never starts a daemon. It prefers the validated publisher, then the PID owner.
Windows uses a current-user PID-addressed named pipe and verifies the server process.
`--force` reconnects; ordinary already-connected calls are successful no-ops. Response
and readiness share a bound. Non-Windows transport remains explicitly unsupported, so
help and shell completions hide `connect` there and direct invocation reports that the
command is not yet supported on the platform.
Status trusts a fresh connected publisher instead of probing Discord and exposes
concurrent PID/publisher faults. Internal command errors are phrased to compose after the
CLI adds its user-facing prefix while retaining proper-noun capitalization.
The live watch preview uses the same validated publisher fallback as status and daemon
lifecycle, including when the PID file is unavailable (#509). This matters on Windows:
probing Discord directly while the active-tool daemon already owns IPC can fail and used
to make settings-driven preview rerenders claim Discord was not running even though the
fresh daemon publisher reported an existing connection. A disconnected or stale publisher
still falls back to a direct probe, so an old positive is never cached indefinitely.
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

An unparseable PID file (garbage/non-JSON/non-numeric bytes, or a numeric-but-invalid
record such as a negative PID) is treated like a stale one rather than surfacing the raw
parser error: `readPIDIdentityFromFile` wraps the `parsePIDRecord` failure in the sentinel
`errPIDRecordUnparseable`, and `stopDaemon`/`stopDaemonAndPublisher` remove the file on a
best-effort basis via `removeUnreadablePIDFile` (ownership proven by holding the same
owner/regular-file-validated, locked handle and re-checking `os.SameFile` immediately
before removal, so a file that became parseable — a concurrent writer replaced it — is
left alone) and return `errUnreadablePIDFileRemoved`. `termp stop` prints "removed an
unreadable PID file; daemon is not running" and exits 0 instead of `log.Fatal`-ing the raw
JSON error and leaving the file wedged until a manual delete; `stopRunningDaemon` (used by
autostart disable and full uninstall) treats it the same as "wasn't running" (issue #491).

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
The parsed `all` result flows through command dispatch, so every true spelling accepted
by Go's flag parser, including single-dash and explicit truthy values, suppresses the
plain-uninstall guidance after a successful full uninstall (#516).
The confirmation requires stdin and stdout to pass the operating system's fd-level TTY
query, so character devices such as `/dev/null` cannot enter the Bubble Tea prompt and
stall on EOF. It never deletes the running executable. It reuses install ownership
detection and the resolved generic install directory to print exact Homebrew, Scoop, apt,
detected RPM front-end (`dnf`/`zypper`/`yum`, or `rpm -e`), Go, generic Unix, or Windows
binary-removal guidance. The generic Windows removal command deletes both
`termp.exe` and the companion autostart launcher `termpw.exe` (issue #473), which
the Windows archive ships side by side, so a generic install does not leave the
launcher orphaned; Scoop/apt/rpm removals already take the whole package.
Destructive tests inject paths beneath an asserted temporary
home. Full uninstall also removes the daemon log's rotation lock and three retained
generations. Autostart removal deletes the scheduled task regardless of whether it
targets the launcher or the daemon, so the task is never left behind.

Automatic updates are asynchronous and non-interactive; the no-sudo rule is absolute for
them (an unattended update must never invoke `sudo`), so the destination-writability
preflight below is narrowly fail-*closed*, not fail-open. Unix generic installs preflight
the resolved running executable's directory (`genericAutomaticUpdatePreflight`,
`cmd/termp/update_elevation_unix.go`) via `unix.Access(dest, W_OK)` and record a skipped
reason when it is not writable. Only two outcomes let the updater proceed: the access
check succeeding, or the destination not existing (`ENOENT`) — `install.sh`'s own
`[ ! -d "$bindir" ]` guard fails that case closed itself before ever reaching its `sudo`
branch, so it is safe to let the updater run and report the failure. Every other errno,
`EACCES` (unwritable permissions) and anything else including `EROFS` (a read-only
mount), skips with `automaticUpdateElevationError`. This closes issue #495: `unix.Access`
and `install.sh`'s `[ -w "$bindir" ]` write probe share `access(2)` semantics, so a
non-EACCES-unwritable directory (chiefly a read-only mount) that used to fail *open* here
would reach `install.sh`, fail the same write probe there, and hit its `sudo mktemp`/`cp`/
`mv` escalation branch for a non-interactive automatic update — exactly the sudo-on-
unattended-update the rule forbids. A read-only mount cannot be created in the sandbox
this was verified in, so the fix is pinned by an injected-errno unit test
(`TestAutomaticGenericUpdateSkipsOnNonEACCESUnwritableErrno`, `EROFS` via a stubbed
`genericInstallDirAccess`) alongside the existing `EACCES`-skips and `ENOENT`-proceeds
(`TestAutomaticGenericUpdatePreflightFailsOpen`) tests, not an end-to-end RO-mount repro.
An install-directory *resolution* failure (`genericUpdateInstallDir` erroring, e.g. the
running executable can no longer be resolved) is unrelated to writability and remains
fail-open via a separate `automaticUpdateInstallDirError` path — the installer reports and
persists that failure itself. Generic Windows installs record an unsupported-platform
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

**Update-check opt-outs (#463).** There are two, and since #463 they are *exactly*
equivalent in effect:

- `update_check = false` in config, and
- the `NO_UPDATE_CHECK` environment variable being **present**, whatever its value
  (`NO_UPDATE_CHECK=` counts — that is how shells usually spell "off").

Either one makes `runAutomaticUpdateWithStatePathForPlatform` (cmd/termp/update.go)
return immediately. Precisely: no release lookup, no cache write, **and no state-file
mutation** — `retireStaleAutomaticUpdateAttempt` does not run. The env opt-out is tested
at this call site with `updatepkg.DisabledByEnv()` even though `Checker` already enforces
it internally; that inner gate stops the network call but not the retirement that follows
it, which is the asymmetry #463 reported. "Made no network call" and "did nothing" are
different promises, and a user who sets `NO_UPDATE_CHECK` asked for the second.

The accepted cost: while either opt-out is in force a stale automatic-update record is
not retired, so `termp status` may keep showing a failure for a target that is no longer
on offer. This was already true of `update_check = false`, it is recoverable (unset the
opt-out and the next daemon start clears it), and nothing else in the system can act on
the record while checks are off anyway.

Pinned by `TestAutomaticUpdateOptOutsLeaveStateAlone` plus its control,
`TestAutomaticUpdateRetiresSeededStateWithoutAnOptOut`
(cmd/termp/update_optout_symmetry_test.go). The control is load-bearing: retirement draws
no conclusion from an empty cache, so the pre-#463 opt-out test — which seeded only an
attempt record and no cached latest — reported "not cleared" under either behaviour and
pinned neither. The opt-out cases therefore seed a **non-empty** cache and the control
proves that state really is retirable.

**Update completion and the stale daemon (#584).** On Unix, replacing the file a
process is executing leaves that process on the old inode, so `termp update` swaps the
binary while the running daemon keeps publishing with the old code. `termp version` and
`termp status` then both report the new version and the report is wrong about the thing
actually talking to Discord. Nothing on screen said so, and a successful update printed
only `Updating termp from X to Y...` and returned, which reads like an interrupted
command.

Two surfaces carry the fix, one per path:

- **Manual (`termp update`).** `printUpdateComplete` (cmd/termp/update.go) prints
  `Updated termp from X to Y.` after each path that actually performed an update: the
  generic/Homebrew branch and the system-package branch. The paths that only print
  guidance (Scoop, and the Windows generic/archive case, which cannot replace a running
  `termp.exe` at all) deliberately do not print it, because no update happened there.
  The second line, naming the running daemon and the commands to restart it, prints only
  when a daemon is running, so a user with none is not told to restart something that is
  not there.
- **Automatic (daemon).** That path has no user attached: its stdout is a log file, and
  its notice went to `debugf` where nobody saw it. It cannot print to a person, so the
  recorded success is the carrier and `termp status` renders it.
  `automaticUpdatePendingRestart` (cmd/termp/main.go) shows the notice on the existing
  `Automatic` row when all three of these hold: a daemon is running, the last recorded
  attempt succeeded, and this binary is no longer behind the attempted target. A recorded
  **failure** still takes precedence on that row, being the more actionable of the two.
  The notice ends by itself because the restarted daemon runs the new version and its own
  startup retirement clears the record (see **Stale automatic-attempt clearing** below).

**There is no `termp restart`.** The guidance names the two real subcommands,
`termp stop` then `termp start`. `TestUpdateRestartGuidanceNamesRealCommands`
(cmd/termp/update_notice_test.go) pins that, and fails loudly if a `restart` command is
ever added so the copy gets revisited rather than silently going stale.

Restarting the daemon automatically, or prompting to, is **out of scope by owner
decision** (#584). An updater that stops and starts a daemon can orphan or double-start
it, and this repo has a settled posture against reactivating anything on the user's
behalf. Do not add it without an owner decision.

Daemon detection here reuses `statusDaemonPID`, not `knownDaemonPID`. The two agree on
the PID file, which is the primary source, and differ only in the fallback: `statusDaemonPID`
additionally requires the daemon's Discord state file to be fresh. That bound is the
right one because the message is a claim about the machine that should match what the
user reads back from `termp status` a second later, and because it errs toward silence:
a stale state file cannot make the updater assert a daemon that status calls stopped.

**Stale automatic-attempt clearing (#418, #458).** `retireStaleAutomaticUpdateAttempt`
(cmd/termp/update.go) owns the rule and runs on every daemon startup where neither
update-check opt-out is in force (see **Update-check opt-outs** above),
*before* the `auto_update` branch — a record recorded while
automatic updates were enabled and then stranded by turning them off (often *because*
they failed) is exactly the case that has to clear (#458 cause 1, gate moved in #459).
A recorded **failure** is stale when either:

- the running version already satisfies the target (`!IsNewer(current, target)`), the
  original #418 rule; or
- the release source is not offering the target
  (`!SameVersion(target, latest) && latest != ""`), added for #458 cause 2.

A recorded **success** is now reached by this pass too (#584). It is stale under the
first rule only. `clearStaleAutomaticUpdateAttempt` used to skip every success before
consulting the predicate, on the reasoning that nothing rendered one; something does
now (see **Update completion and the stale daemon** below), so the skip moved out of
the helper and into the predicate. The second rule deliberately does not apply to a
success: a successful install superseded by a newer release is still an install the
running daemon has not picked up, and clearing it would silently drop the notice.

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
`TestBuildActivityDirectoryPrivacy` guards the final outgoing `presence.Activity.State`
boundary: the directory stays absent under the default `show_directory = false`, when
the cwd is outside the effective allowlist, and under a per-tool opt-out, while an
allowlisted cwd is present as a non-vacuous control (#478). The same table also
guards `directory_basename_only` at that boundary (#484): with the cwd allowlisted,
basename-only publishes only the final segment (`📁 project`) while `basename_only =
false` publishes the fuller two-segment form (`📁 private/project`), and a per-tool
`directory_basename_only` override reasserts basename-only over a global full-path
setting — mutating `presence.DirectoryDisplay` to ignore the flag fails these cases.
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

An existing config that fails to load for any *other* reason — an invalid value
(e.g. `scan_interval = "5"` with no unit), undecodable TOML, or an unreadable
file — is not fatal for `settings` (#475). Exiting non-zero there left settings,
the only tool that can repair the file, unreachable while every load fails closed
with presence off. `settingsLoadRecovery` (`cmd/termp/main.go`) classifies the
load error: `ErrConfigBeingWritten` stays fatal (a partial read must not clobber
an in-flight whole-document write, #438), any other error is recovered. On
recovery `settings` opens the editor against the fail-closed fallback config
(safe defaults, presence off) and shows a persistent banner naming the problem;
`status` already surfaced the same failure. Saving from that recovered editor
writes a full valid document, so any other authored values in the unloadable file
are lost — the banner discloses this, because the invalid file could not be
decoded to preserve them. Duration edits inside the editor are validated at the
point of entry, so the editor cannot itself write back a value that would trip
this recovery on the next load.

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
   `runAutomaticUpdateWithStatePathForPlatform` is now gated on the update-check
   opt-outs alone (not `auto_update`); after the check it returns before any
   preflight/install when `auto_update` is off. Automatic *installing* is unchanged and
   still requires `auto_update`.

Scope of the refresh (issue #460 closed the freshness half): the daemon runs the refresh
at startup and then on a ticker for as long as it lives, so a machine whose daemon keeps
running past the cache lifetime no longer goes quiet. `runPeriodicAutomaticUpdate`
(cmd/termp/update.go) performs the startup refresh, then repeats it every
`daemonUpdateRefreshInterval` (`updatepkg.CacheLifetime / 4`, i.e. 6h) until the daemon's
context is cancelled, at which point the ticker is stopped and the goroutine returns. The
interval is deliberately *below* the cache lifetime rather than equal to it: a tick that
lands moments before the entry expires finds it still fresh, makes no lookup, and would
otherwise leave the cache stale for nearly another whole period. It is injectable so
tests drive the loop instead of sleeping.

What this promises and what it does not: while a daemon runs, the cache is refreshed
within one interval of expiring. It does not promise an alert the moment a release
publishes — `cacheLifetime` (24h) still bounds the real lookup rate, because a tick whose
cache entry is fresh short-circuits to a local file read with no network call — and it
promises nothing at all while no daemon is running, where `termp version`, `termp
status`, and `termp update` remain the refresh points.

The daemon calls `Checker.Refresh`, not `Checker.Check`: `Check`'s `sync.Once` gives a
short-lived CLI run one lookup and one stable answer, which is correct there and was
exactly what kept a long-lived daemon from ever looking again. See
[`update.md`](update.md). Failed lookups are cached like successful ones, so ticking
cannot become a retry loop on an offline machine.

Config is re-read on every refresh through `Manager.Current` rather than captured at
startup, so `update_check = false` takes effect at the next tick instead of the next
daemon restart. Both opt-outs suppress every network call, the alert itself, and the
whole automatic-update body (see **Update-check opt-outs** above), on the startup refresh
and on every tick alike. A config that cannot be read suppresses the refresh entirely
(it may hold an opt-out we cannot see — the same rule the alert paths already applied to
`loadErr`); previously the startup refresh ran anyway on the fallback config. Automatic
*installing* is unchanged and still requires `auto_update`: repetition must not turn into
a repeated unattended install for someone who declined it.

Only the refresh repeats — the install does not. `installOncePerTarget` wraps the
daemon's checker and reports "no update" for a target this process already acted on, so
a daemon with `auto_update` on does not re-run `brew upgrade` (or re-download and re-run
the generic installer) every 6h. It would, otherwise: the running process keeps reporting
its own old version until it restarts, so the same target looks new on every tick. The
dedupe is per process and per target, so a release published mid-session is still
installed and a failed install is still retried at the next daemon start, exactly as
before the ticker existed. It suppresses only the install: the cache write already
happened inside `Refresh`, and stale-record retirement still sees the version through
`Result.Latest`.

Test isolation for the update cache (found while fixing #463): `runAutomaticUpdate`
reaches the state file through `updatepkg.DefaultCachePath()` rather than an injected
path, so every test that drives it — the periodic-loop tests included — used to read and
**write** whatever cache the ambient environment pointed at. Probed: a
`~/.cache/termp/update-check.json` holding a recorded attempt for `9.9.9` came out of
`go test ./cmd/termp` with the attempt deleted. `TestMain`
(cmd/termp/main_testmain_test.go) now redirects `XDG_CACHE_HOME` (and `LOCALAPPDATA`,
which `DefaultCachePath` uses on Windows) to a temporary directory for the whole package.
Tests needing a specific location still override with `t.Setenv`, which wins.

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
The Windows no-`termpw.exe` Task Scheduler fallback now passes its own internal marker and
owns the same rotating log. Previously that action ran plain `start --foreground`, while
the log-owning gate covered only detached children and the login-service marker. The
rotating writer was therefore never opened, and every normal lifecycle line was gated by
verbose `debugf`, which explains the observed zero-byte `%LOCALAPPDATA%\termp\termp.log`.
Every daemon now records start at default verbosity with version, executable path, and
trigger, then records exit with its error, shutdown request, or completed-loop
reason. `termpw.exe` now passes the existing daemon-log marker, while the no-launcher
fallback marker also sets the Windows console title, prints the one-line
closing warning, and releases the console before daemon initialization (#545, #546).
Lifecycle records wrap the executable path in literal double quotes without Go-escaping
Windows backslashes, keeping the logged path readable while clearly delimiting its end.
Manual foreground starts do not open the file log; their lifecycle records go to the
attached terminal. Linux autostart continues to use journald.
On Windows, stderr rebinding closes the previous owning `*os.File` after replacing both
the process standard handle and `os.Stderr`, unless the old handle is invalid or identical
to the replacement. This prevents the old file finalizer from double-closing a reused
numeric handle. Windows coverage verifies the `*os.File` close state through a second
`Close` returning `os.ErrClosed`, which distinguishes an owning close from raw handle
closure underneath a still-open `*os.File` (#514).
The banner, startup error, and reload error render through `SanitizeSingleLine`, matching
every other single-line render boundary in the CLI.

**Security hardening (2026-08-15 hunt, #560-#563).** Four related fixes to daemon
signaling and the daemon log, from the same hunt cycle as the config/detector/presence
fixes in the same window:

- **#560 (PID record start time bound to the signaled object).** The last
  start-time comparison used to happen inside `pidRecordIdentityMatches`, then the
  platform signal callback (`signalTermpProcessAtPath`) received only the PID and
  executable path, so it re-validated owner and image path against a fresh snapshot but
  never against the record's recorded start time, leaving a PID-reuse race window
  between that comparison and the actual signal. `stopDaemon`/`stopDaemonAndPublisher`'s
  `signal` parameter now carries `(pid, expectedPath, expectedStartTime,
  startTimeKnown)`, and every platform binds the check to the object it actually opens:
  Linux rereads `/proc/<pid>/stat` immediately after `pidfd_open` (the pidfd itself stays
  bound to the original task even if the PID number is later reused, but the identity
  re-check still goes by PID number, so this catches a reused PID before signaling);
  Windows calls `GetProcessTimes` on the exact handle `PROCESS_TERMINATE` will use, not a
  fresh `OpenProcess`; Darwin re-snapshots via `kern.proc.pid` immediately before `kill`
  (no pidfd equivalent exists); the `!linux && !darwin && !windows` fallback shells out to
  `ps -o lstart=` the same way `processStartTime` does. A record with
  `StartTimeUnavailable` (legacy records, or a platform where the lookup is not
  available) passes `startTimeKnown = false` and the platform functions skip the check,
  preserving the existing liveness+owner+path fallback.
  The separate question of `processIdentityMatches` returning `true` when
  `lookupProcessStartTime` itself errors was investigated and left as the existing
  deliberate fallback, not tightened to fail closed: `lookupProcessStartTime` is only
  ever called after `alive(pid)` already observed the process running, so its dominant
  real failure mode is the process exiting in that gap, an ordinary race during a stop
  poll loop rather than a rare condition; failing closed there would make `termp stop`
  spuriously refuse a daemon that happened to be exiting right as the check ran. The
  residual protection when the fallback triggers is unchanged: `looksLikeTermp` still
  requires same-user ownership and a matching executable path, so a wrongly authorized
  victim is bounded to another termp process at the recorded path. This reasoning is now
  a code comment on `processIdentityMatches` itself (`cmd/termp/main.go`).
- **#561 (daemon log symlink/permission hardening).** The daemon log and the detached
  parent's panic log now open through a shared `openLogFile(path, perm)`
  (`cmd/termp/log_rotation.go`, `log_rotation_unix.go`, `log_rotation_windows.go`) instead
  of a raw `os.OpenFile`. Unix opens with `syscall.O_NOFOLLOW` so a symlink planted at the
  log path is refused outright rather than transparently written through, then verifies
  the opened file is a regular file owned by the current user (reusing
  `requireCurrentUserOwner` from `pidfile_unix.go`) and `Chmod`s it to 0600 regardless of
  whatever mode it already had, since the creation-mode argument to `OpenFile` only
  applies when the file is newly created. Windows opens with
  `FILE_FLAG_OPEN_REPARSE_POINT` (mirroring the PID file's `openWindowsPIDFile`) so a
  reparse point is opened as itself rather than followed, and `FILE_APPEND_DATA` for the
  same atomic append-at-EOF semantics `os.O_APPEND` gives on the other platforms; it then
  rejects a reparse point and checks SID ownership by reusing the PID file's
  `pidFileAttributesSafe`/`windowsHandleOwnerSID`/`currentTokenOwnerSIDs` helpers, since
  Windows has no POSIX mode bits to tighten. `newRotatingLogWriter` also `Chmod`s the log
  directory to 0700 after `MkdirAll`, since `MkdirAll` no-ops (and does not tighten the
  mode) on an already-existing directory.
- **#562 (full uninstall no longer recursively deletes unknown files).** Full
  uninstall's directory targets (config, state, presence-state, update-cache) are termp's
  own namespace directories, but `removeUninstallTarget` used to hand each one whole to
  `os.RemoveAll`, deleting anything the user had placed there too (a hand-kept note, a
  backup config copy) while the completion message claimed only termp-created data was
  removed. `removeKnownFilesFromDirectory` (`cmd/termp/uninstall.go`) now removes only the
  basenames `isKnownUninstallFile` recognizes (`config.toml`, `usage.json`,
  `presence.json`, `update-check.json`, the daemon log and its lock/rotated generations
  (included because on Linux the update-cache directory and the detached log directory are
  the same XDG cache directory), plus each name's `.tmp-<random>` atomic-write prefix) and
  removes the directory itself only once nothing else remains in it. A directory holding
  only recognized files still ends up fully gone, preserving the "everything termp
  created is gone" promise; anything else, and therefore the directory around it,
  survives.
- **#563 (single oversized log record bounded).** `rotatingLogWriter.Write` used to
  rotate only when the *existing* file was nonempty and the new record would push it over
  `maxBytes`, so one write larger than the cap went straight into an empty file whole,
  and the same record then filled the fresh file rotation just created, unboundedly. A
  new `boundLogRecord(line, maxBytes)` truncates any single record to at most `maxBytes`
  bytes (preserving a trailing newline if the original had one) before the existing
  rotate-then-write logic runs, so no single write can defeat the cap regardless of
  whether it lands in an empty or freshly rotated file.

**Depends on / used by:** Composes every `internal/*` package and is the application
entry point. Release automation depends on GitHub Actions and GoReleaser.

**Open questions / TODO:** Implement and live-verify non-Windows daemon control transport.
