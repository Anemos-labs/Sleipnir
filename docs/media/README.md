# The mark

![Sleipnir](logo.png)

Sleipnir is Odin's eight-legged horse, and the mark is exactly that, drawn a little silly on purpose: a galloping horse with
**eight legs, one for each rider on the shared prefix**, each leg in the colour of a role (manager violet, backend and frontend
blues, tester green, reviewer amber, docs orange). The mane and the tail are the harness's own violet. The same colours colour
the roles in the terminal UI (`docs/UX.md`).

| File | Use |
|---|---|
| `logo.svg`, `logo.png` | the mark alone, on light backgrounds |
| `logo-dark.svg` | the mark alone, on dark backgrounds |
| `logo-wordmark.svg`, `.png` / `-dark.svg` | the mark with the name and the tagline, for headers |
| `favicon.svg` | the reduction for 16 to 64 px: a pale horse over eight bold leg bars (the detailed mark cannot be read that small) |
| `social-preview.png` | 1280 × 640, for the GitHub repository's social preview (Settings, Social preview: upload it there) |

Colours: ink `#24232e`, paper `#fbf8f1`, violet `#7048e8` (light) and `#b79cf9` (dark); the role colours are in `make_logo.py`.

Rules of thumb: keep clear space of one leg-width around it; do not recolour the legs, stretch it, add a fifth pair of legs or
remove one (eight is the point); do not use the wobble filter on anything else; below 48 px wide use `favicon.svg`.

Regenerate: `python3 make_logo.py && node render.mjs logo.html wordmark.html social.html favicon-test.html` (Python 3 and headless
Chromium through Playwright; the SVGs are plain paths plus one SVG filter for the hand-drawn wobble).

## The gallery

The recordings of the terminal interface are not drawings: each is the program's own screen, played from a recorded session. The
cockpit's are played from the event log of one session, `showcase/events.jsonl` (the shop demo: `sleipnir demo --scenario shop`, with
the paths of the machine that made it replaced); the chat's is the chat program itself, played from a transcript of a session,
`chat/transcript.jsonl` (below). `gallery.json` lists them (which screen, how big, which stretch of the session, at which seconds the
stills are taken), and the same manifest is read by `sleipnir replay --gallery`, by `scripts/record-demo.sh` and by the tests that
check the committed files.

| File | What it shows |
|---|---|
| `swarm.svg`, `swarm.png` | the swarm cockpit over the whole session: the shared prefix and its riders, mail, a stuck agent, the provider losing its cache, the merge queue sending a piece back |
| `cache.svg`, `cache.png` | the cache of one agent (be-2) from its start: layers, the clock, the hit ratio with its marks, a compaction while warm, the break and what it cost |
| `fold.svg`, `fold.png` | another agent's (be-1) compaction at a cold moment, after a build that outlasted the cache |
| `chat.svg`, `chat.png`, `chat-ask.png` | `sleipnir chat` at work for twenty seconds: a goal typed and sent, the answer streaming as markdown, tool lines (a read, a `go test` that fails, an edit as a diff, the tests passing), a permission question that a letter does not answer (it lands in the input box) and the key `1` does, the thread folding into one line, the provider losing its cache, and a Ctrl-C that cancels a turn and keeps the session. The stills are the question with the letter in the input box (`chat-ask.png`, 10.3 s) and the end of the first answer (`chat.png`, 17.5 s) |

The SVGs are animated with CSS only (no script: they play in a README and in any browser), identical frames are merged, and the file is a
function of the log (or transcript) and the manifest, so `scripts/record-demo.sh --check` (and `go test ./internal/tui/app`) fail when a
change to a screen or a widget has not been recorded again. The PNG stills are taken from the SVGs with headless Chromium
(`scripts/svg2png.mjs`). To make a new session: `scripts/record-demo.sh --new-session` (about twenty seconds, no key), then read the diff of
the pictures and commit.

### How the chat is recorded

A log of events cannot say what a person typed, and the chat is the program that takes the typing; so its recording is not the cockpit
over a log but the chat program itself. It is built in two steps, each of which is real.

1. **The session** (`scripts/record-demo.sh --new-chat`, which runs the hidden command `sleipnir chat-record`; about half a minute, no
   key, no network, and the `go` command). A session is made the way `sleipnir chat` makes one (the same options, the same host, the
   same subscription to the log): the harness, the tools (the real `go test` runs in a small Go project and fails, then passes), the
   permission engine, the cache planner, the accounting. The model is a script (`internal/demo/chat.go`) served by the repository's mock
   endpoint, and the person is a script too: it types a goal, presses `y` at the question (which answers nothing: the letter lands in the
   input box, and the dialog's own table, `AnswerFor`, is asked first whether a letter answers), deletes it and answers with the key `1`,
   types another goal and presses Ctrl-C. What the program would have been given is written down in order (the keys, what the session says through its sink and its
   prompter, the events of its log, how each turn ended) as `chat/transcript.jsonl` (the format is documented in
   `internal/tui/app/chat_transcript.go`). The command refuses to write the transcript if the session did not tell the story the script is
   for (the tests failing and then passing, one question answered after a letter had not, one compaction, one cache break, a cancelled
   turn), or if it names a path of the machine that made it.
2. **The picture** (`scripts/record-demo.sh`, and nothing else). The chat program is run on a virtual clock in a terminal emulator and
   given the transcript record by record, one at a time, drawing once for each; its screens are captured and written as the SVG. Nothing
   reads a clock of the machine, so the same transcript is the same SVG, byte for byte, on any machine and at any load.

The time of the transcript is designed, not measured: the session is recorded with no latency at all (so that a busy machine cannot stretch
it) and `cmd/sleipnir/chat_pace.go` then says when each thing happened, from what it was: the model's first token comes after 600 ms and a
tenth of a millisecond for each token of the prompt that the provider had not cached, its words come at about 530 characters a second, a key
every 55 ms, the real time of a tool (held between 1 ms and 2.5 s). The order of everything, what is said and every number the harness works out
from it (tokens, cache hits, the cost, what the compaction saved) are the session's own, with one exception: when a compaction starts, its
goroutine and the main thread race to put their next events into the log, and the pace puts the compaction's right after its plan, so that the
same session is the same story twice (a test records twice and compares). A new recording is a new session, so its real timings (the `go`
command's own, in the output of the tests and in the time a tool line says) differ from the committed one's; the file is committed so that
the picture is drawn from the same session every time.
