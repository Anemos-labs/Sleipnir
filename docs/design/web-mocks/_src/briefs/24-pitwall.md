# V4 · PIT WALL: review while it runs

Deliverable: `docs/design/web-mocks/v2/04-pitwall.html`, title `Sleipnir Pit Wall`.

## Idea
A coding agent's product is changes to a repository, and the owner liked Forge's Files / Changes / Checkpoints tracker.
Pit Wall keeps the Cockpit as the **live instrument column** on the left and makes the **review workspace** the centre of the
screen: you supervise a swarm by reading what it changes, and you can see *which leg* changed it. The horse is a control
surface for the diff: hover a leg, its files and lines light up; hover a file, its leg and task light up.

## Information architecture
* **Left column (~28%): the pit wall**: HUD condensed to a stack (goal ring + plan checklist, warm-clock ring, hit ring, budget,
  `4 active of 8 workers`), a medium horse with eight live legs, the stall list (manager + 8) as compact rows
  (state, task, +/- lines, cost). Session tabs at the very top (three sessions, inbox).
* **Centre (~46%): the Workspace**: a left sub-pane with three tabs **Files / Changes / Checkpoints** (tree with ownership stripes
  in role colours, lock = leased scope, protected paths locked, `ask` markers, `sample` marks; Changes grouped by task or by agent,
  with +/- counts and verification state and per-file **Reviewed** checkboxes with a progress ring; Checkpoints c07..c04 + older
  as a vertical timeline with Diff/Restore and a preview-then-confirm). The editor: tabs, unified/split diff, syntax colouring,
  **per-line attribution gutter** (hover: `be-2 · T5 · c07 · 03:04:40`), hunk header actions (`Revert hunk`, `Tell owner`),
  `+` in the gutter to send a line-anchored steer to that line's owner (**web-only; needs harness support**), change
  overview ruler, **time-travel scrubber** c04..now with Restore, a caret typing live where an agent writes, ghost file for the
  pending `web/shop.js`/`package.json` change tied to the open question.
  Under the editor: **Verify / Merge queue / Log / Cache / Problems** panel (per-task `go test {dirs}` output, the serial merge queue
  with rebase/verify steps and bounce reasons, every tool call, the be-2 cache anomaly).
* **Right (~26%): Radio**: channel switcher (manager, 8 workers, mail), the diff-aware approval, composer. Hold-on-hover applies.
* **Changeset view** (end of goal / on demand): all merged tasks with their verification evidence, files changed, cost, and a
  **PR description** generator (conventional-commit title, behaviour, validation from the verify output, limitations): Copy
  and `Accept verified -> commit` (`--commit`, needs a clean branch; show the preconditions and a dry-run result).
* **Worktrees**: list of the isolation worktrees/branches per writer (`be-1/T4`...), their merge state, conflicts and bounces.
* **Settings, Tools, Sessions, Cache, Replay, Board, Mail**: all present (compact), the same floor; Settings is real.

## Signature moments
1. Hover leg `be-2`: in the tree `cart.go` glows, in the diff its lines are lit and the others dim; the hold eases the animation.
2. Drag the scrubber back to c05: `items.go` disappears; `Restore` shows exactly what will change before you confirm.
3. T4 verifies in the Verify panel, rv-1 wakes and reviews (a review note anchored on a line), T4 merges, `Accept verified` enables.
4. The ghost hunk of `web/package.json` inside the question card; answer `1`; it un-ghosts and fe-1's leg resumes.
5. Changeset -> PR description -> Copy.
6. Switch session: `orders-api`'s one-file fix with its two checkpoints.

## Avoid
A VS Code clone; making chat the centre; losing the horse; a diff viewer without attribution.
