package scan

import "strings"

// Run is one deep scan as scan.json holds it. Slices keep the order things were added in and ids are never reused, so the
// file and every report made from it are the same bytes for the same history.
type Run struct {
	Slug string `json:"slug"`
	// Name is what the scan reads, as a report's title gives it: the project's folder name.
	Name string `json:"name,omitempty"`
	// Commit is the commit the scan reads: every citation is checked against it, whatever the work tree holds later.
	Commit  string `json:"commit"`
	Depth   string `json:"depth"`
	Focus   string `json:"focus,omitempty"`
	Created string `json:"created,omitempty"`
	// Base is set when the scan reviews a change: the commit the change starts from (Commit is where it ends), and Changes the
	// lines it added or modified. A review's leads each have a step on one of those lines.
	Base    string   `json:"base,omitempty"`
	Changes []Change `json:"changes,omitempty"`
	// Previous is the scan this one reruns: the same code, depth and focus, taken again at a later HEAD, so that the two can be
	// compared (Compare) and the team re-checks what the previous scan found.
	Previous string `json:"previous,omitempty"`

	Inventory Inventory `json:"inventory"`
	Shards    []Shard   `json:"shards"`
	Points    []Point   `json:"points"`
	Edges     []Edge    `json:"edges"`
	Leads     []Lead    `json:"leads"`
	// Reported is the version of the ledger the last report action saw, and whether it found problems.
	Reported *Mark `json:"reported,omitempty"`
	Version  int   `json:"version"`
}

// Mark is what the last report saw.
type Mark struct {
	Version  int `json:"version"`
	Problems int `json:"problems"`
}

// Inventory is what the harness counted at the commit: the files it scans and the functions in them.
type Inventory struct {
	Files []File `json:"files"`
	// Functions are kept in functions.json, written once when the scan starts: they never change, and scan.json is written
	// after every call.
	Functions []Func `json:"-"`
	// Skipped counts the tracked files left out (vendored, generated, binary, too large), by reason.
	Skipped map[string]int `json:"skipped,omitempty"`
}

// File is one scanned file.
type File struct {
	Path  string `json:"path"`
	Lang  string `json:"lang"`
	Lines int    `json:"lines"`
	// Test is a test file: scanned for what it reveals, but a lead whose trace is only in tests is test-only.
	Test bool `json:"test,omitempty"`
	// Funcs and Complexity sum the file's functions.
	Funcs      int `json:"funcs"`
	Complexity int `json:"complexity"`
}

// Func is one function or method the harness found: where it is and how many decisions it makes.
type Func struct {
	File       string `json:"file"`
	Name       string `json:"name"`
	Line       int    `json:"line"`
	End        int    `json:"end"`
	Complexity int    `json:"complexity"`
}

// Shard is one share of the inventory: a set of files close together, sized so one agent can read all of it.
type Shard struct {
	ID         string   `json:"id"`
	Files      []string `json:"files"`
	Funcs      int      `json:"funcs"`
	Lines      int      `json:"lines"`
	Complexity int      `json:"complexity"`
	// Claimed is the agent working the shard, and Phase what it works on (map or hunt).
	Claimed string `json:"claimed,omitempty"`
	// Mapped is the shard's map: what it does, as the agent who read it says. MappedBy wrote it.
	Mapped   string `json:"mapped,omitempty"`
	MappedBy string `json:"mapped_by,omitempty"`
	// Hunted is the note of the agent who investigated the shard for holes, with HuntedBy.
	Hunted   string `json:"hunted,omitempty"`
	HuntedBy string `json:"hunted_by,omitempty"`
}

// Lanes are the five kinds of attack-surface point.
const (
	LaneEntry    = "entry"    // where input comes in: routes, handlers, CLI arguments, queue consumers, file readers
	LaneBoundary = "boundary" // where trust changes: parsing, deserialization, validation, the edge of a sandbox
	LaneAccess   = "access"   // who may do what: authentication, authorization, tenancy checks
	LaneSink     = "sink"     // where data can do harm: queries, commands, file paths, templates, redirects, outbound requests
	LaneConfig   = "config"   // security settings: CORS, cookies, TLS, secrets, debug switches, defaults
)

