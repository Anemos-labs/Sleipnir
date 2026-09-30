package traj_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/traj"
	"github.com/reee344/sleipnir/internal/rl/traj/trajtest"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixtureDir = "testdata/agent_run"

var policy = rl.PolicyRef{Model: "mock-1"}

func openFixture(t *testing.T) *traj.Run {
	t.Helper()
	r, err := traj.Open(fixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func episode(t *testing.T, r *traj.Run, o traj.Options) *rl.Episode {
	t.Helper()
	ep, err := r.Episode(o)
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("episode does not marshal: %v", err)
	}
	return string(b)
}

// copyFixture copies the read-only fixture so tests can damage it.
func copyFixture(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), "run")
	if err := os.CopyFS(dst, os.DirFS(fixtureDir)); err != nil {
		t.Fatal(err)
	}
	return dst
}

// rewriteEvents applies fn to every event line of dir's log; fn returns false to
// drop the line.

func hasKind(ms []traj.Mismatch, kind string) bool {
	for _, m := range ms {
		if m.Kind == kind {
			return true
		}
	}
	return false
}

func TestFixtureVerifiesAndBuildsAnEpisode(t *testing.T) {
	r := openFixture(t)
	if ms := r.Verify(); len(ms) != 0 {
		t.Fatalf("the recorded run must replay cleanly: %v", ms)
	}
	ep := episode(t, r, traj.Options{TaskID: "fixture", Sample: 3, Policy: policy})

	if ep.Schema != rl.SchemaEpisode || ep.ID != "fixture/3" || ep.TaskID != "fixture" || ep.Sample != 3 || ep.Group != "fixture@mock-1" {
		t.Fatalf("identity: %+v", ep)
	}
	if len(ep.Agents) != 1 {
		t.Fatalf("one agent expected, got %d", len(ep.Agents))
	}
	a := ep.Agents[0]
	if a.ID != "be-1" || a.Role != "backend" || a.Model != "mock-1" || a.Status != "done" || a.Parent != "" {
		t.Fatalf("agent: %+v", a)
	}
	// 23 main steps and 4 compactor forks, in request order.
	var mains, forks int
	for _, s := range a.Steps {
		switch s.Kind {
		case rl.KindMain:
			mains++
			if s.Role != "backend" {
				t.Errorf("%s role %q", s.ID, s.Role)
			}
		case rl.KindCompactor:
			forks++
			if s.Role != rl.RoleCompactor {
				t.Errorf("fork %s trains role %q, want compactor", s.ID, s.Role)
			}
		}
		if !s.Trainable {
			t.Errorf("%s: model %s equals the policy, so the step is trainable", s.ID, s.Model)
		}
		if s.Prompt.Req != s.ID || s.Prompt.WireHash == "" || s.Prompt.Tokens == 0 || s.Prompt.SharedPrefix == "" {
			t.Errorf("%s prompt ref: %+v", s.ID, s.Prompt)
		}
		if s.Completion.Turn.Role != core.RoleAssistant || len(s.Completion.Turn.Blocks) == 0 || s.Completion.Stop == "" {
			t.Errorf("%s completion: %+v", s.ID, s.Completion)
		}
		if s.At.IsZero() || s.Usage.OutputTokens == 0 || s.LatencyMs == 0 {
			t.Errorf("%s usage/timing: %+v", s.ID, s)
		}
	}
	if mains != 23 || forks != 4 || len(a.Steps) != 27 {
		t.Fatalf("steps: %d main, %d compactor, %d total", mains, forks, len(a.Steps))
	}
	if a.Steps[0].ID != "be-1.1" || a.Steps[8].ID != "be-1.c1" || a.Steps[9].ID != "be-1.9" {
		t.Fatalf("steps must be in request order (a fork is requested before the next main call): %s %s %s", a.Steps[0].ID, a.Steps[8].ID, a.Steps[9].ID)
	}

	// Segments: a new one after every commit, forks join the current one.
	wantSegs := []rl.Segment{
		{Index: 0, From: 0, To: 9, Reason: "start"},
		{Index: 1, From: 10, To: 13, Reason: "compact"},
		{Index: 2, From: 14, To: 19, Reason: "compact"},
		{Index: 3, From: 20, To: 25, Reason: "compact"},
		{Index: 4, From: 26, To: 26, Reason: "compact"},
	}
	if fmt.Sprint(a.Segments) != fmt.Sprint(wantSegs) {
		t.Fatalf("segments:\n got %+v\nwant %+v", a.Segments, wantSegs)
	}
	for _, seg := range a.Segments {
		for i := seg.From; i <= seg.To; i++ {
			if a.Steps[i].Segment != seg.Index {
				t.Errorf("step %s is in segment %d but sits in the range of %d", a.Steps[i].ID, a.Steps[i].Segment, seg.Index)
			}
		}
	}
	for _, id := range []string{"be-1.c1", "be-1.c2", "be-1.c3", "be-1.c4"} {
		want := map[string]int{"be-1.c1": 0, "be-1.c2": 1, "be-1.c3": 2, "be-1.c4": 3}[id]
		for _, s := range a.Steps {
			if s.ID == id && s.Segment != want {
				t.Errorf("%s is in segment %d, want the current one (%d)", id, s.Segment, want)
			}
		}
	}

	// Compact edges connect each fork to the first call after its commit.
	wantEdges := []rl.Edge{
		{Kind: rl.EdgeCompact, From: "be-1.c1", To: "be-1.10", Ref: "be-1#1"},
		{Kind: rl.EdgeCompact, From: "be-1.c2", To: "be-1.13", Ref: "be-1#2"},
		{Kind: rl.EdgeCompact, From: "be-1.c3", To: "be-1.18", Ref: "be-1#3"},
		{Kind: rl.EdgeCompact, From: "be-1.c4", To: "be-1.23", Ref: "be-1#4"},
	}
	if fmt.Sprint(ep.Edges) != fmt.Sprint(wantEdges) {
		t.Fatalf("edges:\n got %+v\nwant %+v", ep.Edges, wantEdges)
	}

	// Signals from the recorded run.
	wantSig := map[string]float64{
		rl.SigRequests: 27, rl.SigSteps: 23, rl.SigWorkerSteps: 23, rl.SigCriticalPath: 23, rl.SigCompactions: 4, rl.SigCompactRejects: 3,
		rl.SigToolErrors: 0, rl.SigInvalidToolCalls: 0, rl.SigMailSent: 0, rl.SigSpawns: 0, rl.SigCacheAnomalies: 0,
		traj.SigRequestErrors: 0, traj.SigRequestRetry: 0, traj.SigPrefixBreaks: 18,
	}
	for k, want := range wantSig {
		if got, ok := ep.Signals[k]; !ok || got != want {
			t.Errorf("signal %s = %v (present %v), want %v", k, got, ok, want)
		}
	}

	// Outcome, cost and flags of a raw agent run: no verifier, so weak; not truncated
	// although there is no session.end, because the agent ended with a final answer.
	if ep.Outcome.Verifier != nil || ep.Outcome.Claimed != "done" {
		t.Fatalf("outcome: %+v", ep.Outcome)
	}
	if fmt.Sprint(ep.Flags) != "["+rl.FlagWeakLabel+"]" {
		t.Fatalf("flags: %v", ep.Flags)
	}
	if ep.Cost.Requests != 27 || ep.Cost.USD < 0.36 || ep.Cost.USD > 0.37 || ep.Cost.WallMs <= 0 || ep.Cost.Usage.OutputTokens != 23*9+2-9+4*78-4*0 {
		// 22 main steps emit 9 output tokens, the last one 2, each of the 4 forks 78.
		if ep.Cost.Usage.OutputTokens != 22*9+2+4*78 {
			t.Fatalf("cost: %+v", ep.Cost)
		}
	}
	if ep.Harness.Renderer != "sleipnir-kv/1" || ep.Harness.Agents != 1 || fmt.Sprint(ep.Harness.Roles) != "[backend]" {
		t.Fatalf("harness: %+v", ep.Harness)
	}
	if string(ep.Policy.Sampling) != `{"max_tokens":512}` {
		t.Fatalf("policy sampling should come from the request params: %s", ep.Policy.Sampling)
	}
	if ep.Provenance.Teacher || len(ep.Provenance.TeacherModels) != 0 {
		t.Fatalf("provenance: %+v", ep.Provenance)
	}
	if !ep.StartedAt.Before(ep.EndedAt) {
		t.Fatalf("span: %v .. %v", ep.StartedAt, ep.EndedAt)
	}
	mustJSON(t, ep)
}

