package webtest

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// The canned session: a manager and four workers building a shop, 38 seconds into the run.
const (
	// TabID is the id of the canned tab.
	TabID = "shop"
	// SID is the harness session id of the canned tab.
	SID = "20261009-221530-a91c3e"
	// Cwd is the project directory of the canned tab.
	Cwd = "/home/me/projects/shop"
	// Model is the manager's model.
	Model = "anthropic/claude-sonnet-5-5"
	// StartedAt is when the canned run began, in epoch milliseconds (2025-10-09 22:15:30 UTC).
	StartedAt int64 = 1760048130000
	// Now is the session time of the snapshot, in seconds.
	Now = 38.0
	// Boot is the boot id the fake server reports.
	Boot = "fakeboot"
	// Goal is the standing goal of the canned session.
	Goal = "Build the shop: a catalogue endpoint, a cart screen and tests"
)

// Scenario is a canned session: what a late joiner sees (Keyframe, History, Questions at Now) and what happens next (Live).
type Scenario struct {
	// Tab is the tab as lists show it.
	Tab wire.TabSummary
	// Meta is the tab's meta at Now.
	Meta wire.MetaPatch
	// Roster is the agents.
	Roster []wire.RosterEntry
	// Keyframe is the state folded from events that are no longer retained, as synthetic events (seq 0).
	Keyframe []wire.Event
	// History is the retained events up to Now, in seq order from 1.
	History []wire.Event
	// Live is what happens after Now, in seq order following History.
	Live []wire.Event
	// Questions are the questions open at Now.
	Questions []wire.Question
	// Hist is the composer's history, oldest first.
	Hist []string
	// Now is the session time of the snapshot, in seconds.
	Now float64
}

// timeline collects events with their times; the sequence numbers are given when a tab journals them.
type timeline struct{ evs []wire.Event }

// at adds an event at session time t and returns it.
func (tl *timeline) at(t float64, e wire.Event) {
	wire.Stamp(e, t, 0)
	tl.evs = append(tl.evs, e)
}

// str returns a pointer to s (the task of a state event).
func str(s string) *string { return &s }

// Roster returns the canned agents: the manager, a scout, a backend, a frontend and a tester worker.
func Roster() []wire.RosterEntry {
	return []wire.RosterEntry{
		{ID: "mgr", Role: "manager", Code: "mgr", Nth: 0, K: 0, Leg: -1, Scope: "- (edits no file)", Model: Model, Spawn: 0},
		{ID: "sc-1", Role: "scout", Code: "sc", Nth: 1, K: 1, Leg: 0, Scope: "- (read-only)", RO: true, Model: "anthropic/claude-haiku-5-5", Spawn: 2.6},
		{ID: "be-1", Role: "backend", Code: "be", Nth: 1, K: 2, Leg: 1, Scope: "api/**", Model: Model, Spawn: 8.6},
		{ID: "fe-1", Role: "frontend", Code: "fe", Nth: 1, K: 3, Leg: 2, Scope: "web/**", Model: Model, Spawn: 8.6},
		{ID: "te-1", Role: "tester", Code: "te", Nth: 1, K: 4, Leg: 3, Scope: "**/*_test.go", Model: Model, Spawn: 20.0},
	}
}

// ShopQuestion is the question that is open in the snapshot: the frontend wants to install a package.
func ShopQuestion() wire.Question {
	return wire.Question{
		ID: "q_k3m5w2c7x4b7d2f5h6j3n2p4r7", Agent: "fe-1", Task: "T3", Cmd: "npm install --save-dev vitest", Cwd: "web/",
		Why:   "installs a package from the network and edits web/package.json; it is not a build or test command",
		Scope: "cwd web/ (inside fe-1's lease web/**)", What: "this command", Rule: "Bash(npm install --save-dev vitest)",
		Kind: "command", Tool: "bash",
	}
}

// LaterQuestion is the question the continuation opens: the tester wants to run the tests.
func LaterQuestion() wire.Question {
	return wire.Question{
		ID: "q_b5t7x2m4c3d3f4h2j6k3n2p4rw", Agent: "te-1", Task: "T4", Cmd: "go test ./api/catalog/...", Cwd: "api/catalog/",
		Why:   "runs the package's tests; the tests preset would allow every build and test command",
		Scope: "cwd api/catalog/ (inside te-1's lease **/*_test.go)", What: `"go test" commands`, Rule: "Bash(go test:*)",
		Kind: "command", Tool: "bash", OffersTests: true,
	}
}

