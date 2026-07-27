# CLI context

## Daemon lifecycle

`termp start` treats the PID file as the final startup arbiter and waits for a
bounded parent/child readiness check before reporting success. It also checks
the daemon-owned `discord.json`: a live publisher from the same executable path
and user is an existing daemon even when the PID file is missing or names a
different process.

`termp stop` stops both a valid PID-file owner and a different live Discord
publisher. Process validation requires the same user and exact executable image
path, so a development or staged binary cannot stop another installed copy of
termp.

`termp autostart disable` and `termp autostart uninstall` stop the tracked
daemon after pausing or removing the OS login service, including a detached
daemon started independently with `termp start`. They report a partial failure
instead of success if the service action completes but the daemon survives.

`termp connect` does not start a daemon. It returns exit status 1 with an
instruction to run `termp start` when no validated daemon exists. It targets a
fresh, validated `discord.json` publisher before the PID-file owner because the
publisher is the process that actually owns Discord IPC. When no publisher can
be validated, the PID-file owner is the fallback.

On Windows, connect uses a PID-addressed named pipe restricted to the current
user. The client validates that the named-pipe server is the intended termp
process before sending a JSON request. This request/response channel is
preferred over a signal because it can carry command options, structured
results, and future daemon commands; it is preferred over a watched control
file because delivery, acknowledgement, and access control are direct.

An ordinary connect is a successful no-op that prints `already connected (pid
N)` when the writer is connected. `--force` closes and re-establishes Discord
IPC even in that state. Otherwise the daemon attempts an immediate connection,
bypassing reconnect backoff. It returns the actual login or activity replay
error to the CLI. After a successful daemon response, the CLI polls for a newer,
connected daemon state from the targeted PID and prints `connected (pid N)` only
after confirmation. The request and readiness poll share a bounded timeout.
Timeouts and operational failures exit 1 without success output; invalid command
usage exits 2; confirmed and already-connected outcomes exit 0.

The daemon control transport is intentionally unimplemented on macOS and Linux
pending implementation and live verification on those platforms. The command
and exit behavior are platform-neutral, but a running non-Windows daemon returns
an explicit unsupported-transport error.

`termp status` uses a fresh, connected `discord.json` without making a direct
Discord IPC probe. If its publisher PID differs from the PID-file owner, status
reports both PIDs and identifies the concurrent-daemon fault instead of hiding
it behind a handshake timeout.

Setup continues to rewrite enabled autostart definitions so existing users get
corrected service definitions. When a daemon is already running, definition
reconciliation does not immediately launch the service again; the explicit
`termp autostart install` command retains its start-now behavior.

On Windows, detached startup publishes the PID file only after the named
shutdown event and its cleanup watcher have been created successfully. A parent
therefore cannot observe startup readiness before graceful `termp stop`
cancellation is available.

The canonical `install.sh` fetches release archives through
`https://termp.polter.sh/dl/curl/{os}/{arch}/{tag}` by default, using the same resolved
tag as the archive filename and checksum, and falls back to the tag-pinned GitHub asset
on any failure. Checksums remain tag-pinned direct GitHub downloads and are verified
unchanged.

Automatic updates delegate Homebrew Formula installations to
`brew upgrade polter-dev/tap/termp`; they do not use the Cask-only `--cask` flag.

`termp config init` refuses to replace symlinks and other non-regular config
paths, including with `--force`. Config creation and forced replacement write a
temporary file in the config directory and atomically rename it into place.
