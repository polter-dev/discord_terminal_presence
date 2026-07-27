# Rate-limit ladder

Agent work should use the available delegated capacity first and degrade predictably.

## The ladder

```text
1. Codex CLI implementation agents
        |
        | Codex is rate-limited
        v
2. The lead dispatches fallback implementation agents:
        - a capable coding model for design-sensitive work
        - a smaller model for bounded mechanical work
        |
        | fallback capacity is also unavailable
        v
3. Pause dispatch until capacity resets, then retry from the top
```

Codex is the primary implementation runner. Fallback agents are used only when that
capacity is unavailable; the pipeline pauses when neither runner has usable capacity.

## Task routing on the fallback rung

| Task type | Routing |
| --- | --- |
| Implementation, non-trivial logic, or design-sensitive work | Capable coding model |
| Mechanical, low-judgment, or high-volume work | Smaller model |
| Planning, review, approval, and cross-cutting decisions | Lead; never delegated |

When the correct route is unclear, use the more capable implementation model.

## Detecting a Codex rate limit

Treat Codex as unavailable when `codex exec` returns an explicit quota/rate-limit error
such as HTTP 429, or repeatedly times out:

1. Record the condition and reset estimate when one is supplied.
2. Use the fallback rung for subsequent dispatches.
3. Retry Codex periodically and return to the first rung once it succeeds.

## Pausing

If fallback capacity is also unavailable:

- Stop dispatching instead of repeatedly retrying.
- Record the in-flight task, approved but unlanded work, and blockers.
- Use the context ledger and open issues as the resume point.
- Restart from the top of the ladder when capacity returns.

## Invariant

Fallback never relaxes the approval gate. The lead still reviews every command and
change regardless of which implementation runner produced it; see
[`orchestration.md`](orchestration.md).
