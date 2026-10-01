package export_test

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/export"
)

func withFlags(src export.Source, flags ...string) export.Source {
	ep := *src.Episode
	ep.Flags = append([]string(nil), src.Episode.Flags...)
	for _, f := range flags {
		ep.AddFlag(f)
	}
	return export.Source{Episode: &ep, Prompts: src.Prompts}
}

func TestHardFlagsDropEpisodes(t *testing.T) {
	base := rollout(t, variant{plan: "A", reward: 1, pass: true, tokens: true})
	for _, flag := range []string{rl.FlagInfraError, rl.FlagTruncated, rl.FlagReplayMismatch, rl.FlagContaminated, rl.FlagHackProtected, rl.FlagHackNetwork} {
		src := withFlags(base, flag)
		out, st := run(t, []export.Source{src}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true})
		reason := "episode:" + strings.Replace(flag, "hack:", "hack_", 1)
		if out != "" || st.Drops[reason] != 1 || st.Episodes != 1 || st.Kept != 0 {
			t.Errorf("%s: out=%d drops=%v", flag, len(out), st.Drops)
		}
		// DropFlagged=false exports everything, flagged or not.
		out, st = run(t, []export.Source{src}, export.Options{Format: export.FormatSteps, KeepFlat: true})
		if out == "" || len(st.Drops) != 0 {
			t.Errorf("%s: DropFlagged=false must keep the episode: %v", flag, st.Drops)
		}
	}
	// Soft flags never drop.
	for _, flag := range []string{rl.FlagBudgetExceeded} {
		if out, _ := run(t, []export.Source{withFlags(base, flag)}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true}); out == "" {
			t.Errorf("%s dropped an episode", flag)
		}
	}
}

func TestTokenMismatchOnlyMattersForTokens(t *testing.T) {
	src := withFlags(rollout(t, variant{plan: "A", reward: 1, pass: true, tokens: true}), rl.FlagTokenMismatch)
	o := export.Options{KeepFlat: true, DropFlagged: true}
	for _, f := range []export.Format{export.FormatSteps, export.FormatSFT, export.FormatGroups, export.FormatKTO, export.FormatCanonical} {
		o.Format = f
		if out, _ := run(t, []export.Source{src}, o); out == "" {
			t.Errorf("%s: a token trace problem must not cost the text data", f)
		}
	}
	o.Format = export.FormatTokens
	out, st := run(t, []export.Source{src}, o)
	if out != "" || st.Drops["episode:token_mismatch"] != 1 {
		t.Errorf("tokens: %d bytes, %v", len(out), st.Drops)
	}
}

func TestWeakLabelsAreOptIn(t *testing.T) {
	src, _ := fixture(t) // no verifier: weak_label
	if !src.Episode.Has(rl.FlagWeakLabel) {
		t.Fatalf("flags: %v", src.Episode.Flags)
	}
	for _, f := range []export.Format{export.FormatSteps, export.FormatTokens, export.FormatGroups, export.FormatSFT, export.FormatKTO, export.FormatDPO} {
		out, st := run(t, []export.Source{src}, export.Options{Format: f, KeepFlat: true, DropFlagged: true})
		if out != "" || st.Drops["episode:weak_label"] != 1 {
			t.Errorf("%s: weak labels must be opt-in: %d bytes %v", f, len(out), st.Drops)
		}
		out, _ = run(t, []export.Source{src}, export.Options{Format: f, KeepFlat: true, DropFlagged: true, KeepWeak: true})
		if out == "" && f != export.FormatDPO {
			t.Errorf("%s: KeepWeak must keep them", f)
		}
	}
	// Archives keep them regardless.
	for _, f := range []export.Format{export.FormatCanonical, export.FormatATIF} {
		if out, _ := run(t, []export.Source{src}, export.Options{Format: f, DropFlagged: true, Inline: true}); out == "" {
			t.Errorf("%s: archives keep weak episodes", f)
		}
	}
}

