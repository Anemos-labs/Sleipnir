package swarm

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// ravenTeam is a board shaped like a 32-worker team at work: every role's workers, each with a task, and a tester pool (T1,
// leader ts-1) whose four members each own a child. In roster order the testers come last.
func ravenTeam(t *testing.T) *Snapshot {
	t.Helper()
	b := NewBoard(nil)
	b.SetLimits(BoardLimits{MaxTasks: 200})
	roles := []struct {
		short, role string
		n           int
	}{{"ar", "architect", 2}, {"be", "backend", 8}, {"dc", "docs", 2}, {"dv", "devops", 3}, {"fe", "frontend", 8}, {"rv", "reviewer", 4}, {"sc", "scout", 1}, {"ts", "tester", 4}}
	b.SetAgent(AgentInfo{ID: "mgr", Role: "manager", State: "running", Line: "coordinating"})
	pool, err := b.CreateTask("mgr", TaskSpec{Title: "acceptance tests", Role: "tester", Files: []string{"tests/**"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.beginPool("mgr", pool.ID, []string{"ts-1", "ts-2", "ts-3", "ts-4"}); err != nil {
		t.Fatal(err)
	}
	if err := b.poolUpdate("mgr", pool.ID, func(_ *Snapshot, _ *Task, p *PoolState) error { p.Phase = "executing"; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, r := range roles {
		for i := 1; i <= r.n; i++ {
			id := fmt.Sprintf("%s-%d", r.short, i)
			spec := TaskSpec{Title: fmt.Sprintf("%s work item %d with a title of ordinary length", r.role, i), Role: r.role}
			if r.short == "ts" {
				spec = TaskSpec{Parent: pool.ID, Title: fmt.Sprintf("acceptance suite part %d", i), Role: "tester", Files: []string{fmt.Sprintf("tests/p%d/**", i)}}
			}
			task, err := b.CreateTask("mgr", spec)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := b.assignTask(assignReq{by: "mgr", agent: id, id: task.ID}); err != nil {
				t.Fatal(err)
			}
			b.SetAgent(AgentInfo{ID: id, Role: r.role, State: "running", Task: task.ID, Line: "running a command"})
		}
	}
	b.SetAgent(AgentInfo{ID: "mm-1", Role: "mailman", State: "idle"})
	return b.Snapshot()
}

// mentioned reports whether the view names agent id as a word.
func mentioned(view, id string) bool {
	return regexp.MustCompile(`(^|[\s,(])` + regexp.QuoteMeta(id) + `([\s,)]|$)`).MatchString(view)
}

// Nobody on the team is missing from anyone's live view: a teammate has a line, or is named on the "not shown" line. The roster
// is chosen by relevance before it is capped: a pool member sees the other members of its pool with their tasks, wherever they
// are in roster order. A team run once capped the roster at 30 in roster order, so two testers were never shown to anyone, and
// their pool reported one of them "off-roster" while it worked; the manager excused it.
func TestLiveRosterShowsEveryTeammate(t *testing.T) {
	s := ravenTeam(t)
	est := core.NewBytesEstimator().WithRatio(4)
	views := map[string]string{
		"mgr":  RenderHot(s, "mgr", "manager", true, DefaultHotConfig(), est),
		"ts-1": RenderHot(s, "ts-1", "tester", false, DefaultHotConfig(), est),
		"ts-3": RenderHot(s, "ts-3", "tester", false, DefaultHotConfig(), est),
		"be-4": RenderHot(s, "be-4", "backend", false, DefaultHotConfig(), est),
	}
	for reader, view := range views {
		for _, a := range s.Agents {
			if a.ID != reader && !mentioned(view, a.ID) {
				t.Errorf("%s's view does not show or name %s:\n%s", reader, a.ID, view)
			}
		}
		budget := DefaultHotConfig().MaxTokens
		if reader == "mgr" {
			budget = DefaultHotConfig().ManagerMaxTokens
		}
		if tok := est.Tokens(view); tok > budget+50 {
			t.Errorf("%s's view is %d tokens, budget %d", reader, tok, budget)
		}
	}
	for _, reader := range []string{"ts-1", "ts-3"} {
		for _, mate := range []string{"ts-1", "ts-2", "ts-3", "ts-4"} {
			if mate != reader && !strings.Contains(views[reader], "  "+mate+" tester running T") {
				t.Errorf("%s's view has no line for its pool mate %s:\n%s", reader, mate, views[reader])
			}
		}
		if !strings.Contains(views[reader], "  mgr manager running") {
			t.Errorf("%s's view has no line for the manager:\n%s", reader, views[reader])
		}
	}
	if !strings.Contains(views["be-4"], " not shown: ") {
		t.Errorf("a worker whose view drops teammates does not name them:\n%s", views["be-4"])
	}
	if again := RenderHot(s, "ts-3", "tester", false, DefaultHotConfig(), est); again != views["ts-3"] {
		t.Error("the live view is not deterministic")
	}
}

// Under a small budget the least relevant teammates lose their lines first, and are named instead.
func TestLiveRosterDropsTheLeastRelevantFirst(t *testing.T) {
	s := ravenTeam(t)
	est := core.NewBytesEstimator().WithRatio(4)
	view := RenderHot(s, "ts-2", "tester", false, HotConfig{MaxTokens: 220}, est)
	for _, mate := range []string{"ts-1", "ts-3", "ts-4", "mgr"} {
		if !regexp.MustCompile(`(?m)^  ` + mate + ` \w+ running`).MatchString(view) {
			t.Errorf("%s lost its line before less relevant teammates did:\n%s", mate, view)
		}
	}
	at := strings.Index(view, " not shown: ")
	if at < 0 {
		t.Fatalf("no teammate is named:\n%s", view)
	}
	names := view[at:]
	for _, id := range []string{"dc-1", "fe-8", "rv-4", "sc-1", "mm-1"} {
		if !mentioned(names, id) {
			t.Errorf("%s kept its line, or is not named:\n%s", id, view)
		}
	}
	for _, a := range s.Agents {
		if a.ID != "ts-2" && !mentioned(view, a.ID) {
			t.Errorf("%s is neither shown nor named:\n%s", a.ID, view)
		}
	}
	if tok := est.Tokens(view); tok > 220 {
		t.Fatalf("view is %d tokens, budget 220:\n%s", tok, view)
	}
}

// The "not shown" line fits the budget too: with more teammates than it can name in the tokens left, it names fewer of them, the
// most relevant first, and still counts them all.
func TestLiveRosterNamesFitTheBudget(t *testing.T) {
	b := NewBoard(nil)
	b.SetLimits(BoardLimits{MaxTasks: 200})
	b.SetAgent(AgentInfo{ID: "mgr", Role: "manager", State: "running"})
	for i := 0; i < 70; i++ {
		b.SetAgent(AgentInfo{ID: fmt.Sprintf("backend-worker-%05d", i), Role: "backend", State: "idle"})
	}
	est := core.NewBytesEstimator().WithRatio(4)
	for _, budget := range []int{220, 120} {
		view := RenderHot(b.Snapshot(), "backend-worker-00007", "backend", false, HotConfig{MaxTokens: budget}, est)
		if tok := est.Tokens(view); tok > budget {
			t.Fatalf("view is %d tokens, budget %d:\n%s", tok, budget, view)
		}
		if !strings.Contains(view, "+70 not shown") && !strings.Contains(view, " not shown: ") {
			t.Fatalf("the teammates are not accounted for:\n%s", view)
		}
		if !mentioned(view, "mgr") {
			t.Errorf("the most relevant teammate, the manager, is not named first:\n%s", view)
		}
		if n := strings.Count(view, "backend-worker-"); n >= 69 || !regexp.MustCompile(`not shown: .* and \d+ more`).MatchString(view) {
			t.Errorf("the line named %d teammates and did not count the rest:\n%s", n, view)
		}
	}
}
