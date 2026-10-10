# V2 · FLEET: many sessions, one pit wall

Deliverable: `docs/design/web-mocks/v2/02-fleet.html`, title `Sleipnir Fleet`.

## Idea
The owner could not find how to manage multiple sessions. Fleet makes **sessions the top level**. The landing screen is a
live **Fleet board**: every running session as a miniature Cockpit (a tiny drawn horse whose legs really move, a goal ring,
cost against budget, hit ratio, last event, a needs-you badge), plus recorded sessions, scheduled jobs and the daemon.
Click a card and the camera **zooms into that session's full Cockpit** (the beloved layout), while a thin **fleet strip**
of mini-horses stays on top so you can hop between sessions without going back. It is the option for someone who runs
several sessions and scheduled jobs at once and wants one inbox.

## Information architecture
* **Fleet board (home)**
  * Running sessions as cards (live animation, scaled down). Card actions: open, steer manager (one-line quick message),
    answer its question inline, stop, rename, move to background. A `+ New session` card opens a **stepper wizard**
    (project & model -> team size & isolation & verify -> permissions & budget & trust -> review the equivalent command line,
    copyable, with `sleipnir chat --swarm 8 --isolation worktree ...` -> Start).
  * **Inbox** lane (right or top): every open question across sessions as an approval card (agent, operation, scope, diff/command,
    why, 3 choices, quiet-period). A queue count; answered ones leave a trail ("answered 1 · fe-1 · shop").
  * **Today** strip: total spend vs a global budget, requests/min across sessions, cache saved est., sessions needing you.
  * **Recorded sessions** (resumable `↺`): searchable, filterable (project, model, cost, age), sortable; select several ->
    **prune** with a dry-run preview then apply (`--older-than`, `--keep`); `Resume`, `Continue latest`, `Open replay`,
    `Inspect cache`, `Fork from checkpoint` (web-only suggestion: flag it).
  * **Scheduled jobs** (cron): table + editor (cron expression with a human reading and next five runs, goal, cwd, model, mode,
    budget), `Run now`, `Pause`, daemon status/start, log excerpts; running scheduled jobs appear as cards (`docs-sweep`) with the
    refusal behaviour (`--ask-timeout`) visible.
  * **Compare**: select two sessions -> side-by-side half-width Cockpits (pair view) with a shared time scrubber.
* **Zoomed session**: full Cockpit view (8 worker legs, stalls, gantt, board, mail, queue, governor), the Radio rail with
  channels (manager, each worker, mail), Files/Changes/Checkpoints as a slide-over workspace (Forge tracker), Cache, Replay,
  and that session's Settings drawer. A breadcrumb `Fleet › shop` and the fleet strip (mini-horses with needs-you dots)
  stay on screen; `ctrl+1..9` jumps between sessions; `Esc Esc` returns to the board.
* **Global Settings**: Models, Roles, Providers & login, Permissions defaults, Trust ledger across projects (`trust list`),
  MCP approvals per project, Config layers, Appearance & motion (hover behaviour). The runner for every CLI command.

## Signature moments
1. First paint: three mini-horses trotting at different gaits on the board, a toast when `orders-api` raises a question.
2. The zoom transition into `shop` (the card grows into the Cockpit; it must unmount cleanly: lifecycle rule) and back.
3. Answer a question from the inbox without opening the session; the card's leg stops pulsing.
4. New-session wizard end to end: the command line preview is the real flags; the new card appears and starts its run.
5. Prune: dry-run table with sizes, total reclaimed, then apply; the recorded list updates.
6. Pair view: two sessions, one scrubber.

## Avoid
A spreadsheet look; cards that are static pictures; hiding what each session is *doing right now*.