func TestAdvantageHookRunsOnClones(t *testing.T) {
	srcs := group(t, 4)
	before := srcs[0].Episode.Agents[0].Steps[0].Advantage
	var seen int
	hook := func(eps []*rl.Episode) error {
		seen = len(eps)
		var mean float64
		for _, e := range eps {
			mean += e.Reward.Total
		}
		mean /= float64(len(eps))
		for _, e := range eps {
			for ai := range e.Agents {
				for si := range e.Agents[ai].Steps {
					e.Agents[ai].Steps[si].Advantage = e.Reward.Total - mean
				}
			}
			e.Reward.Total += 100 // hooks may scribble; the caller's episodes must not change
		}
		return nil
	}
	out, _ := run(t, srcs, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true, Advantage: hook})
	if seen != 4 {
		t.Fatalf("the hook must see the kept episodes: %d", seen)
	}
	recs := decode(t, out)
	advs := map[string]float64{}
	for _, r := range recs {
		a, ok := r["advantage"].(float64)
		if !ok {
			t.Fatalf("with a hook every record carries an advantage: %v", r)
		}
		advs[r["id"].(string)] = a
	}
	if math.Abs(advs["T/0#solo.1"]-0.5) > 1e-9 || math.Abs(advs["T/1#solo.1"]+0.5) > 1e-9 {
		t.Fatalf("advantages: %v", advs)
	}
	if srcs[0].Episode.Agents[0].Steps[0].Advantage != before || srcs[0].Episode.Reward.Total != 1 {
		t.Fatal("the hook must run on clones: the caller's episodes changed")
	}
	// A failing hook aborts with its error and writes nothing.
	var sb strings.Builder
	_, err := export.Export(&sb, srcs, export.Options{Format: export.FormatSteps, KeepFlat: true, Advantage: func([]*rl.Episode) error { return fmt.Errorf("boom") }})
	if err == nil || !strings.Contains(err.Error(), "boom") || sb.Len() != 0 {
		t.Fatalf("err %v, wrote %d bytes", err, sb.Len())
	}
	// The hook sees only what survived the drops.
	seen = -1
	run(t, []export.Source{srcs[0], withFlags(srcs[1], rl.FlagInfraError)}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true, Advantage: hook})
	if seen != 1 {
		t.Fatalf("hook saw %d episodes, want 1", seen)
	}
}

