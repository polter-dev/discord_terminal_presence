# Config schema

On macOS and Linux, the default location is `~/.config/termp/config.toml`, or
`$XDG_CONFIG_HOME/termp/config.toml` when `XDG_CONFIG_HOME` is set. Windows uses the
native user config directory, normally `%AppData%\termp\config.toml`.

The format is TOML. Valid changes hot-reload; an invalid edit leaves the daemon's
last-good behavior in place and is surfaced through logs or `termp status`. Unknown keys
produce warnings instead of aborting the load.

Generate the current fully commented schema with:

```sh
termp config init
```

Use `termp config init --force` only when intentionally replacing an existing regular
config file.

## Design principles

- **Privacy first:** working-directory display is off by default.
- **Layered controls:** global settings are followed by display/privacy defaults and
  per-tool overrides.
- **Extensible:** custom tools can supply exact or regex identity matching and an image
  URL, Discord asset key, or icon slug.

## Global options

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | Master switch; false clears presence |
| `start_at_login` | bool | `true` | Setup preference mirrored by login-service installation |
| `update_check` | bool | `true` | Permit anonymous GitHub release checks |
| `auto_update` | bool | `false` | Attempt a best-effort update when the daemon starts |
| `scan_interval` | duration | `"3s"` | Process-scan cadence; must be positive |
| `idle_clear_timeout` | duration | `"20m"` | Terminal-idle timeout; zero disables idle clearing |
| `pin` | string | `""` | Preferred featured tool ID while it is eligible |
| `headliner_idle_timeout` | duration | `"60s"` | Idle time before a busier tool may replace the current featured tool |
| `activity_switching` | bool | `true` | Allow activity-based featured-tool changes |
| `details_format` | string | `"Using {tool}"` | Custom details template supporting `{tool}` and `{dir}` |
| `fallback_messages` | string[] | built-in messages | Non-empty details choices when no directory or collection is rendered |
| `feedback_url` | URL | project feedback URL | Link opened by the settings feedback action |

`NO_UPDATE_CHECK` disables release checks whenever that environment variable is present,
regardless of `update_check`.

## UI options (`[ui]`)

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `accent_color` | string | `"purple"` | Named palette color or `#RGB`/`#RRGGBB` value |

Supported names are `purple`, `blue`, `green`, `orange`, `pink`, and `red`. An invalid
accent produces a warning and falls back to purple.

## Display options (`[display]`)

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `tool_name` | bool | `true` | Allow tool-name content |
| `elapsed_timer` | bool | `true` | Send the episode start timestamp |
| `small_image` | bool | `true` | Show the highest-ranked other tool as the small image |
| `collection` | bool | `true` | Render other enabled tools in collection text |
| `buttons` | bool | `true` | Allow activity buttons |

## Privacy options (`[privacy]`)

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `show_directory` | bool | `false` | Opt in to directory display |
| `directory_allowlist` | string[] | `[]` | Allowed path prefixes; an empty list allows any path after opt-in |
| `directory_basename_only` | bool | `true` | Show only the final path component; false shows at most the last two components |

Allowlist entries expand `~` and are compared by path components, not raw string prefix.

## CTA options (`[cta]`)

| Key | Type | Default | Meaning |
| --- | --- | --- | --- |
| `enabled` | bool | `true` | Add the project CTA if fewer than two tool buttons exist |
| `label` | string | `"What is this?"` | CTA button label |
| `url` | URL | `https://termp.polter.sh/` | CTA target |

## Per-tool overrides (`[tools.<id>]`)

Each tool may override:

- `enabled`
- `tool_name`
- `elapsed_timer`
- `small_image`
- `show_directory`
- `directory_allowlist`
- `directory_basename_only`
- `buttons`

An explicitly empty per-tool allowlist or button list replaces the corresponding global
or built-in value.

## Custom tools (`[[custom_tools]]`)

| Key | Required | Meaning |
| --- | --- | --- |
| `id` | yes | Stable ID; reusing a built-in ID replaces it |
| `display_name` | yes | User-facing name, 2–128 characters |
| `match.name` or `match.regex` | one | Exact or regex match against process identity |
| `exclude` | no | Regex rejecting an immediate helper subcommand |
| `image_url`, `image_key`, or `icon_slug` | one | Image source |
| `icon_source` | no | `simpleicons`, `lobehub`, `url`, or `key` |
| `priority` | no | Higher-priority match and selection tie-breaker |
| `buttons` | no | Up to two `{ label, url }` entries |

IDs, display names, images, and buttons have Discord-facing length and URL validation.
Regexes are compiled when the registry is built. Matching uses process identity and
recognized runtime entrypoints, not arbitrary later command arguments.

## Example

```toml
enabled = true
update_check = true
auto_update = false
scan_interval = "3s"
idle_clear_timeout = "20m"
pin = ""
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

[[custom_tools]]
id = "lazygit"
display_name = "lazygit"
match = { name = "lazygit" }
icon_slug = "lazygit"
icon_source = "simpleicons"
priority = 10
```

## Resolution and validation

1. Global `enabled = false` suppresses every activity.
2. A per-tool `enabled = false` suppresses that tool.
3. Per-tool display/privacy values override their global section.
4. Per-tool buttons replace registry defaults; the CTA may fill a remaining slot.

`scan_interval` and `headliner_idle_timeout` must be positive Go durations.
`idle_clear_timeout` must be non-negative. Directory display stays disabled until
explicitly enabled, and at most two validated HTTP(S) buttons reach Discord.

Related references: [`detection.md`](detection.md) and [`assets.md`](assets.md).
