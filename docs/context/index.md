# Context ledger index

Start here, then read the module entry before opening source.

| Module | Context | Source | Purpose |
| --- | --- | --- | --- |
| CLI | [`cli.md`](cli.md) | `cmd/termp`, `install.sh`, `.github/workflows/release.yml`, `.goreleaser.yaml` | Owns commands, daemon lifecycle and status, setup wiring, updates, and the gated release path. |
| Completion install | [`completioninstall.md`](completioninstall.md) | `internal/completioninstall` | Installs and removes generated completions for bash, zsh, and fish. |
| Config | [`config.md`](config.md) | `internal/config` | Defines, validates (including Discord field bounds), migrates, watches, initializes, loads, and saves user configuration. |
| Detector | [`detector.md`](detector.md) | `internal/detector` | Scans processes, applies terminal-activity rules, and selects the featured and other present tools. |
| Presence | [`presence.md`](presence.md) | `internal/presence` | Maps detections to bounded Discord activity and classifies payload versus transport IPC failures. |
| Registry | [`registry.md`](registry.md) | `internal/registry` | Defines and validates built-in/custom tools, resolves logos, and matches process identity to tools. |
| Service | [`service.md`](service.md) | `internal/service` | Manages per-OS login services and context-bounded status queries. |
| Terminal text | [`terminaltext.md`](terminaltext.md) | `internal/terminaltext` | Sanitizes externally derived text at terminal and log rendering boundaries. |
| TUI | [`tui.md`](tui.md) | `internal/tui` | Owns setup, settings, confirmation, watch, and card UI with shared safe rendering. |
| Update | [`update.md`](update.md) | `internal/update` | Checks releases, persists check/install outcomes, detects install ownership, and runs the matching updater. |
| Usage | [`usage.md`](usage.md) | `internal/usage` | Stores bounded local tool-usage history for settings ranking. |

Platform-specific test contracts live with their module entries: launchd/systemd and
Linux mount semantics run only on their native OS, while the Windows tty-presence gap
remains explicitly skipped under #183.
