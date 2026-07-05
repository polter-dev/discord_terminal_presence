# termp

> Working name — final name TBD.

**termp** detects which CLI you're running in your terminal —
[Claude Code](https://claude.com/claude-code) (`claude`), gemini-cli (`gemini`),
Codex CLI (`codex`), and any other interesting TUI — and shows it as a **Discord Rich
Presence** activity: a per-tool logo, an optional working directory, an elapsed timer,
and link buttons. Think "Discord shows what game you're playing," but for your terminal.

Everything is user-toggleable — global on/off, per-field, and per-tool — with
**privacy-first defaults** (your working directory is hidden unless you opt in).

## Status

🚧 **Early development.** Building toward a first release. Detection + Discord presence
are the core; see [Roadmap](#roadmap) below.

## How it works

```
terminal (claude / gemini / codex / …)
        │  process scan
        ▼
   detector ── active tool ──▶ presence writer
        │                              │  Discord IPC socket
        ▼                              ▼
   tool registry                  Discord desktop app  ──▶  your profile
   (built-in + custom, config-driven)
```

A small Go daemon polls the OS process list, matches running processes against a
configurable **tool registry**, and pushes a Rich Presence payload to the local
Discord IPC socket. Built-in tools ship with logos; you can add any custom tool by
pointing at an image URL in your config — no code changes and no Discord app of your own.

## Quickstart (planned)

```sh
termp start      # run the background daemon
termp status     # show what's currently detected / displayed
termp stop       # stop the daemon
```

## Autostart

Use `termp install` to start the daemon at login and let the OS restart it if it
dies. Use `termp uninstall` to remove the login service.

## Configuration

Config lives at `~/.config/termp/config.toml` (respects `$XDG_CONFIG_HOME`) and is
**hot-reloaded** — edits apply without restarting the daemon.

```toml
enabled       = true        # master switch
scan_interval = "3s"        # process-scan cadence

[display]
tool_name     = true        # show the tool's display name
elapsed_timer = true        # show the session "elapsed" timer
small_image   = true        # optional corner icon
buttons       = true        # link buttons

[privacy]
show_directory = false      # OFF by default — opt in to show your cwd
directory_allowlist = ["~/dev"]   # if shown, restrict to these paths
directory_basename_only = true    # show "myrepo", not the full path

# Per-tool overrides
[tools.claude-code]
show_directory = true

# Add any tool with a custom logo — no Discord app needed
[[custom_tools]]
id           = "lazygit"
display_name = "lazygit"
match        = { name = "lazygit" }
image_url    = "https://example.com/lazygit.png"
```

## Privacy

- Nothing is tracked or sent anywhere except the Discord Rich Presence payload to your
  own local Discord client.
- Your working directory is **never shown unless you explicitly opt in**, and defaults to
  basename-only even then.

## Design decisions

| Area | Decision |
|---|---|
| Language | Go (single cross-compiled binary) |
| Detection | Process scanning core; optional per-tool hooks later |
| Discord | One shared app + external image URLs for custom tools |
| Privacy | Directory display **off** by default |

## Roadmap

- **Detector + registry** — process scan loop; built-in + custom tool matching; active-tool
  selection; working-directory resolution.
- **Presence writer** — Discord IPC connection; detection → activity payload; reconnect
  handling; session timer.
- **Config + privacy** — TOML load + hot reload; global/per-field/per-tool toggles;
  directory privacy; `start`/`stop`/`status` CLI.
- **Assets + buttons** — built-in logos; optional small image; up to two link buttons.
- **Hooks enhancement** — optional richer intra-tool detail for tools that expose hooks.

Platforms: macOS and Linux first; Windows (named-pipe IPC) later.

## License

TBD.
