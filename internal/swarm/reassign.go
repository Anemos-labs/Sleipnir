package swarm

import "strings"

// reassignCard is the user turn a reused worker receives for its next task.
//
// A reused worker keeps its context and its warm cache, but its <my-notes>
// "assignment" was written once, at first spawn, and still describes the first
// task. Editing notes would cost a re-write of everything behind them, so the
// new brief, scope and dependencies travel in the turn itself; the harness folds
// a user turn into the instructions notes when it is compacted, so the new
// assignment survives, and the text says which assignment wins until then.
func reassignCard(t Task, agentID string) string {
	var sb strings.Builder
	sb.WriteString("New assignment. It replaces the assignment in <my-notes>: the previous task is finished, do not continue it.\n")
	sb.WriteString(taskCard(t, agentID, true))
	return sb.String()
}
