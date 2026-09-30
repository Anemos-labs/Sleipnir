package inspect

import (
	"time"

	"github.com/reee344/sleipnir/internal/core"
)

// secRec is one pinned-layer section as model.request records it.
type secRec struct {
	name   string
	hash   string
	tokens int
	bp     bool
}

// req is one model call. It is created by model.request and completed by
// model.response (or model.error).
type req struct {
	id       string
	seq      uint64
	rev      uint64
	agent    *agent
	role     string
	kind     string
	model    string
	provider string
	t        time.Time

	// Recipe.
	sections     []secRec
	g0Sig        string // tools hash + system hashes: equal signature, equal G0 bytes
	toolsHash    string
	systemHashes []string
	hotHash      string
	threadFrom   int64
	threadTo     int64
	cacheKey     string
	prefixKey    string
	breakpoints  int
	sharedBlocks int
	sharedTokens int
	renderer     string
	wire         string
	ratio        float64 // bytes per token, when a layer blob revealed it
	g0Tok        int     // -1 unknown
	hotTok       int     // -1 unknown

	// Layer comparison with the agent's previous main request.
	changed    uint8 // bit i set: layer Gi's own bytes changed
	anyChange  bool
	first      int // index of the first changed layer, -1 when none
	rebase     string
	undeclared bool
	epoch      int
	isFirst    bool // first main request of the agent
	hadPrev    bool

	// Outcome.
	done     bool
	failed   bool
	err      string
	usage    core.Usage
	prompt   int
	hit      float64
	expected int
	anomaly  bool
	cost     float64 // reported cost_usd, else priced
	reported bool
	gateway  bool
	priced   float64
	noCache  float64
	naive    float64
	naiveCtx int
	ttfb     int64
	total    int64
	stop     string
	side     bool

	foldedAtReq int64
}

// toolAgg aggregates one tool's calls for one agent.
type toolAgg struct {
	calls, errors, truncated int
	totalMs, maxMs, chars    int64
}

// openCall is a tool.call waiting for its tool.result.
type openCall struct {
	name  string
	t     time.Time
	board *boardCall // parsed arguments of task/spawn calls, for the board reconstruction
}

// agent is one participant, keyed by id.
type agent struct {
	id, role, model, parent, task string
	spawned, last                 time.Time
	hasSpawn                      bool
	ended                         bool
	endState                      string
	evidence                      string

	reqs             []*req // retained window, in request order
	nMain, nSide     int
	nDone            int
	prompt           int64
	usage            core.Usage
	usd, noCache     float64
	naive            float64
	hitRead, hitAll  int64 // main requests only, for the agent's hit ratio
	ctxLast, ctxMax  int
	ctxSum           int64
	ctxN             int
	naiveCtxSum      int64
	commits          int
	netFolded        int64
	anomalies        int
	turns            int
	toolCalls        int
	toolErrs         int
	tools            map[string]*toolAgg
	open             map[string]*openCall
	openReq          int
	waiting          bool
	line             string
	lastTool         string
	lastToolAt       time.Time
	failedReqs       int
	mainSeen         int // main requests started (drives first)
	prevMain         *req
	foldedCum        int64
	naivePrev        int64
	naiveT           time.Time
	naiveAny         bool
	rebasePending    string
	pendingDrift     *anomaly
	ep               *compaction // open compaction episode
	lastCommit       *compaction // committed, waiting for the next main response
	lastPlan         *planNote
	lastThreadTokens int
}

// planNote remembers the planner's last statement for an agent.
type planNote struct {
	warm   *bool
	thread int
	reason string
}

// compaction is the mutable form of Compaction.
type compaction struct {
	Compaction
	startTS time.Time
	reqIDs  []string
}

// anomaly is the mutable form of Anomaly.
type anomaly struct {
	Anomaly
	agent *agent
}

// minuteBucket counts coordination activity within one minute.
type minuteBucket struct {
	board, mailSent, mailDeliv, spawns, leases, requests int
}

// boardCall is what the board reconstruction needs from a task/spawn call.
type boardCall struct {
	name    string // task | spawn
	action  string
	id      string
	title   string
	desc    string
	text    string
	role    string
	agent   string // spawn: reuse this idle worker
	deps    []string
	files   []string
	created string // id of the task this call created, once a board.op create was seen
}
