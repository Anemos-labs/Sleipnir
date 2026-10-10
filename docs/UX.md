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
each turn and can request another pass. `/goal` shows its state and plan.

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
has no session, or whose session has ended (24 hours, logout, or a rotation of the token), shows a short page that says to
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
  again. Outside the New session dialog, a dangerous mode is confirmed by typing its name; in that dialog, choosing the mode is
  the confirmation. A confirmation covers what was shown: when an approved tool server's entry changed after its card was read,
  nothing is confirmed, and the card shows the entry as it is now for a new approval.
- Names in a session log (agents, tasks, questions) are shown only when they are plain words (letters, digits, `.`, `_`, `:`
  and `-`, at most 64); any other name is shown as `invalid-id`.
- The page sends no request to any other origin; the server's Content-Security-Policy would block it.

### Sessions and views

One page shows every session the server hosts, one tab each in the session strip; a tab keeps running, and can ask questions,
while another is in front. `+ New` starts a session with every chat flag, seeded from the flags `sleipnir web` was started
with; its directory is one of the server's projects, never a free path, and the directory list names a project's trust when it
is not trusted (`not trusted`, `changed since trusted`, `partial` when not every file could be scanned, `unreadable`). When the
server answers that the project's own files need trust, for a new session and for a resume alike, the trust dialog lists every
file it named, with their count and size, and each path that could not be read; its yes stays disabled until that list is on
screen and repeats the request with the confirmation of exactly that list. A project that could not be read whole is trusted for
that session only. When the server does not start the session, the dialog opens again as it was, with the reason under its
title; an allow rule typed but not added, and a role and model chosen but not added, are part of what starts. The dialog's
command line quotes every value for a POSIX shell. `↺ Resume` continues a recorded session; a recorded session can also be
opened read-only, to replay it, or followed while another process writes it; its first 400,000 events are read, and the banner
says when the log has more. With no session hosted, the page opens the New session dialog.

The left rail switches views: the Cockpit (the team as a horse whose eight legs are the first eight workers, the stalls, the
gantt, the task board, the merge queue, mail and the governor), the Workspace (Files, Changes, Checkpoints), Cache, Mail,
Board, Replay, Sessions, Tools and Settings. The Radio rail on the right is the conversation with the manager, which is the
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

### Hold and catch-up

The view keeps its own clock. While the pointer is over the conversation, keyboard focus is in it, or the hold is pinned (click
the hold chip, or Space over the conversation), the view stops: nothing is appended, scrolled or typed, and the chip counts what
arrived. Pointing at a stall or another linked item slows the view. On release, what is older than 18 seconds is folded into one
digest row and the rest replays at up to six times speed. Questions are not held back: the question box, the `Needs you` count
and the tab's badge appear at once, also while the view is held, paused or scrubbed, with a toast when the question is behind the
hold, in a paused view or in another tab. A form being typed in stays as it is when the team grows or the page fetches a tab
again. The tab title shows
`(? N)` while questions wait, unless Settings › Appearance turns the badge off.

### Approvals

A question names the agent, its task, what it wants to do (run a command, edit or write a file, apply a patch, fetch a page,
search the web), the working directory, the scope and why it asks; a change to a file is shown as a diff, which opens whole in a
larger view. The command, the reason and the diff are shown whole, each in a box of its own scroll in which every line wraps;
a long one is headed by its number of lines and bytes and a `Show the end` button. The command, the rules, the path, the scope
and the reason are drawn left to right in the order they are written, so a right-to-left word keeps its place, and every
character outside printable ASCII is marked, with its code point in the mark's title and a count under the command; a
directional formatting character acts on nothing beyond its own mark. Copying the text copies it as written. The answers are 1 yes, 2 yes and do not ask
again for that kind of request in this session (the exact rules it adds are listed under it; offered only when it would remember
something, else the option is absent and the key 2 does nothing), and 3 no with an instruction for the agent; a build or test
command also offers allowing builds and tests for the session, which takes key 3 and moves no to 4. A question that closes without
an answer (canceled, timed out, the session closed) leaves a row that says so.
Esc is no. The buttons and the keys work only after the keyboard has been quiet for 0.8 seconds since the question appeared on
screen or since the last key; text typed meanwhile goes to the message box and never answers. A question that arrives while
the rail is collapsed opens it; an answer key pressed while the question is not on screen opens it and answers nothing. The
server also refuses an answer given within 350 ms. The `Needs you` inbox answers questions of every session under the same rule.

### Keys

| Keys | Action |
|---|---|
| `Enter` | send |
| `alt+Enter`, `ctrl+J`, or a backslash at the end of the line before `Enter` | a new line |
| `↑` `↓` | the lines sent before; outside the message box, the agent the views are about |
| `ctrl+R` | search the lines sent before |
| `/`, `@` | commands of the session; a path of the project |
| `Tab`, `Enter`, `↑` `↓`, `Esc` in a menu | complete, choose, close (commands, paths, permission mode, palette) |
| `shift+Tab` | mode default → accept-edits → plan (never bypass or yolo) |
| `Esc` | release the hold, close what is open, answer no, leave a field or a replay, then interrupt the turn |
| `ctrl+C` | discard the line; during a turn, interrupt it; twice at an empty prompt, close the session |
| `ctrl+D` | in an empty message box, or with nothing focused, close the session, asking first as its tab's close does; with text in the box, the browser's own |
| `1` `2` `3` `4` | answer the question in front, once the keyboard is quiet; 2 only where it would remember something, 4 only for a build or test command |
| `ctrl+K` | the command palette |
| `ctrl+T` `alt+T`, `ctrl+G` `alt+G` | Cache, Cockpit; a browser may keep ctrl+T and ctrl+G, the alt keys reach the page |
| `ctrl+O` | expand or collapse the output of the tools in the conversation |
| `g` then `c` `r` `f` `h` `k` `q` `a` `m` `b` `p` `s` `t` `e` | a view: Cockpit, Radio, Files, Changes, Checkpoints, Merge, Cache, Mail, Board, Replay, Sessions, Tools, Settings |
| `o` `c` `m` `b` `r` `s` `,` `.` | a view: Cockpit, Cache, Mail, Board, Replay, Sessions, Settings, Tools |
| `alt+1` to `alt+9` | the nth session |
| `Space`, `←` `→`, `shift+←` `shift+→`, `Home`, `End`, `+` `=` `-` | pause, seek (a minute with shift), start, live, replay speed |
| `f` | in the Cache view, follow the agent that was answered last |
| `F2`, `Delete` | on a focused session tab: rename, close |
| `?` | the keys and the commands |

Changes to the page are checked in a real browser at narrow and wide sizes, with the screenshots opened and read, as terminal
changes are. See [Building](BUILDING.md).