// Shop returns the canned session. Every call returns an independent value.
func Shop() *Scenario {
	h := &timeline{}
	steps := []string{"Survey the API and the seed data", "Catalogue endpoint", "Cart screen", "Tests for both"}
	h.at(0.0, &wire.Say{Who: "you", Text: "/goal " + Goal})
	h.at(0.1, &wire.Say{Who: "sys", Glyph: "◇", Text: "goal set; plan:", Plan: true})
	h.at(0.1, &wire.Goal{S: "active", Objective: Goal, Turns: 0, Max: 20})
	h.at(0.1, &wire.Plan{Steps: steps, St: []string{"act", "pending", "pending", "pending"}})
	h.at(0.2, &wire.Verdict{Text: "not yet: no work is done", Kind: "continue", Left: []string{"T1-T4 merged", "go test ./... passes"}})
	h.at(0.3, &wire.Turn{S: "start"})
	h.at(0.3, &wire.State{ID: "mgr", S: "think", Doing: "plans the build"})
	h.at(1.0, &wire.Say{Who: "mgr", Text: "I'll have a scout map the API and the seed data, then split the build by directory so no two workers touch the same file.", Stream: true, Rate: 240, Mid: "@plan"})
	h.at(2.5, &wire.Tool{ID: "mgr", Name: "TaskBoard", Arg: "create T1 T2 T3 T4", Out: "created 4 tasks", OK: true})
	h.at(2.6, &wire.Task{ID: "T1", Title: "survey the API and the seed data", Owner: "sc-1", Scope: "- (read-only)", S: "running"})
	h.at(2.6, &wire.Task{ID: "T2", Title: "catalogue: GET /items?page&size", Owner: "be-1", Deps: []string{"T1"}, Scope: "api/catalog/**, api/server.go", S: "todo"})
	h.at(2.6, &wire.Task{ID: "T3", Title: "cart screen", Owner: "fe-1", Deps: []string{"T1"}, Scope: "web/cart/**, web/package.json", S: "todo"})
	h.at(2.6, &wire.Task{ID: "T4", Title: "tests for the catalogue and the cart", Owner: "te-1", Deps: []string{"T2", "T3"}, Scope: "**/*_test.go", S: "todo"})
	h.at(2.7, &wire.State{ID: "sc-1", S: "tool", Doing: "Read api/server.go", Task: str("T1")})
	h.at(3.0, &wire.Tool{ID: "sc-1", Name: "Read", Arg: "api/server.go", Out: "212 lines", OK: true, Task: "T1", TID: "toolu_01"})
	h.at(3.1, &wire.State{ID: "sc-1", S: "think", Doing: "reads the seed data", Task: str("T1")})
	h.at(3.5, &wire.Req{ID: "sc-1", Ratio: 0, P: 3100, O: 180})
	h.at(3.5, &wire.Use{ID: "sc-1", Rd: 0, Un: 3100, Out: 180, Wr: 3100, Cost: 0.0113, Saved: 0})
	h.at(3.5, &wire.Warm{TTL: 300})
	h.at(3.6, &wire.Layers{ID: "sc-1", Toks: [6]int{900, 1200, 300, 0, 700, 0}})
	h.at(5.5, &wire.Mail{From: "sc-1", To: "mgr", Text: "API: GET /items?page&size -> {items[], page}; seed data: 40 items; paging by page and size"})
	h.at(6.2, &wire.Note{ID: "sc-1", G: "done", Text: "submitted T1", Task: "T1"})
	h.at(6.3, &wire.Task{ID: "T1", S: "verify"})
	head := "T1"
	h.at(6.5, &wire.Queue{QHead: &head, Cmd: "go vet ./...", Step: "verifying"})
	h.at(8.0, &wire.Queue{QHead: &head, Cmd: "go vet ./...", Step: "verified", Ms: 1500})
	h.at(8.1, &wire.Merge{ID: "T1", Cmd: "go vet ./...", Ms: 1500})
	h.at(8.2, &wire.Task{ID: "T1", S: "merged"})
	h.at(8.4, &wire.Queue{})
	h.at(8.5, &wire.State{ID: "sc-1", S: "idle", Doing: "waits for work", Task: str("T1")})
	h.at(8.6, &wire.Task{ID: "T2", S: "running"})
	h.at(8.6, &wire.Task{ID: "T3", S: "running"})
	h.at(8.7, &wire.Plan{Steps: steps, St: []string{"done", "act", "act", "pending"}})
	h.at(9.0, &wire.State{ID: "be-1", S: "edit", Doing: "Write api/catalog/items.go +31", Task: str("T2")})
	h.at(9.1, &wire.Stream{ID: "be-1", Text: "package catalog\n\n// Page is one page of items.\ntype Page struct {\n\tItems []Item `json:\"items\"`\n\tPage  int    `json:\"page\"`\n}\n", Rate: 400, Code: true, File: "api/catalog/items.go", Mid: "@items"})
	h.at(12.0, &wire.Tool{ID: "be-1", Name: "Write", Arg: "api/catalog/items.go", Out: "wrote 31 lines", OK: true, File: "api/catalog/items.go", Add: 31, Task: "T2", TID: "toolu_02"})
	h.at(12.1, &wire.Diff{File: "api/catalog/items.go", Done: true})
	h.at(12.2, &wire.Ckpt{CID: "c01", TS: "22:15:42", Files: 0, Note: "turn 1: build the shop", Skipped: true})
	h.at(12.3, &wire.Ckpt{CID: "c01", TS: "22:15:42", Files: 1, Note: "turn 1: build the shop", Agents: []string{"be-1"}, Add: 31})
	h.at(13.0, &wire.State{ID: "fe-1", S: "edit", Doing: "Write web/cart/Cart.tsx +54", Task: str("T3")})
	h.at(15.0, &wire.Tool{ID: "fe-1", Name: "Write", Arg: "web/cart/Cart.tsx", Out: "wrote 54 lines", OK: true, File: "web/cart/Cart.tsx", Add: 54, Task: "T3", TID: "toolu_03"})
	h.at(15.1, &wire.Diff{File: "web/cart/Cart.tsx", Done: true})
	h.at(15.5, &wire.Ckpt{CID: "c01", TS: "22:15:42", Files: 2, Note: "turn 1: build the shop", Agents: []string{"be-1", "fe-1"}, Add: 85})
	h.at(16.0, &wire.Req{ID: "be-1", Ratio: 0.91, P: 6200, O: 400})
	h.at(16.0, &wire.Use{ID: "be-1", Rd: 5642, Un: 558, Out: 400, Wr: 558, Cost: 0.0261, Saved: 0.0148})
	h.at(16.1, &wire.Layers{ID: "be-1", Toks: [6]int{900, 1200, 400, 300, 800, 2600}})
	h.at(16.5, &wire.Req{ID: "fe-1", Ratio: 0.88, P: 5900, O: 520})
	h.at(16.5, &wire.Use{ID: "fe-1", Rd: 5192, Un: 708, Out: 520, Wr: 708, Cost: 0.0298, Saved: 0.0131})
	h.at(18.0, &wire.Gov{RPM: 31, R429: 0, Retries: 0, Inflight: 3, Queued: 0})
	h.at(20.0, &wire.Break{ID: "be-1", Kind: "drift", Read: 1200, Expected: 5900, Why: "the tool list changed between two requests"})
	h.at(20.0, &wire.State{ID: "te-1", S: "think", Doing: "reads the new code", Task: str("T4")})
	h.at(22.0, &wire.Sys{Ch: "mgr", Glyph: "⚙", Text: "lease api/catalog/** → be-1", Ag: "be-1", Task: "T2"})
	h.at(24.0, &wire.Refuse{ID: "te-1", Name: "Bash", Arg: "rm -rf build", Reason: "needs approval and nobody can answer: headless run"})
	h.at(26.0, &wire.Stall{ID: "fe-1", Task: "T3", Kind: "claimed_no_progress", S: "raise", Text: "fe-1 claimed T3 and has not reported progress"})
	h.at(26.1, &wire.Sys{Ch: "mgr", Glyph: "⚠", Text: "fe-1 claimed T3 and has not reported progress", Ag: "fe-1", Task: "T3"})
	h.at(28.0, &wire.Stall{ID: "fe-1", Task: "T3", Kind: "claimed_no_progress", S: "clear", Text: "progress reported"})
	h.at(29.0, &wire.Compact{ID: "mgr", From: 41000, To: 12000, Pct: -71})
	h.at(30.0, &wire.Steer{To: "be-1", Text: "keep the page size under 50"})
	h.at(31.0, &wire.Mail{From: "be-1", To: "fe-1", Text: "catalogue: GET /items?page=1&size=12 -> {items[], page}"})
	h.at(33.0, &wire.Ask{Q: ShopQuestion()})
	h.at(33.0, &wire.State{ID: "fe-1", S: "ask", Doing: "asks: npm install --save-dev vitest", Task: str("T3")})
	h.at(35.0, &wire.Req{ID: "mgr", Ratio: 0.95, P: 9100, O: 310})
	h.at(35.0, &wire.Use{ID: "mgr", Rd: 8645, Un: 455, Out: 310, Wr: 455, Cost: 0.0419, Saved: 0.0213})
	h.at(37.0, &wire.Warm{TTL: 300})
	h.at(38.0, &wire.Gov{RPM: 44, R429: 0, Retries: 0, Inflight: 4, Queued: 0})

	l := &timeline{}
	l.at(39.5, &wire.State{ID: "be-1", S: "tool", Doing: "Bash go test ./api/catalog/...", Task: str("T2")})
	l.at(41.0, &wire.Tool{ID: "be-1", Name: "Bash", Arg: "go test ./api/catalog/...", Out: "ok  shop/api/catalog 0.4s", OK: true, Task: "T2", TID: "toolu_04"})
	l.at(41.5, &wire.Req{ID: "be-1", Ratio: 0.93, P: 7000, O: 210})
	l.at(41.5, &wire.Use{ID: "be-1", Rd: 12142, Un: 1058, Out: 610, Wr: 1058, Cost: 0.0466, Saved: 0.0279})
	l.at(42.0, &wire.Note{ID: "be-1", G: "done", Text: "submitted T2", Task: "T2"})
	l.at(42.1, &wire.Task{ID: "T2", S: "verify"})
	t2 := "T2"
	l.at(42.5, &wire.Queue{QHead: &t2, Cmd: "go test ./api/catalog/...", Step: "verifying"})
	l.at(46.8, &wire.Queue{QHead: &t2, Cmd: "go test ./api/catalog/...", Step: "verified", Ms: 4300})
	l.at(46.9, &wire.Merge{ID: "T2", Cmd: "go test ./api/catalog/...", Ms: 4300})
	l.at(47.0, &wire.Task{ID: "T2", S: "merged"})
	l.at(47.2, &wire.Queue{})
	l.at(47.5, &wire.State{ID: "be-1", S: "idle", Doing: "waits for work", Task: str("T2")})
	l.at(48.0, &wire.Ask{Q: LaterQuestion()})
	l.at(48.0, &wire.State{ID: "te-1", S: "ask", Doing: "asks: go test ./api/catalog/...", Task: str("T4")})
	l.at(50.0, &wire.Gov{RPM: 52, R429: 1, Retries: 1, Inflight: 3, Queued: 1})
	l.at(52.0, &wire.Plan{Steps: steps, St: []string{"done", "done", "act", "pending"}})
	l.at(60.0, &wire.Say{Who: "mgr", Text: "The catalogue endpoint is merged. The cart screen and the tests are still running.", Stream: true, Rate: 240, Mid: "@wait"})
	l.at(61.0, &wire.More{Mid: "@wait", Text: " I will wait for the tester's question to be answered.", End: true})
	l.at(62.0, &wire.State{ID: "mgr", S: "idle", Doing: "waits for you"})
	l.at(62.0, &wire.Final{})
	l.at(62.0, &wire.Turn{S: "end"})

	frame := []wire.Event{
		&wire.Req{ID: "mgr", Ratio: 0.95, Hist: true}, &wire.Req{ID: "mgr", Ratio: 0.96, Hist: true},
		&wire.Layers{ID: "mgr", Toks: [6]int{900, 1200, 400, 600, 1100, 5200}},
	}
	for _, e := range frame {
		wire.Stamp(e, 0, 0)
	}
	resolveMids(h.evs, l.evs)
	return &Scenario{
		Tab: wire.TabSummary{ID: TabID, SID: SID, Name: "shop", Cwd: Cwd, Gen: 1, CreatedAt: StartedAt, Order: 0},
		Meta: func() wire.MetaPatch {
			s, m, e, sw, iso, v := Cwd, "default", "default", 4, "worktree", "go test {dirs}"
			model, launch, ask := Model, "sleipnir chat --swarm 4 --isolation worktree", "off"
			b, tr, mm, no, run, started := 5.0, true, false, false, true, StartedAt
			gt := Goal
			rm := map[string]string{"scout": "anthropic/claude-haiku-5-5"}
			rules := []wire.Rule{
				{Effect: "allow", Rule: "Bash(go test:*)", Origin: "--allow flag"},
				{Effect: "deny", Rule: "Read(./.env)", Origin: "project config", File: ".sleipnir/config.json"},
			}
			q := []wire.QueuedLine{}
			return wire.MetaPatch{Cwd: &s, Model: &model, Mode: &m, Effort: &e, Budget: &b, Swarm: &sw, Isolation: &iso, Verify: &v, Mailman: &mm,
				TrustProject: &tr, NoMcp: &no, RoleModels: &rm, Rules: &rules, GoalText: &gt, Launch: &launch, StartedAt: &started,
				AskTimeout: &ask, Queued: &q, Running: &run}
		}(),
		Roster: Roster(), Keyframe: frame, History: h.evs, Live: l.evs, Questions: []wire.Question{ShopQuestion()},
		Hist: []string{"/goal " + Goal, "/permissions", "keep the page size under 50"}, Now: Now,
	}
}

