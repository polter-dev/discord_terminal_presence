# Context ledger — index

Entry point for the per-module context ledger. Agents start **here** before reading
source. Convention: [`../process/context-ledger.md`](../process/context-ledger.md).

## Modules

| Module | Context doc | Source path(s) | Purpose (one line) |
|---|---|---|---|
| registry | `registry.md` | `internal/registry/` | Known-tool registry: match processes (name/argv0/exe/regex) → tools |
| detector | `detector.md` | `internal/detector/` | gopsutil scan loop; activity-aware featured tool + ordered others; debounced Detection channel |
| presence | `presence.md` | `internal/presence/` | Discord IPC (rich-go); collection Detection/Activity → throttled presence |
| config   | `config.md`   | `internal/config/`   | TOML load + fsnotify hot reload; toggles, headliner knobs, per-tool overrides, privacy |
| usage    | `usage.md`    | `internal/usage/`    | Local JSON per-tool usage counts for settings pin ranking |
| cli      | `cli.md`      | `cmd/termp/`         | `start/stop/status/install/uninstall/settings/watch`; daemon lifecycle; wires config→detector→usage→presence |
| tui      | `tui.md`      | `internal/tui/`      | Bubble Tea setup/settings/watch models plus shared Discord-card terminal renderer |
| service  | `service.md`  | `internal/service/`  | macOS LaunchAgent, Linux systemd user service, and Windows scheduled task lifecycle for autostart |

## Status

- **M1** (registry, detector) — done.
- **M2** (presence) — done; live-verified against the real Discord IPC socket.
- **M3** (config, cli) — done; daemon wired end-to-end (start→detect→present→stop).
- **Headliner/collection** (issue #6) — merged: activity-aware featured selection, sticky
  hysteresis + pin + idle-switch, collection rendering (small badge + "also:" line), write
  throttle.
- **Autostart** (issue #7) — merged: `install`/`uninstall` (launchd/systemd), `status`
  reports service state.
- **Settings TUI** (issue #8) — in review: local usage tracking plus Bubble Tea
  `termp settings` config editor with usage-ranked pin picker.
- **CTA button prototype** (issue #10) — in review: config-driven presence button defaults
  on and links to the live termp landing page at `https://termp.polter.sh/`.
- **Polish 2** — in review: opt-in idle/AFK clear via `idle_clear_timeout` and
  configurable details text via `details_format`.
- **Watch TUI** (issue #22) — in review: `termp watch`/bare TTY invocation renders a
  passive live terminal mock of the config-resolved Discord card; `watch --once` prints a
  single snapshot for scripts.
- **Autostart pause/resume** (issue #24) — in review: `disable`/`enable` pause or resume
  login-service autostart, `stop` warns when KeepAlive/Restart may relaunch the daemon,
  Windows disabled scheduled tasks are reported as not relaunching, and macOS repeated
  disable/enable calls are idempotent for launchctl already-unloaded/already-loaded states.
- Public Discord Application ID `1523168764793847918` is embedded as
  `presence.DefaultAppID`. Built-in logos can be URL-based (claude-code seeded).

## Remaining (open issues)

- **#3 name**, **#4 LICENSE**, **#5 metrics** — owner decisions.
- **#9** Homebrew · **#11** landing page · **#12** README/demo GIF.
- **#2 (M4 assets)** — upload built-in logos as Discord art assets (optional now that
  URL logos work). **M5** — optional Claude Code hook layer for richer detail.
