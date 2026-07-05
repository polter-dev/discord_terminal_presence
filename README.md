# termp

**termp** detects which CLI you're running in your terminal - Claude Code,
gemini-cli, Codex CLI, plus 48 built-in terminal tools - and shows it as a
Discord Rich Presence activity with a per-tool logo, elapsed timer, optional
working directory, optional small image, multi-tool context, and buttons.

Think "Discord shows what game you're playing," but for your terminal.

## Demo

<!-- Demo GIF placeholder: the project owner will add a recorded demo here. -->

## How It Works

```text
terminal processes
        |
        | process scan
        v
   detector  ---- active tool + cwd + other tools ----> presence writer
        |                                                |
        | uses                                           | Discord IPC
        v                                                v
   tool registry <---- config.toml ---- hot reload ---- Discord desktop app
   (built-in + custom)                                  |
                                                        v
                                                   your profile
```

`termp start` runs a small Go daemon. It scans the local process list, matches
running processes against a config-driven registry, selects one featured
"headliner" tool, builds a Discord activity payload, and sends it to the local
Discord desktop client over Discord IPC.

No bot token is required. The embedded Discord Application ID is public and is
not a secret.

## Install

```sh
go install github.com/polter-dev/discord_terminal_presence/cmd/termp@latest
```

This installs the `termp` binary into your Go bin directory. Prebuilt binaries
and Homebrew packaging are planned, but are not available yet.

## Usage

```sh
termp start
```

| Command | What it does |
|---|---|
| `termp start` | Runs the daemon in the foreground until interrupted. It writes a PID file, scans processes, hot-reloads config, and updates Discord presence. |
| `termp stop` | Sends `SIGTERM` to the running daemon recorded in the PID file and removes the PID file. |
| `termp status` | Prints daemon status, Discord IPC reachability, autostart service status, config path/load status, warnings, and the currently detected tool. |
| `termp install` | Installs and starts autostart for login. Uses a macOS LaunchAgent or Linux systemd user service. |
| `termp uninstall` | Removes the autostart service. |
| `termp settings` | Opens the interactive terminal settings TUI. Requires a TTY. |
| `termp version` | Prints version, commit, build date, Go version, OS, and architecture. |

Global flags:

| Flag | What it does |
|---|---|
| `--verbose`, `-v` | Enables verbose logging. Use it before the subcommand, for example `termp --verbose start`. Most subcommands also accept it after the command. |
| `--version` | Prints version information and exits. |

## Autostart

```sh
termp install
termp uninstall
```

On macOS, `termp install` writes a LaunchAgent at:

```text
~/Library/LaunchAgents/dev.termp.daemon.plist
```

The LaunchAgent runs `termp start`, restarts it on failure, and logs to:

```text
~/Library/Logs/termp.log
```

On Linux, `termp install` writes a systemd user unit at:

```text
~/.config/systemd/user/termp.service
```

It then runs `systemctl --user daemon-reload` and
`systemctl --user enable --now termp.service`.

Autostart is currently supported on macOS and Linux.

## Configuration

Config is TOML at:

```text
~/.config/termp/config.toml
```

If `XDG_CONFIG_HOME` is set, the path is:

```text
$XDG_CONFIG_HOME/termp/config.toml
```

The daemon creates the config directory when it starts and hot-reloads the file.
Malformed reloads keep the last-good config active; unknown keys are reported as
warnings in `termp status`.

### Defaults

| Key | Type | Default | Meaning |
|---|---:|---:|---|
| `enabled` | bool | `true` | Master switch. When false, no presence is shown. |
| `scan_interval` | duration string | `"3s"` | Process scan cadence. Invalid or non-positive values fall back to 3 seconds. |
| `pin` | string | `""` | Tool ID to prefer as the headliner when that tool is running. |
| `headliner_idle_timeout` | duration string | `"60s"` | How long the current headliner must be idle before activity-aware switching can replace it. Invalid or non-positive values fall back to 60 seconds. |
| `activity_switching` | bool | `true` | Allows the headliner to switch to a more active tool after the idle timeout. |
| `[display].tool_name` | bool | `true` | Shows `Using <tool name>` in activity details. |
| `[display].elapsed_timer` | bool | `true` | Sends the process start time so Discord shows elapsed time. |
| `[display].small_image` | bool | `true` | Uses the top "also running" tool as the small image when available. |
| `[display].collection` | bool | `true` | Shows other running tools as `also: ...` in the state line when no directory is shown. |
| `[display].buttons` | bool | `true` | Enables activity buttons. Discord allows at most two. |
| `[privacy].show_directory` | bool | `false` | Shows the working directory only when enabled and allowed. |
| `[privacy].directory_allowlist` | string array | `[]` | Optional allowed path prefixes. `~` is expanded. Empty allowlist allows any directory when directory display is enabled. |
| `[privacy].directory_basename_only` | bool | `true` | Shows only the final directory name instead of the full path. |
| `[cta].enabled` | bool | `true` | Adds the prototype termp CTA button if fewer than two tool buttons are present. |
| `[cta].label` | string | `"What is this?"` | Prototype CTA button label. |
| `[cta].url` | string | `"https://termp.example"` | Placeholder CTA URL. This is intentionally not a real landing page yet. |