func TestZeroVarianceGroups(t *testing.T) {
	flat := []export.Source{
		rollout(t, variant{sample: 0, plan: "A", reward: 1, pass: true, tokens: true}),
		rollout(t, variant{sample: 1, plan: "B", reward: 1, pass: true, tokens: true}),
	}
	mixed := group(t, 2)
	for _, f := range []export.Format{export.FormatSteps, export.FormatTokens, export.FormatGroups} {
		out, st := run(t, flat, export.Options{Format: f, DropFlagged: true, KeepWeak: true})
		if out != "" || st.Drops["episode:flat_group"] != 2 {
			t.Errorf("%s: a group whose rewards are all equal teaches nothing: %d bytes %v", f, len(out), st.Drops)
		}
		if out, _ := run(t, flat, export.Options{Format: f, DropFlagged: true, KeepFlat: true}); out == "" {
			t.Errorf("%s: KeepFlat must keep flat groups", f)
		}
		if out, _ := run(t, mixed, export.Options{Format: f, DropFlagged: true}); out == "" {
			t.Errorf("%s: a group with different rewards is kept", f)
		}
	}
	// Non-RL formats do not care.
	if out, _ := run(t, flat, export.Options{Format: export.FormatSFT, DropFlagged: true}); out == "" {
		t.Error("sft keeps flat groups")
	}
	// A single episode is a flat group (nothing to compare with).
	if out, _ := run(t, flat[:1], export.Options{Format: export.FormatSteps, DropFlagged: true}); out != "" {
		t.Error("a lone episode has no group signal")
	}
	// Equal episode rewards but a role reward that differs across the group is a signal.
	a, b := rollout(t, variant{sample: 0, plan: "A", reward: 1, pass: true}), rollout(t, variant{sample: 1, plan: "B", reward: 1, pass: true})
	a.Episode.Agents[0].Reward = rl.Reward{Total: 1.1, Components: map[string]float64{"evidence": 0.1}}
	b.Episode.Agents[0].Reward = rl.Reward{Total: 0.9, Components: map[string]float64{"evidence": -0.1}}
	if out, _ := run(t, []export.Source{a, b}, export.Options{Format: export.FormatSteps, DropFlagged: true}); out == "" {
		t.Error("role-level reward variance is a learning signal")
	}
	// Non-zero advantages from a hook also keep a group.
	hook := func(eps []*rl.Episode) error { eps[0].Agents[0].Steps[0].Advantage = 0.3; return nil }
	if out, _ := run(t, flat, export.Options{Format: export.FormatSteps, DropFlagged: true, Advantage: hook}); out == "" {
		t.Error("a group with a non-zero advantage has signal")
	}
	// Groups are judged separately.
	other := rollout(t, variant{sample: 0, plan: "A", reward: 1, pass: true})
	other.Episode.Group, other.Episode.TaskID = "other@p", "U"
	other2 := rollout(t, variant{sample: 1, plan: "B", reward: 0, pass: false})
	other2.Episode.Group, other2.Episode.TaskID = "other@p", "U"
	_, st := run(t, append(append([]export.Source(nil), flat...), other, other2), export.Options{Format: export.FormatSteps, DropFlagged: true})
	if st.Drops["episode:flat_group"] != 2 || st.Kept != 2 {
		t.Errorf("per-group judgement: %+v", st)
	}
}

func TestRoleAliasAndFilter(t *testing.T) {
	src := swarmSource(t, false)
	count := func(roles ...string) export.Stats {
		_, st := run(t, []export.Source{src}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true, Roles: roles})
		return st
	}
	if st := count("manager"); st.Records != 4 || st.ByRole["manager"] != 4 {
		t.Errorf("manager: %+v", st)
	}
	if st := count("compactor"); st.Records != 1 {
		t.Errorf("compactor: %+v", st)
	}
	if st := count("backend"); st.Records != 8 {
		t.Errorf("backend by exact name: %+v", st)
	}
	if st := count("worker"); st.Records != 8 || st.ByRole["backend"] != 8 {
		t.Errorf("worker aliases domain roles: %+v", st)
	}
	if st := count("worker", "manager", "compactor"); st.Records != 13 {
		t.Errorf("the documented flag combination: %+v", st)
	}
	if st := count(); st.Records != 13 { // reviewer runs on a teacher and is not trainable
		t.Errorf("no filter: %+v", st)
	}
	if st := count("reviewer"); st.Records != 0 || st.Drops["step:not_trainable"] != 2 {
		t.Errorf("reviewer steps are teacher steps: %+v", st)
	}
	if st := count("nonsense"); st.Records != 0 {
		t.Errorf("unknown role: %+v", st)
	}
}

