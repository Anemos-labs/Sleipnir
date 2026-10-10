# 04 · LONGHOUSE — "your team, in a room"

Deliverable: `docs/design/web-mocks/04-longhouse.html`

## The idea
A swarm is a team, and everyone already knows the best interface for a team: a **shared workspace with channels,
direct messages and threads**. In Longhouse the manager and the seven workers are *teammates in a room*. You talk to
the manager in `#hall`; you can walk over to any rider's channel and tell them something (`@be-1 use the other file` is
`/steer` to that agent); their mail to each other is a set of DMs you can overhear (read-only, labelled as data); each
task is a **thread** with a status stepper; the merge queue, the board and the cache alerts are channels with bots
posting structured cards. Approvals are *interactive messages from the agent who is asking*.

It is the **familiar, social, low-friction** option: zero learning curve for anyone who has used Slack, Discord or
Linear's inbox. Its craft is *rich message design* and *presence*: knowing who is doing what right now.

## Layout (three panes + a thin rail)
* **Workspace rail (far left, 56px)**: the project/session as a squircle avatar (the favicon horse), other sessions
  (`orders-api`), `+` new session, bottom: settings and the connection pip `● 127.0.0.1:6969`.
* **Sidebar (dark ink `#24232e`-family, ~270px)**: project name + branch + isolation chip (`worktree`); sections with
  unread badges: **Channels** `#hall` (you ↔ manager) · `#board` · `#merge-queue` · `#cache` (alerts) · `#approvals`;
  **Riders** (the 8 agents; role-coloured avatar with a leg/hoof glyph, presence dot = state, a live one-line status
  under the name, typing dots when thinking, `ask` shows an amber badge); **Mail** (DM threads between workers, read-only);
  **Sessions** (history); a footer with the budget `$0.08 of $5.00`, mode chip, model chip.
* **Main (light, crisp)**: channel header (name, topic = the goal, the warm-clock chip `warm 0:25`, member stack, search `⌘K`),
  the message stream, and the composer. A message = avatar, name, role pill, time, body. Agent bodies carry **rich
  blocks**: tool chips (`⚙ go test ./api/catalog/...`), compact diff cards with expand, test result cards, file
  chips; system rows for `T4 merged`, `◆ compacted`, `⚠ cache break` (as a `#cache` bot card with the explanation).
  Day/time dividers, "new messages" rule, hover toolbar (reply in thread, copy, rewind to here), typing indicator.
* **Thread/Details panel (right, toggled, ~380px)**: opening a task shows its **thread**: header `T4 catalogue: GET
  /items?page&size` with a stepper `todo → running → verify → merged`, owner, scope/lease, files owned, cost, hit
  sparkline, messages, the verification output; opening a rider shows their profile (state, current line, their own
  transcript, cache, `Steer`, `Interrupt`, `Reassign`). The goal and its six-step plan live in a pinned message in `#hall`.
* **Composer**: rich text box with `@` mention autocomplete (agents and files), `/` commands, mode chip, attach, `Send`,
  and while a turn runs: `Steer` / `Interrupt`; typed-ahead shows as a queued message with a clock icon.
* **The question** is a pinned **interactive message from fe-1** in `#approvals` *and* a banner in `#hall`: the
  command in a code block, why it asks, three buttons (`Yes`, `Yes, and don't ask again for npm install this session`,
  `No, and tell Sleipnir what to do instead`) inert until the pause meter fills; the sidebar shows an amber badge.
* **More screens** (real, designed): **#board** as a kanban, **#merge-queue** as a verification feed, **#cache** as
  alert cards + a per-rider cache table, a **Rider profile**, **Sessions/Archive** (history + resume), **Preferences**
  (models & roles, effort, budget, permissions, trust, MCP, skills, providers/login), **Search** results (messages,
  commands, sessions, files).

## Look
* **Split-tone**: sidebar and rail in the logo ink (`#24232e` / `#1b1a24`), main area clean off-white (`#fbfbfd`) with
  hairline borders, soft shadows, 8-10px corner radii, 14px base type, a friendly humanist sans (`Figtree`, `Onest`, `DM Sans` or
  `Instrument Sans`, not Inter; system fallback `system-ui`), code in `JetBrains Mono`.
* Avatars: rounded squares filled with the role colour, a tiny white leg glyph; the manager's avatar is the horse mark.
  Presence dots: green working, amber asking, gray idle, teal done, red stuck. Accent for UI chrome: violet `#7048e8`.
* Micro-interactions: typing dots, message slide-in, unread badges, hover toolbars, thread open transition,
  keyboard `⌘K` switcher, `Esc` to close panels. Reduced-motion safe.

## Signature moments to nail
1. First paint: a lively `#hall` with the manager's plan card pinned, the sidebar presence dots changing as agents work.
2. Opening the T4 thread: stepper, verification card turning ✓, "merged" system row.
3. Overhearing `be-1 ↔ ts-1` in Mail (clamp question and answer), clearly labelled data.
4. The interactive approval message and its pause meter.
5. `@be-2 keep money in cents` in the composer: autocomplete, send, and be-2 replying in its channel (`/steer`).
6. `#cache`: the cache-break bot card with explanation and the est. cost.

## Avoid
Terminal aesthetics, dense HUD instruments, serif/editorial styling, emoji reactions, a gimmicky "tavern" skin. Keep it
professional: this is a serious workspace that happens to feel like a team chat.
