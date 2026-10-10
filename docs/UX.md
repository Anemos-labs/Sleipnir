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
  for that action only.
- The page sends no request to any other origin; the server's Content-Security-Policy would block it.

Changes to the page are checked in a real browser at narrow and wide sizes, with the screenshots opened and read, as terminal
changes are. See [Building](BUILDING.md).
