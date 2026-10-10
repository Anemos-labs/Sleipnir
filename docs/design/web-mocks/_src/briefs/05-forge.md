# 05 · FORGE — "the change is the unit"

Deliverable: `docs/design/web-mocks/05-forge.html`

## The idea
A coding agent's product is **changes to a repository**. The terminal is weak at exactly that: reading a diff, seeing
who wrote which line, comparing a file now with how it was ten minutes ago, deciding what to keep. The browser is
strong at it. Forge is a **review-first code workbench**: the file tree, the diff and the checkpoint timeline are the
centre of the screen; the agent (and the team) is a pane beside them. You supervise by reading changes, not
by reading a transcript.

It maps onto features Sleipnir already has and a terminal cannot do justice to: **checkpoints and `/rewind`**,
**`/diff`**, **file leases and scopes** (each writer owns a directory), **worktree isolation** with a **verifying
merge queue**, **permission questions that show the change as a diff**, **stale-read checks** and **protected paths**.

It is the **developer-native, review-centric** option. Its craft is *information density with clarity*, and a
diff viewer that is a joy.

## Layout (IDE-like, four regions)
* **Title/command bar (top)**: project + branch + `isolation: worktree` chip, a command palette field (`⌘K`: every CLI
  command), mode chip, model chip, budget/cost, connection chip, a one-click **`Accept verified → commit`** (the
  `--commit` flow) that is disabled until the merge queue has verified work.
* **Explorer (left, ~280px)** with three tabs: **Files** (a real tree of the project: each changed file has a status
  letter `A/M/D`, a coloured ownership stripe in the writer's role colour, a lock glyph when leased
  ("leased to be-2 · api/cart/**"), an `ask` marker when a question is pending for it; protected paths from the deny
  rules are shown locked), **Changes** (the same files grouped by task T4/T5/T6/T7 or by agent, with +/- counts and
  verification state), **Checkpoints** (the `/rewind` list as a vertical timeline `c07 … c04` with file counts, and
  `Diff` / `Restore` buttons that preview before restoring).
* **Editor/diff (centre)**: tabs for open files. Unified and split diff with syntax highlighting (Go, JS), line numbers,
  a **per-line attribution gutter** (a 3px stripe in the author agent's colour; hover → `be-1 · T4 · c07 · 03:04:38`),
  hunk headers with `Revert hunk` (a scoped rewind), inline "tell Sleipnir" on any line (click `+` in the gutter → a
  small composer that sends a `/steer` to the owner of that line), and a **time-travel scrubber** under the editor: drag
  from `c04` to `now` to see the file's diff at each checkpoint. A file being written *right now* (`api/cart/cart.go`) shows
  the agent's coloured caret and characters arriving; a file waiting on the question shows a ghost hunk labelled "pending".
* **Agent panel (right, ~400px)**: tabs `Manager · Team · Plan`. *Manager*: the conversation (compact), composer,
  `/` commands. *Team*: the eight agents as a compact list with state and a mini gantt strip. *Plan*: the six-step goal
  checklist and judge verdict. **The question is rendered diff-aware**: it names the agent and scope, shows the command
  (or the edit as a diff) and why it asks, three choices `1 2 3`, pause meter; it also dims the corresponding file in
  the explorer with a marker.
* **Bottom panel (tabs, collapsible)**: **Verify** (per-task output of `go test {dirs}` with ✓/✗ and timings),
  **Merge queue** (head `▸ T4`, rebase ✓, verifying; merged list; conflicts 0), **Log** (every tool call with agent id,
  like a read-only terminal), **Cache** (hit ratio sparkline per agent + the anomaly), **Problems**.
* **Status bar (bottom)**: branch, worktree, tests status, trust ✓, MCP 2, `4 active of 8`, warm clock `0:25`, cost.
* **More screens** (real, designed): **Merge queue** full view (verify results, bounce reasons), **Sessions** (history +
  resume), **Models/Settings** (models, roles, effort, budget, permissions + rules, trust, MCP, skills, providers),
  **Stats/Cache** page (layers, hit ratio, est. savings), **Cockpit** as a compact overlay (agents + board).

## Look
* Warm charcoal (`#15161a` base, `#1c1d22` panels, `#2a2c33` borders) with **ember accents** (`#ff9e64`, `#e0af68`) for the
  forge feeling; text `#d7d9e0`; syntax colours derived from Tokyo Night but tuned for charcoal. Role colours remain the
  per-agent stripes and carets.
* UI in `IBM Plex Sans` or `Geist` (system: `system-ui`), 13px base, compact 28px rows; code in `JetBrains Mono` 12.5px.
  Crisp 1px borders, tiny radii (3-4px), icons you draw as a consistent 16px stroke set. Do not clone VS Code's chrome
  literally: give it its own proportions and language.
* Motion is small and purposeful: caret + typing, hunk highlight when changed, a gutter stripe pulse when a line lands,
  scrubber transitions.

## Signature moments to nail
1. First paint: `cart.go` open as a diff with be-2's caret typing the `+` line; the tree showing ownership stripes and a lock.
2. The attribution gutter hover and the `Revert hunk` affordance.
3. The scrubber: drag back to `c05`, see `items.go` appear/disappear; `Restore` shows a preview first.
4. The question as a diff-aware dialog in the right panel; answer with `1`; the Log tab records it; `web/shop.js` un-ghosts.
5. T4 verifies in the Verify tab and moves into merged; `Accept verified → commit` enables.
6. `Changes` grouped by task with +/- counts and verification state.

## Avoid
Chat as the primary surface, a VS Code skin, neon HUD instruments, serif/editorial styling, dashboards for their own sake.
