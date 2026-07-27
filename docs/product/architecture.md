# Architecture

Terminal Presence is written in Go and distributed as the single `termp` binary. Its
main process-inspection dependency is `gopsutil`; Discord IPC framing and transport are
implemented directly in `internal/presence`.

## Data flow

```text
OS process list
      |
      v
 detector <----- registry <----- config and custom tools
      |
      | Detection (featured tool, cwd, episode start, other tools)
      v
 CLI activity mapper <----- display and privacy config
      |
      v
 presence writer ---- Discord IPC ----> local Discord desktop app
```

The daemon also records bounded local usage data, publishes its connection state for
`termp status`, and watches the config for validated hot reloads.

## Components and contracts

Package and application boundaries match the entries in `docs/context/`.

### `registry`

- Owns the embedded built-in tool catalog and custom tool definitions.
- Matches process identity fields, including recognized runtime entrypoints, while
  avoiding arbitrary argument matching.
- Resolves an uploaded image key or an external image URL and selects the
  highest-priority matching tool.

### `detector`

- Polls process identities at `scan_interval` and enriches only matching processes.
- Applies platform-specific terminal-presence and idle rules.
- Selects one featured tool with pinning and activity-aware hysteresis, retains other
  present tools as a collection, and debounces changes.
- Persists episode anchors so elapsed timers survive daemon restarts and valid config
  reloads.

### `presence`

- Maps validated activities to Discord's IPC protocol and owns the socket or named-pipe
  connection on one writer goroutine.
- Throttles and coalesces writes, reconnects with backoff, clears stale activity, and
  validates Discord-facing text, images, and buttons.
- Uses Unix-domain sockets on macOS/Linux and named pipes on Windows.

### `config`

- Loads and hot-reloads TOML while preserving last-good runtime behavior after an
  invalid edit.
- Owns display, privacy, tool override, update, CTA, and detector-selection settings.
- Uses `~/.config/termp/config.toml` on macOS/Linux, honoring
  `$XDG_CONFIG_HOME/termp/config.toml`. Windows uses the native user config directory
  (normally `%AppData%\termp\config.toml`) and migrates the legacy Unix-style path when
  possible.

### `cli` and daemon

- The `termp` command owns daemon lifecycle, status, setup, settings/watch TUIs,
  autostart, completion, config initialization, version, and updates.
- `termp start`, `termp stop`, and `termp status` are the primary lifecycle commands.
- Platform service definitions live in `internal/service`; completion installation,
  update state, TUI rendering, and usage history each have their own modules.

## Concurrency model

The detector runs its scan loop and emits detections on a channel. A mapping goroutine
combines those detections with validated config changes and emits activities. The
presence writer consumes those activities and is the sole owner of the Discord IPC
client. Config changes that affect detection trigger an immediate rescan; display-only
changes re-render the last detection.

## Platform notes

- macOS and Linux discover and validate Discord Unix-domain sockets across standard
  runtime and packaged-app locations.
- Windows Discord presence is implemented through
  `\\.\pipe\discord-ipc-0` through `\\.\pipe\discord-ipc-9`, with peer validation.
- The Discord Application ID is public and safe to embed. No bot token is used.
