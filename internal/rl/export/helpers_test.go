package export_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/export"
	"github.com/anemos-labs/sleipnir/internal/rl/traj"
	"github.com/anemos-labs/sleipnir/internal/rl/traj/trajtest"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixtureDir = "../traj/testdata/agent_run"

// fixture returns the recorded agent run as an episode and its prompt resolver.
func fixture(t testing.TB) (export.Source, *traj.Run) {
	t.Helper()
	r, err := traj.Open(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	ep, err := r.Episode(traj.Options{TaskID: "fixture", Policy: rl.PolicyRef{Model: "mock-1"}})
	if err != nil {
		t.Fatal(err)
	}
	return export.Source{Episode: ep, Prompts: traj.Resolver{Run: r}}, r
}

// run exports srcs and returns the output and stats.
func run(t testing.TB, srcs []export.Source, o export.Options) (string, export.Stats) {
	t.Helper()
	var buf bytes.Buffer
	st, err := export.Export(&buf, srcs, o)
	if err != nil {
		t.Fatalf("export %s: %v", o.Format, err)
	}
	return buf.String(), st
}

// lines splits JSONL output.
func lines(out string) []string {
	var ls []string
	sc := bufio.NewScanner(strings.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 256<<20)
	for sc.Scan() {
		if sc.Text() != "" {
			ls = append(ls, sc.Text())
		}
	}
	return ls
}

// decode parses every line of out into maps.
func decode(t testing.TB, out string) []map[string]any {
	t.Helper()
	var recs []map[string]any
	for _, l := range lines(out) {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad JSONL line: %v\n%.200s", err, l)
		}
		recs = append(recs, m)
	}
	return recs
}

// fake credentials, assembled at run time so this file holds no secret-shaped
// literal (see the note in package redact's tests).
func frag(parts ...string) string { return strings.Join(parts, "") }

var (
	ghToken = frag("gh", "p_", "16C7e42F292c6912E7710c838347Ae178B4a")
	awsKey  = frag("AKI", "AIOSFODNN7", "EXAMPLE")
)

// variant describes one rollout of a synthetic task.
type variant struct {
	sample   int
	plan     string  // text of the first assistant turn
	reward   float64 // episode reward
	pass     bool
	model    string
	resultTx string // tool result text
	tokens   bool
	hot      string
	extra    func(a *trajtest.Agent)
	user     string // first user message (default: fix the bug in api/a.go)
	input    string // arguments of the read call (default: {"path":"api/a.go"})
	stuck    bool   // the repetition guard ends the run after extra: no final answer
	ite      float64
}

// rollout records one single-agent run of task "T": user "fix the bug", a read
// call, a final answer, and a verifier outcome. All variants share the first
// prompt, so their first steps are comparable.
func rollout(t testing.TB, v variant) export.Source {
	t.Helper()
	b := trajtest.New()
	model := v.model
	if model == "" {
		model = "policy-1"
	}
	a := b.Agent("solo", "backend", model)
	a.Tokens = v.tokens
	a.Hot = v.hot
	user, input := v.user, v.input
	if user == "" {
		user = "fix the bug in api/a.go"
	}
	if input == "" {
		input = `{"path":"api/a.go"}`
	}
	a.User(user)
	res := v.resultTx
	if res == "" {
		res = "package api"
	}
	a.Step(trajtest.Call{Text: v.plan, Tool: "read", Input: input, Result: res})
	if v.extra != nil {
		v.extra(a)
	}
	if v.stuck {
		b.Emit("solo", events.TypeAgentStuck, map[string]any{"phase": "stop", "error": "agent solo: agent stuck"})
		b.Emit("swarm", events.TypeAgentEnd, map[string]any{"id": "solo", "state": "failed"})
	} else {
		a.Step(trajtest.Call{Text: "fixed: " + v.plan})
	}
	b.Outcome("verifier", v.pass, map[bool]float64{true: 1, false: 0}[v.pass])
	run := traj.OpenWith(b.Events(), b.Blobs)
	ep, err := run.Episode(traj.Options{TaskID: "T", Sample: v.sample, Policy: rl.PolicyRef{Model: "policy-1"}})
	if err != nil {
		t.Fatal(err)
	}
	ep.Reward = rl.Reward{Total: v.reward, Components: map[string]float64{"outcome": v.reward}}
	ep.Cost.ITE = v.ite
	return export.Source{Episode: ep, Prompts: traj.Resolver{Run: run}}
}