func TestTeacherModelsAreNeverTrainable(t *testing.T) {
	src := swarmSource(t, false)
	// RL formats: never, even when the model is listed.
	for _, f := range []export.Format{export.FormatSteps, export.FormatTokens, export.FormatGroups} {
		out, st := run(t, []export.Source{src}, export.Options{Format: f, KeepFlat: true, DropFlagged: true, TeacherOK: []string{"teacher-x"}})
		if strings.Contains(out, "teacher-x") && f != export.FormatGroups {
			t.Errorf("%s: teacher output exported as trainable", f)
		}
		if f == export.FormatSteps && st.Drops["step:not_trainable"] != 2 {
			t.Errorf("%s: %v", f, st.Drops)
		}
	}
	// sft: refused unless listed, and counted.
	out, st := run(t, []export.Source{src}, export.Options{Format: export.FormatSFT, KeepFlat: true, DropFlagged: true})
	if strings.Contains(out, "rv-1") || st.TeacherSkipped["teacher-x"] != 2 || st.Drops["step:teacher_not_ok"] != 2 {
		t.Errorf("sft without TeacherOK: skipped=%v drops=%v", st.TeacherSkipped, st.Drops)
	}
	out, st = run(t, []export.Source{src}, export.Options{Format: export.FormatSFT, KeepFlat: true, DropFlagged: true, TeacherOK: []string{"teacher-x"}})
	if !strings.Contains(out, "rv-1") || st.TeacherUsed["teacher-x"] != 2 || st.TeacherSkipped["teacher-x"] != 0 {
		t.Errorf("sft with TeacherOK: used=%v skipped=%v", st.TeacherUsed, st.TeacherSkipped)
	}
	// Another model in the list does not help.
	_, st = run(t, []export.Source{src}, export.Options{Format: export.FormatKTO, KeepFlat: true, DropFlagged: true, TeacherOK: []string{"someone-else"}})
	if st.TeacherSkipped["teacher-x"] != 2 {
		t.Errorf("kto: %v", st.TeacherSkipped)
	}
	// Archives carry teacher steps, marked as not trainable.
	out, st = run(t, []export.Source{src}, export.Options{Format: export.FormatCanonical, DropFlagged: true, Inline: true})
	if !strings.Contains(out, `"trainable":false`) || st.TeacherUsed["archived:teacher-x"] != 2 {
		t.Errorf("canonical: %v", st.TeacherUsed)
	}
}

func TestLicenseFilter(t *testing.T) {
	a := rollout(t, variant{sample: 0, plan: "A", reward: 1, pass: true})
	a.Episode.Provenance.License = "MIT"
	b := rollout(t, variant{sample: 1, plan: "B", reward: 0, pass: false})
	b.Episode.Provenance.License = "GPL-3.0-only"
	c := rollout(t, variant{sample: 2, plan: "C", reward: 1, pass: true})
	o := export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true, Licenses: []string{"mit", " Apache-2.0 "}}
	out, st := run(t, []export.Source{a, b, c}, o)
	recs := decode(t, out)
	if len(recs) != 2 || st.Drops["episode:license"] != 1 || st.Drops["episode:license_unknown"] != 1 {
		t.Fatalf("%d records, drops %v", len(recs), st.Drops)
	}
	if recs[0]["meta"].(map[string]any)["license"] != "MIT" {
		t.Fatalf("the licence travels with the record: %v", recs[0]["meta"])
	}
	// No allow-list: everything passes.
	o.Licenses = nil
	if _, st := run(t, []export.Source{a, b, c}, o); len(st.Drops) != 0 {
		t.Fatalf("drops: %v", st.Drops)
	}
}

