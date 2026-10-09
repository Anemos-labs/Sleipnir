# Roadmap

Priorities are ordered by their effect on reliable everyday use. This file
tracks unfinished product work; implementation history belongs in Git.

## Reliability

- Validate cache reuse on each supported provider dialect using actual API
  payloads and reported usage. Cover tool loops, worker fan-out, cancellation,
  model changes, resume, and compaction.
- Recover from named team stalls within a bound: hand a stalled worker's task
  over, or return an orphaned task to the pool, without waiting for the manager.
  Detection and the nudge to the responsible agent exist; recovery is manual.
- Refuse the manager's file edits at run time in a shared tree, as an isolated
  run already does: the manager delegates and reviews.
- Strengthen Windows support for shell analysis, process cleanup, and terminal
  behavior. The Windows test exclusions identify remaining portability gaps.

## Interaction

- Keep progress, approval ownership, and active worker counts accurate.
- Make long command output, help, and approval details readable in narrow terminals.
- Expose model reasoning settings only where the selected provider supports them.

## Evaluation

- Admit benchmark tasks only when acceptance criteria are recoverable from the
  prompt. Hidden tests must not require unspecified symbol names.
- Compare task success, total input, output, duration, and cost together.
  A higher cache percentage alone is not an improvement.
- Exercise the standing-goal loop and preserve its evidence-based completion checks.

See [Status](STATUS.md) for supported behavior, [Testing](TESTING.md) for offline
checks, and [Validation](VALIDATION.md) for endpoint tests.
