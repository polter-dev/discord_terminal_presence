# CLI (package `cmd/termp`)

**Purpose:** Owns command dispatch, daemon lifecycle, status/control output, setup
wiring, update policy, installation guidance, and TUI startup.

**Public surface:** The `termp` binary exposes start/stop/connect/status, autostart,
settings/watch/setup, config, completion, version, and update commands. `install.sh` is
the canonical generic release installer.

**Key files:** `cmd/termp/main.go` owns dispatch, daemon operation, status, setup, and
usage/config wiring. `cmd/termp/connect.go` and `control_*` own daemon control.
`cmd/termp/update.go` owns manual notices and opt-in automatic updates. `spawn_*`,
`pidfile_*`, and `shutdown_*` contain platform lifecycle behavior. `install.sh` installs
tag-pinned archives. `.github/workflows/release.yml` and `.goreleaser.yaml` own releases.

**Invariants / gotchas:** Start treats the validated PID file as final arbiter, also
recognizes a fresh same-user/same-executable `discord.json` publisher, and waits for
bounded child readiness. Stop targets both a valid PID owner and a distinct valid
publisher. Windows publishes readiness only after shutdown primitives exist. Autostart
disable/uninstall stop the tracked daemon and report partial failure if it survives.

Connect never starts a daemon. It prefers the validated publisher, then the PID owner.
Windows uses a current-user PID-addressed named pipe and verifies the server process.
`--force` reconnects; ordinary already-connected calls are successful no-ops. Response
and readiness share a bound. Non-Windows transport remains explicitly unsupported.
Status trusts a fresh connected publisher instead of probing Discord and exposes
concurrent PID/publisher faults.

Plain legacy PID records remain readable for stale-file cleanup, but a record without
a process start time never authorizes signaling. Shutdown polling follows the recorded
process identity, so an exited daemon whose PID is reused is treated as successfully
stopped.

Setup rewrites enabled service definitions but does not relaunch an already-running
daemon. Config/autostart success survives a completion-only failure and the summary
reports that partial outcome. Completion removal attempts every shell; details live in
[`completioninstall.md`](completioninstall.md).

Automatic updates are fail-open, asynchronous, and non-interactive. Unix generic
installs preflight `BINDIR` (default `/usr/local/bin`) and record a skipped reason when it
is not writable. Generic Windows installs record an unsupported-platform skip; Go and
Homebrew installs remain eligible. Attempts are visible in `termp status`, and a later
success clears the reported failure/skip. Interactive `termp update` is unchanged and
may still use sudo/manual guidance.

Homebrew automation currently runs `brew upgrade polter-dev/tap/termp` without `--cask`,
but GoReleaser publishes a Cask. Issue #303 records this unresolved contradiction pending
an empirical test release; do not change either stance without the owner decision.

The installer resolves one tag for archive/checksum, prefers
`https://termp.polter.sh/dl/curl/{os}/{arch}/{tag}`, and falls back to the tag-pinned
GitHub asset. Tag runs create a draft release (`release.draft: true`) and attach the
generated Cask without writing the tap. Only `release.published` triggers a second job
that verifies the release is public, downloads that exact Cask, and updates the tap.

Config initialization safety is documented in [`config.md`](config.md), terminal
rendering in [`tui.md`](tui.md), update cache/detection in [`update.md`](update.md), and
usage retention in [`usage.md`](usage.md).

Structured CLI output and verbose log messages sanitize externally derived terminal
text. Detection logs resolve the working directory through the same effective privacy
policy as presence output and report `hidden` when directory display is not allowed.

**Depends on / used by:** Composes every `internal/*` package and is the application
entry point. Release automation depends on GitHub Actions and GoReleaser.

**Open questions / TODO:** Resolve the Homebrew Formula/Cask contract in #303. Implement
and live-verify non-Windows daemon control transport.
