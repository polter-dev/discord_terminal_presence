# Context ledger index

Start here, then read the module entry before opening source.

| Module | Context | Source | Purpose |
| --- | --- | --- | --- |
| CLI | [`cli.md`](cli.md) | `cmd/termp`, `install.sh`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.github/workflows/verify-release-secrets.yml`, `.goreleaser.yaml` | Owns commands, daemon lifecycle and status, real-TTY interaction gates, invalid-config startup/reload surfacing with a self-healing config watcher, publication-rejection reporting, bounded detached and macOS-autostart file logs (Linux autostart uses journald), setup wiring, bounded-network updates with a stale-failure clearing rule, full uninstall, static analysis, deb/rpm/dnf/openSUSE package integration coverage, and gated Homebrew/Scoop publication. |
| Completion install | [`completioninstall.md`](completioninstall.md) | `internal/completioninstall` | Installs and removes generated completions for bash, zsh, and fish. |
| Config | [`config.md`](config.md) | `internal/config` | Defines, validates (including Discord field bounds), migrates, watches with ordered reload results plus partial-snapshot protection and a sampled-reload `enabled`-specific loosening guard for non-atomic writes (reload only; see the module doc for known gaps), initializes, fail-closes invalid existing files, loads, and saves user configuration. |
| Detector | [`detector.md`](detector.md) | `internal/detector` | Scans processes, applies terminal-activity rules, and selects the featured and other present tools. |
| Presence | [`presence.md`](presence.md) | `internal/presence` | Maps detections to defensively bounded activity, sanitizes Discord-facing text at the wire payload boundary, reports optional-field omissions for caller-gated diagnostics, classifies payload versus transport IPC failures, and reports connection health and publication (last-publish) health through separate hooks. |
| Registry | [`registry.md`](registry.md) | `internal/registry` | Defines built-in/custom tools, validates Discord-facing custom fields (including rejecting control characters, named by codepoint, in display names, image keys, and button labels), resolves logos, and matches process identity to tools. |
| Service | [`service.md`](service.md) | `internal/service` | Manages per-OS login services, canonical Windows task ownership, and bounded locale-independent status queries. |
| Terminal text | [`terminaltext.md`](terminaltext.md) | `internal/terminaltext` | Conservatively sanitizes externally derived text with bounded handling of unterminated sequences and safely flattens multi-line values. |
| TUI | [`tui.md`](tui.md) | `internal/tui` | Owns setup, settings, confirmation, watch, and card UI with shared safe rendering. |
| Update | [`update.md`](update.md) | `internal/update` | Checks releases, persists and retires (once no longer newer) automatic-update outcomes, detects generic/Homebrew/Scoop/Go/system-package ownership, and selects safe, real-TTY-gated, network-bounded update guidance or execution. |
| Usage | [`usage.md`](usage.md) | `internal/usage` | Stores bounded local tool-usage history for settings ranking. |

Platform-specific test contracts live with their module entries: launchd/systemd and
Linux mount semantics run only on their native OS. Windows terminal presence is covered
by dedicated Windows tests; five Unix-specific TTY-atime/tmux fixture tests remain
skipped on Windows.

Package-manager setup guidance, including Homebrew's pre-install caveat ordering and
the non-TTY rendering safety contract, lives in the CLI module entry.
