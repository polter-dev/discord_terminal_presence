# presence (package `internal/presence`)

**Purpose:** Maps detector snapshots into Discord Rich Presence and pushes throttled
updates through one client-owning writer goroutine.

**Public surface:** `Client` abstracts login/activity/logout and `RichClient` implements
validated Discord IPC. `Probe(appID)` performs a login/logout reachability check.
`StatusProbe(ctx, appID)` adds status-specific cancellation and I/O bounds. `Activity`,
`Image`, `Button`, `DisplayOptions`, `ActivityFromDetection`, and `CollectionState` form
the mapping boundary. `Writer` owns reconnect, throttling, coalescing, clear, and replay.

**Key files:** `internal/presence/activity.go` defines payload mapping and privacy-aware
display options. `internal/presence/client.go` owns framing, handshake, deadlines, and
probes. `internal/presence/conn_unix.go` and `conn_windows.go` discover, validate, and
dial IPC endpoints. `internal/presence/writer.go` owns client lifecycle.

**Invariants / gotchas:** Directory display is off by default and callers pass only an
allowlisted display directory. Buttons are capped at two. One writer goroutine owns all
client calls; the default activity write throttle remains 15 seconds. Final rendered
details, state, and large/small image tooltip text are capped at 128 runes with an
ellipsis. One-rune optional values are omitted and logged, so mapped output is either
empty or 2–128 runes. The activity name has no minimum. Opted-in directory or collection
placement does not depend on tool-name display.

The IPC boundary validates a per-field minimum for non-empty details, state, and image
tooltip text, along with the other bounded activity text, image values, button
labels/count, and absolute HTTP(S) URLs before encoding. The 2–128-character constraints
come from Discord's Social SDK references for
[`discordpp::Activity`](https://discord.com/developers/docs/social-sdk/classdiscordpp_1_1Activity.html)
and
[`discordpp::ActivityAssets`](https://discord.com/developers/docs/social-sdk/classdiscordpp_1_1ActivityAssets.html).
termp publishes through classic IPC via `rich-go`; a whole-payload rejection for violating
these constraints on that transport is inferred rather than observed. If classic IPC
returns code 4000, the writer currently classifies it as a permanent rejection for that
payload: it keeps the connection, does not schedule transport backoff or reapply the
payload, and attempts normally again when the desired activity changes. Transport,
timeout, and connection failures retain the existing reconnect backoff.

Directory path reduction is centralized in `DirectoryDisplay`: basename-only mode returns
one component and expanded mode returns at most the final two. Presence adds the folder
emoji separately so non-payload consumers can reuse the same privacy boundary.

`StatusProbe` checks cancellation before work and threads its context through discovery
and dialing. A watcher goroutine forces a read/write deadline to `time.Now()` when the
context ends so frame I/O unblocks promptly. The status-only `statusIOTimeout` remains
2 seconds; the daemon/client default remains 5 seconds, and Unix discovery retains its
separate 2-second aggregate dial budget.

Unix discovery tries an absolute `DISCORD_IPC_PATH`, deterministic runtime locations,
known Snap/Flatpak locations, then a deduplicated one-level glob. Candidates must pass
directory, ownership, socket-type, replacement, and peer-credential checks. Windows
validates the named-pipe peer.

**Depends on / used by:** Consumes `internal/detector` and `internal/registry`; used by
the daemon, status command, and TUI activity rendering.

**Open questions / TODO:** None currently.
