# CLI (package `cmd/termp`)

**Purpose:** Owns command dispatch, daemon lifecycle, status/control output, setup
wiring, update policy, installation guidance, and TUI startup.

**Public surface:** The `termp` binary exposes start/stop/status, autostart,
settings/watch/setup, config, completion, version, update, and full-uninstall commands.
Windows also exposes `connect`. `install.sh` is the canonical generic release installer.

**Key files:** `cmd/termp/main.go` owns dispatch, daemon operation, status, setup, and
usage/config wiring. `cmd/termp/connect.go` and `control_*` own daemon control.
`cmd/termp/update.go` owns manual notices and opt-in automatic updates. `spawn_*`,
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
concurrent PID/publisher faults.

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
It never deletes the running executable. It reuses install ownership detection and the
resolved generic install directory to print exact Homebrew, Scoop, apt, detected RPM
front-end (`dnf`/`zypper`/`yum`, or `rpm -e`), Go, generic
Unix, or Windows binary-removal guidance. Destructive tests inject paths beneath an
asserted temporary home.

Automatic updates are fail-open, asynchronous, and non-interactive. Unix generic
installs preflight the resolved running executable's directory and record a skipped
reason when it is not writable. Generic Windows installs record an unsupported-platform
skip; Go and Homebrew installs remain eligible. Scoop- and Debian/RPM-owned installs
record a managed-package skip without invoking an updater. Automatic Unix commands use
a separate process group so timeout cancellation terminates their full process tree.
Interactive `termp update` commands stay in the foreground process group, allowing
`sudo` in both generic and deb/rpm updates to read from the controlling terminal.
Attempts are visible in
`termp status`, and a later success clears the reported failure/skip. Interactive
`termp update` on a Scoop install prints the available-version header and
`To update: scoop update termp` guidance without a system-package preamble, an
`Updating...` line, stderr output, or an installer command. For a known Debian/RPM-owned
install it downloads the exact-tag, architecture-specific release package and
`checksums.txt` into private temporary files, verifies SHA-256 fail-closed,
and then runs `sudo apt install -y <file>` or the detected RPM front-end command
(`dnf`/`zypper`/`yum`/`rpm`, probed in that order). It never uses
the generic installer or writes to `/usr/local/bin`. When sudo, the package manager, or a
TTY is unavailable, package ownership is ambiguous, or download/checksum/install fails,
the command reports the reason and prints the existing exact-tag GitHub release download
and local apt or detected-RPM-front-end instructions. Automatic/background updates never enter this path and
continue to record a managed-package skip with zero commands. A failed executable update
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

Homebrew updates run `brew upgrade polter-dev/tap/termp` without `--cask`. Homebrew
resolves the fully qualified token to the GoReleaser-published Cask; the orphaned
hand-written Formula draft was removed under #303.
Homebrew Cask installs and deb/rpm postinstall scripts print the same prominent,
fixed-width 80-column ASCII setup box. Homebrew prints Cask caveats unindented at
column 0, so the box renders intact. GoReleaser's caveats templating trims the
authored padding blank lines, so deb/rpm is the only channel whose padding comes
from this repo; Homebrew renders the box directly under its own `==> Caveats`
header, followed by a single trailing blank line that Homebrew itself emits. The
package script always exits successfully so guidance cannot break an install.

CI snapshot-builds and installs the real deb and rpm artifacts in digest-pinned
`debian:stable` and `fedora:latest` containers. Before the detection probe runs, the job
plants a disabled stub for the *other* package tool (`rpm` on the debian job,
`dpkg-query` on the fedora job) into `/usr/sbin`, never `/usr/local/bin`. Without that
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

Structured CLI output, verbose log messages, and daemon/watch config warnings sanitize
externally derived terminal text. Detection logs resolve the working directory through
the same effective privacy policy and path-reduction helper as presence output, cap
expanded paths at their final two components, and report `hidden` when directory display
is not allowed. Labelled status values and single-line log records replace embedded line
breaks with a visible ` ; ` separator before sanitizing, preserving multi-step update
commands without breaking status-column alignment, gluing tokens, or permitting log-line
injection.
Per-tool enablement is passed into detector selection, and hot reloads that change it
trigger an immediate rescan; the global `enabled` switch still clears mapped presence.
Live watch loads the daemon's episode anchors for matching elapsed-time display but runs
the detector read-only, leaving `presence.json` persistence exclusively to the daemon.
It shares the daemon's config-change transaction and hot-reconfigures detector settings;
display-only changes re-render the last detection without forcing another scan.
When initial config loading fails, `watch --once` warns on stderr that built-in defaults
are active and points to `termp status`; interactive watch shows the same warning inside
the alternate-screen view so it remains visible without corrupting the terminal.

**Depends on / used by:** Composes every `internal/*` package and is the application
entry point. Release automation depends on GitHub Actions and GoReleaser.

**Open questions / TODO:** Implement and live-verify non-Windows daemon control transport.
