# Detection

## Current strategy: process scanning

The daemon scans local processes with `gopsutil`, matches process identity against the
embedded and custom registry, and enriches only matched processes with working
directory, creation time, CPU time, and terminal-presence information. It requires no
shell modification or per-tool integration and works on macOS, Linux, and Windows.

Tool-specific hooks are not part of the current detection pipeline.

## Scan loop

- Poll every `scan_interval` (3 seconds by default).
- Match against process name, executable, argv0, and recognized language-runtime
  entrypoints. Registry regexes do not scan arbitrary later arguments.
- Exclude shell interpreters and configured helper subcommands to reduce false matches.
- Resolve terminal presence and activity with platform-specific implementations.
- Deduplicate matching processes by tool ID, retaining the strongest instance.
- Debounce normal changes for two matching scans. After three consecutive process-list
  failures, clear a previously emitted presence rather than leaving it stale.

## Featured-tool selection

The detector emits one featured tool plus other present tools:

1. If `pin` names an eligible running tool, it is featured.
2. Otherwise, the current featured tool remains until it has been idle for
   `headliner_idle_timeout` and another tool has measurably greater recent CPU activity.
3. If the previous featured tool is gone, choose by newest persisted episode start,
   priority, process creation time, and stable tool ID ordering.
4. If `activity_switching = false`, keep the current featured tool while it remains
   eligible.

Other tools are ordered by recent activity, priority, episode start, and ID. Up to three
are rendered in the collection text.

## Ownership boundary (shared hosts)

A candidate process must be affirmatively proven to belong to the current effective user
before it can be featured or appear in the collection at all — proof, not TTY presence,
is the gate. On Unix this compares gopsutil's reported effective UID against the daemon's
own `os.Geteuid()`; on Windows it compares process token-owner SIDs. If ownership cannot
be established (permission denied, the process exited mid-scan, or the platform is
unsupported), the process is excluded — the opposite of the terminal-presence fields
above, which fail open on an inspection failure. This exists because, without it, another
local user's TTY-attached tool session on a shared host (lab machine, shared build
server, multi-user `ssh` box) could be featured on your Discord profile and could
influence which tool it claims to be. The Unix comparison has real unit coverage; the
Windows token-SID comparison currently only cross-compiles and vets and has not been
exercised on Windows hardware (see `docs/context/detector.md`).

## Terminal presence and idle behavior

- A process definitively lacking a controlling terminal, or a detached tmux process, is
  excluded.
- macOS and Linux use the controlling terminal device and its access time.
- Windows associates a process with its console/terminal window. Foreground history and
  recent CPU activity determine featured eligibility after focus is lost. Window focus
  is per terminal window, so Windows Terminal tabs in one window cannot be distinguished.
- `idle_clear_timeout = "0"` disables idle clearing. Unknown terminal information fails
  open so inspection limitations do not erase legitimate presence.

## Tool registry

| Field | Meaning |
| --- | --- |
| `id` | Stable tool key |
| `display_name` | User-facing name |
| `match.name` / `match.regex` | Exact or regex process-identity matcher |
| `exclude` | Optional immediate-subcommand/helper exclusion regex |
| `image_url`, `image_key`, `icon_slug` | Logo source |
| `icon_source` | `simpleicons`, `lobehub`, `url`, or `key` resolution |
| `priority` | Match and selection tie-breaker |
| `buttons` | Optional validated default activity buttons |

Built-ins are embedded in `internal/registry/catalog.json`. A custom entry with an
existing ID replaces that built-in. See [`assets.md`](assets.md) for image resolution.

## Privacy boundary

The detector may resolve a matched process's working directory for selection and
rendering, but the CLI activity mapper removes it unless `show_directory` is enabled and
the path passes the effective allowlist. The presence payload never receives a
disallowed directory.