// group returns n rollouts of one task with rewards 1, 0, 1, 0, ... and different plans.
func group(t testing.TB, n int) []export.Source {
	t.Helper()
	var out []export.Source
	for i := 0; i < n; i++ {
		pass := i%2 == 0
		r := 0.0
		if pass {
			r = 1
		}
		out = append(out, rollout(t, variant{sample: i, plan: fmt.Sprintf("plan %c", 'A'+i), reward: r, pass: pass, tokens: true}))
	}
	return out
}

// swarmSource records the manager + worker + reviewer swarm used by the traj
// tests, with a compaction, a hot tail, mail and an epoch.
func swarmSource(t testing.TB, tokens bool) export.Source {
	t.Helper()
	b := trajtest.New()
	mgr := b.SpawnRoot("mgr", "manager", "policy-1")
	mgr.Tokens = tokens
	mgr.User("Ship the API")
	mgr.Step(trajtest.Call{Text: "plan it", Tool: "task", Input: `{"action":"create","title":"api"}`, Result: "created T1"})
	mgr.Step(trajtest.Call{Text: "spawn a worker", Tool: "spawn", Input: `{"role":"backend","task":"T1"}`, Result: "started be-1"})
	wk := b.Spawn("mgr", "be-1", "backend", "policy-1", "T1")
	wk.Tokens = tokens
	wk.User("Begin task T1")
	wk.Step(trajtest.Call{Text: "look", Tool: "read", Input: `{"path":"api/b.go"}`, Result: "package api"})
	wk.Step(trajtest.Call{Text: "edit", Tool: "edit", Input: `{"path":"api/a.go","old_string":"x","new_string":"y"}`, Result: "ok"})
	wk.Step(trajtest.Call{Text: "edit again", Tool: "edit", Input: `{"path":"api/a.go","old_string":"y","new_string":"z"}`, Result: "changed since you last read it", IsError: true})
	wk.Fork(`{"keep_from":"t5"}`, "")
	wk.Commit(5, false, "pays back within 3 turns")
	wk.Step(trajtest.Call{Text: "read again", Tool: "read", Input: `{"path":"api/b.go"}`, Result: "package api"})
	wk.Hot = "<live>board v3</live>"
	wk.Step(trajtest.Call{Text: "test", Tool: "bash", Input: `{"command":"go test ./..."}`, Result: "ok"})
	wk.Step(trajtest.Call{Text: "test again", Tool: "bash", Input: `{"command":"go test ./..."}`, Result: "ok"})
	b.Mail("be-1", "mgr", "info", "api done")
	wk.Hot = ""
	wk.Step(trajtest.Call{Text: "wrap up", Tool: "task", Input: `{"action":"done","id":"T1","text":"done"}`, Result: "T1 is now in review"})
	wk.Step(trajtest.Call{Text: "all done"})
	b.End(wk, "idle")
	rev := b.Spawn("mgr", "rv-1", "reviewer", "teacher-x", "T1")
	rev.Tokens = tokens
	rev.User("Review T1")
	rev.Step(trajtest.Call{Text: "review", Tool: "read", Input: `{"path":"api/b.go"}`, Result: "package api"})
	rev.Step(trajtest.Call{Text: "lgtm"})
	b.End(rev, "idle")
	b.EpochCommit("promote")
	mgr.SyncShared("shared v2")
	mgr.Step(trajtest.Call{Text: "accept", Tool: "task", Input: `{"action":"accept","id":"T1"}`, Result: "T1 accepted"})
	mgr.Step(trajtest.Call{Text: "shipped"})
	b.Outcome("verifier", true, 1)
	run := traj.OpenWith(b.Events(), b.Blobs)
	ep, err := run.Episode(traj.Options{TaskID: "swarm-1", Policy: rl.PolicyRef{Model: "policy-1", Checkpoint: "ckpt-7"},
		Harness: rl.HarnessRef{Version: "0.1.0"}})
	if err != nil {
		t.Fatal(err)
	}
	ep.Reward = rl.Reward{Total: 0.9, Components: map[string]float64{"outcome": 1, "cost": -0.1}}
	for i := range ep.Agents {
		if ep.Agents[i].Role == "backend" {
			ep.Agents[i].Reward = rl.Reward{Total: 0.95, Components: map[string]float64{"outcome": 1, "evidence": 0.05, "cost": -0.1}}
		}
	}
	return export.Source{Episode: ep, Prompts: traj.Resolver{Run: run}}
}

