# Terminal interface

## Chat

Run `sleipnir` or `sleipnir chat` in a terminal. Conversation output goes to
scrollback; the input, current activity, approval prompt, and key hints occupy
the live region. `--plain` selects the line-oriented interface.

The model can stream text while input is edited. Ordinary messages submitted
during a turn queue for the next turn. Inspection commands such as `/cost`
answer during work.

## Goals and interruption

`/goal TEXT` starts a standing goal. The completion judge checks evidence after
each turn and can request another pass. It searches the durable session log for
worker and manager tool evidence across earlier turns and compaction. Results are
paged newest first, including full stored tool outputs, so the latest run of a
check is found before older ones and missing evidence in the current
conversation does not by itself require repeating completed work. Inspection is
read-only, supports batched record reads, and is bounded by the session budget,
model context budget and 24 retrieval requests per judgment; an exhausted
inspection limit pauses the goal without certifying completion. A judge answer
that is neither an evidence request nor a verdict is asked for again, twice at
most; a third pauses the goal. `/goal` shows its state and plan.

The judge runs on the session's model unless the `judge` role names one
(`--role-model judge=provider/model`, `models.roles.judge`, or `/roles
judge=provider/model`). With a judge model of its own, a "done" is checked a
second time on the session's model before the goal is met: when that judgment
disagrees, the agent is sent on with what it found missing. Only "done" is
checked twice, so the second judgment costs a request at most once per claimed
completion.

A lesson memory turns on when the `embedder` role names an embeddings model
(`models.roles.embedder` or `--role-model embedder=provider/model`, any
OpenAI-compatible `/embeddings` endpoint). A goal's first message then carries
up to five lessons from earlier goals whose tasks are closest to its objective,
and when the goal is met or given up on, the lessons it was shown are credited
or debited and up to three new ones are distilled from the judge's verdicts and
saved in `~/.sleipnir/lessons.json`. Lessons are ranked by similarity and by how
often they helped; ones that keep failing are dropped. An optional `reranker`
role (a `/rerank` endpoint) orders the closest twelve before five are shown.
No model is built in: both are whatever you name. A session started with
`--no-memory` uses neither.

Escape or Ctrl+C during a turn cancels it. `/goal pause` and `/goal clear`
interrupt first, then update goal state after the running turn has stopped.
`/goal resume` continues a paused goal. `/exit` cancels active work and exits.

## Panels

- Ctrl+G or `/agents` opens the team's live cockpit. Ctrl+G returns to chat.
  The cockpit can open when workers start and closes for an approval or the
  end of a turn.
- Ctrl+T or `/stats` opens live token usage, cache reads, estimated cost, and
  the current prompt layers. Escape or Ctrl+T returns to chat. Up/Down and
  Page Up/Page Down scroll the panel.
- `sleipnir watch` follows a session in a separate terminal.
- `sleipnir replay` reads recorded events. It is not a live model run.

Panels read the event log and do not append stale snapshots to conversation
scrollback. Approval prompts take precedence over panels.

## Approval prompts

Prompts identify the requesting agent, operation, and scope. Approval choices
distinguish one operation from remembered rules and recognized project
build/test commands. Input typed before a prompt becomes active must not approve it.

Long operations must remain inspectable before approval. Displayed durations
exclude time spent waiting for approval where that time is known.

## Rendering requirements

- Check at 80 columns, in short windows, and after resize.
- Preserve readable highlights in color and monochrome modes.
- Sanitize untrusted terminal text.
- Keep focus, cursor position, and queued input visible.
- Respect `NO_COLOR`, `REDUCE_MOTION`, `SLEIPNIR_ANIM=0`, and `--no-anim`.
- Report active workers separately from team capacity.
- Show observations as observations: cache estimates and unknown prices need
  appropriate labels.

## Verification

Terminal tests run the program against scripted sessions and inspect an emulator.
Visual changes also require a real-terminal capture and image inspection.
See [Building](BUILDING.md).

Committed recordings are fixtures with specific provenance.
[Gallery](GALLERY.md) identifies scripted and real-model recordings; they are
not a guarantee that the current interface or performance matches a capture.

## Web interface

`sleipnir web` serves the same sessions in a browser. The first line it prints is the address to open (see
[Command-line reference](CLI.md)); opening it signs the browser in and replaces the address with a clean one. A tab that
has no session, or whose session has ended (24 hours without use, logout, or a rotation of the token), shows a short page that says to
open the address again; nothing is shown from the session until then.

