package swarm

// runStart is what a launched run begins with: the brief the model reads first and, for a
// reused worker, the assignment card that takes the place of the one in its notes. It is
// handed to Agent.RunTask, so neither is ever pinned as the user's word (core.OriginTask).
// The zero value starts the run from the thread as it stands (a wake for mail).
type runStart struct {
	brief string
	card  string
	// kickoff marks the first run of a worker that was just spawned (not a reuse, not a
	// wake): the run for which SubagentStart hooks may add to the brief.
	kickoff bool
}

// reassignBrief is what a reused worker reads before its new card.
//
// A reused worker keeps its context and its warm cache, but its <my-notes> "assignment"
// was written once, at first spawn, and still describes the first task. Editing notes
// would cost a re-write of everything behind them, so the new brief, scope and
// dependencies travel in the turn itself as a task block: the harness replaces the
// assignment in the notes with it when the turn is folded away, and this text says which
// assignment wins until then.
const reassignBrief = "New assignment. It replaces the assignment in <my-notes> and includes any older unfinished tasks. Blocked tasks must be resumed before continuing; tasks no longer listed are not assigned to this run."

// reassignStart is the start of a run that gives an existing worker a new task: the
// brief above, then the card in the same form as the one a fresh worker's notes hold (an
// isolated writer's includes the isolation card, so the replaced assignment keeps it).
func reassignStart(snap *Snapshot, m *member) runStart {
	card := claimedCards(snap, m.id, nil)
	if m.tree != nil {
		card += isolationCard
	}
	return runStart{brief: reassignBrief, card: card}
}