// golden compares got with testdata/golden/<name>, rewriting it with -update.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", "golden", name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s (run with -update): %v", path, err)
	}
	if string(want) != got {
		t.Fatalf("output differs from %s (run with -update to accept):\n%s", path, firstDiff(string(want), got))
	}
}

func firstDiff(want, got string) string {
	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			return fmt.Sprintf("line %d:\n want: %.300s\n  got: %.300s", i+1, w, g)
		}
	}
	return "(identical)"
}

// wireEqual compares two wire messages after normalising tool-call arguments,
// which the steps format writes as JSON objects and the wire as JSON strings.
func wireEqual(t testing.TB, fromRecord any, fromBuild json.RawMessage) bool {
	t.Helper()
	a := normalize(t, fromRecord)
	var b any
	if err := json.Unmarshal(fromBuild, &b); err != nil {
		t.Fatal(err)
	}
	b = normalize(t, b)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}

// normalize rewrites function.arguments of tool calls into parsed JSON.
func normalize(t testing.TB, v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return v
	}
	calls, _ := m["tool_calls"].([]any)
	for _, c := range calls {
		cm, _ := c.(map[string]any)
		fn, _ := cm["function"].(map[string]any)
		if s, ok := fn["arguments"].(string); ok {
			var parsed any
			if json.Unmarshal([]byte(s), &parsed) == nil {
				fn["arguments"] = parsed
			}
		}
	}
	return m
}

// promptsOf returns the resolver's prompt for a step id of an episode.
func promptOf(t testing.TB, src export.Source, stepID string) *core.Prompt {
	t.Helper()
	for ai := range src.Episode.Agents {
		for si := range src.Episode.Agents[ai].Steps {
			st := &src.Episode.Agents[ai].Steps[si]
			if st.ID == stepID {
				p, err := src.Prompts.Prompt(src.Episode, st)
				if err != nil {
					t.Fatal(err)
				}
				return p
			}
		}
	}
	t.Fatalf("no step %s", stepID)
	return nil
}

var _ = events.TypeToolCall

// rolloutWithThinking records a run whose first assistant turn carries a reasoning
// block with provider-native reasoning_details.
func rolloutWithThinking(t testing.TB) export.Source {
	t.Helper()
	b := trajtest.New()
	a := b.Agent("solo", "backend", "policy-1")
	a.User("think about it")
	a.Step(trajtest.Call{Thinking: "thinking hard", ReasoningWire: `[{"type":"reasoning.text","text":"thinking hard"}]`, Text: "reading", Tool: "read", Input: `{"path":"a.go"}`, Result: "package a"})
	a.Step(trajtest.Call{Text: "done"})
	b.Outcome("verifier", true, 1)
	run := traj.OpenWith(b.Events(), b.Blobs)
	ep, err := run.Episode(traj.Options{TaskID: "think", Policy: rl.PolicyRef{Model: "policy-1"}})
	if err != nil {
		t.Fatal(err)
	}
	return export.Source{Episode: ep, Prompts: traj.Resolver{Run: run}}
}
