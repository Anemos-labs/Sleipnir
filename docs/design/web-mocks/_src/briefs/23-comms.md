# V3 · COMMS: every stall is a channel

Deliverable: `docs/design/web-mocks/v2/03-comms.html`, title `Sleipnir Comms`.

## Idea
The owner liked Longhouse's chat *functionality*: several sessions and talking to individual team members. Comms puts that
inside the Cockpit's world: the **stalls are the channel list**. A compact live Cockpit (the horse, nine stalls with
presence) is always visible on top; clicking a stall (or a leg on the horse) opens that agent's channel in a large **comms
console** below. The hover-hold governor is the star here: you read a transcript while the linked legs, stalls, gantt rows and
files stay lit and still.

## Information architecture
* **Top band (~35% height, collapsible to a one-line strip)**: the Cockpit hero, smaller: horse with 8 legs, the
  shared-prefix bar, the warm-clock ring, plus the **stall row** (manager + 8 workers, each a channel button with state glyph, a
  one-line status that scrolls, unread count, `ask` pulse). The session tabs sit above it (three sessions, inbox).
* **Console (bottom, large)**: a 3-column layout.
  * **Channel list**: Manager (`#hall`) · Workers (8, grouped by state: asking / working / waiting / done) · **Tasks** (T1-T8 as threads with
    status steppers) · **Mail** (every worker-to-worker thread, read-only, labelled data) · **Approvals** (queue) · **System**
    (cache breaks, compactions, merges, governor).
  * **Transcript**: the selected channel in full: messages with tool chips (`Read api/server.go ✓ 6ms`), file-edit diff cards
    (compact, expandable, attribution-coloured), test result cards, refused actions (the manager's refused `Edit` with the reason),
    checkpoint markers, system rows; a message hover toolbar: `Reply in thread`, `Quote`, `Copy`, `Rewind to here`, `Open diff`,
    `Jump to event` (scrubs the gantt); typing indicators; unread rule; per-channel scrollback search; jump-to-bottom pill; Markdown
    export of a channel.
  * **Thread/details panel**: the agent or task: scope/lease, files owned (click opens the diff), cost, cache hit sparkline, current
    plan item, `Steer`, `Interrupt`, `Reassign`/`Add a worker`, tool log with filters.
* **Composer**: `@be-2 keep money in cents` (autocomplete) sends guidance to that worker only (**web-only; needs harness
  support**: the CLI's `/steer` reaches the main agent today: flag it in the UI and the report); `/` palette, `@file`, `⏎ queued`
  typed-ahead, `\` newlines, paste chip, history, mode chip, Send / Steer / Interrupt. A **split view**: two channels side by side.
* **Approvals** are first-class: the queue with `N waiting`, each as a rich message from the asking agent; also pinned over the
  manager channel; remembered rules appear as system rows ("rule added: Bash(npm install) · this session").
* **Everything else of the floor**: Files/Changes/Checkpoints as a console tab (`Workspace`), Cache page, Board, Replay, Sessions,
  Settings (all sections), Tools (runner), reachable from a slim top nav and the palette.

## Signature moments
1. Hover a message from be-2 in `#hall`: the view eases to a stop; be-2's leg, stall, gantt row, task T5, cart.go in the file strip all glow; release, the digest row summarises what you missed.
2. Click the `fe-1` stall: its channel opens with the question as a message; answer `1`; the stall un-pulses.
3. `@be-2 keep money in cents` -> only be-2's channel shows it; be-2 replies in its next step.
4. Overhear `be-1 ↔ ts-1` in Mail: the clamp question and answer, labelled data.
5. Switch to `orders-api` (single agent): the console has one channel, the horse stands.
6. Split view: manager + be-2.

## Avoid
Making it look like Slack (it must look like Cockpit); losing the live hero when you read; static avatars (the stall state is the avatar).