// Lanes lists the lanes in order.
var Lanes = []string{LaneEntry, LaneBoundary, LaneAccess, LaneSink, LaneConfig}

// Cite is a place in the code at the commit, with the text an agent quoted from it. The harness keeps a cite only when the
// quote is there, and records the line it found it on.
type Cite struct {
	File  string `json:"file"`
	Line  int    `json:"line"`
	Quote string `json:"quote"`
}

// Point is one point of the attack surface.
type Point struct {
	ID     string `json:"id"`
	Lane   string `json:"lane"`
	Title  string `json:"title"`
	Symbol string `json:"symbol,omitempty"`
	Cite
	Note  string `json:"note,omitempty"`
	Shard string `json:"shard,omitempty"`
	By    string `json:"by"`
}

// Edge relations.
var Relations = []string{"calls", "routes", "reads", "writes", "guards", "validates", "configures", "imports", "spawns", "sends"}

// Edge is a relation between two places: From calls, routes to, guards... To. A cited edge is extracted (the harness found
// its quote); an edge without a cite is inferred, and the map shows it as a guess.
type Edge struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	To       string `json:"to"`
	Relation string `json:"relation"`
	Cite     *Cite  `json:"cite,omitempty"`
	Note     string `json:"note,omitempty"`
	By       string `json:"by"`
}

// Extracted reports whether the edge rests on a checked quote.
func (e Edge) Extracted() bool { return e.Cite != nil }

// Reachability tiers: how an attacker gets to the code.
const (
	ReachExternal    = "external"    // from outside the system without an account the attacker could not get
	ReachInternal    = "internal"    // from an authenticated user, another service, an operator or the local machine
	ReachUnreachable = "unreachable" // no path from an entry point was found
)

// Exploitability tiers: what it takes once there.
const (
	ExploitTrivial     = "trivial"     // one request, no special conditions
	ExploitModerate    = "moderate"    // a crafted input or a common configuration
	ExploitDifficult   = "difficult"   // a race, an uncommon configuration, a chain of conditions
	ExploitTheoretical = "theoretical" // no practical way was shown
)

// Impact levels, which become severities.
const (
	SevCritical = "critical"
	SevHigh     = "high"
	SevMedium   = "medium"
	SevLow      = "low"
)

// Severities lists the severities from the worst.
var Severities = []string{SevCritical, SevHigh, SevMedium, SevLow}

// Categories are the kinds of hole a lead can be.
var Categories = []string{"injection", "command-injection", "path-traversal", "ssrf", "xss", "auth", "access-control", "idor",
	"deserialization", "secrets", "crypto", "race", "memory", "dos", "redirect", "csrf", "info-leak", "supply-chain", "config",
	"logic", "correctness", "other"}

// Lead states.
const (
	LeadOpen      = "open"      // filed; not yet confirmed by enough verifiers
	LeadConfirmed = "confirmed" // the verifiers confirmed it, and it is proven where the depth asks for proof
	LeadUnproven  = "unproven"  // confirmed, but the depth wants a reproduction and none reproduced it yet
	LeadRejected  = "rejected"  // the verifiers rejected it
	LeadDisputed  = "disputed"  // ruled on by more verifiers than the depth asks for without them agreeing: settled, reported apart
	LeadDeferred  = "deferred"  // recorded past the scan's stopping point (Run.intake): kept and reported, never verified
)

// Rejection reasons.
var RejectReasons = []string{"duplicate", "mitigated", "speculative", "test-only", "dead-code", "unreachable", "wrong"}