func TestSFTSelection(t *testing.T) {
	var srcs []export.Source
	rewards := []float64{0.9, 0.2, 1.0, -0.3, 0.6}
	for i, r := range rewards {
		srcs = append(srcs, rollout(t, variant{sample: i, plan: fmt.Sprintf("p%d", i), reward: r, pass: r > 0.5}))
	}
	failing := rollout(t, variant{sample: 9, plan: "cheat", reward: 0.95, pass: false}) // shaped reward, but the verifier failed
	srcs = append(srcs, failing)
	ids := func(o export.Options) []string {
		out, _ := run(t, srcs, o)
		seen := map[string]bool{}
		var ep []string
		for _, r := range decode(t, out) {
			e := r["id"].(string)[:strings.Index(r["id"].(string), "#")]
			if !seen[e] {
				seen[e] = true
				ep = append(ep, e)
			}
		}
		return ep
	}
	base := export.Options{Format: export.FormatSFT, DropFlagged: true}
	if got := ids(base); fmt.Sprint(got) != "[T/0 T/2 T/4]" { // verified passes; the failed verifier and low rewards are out
		t.Fatalf("verified episodes with reward >= 0: %v", got)
	}
	o := base
	o.MinReward = 0.7
	if got := ids(o); fmt.Sprint(got) != "[T/0 T/2]" {
		t.Fatalf("MinReward: %v", got)
	}
	o.TopK = 1
	if got := ids(o); fmt.Sprint(got) != "[T/2]" {
		t.Fatalf("TopK=1 keeps the best per task: %v", got)
	}
	o.MinReward, o.TopK = 0, 2
	if got := ids(o); fmt.Sprint(got) != "[T/0 T/2]" {
		t.Fatalf("TopK=2: %v", got)
	}
	_, st := run(t, srcs, export.Options{Format: export.FormatSFT, DropFlagged: true, MinReward: 0.7})
	if st.Drops["episode:not_verified"] != 3 || st.Drops["episode:below_min_reward"] != 1 {
		t.Fatalf("drops: %v", st.Drops)
	}
}

func TestSplitIsByRepositoryAndDeterministic(t *testing.T) {
	var srcs []export.Source
	for i := 0; i < 40; i++ {
		s := rollout(t, variant{sample: i % 2, plan: "p", reward: float64(i % 2), pass: i%2 == 0})
		s.Episode.TaskID = fmt.Sprintf("task-%02d", i)
		s.Episode.Group = fmt.Sprintf("g-%02d", i)
		s.Episode.ID = fmt.Sprintf("%s/%d", s.Episode.TaskID, s.Episode.Sample)
		s.Episode.Env.Repo = fmt.Sprintf("https://example.org/repo-%d", i%10) // four episodes per repo
		srcs = append(srcs, s)
	}
	o := export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true, Split: "train:0.75,val:0.25", Seed: 7}
	out, st := run(t, srcs, o)
	splitOf := map[string]string{}
	for _, r := range decode(t, out) {
		repo := r["task_id"].(string)
		_ = repo
		s := r["split"].(string)
		key := srcsRepo(srcs, r["task_id"].(string))
		if prev, ok := splitOf[key]; ok && prev != s {
			t.Fatalf("repository %s straddles %s and %s", key, prev, s)
		}
		splitOf[key] = s
	}
	if len(splitOf) != 10 || st.BySplit["train"] == 0 || st.BySplit["val"] == 0 || st.BySplit["train"]+st.BySplit["val"] != st.Records {
		t.Fatalf("splits: %v %v", splitOf, st.BySplit)
	}
	out2, _ := run(t, srcs, o)
	if out != out2 {
		t.Fatal("the split must be deterministic")
	}
	// The seed reshuffles; the assignment of a repository does not depend on the
	// other repositories present.
	o2 := o
	o2.Seed = 8
	out3, _ := run(t, srcs, o2)
	if out3 == out {
		t.Fatal("a different seed should give a different assignment")
	}
	subset, _ := run(t, srcs[:8], o)
	for _, r := range decode(t, subset) {
		if splitOf[srcsRepo(srcs, r["task_id"].(string))] != r["split"] {
			t.Fatal("a repository's split must not depend on which other episodes are exported")
		}
	}
	// No split option: no field.
	o.Split = ""
	if out, _ := run(t, srcs[:2], o); strings.Contains(out, `"split"`) {
		t.Fatal("no split requested, no field")
	}
	// Proportions are respected on a large sample of repositories.
	var many []export.Source
	for i := 0; i < 400; i++ {
		s := rollout(t, variant{plan: "p", reward: 1, pass: true})
		s.Episode.Env.Repo = fmt.Sprintf("r%d", i)
		s.Episode.TaskID, s.Episode.Group = fmt.Sprintf("t%d", i), fmt.Sprintf("g%d", i)
		s.Episode.ID = s.Episode.TaskID + "/0"
		many = append(many, s)
	}
	_, st = run(t, many, export.Options{Format: export.FormatCanonical, DropFlagged: true, Inline: true, Split: "train:0.9,val:0.1"})
	if v := st.BySplit["val"]; v < 20 || v > 60 {
		t.Fatalf("val share of 400 repositories should be about 40, got %d (%v)", v, st.BySplit)
	}
}