// resolveMids gives the messages their ids: "m" and the sequence number of the event that opened them. The
// timelines name a message "@name" where it opens and where it continues; the events are journaled in order from seq 1, history
// first.
func resolveMids(parts ...[]wire.Event) {
	ids := map[string]string{}
	seq := 0
	for _, part := range parts {
		for _, e := range part {
			seq++
			var mid *string
			switch v := e.(type) {
			case *wire.Say:
				mid = &v.Mid
			case *wire.Stream:
				mid = &v.Mid
			}
			if mid != nil && strings.HasPrefix(*mid, "@") {
				ids[*mid] = "m" + strconv.Itoa(seq)
				*mid = ids[*mid]
			}
		}
	}
	for _, part := range parts {
		for _, e := range part {
			if m, ok := e.(*wire.More); ok && strings.HasPrefix(m.Mid, "@") {
				m.Mid = ids[m.Mid]
			}
		}
	}
}

// raw encodes an event as the journal keeps it.
func raw(e wire.Event) json.RawMessage {
	b, err := json.Marshal(e)
	if err != nil {
		panic("webtest: encoding " + wire.KindOf(e) + ": " + err.Error()) // the types are those of package wire: a failure is a bug here
	}
	return b
}

// Classifier assigns the hub class of a frame (docs/WEB-API.md lists the classes). It remembers the checkpoints it has seen: the
// first ckpt of an id is critical, later ones coalesce.
type Classifier struct{ seen map[string]bool }