- The page follows a session through an event stream. When the connection drops, the browser reconnects and the server
  replays what was missed from its recent history; when the browser has been away longer than that history reaches, or the
  server restarted, the page is told and fetches the current state instead of guessing.
- A client that cannot keep up with the stream loses progress updates first, then ordinary events, and is told that it
  did. Questions and state changes are never dropped: a client that cannot hold them is disconnected and reconnects.
- Actions that raise privilege (a permissive mode, trusting a project, approving a tool server, adding a schedule,
  updating, saving a key) take a second step: the page asks for a single-use confirmation that is valid for one minute and
  for that action only. The server decides what a request raises from the settings that would take effect (a dangerous mode,
  allow rules the session does not have, a verify command, a project's own files, the removal of a deny or ask rule); the page
  lists those reasons, with the request (method and path) and the scope it confirms, and asks before it sends the request
  again. Every reason is in view at once: the rules of the tests preset are one line (`allow the tests preset: build and test
  commands of go, cargo, npm… (N rules) ▸ show`), and any other list of more than six rules is one line with its count; each
  comes after the other reasons and opens to its rules on a click. Outside the New session dialog, a dangerous mode is confirmed by typing its name; in that dialog, choosing the mode is
  the confirmation. A confirmation covers what was shown: when an approved tool server's entry changed after its card was read,
  nothing is confirmed, and the card shows the entry as it is now for a new approval.
- Names in a session log (agents, tasks, questions) are shown only when they are plain words (letters, digits, `.`, `_`, `:`
  and `-`, at most 64); any other name is shown as `invalid-id`.
- The page sends no request to any other origin; the server's Content-Security-Policy would block it.

### Sessions and views

One page shows every session the server hosts, one tab each in the session strip; a tab keeps running, and can ask questions,
while another is in front. `+ New` starts a session with every chat flag, seeded from the flags `sleipnir web` was started with. Its folder is a path typed in a
field that completes folder names as it is typed (`Tab` completes, the arrow keys choose, `Enter` takes one; `~` is the home folder) and says
under it what the path is: a project, a folder that will be made, a git repository or not. The `--cwd` of the Schedule form is the same field (a job's folder must exist: it is not offered to be made). A new or empty folder can be made a git
repository with an empty first commit (on by default); a folder that has files is never made one. The `Projects` button lists the folders the
server knows, with their trust when the project's files are not trusted (`not trusted`, `changed since trusted`, `partial` when not every
file could be scanned, `unreadable`). Whatever needs explaining has a `?` beside it that says in plain words what it is, on hover, on keyboard focus and on a tap (`Escape` hides it before it closes the dialog): the workers, the isolation, `--verify` (the
command that checks a worker's task before it counts as done, with the checks the folder's build files suggest), the team and trust options,
and `--allow` (actions answered yes in advance, with examples to click and a check of the form of a rule that was typed; read-only in `bypass` and `yolo`, which never ask, and the rules are kept for another mode and not sent). The budget starts at `off`, no limit: the cap that a team has in the terminal where nobody chose a budget (`swarm.budget_usd`, $50 built in) is not applied to the sessions of the page, unless `sleipnir web --budget-usd` or the configuration sets a budget, and the command line under the dialog sets `SLEIPNIR_SWARM_BUDGET_USD=0` for a team so that it means the same. When the
server answers that the project's own files need trust, for a new session and for a resume alike, the trust dialog lists every
file it named, with their count and size, and each path that could not be read; its yes stays disabled until that list is on
screen and repeats the request with the confirmation of exactly that list. A project that could not be read whole is trusted for
that session only. A team's isolation that neither the person nor `sleipnir web --isolation` nor `swarm.isolation` chose follows the folder:
`worktree` in the root of a git repository with a commit, or in a folder that git init will make one, else `none`. The team options
include `manager answers routine questions` (`--manager-approves`, on by default; only an off is sent). `compaction at` (`--compact-at`)
is a slider from 10 to 90 percent of each model's context window, with what its value means beside it: it starts at
`sleipnir web --compact-at`, else at the share a session takes without the flag (`cache.compact_at_percent`, else 25%, or the
configuration's token limits), and is sent, and shown in the command line, only when it differs from that share; `default` takes
it back. Once the server has made
the session, its tab is in front at once, before its first state arrives; a goal, a line or a command sent to it while it starts is
sent again, after 0.5, 1, 2 and 4 seconds and then every 5, until it has started (for at most two minutes), and the Workspace waits
for it in the same way without a message. When the server does not start the session, the dialog opens again as it was, with the reason under its
title; an allow rule typed but not added, and a role and model chosen but not added, are part of what starts. The dialog's
command line quotes every value for a POSIX shell. `↺ Resume` continues a recorded session; a recorded session can also be
opened read-only, to replay it, or followed while another process writes it; its first 400,000 events are read, and the banner
says when the log has more. With no session hosted, the page opens the New session dialog.

The left rail switches views: the Cockpit (the team as a horse whose eight legs are the eight roles of the team, the stalls, the
gantt, the task board, the merge queue, mail and the governor), the Workspace (Files, Changes, Checkpoints), Cache, Mail,
Board, Replay, Sessions, Schedule, Tools and Settings. The Radio rail on the right is the conversation with the manager, which is the
only agent the person talks to, and a read-only feed of what the team does. Agent drawers, opened from a stall or a gantt row,
are read-only.

The connection chip in the session strip shows the server's address. While the stream is down it reads `reconnecting` (the
browser reconnects by itself), `the server stopped` after the server said it is stopping, or `disconnected`; a new server run
requires the new address. A tab whose state could not be loaded says so in its banner and is fetched again (after 1, 2, 4 and
8 seconds, then every 15). A message is kept: one the server refuses comes back into the message box. Error toasts stay until
they are clicked away or for 12 seconds (the pointer over one pauses that); the last five stay on screen.

Keys: an arrow, Home or End on the session tabs, the views rail, a menu or a list moves there and nowhere else; a key that an
input method is composing with (Enter choosing a candidate) never sends, submits or runs a shortcut. A screen reader hears a
question once when it arrives, never its ticking wait time, and a toast once.

### Large teams

A team has at least eight workers and no upper size but the one `swarm.max_workers` sets: 32 workers, four of each role, start
from the New session dialog as any other team (`manager + 32 workers · your team`). In the dialog a role's row has a stepper for its
number of workers and one model for all of them; with more than one worker, its `▸` opens one model per slot, so that two backends
can run on one model and two on another, and the row's own model then sets all the slots again. The request carries the team slot
by slot, and the command line under the dialog has one `--role-model role=model` per slot.

The horse has eight legs whatever the size of the team, and a leg is a role: architect, scout, backend, frontend, devops, tester,
docs and reviewer in that order, and a project's own roles after them, eight to a round (a ninth role shares the first leg and its
label says `+1`). A leg is labelled with its worker when its role has one, else with the role's short name and count (`be ×4`); it
moves with the busiest of its workers, a role with no worker started stands as a free leg, a role the team has no slot of stands
empty (`no slot`), and pointing at a leg or focusing it lists its workers and their states. The terminal's horse has the same legs.
Above twelve workers the stalls are drawn in role bands, one line per worker, each band naming its count and its models; the gantt
draws one lane per worker inside a band per role when a row would be thinner than 11 pixels.