// Lead is a suspected hole: what it is, the trace from where input comes in to where it does harm, and what the verifiers and
// the reproductions made of it.
type Lead struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Claim    string `json:"claim"`
	// Trace runs from the entry point to the sink, each step a checked cite.
	Trace   []Step `json:"trace"`
	Impact  string `json:"impact"`
	Reach   string `json:"reach"`
	Exploit string `json:"exploit"`
	// Points are the attack-surface points the trace goes through.
	Points []string `json:"points,omitempty"`
	Fix    string   `json:"fix,omitempty"`
	Shard  string   `json:"shard,omitempty"`
	By     string   `json:"by"`
	// Verdicts and Proofs are what the verifiers and the reproductions made of it, in order.
	Verdicts []Verdict `json:"verdicts,omitempty"`
	Proofs   []Proof   `json:"proofs,omitempty"`
	// Duplicate is the lead this one repeats, when a verifier rejected it as a duplicate.
	Duplicate string `json:"duplicate,omitempty"`
	// Late says the lead was filed after every shard was hunted, and Deferred why it is not verified when the scan's stopping
	// rules held it back (Run.intake).
	Late     bool   `json:"late,omitempty"`
	Deferred string `json:"deferred,omitempty"`
}

// Step is one step of a trace.
type Step struct {
	Cite
	Note string `json:"note"`
}

// Verdict is one verifier's ruling on a lead. A verifier may correct the reachability or exploitability it found; the lead's
// severity then uses the most cautious of the corrections.
type Verdict struct {
	By      string `json:"by"`
	Role    string `json:"role"`
	Confirm bool   `json:"confirm"`
	Reason  string `json:"reason,omitempty"`
	Note    string `json:"note"`
	Reach   string `json:"reach,omitempty"`
	Exploit string `json:"exploit,omitempty"`
	// Cites back the verifier's argument: a guard the filer missed, a caller that cannot pass the input.
	Cites []Cite `json:"cites,omitempty"`
}

// Proof outcomes.
const (
	ProofReproduced   = "reproduced"
	ProofNotReproduce = "not-reproduced"
	ProofInconclusive = "inconclusive"
)

// Proof is one run of a reproducer in the sandbox.
type Proof struct {
	By     string `json:"by"`
	Script string `json:"script"`
	// Ready says the program under test started (the reproducer printed the ready marker), Exercised that the reproducer
	// reached the code of the trace (the target marker), and Outcome what the harness concluded from the markers and the exit.
	Ready     bool   `json:"ready"`
	Exercised bool   `json:"exercised"`
	Outcome   string `json:"outcome"`
	Exit      int    `json:"exit"`
	Output    string `json:"output"`
	Network   bool   `json:"network,omitempty"`
	Seconds   int    `json:"seconds"`
	At        string `json:"at,omitempty"`
}

// Markers a reproducer prints, each on a line of its own.
const (
	MarkerReady      = "SLEIPNIR-APP-READY"
	MarkerExercised  = "SLEIPNIR-TARGET-EXERCISED"
	MarkerReproduced = "SLEIPNIR-REPRODUCED"
)

// Shard returns the shard with this id, or nil.
func (r *Run) Shard(id string) *Shard {
	for i := range r.Shards {
		if strings.EqualFold(r.Shards[i].ID, id) {
			return &r.Shards[i]
		}
	}
	return nil
}

// Lead returns the lead with this id, or nil.
func (r *Run) Lead(id string) *Lead {
	for i := range r.Leads {
		if strings.EqualFold(r.Leads[i].ID, id) {
			return &r.Leads[i]
		}
	}
	return nil
}

// Point returns the point with this id, or nil.
func (r *Run) Point(id string) *Point {
	for i := range r.Points {
		if strings.EqualFold(r.Points[i].ID, id) {
			return &r.Points[i]
		}
	}
	return nil
}

// ShardOf is the shard a file belongs to, or "".
func (r *Run) ShardOf(file string) string {
	for _, s := range r.Shards {
		for _, f := range s.Files {
			if f == file {
				return s.ID
			}
		}
	}
	return ""
}

// File returns the inventory's file at path, or nil.
func (r *Run) File(path string) *File {
	for i := range r.Inventory.Files {
		if r.Inventory.Files[i].Path == path {
			return &r.Inventory.Files[i]
		}
	}
	return nil
}

func oneOf(v string, list []string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
