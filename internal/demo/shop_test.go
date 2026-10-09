package demo

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
)

// The shop scenario is the showcase: a team builds a small shop in git worktrees, and the log it writes is what the README's recordings
// are made from. So it is tested for the things the recordings show, not only for finishing: the merge queue's bounce, mail, the
// repetition guard, a compaction, and a result in the checkout that passes the project's own check.
func TestTheShopTeamBuildsTheShopThroughTheMergeQueue(t *testing.T) {
	if testing.Short() {
		t.Skip("the shop scenario runs a team for several seconds")
	}
	for _, tool := range []string{"git", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("the shop scenario needs %s", tool)
		}
	}
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	// A quarter of the time the shop takes (and of its cache lifetime): the same story, in about a fifth of a minute.
	rep, err := Run(ctx, Options{Scenario: "shop", Scale: 0.25, Dir: t.TempDir(), Out: &out})
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	t.Log("\n" + out.String())
	if rep.Tasks != 8 || rep.TasksDone != 8 {
		t.Fatalf("%d of %d tasks accepted, want 8 of 8", rep.TasksDone, rep.Tasks)
	}
	if rep.Agents != 9 {
		t.Errorf("%d agents took part, want the manager, three scouts, four writers and a reviewer", rep.Agents)
	}
	c, err := CountShop(rep.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if c.Merged < 4 {
		t.Errorf("%d merges, want the four writers' work integrated", c.Merged)
	}
	if c.Bounced < 1 {
		t.Errorf("the merge queue sent nothing back: two workers each keep the one-DefaultPort rule in their own tree and break it together")
	}
	if c.Mail < 2 {
		t.Errorf("%d messages between agents, want the backend's contract and the tester's question", c.Mail)
	}
	if c.Nudges < 1 {
		t.Errorf("the repetition guard never spoke: a worker runs the same failing check four times")
	}
	if c.Compactions < 1 {
		t.Errorf("no thread was folded: the worker that reads the most has a thread over the soft limit")
	}
	if rep.HitRatio < 0.5 {
		t.Errorf("hit ratio %.2f: the shared prefix is not being shared", rep.HitRatio)
	}

	// What the team made is in the checkout, and it passes the project's own check: the merge queue verified every integration.
	ws := rep.Workspace
	for _, f := range []string{"shop/catalogue/items.go", "shop/catalogue/store.go", "shop/cart/cart.go", "shop/web/handlers.go", "tests/smoke.sh"} {
		if _, err := os.Stat(filepath.Join(ws, f)); err != nil {
			t.Errorf("the result lacks %s: %v", f, err)
		}
	}
	cart, _ := os.ReadFile(filepath.Join(ws, "shop/cart/cart.go"))
	if strings.Contains(string(cart), "TODO(rounding)") {
		t.Errorf("the cart still says TODO(rounding): the worker was stopped before it fixed it")
	}
	check := exec.Command("sh", "verify.sh")
	check.Dir = ws
	if b, err := check.CombinedOutput(); err != nil {
		t.Errorf("the project's check fails on the result: %v\n%s", err, b)
	}

	// The log is a session the terminal UI can show: every agent is in it, the board is merged, the cache view has a story to tell.
	st, err := state.Fold(filepath.Join(rep.Dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sn := st.Snapshot()
	if sn.Stats.Unknown != 0 || sn.Stats.Bad != 0 || sn.Stats.Panics != 0 {
		t.Errorf("the terminal UI does not understand the log: %+v", sn.Stats)
	}
	if sn.Board.Counts.Merged != 8 {
		t.Errorf("the board shows %d tasks merged, want 8: %+v", sn.Board.Counts.Merged, sn.Board.Counts)
	}
	if !sn.Merge.Seen || sn.Merge.Counts.Bounced < 1 {
		t.Errorf("the merge queue in the log: %+v", sn.Merge.Counts)
	}
	if sn.Mail.Counts.Sent < 2 {
		t.Errorf("mail in the log: %+v", sn.Mail.Counts)
	}
	if sn.Totals.Compactions < 1 {
		t.Errorf("compactions in the log: %d", sn.Totals.Compactions)
	}

	// The team is the default one while the pieces are built: the reviewer is on it before the first writer reports, and it runs its
	// checks only after the last writer's piece went through the merge queue.
	var reviewerSpawn, lastDone, firstCheck uint64
	if err := events.Scan(filepath.Join(rep.Dir, "events.jsonl"), func(e events.Event) error {
		var p struct {
			ID    string
			Name  string
			Input struct{ Action, Command string }
		}
		if json.Unmarshal(e.Data, &p) != nil {
			return nil
		}
		switch {
		case e.Type == events.TypeAgentSpawn && p.ID == "rv-1":
			reviewerSpawn = e.Seq
		case e.Type == events.TypeToolCall && p.Name == "task" && p.Input.Action == "done" && e.Agent != "rv-1" && e.Agent != "mgr" && !strings.HasPrefix(e.Agent, "sc-"):
			lastDone = e.Seq // a writer's done call: the last one is the last piece
		case e.Type == events.TypeToolCall && e.Agent == "rv-1" && p.Name == "bash" && firstCheck == 0 && !strings.HasPrefix(p.Input.Command, "sleep"):
			firstCheck = e.Seq // not its waits, which are a sleep in tool time
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if reviewerSpawn == 0 || lastDone == 0 || firstCheck == 0 {
		t.Fatalf("the log lacks the reviewer's spawn (%d), a writer's done (%d) or the reviewer's first check (%d)", reviewerSpawn, lastDone, firstCheck)
	}
	if reviewerSpawn > lastDone {
		t.Errorf("the reviewer joined (seq %d) after the last writer was done (seq %d): the team was not eight workers while the pieces were built", reviewerSpawn, lastDone)
	}
	if firstCheck < lastDone {
		t.Errorf("the reviewer ran a check (seq %d) before the last piece was merged (seq %d)", firstCheck, lastDone)
	}
}

func TestAnUnknownScenarioIsRefused(t *testing.T) {
	if _, err := Run(context.Background(), Options{Scenario: "casino"}); err == nil || !strings.Contains(err.Error(), "unknown scenario") {
		t.Fatalf("got %v", err)
	}
}

// The shop's check is a real script: it passes on the project as it starts, and fails on the two mistakes the scenario plants.
func TestTheShopsCheckPassesOnTheStartAndFailsOnItsRules(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh")
	}
	run := func(dir string) (string, bool) {
		cmd := exec.Command("sh", "verify.sh")
		cmd.Dir = dir
		b, err := cmd.CombinedOutput()
		return string(b), err == nil
	}
	dir := t.TempDir()
	for name, body := range shopFiles() {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if out, ok := run(dir); !ok {
		t.Fatalf("the project as it starts must pass its own check:\n%s", out)
	}
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("shop/catalogue/port.go", "package catalogue\n\nconst DefaultPort = 8080\n")
	if out, ok := run(dir); !ok {
		t.Fatalf("one DefaultPort is the rule:\n%s", out)
	}
	write("shop/web/port.go", "package web\n\nconst DefaultPort = 8080\n")
	if out, ok := run(dir); ok || !strings.Contains(out, "2 definitions of DefaultPort") {
		t.Fatalf("two DefaultPort definitions must fail the check (the merge queue's bounce):\n%s", out)
	}
	write("shop/web/port.go", "const X = 1\n")
	if out, ok := run(dir); ok || !strings.Contains(out, "package clause") {
		t.Fatalf("a Go file with no package clause must fail:\n%s", out)
	}
	write("shop/web/port.go", "package web\n\n// FIXME\n")
	if out, ok := run(dir); ok || !strings.Contains(out, "FIXME") {
		t.Fatalf("FIXME must fail:\n%s", out)
	}
}

// The cart's own check is what a worker loops on: it fails while the marker is there and passes when it is gone.
func TestTheCartsCheckFailsUntilTheRoundingIsDone(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("needs sh")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "shop/cart"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "checks.sh"), []byte(checksScript), 0o644); err != nil {
		t.Fatal(err)
	}
	for body, want := range map[string]bool{cartGoFirst: false, cartGoFixed: true} {
		if err := os.WriteFile(filepath.Join(dir, "shop/cart/cart.go"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "checks.sh")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if (err == nil) != want {
			t.Errorf("checks.sh on %.40q...: passed=%v, want %v\n%s", body, err == nil, want, out)
		}
	}
}