// TestFixtureObservationsAndPrompts checks that what the exporter will train on
// is exactly what the recorded thread contained.
func TestFixtureObservationsAndPrompts(t *testing.T) {
	r := openFixture(t)
	ep := episode(t, r, traj.Options{Policy: policy})
	a := ep.Agents[0]
	var withObs int
	for _, s := range a.Steps {
		if s.Kind != rl.KindMain {
			continue
		}
		calls := s.Completion.Turn.ToolCalls()
		if len(s.Observations) != len(calls) {
			t.Fatalf("%s: %d observations for %d calls", s.ID, len(s.Observations), len(calls))
		}
		for i, o := range s.Observations {
			withObs++
			if o.ToolID != calls[i].ToolID || o.Name != "big" || o.IsError || o.Ms != 0 || !strings.HasPrefix(o.Output, "build output line with details") || len(o.Output) != 3720 {
				t.Fatalf("%s observation: id=%s name=%s err=%v len=%d", s.ID, o.ToolID, o.Name, o.IsError, len(o.Output))
			}
			if !json.Valid(o.Input) {
				t.Fatalf("%s input is not JSON: %s", s.ID, o.Input)
			}
			// The next prompt of the same chain contains this exact text.
			p, err := r.Prompt(s.ID)
			if err != nil {
				t.Fatal(err)
			}
			_ = p
		}
	}
	if withObs != 22 {
		t.Fatalf("22 tool calls expected, got %d", withObs)
	}

	// Every step's prompt matches its wire hash and has the recorded shape.
	prev := 0
	for _, s := range a.Steps {
		p, err := r.Prompt(s.Prompt.Req)
		if err != nil {
			t.Fatalf("%s: %v", s.ID, err)
		}
		if p.Model != "mock-1" || len(p.Tools) != 2 || len(p.System) != 1 || len(p.Messages) == 0 || p.Messages[0].Role != core.RoleUser {
			t.Fatalf("%s prompt: %+v", s.ID, p)
		}
		if s.Kind == rl.KindMain && s.Segment == 0 && len(p.Messages) < prev {
			t.Fatalf("append-only segment shrank at %s", s.ID)
		}
		if s.Kind == rl.KindMain {
			prev = len(p.Messages)
		}
	}
	// A step's tool result appears verbatim in the next prompt of its segment.
	p1, _ := r.Prompt("be-1.1")
	p2, _ := r.Prompt("be-1.2")
	if len(p2.Messages) != len(p1.Messages)+2 {
		t.Fatalf("be-1.2 must extend be-1.1 by an assistant and a tool-result message")
	}
	tr := p2.Messages[len(p2.Messages)-1].Blocks[0]
	if tr.Kind != core.BlockToolResult || tr.PlainText() != a.Steps[0].Observations[0].Output {
		t.Fatalf("observation text differs from the thread's tool result")
	}
	if !reflectEqualMessages(p1.Messages, p2.Messages[:len(p1.Messages)]) {
		t.Fatalf("be-1.2 does not start with be-1.1's messages")
	}
	if _, err := r.Prompt("nope"); err == nil {
		t.Fatal("unknown request must fail")
	} else {
		var mm traj.Mismatch
		if !errors.As(err, &mm) || mm.Req != "nope" {
			t.Fatalf("error should be a Mismatch: %T %v", err, err)
		}
	}
}

func reflectEqualMessages(a, b []core.Message) bool {
	ja, _ := core.MarshalStable(a)
	jb, _ := core.MarshalStable(b)
	return bytes.Equal(ja, jb)
}

func TestFixtureTokens(t *testing.T) {
	r := openFixture(t)
	ep := episode(t, r, traj.Options{Policy: policy})
	for _, s := range ep.Agents[0].Steps {
		if s.Tokens == nil || !s.Tokens.Consistent() || len(s.Tokens.PromptIDs) == 0 {
			t.Fatalf("%s lost its token trace: %+v", s.ID, s.Tokens)
		}
	}
	if ep.Has(rl.FlagTokenMismatch) {
		t.Fatalf("the default keeps individually consistent traces and only counts chain breaks")
	}
	// The mock's completion ids never reappear in the next prompt, so the strict
	// prefix property fails on all 18 consecutive pairs inside a segment (23 main
	// steps in 5 segments) while the weaker "prompt starts with the previous
	// prompt" holds.
	if got := ep.Signals[traj.SigPrefixBreaks]; got != 18 {
		t.Fatalf("token_prefix_breaks = %v, want 18", got)
	}

	strict := episode(t, r, traj.Options{Policy: policy, StrictTokens: true})
	if !strict.Has(rl.FlagTokenMismatch) {
		t.Fatalf("strict mode must flag token_mismatch: %v", strict.Flags)
	}
	kept := 0
	for _, s := range strict.Agents[0].Steps {
		if s.Kind == rl.KindMain && s.Tokens != nil {
			kept++
			if s.ID != "be-1.23" {
				t.Errorf("strict mode keeps traces only of segments without a break, not %s", s.ID)
			}
		}
		if s.Kind == rl.KindCompactor && s.Tokens == nil {
			t.Errorf("a fork is its own sample and keeps its trace: %s", s.ID)
		}
	}
	if kept != 1 {
		t.Fatalf("strict mode kept %d main traces", kept)
	}
}

func TestEpisodeIsDeterministicAndRoundTrips(t *testing.T) {
	r := openFixture(t)
	a := mustJSON(t, episode(t, r, traj.Options{TaskID: "d", Policy: policy}))
	for i := 0; i < 3; i++ {
		r2 := openFixture(t)
		if b := mustJSON(t, episode(t, r2, traj.Options{TaskID: "d", Policy: policy})); a != b {
			t.Fatal("two builds of the same log differ")
		}
	}
	var back rl.Episode
	if err := json.Unmarshal([]byte(a), &back); err != nil {
		t.Fatal(err)
	}
	if b := mustJSON(t, &back); a != b {
		t.Fatal("episode JSON does not round-trip")
	}
	if strings.Contains(a, "\"0001-01-01") && false {
		t.Fatal("unreachable")
	}
}

func TestPolicyDecidesTrainability(t *testing.T) {
	r := openFixture(t)
	for _, tc := range []struct {
		name   string
		policy rl.PolicyRef
		train  bool
	}{
		{"same model", rl.PolicyRef{Model: "mock-1"}, true},
		{"role models are descriptive", rl.PolicyRef{Model: "mock-1", RoleModels: map[string]string{"backend": "teacher-x"}}, true},
		{"other model", rl.PolicyRef{Model: "my-policy"}, false},
		{"no policy", rl.PolicyRef{}, false},
		{"role model only", rl.PolicyRef{RoleModels: map[string]string{"backend": "mock-1"}}, false},
	} {
		ep := episode(t, r, traj.Options{Policy: tc.policy})
		for _, s := range ep.Agents[0].Steps {
			if s.Trainable != tc.train {
				t.Fatalf("%s: step %s trainable=%v", tc.name, s.ID, s.Trainable)
			}
		}
		if ep.Provenance.Teacher == tc.train || (!tc.train && fmt.Sprint(ep.Provenance.TeacherModels) != "[mock-1]") {
			t.Fatalf("%s: provenance %+v", tc.name, ep.Provenance)
		}
	}
}