### Per-Tool Overrides

Use `[tools.<id>]` to override a built-in or custom tool by ID.

| Key | Type | Meaning |
|---|---:|---|
| `enabled` | bool | Enables or disables this tool. |
| `tool_name` | bool | Overrides `[display].tool_name`. |
| `elapsed_timer` | bool | Overrides `[display].elapsed_timer`. |
| `small_image` | bool | Overrides `[display].small_image`. |
| `show_directory` | bool | Overrides `[privacy].show_directory`. |
| `directory_allowlist` | string array | Overrides `[privacy].directory_allowlist` for this tool. |
| `directory_basename_only` | bool | Overrides `[privacy].directory_basename_only`. |
| `buttons` | array | Replaces the tool's default buttons. Each button has `label` and `url`. |

### Custom Tools

Add custom entries with `[[custom_tools]]`.

| Key | Type | Required | Meaning |
|---|---:|---:|---|
| `id` | string | yes | Stable tool ID. If it matches a built-in ID, it overrides that built-in. |
| `display_name` | string | yes | Name shown in Discord. |
| `match.name` | string | one match required | Exact executable/base name match, case-insensitive. |
| `match.regex` | string | one match required | Case-insensitive regex matched against executable path and command line. |
| `image_url` | string | one image or slug required | External raster image URL for Discord. |
| `image_key` | string | one image or slug required | Uploaded Discord asset key. |
| `icon_slug` | string | one image or slug required | CDN logo slug resolved automatically. |
| `icon_source` | string | no | Slug source: `simpleicons` or `lobehub`. Defaults to `simpleicons`. |
| `priority` | int | no | Tie-breaker when multiple tools match. Higher wins. |
| `buttons` | array | no | Default buttons for this tool. At most two reach Discord. |

Custom tools can use `image_url`, `image_key`, or `icon_slug` for logos.
Explicit image URLs take precedence over image keys, which take precedence over
slug resolution.

```toml
[[custom_tools]]
id = "lazygit"
display_name = "lazygit"
match = { name = "lazygit" }
icon_slug = "lazygit"
icon_source = "simpleicons"
```

### Example

```toml
enabled = true
scan_interval = "3s"
pin = "codex-cli"
headliner_idle_timeout = "60s"
activity_switching = true

[display]
tool_name = true
elapsed_timer = true
small_image = true
collection = true
buttons = true

[privacy]
show_directory = false
directory_allowlist = ["~/dev", "~/work/oss"]
directory_basename_only = true

[cta]
enabled = true
label = "What is this?"
url = "https://termp.example"

[tools.claude-code]
show_directory = true
directory_allowlist = ["~/dev/oss"]
buttons = [
  { label = "Claude Code", url = "https://claude.com/claude-code" },
]

[tools.gemini-cli]
enabled = false

[[custom_tools]]
id = "my-tui"
display_name = "My TUI"
match = { name = "my-tui" }
image_url = "https://example.com/my-tui.png"
priority = 90
buttons = [
  { label = "Project", url = "https://example.com/my-tui" },
]
```

## Tools And Logos

The built-in registry currently ships 48 tools:

| Category | Tools |
|---|---|
| AI CLIs | Claude Code, Gemini CLI, Codex CLI, aider, Ollama |
| Editors | Neovim, Vim, Emacs, Helix, nano, micro, Kakoune |
| Terminal multiplexers | tmux, Zellij, GNU Screen |
| Git | lazygit, GitUI, tig |
| Files | Yazi, ranger, nnn, lf, Midnight Commander, broot |
| Monitors | htop, btop, Glances, bottom, gtop, bpytop |
| Containers and Kubernetes | k9s, lazydocker, ctop, kubectl tui |
| Disk and tasks | ncdu, gdu, Taskwarrior, calcurse |
| Messaging and media | NeoMutt, WeeChat, Irssi, cmus, ncmpcpp, spotify-tui, spotify_player |
| Network and utilities | gping, bandwhich, dust |

Logo resolution is dynamic. Built-in AI tools use LobeHub PNG assets; many dev
tools use Simple Icons rendered to PNG through `wsrv.nl`; entries without a
specific logo fall back to a generic terminal image. Custom tools use either
`image_url` or `image_key`.

## Multi-Tool Headliner

When multiple known tools are running, termp features one headliner and lists up
to three others in the state line as `also: ...`. The small image can show the
top other tool.

The headliner is sticky: it keeps the current featured tool unless another tool
is active enough and the current one has been idle for
`headliner_idle_timeout`. Set `pin = "<tool-id>"` to feature a favorite whenever
it is running.

## Privacy

termp has no telemetry. Nothing leaves your machine except the Discord Rich
Presence payload sent to your local Discord client.

The working directory is hidden by default. If you opt in with
`show_directory = true`, you can still restrict it with `directory_allowlist`,
and `directory_basename_only = true` keeps Discord from seeing the full path.

## License

[MIT](LICENSE)