On the task board every task is in the column of its own status, and a column's count is the number of its tasks. A subtask in its
parent's column is drawn inside the parent's card, open in todo, running and verify and folded in merged, behind a toggle that says
how many it holds (`▾ 2 subtasks`); a subtask in another column is a card of that column marked with its parent (`↳ T124`), and the
parent's card says where the others are (`+ 6 in todo · 1 merged`). A failed task stays in todo with its `✗` and is counted apart
(`3 + 1 ✗`). Past eight merged tasks, the merged column is one line per task (id, owner, title, time of the merge), the newest merge
first, and an empty column is drawn narrower. The Cockpit's merge queue names the twelve newest merges and counts the earlier ones.
The Board view's dependency graph draws every task when they fit its panel; otherwise the merged tasks that no unfinished task waits
on are one node (`✓ 118 merged`), a column taller than the panel is wrapped into several, and a graph that still does not fit is
drawn at full size and scrolls. Two nodes never overlap.

### Every page at a glance

Each page has a title, one sentence of what it is for and one primary action; empty lists say what will appear and
what starts it; internal names (the cache layers G0 to G5, flags such as `--isolation`) are secondary text. The rail
groups the pages into Work, Inspect and Manage, and a page whose plugin is off is left out of it (docs/PLUGINS.md).
Screenshots of each page at 1440 and 390 pixels wide, beside the page as it was before these rules:

| Page | Before | After |
|---|---|---|
| Cockpit | [before](media/web/before/cockpit.png) | [1440](media/web/after/cockpit-1440.png) · [390](media/web/after/cockpit-390.png) |
| Board | [before](media/web/before/board.png) | [1440](media/web/after/board-1440.png) · [390](media/web/after/board-390.png) |
| Changes | [before](media/web/before/changes.png) | [1440](media/web/after/changes-1440.png) · [390](media/web/after/changes-390.png) |
| Files | [before](media/web/before/files.png) | [1440](media/web/after/files-1440.png) · [390](media/web/after/files-390.png) |
| Checkpoints | [before](media/web/before/checkpoints.png) | [1440](media/web/after/checkpoints-1440.png) · [390](media/web/after/checkpoints-390.png) |
| Merge | [before](media/web/before/merge.png) | [1440](media/web/after/merge-1440.png) · [390](media/web/after/merge-390.png) |
| Mail | [before](media/web/before/mail.png) | [1440](media/web/after/mail-1440.png) · [390](media/web/after/mail-390.png) |
| Cache | [before](media/web/before/cache.png) | [1440](media/web/after/cache-1440.png) · [390](media/web/after/cache-390.png) |
| Replay | [before](media/web/before/replay.png) | [1440](media/web/after/replay-1440.png) · [390](media/web/after/replay-390.png) |
| Sessions | [before](media/web/before/sessions.png) | [1440](media/web/after/sessions-1440.png) · [390](media/web/after/sessions-390.png) |
| Plugins | new | [1440](media/web/after/plugins-1440.png) · [390](media/web/after/plugins-390.png) |
| Tools | [before](media/web/before/tools.png) | [1440](media/web/after/tools-1440.png) · [390](media/web/after/tools-390.png) |
| Settings › Models & roles | [before](media/web/before/settings-models.png) | [1440](media/web/after/settings-models-1440.png) · [390](media/web/after/settings-models-390.png) |
| Settings › Team & run | [before](media/web/before/settings-run.png) | [1440](media/web/after/settings-run-1440.png) · [390](media/web/after/settings-run-390.png) |
| Settings › Budget | [before](media/web/before/settings-budget.png) | [1440](media/web/after/settings-budget-1440.png) · [390](media/web/after/settings-budget-390.png) |
| Settings › Permissions & trust | [before](media/web/before/settings-permissions.png) | [1440](media/web/after/settings-permissions-1440.png) · [390](media/web/after/settings-permissions-390.png) |
| Settings › Connections | [before](media/web/before/settings-providers.png) | [1440](media/web/after/settings-providers-1440.png) · [390](media/web/after/settings-providers-390.png) |
| Settings › Skills & extensions | [before](media/web/before/settings-skills.png) | [1440](media/web/after/settings-skills-1440.png) · [390](media/web/after/settings-skills-390.png) |
| Settings › Configuration | [before](media/web/before/settings-config.png) | [1440](media/web/after/settings-config-1440.png) · [390](media/web/after/settings-config-390.png) |
| Settings › Appearance | [before](media/web/before/settings-look.png) | [1440](media/web/after/settings-look-1440.png) · [390](media/web/after/settings-look-390.png) |

Settings › Roles, Trust and MCP servers joined Models & roles, Permissions & trust and Connections; their old names
still open the page that holds their cards.

### Cards on every page

Every page is a grid of cards, twelve columns wide: the Cockpit, each tab of the Workspace (Files, Changes, Checkpoints, Merge),
Cache, Mail, Board, Replay, Sessions, Tools, the command runner, Doctor, Schedule and each page of Settings. Every panel of a page is
a card with a stable id (`cache.hit`, `settings.budget.cost`, `ws.main` ...), and its default layout is the arrangement the page had
before it was customised. The Cockpit's rows fill the height of the view: the horse, the manager's card and the stalls on top, the
gantt and the task board under them, and the merge queue, the latest mail and the governor in a short band. On the other pages a
card is as tall as what it holds until it is sized by hand, and the cards under it close up when it gets shorter; the panes that
fill a view (the mail list and its message, the runner, the file and its diff) keep the rows they are given.

