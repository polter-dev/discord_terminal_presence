# Discord assets and the activity ceiling

## Discord application

Terminal Presence uses one shared Discord Application. Its Application ID
(`1523168764793847918`) is public because the client sends it to Discord to render
presence. The ID is safe to embed and commit; no bot token or other secret is needed.

The Discord Application's configured name appears as the activity title prefix, so the
product name should remain **Terminal Presence**. The executable and command remain
`termp`.

## How logos resolve

A tool can identify its large or small activity image in several ways:

1. `image_url` supplies an absolute HTTP(S) raster image URL.
2. `image_key` names an art asset uploaded to the shared Discord Application.
3. `icon_slug` resolves through the configured icon source (`simpleicons` or `lobehub`).
4. A tool without a usable image falls back to the Terminal Presence mark at
   `https://termp.polter.sh/discord-app-icon.png`.

When multiple values are present, runtime resolution prefers `image_url`, then
`image_key`, then the resolved icon slug. The five flagship tools (`claude-code`,
`gemini-cli`, `codex-cli`, `aider`, and `ollama`) use project-hosted PNGs at
`https://termp.polter.sh/logos/<id>.png`. Other built-ins use their configured icon
source or the generic fallback.

External URLs let custom tools use logos without requiring a new upload to the Discord
Application. Discord activity images must be raster-compatible; the Simple Icons path
uses an image proxy to produce PNG output.

## Activity schema

Discord fixes the available fields. Terminal Presence can send:

| Field | Use |
| --- | --- |
| Activity name/type | Featured tool name with activity type `Playing` |
| `details` and `state` | Configured details, other running tools, fallback text, and an opted-in directory |
| Large image and tooltip | Featured-tool logo and display name |
| Small image and tooltip | Highest-ranked other running tool when enabled |
| Start timestamp | Elapsed time for the featured tool's persisted episode |
| Buttons | Up to two validated label/URL pairs |

The application cannot add arbitrary rich text, custom colors, extra lines, more than
two images, or more than two buttons.

## Mapping detection to payload

| Detection or config | Payload |
| --- | --- |
| Featured tool | Activity name and large image |
| Up to three other enabled tools | Collection text; the first may also supply the small image |
| Allowed working directory | Directory text, only after privacy opt-in |
| Episode start | Start timestamp when `elapsed_timer` is enabled |
| Tool buttons plus optional CTA | At most two buttons when `display.buttons` is enabled |
| `details_format` and fallback messages | Details/state text selected by the activity mapper |

When present, `details`, `state`, and large/small image tooltip text contain 2–128
characters. A rendered one-character optional value is omitted and the omission is
logged, keeping termp from sending a value outside Discord's documented constraint.
Custom-tool `display_name` values must contain at least two characters so invalid
tooltip text is reported while loading configuration.

## Privacy reminder

The working directory is the field most likely to reveal a sensitive name. Directory
display is off by default and is filtered through the configured allowlist before the
presence module receives it. See [`config-schema.md`](config-schema.md).
