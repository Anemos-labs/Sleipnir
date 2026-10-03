package widget_test

// Plausible data for the swarm widgets and the cockpit: the eight agents of the swarm sketch (docs/design/ux/swarm.png) and a
// generated swarm of fifty. All of it is deterministic: a fixed seed, no clock, no map iteration.

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

// showSketchAgents are the eight agents of the sketch, with their activity and marks.
func showSketchAgents() []widget.AgentRow {
	specs := []struct {
		id, role string
		color    int
		state    widget.AgentState
		doing    string
		shared   int
		own      int
		cost     float64
		hit      float64
		scope    string
		bias     float64
	}{
		{"m0", "manager", 0, widget.StateThinking, "plan: split by endpoint", 0, 12100, 0.012, 0.96, "", 0.7},
		{"w1", "backend", 1, widget.StateTool, "go test ./orders/...", 41200, 31000, 0.020, 0.97, "orders/*", 0.9},
		{"w2", "backend", 1, widget.StateEdit, "internal/users/list.go", 41200, 28400, 0.018, 0.95, "users/*", 0.85},
		{"w3", "frontend", 2, widget.StateWait, "schema: GET /orders", 20000, 14200, 0.009, 0.96, "web/api", 0.45},
		{"w4", "frontend", 2, widget.StateThinking, "paginate <Orders>", 22000, 22600, 0.014, 0.94, "web/ord", 0.8},
		{"w5", "tester", 3, widget.StateIdle, "waits for t3 merge", 0, 6800, 0.004, 0.91, "", 0.15},
		{"w6", "reviewer", 4, widget.StateDone, "approved t2", 0, 9900, 0.006, 0.95, "", 0.4},
		{"w7", "docs", 5, widget.StateStuck, "npm test - same failure", 30000, 18000, 0.012, 0.88, "docs/*", 0.6},
	}
	rows := make([]widget.AgentRow, len(specs))
	for i, s := range specs {
		rows[i] = widget.AgentRow{
			ID: s.id, Role: s.role, RoleColor: s.color, State: s.state, Doing: s.doing,
			Shared: s.shared, Own: s.own, Hit: s.hit, Cost: s.cost, Scope: s.scope,
			Levels: showLane(showRand(int64(100+i)), widget.GanttBuckets, s.bias, s.state == widget.StateDone),
		}
	}
	rows[0].Marks = []widget.LaneMark{{Bucket: 44, Kind: widget.LaneCompact}}
	rows[1].Marks = []widget.LaneMark{{Bucket: 12, Kind: widget.LaneMail}}
	rows[3].Marks = []widget.LaneMark{{Bucket: 38, Kind: widget.LaneMail}}
	rows[7].Marks = []widget.LaneMark{{Bucket: 52, Kind: widget.LaneStuck}}
	return rows
}

// showLane is a minute of activity: runs of busy and idle with levels, like the lanes of the sketch.
func showLane(r *rand.Rand, n int, bias float64, endsEarly bool) []uint8 {
	var out []uint8
	for len(out) < n {
		busy := float64(r.Intn(1000))/1000 < bias
		run := 2 + r.Intn(8)
		for k := 0; k < run; k++ {
			if busy {
				out = append(out, uint8(4+r.Intn(5)))
			} else {
				out = append(out, uint8(r.Intn(3)))
			}
		}
	}
	out = out[:n]
	if endsEarly {
		for i := n - 14; i < n; i++ {
			out[i] = 0
		}
	}
	return out
}

// showLanes are the lanes of agents (the activity and the marks of each).
func showLanes(agents []widget.AgentRow) []widget.Lane {
	lanes := make([]widget.Lane, len(agents))
	for i, a := range agents {
		lanes[i] = widget.Lane{Name: a.ID, Color: a.RoleColor, Levels: a.Levels, First: a.LevelsFrom, Marks: a.Marks}
	}
	return lanes
}

// showSwarm is n agents in the roles of the sketch (the first eight are the sketch's), with states, prompts and activity from a
// fixed seed.
func showSwarm(n int, seed int64) []widget.AgentRow {
	rows := showSketchAgents()
	if n <= len(rows) {
		return rows[:n]
	}
	r := showRand(seed)
	roles := []struct {
		name  string
		color int
	}{{"backend", 1}, {"frontend", 2}, {"tester", 3}, {"reviewer", 4}, {"docs", 5}}
	states := []widget.AgentState{widget.StateThinking, widget.StateTool, widget.StateTool, widget.StateEdit, widget.StateWait, widget.StateIdle,
		widget.StateDone, widget.StateThinking, widget.StateTool, widget.StateStuck}
	doing := []string{"go test ./...", "internal/orders/list.go", "waits for t3", "paginate <Orders>", "review t9", "docs/api.md"}
	scopes := []string{"", "orders/*", "users/*", "web/api", "docs/*"}
	for i := len(rows); i < n; i++ {
		ro := roles[r.Intn(len(roles))]
		st := states[r.Intn(len(states))]
		if st == widget.StateStuck && r.Intn(4) != 0 {
			st = widget.StateTool
		}
		own := 2000 + r.Intn(40000)
		rows = append(rows, widget.AgentRow{
			ID: fmt.Sprintf("w%d", i), Role: ro.name, RoleColor: ro.color, State: st, Doing: doing[r.Intn(len(doing))],
			Shared: 41200, Own: own, Hit: 0.85 + float64(r.Intn(14))/100, Cost: float64(own) * 0.0000005,
			Scope:  scopes[r.Intn(len(scopes))],
			Levels: showLane(r, widget.GanttBuckets, 0.3+float64(r.Intn(60))/100, st == widget.StateDone),
		})
	}
	return rows
}

