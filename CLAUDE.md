@AGENTS.md

# Claude Code notes

The line above imports [`AGENTS.md`](AGENTS.md), the single source of truth for this
repository. This file adds only what is specific to Claude Code.

## Models

- **Orchestrator:** the latest Opus model. Set it with `/model` when the session starts.
- **Review and scout agents:** the latest Opus. The orchestrator cannot read code, so
  their judgment is the only check on what merges.
- **Implementation agents:** the latest Opus by default. Use the latest Sonnet only when
  the change is small and fully specified: the issue already contains a failing test, or
  the task is mechanical, such as a docs fix or a rename. When unsure, use Opus.

## Subagents

The role definitions are in `.claude/agents/` (machine-local, not tracked in git):
`implementer`, `reviewer` and `scout`. Each one points back to its section of
`AGENTS.md`, so the rules live in one place.

- Give every implementer its own isolated worktree.
- A subagent starts with no context except its brief. Name the issue or PR number in it.
- To run an implementer on Sonnet, override the model when dispatching it.

## Skills

- Implementation agents load `ponytail:ponytail` before writing anything.
- Review agents load `ponytail:ponytail-review`.

## Command tripwire

A `PreToolUse` hook (`.claude/hooks/approval-gate.py`, wired in `.claude/settings.json`)
blocks fetch-and-execute commands, secret exfiltration and destructive wipes. It is a
tripwire, not a review. If it blocks a command, do not work around it; report what was
blocked and why you wanted to run it.