`Customise`, at the top right of each page, unlocks that page; `Done` locks it again, and every page load starts locked. Unlocked, a
card is moved by dragging it, resized from its right edge, its bottom edge or its corner, and hidden with its `×`; a hidden card
keeps its place and waits in the `Hidden` tray of the toolbar, which restores it. `+ Add card` lists the page's hidden cards, the
cards of every other page (each drawn on this page by its own view, apart from the rest of its page) and every view of the left rail
whole, with an `open ↗` to show it full size. A card of another page leaves with its `×` and is offered again by `+ Add card`.
`Reset layout` puts back the page's default. Each card's `⋯` menu has `Hide`, `Reset size`, whether its height follows its content,
and, for a list, its density (`Comfortable` or `Compact`). A card that lands on others pushes them down. Each card keeps a minimum
size, and lays out for any width and height: the stalls take two, four or eight columns by the card's shape, the gantt fits its rows
to the card's height, the board's columns wrap, and lists scroll. While a page is unlocked the cards' contents do not take clicks, so
that a press arranges and never acts. With the keyboard, a card is a focus stop while unlocked: the arrows move it one cell, the
arrows with shift held resize it, and `Delete` hides it; a screen reader hears its new place and size.

On a screen up to 900 pixels wide (and in a view narrower than 620 pixels) the cards stack in one column, in their order on the
grid, and customising there hides, restores and reorders only: the up and down arrows, or `Move up` and `Move down` in the card's
menu, trade places with the neighbour. At that width a wide diagram (the dependencies, the mail between agents, the replay tape)
keeps the size it is drawn at and scrolls sideways. In the phone layout every small control takes a touch of at least 32 pixels
square.

The header and the Radio rail stay where they are, but their parts can be hidden: `Header & rail` in the toolbar of a page being
customised, `Customise header & rail` in Settings › Appearance and the rail's own `⋯` list the header's widgets (goal, budget, cache
hit, prefix warmth, team) and the status bar's line and model, each ticked while it shows, and the rail's manager pane and team
activity, which collapse to a line that opens them again or hide, and its composer, which hides.

The layout of every page is one document, kept for the person by the server in `~/.sleipnir/web-layout.json` (`/api/layout`,
docs/WEB-API.md) so that it follows them from one browser to another, and cached in the browser so that the page has it at once and
works without the server. Every change is kept in the browser at once and sent to the server a moment after the last one; at boot
the server's copy wins over the browser's unless the person changed something before it arrived. A stored layout that does not read,
or that a later version of the page cannot use, gives the default layout; the Cockpit layout of the first version (browser storage
only) is carried over, with the panels it had taken off hidden. Settings › Appearance has `Customise the Cockpit` and `Reset the
layout` for the Cockpit, `Export layout` (the document as a JSON file), `Import layout` (a file exported before, checked as a stored
one, after a confirmation) and `Reset all pages`; the command palette has `Customise the Cockpit`.

### The guided tour

The first time the page opens in a browser, once a session is on screen and no dialog is open, a tour explains the important parts
one at a time: the session tabs and `+ New`, the top bar (goal, budget, cache), the views rail, the horse and the stalls with their
free slots, the gantt and its replay controls, the task board and the merge queue, the conversation with the manager and the message
box (queued lines, steering, interrupting), questions and their keys, the Cache view and cache breaks, the Cockpit's cards and
Settings. Each step dims and blurs the page around the region it explains, scrolls it into view, and follows it when the window is
resized; a step about a card taken off the Cockpit is passed over. The page behind the tour takes no click and no key while it is
open.

`Next` or `→` steps, `Back` or `←` goes back, and `Skip tour` or `Esc` closes it. Finishing the tour, or skipping it with `Don't
show this again` ticked, keeps it from starting by itself again in that browser; a skip without the tick leaves it to start the next
time the page opens. It starts again on demand from the `Take the tour` button of the Help sheet (`/help`), `Take the tour` in the
command palette, and Settings › Appearance. It never starts by itself in a browser driven by automation
(`navigator.webdriver`). The terminal has no tour: its chat program shows `/help`.

### The live view and questions

The view follows the session as it happens: an event is drawn as soon as it arrives, wherever the pointer or the keyboard focus
is, and a tab that was in the background or hidden draws every row it missed, as rows, when it is shown again. Pointing at a stall
or another linked item highlights what it is linked to and changes nothing else. Space, and the controls of the gantt and the
Replay view, pause the view: a paused or scrubbed view is a replay of the session, and End or Esc goes back to live. Questions are
not held back: the question box, the `Needs you` count and the tab's badge appear at once, also while the view is paused or
scrubbed, with a toast when the question is in a paused view or in another tab. A form being typed in stays as it is when the
team grows or the page fetches a tab again. The tab title shows `(? N)` while questions wait, unless Settings › Appearance turns
the badge off.

