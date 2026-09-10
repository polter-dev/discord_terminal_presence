# Rate-limit ladder

Agent work should use the available delegated capacity first and degrade predictably.

## The ladder

```text
1. The lead dispatches Claude Code implementation subagents:
        - implementer-sonnet  (non-trivial or design-sensitive work)
        - mechanical-haiku    (bounded mechanical work)
        |
        | subagent capacity is unavailable
        v
2. Pause dispatch until capacity resets, then retry from the top
```

Implementation is always delegated. The lead plans, reviews, and approves; it does not
write the change itself. When subagent capacity runs out the pipeline pauses rather than
collapsing the dispatch step into the lead.

## Task routing

| Task type | Routing |
| --- | --- |
| Implementation, non-trivial logic, or design-sensitive work | `implementer-sonnet` |
| Mechanical, low-judgment, or high-volume work | `mechanical-haiku` |
| Planning, review, approval, and cross-cutting decisions | Lead; never delegated |

When the correct route is unclear, use `implementer-sonnet`. A task dispatched as
mechanical that turns out to need design judgment is handed back and re-dispatched — the
smaller model must not make the call itself.

## Detecting exhausted capacity

Treat a runner as unavailable when it returns an explicit quota/rate-limit error such as
HTTP 429, or repeatedly times out:

1. Record the condition, and the reset estimate when one is supplied.
2. Stop dispatching to that runner.
3. Retry periodically and resume once a dispatch succeeds.

## Pausing

If subagent capacity is unavailable:

- Stop dispatching instead of repeatedly retrying.
- Record the in-flight task, approved but unlanded work, and blockers.
- Use the context ledger and open issues as the resume point.
- Restart from the top of the ladder when capacity returns.

## Invariant

A capacity shortfall never relaxes the approval gate, and it is never a reason for the
lead to implement the change itself. The lead still reviews every command and change
regardless of which subagent produced it; see [`orchestration.md`](orchestration.md).
