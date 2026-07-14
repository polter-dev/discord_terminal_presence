# termp

> Show your terminal work as a Discord Rich Presence — like "Discord shows what game you're playing," but for your CLI.

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.24-00ADD8?logo=go&logoColor=white)](go.mod)
[![Release](https://img.shields.io/github/v/release/polter-dev/discord_terminal_presence?display_name=tag&sort=semver)](https://github.com/polter-dev/discord_terminal_presence/releases)

termp watches your terminal and puts whatever command-line tool you're using on
your Discord profile — so friends see "Using Neovim" or "Using Claude Code"
instead of nothing at all.

Discord Rich Presence is the little status that shows up on your profile, like
"Playing…" when you launch a game. termp does the same thing for terminal tools.
It notices which CLI you're running — Claude Code, Gemini CLI, Codex CLI, and 48
other built-in tools — and shows it with the tool's logo, a running timer, an
optional folder name, a small "also running" icon, and buttons.

You don't need a Discord bot or any token to use it. termp ships with a Discord
Application ID (`1523168764793847918`) baked in. That ID is public and safe to
share — it's not a password or secret.

## Table of Contents

- [Features](#features)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Commands](#commands)
- [Autostart](#autostart)
- [Configuration](#configuration)
- [Supported Tools](#supported-tools)
- [How It Works](#how-it-works)
- [Privacy](#privacy)
- [Contributing](#contributing)
- [License](#license)

## Features

- **Spots your tools on its own** — checks what's running and matches it against
  a built-in list of 48 tools.
- **Real logos** — AI CLIs use LobeHub icons, dev tools use Simple Icons, and
  anything it doesn't recognize gets a generic terminal icon.
- **Handles several tools at once** — puts one tool in the spotlight and lists up
  to three more as `also: ...`.
- **Preview in your terminal** — `termp watch` shows the presence card live,
  right in the terminal.
- **Edit config without restarting** — change the TOML file and it takes effect
  right away. If you make a mistake, termp keeps using the last version that worked.
- **Private by default** — no tracking, your folder name stays hidden, and
  starting at login is opt-in.
- **Add your own tools** — match by name or pattern and give them a custom logo.
- **Starts at login** — works on both macOS and Linux.

## Installation

### Homebrew (macOS & Linux)

```sh
brew install --cask polter-dev/tap/termp
```

This grabs a ready-to-run build from GitHub Releases.

### Shell installer

```sh
curl -fsSL https://raw.githubusercontent.com/polter-dev/discord_terminal_presence/main/install.sh | sh
```

This downloads the latest release, checks it against `checksums.txt` to make sure
it wasn't tampered with, and installs `termp` to `/usr/local/bin`. To install
somewhere else, set `BINDIR`. To install a specific version, set `VERSION`.

### Linux packages

Grab the `.deb` or `.rpm` that matches your system from the
[Releases](https://github.com/polter-dev/discord_terminal_presence/releases)
page, then install it:

```sh
sudo dpkg -i ./termp_*.deb   # Debian / Ubuntu
sudo rpm -i ./termp_*.rpm    # Fedora / RHEL
```

### From source (Go)

```sh
go install github.com/polter-dev/discord_terminal_presence/cmd/termp@latest
```

This builds `termp` and puts it in your Go bin directory.

## Quick Start

Run the setup wizard once:

```sh
termp setup
```

The wizard creates your config file. It asks before turning on start-at-login,
and it leaves folder names hidden unless you say otherwise. To start termp right
now:

```sh
termp start
```

Then open a tool termp knows about (like `nvim` or `claude`) and look at your
Discord profile. Want to see the card without opening Discord? Run `termp watch`.

## Commands

```sh
termp <command> [flags]
```

If you just type `termp` on its own in a terminal, it opens the live `watch`
view.

| Command | What it does |
|---|---|
| `termp start` | Starts termp and keeps it running until you stop it. It records its process ID, watches your running tools, reloads your config on changes, and updates your Discord profile. |
| `termp stop` | Stops the running termp and cleans up its process-ID file. |
| `termp status` | Shows whether termp is running, whether it can reach Discord, whether start-at-login is on, where your config is and if it's valid, any warnings, and the tool it currently sees. |
| `termp watch` | Shows a live preview of the card in your terminal. Needs a real terminal window. Add `--once` to print one snapshot and quit. |
| `termp install` | Sets up start-at-login and starts termp (macOS LaunchAgent or Linux systemd user service). |
| `termp uninstall` | Turns off start-at-login. |
| `termp enable` / `termp disable` | Resumes or pauses start-at-login without removing it. |
| `termp settings` | Opens a menu to change settings. Needs a real terminal window. |
| `termp setup` | Runs the set-it-and-forget-it setup wizard. If there's no interactive terminal, it just writes the default config and prints what to do next. |
| `termp config init` | Writes a sample config, fully commented, to the default location. Add `--force` to replace an existing one. |
| `termp completion <bash\|zsh\|fish>` | Prints a tab-completion script for your shell. |
| `termp version` | Prints the version, commit, build date, Go version, and your OS and architecture. |

**Global flags:**

| Flag | What it does |
|---|---|
| `--verbose`, `-v` | Prints extra log detail (e.g. `termp --verbose start`). |
| `--version` | Prints the version and exits. |

## Autostart

```sh
termp install     # install + start at login
termp disable     # pause without removing
termp enable      # resume
termp uninstall   # remove entirely
```

Start-at-login works on **macOS** and **Linux**.

- **macOS** — adds a LaunchAgent at `~/Library/LaunchAgents/dev.termp.daemon.plist`
  that runs `termp start`, restarts it if it crashes, and writes logs to
  `~/Library/Logs/termp.log`.
- **Linux** — adds a systemd user service at `~/.config/systemd/user/termp.service`,
  then runs `systemctl --user daemon-reload` and
  `systemctl --user enable --now termp.service`.

## Configuration

Your config lives at `~/.config/termp/config.toml` (or
`$XDG_CONFIG_HOME/termp/config.toml` if you've set that variable). termp creates
the folder when it starts and reloads the file whenever you change it. If a change
has an error, termp keeps using the last good version. Unknown settings show up as
warnings in `termp status`.

Create a starter config with comments explaining every option:

```sh
termp config init          # won't overwrite a config you already have
termp config init --force  # overwrite it anyway
```

### Global options

| Key | Type | Default | Meaning |
|---|---|---|---|
| `enabled` | bool | `true` | The main on/off switch. Set to false to show no presence at all. |
| `scan_interval` | duration | `"3s"` | How often termp checks your running tools. Bad or zero values fall back to 3s. |
| `pin` | string | `""` | The ID of a tool you always want in the spotlight while it's running. |
| `headliner_idle_timeout` | duration | `"60s"` | How long the spotlighted tool must sit idle before another tool can take its place. |
| `activity_switching` | bool | `true` | Lets a busier tool take the spotlight once the current one has been idle. |

### `[display]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `tool_name` | bool | `true` | Shows `Using <tool name>` on the detail line. |
| `elapsed_timer` | bool | `true` | Shows how long the tool has been running. |
| `small_image` | bool | `true` | Uses your top "also running" tool as the small icon. |
| `collection` | bool | `true` | Lists your other running tools as `also: ...` when no folder is shown. |
| `buttons` | bool | `true` | Shows buttons on your presence (Discord allows two at most). |

### `[privacy]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `show_directory` | bool | `false` | Shows your folder, but only when this is on and the folder is allowed. |
| `directory_allowlist` | string[] | `[]` | Which folders are allowed to show, by path prefix (`~` works). Empty means any folder. |
| `directory_basename_only` | bool | `true` | Shows just the folder's name, not the whole path. |

### `[cta]`

| Key | Type | Default | Meaning |
|---|---|---|---|
| `enabled` | bool | `true` | Adds a termp button when there's room (fewer than two tool buttons already showing). |
| `label` | string | `"What is this?"` | The text on the button. |
| `url` | string | `"https://termp.polter.sh/"` | The link the button opens (the termp landing page). |

### Per-tool overrides — `[tools.<id>]`

Change the settings for one specific tool by its ID. You can set: `enabled`,
`tool_name`, `elapsed_timer`, `small_image`, `show_directory`,
`directory_allowlist`, `directory_basename_only`, and `buttons` (a list of
`{ label, url }` pairs that replace that tool's default buttons).

### Custom tools — `[[custom_tools]]`

| Key | Type | Required | Meaning |
|---|---|---|---|
| `id` | string | yes | An ID for the tool. Reusing a built-in ID overrides that tool. |
| `display_name` | string | yes | The name shown in Discord. |
| `match.name` | string | one match | Matches the program's exact name (ignores upper/lowercase). |
| `match.regex` | string | one match | A pattern matched against the program's path and command line (ignores upper/lowercase). |
| `image_url` | string | one image | A link to an image to use. |
| `image_key` | string | one image | The key of an image you uploaded to Discord. |
| `icon_slug` | string | one image | A logo name that termp looks up automatically. |
| `icon_source` | string | no | Where to look up the logo: `simpleicons` (default) or `lobehub`. |
| `priority` | int | no | Which tool wins when more than one matches — higher wins. |
| `buttons` | array | no | Default buttons for this tool (only two reach Discord). |

Pick one way to set the image. If you set more than one, termp uses `image_url`
first, then `image_key`, then `icon_slug`.

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

[tools.claude-code]
show_directory = true
directory_allowlist = ["~/dev/oss"]
buttons = [
  { label = "Claude Code", url = "https://claude.com/claude-code" },
]

[tools.gemini-cli]
enabled = false

[[custom_tools]]
id = "lazygit"
display_name = "lazygit"
match = { name = "lazygit" }
icon_slug = "lazygit"
icon_source = "simpleicons"
```

## Supported Tools

termp recognizes 48 tools right out of the box:

| Category | Tools |
|---|---|
| AI CLIs | Claude Code, Gemini CLI, Codex CLI, aider, Ollama |
| Editors | Neovim, Vim, Emacs, Helix, nano, micro, Kakoune |
| Multiplexers | tmux, Zellij, GNU Screen |
| Git | lazygit, GitUI, tig |
| Files | Yazi, ranger, nnn, lf, Midnight Commander, broot |
| Monitors | htop, btop, Glances, bottom, gtop, bpytop |
| Containers / K8s | k9s, lazydocker, ctop, kubectl tui |
| Disk & tasks | ncdu, gdu, Taskwarrior, calcurse |
| Messaging & media | NeoMutt, WeeChat, Irssi, cmus, ncmpcpp, spotify-tui, spotify_player |
| Network & utilities | gping, bandwhich, dust |

Using something that's not on the list? Add it yourself with `[[custom_tools]]`.

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

`termp start` runs a small background program. It looks at what's running on your
computer, matches it against its list of tools (built-in plus any you added),
picks one tool to spotlight, and hands the result to the Discord app on your
computer. There's no bot, no token, and nothing leaves your machine.

When several known tools are running, termp spotlights one and lists up to three
more as `also: ...`. The spotlight stays put: it keeps showing the current tool
unless another is busier *and* the current one has been idle for
`headliner_idle_timeout`. To always spotlight a favorite, set `pin = "<tool-id>"`.

## Privacy

termp does **no tracking**. The only thing that leaves your machine is the
presence info handed to the Discord app on your computer.

Your folder name is hidden by default. If you turn it on with
`show_directory = true`, you can still limit which folders show with a
`directory_allowlist`, and `directory_basename_only = true` shows only the
folder's name instead of the full path.

## Contributing

Contributions are welcome. See [AGENTS.md](AGENTS.md) for how the project is built
and how changes are proposed and reviewed.

## License

Released under the [MIT License](LICENSE).
