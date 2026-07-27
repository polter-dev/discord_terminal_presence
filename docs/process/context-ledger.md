# Context ledger

A token-management system. Instead of re-reading source every session, agents read a
compact per-module summary. Whoever edits a module updates its doc, so the ledger stays
true without a separate documentation pass.

## Layout

The ledger currently contains 11 Markdown files: the index plus one entry for each of
the 10 maintained module boundaries.

```text
docs/context/
├── index.md
├── cli.md
├── completioninstall.md
├── config.md
├── detector.md
├── presence.md
├── registry.md
├── service.md
├── tui.md
├── update.md
└── usage.md
```

The index maps each module to its context document and source paths. Module boundaries
follow the packages and application-level components described in
[`../product/architecture.md`](../product/architecture.md).

## Per-module doc format

Keep each doc short (aim for fewer than roughly 150 lines). It is a lookup index, not a
re-derivation of the code.

```markdown
# <module> (package `<path>`)

**Purpose:** one or two sentences.

**Public surface:** key exported types/functions and their contracts.

**Key files:** path and what it contains.

**Invariants / gotchas:** things a change must not break; non-obvious constraints.

**Depends on / used by:** neighboring modules.

**Open questions / TODO:** known gaps, with issue links when available.
```

## Rules

1. **Update with the change.** Editing a module's code means updating its
   `docs/context/<module>.md` in the same change. Update `index.md` only when the module
   list, source mapping, or one-line summary changes.
2. **Read the ledger first.** Before opening source, read the module's context doc. Dive
   into source only for details the ledger does not answer.
3. **Summarize, don't mirror.** Capture purpose, contracts, invariants, and gotchas
   instead of pasting code.
4. **One writer at a time per doc.** Avoid dispatching two agents that edit the same
   module concurrently.
5. **Prune stale entries.** Delete an entry when its module is removed. A wrong ledger is
   worse than no ledger.

## `index.md`

The single entry point is a table from every module to its context doc, source paths,
and one-line purpose. Add or remove a row when a module boundary changes. Agents start
there.