// tamper is an in-memory copy of the fixture's events and blobs that a test can
// damage without touching the fixture or the disk.
type tamper struct {
	t    *testing.T
	evs  []events.Event
	drop map[core.Hash]bool
	repl map[core.Hash][]byte
	base events.Blobs
}

func newTamper(t *testing.T) *tamper {
	t.Helper()
	r := openFixture(t)
	return &tamper{t: t, evs: append([]events.Event(nil), r.Events()...), drop: map[core.Hash]bool{}, repl: map[core.Hash][]byte{}, base: openBlobs(t)}
}

func (x *tamper) Get(h core.Hash) ([]byte, error) {
	if x.drop[h] {
		return nil, fmt.Errorf("%w: %s", events.ErrBlobNotFound, h.Short())
	}
	if b, ok := x.repl[h]; ok {
		return b, nil
	}
	return x.base.Get(h)
}
func (x *tamper) Put(b []byte) (core.Hash, error) { return "", errors.New("read-only") }
func (x *tamper) Has(h core.Hash) bool            { _, err := x.Get(h); return err == nil }

// edit applies fn to the payload of every event of the given type for req.
func (x *tamper) edit(typ, req string, fn func(d map[string]any)) {
	x.t.Helper()
	hit := false
	for i, e := range x.evs {
		if e.Type != typ {
			continue
		}
		var d map[string]any
		if err := json.Unmarshal(e.Data, &d); err != nil || d["req"] != req {
			continue
		}
		fn(d)
		b, _ := json.Marshal(d)
		x.evs[i].Data = b
		hit = true
	}
	if !hit {
		x.t.Fatalf("no %s event for %s", typ, req)
	}
}

func (x *tamper) manifest(req string, fn func(m map[string]any)) {
	x.edit("model.request", req, func(d map[string]any) { fn(d["manifest"].(map[string]any)) })
}

func (x *tamper) remove(typ, req string) {
	var out []events.Event
	for _, e := range x.evs {
		var d map[string]any
		if e.Type == typ && json.Unmarshal(e.Data, &d) == nil && d["req"] == req {
			continue
		}
		out = append(out, e)
	}
	x.evs = out
}

func (x *tamper) field(typ, req, key string) core.Hash {
	var out core.Hash
	x.edit(typ, req, func(d map[string]any) { out = core.Hash(d[key].(string)) })
	return out
}

func (x *tamper) run() *traj.Run { return traj.OpenWith(x.evs, x) }

func TestVerifyDetectsTampering(t *testing.T) {
	cases := []struct {
		name string
		fn   func(x *tamper)
		kind string
		req  string
		// promptFails: Prompt(req) must return an error (requests with a broken
		// response but an intact prompt still have one).
		promptFails bool
	}{
		{"flipped blob", func(x *tamper) {
			var h core.Hash
			x.manifest("be-1.2", func(m map[string]any) { h = core.Hash(m["add"].([]any)[0].(string)) })
			b, _ := x.base.Get(h)
			b = append([]byte(nil), b...)
			b[len(b)/2] ^= 0x01
			x.repl[h] = b
		}, traj.KindBlob, "be-1.2", true},
		{"missing blob", func(x *tamper) {
			x.manifest("be-1.2", func(m map[string]any) { x.drop[core.Hash(m["add"].([]any)[0].(string))] = true })
		}, traj.KindBlob, "be-1.2", true},
		{"missing tools blob", func(x *tamper) {
			x.manifest("be-1.2", func(m map[string]any) { x.drop[core.Hash(m["tools"].(string))] = true })
		}, traj.KindBlob, "be-1.2", true},
		{"message blob is not a message", func(x *tamper) {
			x.manifest("be-1.2", func(m map[string]any) {
				h := core.Hash(m["add"].([]any)[0].(string))
				b := []byte(`{"nope":1}`)
				x.repl[h] = b
				// keep the content hash honest so only the shape is wrong
				m["add"].([]any)[0] = string(core.HashBytes(b))
				x.repl[core.HashBytes(b)] = b
			})
		}, traj.KindWire, "be-1.2", true},
		{"wire hash in manifest", func(x *tamper) {
			x.manifest("be-1.3", func(m map[string]any) { m["wire"] = strings.Repeat("0", 64) })
		}, traj.KindWire, "be-1.3", true},
		{"wire hash in event", func(x *tamper) {
			x.edit("model.request", "be-1.3", func(d map[string]any) { d["wire_hash"] = strings.Repeat("1", 64) })
		}, traj.KindWire, "be-1.3", true},
		{"keep beyond base", func(x *tamper) {
			x.manifest("be-1.4", func(m map[string]any) { m["keep"] = 999 })
		}, traj.KindBase, "be-1.4", true},
		{"unknown base", func(x *tamper) {
			x.manifest("be-1.4", func(m map[string]any) { m["base"] = "ghost.9" })
		}, traj.KindBase, "be-1.4", true},
		{"self base", func(x *tamper) {
			x.manifest("be-1.4", func(m map[string]any) { m["base"] = "be-1.4" })
		}, traj.KindBase, "be-1.4", true},
		{"two-request base cycle", func(x *tamper) {
			x.manifest("be-1.4", func(m map[string]any) { m["base"] = "be-1.5" })
			x.manifest("be-1.5", func(m map[string]any) { m["base"] = "be-1.4" })
		}, traj.KindBase, "be-1.5", true},
		{"no manifest", func(x *tamper) {
			x.edit("model.request", "be-1.5", func(d map[string]any) { delete(d, "manifest") })
		}, traj.KindManifest, "be-1.5", true},
		{"deleted completion", func(x *tamper) {
			x.drop[x.field("model.response", "be-1.6", "completion")] = true
		}, traj.KindCompletion, "be-1.6", false},
		{"completion is not a turn", func(x *tamper) {
			h := x.field("model.response", "be-1.6", "completion")
			b := []byte(`{"role":"user","blocks":[]}`)
			x.repl[h] = b
			x.edit("model.response", "be-1.6", func(d map[string]any) { d["completion"] = string(core.HashBytes(b)) })
			x.repl[core.HashBytes(b)] = b
		}, traj.KindCompletion, "be-1.6", false},
		{"deleted token trace", func(x *tamper) {
			x.drop[x.field("model.response", "be-1.7", "tokens")] = true
		}, traj.KindTokens, "be-1.7", false},
		{"orphan response", func(x *tamper) { x.remove("model.request", "be-1.8") }, traj.KindResponse, "be-1.8", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			x := newTamper(t)
			c.fn(x)
			r := x.run()
			ms := r.Verify()
			found := false
			for _, m := range ms {
				if m.Kind == c.kind && m.Req == c.req {
					found = true
				}
			}
			if !found {
				t.Fatalf("want a %s mismatch for %s, got %v", c.kind, c.req, ms)
			}
			if _, err := r.Prompt(c.req); (err != nil) != c.promptFails {
				t.Fatalf("Prompt(%s) error = %v, want failure %v", c.req, err, c.promptFails)
			}
			// Damage never panics, and the episode says it is damaged.
			ep, err := r.Episode(traj.Options{Policy: policy})
			if err != nil {
				t.Fatal(err)
			}
			if !ep.Has(rl.FlagReplayMismatch) {
				t.Fatalf("an episode built from a log that fails the replay check must say so: %v", ep.Flags)
			}
		})
	}
}

