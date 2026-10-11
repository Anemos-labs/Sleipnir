package export_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/export"
	"github.com/anemos-labs/sleipnir/internal/rl/traj"
	"github.com/anemos-labs/sleipnir/internal/rl/traj/trajtest"
)

const dsModel = "deepseek/deepseek-v4.1-flash"

// toolStep is a step whose completion makes one tool call.
func toolStep(model, role, tool, input string) rl.Step {
	return rl.Step{ID: "a.1", Kind: rl.KindMain, Role: role, Model: model, Trainable: true,
		Completion: rl.Completion{Turn: core.Turn{Role: core.RoleAssistant, Blocks: []core.Block{core.ToolUse("c1", tool, json.RawMessage(input))}}}}
}

func TestDropRuleMatch(t *testing.T) {
	ep := &rl.Episode{TaskID: "gh-shop/12", Env: rl.EnvRef{Tags: []string{"Go", "frontend_design"}}}
	fe := &rl.Agent{ID: "fe-1", Role: "frontend"}
	st := toolStep(dsModel, "frontend", "read", `{"path":"a.go"}`)
	st.Provider = "ImpCC"
	comp := toolStep(dsModel, "compactor", "read", `{}`)
	css := toolStep("qwen/qwen3.8", "worker", "write", `{"path":"web/site.css","content":"x"}`)

	for _, tc := range []struct {
		name string
		rule export.DropRule
		ag   *rl.Agent
		st   rl.Step
		want bool
	}{
		{"model glob crosses slashes", export.DropRule{Name: "r", Models: []string{"*deepseek*"}}, fe, st, true},
		{"model glob ignores case", export.DropRule{Name: "r", Models: []string{"DeepSeek/*"}}, fe, st, true},
		{"model glob anchored", export.DropRule{Name: "r", Models: []string{"deepseek"}}, fe, st, false},
		{"model question mark", export.DropRule{Name: "r", Models: []string{"deepseek/deepseek-v4.?-flash"}}, fe, st, true},
		{"other model", export.DropRule{Name: "r", Models: []string{"*qwen*"}}, fe, st, false},
		{"step role", export.DropRule{Name: "r", Roles: []string{"frontend"}}, nil, st, true},
		{"agent role covers its compactor", export.DropRule{Name: "r", Roles: []string{"frontend"}}, fe, comp, true},
		{"no agent, compactor role only", export.DropRule{Name: "r", Roles: []string{"frontend"}}, nil, comp, false},
		{"tags any of", export.DropRule{Name: "r", Tags: []string{"ui", "FRONTEND_*"}}, fe, st, true},
		{"tags none", export.DropRule{Name: "r", Tags: []string{"ui", "design"}}, fe, st, false},
		{"task id glob", export.DropRule{Name: "r", TaskIDs: []string{"gh-shop/*"}}, fe, st, true},
		{"task id exact miss", export.DropRule{Name: "r", TaskIDs: []string{"gh-shop"}}, fe, st, false},
		{"provider ignores case", export.DropRule{Name: "r", Providers: []string{"impcc"}}, fe, st, true},
		{"provider missing", export.DropRule{Name: "r", Providers: []string{"impcc"}}, fe, comp, false},
		{"provider star matches unrecorded", export.DropRule{Name: "r", Providers: []string{"*"}}, fe, comp, true},
		{"edits ui", export.DropRule{Name: "r", EditsUI: true}, nil, css, true},
		{"reads are not edits", export.DropRule{Name: "r", EditsUI: true}, fe, toolStep(dsModel, "frontend", "read", `{"path":"index.html"}`), false},
		{"all conditions must hold", export.DropRule{Name: "r", Models: []string{"*deepseek*"}, EditsUI: true}, nil, css, false},
		{"and of several", export.DropRule{Name: "r", Models: []string{"*deepseek*"}, Roles: []string{"frontend"}, Providers: []string{"ImpCC"}}, fe, st, true},
	} {
		if got := tc.rule.Match(ep, tc.ag, &tc.st); got != tc.want {
			t.Errorf("%s: Match = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEditedPaths(t *testing.T) {
	patch := "*** Begin Patch\n*** Add File: web/new.html\n+<p>\n*** Update File: api/a.go\n*** Move to: api/b.go\n@@\n-x\n+y\n*** Delete File: old.css\n*** End Patch"
	for _, tc := range []struct {
		name string
		st   rl.Step
		want []string
	}{
		{"write", toolStep("m", "w", "write", `{"path":"a/index.html","content":"x"}`), []string{"a/index.html"}},
		{"edit file_path alias", toolStep("m", "w", "Edit", `{"file_path":"src/App.tsx","old_string":"a","new_string":"b"}`), []string{"src/App.tsx"}},
		{"apply_patch headers", toolStep("m", "w", "apply_patch", mustMarshal(map[string]string{"patch": patch})), []string{"api/a.go", "api/b.go", "old.css", "web/new.html"}},
		{"apply_patch input key", toolStep("m", "w", "apply_patch", mustMarshal(map[string]string{"input": "*** Update File: x.vue\n"})), []string{"x.vue"}},
		{"cut-off arguments", toolStep("m", "w", "write", mustMarshal(`{"path":"site/style.css","content":"body{`)), []string{"site/style.css"}},
		{"not a file tool", toolStep("m", "w", "bash", `{"command":"echo > index.html"}`), []string{}},
		{"read is not a write", toolStep("m", "w", "read", `{"path":"index.html"}`), []string{}},
	} {
		if got := export.EditedPaths(&tc.st); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: EditedPaths = %q, want %q", tc.name, got, tc.want)
		}
	}
	// Observations count too: a recorded call whose completion is not at hand.
	st := rl.Step{Observations: []rl.Observation{{Name: "write", Input: json.RawMessage(`{"path":"ui/x.go","content":""}`)}}}
	if got := export.EditedPaths(&st); !reflect.DeepEqual(got, []string{"ui/x.go"}) {
		t.Errorf("from observations: %q", got)
	}
}

func mustMarshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestPresetDSFrontend(t *testing.T) {
	rules, ok := export.Preset("ds-frontend")
	if !ok || len(rules) != 3 {
		t.Fatalf("Preset(ds-frontend) = %v, %v; want three rules", rules, ok)
	}
	for _, r := range rules {
		if r.Name != export.PresetDSFrontend || !reflect.DeepEqual(r.Models, []string{"*deepseek*"}) {
			t.Errorf("preset rule %+v: every rule is named ds-frontend and needs a deepseek model", r)
		}
	}
	match := func(ep *rl.Episode, ag *rl.Agent, st rl.Step) bool {
		for _, r := range rules {
			if r.Match(ep, ag, &st) {
				return true
			}
		}
		return false
	}
	plain := &rl.Episode{TaskID: "t", Env: rl.EnvRef{Tags: []string{"go"}}}
	tagged := &rl.Episode{TaskID: "t", Env: rl.EnvRef{Tags: []string{"python", "frontend_correct"}}}
	fe := &rl.Agent{Role: "frontend"}
	be := &rl.Agent{Role: "backend"}
	for _, tc := range []struct {
		name string
		ep   *rl.Episode
		ag   *rl.Agent
		st   rl.Step
		want bool
	}{
		{"deepseek frontend agent", plain, fe, toolStep(dsModel, "frontend", "read", `{}`), true},
		{"deepseek compactor of a frontend agent", plain, fe, toolStep(dsModel, "compactor", "read", `{}`), true},
		{"deepseek on a tagged task", tagged, be, toolStep(dsModel, "worker", "read", `{}`), true},
		{"deepseek editing css", plain, be, toolStep(dsModel, "backend", "edit", `{"path":"static/app.css","old_string":"a","new_string":"b"}`), true},
		{"deepseek backend work", plain, be, toolStep(dsModel, "backend", "edit", `{"path":"api/a.go","old_string":"a","new_string":"b"}`), false},
		{"another model doing frontend work", tagged, fe, toolStep("qwen/qwen3.8-flash-next", "frontend", "write", `{"path":"index.html","content":""}`), false},
	} {
		if got := match(tc.ep, tc.ag, tc.st); got != tc.want {
			t.Errorf("%s: matched %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestParseDropRule(t *testing.T) {
	rules, err := export.ParseDropRule(" DS-Frontend ")
	if err != nil || len(rules) != 3 {
		t.Fatalf("preset: %v %v", rules, err)
	}
	rules, err = export.ParseDropRule("name=no-ds-docs; models=*deepseek*, *glm*; roles=docs; tags=ui; task-ids=t1,t2; providers=ImpCC; edits-ui")
	want := export.DropRule{Name: "no-ds-docs", Models: []string{"*deepseek*", "*glm*"}, Roles: []string{"docs"}, Tags: []string{"ui"},
		TaskIDs: []string{"t1", "t2"}, Providers: []string{"ImpCC"}, EditsUI: true}
	if err != nil || len(rules) != 1 || !reflect.DeepEqual(rules[0], want) {
		t.Fatalf("ParseDropRule = %+v, %v; want %+v", rules, err, want)
	}
	if r, err := export.ParseDropRule("name=x;edits-ui=false;roles=frontend"); err != nil || r[0].EditsUI {
		t.Errorf("edits-ui=false: %+v %v", r, err)
	}
	for _, bad := range []string{
		"",                      // nothing
		"frontend",              // neither a preset nor conditions
		"models=*deepseek*",     // no name
		"name=everything",       // no condition: would drop every step
		"name=x;colour=red",     // unknown key
		"name=x;roles=",         // empty list
		"name=x;edits-ui=maybe", // not a boolean
		"name=x;edits-ui=false", // the only condition switched off
	} {
		if _, err := export.ParseDropRule(bad); err == nil {
			t.Errorf("ParseDropRule(%q) accepted", bad)
		}
	}
	// Export refuses a rule that could not be told apart or that drops everything.
	for _, r := range []export.DropRule{{Models: []string{"x"}}, {Name: "all"}} {
		o := export.DefaultOptions(export.FormatSteps)
		o.Drop = []export.DropRule{r}
		if _, err := export.Export(&strings.Builder{}, nil, o); !errors.Is(err, export.ErrBadOptions) {
			t.Errorf("rule %+v: err = %v, want ErrBadOptions", r, err)
		}
	}
	o := export.DefaultOptions(export.FormatSteps)
	o.RoleTeachers = map[string][]string{"frontend": nil}
	if _, err := export.Export(&strings.Builder{}, nil, o); !errors.Is(err, export.ErrBadOptions) {
		t.Errorf("role teacher without models: %v", err)
	}
}

// dsTeam records a team run in which deepseek is the policy (every step trainable):
// a manager, a frontend worker with a compactor call, and a backend worker that
// writes one style sheet and edits Go code.
func dsTeam(t testing.TB, sample int, tags []string) export.Source {
	t.Helper()
	b := trajtest.New()
	mgr := b.SpawnRoot("mgr", "manager", dsModel)
	mgr.Tokens = true
	mgr.User("Ship the shop page")
	mgr.Step(trajtest.Call{Text: "spawn the frontend", Tool: "spawn", Input: `{"role":"frontend","task":"T1"}`, Result: "started"})
	fe := b.Spawn("mgr", "fe-1", "frontend", dsModel, "T1")
	fe.Tokens = true
	fe.User("Begin task T1")
	fe.Step(trajtest.Call{Text: "layout", Tool: "edit", Input: `{"path":"shop/view.go","old_string":"a","new_string":"b"}`, Result: "ok"})
	fe.Fork(`{"keep_from":"t2"}`, "")
	fe.Step(trajtest.Call{Text: "page done"})
	b.End(fe, "idle")
	be := b.Spawn("mgr", "be-1", "backend", dsModel, "T2")
	be.Tokens = true
	be.User("Begin task T2")
	be.Step(trajtest.Call{Text: "theme", Tool: "write", Input: `{"path":"web/site.css","content":"body{}"}`, Result: "ok"})
	be.Step(trajtest.Call{Text: "api", Tool: "edit", Input: `{"path":"api/a.go","old_string":"x","new_string":"y"}`, Result: "ok"})
	be.Step(trajtest.Call{Text: "api done"})
	b.End(be, "idle")
	mgr.Step(trajtest.Call{Text: "shipped"})
	b.Outcome("verifier", true, 1)
	run := traj.OpenWith(b.Events(), b.Blobs)
	task := &rl.Task{ID: "shop", Tags: tags}
	ep, err := run.Episode(traj.Options{TaskID: "shop", Sample: sample, Policy: rl.PolicyRef{Model: dsModel}, Task: task})
	if err != nil {
		t.Fatal(err)
	}
	ep.Reward = rl.Reward{Total: 1, Components: map[string]float64{"outcome": 1}}
	return export.Source{Episode: ep, Prompts: traj.Resolver{Run: run}}
}

// trainedSteps lists, sorted, the request ids an export trains on: the step of a
// steps or kto record, the steps of a tokens or sft record, the steps of every
// trajectory of a groups record.
func trainedSteps(t *testing.T, f export.Format, out string) []string {
	t.Helper()
	var ids []string
	for _, r := range decode(t, out) {
		switch f {
		case export.FormatSteps, export.FormatKTO:
			id := r["id"].(string)
			ids = append(ids, id[strings.Index(id, "#")+1:])
		case export.FormatTokens, export.FormatSFT:
			for _, s := range r["steps"].([]any) {
				ids = append(ids, s.(string))
			}
		case export.FormatGroups:
			for _, tr := range r["trajectories"].([]any) {
				for _, s := range tr.(map[string]any)["steps"].([]any) {
					ids = append(ids, s.(string))
				}
			}
		}
	}
	sort.Strings(ids)
	return ids
}

func TestDropRulesApplyToEveryTrainingFormat(t *testing.T) {
	src := dsTeam(t, 0, nil)
	all := map[string]bool{}
	for _, a := range src.Episode.Agents {
		for _, s := range a.Steps {
			if !s.Trainable {
				t.Fatalf("step %s of the deepseek policy is not trainable: the test needs trainable steps", s.ID)
			}
			all[s.ID] = true
		}
	}
	// fe-1's main steps and its compactor call (by role) and be-1's style sheet
	// write (EditsUI) are front-end work; the manager and the Go edit are not.
	dropped := []string{"be-1.1", "fe-1.1", "fe-1.2", "fe-1.c1"}
	for _, id := range dropped {
		if !all[id] {
			t.Fatalf("fixture lacks step %s (have %v)", id, all)
		}
	}
	rules, _ := export.Preset("ds-frontend")
	for _, f := range []export.Format{export.FormatSteps, export.FormatTokens, export.FormatGroups, export.FormatSFT, export.FormatKTO} {
		for _, pack := range []bool{false, true} {
			o := export.DefaultOptions(f)
			o.KeepFlat, o.PackSegments, o.Drop = true, pack, rules
			out, st := run(t, []export.Source{src}, o)
			got := trainedSteps(t, f, out)
			if len(got) == 0 {
				t.Fatalf("%s pack=%v: nothing exported\n%+v", f, pack, st)
			}
			for _, id := range dropped {
				for _, g := range got {
					if g == id {
						t.Errorf("%s pack=%v: trained on %s, which ds-frontend drops (trained: %v)", f, pack, id, got)
					}
				}
			}
			if st.Drops["step:rule:ds-frontend"] < len(dropped) {
				t.Errorf("%s pack=%v: drops %v, want step:rule:ds-frontend >= %d", f, pack, st.Drops, len(dropped))
			}
			want := map[string]map[string]int{"frontend": {"rule:ds-frontend": 2}, "compactor": {"rule:ds-frontend": 1}, "backend": {"rule:ds-frontend": 1}}
			for role, m := range want {
				if st.DroppedByRole[role]["rule:ds-frontend"] != m["rule:ds-frontend"] {
					t.Errorf("%s pack=%v: dropped_by_role[%s] = %v, want %v", f, pack, role, st.DroppedByRole[role], m)
				}
			}
		}
	}
	// A tag on the task makes every deepseek step front-end work.
	o := export.DefaultOptions(export.FormatSteps)
	o.KeepFlat, o.Drop = true, rules
	if out, st := run(t, []export.Source{dsTeam(t, 0, []string{"ui"})}, o); len(lines(out)) != 0 {
		t.Errorf("a task tagged ui keeps %d records; drops %v", len(lines(out)), st.Drops)
	}
	// The archives keep everything.
	o = export.DefaultOptions(export.FormatCanonical)
	o.Inline, o.Drop = true, rules
	out, _ := run(t, []export.Source{src}, o)
	for _, id := range dropped {
		if !strings.Contains(out, `"id":"`+id+`"`) {
			t.Errorf("canonical lost step %s: archives ignore drop rules", id)
		}
	}
}

// A role teacher constrains trainable steps too: deepseek is the policy here, and
// the frontend's steps, its compactor call included, must come from another model.
func TestRoleTeacherDropsTrainableSteps(t *testing.T) {
	src := dsTeam(t, 0, nil)
	o := export.DefaultOptions(export.FormatSteps)
	o.KeepFlat = true
	o.RoleTeachers = map[string][]string{"frontend": {"claude-*"}}
	out, st := run(t, []export.Source{src}, o)
	got := trainedSteps(t, export.FormatSteps, out)
	for _, g := range got {
		if strings.HasPrefix(g, "fe-1.") {
			t.Errorf("trained on frontend step %s from %s; only claude-* may teach the frontend", g, dsModel)
		}
	}
	if st.Drops["step:role_teacher"] != 3 || st.DroppedByRole["frontend"]["role_teacher"] != 2 || st.DroppedByRole["compactor"]["role_teacher"] != 1 {
		t.Errorf("drops %v by role %v; want 3 role_teacher drops (2 frontend, 1 compactor)", st.Drops, st.DroppedByRole)
	}
	if !reflect.DeepEqual(got, []string{"be-1.1", "be-1.2", "be-1.3", "mgr.1", "mgr.2"}) {
		t.Errorf("trained on %v; the other roles are not constrained", got)
	}
	// The policy itself, when allowed, passes.
	o.RoleTeachers = map[string][]string{"FRONTEND": {"*DeepSeek*"}}
	if _, st := run(t, []export.Source{src}, o); st.Drops["step:role_teacher"] != 0 {
		t.Errorf("an allowed model was dropped: %v", st.Drops)
	}
}

// A preference pair never has a dropped step on either side.
func TestDPOPairDroppedWhenEitherSideMatches(t *testing.T) {
	pair := func(t *testing.T, marked int, rules []export.DropRule) (string, export.Stats) {
		t.Helper()
		srcs := []export.Source{
			rollout(t, variant{sample: 0, plan: "plan A", reward: 1, pass: true}),
			rollout(t, variant{sample: 1, plan: "plan B", reward: 0, pass: false}),
		}
		ep := srcs[marked].Episode
		for ai := range ep.Agents {
			for si := range ep.Agents[ai].Steps {
				ep.Agents[ai].Steps[si].Provider = "ImpCC"
			}
		}
		o := export.DefaultOptions(export.FormatDPO)
		o.Drop = rules
		return run(t, srcs, o)
	}
	if out, _ := pair(t, 1, nil); len(lines(out)) == 0 {
		t.Fatal("without a rule the two rollouts must give a pair; the test proves nothing otherwise")
	}
	rule := []export.DropRule{{Name: "impcc", Providers: []string{"impcc"}}}
	for _, marked := range []int{0, 1} { // the chosen side, then the rejected side
		out, st := pair(t, marked, rule)
		if n := len(lines(out)); n != 0 {
			t.Errorf("side %d served by ImpCC: %d pairs written, want none\n%s", marked, n, out)
		}
		if st.Drops["step:rule:impcc"] == 0 {
			t.Errorf("side %d: drops %v, want step:rule:impcc", marked, st.Drops)
		}
	}
}

// lengthRun is one worker whose three calls grow: 100+20, 200+60 (40 of them
// reasoning) and 300+20 tokens.
func lengthRun(t testing.TB) export.Source {
	t.Helper()
	b := trajtest.New()
	a := b.Agent("solo", "worker", "policy-1")
	a.User("fix it")
	a.Step(trajtest.Call{Text: "look", Tool: "read", Input: `{"path":"a.go"}`, Result: "package a", Usage: &core.Usage{InputTokens: 100, OutputTokens: 20}})
	a.Step(trajtest.Call{Text: "think", Tool: "read", Input: `{"path":"b.go"}`, Result: "package b", Usage: &core.Usage{InputTokens: 200, OutputTokens: 60, ReasoningTokens: 40}})
	a.Step(trajtest.Call{Text: "done", Usage: &core.Usage{InputTokens: 300, OutputTokens: 20}})
	b.Outcome("verifier", true, 1)
	r := traj.OpenWith(b.Events(), b.Blobs)
	ep, err := r.Episode(traj.Options{TaskID: "len", Policy: rl.PolicyRef{Model: "policy-1"}})
	if err != nil {
		t.Fatal(err)
	}
	ep.Reward = rl.Reward{Total: 1}
	return export.Source{Episode: ep, Prompts: traj.Resolver{Run: r}}
}

func TestMaxSampleTokensDropsAreCountedByRole(t *testing.T) {
	src := lengthRun(t)
	for _, tc := range []struct {
		reasoning string
		kept      []string
	}{
		{"drop", []string{"solo.1", "solo.2"}}, // 200+60-40 = 220 fits, 320 does not
		{"field", []string{"solo.1"}},          // 260 and 320 do not fit
	} {
		o := export.DefaultOptions(export.FormatSteps)
		o.KeepFlat, o.MaxSampleTokens, o.Reasoning = true, 250, tc.reasoning
		out, st := run(t, []export.Source{src}, o)
		if got := trainedSteps(t, export.FormatSteps, out); !reflect.DeepEqual(got, tc.kept) {
			t.Errorf("reasoning %s: kept %v, want %v", tc.reasoning, got, tc.kept)
		}
		n := 3 - len(tc.kept)
		if st.Drops["step:sample_too_long"] != n || st.DroppedByRole["worker"]["sample_too_long"] != n || st.CandidatesByRole["worker"] != 3 {
			t.Errorf("reasoning %s: drops %v by role %v candidates %v; want %d sample_too_long of 3 worker steps", tc.reasoning, st.Drops, st.DroppedByRole, st.CandidatesByRole, n)
		}
		if r := st.LengthDropRates()["worker"]; r != float64(n)/3 {
			t.Errorf("reasoning %s: worker length drop rate %v, want %v", tc.reasoning, r, float64(n)/3)
		}
	}
	// --max-prompt-tokens keeps working and is counted the same way.
	o := export.DefaultOptions(export.FormatSteps)
	o.KeepFlat, o.MaxPromptTokens = true, 150
	if _, st := run(t, []export.Source{src}, o); st.DroppedByRole["worker"]["prompt_too_long"] != 2 || st.CandidatesByRole["worker"] != 3 {
		t.Errorf("max prompt tokens: by role %v candidates %v", st.DroppedByRole, st.CandidatesByRole)
	}
	// A conversation format ends before the steps that are too long, so the
	// rendered conversation fits as well.
	full := func(limit int) (steps []string, msgs int) {
		o := export.DefaultOptions(export.FormatGroups)
		o.KeepFlat, o.MaxSampleTokens = true, limit
		out, _ := run(t, []export.Source{src}, o)
		recs := decode(t, out)
		if len(recs) != 1 {
			t.Fatalf("limit %d: %d group records", limit, len(recs))
		}
		tr := recs[0]["trajectories"].([]any)[0].(map[string]any)
		for _, s := range tr["steps"].([]any) {
			steps = append(steps, s.(string))
		}
		return steps, len(tr["messages_and_choices"].([]any))
	}
	allSteps, allMsgs := full(0)
	cutSteps, cutMsgs := full(250)
	if len(allSteps) != 3 || !reflect.DeepEqual(cutSteps, []string{"solo.1", "solo.2"}) || cutMsgs >= allMsgs {
		t.Errorf("groups: steps %v (%d messages) without a limit, %v (%d messages) with one; want the chain cut after solo.2", allSteps, allMsgs, cutSteps, cutMsgs)
	}
}

func TestCheckLengthDropRate(t *testing.T) {
	st := export.Stats{
		CandidatesByRole: map[string]int{"worker": 10, "manager": 4, "compactor": 2},
		DroppedByRole: map[string]map[string]int{
			"worker":    {"sample_too_long": 2, "prompt_too_long": 1, "rule:x": 7},
			"manager":   {"rule:x": 3},
			"compactor": {"sample_too_long": 1},
		},
	}
	if err := st.CheckLengthDropRate(0.5); err != nil {
		t.Errorf("50%% allowed: %v", err)
	}
	if err := st.CheckLengthDropRate(1); err != nil {
		t.Errorf("1 never fails: %v", err)
	}
	err := st.CheckLengthDropRate(0.25)
	if !errors.Is(err, export.ErrDropRate) || !strings.Contains(err.Error(), "compactor: 1 of 2 steps (50.0%)") || !strings.Contains(err.Error(), "worker: 3 of 10 steps (30.0%)") || strings.Contains(err.Error(), "manager") {
		t.Errorf("25%% allowed: %v", err)
	}
	if err := st.CheckLengthDropRate(0); err == nil || strings.Contains(err.Error(), "manager") {
		t.Errorf("0 fails on any length drop, and only for roles that had one: %v", err)
	}
	// Through an export: two of three worker steps too long.
	o := export.DefaultOptions(export.FormatSteps)
	o.KeepFlat, o.MaxSampleTokens, o.Reasoning = true, 250, "field"
	_, got := run(t, []export.Source{lengthRun(t)}, o)
	if err := got.CheckLengthDropRate(0.5); !errors.Is(err, export.ErrDropRate) {
		t.Errorf("2 of 3 dropped with 50%% allowed: %v", err)
	}
}

func TestExcludedSourcesAreDroppedEverywhere(t *testing.T) {
	keep := dsTeam(t, 0, nil)
	gone := dsTeam(t, 1, nil)
	gone.Excluded = true
	for _, f := range export.Formats() {
		o := export.DefaultOptions(f)
		o.KeepFlat, o.Inline = true, true
		o.DropFlagged = false // exclusion does not depend on the flag filters
		out, st := run(t, []export.Source{keep, gone}, o)
		if st.Drops["episode:excluded"] != 1 || st.Episodes != 2 {
			t.Errorf("%s: drops %v of %d episodes, want episode:excluded 1", f, st.Drops, st.Episodes)
		}
		if strings.Contains(out, `"shop/1`) {
			t.Errorf("%s: the excluded episode shop/1 is in the output", f)
		}
	}
}