func srcsRepo(srcs []export.Source, task string) string {
	for _, s := range srcs {
		if s.Episode.TaskID == task {
			return s.Episode.Env.Repo
		}
	}
	return ""
}

func TestCaps(t *testing.T) {
	srcs := group(t, 4)
	out, st := run(t, srcs, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true, MaxSamples: 3})
	if len(lines(out)) != 3 || st.Records != 3 {
		t.Fatalf("MaxSamples: %d lines, %+v", len(lines(out)), st)
	}
	// The cap counts records, not episodes, in every format.
	for _, f := range export.Formats() {
		out, st := run(t, srcs, export.Options{Format: f, KeepFlat: true, KeepWeak: true, DropFlagged: true, Inline: true, MaxSamples: 1, Table: &strings.Builder{}})
		if len(lines(out)) != 1 || st.Records != 1 {
			t.Errorf("%s: %d records", f, st.Records)
		}
	}
	// MaxPromptTokens drops long prompts (usage-reported input size).
	src := rollout(t, variant{plan: "A", reward: 1, pass: true})
	first := src.Episode.Agents[0].Steps[0].Prompt.Tokens
	second := src.Episode.Agents[0].Steps[1].Prompt.Tokens
	if first >= second {
		t.Fatalf("prompts grow: %d then %d", first, second)
	}
	out, st = run(t, []export.Source{src}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true, MaxPromptTokens: first})
	if len(lines(out)) != 1 || st.Drops["step:prompt_too_long"] != 1 {
		t.Fatalf("MaxPromptTokens: %d lines %v", len(lines(out)), st.Drops)
	}
}

func TestUnresolvablePromptsAreDroppedAndReported(t *testing.T) {
	src := rollout(t, variant{plan: "A", reward: 1, pass: true})
	noRes := export.Source{Episode: src.Episode}
	out, st := run(t, []export.Source{noRes}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true})
	if out != "" || st.Drops["step:no_prompt"] != 2 || len(st.Warnings) == 0 || !strings.Contains(st.Warnings[0], "resolver") {
		t.Fatalf("out=%d drops=%v warnings=%v", len(out), st.Drops, st.Warnings)
	}
	// Steps that already carry an inline prompt need no resolver.
	inl := export.Source{Episode: src.Episode, Prompts: failingResolver{}}
	for ai := range inl.Episode.Agents {
		for si := range inl.Episode.Agents[ai].Steps {
			st := &inl.Episode.Agents[ai].Steps[si]
			p, _ := src.Prompts.Prompt(src.Episode, st)
			st.Inline = p
		}
	}
	out, st = run(t, []export.Source{inl}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true})
	if len(lines(out)) != 2 || len(st.Drops) != 0 {
		t.Fatalf("inline prompts: %d lines %v", len(lines(out)), st.Drops)
	}
	// An inline prompt that does not hash to the recorded wire hash is refused.
	inl.Episode.Agents[0].Steps[0].Inline = &core.Prompt{Model: "policy-1", Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text("tampered")}}}}
	out, st = run(t, []export.Source{inl}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true})
	if len(lines(out)) != 1 || st.Drops["step:no_prompt"] != 1 || !strings.Contains(strings.Join(st.Warnings, " "), "hashes to") {
		t.Fatalf("tampered inline prompt: %d lines %v %v", len(lines(out)), st.Drops, st.Warnings)
	}
}

type failingResolver struct{}

func (failingResolver) Prompt(*rl.Episode, *rl.Step) (*core.Prompt, error) {
	return nil, fmt.Errorf("resolver must not be called")
}