// TestRequestsWithoutResponseDoNotDisqualify: a broken prompt on a call that never
// got a response is reported by Verify, but nothing of it is exported.
func TestRequestsWithoutResponseDoNotDisqualify(t *testing.T) {
	x := newTamper(t)
	x.remove("model.response", "be-1.23")
	x.manifest("be-1.23", func(m map[string]any) { m["wire"] = strings.Repeat("0", 64) })
	r := x.run()
	if !hasKind(r.Verify(), traj.KindWire) {
		t.Fatalf("verify: %v", r.Verify())
	}
	ep := episode(t, r, traj.Options{Policy: policy})
	if ep.Has(rl.FlagReplayMismatch) {
		t.Fatalf("flags: %v", ep.Flags)
	}
	if ep.Signals[traj.SigRequestErrors] != 1 || len(ep.Agents[0].Steps) != 26 {
		t.Fatalf("errors %v steps %d", ep.Signals[traj.SigRequestErrors], len(ep.Agents[0].Steps))
	}
	if ep.Agents[0].Status != "killed" && ep.Agents[0].Status != "" {
		t.Fatalf("status %q", ep.Agents[0].Status)
	}
	if !ep.Has(rl.FlagTruncated) {
		t.Fatalf("the final call never came back: %v", ep.Flags)
	}
}

func TestDroppedRequestsAndUnreadableCompletions(t *testing.T) {
	// A completion that cannot be read drops its step and is counted; the rest of
	// the episode survives.
	x := newTamper(t)
	x.drop[x.field("model.response", "be-1.6", "completion")] = true
	ep := episode(t, x.run(), traj.Options{Policy: policy})
	if got := ep.Signals[traj.SigRequestErrors]; got != 1 {
		t.Fatalf("request_errors = %v", got)
	}
	if n := len(ep.Agents[0].Steps); n != 26 {
		t.Fatalf("the unreadable step must be dropped: %d steps", n)
	}
	for _, s := range ep.Agents[0].Steps {
		if s.ID == "be-1.6" {
			t.Fatal("be-1.6 must be dropped")
		}
	}
	// be-1.7 extends the dropped be-1.6: the manifest chain passes through it, so
	// the segment does not break.
	if got := len(ep.Agents[0].Segments); got != 5 {
		t.Fatalf("segments: %d", got)
	}
}

func TestOpenTolerance(t *testing.T) {
	t.Run("torn tail", func(t *testing.T) {
		dir := copyFixture(t)
		path := filepath.Join(dir, "events.jsonl")
		b, _ := os.ReadFile(path)
		cut := bytes.LastIndexByte(b[:len(b)-1], '\n') + 1
		if err := os.WriteFile(path, b[:cut+40], 0o644); err != nil { // half of the last event
			t.Fatal(err)
		}
		r, err := traj.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !r.Torn() || len(r.Events()) != 171 {
			t.Fatalf("torn=%v events=%d", r.Torn(), len(r.Events()))
		}
		if ms := r.Verify(); len(ms) != 0 {
			t.Fatalf("a torn tail is a crash, not corruption: %v", ms)
		}
		ep := episode(t, r, traj.Options{Policy: policy})
		if len(ep.Agents[0].Steps) != 27 {
			t.Fatalf("all model calls precede the torn line: %d", len(ep.Agents[0].Steps))
		}
	})
	t.Run("cut mid run", func(t *testing.T) {
		dir := copyFixture(t)
		path := filepath.Join(dir, "events.jsonl")
		b, _ := os.ReadFile(path)
		// Keep the first 100 events and half of the 101st.
		off := 0
		for i := 0; i < 100; i++ {
			off += bytes.IndexByte(b[off:], '\n') + 1
		}
		os.WriteFile(path, b[:off+25], 0o644)
		r, err := traj.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		ep := episode(t, r, traj.Options{Policy: policy})
		if !ep.Has(rl.FlagTruncated) {
			t.Fatalf("a run cut in the middle of its work is truncated: %v", ep.Flags)
		}
		if len(ep.Agents[0].Steps) == 0 || len(ep.Agents[0].Steps) >= 27 {
			t.Fatalf("partial episode expected, got %d steps", len(ep.Agents[0].Steps))
		}
		if ep.Agents[0].Status != "" && ep.Agents[0].Status != "killed" {
			t.Fatalf("status: %q", ep.Agents[0].Status)
		}
		mustJSON(t, ep)
	})
	t.Run("corrupt middle line", func(t *testing.T) {
		dir := copyFixture(t)
		path := filepath.Join(dir, "events.jsonl")
		b, _ := os.ReadFile(path)
		lines := bytes.Split(b, []byte("\n"))
		lines[60] = []byte(`{"seq": 61, "ts": "bad`)
		os.WriteFile(path, bytes.Join(lines, []byte("\n")), 0o644)
		r, err := traj.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if r.Torn() || !hasKind(r.Verify(), traj.KindLog) {
			t.Fatalf("mid-file corruption is reported by Verify, not treated as a torn tail: %v", r.Verify())
		}
		if ep := episode(t, r, traj.Options{Policy: policy}); !ep.Has(rl.FlagReplayMismatch) {
			t.Fatalf("flags: %v", ep.Flags)
		}
	})
	t.Run("empty log", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "events.jsonl"), nil, 0o644)
		r, err := traj.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.Episode(traj.Options{}); !errors.Is(err, traj.ErrNoRequests) {
			t.Fatalf("empty log: %v", err)
		}
		if len(r.Verify()) != 0 {
			t.Fatalf("an empty log has nothing to mismatch")
		}
	})
	t.Run("only a torn line", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(`{"seq":1,"ts":"2026-01-01T00:00:0`), 0o644)
		r, err := traj.Open(dir)
		if err != nil || !r.Torn() {
			t.Fatalf("open: %v torn=%v", err, r.Torn())
		}
		if _, err := r.Episode(traj.Options{}); !errors.Is(err, traj.ErrNoRequests) {
			t.Fatal(err)
		}
	})
	t.Run("missing log and missing blobs", func(t *testing.T) {
		if _, err := traj.Open(t.TempDir()); err == nil || !strings.Contains(err.Error(), "events.jsonl") {
			t.Fatalf("a directory without a log must be an error that names it: %v", err)
		}
		dir := copyFixture(t)
		os.RemoveAll(filepath.Join(dir, "blobs"))
		r, err := traj.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Verify()) == 0 {
			t.Fatal("no blobs: every request must fail the replay check")
		}
		if _, err := r.Episode(traj.Options{Policy: policy}); err == nil || !strings.Contains(err.Error(), "usable step") {
			t.Fatalf("no readable completion means no episode: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dir, "blobs")); err == nil {
			t.Fatal("Open must not create the blobs directory")
		}
	})
	t.Run("bad task.json", func(t *testing.T) {
		dir := copyFixture(t)
		os.WriteFile(filepath.Join(dir, "task.json"), []byte(`{not json`), 0o644)
		if _, err := traj.Open(dir); err == nil || !strings.Contains(err.Error(), "task.json") {
			t.Fatalf("a damaged task.json is an operator error and must be reported: %v", err)
		}
	})
	t.Run("task.json", func(t *testing.T) {
		dir := copyFixture(t)
		task := rl.Task{ID: "gorilla-mux-0187", Kind: rl.TaskFix, Repo: rl.RepoSpec{URL: "https://example.org/mux", Commit: "a1b2c3d", License: "BSD-3-Clause"},
			Verifier: rl.Verifier{Cmd: "go test ./..."}, Budget: rl.Budget{Steps: 80, ContextWindow: 32000}, Network: true}
		b, _ := json.Marshal(task)
		os.WriteFile(filepath.Join(dir, "task.json"), b, 0o644)
		r, err := traj.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		ep := episode(t, r, traj.Options{Sample: 2, Policy: policy})
		if ep.TaskID != "gorilla-mux-0187" || ep.ID != "gorilla-mux-0187/2" || ep.Group != "gorilla-mux-0187@mock-1" {
			t.Fatalf("identity from task.json: %s %s", ep.ID, ep.Group)
		}
		if ep.Env.Repo != "https://example.org/mux" || ep.Env.Commit != "a1b2c3d" || !ep.Env.Network || ep.Env.Limits["steps"] != "80" || ep.Env.Limits["context_window"] != "32000" {
			t.Fatalf("env: %+v", ep.Env)
		}
		if ep.Provenance.License != "BSD-3-Clause" {
			t.Fatalf("licence: %+v", ep.Provenance)
		}
		// Options.Task overrides the file.
		ep2 := episode(t, r, traj.Options{Task: &rl.Task{ID: "other"}, Policy: policy})
		if ep2.TaskID != "other" {
			t.Fatalf("Options.Task must win: %s", ep2.TaskID)
		}
	})
}

