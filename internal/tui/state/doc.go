// Package state is the data side of Sleipnir's terminal UI: the session as a pure function of its event log.
//
// Every screen (chat, swarm progress, watch, replay, the headless recorder) draws what this package holds, and this package
// learns it from one place, the stream of events.Event that the harness already writes (docs/UX.md, "The UI is a function of the
// event log"). There is no second source of truth and no instrumentation added to the agent: State.Apply folds one event in,
// State.Snapshot hands a renderer an immutable copy. The same fold runs over a live subscription, over a log being followed
// (Tail, Follow), over a recorded log at any speed (Replay) and over a whole file at once (Fold).
//
// # What is held
//
// Session (model, provider, cwd, start, renderer version, swarm or single), Agents (status, current tool, tokens, cost, requests,
// errors, retries, compactions, lease and scope, stuck phase, cancelled runs, the permission questions it waits on, hit-ratio
// history), each agent's prompt stack (the sections of its latest request, the cache reads its latest response reported, the
// expected read and whether it missed, anomalies with the layer that diverged, compaction commits with before and after tokens and
// whether the moment was cold, epochs), Totals and Savings, the cache history of an agent for a sparkline, TTL clocks per prefix,
// the task Board, the MergeQueue, Mail, Leases, the Governor, the Perms (the permission questions that wait for an answer, who
// asked what, and the last answers with who gave them), a Feed of short human lines and an Activity matrix for the swarm gantt.
//
// # Rules the package keeps
//
//   - Pure. No time.Now, no I/O (except the loaders in load.go and replay.go, which read a log), no goroutines, no global state.
//     Time comes from event timestamps, and from the explicit now argument of SnapshotAt and TTLRemaining. Two States that
//     were fed the same events hold the same data, however the events were chunked, and however many times a State was snapshotted
//     meanwhile.
//   - Tolerant. An unknown event type, a payload that is not JSON, a field of the wrong type, a missing field, an event from the
//     future or one that arrives twice is ignored or partly applied, counted in Stats, and never panics (a handler that does, which
//     is a bug, is contained and recorded: Stats.Panics and Stats.LastPanic). Text that came from a model, a tool or a peer is
//     data: it is made one line, stripped of control and format characters and cut to a bounded length before it is stored, so a
//     renderer that prints it cannot be made to print an escape sequence by it.
//   - Bounded. Nothing grows with the length of the session. The caps are the exported constants of limits.go; a session of
//     millions of events holds at most MaxAgents agents, MaxTasks tasks and the rings (feed, mail, merge queue, history,
//     marks, anomalies, compactions, activity), and evicts the oldest finished entry first when a cap is reached.
//   - Ordered by the log. An event whose Seq is not greater than the highest one applied is a duplicate or a replay and is
//     ignored (Stats.Stale); events without a Seq are applied as they come. Events about an agent or a task that has not been
//     introduced yet (the log writes an agent's first board and state events before its agent.spawn) create it.
//   - Derived, never invented. What the events do not say is left empty or flagged (an unknown price is an unknown saving, an
//     unknown TTL is a flagged default of five minutes, a session that does not log its permission mode has no Mode), and the
//     places where the package derives a figure (task state, agent status, busy level, saved dollars) say how in their comments.
//
// # Concurrency
//
// A State is safe for concurrent use: Apply takes a write lock, Snapshot and the accessors a read lock. The Snapshot they return
// shares nothing with the State and is immutable by convention (its slices are not to be modified by the caller).
//
// # Layout
//
// state.go holds State and the dispatch of Apply; agent.go, tools.go and model.go fold the lifecycle, tool and model events of an
// agent; cache.go the anomalies, compactions and layer commits; prices.go and prefix.go the price table, the prefix groups and
// the TTL clocks; board.go, mail.go, merge.go and session.go the swarm's board, mail, merge queue, leases, governor, permissions and
// the session itself; activity.go the gantt; snapshot.go the immutable copy and the focus helpers; types.go and types_swarm.go
// the public types; limits.go the caps. load.go (Fold), replay.go (Replay) and tail.go (Tail, Follow) read logs. Package
// statetest builds event sequences by hand and holds the recording of a real session for tests.
//
// # Where the knowledge comes from
//
// The shapes of the payloads are those of their producers (internal/agent agent.go, request.go, exec.go, compact.go; internal/swarm
// board.go, mailbox.go, mailman.go, leases.go, isolate.go, lifecycle.go; internal/workspace events.go; internal/session session.go
// and permaudit.go), as internal/inspect folds them for the web dashboard; this package imports none of them, only internal/events
// (the envelope) and internal/cost (the price table that turns cache reads into saved dollars).
//
// Three of those shapes are worth knowing before reading the code. perm.ask and perm.decide carry no id, so a question and its
// answer are paired by who asked, the tool, the command and the paths, oldest first, and a perm.decide with no question before it
// is a request that a rule refused without asking (by "policy"). agent.cancel is the last thing a cancelled run writes: it ends the
// run, and it says that the model.error just before it, whatever words the transport used, was the cancellation and not a failure;
// its phase "tools" is in practice reported as "between" (see CancelBetween). A tool.call whose name the harness repaired carries
// the tool that ran as "as", and that is the one shown.
//
// The tests check what the State makes of the logs of real sessions, recorded hermetically by the repository's own scripted teams
// against its mock endpoint (the demo; a session that compacts, sends mail, gets stuck, is rate limited and meets a cold cache; one
// with worktree isolation and the merge queue; one with the mailman; and four of one agent in the default permission mode: a person
// at the prompt, no one to ask, a run cancelled while a question waits and one cancelled in a request), against an oracle that
// counts the same log the plain way, and pin it with golden files (testdata/golden). Where the events do not carry a figure a screen would want (the permission mode, the budget until it is
// spent, the phases of a merge, the prompt layers that requests do not size), the type that would hold it says so in its comment,
// and the field is left empty; where the State estimates one (the size of a message in tokens), the comment says that instead.
package state
