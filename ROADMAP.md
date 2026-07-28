# Roadmap

**Terminal Presence v0.1.0 was released on July 27, 2026.** See the
[release](https://github.com/polter-dev/discord_terminal_presence/releases/tag/v0.1.0).
The `termp` command is available through the shell installer, Homebrew Cask, `.deb`/`.rpm`
packages, and Windows release archives. The first-release tracking issue
[#9](https://github.com/polter-dev/discord_terminal_presence/issues/9) is closed.

## Shipped foundation

- [x] **M0 — Project and release harness:** repository, review workflow, Discord
  application/assets, cross-platform CI, and release automation.
- [x] **M1 — Detector and registry:** process-based detection, built-in and custom tools,
  active-tool selection, terminal activity rules, and privacy-aware directory resolution.
- [x] **M2 — Presence writer:** Discord IPC on macOS, Linux, and Windows; activity mapping,
  elapsed sessions, reconnects, throttling, and stale-presence clearing.
- [x] **M3 — Config and privacy:** validated TOML with hot reload, per-tool overrides,
  custom tools, and directory display off by default. Config lives at
  `~/.config/termp/config.toml` or `$XDG_CONFIG_HOME/termp/config.toml`; Windows normally
  uses `%AppData%\termp\config.toml`.
- [x] **M4 — Assets and buttons:** built-in/custom images, optional small image, collection
  text, and up to two validated link buttons.

## Next release

- [ ] Fix package/update ownership detection
  ([#364](https://github.com/polter-dev/discord_terminal_presence/issues/364)): `.deb` and
  `.rpm` installs currently look generic, so `termp update` can install a second binary
  under `/usr/local/bin` that shadows the package-owned copy. Also fix custom `BINDIR`
  drift and the overly long stale-lock window.
- [ ] Make warning-log sanitization structural
  ([#363](https://github.com/polter-dev/discord_terminal_presence/issues/363)): sanitize
  `cfg.Warnings` at the two remaining `log.Print` sites and remove the unused watch
  constructor. Current warning sources are escaped, so this is defense in depth rather
  than a known exploit.
- [ ] Make removal honest and complete
  ([#368](https://github.com/polter-dev/discord_terminal_presence/issues/368)): correct
  the installer's misleading `termp uninstall` label and add a confirmed, install-aware
  `--all` path for full removal.
- [ ] Make the required `termp setup` next step noticeable in Homebrew and `.deb`/`.rpm`
  install output
  ([#369](https://github.com/polter-dev/discord_terminal_presence/issues/369)).

## Windows gaps

v0.1.0 supports Windows detection, Discord IPC, lifecycle commands, and autostart, but
Windows has **no one-line install command** and **`termp update` is not supported**.
Users must install and upgrade by downloading a release archive manually.

- [ ] Add Scoop as the first one-line install and managed-update path; consider WinGet
  later ([#370](https://github.com/polter-dev/discord_terminal_presence/issues/370)).
- [ ] Complete release-binary testing on real Windows hardware for autostart, shutdown,
  restart, and general smoke behavior
  ([#275](https://github.com/polter-dev/discord_terminal_presence/issues/275)).
- [ ] Fix autostart ownership across Windows path aliases and replace English-only
  `schtasks` output parsing, then verify on real hardware and a non-English locale
  ([#304](https://github.com/polter-dev/discord_terminal_presence/issues/304)).

## Later

- [ ] **M5 — Optional hooks:** decide whether to build or formally cut the opt-in hooks
  layer for richer intra-tool detail. The shipped process-scan behavior remains the
  complete default without hooks
  ([#69](https://github.com/polter-dev/discord_terminal_presence/issues/69), labelled
  `later`).
