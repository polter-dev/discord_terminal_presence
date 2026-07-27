# Product overview

## Problem

Discord can show games and editor activity, but terminal users have no general way to
show that they are working in Claude Code, Gemini CLI, Codex CLI, or another terminal
tool.

## What we are building

**Terminal Presence** is a lightweight background daemon whose command and binary name
is **`termp`**. It detects interesting terminal tools and reflects them as Discord Rich
Presence with a tool logo, optional working directory, elapsed timer, other running
tools, and link buttons.

## Users and stories

- A terminal user wants Discord to show the active tool and its logo.
- A privacy-conscious user wants directories hidden by default and shown only after
  explicit opt-in, optionally limited to allowlisted paths.
- A tinkerer wants to add a niche tool and logo through config without changing code or
  creating a Discord application.
- Any user wants to disable the whole activity or individual fields easily.

## Goals

- Zero-config detection for the built-in tool catalog.
- Extensibility through custom tool definitions.
- Privacy-first defaults and no telemetry.
- One self-contained Go binary for macOS, Linux, and Windows.
- Native Discord IPC on Unix sockets and Windows named pipes.

## Non-goals

- Deep, tool-specific introspection of every CLI.
- A desktop GUI; configuration is TOML plus CLI and terminal interfaces.
- Tracking or telemetry. Runtime activity goes only to the local Discord desktop app.
- Content beyond Discord's fixed activity schema; see [`assets.md`](assets.md).

## Success criteria

Starting a known CLI makes the correct activity and logo appear within a few scans;
configuration changes hot-reload; a valid custom tool works end to end; and the working
directory is never displayed unless the user opts in.
