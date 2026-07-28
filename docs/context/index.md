# Context ledger index

Start here, then read the module entry before opening source.

| Module | Context | Source | Purpose |
| --- | --- | --- | --- |
| CLI | [`cli.md`](cli.md) | `cmd/termp`, `install.sh`, `.github/workflows/ci.yml`, `.github/workflows/release.yml`, `.github/workflows/verify-release-secrets.yml`, `.goreleaser.yaml` | Owns commands, daemon lifecycle and status, real-TTY interaction gates, fail-closed invalid-config startup surfacing, setup wiring, bounded-network updates, full uninstall, deb/rpm/openSUSE package integration coverage, and gated Homebrew/Scoop publication. |
| Completion install | [`completioninstall.md`](completioninstall.md) | `internal/completioninstall` | Installs and removes generated completions for bash, zsh, and fish. |
| Config | [`config.md`](config.md) | `internal/config` | Defines, validates (including Discord field bounds), migrates, watches, initializes, fail-closes invalid existing files, loads, and saves user configuration. |
| Detector | [`detector.md`](detector.md) | `internal/detector` | Scans processes, applies terminal-activity rules, and selects the featured and other present tools. |
| Presence | [`presence.md`](presence.md) | `internal/presence` | Maps detections to defensively bounded activity, reports optional-field omissions for caller-gated diagnostics, and classifies payload versus transport IPC failures. |
| Registry | [`registry.md`](registry.md) | `internal/registry` | Defines built-in/custom tools, validates Discord-facing custom fields, resolves logos, and matches process identity to tools. |
| Service | [`service.md`](service.md) | `internal/service` | Manages per-OS login services, canonical Windows task ownership, and bounded locale-independent status queries. |
| Terminal text | [`terminaltext.md`](terminaltext.md) | `internal/terminaltext` | Sanitizes externally derived text and safely flattens multi-line values at single-line terminal and log rendering boundaries. |
| TUI | [`tui.md`](tui.md) | `internal/tui` | Owns setup, settings, confirmation, watch, and card UI with shared safe rendering. |
| Update | [`update.md`](update.md) | `internal/update` | Checks releases, persists outcomes, detects generic/Homebrew/Scoop/Go/system-package ownership, and selects safe, real-TTY-gated, network-bounded update guidance or execution. |
| Usage | [`usage.md`](usage.md) | `internal/usage` | Stores bounded local tool-usage history for settings ranking. |

Platform-specific test contracts live with their module entries: launchd/systemd and
Linux mount semantics run only on their native OS. Windows terminal presence is covered
by dedicated Windows tests; five Unix-specific TTY-atime/tmux fixture tests remain
skipped on Windows.

Package-manager setup guidance and its non-TTY rendering safety contract live in the
CLI module entry.