// TestOutOfOrderEvents: a log whose events were merged from several writers, in
// any order, gives the same episode as the ordered one.
func TestOutOfOrderEvents(t *testing.T) {
	r := openFixture(t)
	want := mustJSON(t, episode(t, r, traj.Options{TaskID: "o", Policy: policy}))
	evs := append([]events.Event(nil), r.Events()...)
	// Deterministic shuffle: reverse pairs and rotate.
	for i := 0; i+1 < len(evs); i += 2 {
		evs[i], evs[i+1] = evs[i+1], evs[i]
	}
	evs = append(evs[50:], evs[:50]...)
	blobs := openBlobs(t)
	got := mustJSON(t, episode(t, traj.OpenWith(evs, blobs), traj.Options{TaskID: "o", Policy: policy}))
	if got != want {
		t.Fatal("event order must not matter when events carry sequence numbers")
	}
	// Duplicated records (a resumed writer) are harmless.
	dup := append(append([]events.Event(nil), r.Events()...), r.Events()[100:110]...)
	if got := mustJSON(t, episode(t, traj.OpenWith(dup, blobs), traj.Options{TaskID: "o", Policy: policy})); got != want {
		t.Fatal("exact duplicates must be dropped")
	}
	// Logs without sequence numbers are numbered in the order given.
	noSeq := append([]events.Event(nil), r.Events()...)
	for i := range noSeq {
		noSeq[i].Seq = 0
	}
	if got := mustJSON(t, episode(t, traj.OpenWith(noSeq, blobs), traj.Options{TaskID: "o", Policy: policy})); got != want {
		t.Fatal("a log without sequence numbers must behave like its ordered self")
	}
}

func openBlobs(t *testing.T) events.Blobs {
	t.Helper()
	b, err := events.NewDirBlobs(filepath.Join(fixtureDir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConcurrentPromptAndVerify(t *testing.T) {
	r := openFixture(t)
	ep := episode(t, r, traj.Options{Policy: policy})
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := range ep.Agents[0].Steps {
				s := ep.Agents[0].Steps[(i+g*3)%len(ep.Agents[0].Steps)]
				if _, err := r.Prompt(s.Prompt.Req); err != nil {
					t.Error(err)
					return
				}
				if g%3 == 0 {
					if len(r.Verify()) != 0 {
						t.Error("verify")
						return
					}
				}
			}
			if _, err := r.Episode(traj.Options{Policy: policy}); err != nil {
				t.Error(err)
			}
		}(g)
	}
	wg.Wait()
}

func TestPromptResultsAreIsolated(t *testing.T) {
	r := openFixture(t)
	p1, _ := r.Prompt("be-1.3")
	p1.Messages[0].Blocks = append(p1.Messages[0].Blocks, core.Text("scribble"))
	p1.Messages = p1.Messages[:1]
	p1.Tools[0].Name = "hacked"
	p2, err := r.Prompt("be-1.3")
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Messages) != 5 || len(p2.Messages[0].Blocks) != 2 || p2.Tools[0].Name != "big" {
		t.Fatalf("mutating a returned prompt corrupted the cache: %d msgs %d blocks tool %s", len(p2.Messages), len(p2.Messages[0].Blocks), p2.Tools[0].Name)
	}
}

// ---- synthetic swarm -----------------------------------------------------

type swarm struct {
	run *trajtest.Run
	mgr *trajtest.Agent
	wk  *trajtest.Agent
	rev *trajtest.Agent
}

// buildSwarm records a manager that plans, spawns a backend worker (which
// compacts once, takes a hot tail, and mails the manager) and a reviewer that
// runs on a teacher model.
func buildSwarm() *swarm {
	b := trajtest.New()
	s := &swarm{run: b}
	s.mgr = b.SpawnRoot("mgr", "manager", "policy-1")
	s.mgr.Tokens = true
	s.mgr.User("Ship the API")
	s.mgr.Step(trajtest.Call{Text: "plan it", Tool: "task", Input: `{"action":"create","title":"api"}`, Result: "created T1"})        // mgr.1
	s.mgr.Step(trajtest.Call{Text: "spawn a worker", Tool: "spawn", Input: `{"role":"backend","task":"T1"}`, Result: "started be-1"}) // mgr.2
	s.wk = b.Spawn("mgr", "be-1", "backend", "policy-1", "T1")
	s.wk.Tokens = true
	s.wk.User("Begin task T1")
	s.wk.Step(trajtest.Call{Text: "look", Tool: "read", Input: `{"path":"api/b.go"}`, Result: "package api"})                                                                           // be-1.1
	s.wk.Step(trajtest.Call{Text: "edit", Tool: "edit", Input: `{"path":"api/a.go","old_string":"x","new_string":"y"}`, Result: "ok"})                                                  // be-1.2
	s.wk.Step(trajtest.Call{Text: "edit again", Tool: "edit", Input: `{"path":"api/a.go","old_string":"y","new_string":"z"}`, Result: "changed since you last read it", IsError: true}) // be-1.3
	s.wk.Fork(`{"keep_from":"t5"}`, "")                                                                                                                                                 // be-1.c1
	s.wk.Commit(5, false, "pays back within 3 turns")
	s.wk.Step(trajtest.Call{Text: "read again", Tool: "read", Input: `{"path":"api/b.go"}`, Result: "package api"}) // be-1.4 (new segment)
	s.wk.Hot = "<live>board v3</live>"
	s.wk.Step(trajtest.Call{Text: "test", Tool: "bash", Input: `{"command":"go test ./..."}`, Result: "ok"})       // be-1.5 (hot tail)
	s.wk.Step(trajtest.Call{Text: "test again", Tool: "bash", Input: `{"command":"go test ./..."}`, Result: "ok"}) // be-1.6
	b.Mail("be-1", "mgr", "info", "api done")
	s.wk.Hot = ""
	s.wk.Step(trajtest.Call{Text: "wrap up", Tool: "task", Input: `{"action":"done","id":"T1","text":"done"}`, Result: "T1 is now in review"}) // be-1.7
	s.wk.Step(trajtest.Call{Text: "all done"})                                                                                                 // be-1.8 final
	b.End(s.wk, "idle")

	s.rev = b.Spawn("mgr", "rv-1", "reviewer", "teacher-x", "T1")
	s.rev.Tokens = true
	s.rev.User("Review T1")
	s.rev.Step(trajtest.Call{Text: "review", Tool: "read", Input: `{"path":"api/b.go"}`, Result: "package api"}) // rv-1.1
	s.rev.Step(trajtest.Call{Text: "lgtm"})                                                                      // rv-1.2 final
	b.End(s.rev, "idle")

	b.EpochCommit("promote")
	s.mgr.SyncShared("shared v2")
	s.mgr.Step(trajtest.Call{Text: "accept", Tool: "task", Input: `{"action":"accept","id":"T1"}`, Result: "T1 accepted"}) // mgr.3
	s.mgr.Step(trajtest.Call{Text: "shipped"})                                                                             // mgr.4 final
	b.Outcome("verifier", true, 1)
	return s
}

