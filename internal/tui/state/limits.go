package state

import "time"

// The bounds of a State. A session of any length holds at most this much: when a cap is reached the oldest finished entry is
// evicted (agents, tasks, prefixes) or overwritten (the rings), and what could not be kept is counted in Stats.
const (
	// MaxAgents bounds the agents a State tracks. A swarm is 10 to 50 agents (a swarm runs at most 24 workers unless swarm.max_workers says otherwise); when the cap
	// is reached the agent that has been idle, done or failed the longest makes room for a new one, and a new agent is
	// dropped (Stats.Dropped) only when all MaxAgents are active.
	MaxAgents = 256
	// MaxTasks bounds the tasks of the board; it is the board's own limit (swarm.BoardLimits.MaxTasks).
	MaxTasks = 1000

	// FeedCap is the length of the feed, the ring of short human lines.
	FeedCap = 200
	// MailCap is the length of the ring of recent messages.
	MailCap = 100
	// MergeCap is the length of the ring of finished merge-queue entries; waiting entries are bounded by MaxMergeWaiting.
	MergeCap        = 64
	MaxMergeWaiting = 64
	// HistCap is the number of hit-ratio samples kept per agent (one per main request).
	HistCap = 256
	// MarkCap is the number of sparkline markers (anomalies, compactions, epochs) kept per agent.
	MarkCap = 64
	// CompactCap and AnomalyCap are the compaction commits and cache anomalies kept per agent; AnomalyLog is the length of the
	// session-wide ring of anomalies.
	CompactCap = 16
	AnomalyCap = 16
	AnomalyLog = 64

	// ActivitySeconds is the width of the activity matrix: the last 120 one-second buckets of event time.
	ActivitySeconds = 120

	// MaxSections bounds the sections kept from one request (the harness writes four), MaxBreakpoints the breakpoints (at most
	// four on Anthropic), MaxOpenTools the tool calls tracked as running per agent and MaxOpenRequests the model requests.
	MaxSections     = 8
	MaxBreakpoints  = 8
	MaxOpenTools    = 64
	MaxOpenRequests = 32

	// MaxLeasesPerAgent and MaxLeases bound the lease table (the swarm's own table holds at most 512 entries).
	MaxLeasesPerAgent = 16
	MaxLeases         = 512
	// MaxPrefixes bounds the prefix groups (one per distinct prefix_key), MaxRiders the agents named in one. The TTL entries are
	// one per prefix and per agent, so they are bounded by those.
	MaxPrefixes = 64
	MaxRiders   = 64
	// MaxPending bounds the permission questions waiting for an answer, PermLog the answered ones kept and MaxPermPaths the paths
	// kept of one question (the producer sends at most five).
	MaxPending   = 16
	PermLog      = 8
	MaxPermPaths = 5
	// MaxModels bounds the models whose prices are remembered (session.start lists at most 24).
	MaxModels = 64
	// MaxAlerts and MaxNotes bound what the board's alerts and pending notes keep (the board itself shows 8 and 48).
	MaxAlerts = 8
	MaxNotes  = 256
	// MaxFiles bounds the files named on one task, merge entry or scope; MaxDeps the dependencies of a task.
	MaxFiles = 16
	MaxDeps  = 16
	// MaxUnknownTypes bounds the event types counted by name in Stats.Unknown.
	MaxUnknownTypes = 64
)

// What a payload may cost to decode, and how long the strings kept from it may be.
const (
	// maxPayload is the largest event data that is decoded at all (events.MaxEventBytes is 32 MiB; a payload this size is data
	// that belongs in a blob). Tool calls are decoded with a narrow shape and are exempt: their input can legitimately be large.
	maxPayload = 1 << 20
	// maxToolPayload bounds a tool.call payload. Only a few short fields are read from it, the rest is skipped.
	maxToolPayload = 8 << 20

	textLine  = 160 // one line of a feed, a note, a reason
	textShort = 80  // a detail, a tool summary
	textID    = 64  // an id, a name, a label
	textPath  = 200 // a path or a command shown whole
	textLong  = 300 // a result, an evidence line, a goal
	// maxPanicText bounds the record of a panic a handler made (Stats.LastPanic): the value, the event, and the top of the stack.
	maxPanicText = 1600
	maxTokens    = 1 << 40
	smallCount   = 1 << 30
)

// DefaultTTL is the cache entry lifetime assumed when neither session.start nor the request says what the provider keeps a
// prefix for; a TTL that comes from it is flagged (TTLEntry.Default).
const DefaultTTL = 5 * time.Minute

// SavingsAssumption is the text the UI prints next to the saved dollars: the price table is the list price, not the bill.
const SavingsAssumption = "at list price"
