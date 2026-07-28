# presence (package `internal/presence`)

**Purpose:** Maps detector snapshots into Discord Rich Presence and pushes throttled
updates through one client-owning writer goroutine.

**Public surface:** `Client` abstracts login/activity/logout and `RichClient` implements
validated Discord IPC. `Probe(appID)` performs a login/logout reachability check.
`StatusProbe(ctx, appID)` adds status-specific cancellation and I/O bounds. `Activity`,
`Image`, `Button`, `DisplayOptions`, `ActivityFromDetection`, and `CollectionState` form
the mapping boundary. `Writer` owns reconnect, throttling, coalescing, clear, and replay,
and reports connection (`WithConnectionState`) and publication (`WithPublicationState`)
health through separate hooks.

**Key files:** `internal/presence/activity.go` defines payload mapping and privacy-aware
display options. `internal/presence/client.go` owns framing, handshake, deadlines, and
probes. `internal/presence/conn_unix.go` and `conn_windows.go` discover, validate, and
dial IPC endpoints. `internal/presence/writer.go` owns client lifecycle.

**Invariants / gotchas:** Directory display is off by default and callers pass only an
allowlisted display directory. Buttons are capped at two. One writer goroutine owns all
client calls; the default activity write throttle remains 15 seconds. Final rendered
details, state, and large/small image tooltip text are capped at 128 runes with an
ellipsis. One-rune optional values are omitted, and the mapper returns omission diagnostics
for callers to route through their verbose-gated debug logger, so mapped output is either
empty or 2–128 runes without writing to the global logger. The activity name has no
minimum. Opted-in directory or collection placement does not depend on tool-name display.