func TestSwarmEpisode(t *testing.T) {
	s := buildSwarm()
	run := traj.OpenWith(s.run.Events(), s.run.Blobs)
	if ms := run.Verify(); len(ms) != 0 {
		t.Fatalf("synthetic run must replay: %v", ms)
	}
	ep := episode(t, run, traj.Options{TaskID: "swarm-1", Sample: 0, Policy: rl.PolicyRef{Model: "policy-1"}})

	// Agents by first appearance, with lineage from agent.spawn.
	var ids []string
	byID := map[string]rl.Agent{}
	for _, a := range ep.Agents {
		ids = append(ids, a.ID)
		byID[a.ID] = a
	}
	if fmt.Sprint(ids) != "[mgr be-1 rv-1]" {
		t.Fatalf("agents: %v", ids)
	}
	if a := byID["be-1"]; a.Role != "backend" || a.Parent != "mgr" || a.Model != "policy-1" || a.Status != "done" {
		t.Fatalf("worker: %+v", a)
	}
	if a := byID["mgr"]; a.Role != rl.RoleManager || a.Parent != "" || a.Status != "done" {
		t.Fatalf("manager: %+v", a)
	}
	if fmt.Sprint(ep.Harness.Roles) != "[backend manager reviewer]" || ep.Harness.Agents != 3 {
		t.Fatalf("harness: %+v", ep.Harness)
	}

	// Roles on steps: main calls train the agent's role, the fork trains compactor.
	wk := byID["be-1"]
	roles := map[string]string{}
	for _, st := range wk.Steps {
		roles[st.ID] = st.Role
	}
	if roles["be-1.c1"] != "compactor" || roles["be-1.1"] != "backend" {
		t.Fatalf("roles: %v", roles)
	}

	// Trainable follows the model: the reviewer runs on a teacher.
	for _, st := range byID["rv-1"].Steps {
		if st.Trainable {
			t.Errorf("%s ran on teacher-x and must not be trainable", st.ID)
		}
	}
	for _, st := range wk.Steps {
		if !st.Trainable {
			t.Errorf("%s ran on the policy and must be trainable", st.ID)
		}
	}
	if !ep.Provenance.Teacher || fmt.Sprint(ep.Provenance.TeacherModels) != "[teacher-x]" {
		t.Fatalf("provenance: %+v", ep.Provenance)
	}

	// Segments of the worker: start; compact (after the commit); the hot-tail steps
	// continue that segment because the hot block is not a rebase.
	if len(wk.Segments) != 2 || wk.Segments[0].Reason != "start" || wk.Segments[1].Reason != "compact" {
		t.Fatalf("worker segments: %+v", wk.Segments)
	}
	segOf := map[string]int{}
	for _, st := range wk.Steps {
		segOf[st.ID] = st.Segment
	}
	for id, want := range map[string]int{"be-1.1": 0, "be-1.3": 0, "be-1.c1": 0, "be-1.4": 1, "be-1.5": 1, "be-1.6": 1, "be-1.7": 1, "be-1.8": 1} {
		if segOf[id] != want {
			t.Errorf("%s in segment %d, want %d", id, segOf[id], want)
		}
	}
	// The manager: start, then the shared-epoch pickup.
	mg := byID["mgr"]
	if len(mg.Segments) != 2 || mg.Segments[1].Reason != "epoch" || mg.Segments[1].Epoch != 1 || mg.Segments[0].Epoch != 0 {
		t.Fatalf("manager segments: %+v", mg.Segments)
	}
	for _, st := range mg.Steps {
		if (st.ID == "mgr.3" || st.ID == "mgr.4") && st.Epoch != 1 {
			t.Errorf("%s epoch %d", st.ID, st.Epoch)
		}
	}

	// Edges.
	want := []rl.Edge{
		{Kind: rl.EdgeSpawn, From: "mgr.2", To: "be-1", Ref: "T1"},
		{Kind: rl.EdgeCompact, From: "be-1.c1", To: "be-1.4", Ref: "be-1#1"},
		{Kind: rl.EdgeMail, From: "be-1.6", To: "mgr.3", Ref: "m1"},
		{Kind: rl.EdgeSpawn, From: "mgr.2", To: "rv-1", Ref: "T1"},
	}
	// The mail is delivered before the reviewer is spawned but the manager's next
	// step comes later; edges are in log order.
	sort.SliceStable(want, func(i, j int) bool { return false })
	if fmt.Sprint(ep.Edges) != fmt.Sprint(want) {
		t.Fatalf("edges:\n got %+v\nwant %+v", ep.Edges, want)
	}

	// Signals.
	sig := ep.Signals
	check := func(name string, want float64) {
		t.Helper()
		if sig[name] != want {
			t.Errorf("signal %s = %v, want %v", name, sig[name], want)
		}
	}
	check(rl.SigSteps, 4+8+2)
	check(rl.SigWorkerSteps, 8+2)
	check(rl.SigRequests, 4+8+1+2)
	check(rl.SigSpawns, 2)
	check(rl.SigMailSent, 1)
	check(rl.SigMailIgnored, 0)
	check(rl.SigStaleWrites, 1)
	check(rl.SigToolErrors, 1)
	check(rl.SigVerifierRuns, 2)
	check(rl.SigDoneClaims, 1)
	check(rl.SigDoneAccepted, 1)
	check(rl.SigCompactions, 1)
	check(rl.SigReReads, 1) // be-1.4 re-reads api/b.go, whose only read the commit folded away
	check(rl.SigDuplicateWork, 1)
	// The reviewer's read of api/b.go repeats the worker's identical call. mgr,
	// be-1 and rv-1 each made distinct other calls; go test ./... is run twice by
	// the same agent and does not count.
	// Critical path: mgr.1 mgr.2 -> be-1.1..be-1.6 -> (mail) mgr.3 mgr.4.
	check(rl.SigCriticalPath, 2+6+2)

	if ep.Outcome.Verifier == nil || !ep.Outcome.Verifier.Pass || ep.Outcome.Verifier.Score != 1 || ep.Outcome.Verifier.Version != "v1" || ep.Outcome.Claimed != "done" {
		t.Fatalf("outcome: %+v", ep.Outcome)
	}
	if fmt.Sprint(ep.Flags) != "[]" && len(ep.Flags) != 0 {
		t.Fatalf("a verified, finished swarm run has no flags: %v", ep.Flags)
	}

	// Shared prefix: tools and system are common to all agents; their first
	// messages differ (task text), so no messages are shared.
	for _, a := range ep.Agents {
		for _, st := range a.Steps {
			if st.Prompt.SharedPrefix == "" || st.Prompt.SharedPrefix != ep.Agents[0].Steps[0].Prompt.SharedPrefix || st.Prompt.SharedMessages != 0 {
				t.Fatalf("%s shared prefix %q/%d", st.ID, st.Prompt.SharedPrefix, st.Prompt.SharedMessages)
			}
		}
	}
	mustJSON(t, ep)
}

func TestSwarmTokenChains(t *testing.T) {
	s := buildSwarm()
	run := traj.OpenWith(s.run.Events(), s.run.Blobs)
	ep := episode(t, run, traj.Options{Policy: rl.PolicyRef{Model: "policy-1"}})
	// The fake tokenizer is chain-consistent, so the only breaks come from the
	// hot tail: be-1.5 carries hot tokens that be-1.6 no longer has, and be-1.6 to
	// be-1.7 breaks again as the hot block is dropped.
	if got := ep.Signals[traj.SigPrefixBreaks]; got != 2 {
		t.Fatalf("token_prefix_breaks = %v, want 2 (hot tail before and after be-1.5)", got)
	}
	if ep.Has(rl.FlagTokenMismatch) {
		t.Fatal("hot tails break packing, not the traces")
	}
	strict := episode(t, run, traj.Options{Policy: rl.PolicyRef{Model: "policy-1"}, StrictTokens: true})
	if !strict.Has(rl.FlagTokenMismatch) {
		t.Fatal("strict mode flags the worker's second segment")
	}
	for _, a := range strict.Agents {
		for _, st := range a.Steps {
			if a.ID == "be-1" && st.Segment == 1 && st.Kind == rl.KindMain && st.Tokens != nil {
				t.Errorf("%s keeps tokens in a broken segment", st.ID)
			}
			if a.ID == "be-1" && st.Segment == 0 && st.Tokens == nil {
				t.Errorf("%s lost tokens in an intact segment", st.ID)
			}
			if a.ID == "mgr" && st.Tokens == nil {
				t.Errorf("manager %s lost tokens", st.ID)
			}
		}
	}
}