func showStates(rows []widget.AgentRow) []widget.AgentState {
	out := make([]widget.AgentState, len(rows))
	for i, a := range rows {
		out[i] = a.State
	}
	return out
}

func showSketchKanban() []widget.KanbanCol {
	return []widget.KanbanCol{
		{Kind: widget.ColTodo, Cards: []widget.KanbanCard{{ID: "t9", Label: "docs"}, {ID: "t10", Label: "e2e"}, {ID: "t11", Label: "doc"}}},
		{Kind: widget.ColRunning, Cards: []widget.KanbanCard{{ID: "t3", Label: "ord"}, {ID: "t4", Label: "usr"}, {ID: "t5", Label: "web"}, {ID: "t6", Label: "cli"}}},
		{Kind: widget.ColVerifying, Cards: []widget.KanbanCard{{ID: "t7"}}},
		{Kind: widget.ColMerged, Cards: []widget.KanbanCard{{ID: "t1"}, {ID: "t2"}, {ID: "t4"}, {ID: "t5"}, {ID: "t6"}}},
	}
}

func showSketchMerge() []widget.MergeItem {
	return []widget.MergeItem{
		{ID: "t1", Stage: widget.MergeDone}, {ID: "t2", Stage: widget.MergeDone}, {ID: "t4", Stage: widget.MergeDone},
		{ID: "t5", Stage: widget.MergeDone}, {ID: "t6", Stage: widget.MergeDone},
		{ID: "t7", Stage: widget.MergeVerify, Note: "go test ./orders/..."},
		{ID: "t8", Stage: widget.MergeRebase},
	}
}

func showSketchMails() []widget.Mail {
	return []widget.Mail{
		{From: "w1", To: "w3", Subject: "schema: GET /orders", Tokens: 312, Born: -1},
		{From: "m0", To: "w5", Subject: "start e2e after t3", Tokens: 96, Born: -1},
		{From: "w4", To: "m0", Subject: "done: table paged", Tokens: 88, Born: -1},
		{From: "w2", To: "w1", Subject: "rename ListOptions?", Tokens: 41, Born: 10},
	}
}

// showSketchDashboard is the data of the swarm sketch with n agents (8 is the sketch).
func showSketchDashboard(n int) widget.DashboardData {
	return widget.DashboardData{
		Title:   "swarm “add pagination to every list endpoint…”",
		Elapsed: 2*time.Minute + 10*time.Second, Spend: 0.31, Budget: 20, HitRatio: 0.94,
		Shared:     showSharedLayers(),
		PrefixLeft: 3*time.Minute + 41*time.Second, PrefixTTL: 5 * time.Minute,
		Agents: showSwarm(n, 7), NowBucket: widget.GanttBuckets - 1,
		Kanban:    showSketchKanban(),
		Merge:     showSketchMerge(),
		Mail:      showSketchMails(),
		MailStats: widget.MailStats{Routed: 14, Delivered: 14, Dup: 0, Ignored: 1},
		Governor:  widget.Governor{RPM: 212, RPMLimit: 240, Err429: 0, Retries: 1, Speedup: 6},
		Feed: []widget.FeedLine{
			{At: 2*time.Minute + 10*time.Second, Agent: "w1", Color: 1, Kind: widget.FeedOK, Text: "go test ./orders/...", Tail: "1.4 s · $0.0004 · ⛁ 97%"},
			{At: 2*time.Minute + 9*time.Second, Agent: "w7", Color: 5, Kind: widget.FeedWarn, Text: "same failure twice: npm test — nudged, stops at 8", Tail: "repeat guard"},
			{At: 2*time.Minute + 8*time.Second, Agent: "m0", Color: 0, Kind: widget.FeedCompact, Text: "compacted 31.2k → 2.4k at a cold moment", Tail: "free · ⛁ kept 96%"},
			{At: 2*time.Minute + 7*time.Second, Agent: "w3", Color: 2, Kind: widget.FeedMail, Text: "waiting on w1: schema for GET /orders", Tail: "mail 312 tok"},
		},
	}
}
