// Package translate turns the activity of one hosted session into the UI events of `sleipnir web`: the event vocabulary the page's
// reducer consumes, and nothing else. The event kinds, their fields and the classes of the stream frames that carry them are defined
// in internal/web/wire and described in docs/WEB-API.md. One Translator serves one generation of a tab; it implements
// seam.Translator.
//
// # Sources
//
// Three sources feed it. The agent.Sink it hands the session (Sink, NewSink) gives what the log does not hold: streamed text (say
// and stream, continued by more), tool calls as they start and end (an immediate state, the tool row with its output's first line,
// the code stream of a write, the diff of a file written), notices and retries. The session's log (Attach) gives everything else,
// folded by a per-tab internal/tui/state State: agent states (with the doing line of the running tool), token tables, hit ratios,
// prompt layers, the warm clock, the governor, tasks and their closures, the merge queue, mail, cache anomalies, compactions,
// supervision findings, handovers, checkpoints, the plan of the main agent and the standing goal and its verdicts. The host adds
// its own (Emit, Question, Answered): the person's messages, turns, steers, interrupts, questions and answers.
//
// A log another process writes is followed by polling (Follow; FollowDir publishes it for a read-only tab until the session ends), a
// recorded session is read once (Replay, the Replay view of a recorded tab, which opens read-only and plays its log), and a session
// resumed with a log of earlier runs has that history translated first, at t 0 with each event's real time in at, followed by the
// state the whole log folds to and a resumed row. Both read every row from the log: the answers of the main agent come from
// turn.append, tool rows from tool.call, tool.result and the output in the next turn.append.
//
// # Journal, keyframes, snapshots
//
// Every event gets the tab's next seq and a session-relative t (seconds since the run's start, milliseconds, never decreasing in seq
// order) and is kept in the journal and published as an ev frame with its class: critical, coalescable with a key, or ordinary
// (internal/web/wire and docs/WEB-API.md define the classes). The journal keeps the newest DefaultJournalEvents events or
// DefaultJournalBytes of JSON; what it evicts is folded into a mirror (a Go reduction of the page's world model, minus the transcript)
// whose keyframe stands in for it in a snapshot (Journal). Every 2,000 events it keeps a keyframe of the state at that point too
// (Seek), at most 8 MiB of them.
//
// # Bounds
//
// The sink queue holds at most DefaultSinkQueue calls and 8 MiB of text; a sink call never blocks: when the queue is full, text
// deltas are dropped first, the loss is counted and reported once with a fresh state of every agent. A streamed message is cut at
// 64 KiB, an event's text at 16 KiB, one-line fields at fixed caps in runes. The history of a resumed session keeps its newest
// DefaultHistory events and is not read from a log over 256 MiB. Open questions, open tool calls, pending notices and the other
// maps are bounded by the session's agents and tasks or by fixed caps. Per event the work is constant: the State is read through
// accessors that copy one agent or one task, never a whole snapshot. One goroutine runs per translator (two while a log is
// followed by polling), and Close stops them.
//
// # Text
//
// Model text, tool output, file names, mail and notices are data: they are sanitized for display (escape sequences, control,
// bidirectional and invisible characters removed), secret-shaped substrings are masked (internal/rl/redact, one redactor for the
// process whose memo is bounded at 64 MiB), and they are cut. Streamed text is masked in pieces cut at white space, so that a
// secret is never split across two of them. Nothing here ever reaches a prompt.
package translate