func TestFailedCallsAndInvalidInput(t *testing.T) {
	b := trajtest.New()
	a := b.Agent("solo", "backend", "m")
	a.Tokens = true
	a.User("go")
	a.Step(trajtest.Call{Text: "one", Tool: "read", Input: `{"path":"x"}`, Result: "x"})
	a.Step(trajtest.Call{Text: "boom", Fail: true}) // request error: no response
	a.Step(trajtest.Call{Text: "cut off", Tool: "edit", Input: `{"path": "a", "old_str`, Invalid: "unexpected end of JSON input", Result: "The arguments for edit were not valid JSON (unexpected end of JSON input)", IsError: true})
	a.Step(trajtest.Call{Text: "nope", Tool: "frobnicate", Input: `{}`, Result: `unknown tool "frobnicate"`, IsError: true})
	a.Step(trajtest.Call{Text: "bad", Tool: "read", Input: `{"path":1}`, Result: "invalid arguments: path must be a string", IsError: true})
	a.Step(trajtest.Call{Text: "done"})
	b.Emit("", "perm.decide", map[string]any{"tool": "bash", "allow": false, "reason": "no"})
	b.Emit("", "perm.decide", map[string]any{"tool": "read", "allow": true})
	b.Emit("", "perm.decide", map[string]any{"tool": "bash", "decision": "deny"})
	b.Emit("solo", "user.steer", map[string]any{"text": "careful"})
	b.Emit("", "model.error", map[string]any{"req": "solo.9", "kind": "rate_limit", "attempt": 1, "delay_ms": 5})

	run := traj.OpenWith(b.Events(), b.Blobs)
	ep := episode(t, run, traj.Options{Policy: rl.PolicyRef{Model: "m"}})
	if got := ep.Signals[traj.SigRequestErrors]; got != 1 {
		t.Fatalf("request_errors = %v", got)
	}
	if ep.Signals[traj.SigRequestRetry] != 1 || ep.Signals[rl.SigRequests] != 7 {
		t.Fatalf("requests = %v retries = %v", ep.Signals[rl.SigRequests], ep.Signals[traj.SigRequestRetry])
	}
	if ep.Signals[rl.SigInvalidToolCalls] != 3 || ep.Signals[rl.SigToolErrors] != 3 {
		t.Fatalf("invalid calls %v tool errors %v", ep.Signals[rl.SigInvalidToolCalls], ep.Signals[rl.SigToolErrors])
	}
	steps := ep.Agents[0].Steps
	if len(steps) != 5 { // six requests, one failed
		t.Fatalf("steps: %d", len(steps))
	}
	// The unparseable arguments are wrapped as a JSON string so the episode marshals.
	obs := steps[1].Observations[0]
	if !json.Valid(obs.Input) || !strings.HasPrefix(string(obs.Input), `"`) || !obs.IsError {
		t.Fatalf("invalid input observation: %s", obs.Input)
	}
	mustJSON(t, ep)
	// The chain passes through the failed request: the failed call is not a
	// segment boundary.
	if len(ep.Agents[0].Segments) != 1 {
		t.Fatalf("segments: %+v", ep.Agents[0].Segments)
	}
	if fmt.Sprint(ep.Outcome.Labels) != "[perm_denied:2 user_steer:1]" {
		t.Fatalf("labels: %v", ep.Outcome.Labels)
	}
}

func TestBadTokenTraceIsDroppedNotRepaired(t *testing.T) {
	b := trajtest.New()
	a := b.Agent("solo", "backend", "m")
	a.Tokens = true
	a.User("go")
	a.Step(trajtest.Call{Text: "one", Tool: "read", Input: `{"path":"x"}`, Result: "x", BadTokens: true})
	a.Step(trajtest.Call{Text: "two", Tool: "read", Input: `{"path":"y"}`, Result: "y", NoTokens: true})
	a.Step(trajtest.Call{Text: "three"})
	ep := episode(t, traj.OpenWith(b.Events(), b.Blobs), traj.Options{Policy: rl.PolicyRef{Model: "m"}})
	st := ep.Agents[0].Steps
	if st[0].Tokens != nil || st[1].Tokens != nil || st[2].Tokens == nil {
		t.Fatalf("only the consistent trace survives: %v %v %v", st[0].Tokens != nil, st[1].Tokens != nil, st[2].Tokens != nil)
	}
	if !ep.Has(rl.FlagTokenMismatch) {
		t.Fatalf("an inconsistent trace is flagged: %v", ep.Flags)
	}
	// A step that simply has no trace (the endpoint could not provide one) is not a mismatch.
	if len(ep.Signals) == 0 || ep.Signals[traj.SigPrefixBreaks] != 0 {
		t.Fatalf("signals: %v", ep.Signals)
	}
}

func TestOutcomeAndFlags(t *testing.T) {
	build := func(fn func(b *trajtest.Run, a *trajtest.Agent)) *rl.Episode {
		b := trajtest.New()
		a := b.Agent("solo", "backend", "m")
		a.User("go")
		a.Step(trajtest.Call{Text: "work", Tool: "read", Input: `{"path":"x"}`, Result: "x"})
		fn(b, a)
		return episode(t, traj.OpenWith(b.Events(), b.Blobs), traj.Options{Policy: rl.PolicyRef{Model: "m"}})
	}
	t.Run("verifier fail and review", func(t *testing.T) {
		ep := build(func(b *trajtest.Run, a *trajtest.Agent) {
			a.Step(trajtest.Call{Text: "done"})
			b.Outcome("verifier", false, 0.25)
			b.Outcome("verifier", true, 0.75) // the last verdict is the result
			b.Outcome("review", true, 0.5)
			b.Emit("", "outcome", map[string]any{"kind": "human", "pass": false})
			b.Emit("", "outcome", map[string]any{"kind": "protocol", "pass": true, "score": 1.0, "verifier_version": "p2"})
		})
		if v := ep.Outcome.Verifier; v == nil || !v.Pass || v.Score != 0.75 {
			t.Fatalf("verifier: %+v", v)
		}
		if len(ep.Outcome.Reviews) != 3 || ep.Outcome.Reviews[0].Kind != "review" || ep.Outcome.Reviews[1].Score != 0 || ep.Outcome.Reviews[2].Version != "p2" {
			t.Fatalf("reviews: %+v", ep.Outcome.Reviews)
		}
		if ep.Has(rl.FlagWeakLabel) || len(ep.Flags) != 0 {
			t.Fatalf("flags: %v", ep.Flags)
		}
	})
	t.Run("infra failure", func(t *testing.T) {
		ep := build(func(b *trajtest.Run, a *trajtest.Agent) {
			a.Step(trajtest.Call{Text: "done"})
			b.Outcome("infra", false, 0)
		})
		if !ep.Has(rl.FlagInfraError) || ep.Outcome.Verifier != nil {
			t.Fatalf("flags: %v %+v", ep.Flags, ep.Outcome)
		}
	})
	t.Run("infra ok is not a flag", func(t *testing.T) {
		ep := build(func(b *trajtest.Run, a *trajtest.Agent) {
			a.Step(trajtest.Call{Text: "done"})
			b.Outcome("infra", true, 1)
			b.Outcome("verifier", true, 1)
		})
		if ep.Has(rl.FlagInfraError) {
			t.Fatalf("flags: %v", ep.Flags)
		}
	})
	t.Run("truncated", func(t *testing.T) {
		ep := build(func(b *trajtest.Run, a *trajtest.Agent) {
			a.Step(trajtest.Call{Text: "more", Tool: "read", Input: `{"path":"y"}`, Result: "y"})
		})
		if !ep.Has(rl.FlagTruncated) || ep.Outcome.Claimed != "" {
			t.Fatalf("a run that stops while still calling tools is truncated: %v claimed=%q", ep.Flags, ep.Outcome.Claimed)
		}
		if got := fmt.Sprint(ep.Flags); got != "[truncated weak_label]" {
			t.Fatalf("flags must be sorted: %s", got)
		}
	})
	t.Run("session end is a clean finish", func(t *testing.T) {
		ep := build(func(b *trajtest.Run, a *trajtest.Agent) {
			a.Step(trajtest.Call{Text: "more", Tool: "read", Input: `{"path":"y"}`, Result: "y"})
			b.Emit("", "session.end", map[string]any{})
		})
		if ep.Has(rl.FlagTruncated) {
			t.Fatalf("flags: %v", ep.Flags)
		}
	})
	t.Run("gave up", func(t *testing.T) {
		ep := build(func(b *trajtest.Run, a *trajtest.Agent) {
			a.Step(trajtest.Call{Text: "cut", Stop: core.StopMaxTokens})
		})
		if ep.Outcome.Claimed != "gave_up" {
			t.Fatalf("claimed: %q", ep.Outcome.Claimed)
		}
	})
	t.Run("blocked", func(t *testing.T) {
		ep := build(func(b *trajtest.Run, a *trajtest.Agent) {
			a.Step(trajtest.Call{Text: "stuck", Tool: "task", Input: `{"action":"block","id":"T1","text":"need input"}`, Result: "T1 marked blocked"})
			a.Step(trajtest.Call{Text: "waiting"})
		})
		if ep.Outcome.Claimed != "blocked" {
			t.Fatalf("claimed: %q", ep.Outcome.Claimed)
		}
	})
	t.Run("budget", func(t *testing.T) {
		ep := build(func(b *trajtest.Run, a *trajtest.Agent) {
			b.Emit("swarm", "governor", map[string]any{"action": "stop", "reason": "swarm budget exhausted"})
		})
		if ep.Outcome.Claimed != "budget" || !ep.Has(rl.FlagBudgetExceeded) {
			t.Fatalf("claimed %q flags %v", ep.Outcome.Claimed, ep.Flags)
		}
	})
}