While a question waits in any session, every view shows `Needs you` in the session strip and at the top of the left rail, with
the number of open questions and how long the oldest has waited. A click on it makes that question's session the active one,
opens the Radio rail and puts the focus on the question; the `▾` beside it lists every open question of every session.
Settings › Appearance › Question notification (off by default, kept in the browser) sends one browser notification per question
that has waited the set number of seconds (30 unless changed) while the page is hidden or in the background; turning it on asks
the browser for permission, a click on the notification opens the question, and an answered question's notification closes.

A replay never stops answering silently: while the view is a replay, the question box carries a `Replay: answers are paused` banner
with `Go live`. A plain click on the Cockpit's gantt chart opens the agent's details and never starts a replay; its buttons, its
scrub bar and the replay keys do. A tape of the Replay view shown as a Cockpit card seeks only a replay already running.

### Approvals

A question names the agent, its task, what it wants to do (run a command, edit or write a file, apply a patch, fetch a page,
search the web), the working directory, the scope and why it asks; a change to a file is shown as a diff, which opens whole in a
larger view. The command, the reason and the diff are shown whole, each in a box of its own scroll in which every line wraps;
a long one is headed by its number of lines and bytes and a `Show the end` button. The command, the rules, the path, the scope
and the reason are drawn left to right in the order they are written, so a right-to-left word keeps its place, and every
character outside printable ASCII is marked, with its code point in the mark's title and a count under the command; a
directional formatting character acts on nothing beyond its own mark. Copying the text copies it as written. The answers are 1 yes, 2 yes and do not ask
again for that kind of request in this session (the exact rules it adds are listed under it; offered only when it would remember
something, else the option is absent), and 3 no with an instruction for the agent; a build or test
command also offers allowing builds and tests for the session, which takes place 3 and moves no to 4. A question that closes without
an answer (canceled, timed out, the session closed) leaves a row that says so. A question the manager answers for a team
(`--manager-approves`) is never a question for the person: its decision joins one collapsed line in the conversation and above it in
the rail (`the manager approved N actions ▸`, or how many it approved and denied), which opens to the agent, the command and the
decision of each.
A question is answered with its buttons only, never with a key. The buttons work only after the keyboard has been quiet for 0.8
seconds since the question appeared on screen or since the last key; text typed meanwhile goes to the message box and never
answers. A question that arrives while the rail is collapsed opens it. The server also refuses an answer given within 350 ms. The `Needs you` inbox answers questions of every session under the same rule.

### Keys

| Keys | Action |
|---|---|
| `Enter` | send; while a turn runs, queue the line for after it (Ctrl+Enter, or Cmd+Enter, sends it to the running turn as a steer instead; each queued line has Send now, Edit and ×, and Up in an empty message box takes the last one back to edit) |
| `alt+Enter`, `ctrl+J`, or a backslash at the end of the line before `Enter` | a new line |
| `↑` `↓` | in the message box: the lines sent before |
| `ctrl+R` | in the message box: search the lines sent before |
| `/`, `@` | in the message box: commands of the session; a path of the project |
| `Tab`, `Enter`, `↑` `↓`, `Esc` in a menu | complete, choose, close (commands, paths, permission mode, palette) |
| `shift+Tab` | in the message box: mode default → accept-edits → plan (never bypass or yolo) |
| `Esc` | close what is open (a dialog, the inbox, a menu, the drawer); in a field, leave it |
| `←` `→`, `Home`, `End`, `Space`, `Enter`, `F2`, `Delete` | on a focused session tab: move between tabs, open, rename, close |
| `↑` `↓`, `Home`, `End` | on the focused views rail or the rail's handle: move along it |
| `←` `→` `↑` `↓`, `shift+←` `shift+→`, `Delete` | on a card of the unlocked Cockpit: move it one cell (with shift held, resize it), remove it |
| `→` `←`, `Esc` | in the guided tour: the next or the previous step; skip the tour |

The page has no keyboard shortcuts: a key typed without the focus in the message box does nothing of the page's (it once changed
the view, answered a question, paused the timeline or closed the session, which a person typing before clicking the message box
did not mean). Views, questions and commands are buttons, and the keys are those of the message box and of the widget that has
the focus.

Changes to the page are checked in a real browser at narrow and wide sizes, with the screenshots opened and read, as terminal
changes are. See [Building](BUILDING.md).
