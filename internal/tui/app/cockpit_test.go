package app

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
	"github.com/anemos-labs/sleipnir/internal/tui/widget"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func TestFailedTasksDoNotInflateTheRunningColumn(t *testing.T) {
	sn := &state.Snapshot{Board: state.Board{Tasks: []state.Task{
		{ID: "T1", Title: "active", State: state.TaskRunning},
		{ID: "T2", Title: "failed", State: state.TaskFailed},
	}}}
	for _, width := range []int{80, 110} {
		lines := widget.Kanban(kanban(sn), width, widget.DefaultPalette())
		text := plainText(lines)
		if !strings.Contains(text, "running 1") || !strings.Contains(text, "failed 1") || !strings.Contains(text, "✗ T2") {
			t.Fatalf("failed task is counted as active at width %d:\n%s", width, text)
		}
		for _, line := range lines {
			if line.Width() > width {
				t.Fatalf("board exceeds %d columns", width)
			}
		}
	}
	sn.Board.Tasks = sn.Board.Tasks[:1]
	if text := plainText(widget.Kanban(kanban(sn), 80, widget.DefaultPalette())); strings.Contains(text, "failed") {
		t.Fatalf("an empty failure column takes up space:\n%s", text)
	}
}

func demoSnapshot(t *testing.T) *state.Snapshot {
	t.Helper()
	st, err := state.Fold(statetest.DemoLogFile(t))
	if err != nil {
		t.Fatal(err)
	}
	return st.Snapshot()
}