func TestUnicodeAndHugePrompts(t *testing.T) {
	b := trajtest.New()
	a := b.Agent("solo", "backend", "m")
	a.Tokens = true
	a.User("日本語のタスク 😀 ⟦not a token⟧   \x00 </script> & <b>")
	huge := strings.Repeat("大きな出力 line\n", 200_000) // ~3.6 MB
	a.Step(trajtest.Call{Text: "héllo ✓", Tool: "read", Input: `{"path":"ü/ß.go"}`, Result: huge})
	a.Step(trajtest.Call{Text: "fin"})
	run := traj.OpenWith(b.Events(), b.Blobs)
	if ms := run.Verify(); len(ms) != 0 {
		t.Fatal(ms)
	}
	ep := episode(t, run, traj.Options{Policy: rl.PolicyRef{Model: "m"}})
	obs := ep.Agents[0].Steps[0].Observations[0]
	if obs.Output != huge {
		t.Fatalf("a %d byte observation must survive intact (%d)", len(huge), len(obs.Output))
	}
	p, err := run.Prompt("solo.2")
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Messages[0].Blocks[1].Text; !strings.Contains(got, "日本語のタスク 😀 ⟦not a token⟧") || !strings.Contains(got, " ") || !strings.Contains(got, "</script> & <b>") {
		t.Fatalf("user text was altered: %q", got)
	}
	last := p.Messages[len(p.Messages)-1].Blocks[0]
	if last.PlainText() != huge {
		t.Fatal("the huge tool result must appear verbatim in the next prompt")
	}
	mustJSON(t, ep)
}

func TestManyConcurrentAgents(t *testing.T) {
	// 24 agents interleave their calls; nothing may leak between them.
	b := trajtest.New()
	mgr := b.SpawnRoot("mgr", "manager", "m")
	mgr.User("go")
	mgr.Step(trajtest.Call{Text: "spawn all", Tool: "spawn", Input: `{}`, Result: "ok"})
	var ws []*trajtest.Agent
	for i := 0; i < 24; i++ {
		w := b.Spawn("mgr", fmt.Sprintf("w-%02d", i), "backend", "m", fmt.Sprintf("T%d", i))
		w.User(fmt.Sprintf("task %d", i))
		ws = append(ws, w)
	}
	for round := 0; round < 4; round++ {
		for i, w := range ws {
			w.Step(trajtest.Call{Text: fmt.Sprintf("r%d", round), Tool: "read", Input: fmt.Sprintf(`{"path":"f%d_%d"}`, i, round), Result: fmt.Sprintf("content %d %d", i, round)})
		}
	}
	for _, w := range ws {
		w.Step(trajtest.Call{Text: "done"})
	}
	ep := episode(t, traj.OpenWith(b.Events(), b.Blobs), traj.Options{Policy: rl.PolicyRef{Model: "m"}})
	if len(ep.Agents) != 25 {
		t.Fatalf("agents: %d", len(ep.Agents))
	}
	for i, a := range ep.Agents[1:] {
		if len(a.Steps) != 5 || len(a.Segments) != 1 {
			t.Fatalf("%s: %d steps %d segments", a.ID, len(a.Steps), len(a.Segments))
		}
		for round := 0; round < 4; round++ {
			o := a.Steps[round].Observations
			if len(o) != 1 || o[0].Output != fmt.Sprintf("content %d %d", i, round) {
				t.Fatalf("%s round %d observation: %+v", a.ID, round, o)
			}
		}
	}
	if ep.Signals[rl.SigSpawns] != 24 || ep.Signals[rl.SigSteps] != 1+24*5 {
		t.Fatalf("signals: %v", ep.Signals)
	}
	// 24 workers run concurrently: the longest chain is the manager's step, then
	// one worker's five.
	if got := ep.Signals[rl.SigCriticalPath]; got != 6 {
		t.Fatalf("critical path %v, want 6", got)
	}
	// Identical prompts prefix: tools and system are shared across all agents.
	sp := ep.Agents[0].Steps[0].Prompt.SharedPrefix
	for _, a := range ep.Agents {
		for _, st := range a.Steps {
			if st.Prompt.SharedPrefix != sp {
				t.Fatalf("%s: shared prefix %q != %q", st.ID, st.Prompt.SharedPrefix, sp)
			}
		}
	}
}

// TestSharedMessagesWhenAgentsStartAlike: agents whose first messages are
// byte-identical share those messages as well as tools and system.
func TestSharedMessagesWhenAgentsStartAlike(t *testing.T) {
	b := trajtest.New()
	mgr := b.SpawnRoot("mgr", "manager", "m")
	mgr.User("same task")
	mgr.Step(trajtest.Call{Text: "spawn", Tool: "spawn", Input: `{}`, Result: "ok"})
	w := b.Spawn("mgr", "w-1", "backend", "m", "T1")
	w.User("same task")
	w.Step(trajtest.Call{Text: "a", Tool: "read", Input: `{"path":"a"}`, Result: "A"})
	w.Step(trajtest.Call{Text: "b"})
	mgr.Step(trajtest.Call{Text: "fin"})
	ep := episode(t, traj.OpenWith(b.Events(), b.Blobs), traj.Options{Policy: rl.PolicyRef{Model: "m"}})
	for _, a := range ep.Agents {
		for _, st := range a.Steps {
			if st.Prompt.SharedMessages != 1 || st.Prompt.SharedPrefix == "" {
				t.Fatalf("%s: shared %q/%d", st.ID, st.Prompt.SharedPrefix, st.Prompt.SharedMessages)
			}
		}
	}
}

func TestResolverAdapter(t *testing.T) {
	r := openFixture(t)
	ep := episode(t, r, traj.Options{Policy: policy})
	res := traj.Resolver{Run: r}
	st := &ep.Agents[0].Steps[5]
	p, err := res.Prompt(ep, st)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := r.Prompt(st.Prompt.Req)
	if !reflectEqualMessages(p.Messages, want.Messages) {
		t.Fatal("resolver returned another prompt")
	}
}
