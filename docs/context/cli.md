# CLI (package `cmd/termp`)

**Purpose:** Owns command dispatch, daemon lifecycle, status/control output, setup
wiring, update policy, installation guidance, and TUI startup.

**Public surface:** The `termp` binary exposes start/stop/status, autostart,
settings/watch/setup, config, completion, version, and update commands. Windows also
exposes `connect`. `install.sh` is the canonical generic release installer.

**Key files:** `cmd/termp/main.go` owns dispatch, daemon operation, status, setup, and
usage/config wiring. `cmd/termp/connect.go` and `control_*` own daemon control.
`cmd/termp/update.go` owns manual notices and opt-in automatic updates. `spawn_*`,
`pidfile_*`, and `shutdown_*` contain platform lifecycle behavior. `install.sh` installs
tag-pinned archives. `.github/workflows/release.yml` and `.goreleaser.yaml` own releases;
`.github/workflows/verify-release-secrets.yml` provides the manual publishing-token
pre-flight check.

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

Automatic updates are fail-open, asynchronous, and non-interactive. Unix generic
installs preflight `BINDIR` (default `/usr/local/bin`) and record a skipped reason when it
is not writable. Generic Windows installs record an unsupported-platform skip; Go and
Homebrew installs remain eligible. Attempts are visible in `termp status`, and a later
success clears the reported failure/skip. A failed interactive `termp update` prints the
exact retry command built for its detected Homebrew, Go, or generic install method.

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

The installer resolves one tag for archive/checksum, prefers
`https://termp.polter.sh/dl/curl/{os}/{arch}/{tag}`, and falls back to the tag-pinned
GitHub asset. Every fatal installer path prints the advertised fetch-and-pipe retry
command, with explicitly supplied `VERSION`, `BINDIR`, and `TERMP_DOWNLOAD_CHANNEL`
preserved immediately before `sh`; atomic staging prevents failed installs from leaving
a partial destination binary.
Tag runs create a draft release (`release.draft: true`) and attach the generated Cask
without writing the tap. Only `release.published` triggers a second job that verifies
the release is public, downloads that exact Cask, and updates the tap.
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
is not allowed.
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
