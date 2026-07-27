# Human intake via GitHub issues

How agents ask the human a question and get an answer mid-build without guessing.

## When to open an issue

Open a `needs-human` issue when a decision genuinely belongs to the human and cannot be
resolved from the docs or established defaults:

- An ambiguous product requirement or scope.
- A design fork with real trade-offs and no established answer.
- Anything outward-facing or irreversible, such as publishing, deletion, spending, or
  changes to third-party accounts.
- Input only the human has, such as credentials they must create or an asset they must
  supply.

Do not open one for implementation details that the lead can review and approve.

## Protocol

1. An agent or the lead opens an issue with the `needs-human` label using the
   [agent-question template](../../.github/ISSUE_TEMPLATE/agent-question.md).
2. That task pauses. Other independent work may continue.
3. The lead polls with `gh issue list --label needs-human --state open`.
4. The human answers in a comment and removes the `needs-human` label, or closes the
   issue, to signal that the question is answered.
5. The lead records the decision in the relevant documentation or context-ledger entry
   and resumes the task.

## Writing a good question

- Ask for one decision per issue.
- State the context, specific question, available options, and recommended default.
- Link the task and relevant public documentation.
- Make the question answerable in one reply.

## Labels

- `needs-human`: blocking question awaiting the human.
- `question:answered` (optional): retains an open issue for history after removing
  `needs-human`.

## Why issues

Issues provide notifications, threaded discussion, and an auditable history of human
decisions. They require repository and network access, so agents should use them only
when a human decision is actually needed.
