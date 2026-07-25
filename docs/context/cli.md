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

`termp status` uses a fresh, connected `discord.json` without making a direct
Discord IPC probe. If its publisher PID differs from the PID-file owner, status
reports both PIDs and identifies the concurrent-daemon fault instead of hiding
it behind a handshake timeout.

Setup continues to rewrite enabled autostart definitions so existing users get
corrected service definitions. When a daemon is already running, definition
reconciliation does not immediately launch the service again; the explicit
`termp autostart install` command retains its start-now behavior.
