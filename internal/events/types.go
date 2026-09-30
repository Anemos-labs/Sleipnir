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

	// Humans.
	TypeUserInput = "user.input"
	TypeUserSteer = "user.steer"

	// Outcome signals used as training labels.
	TypeOutcome = "outcome"
)
