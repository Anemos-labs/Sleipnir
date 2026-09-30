package events

// Event type names. The names are the stable contract between producers, the
// dashboard, the offline simulator and the training-data exporters; payload
// structs live next to the producer that owns them.
const (
	// Session lifecycle.
	TypeLogOpen      = "log.open"
	TypeSessionStart = "session.start"
	TypeSessionEnd   = "session.end"

	// Agents.
	TypeAgentSpawn = "agent.spawn"
	TypeAgentState = "agent.state"
	TypeAgentEnd   = "agent.end"
	// TypeAgentSnapshot references a blob with everything needed to resume an
	// agent (thread, notes, spine, counters); TypeAgentRestore records a resume.
	TypeAgentSnapshot = "agent.snapshot"
	TypeAgentRestore  = "agent.restore"

	// Transcript. turn.append is the canonical record of every turn an agent's
	// thread ever contained, including turns later compacted out of the prompt.
	TypeTurnAppend = "turn.append"

	// Model traffic. model.request stores a recipe (layer hashes + turn range +
	// hot block hash), not the whole prompt; model.response the outcome.
	TypeModelRequest  = "model.request"
	TypeModelResponse = "model.response"
	TypeModelError    = "model.error"

	// Tools and permissions.
	TypeToolCall   = "tool.call"
	TypeToolResult = "tool.result"
	// TypeToolJob is emitted when a background shell job starts and when it ends
	// (status, duration, exit code); the payload is shell.JobEvent.
	TypeToolJob    = "tool.job"
	TypePermAsk    = "perm.ask"
	TypePermDecide = "perm.decide"

	// Cache engine.
	TypeLayerCommit   = "layer.commit"
	TypeCachePlan     = "cache.plan"
	TypeCacheAnomaly  = "cache.anomaly"
	TypeCachePrewarm  = "cache.prewarm"
	TypeCacheKeepAlv  = "cache.keepalive"
	TypeCompactPlan   = "compact.plan"
	TypeCompactPatch  = "compact.patch"
	TypeCompactCommit = "compact.commit"
	TypeCompactReject = "compact.reject"
	TypeRecall        = "recall"

	// Swarm coordination.
	TypeBoardOp     = "board.op"
	TypeMailSend    = "mail.send"
	TypeMailRoute   = "mail.route"
	TypeMailDeliver = "mail.deliver"
	TypeMailAck     = "mail.ack"
	TypeLease       = "lease"
	TypeGovernor    = "governor"

	// Supervision of the manager. swarm.hold: a batch run's manager gave a final
	// answer while the board still held unfinished work and was sent back to it
	// (payload: reason). swarm.unfinished: the run ended anyway (the veto bound was
	// reached) and reported what was left. swarm.wake: a finished worker, a
	// submission or mail woke an idle manager of an interactive session (payload:
	// n, note); swarm.wake.paused: the bound on automatic turns was reached.
	TypeSwarmHold       = "swarm.hold"
	TypeSwarmUnfinished = "swarm.unfinished"
	TypeSwarmWake       = "swarm.wake"
	TypeSwarmWakePaused = "swarm.wake.paused"

	// Mailman mode (swarm.mailman). mail.route: a worker's message was accepted by the
	// router and handed to the mailman's ledger instead of being delivered (payload: id,
	// from, to, kind). mail.digest: the mailman's digest for one recipient was delivered
	// (payload: id, to, mailman, parcels = the original message ids, senders, frame).
	// mail.direct: messages were delivered directly, as the router would have without a
	// mailman, and why (payload: reason, n, ids). mail.batch: the mailman was asked to
	// digest a batch of parcels (payload: batch, mailman, recipients, parcels).
	// mail.mailman: the mailman is down for a while (or back) (payload: state, reason).
	TypeMailDigest   = "mail.digest"
	TypeMailDirect   = "mail.direct"
	TypeMailBatch    = "mail.batch"
	TypeMailmanState = "mail.mailman"

	// Worktree isolation (swarm.isolation = "worktree"). internal/workspace emits
	// these (its Event* constants are the same strings): one tree per writer, and a
	// serial merge queue that verifies every integration.
	TypeWorkspaceCreate = "workspace.create"
	TypeWorkspaceRemove = "workspace.remove"
	TypeWorkspacePrune  = "workspace.prune"
	TypeWorkspaceCommit = "workspace.commit"
	TypeWorkspaceReset  = "workspace.reset"
	TypeMergeQueued     = "merge.queued"
	TypeMergeMerged     = "merge.merged"
	TypeMergeConflict   = "merge.conflict"
	TypeMergeVerifyFail = "merge.verify_failed"
	TypeMergeRolledBack = "merge.rolled_back"
	TypeMergeRejected   = "merge.rejected"
	TypeMergeFastFwd    = "merge.fast_forward"
	// task.merge is the swarm's account of one submission to the queue: which task,
	// which outcome (merged, empty, conflict, verify_failed, rejected, error), the
	// integration commit and the files; swarm.integration is the end of the run: the
	// integration branch and whether its result reached the user's checkout.
	TypeTaskMerge        = "task.merge"
	TypeSwarmIntegration = "swarm.integration"

	// Humans.
	TypeUserInput = "user.input"
	TypeUserSteer = "user.steer"

	// Outcome signals used as training labels.
	TypeOutcome = "outcome"
)