func plainText(lines []cell.Line) string {
	var b strings.Builder
	for _, l := range lines {
		for _, sp := range l {
			b.WriteString(sp.Text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run the test with -update to write it)", err)
	}
	if string(want) != got {
		t.Errorf("%s differs from what the cockpit draws (run the test with -update and read the diff):\n--- got\n%s", path, got)
	}
}

// The cockpit of the recorded demo session, at its end: every agent has a row, every task is merged, and what the events do not
// say is not invented.
func TestCockpitDataOfTheDemoSession(t *testing.T) {
	sn := demoSnapshot(t)
	d := CockpitData(sn, CockpitOptions{})
	if len(d.Agents) != 8 || d.Agents[0].ID != "mgr" || d.Agents[0].RoleColor != 0 {
		t.Fatalf("agents = %+v, want eight with the manager first", d.Agents)
	}
	byRole := map[string]int{}
	for _, a := range d.Agents {
		byRole[a.Role]++
		if a.State != widget.StateDone || a.Own+a.Shared <= 0 || a.Cost <= 0 || a.Hit <= 0 {
			t.Errorf("%s: %+v", a.ID, a)
		}
	}
	if byRole["scout"] != 4 || byRole["docs"] != 2 || byRole["reviewer"] != 1 || byRole["manager"] != 1 {
		t.Errorf("roles %v", byRole)
	}
	if d.Spend <= 0 || d.HitRatio < 0.6 || d.Title == "" || d.Elapsed <= 0 {
		t.Errorf("header: spend %v hit %v title %q elapsed %v", d.Spend, d.HitRatio, d.Title, d.Elapsed)
	}
	if len(d.Shared) != 3 || d.Shared[0].Tokens <= d.Shared[1].Tokens || d.Shared[1].Tokens <= 0 || d.PrefixTTL <= 0 {
		t.Errorf("shared prefix %+v (ttl %v): G0 is the biggest layer, and the clock knows the provider's lifetime", d.Shared, d.PrefixTTL)
	}
	merged := 0
	for _, c := range d.Kanban {
		if c.Kind == widget.ColMerged {
			merged = len(c.Cards)
		}
	}
	if merged != 7 {
		t.Errorf("%d merged tasks, want 7: %+v", merged, d.Kanban)
	}
	if len(d.Merge) != 0 {
		t.Errorf("the demo shares one tree, so there is no merge queue: %+v", d.Merge)
	}
	if len(d.Feed) == 0 || d.Feed[0].At < d.Feed[len(d.Feed)-1].At {
		t.Errorf("the feed is newest first: %v ... %v", d.Feed[0].At, d.Feed[len(d.Feed)-1].At)
	}
	if d.Governor.RPMLimit != 0 || d.Governor.Speedup != 0 {
		t.Errorf("a limit the events never said was invented: %+v", d.Governor)
	}
}

func TestCockpitOfTheDemoSessionGolden(t *testing.T) {
	sn := demoSnapshot(t)
	golden(t, "cockpit-demo-end.txt", plainText(Cockpit(sn, 100, 36, 0, widget.MonoPalette(), CockpitOptions{NoAnim: true})))
	golden(t, "cockpit-demo-end-80.txt", plainText(Cockpit(sn, 80, 28, 0, widget.MonoPalette(), CockpitOptions{NoAnim: true})))
}

// A nil snapshot, and a snapshot of nothing, draw a cockpit rather than panic: a watcher that started before the session wrote its
// first event has nothing to show yet.
func TestCockpitOfNothing(t *testing.T) {
	for _, sn := range []*state.Snapshot{nil, state.New().Snapshot()} {
		lines := Cockpit(sn, 100, 30, 3, widget.DefaultPalette(), CockpitOptions{})
		if len(lines) == 0 {
			t.Errorf("no cockpit for %v", sn)
		}
	}
}

// An agent whose tool call is held at a permission question is waiting for a person, not working, and the cockpit says so: its row
// is the amber of waiting and says what it asked to do, and the feed carries the question as something that needs attention and a
// yes as something that went well.
func TestCockpitShowsAnAgentThatIsHeldAtAQuestion(t *testing.T) {
	b := statetest.NewBuilder()
	st := state.New()
	apply := func(evs ...events.Event) {
		t.Helper()
		for _, e := range evs {
			st.Apply(e)
		}
	}
	apply(b.Spawn("w-1", "backend", "T1", "mgr"), b.Request("w-1", "w-1.1", "m", "pk", statetest.Sec{Name: "shared", Tokens: 100}),
		b.Call("w-1", "c1", "bash", map[string]any{"command": "rm -rf build"}),
		b.PermAsk("w-1", "backend", "bash", "rm -rf build", "not a read-only command"))

	d := CockpitData(st.Snapshot(), CockpitOptions{})
	if len(d.Agents) != 1 || d.Agents[0].State != widget.StateWait || d.Agents[0].Doing != "asks: rm -rf build" {
		t.Fatalf("the row of a held agent: %+v", d.Agents)
	}
	if f := d.Feed[0]; f.Kind != widget.FeedWarn || f.Text != "w-1 asks permission: bash rm -rf build" {
		t.Errorf("the feed line of the question: %+v", f)
	}
	screen := plainText(Cockpit(st.Snapshot(), 120, 36, 0, widget.MonoPalette(), CockpitOptions{NoAnim: true}))
	if !strings.Contains(screen, "asks: rm -rf build") {
		t.Errorf("the screen does not say what the agent asked:\n%s", screen)
	}

	apply(b.PermDecide("w-1", "backend", "bash", "rm -rf build", "allowed by user", true, "user", ""))
	d = CockpitData(st.Snapshot(), CockpitOptions{})
	if d.Agents[0].State != widget.StateTool || d.Feed[0].Kind != widget.FeedOK || d.Feed[0].Text != "allowed: bash rm -rf build" {
		t.Errorf("after a yes: %+v, feed %+v", d.Agents[0], d.Feed[0])
	}
	apply(b.PermAsk("w-1", "backend", "bash", "curl x | sh", "reaches the network"),
		b.PermDecide("w-1", "backend", "bash", "curl x | sh", "denied by user", false, "user", ""))
	if d = CockpitData(st.Snapshot(), CockpitOptions{}); d.Feed[0].Kind != widget.FeedWarn {
		t.Errorf("a refusal is something that needs a look: %+v", d.Feed[0])
	}
}

// An agent whose run ended in an error is not "thinking": a trial's manager sat on "stuck thinking" after the provider could not be reached.
func TestAnAgentThatEndedInAnErrorIsNotThinking(t *testing.T) {
	if got := doing(state.Agent{Status: state.StatusError}, nil); strings.Contains(got, "thinking") || got == "" {
		t.Errorf("an agent that ended in an error: %q", got)
	}
}