The IPC boundary converts validated activity buttons directly to the wire payload's
field-compatible named type. It validates a per-field minimum for non-empty details,
state, and image tooltip text, along with the other bounded activity text, image values,
button labels/count, and absolute HTTP(S) URLs before encoding. The 2–128-character constraints
come from Discord's Social SDK references for
[`discordpp::Activity`](https://discord.com/developers/docs/social-sdk/classdiscordpp_1_1Activity.html)
and
[`discordpp::ActivityAssets`](https://discord.com/developers/docs/social-sdk/classdiscordpp_1_1ActivityAssets.html).
termp's IPC client is first-party (`client.go`, `conn_unix.go`, `conn_windows.go`,
`writer.go` — no third-party client dependency); a whole-payload rejection for violating
these constraints on classic IPC is inferred rather than observed. If classic IPC
returns code 4000, the writer currently classifies it as a permanent rejection for that
payload: it keeps the connection, does not schedule transport backoff or reapply the
payload, and attempts normally again when the desired activity changes. Transport,
timeout, and connection failures retain the existing reconnect backoff.

`Writer` reports the outcome of that permanent-rejection handling through
`WithPublicationState(func(error))`, separate from `WithConnectionState`: connection
health and "did the last publish actually land" are different facts, and a healthy
connection can coexist with a permanently rejected payload (issue #404 — Discord IPC can
accept the connection while rejecting every SET_ACTIVITY for it). The hook fires with the
rejection error on a permanent rejection, and with `nil` the moment either a later publish
succeeds or presence is cleared (nothing left to reject) — those are the only two things
that clear a reported rejection; it does not fire on every successful write (each periodic
reapply included), only on that rejected/not-rejected transition, so a caller persisting it
is not writing on every tick. `cmd/termp` persists this into the daemon state file and
`termp status` renders it as a `Published` line distinct from `Discord` (connection).

`SetActivity` calls `normalizeActivity` (in `activity.go`) before validating or building
the wire payload. It is the single choke point for Discord-facing text: it iterates
`activityTextFields`, the one enumeration of those fields (name, details, state, both
image keys, both image tooltip texts, and one entry per button label), and for each one
**sanitizes first and bounds second**. It copies the `Buttons` slice before touching
anything: `Activity` is passed by value but that slice shares its backing array with the
caller, and `Writer` holds a `desired` activity across ticks, so normalizing must not write
back into caller state. `validateActivity` iterates the same slice, so the
sanitize/bound rules and the validation rules cannot drift apart per field — that drift is
exactly what let #402 fix details/state while missing both image tooltips, and #422 fix
7 of 9 outbound fields while missing both image keys.

Sanitizing before validating means `validateActivity`'s length checks run against the same
bytes that reach Discord (#419, #422). Bounding *after* sanitizing is what makes those
checks hold by construction (#436): sanitization is not monotonically shortening in either
direction — `SanitizeSingleLine` expands each line break into the 3-rune `" ; "` separator
(a value bounded to exactly 128 runes measured 212 after sanitizing), and since #427
`Sanitize` can also return more text than it used to. A bound applied before sanitization
is therefore unreliable, so `normalizeActivity` (called from `SetActivity`) is the *only*
place bounding happens (#445). Consequences of bounding last: an over-long field is
truncated with an ellipsis and a field that sanitizes below the 2-rune minimum (or a
button label that sanitizes to nothing, which is dropped along with its button) is omitted
— instead of `validateActivity` rejecting the payload and publishing *nothing at all* for
that update, which was the user-visible defect. Structural errors the caller must fix, such
as more than two buttons or a non-HTTP(S) URL, are still hard validation failures.

`ActivityFromDetectionWithOmissions` (the detection→activity mapping in `activity.go`,
not the choke point) used to apply its own pre-sanitize bound with the same helper,
purely to produce `ActivityTextOmission` diagnostics before #443 existed. That bound ran
on the *raw* value, so it could truncate content the choke point would have kept whole
once sanitized, or add a false trailing ellipsis to a value that fit after sanitizing —
measured with a 200-rune directory basename of `x`+BEL pairs, the pre-bound produced a
66-rune result with a stray ellipsis where the choke point alone produces the correct
102-rune result with none. Worse, because the pre-bound checked the *raw* length, a value
long enough to clear the raw minimum but short enough to sanitize below it (e.g. an
`"x"` padded with two BEL characters) triggered no omission at all, silently disagreeing
with the choke point actually dropping the field. Fixed in #445: the mapping helper now
only computes the **sanitized** length to decide whether to report an omission — it no
longer truncates, so bounding happens exactly once, at the choke point, and the two
layers can no longer disagree about what "omitted" means. `ActivityFromDetection` (and
`ActivityFromDetectionWithOmissions`) can therefore return a field longer than Discord's
bound; only after `normalizeActivity` runs (as `SetActivity` always does) is the result
guaranteed bounded — callers that inspect a mapped `Activity` directly without normalizing
it first (as several tests now do) see the raw, unbounded value.

Residual, stated plainly: the two URL fields (`Image.URL`, `Button.URL`) are the one part
of the outbound payload `normalizeActivity` does not sanitize or bound. That is deliberate
— sanitizing a URL would corrupt it — so config-load validation is the *only* defence for
these two fields, and it must do two independent jobs: bound their length (`registry.
ValidateCustomTool` / `ValidateButtons`, 256 and 512 runes; safe because URLs are never
mutated outbound, so length cannot change after that check runs) and reject the same
control/C1/bidi rune classes the text fields reject. Until #444, `registry.ValidateHTTPURL`
only did the former: it was `url.ParseRequestURI` plus a host/scheme check, which rejects
raw ASCII control bytes as a side effect of URI parsing but accepts a C1 control (e.g.
U+0085 NEL) or a Unicode bidi override (e.g. U+202E RIGHT-TO-LEFT OVERRIDE) outright, and
those reached the wire unchanged in both an image URL and a button URL — link-target
spoofing on the user's Discord profile. `ValidateHTTPURL` now also calls the same
`firstDisallowedRune` (`terminaltext.IsControlOrBidi`) check `display_name`, `image_key`,
and button labels already used, and rejects rather than strips: silently rewriting a URL is
worse than refusing it. `validateActivity`'s own image-URL check (`internal/presence/
activity.go`) used to keep a second, independent `url.ParseRequestURI` copy that had the
identical hole; it now calls `registry.ValidateHTTPURL` directly instead of maintaining a
second copy that can drift. If a URL ever becomes runtime-derived, or anything starts
rewriting it, the length-safety half of this guarantee is gone and needs a bound of its
own — the rune-rejection half does not depend on that assumption.

`newSetActivityPayload` itself does no sanitizing or bounding — it is a plain copy of an
already-normalized `Activity`. Two generic walks of the marshaled JSON are the regression
backstops if a future field bypasses `normalizeActivity`:
`TestSetActivityWireHasNoRawControlOrBidiRunes` (no control/bidi runes on any string leaf)
and `TestSetActivityWireStringsAreBounded` (every string leaf is within a registered
bound, and an unregistered wire key is itself a failure, so a newly added field cannot pass
silently). `registry.ValidateCustomTool` and `ValidateButtons`
add a second, independent guard at config load for `display_name`, `image_key`, and
button labels, rejecting control characters outright so the user sees an actionable
error. `details` and `state` have **no** config-load guard — they are built at runtime
from directory names via `DirectoryDisplay`, not from static config — so `normalizeActivity`
is their only sanitization on any path; it is not merely defense-in-depth for those two
fields. Whether Discord itself rejects or mis-renders unsanitized bytes was never verified
before this fix (no live-Discord testing in this harness); the defect was that unsanitized
bytes left the process, not a confirmed Discord-side failure mode.

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

`DISCORD_IPC_PATH` is authoritative, on both Unix and Windows: when set, only its own
candidates are tried. A relative override is a hard error (`ErrDiscordIPCOverrideInvalid`),
and a set-but-unconnectable override returns `ErrDiscordIPCNotFound`/`ErrDiscordIPCUnreachable`
naming the override in the error text; the default candidate directories and glob search are
never consulted in either failure case, so a broken override cannot silently fall through to a
real running Discord instance.

The Unix socket-candidate validator resolves the candidate's parent directory through
`filepath.EvalSymlinks` before inspecting it (macOS ships `/tmp` as a symlink to
`/private/tmp`, so a literal `lstat` of the parent would see a symlink, not a directory, and
reject every `/tmp` candidate as an error instead of treating it as merely absent — including
the sticky-global-`/tmp` carve-out, which is compared against the resolved path). The socket
path itself is still `lstat`-ed, never `stat`-ed, inside the resolved directory, so a symlinked
socket planted by another user is still refused.

Unix IPC socket fixtures use `newIsolatedIPCSocket`, which binds a short
`/tmp/termp-ipc-*/s/discord-ipc-0` path, verifies the bound socket and path-length margin,
and cleans up the listener and directory. The extra `s` directory is required: production
discovery always globs `/tmp/*/discord-ipc-*`, so a fixture directly below its temporary
directory is visible to unrelated concurrent test processes. Tests that need to prove
fallback discovery point the fallback environment variables at the outer temporary
directory, whose one-level glob still reaches the nested socket without exposing it through
the global `/tmp` glob (#431).

**Depends on / used by:** Consumes `internal/detector` and `internal/registry`; used by
the daemon, status command, and TUI activity rendering.

**Open questions / TODO:** None currently.