// EvFrame wraps a UI event of a tab in its "ev" frame with its class and key.
func (c *Classifier) EvFrame(tab string, e wire.Event) wire.Frame {
	f := wire.Frame{Type: "ev", Tab: tab, Data: wire.EvFrame{Tab: tab, Ev: raw(e)}, Critical: true}
	coalesce := func(key string) { f.Critical, f.Coalescable, f.Key = false, true, key }
	switch v := e.(type) {
	case *wire.Use:
		coalesce("use/" + tab + "/" + v.ID)
	case *wire.Layers:
		coalesce("layers/" + tab + "/" + v.ID)
	case *wire.Gov, *wire.Warm, *wire.Plan, *wire.Verdict, *wire.Queue:
		coalesce(wire.KindOf(e) + "/" + tab)
	case *wire.Diff:
		coalesce("diff/" + tab + "/" + v.File)
	case *wire.Ckpt:
		if c.seen == nil {
			c.seen = map[string]bool{}
		}
		if c.seen[tab+"/"+v.CID] {
			coalesce("ckpt/" + tab + "/" + v.CID)
		}
		c.seen[tab+"/"+v.CID] = true
	case *wire.More:
		f.Critical = false
	}
	return f
}

// Frames wraps control data in a frame of the given type with its class (tab, reset, roster, meta, bye are
// critical; ping coalesces under "ping"; recorded, run and toast are ordinary).
func Frames(typ, tab string, data any) wire.Frame {
	f := wire.Frame{Type: typ, Tab: tab, Data: data}
	switch typ {
	case "tab", "reset", "roster", "meta", "bye":
		f.Critical = true
	case "ping":
		f.Coalescable, f.Key = true, "ping"
	}
	return f
}
