package export_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/export"
)

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func TestSFTRecords(t *testing.T) {
	src := rollout(t, variant{plan: "A", reward: 1, pass: true})
	out, st := run(t, []export.Source{src}, export.Options{Format: export.FormatSFT, DropFlagged: true})
	recs := decode(t, out)
	if len(recs) != 2 || st.ByUnit["step"] != 2 {
		t.Fatalf("per-step sft: %d records %v", len(recs), st.ByUnit)
	}
	for _, r := range recs {
		msgs := r["messages"].([]any)
		weights := ints(r["weights"])
		if len(msgs) != len(weights) || r["schema"] != export.SchemaSFT || r["tools"] == nil {
			t.Fatalf("record: %v", r["id"])
		}
		for i, m := range msgs {
			mm := asMap(m)
			w, hasWeight := mm["weight"]
			if mm["role"] == "assistant" != hasWeight {
				t.Fatalf("only assistant messages carry a weight: %v", mm)
			}
			if hasWeight && int(w.(float64)) != weights[i] {
				t.Fatalf("message weight %v disagrees with weights[%d]=%d", w, i, weights[i])
			}
			want := 0
			if i == len(msgs)-1 {
				want = 1
			}
			if weights[i] != want {
				t.Fatalf("only the completion is trained in a per-step record: weights %v", weights)
			}
			// SFT keeps the OpenAI shape: tool-call arguments are JSON strings.
			for _, c := range asSlice(mm["tool_calls"]) {
				if _, isString := asMap(asMap(c)["function"])["arguments"].(string); !isString {
					t.Fatalf("sft arguments must be JSON strings: %v", c)
				}
			}
		}
	}
	// Packed: one conversation per segment, every trained turn weighted.
	o := export.Options{Format: export.FormatSFT, DropFlagged: true, PackSegments: true}
	out, st = run(t, []export.Source{src}, o)
	recs = decode(t, out)
	if len(recs) != 1 || st.ByUnit["segment"] != 1 {
		t.Fatalf("packed sft: %d records", len(recs))
	}
	w := ints(recs[0]["weights"])
	sum := 0
	for _, x := range w {
		sum += x
	}
	if sum != 2 || w[len(w)-1] != 1 || len(recs[0]["steps"].([]any)) != 2 || recs[0]["id"] != "T/0#solo.1+solo.2" {
		t.Fatalf("packed weights %v steps %v id %v", w, recs[0]["steps"], recs[0]["id"])
	}
	msgs := recs[0]["messages"].([]any)
	if asMap(msgs[0])["role"] != "system" || len(msgs) != len(w) {
		t.Fatal("messages")
	}
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

func TestSFTPackedWeightsExcludeNonTargets(t *testing.T) {
	src := chainRun(t, 4, nil)
	// A teacher step in the middle of a chain is context, not a target.
	src.Episode.Agents[0].Steps[1].Trainable = false
	src.Episode.Agents[0].Steps[1].Model = "teacher-x"
	o := export.Options{Format: export.FormatSFT, DropFlagged: true, PackSegments: true}
	out, st := run(t, []export.Source{src}, o)
	recs := decode(t, out)
	w := ints(recs[0]["weights"])
	var assistants []int
	for i, m := range recs[0]["messages"].([]any) {
		if asMap(m)["role"] == "assistant" {
			assistants = append(assistants, w[i])
		}
	}
	if fmt.Sprint(assistants) != "[1 0 1 1]" || st.TeacherSkipped["teacher-x"] != 1 {
		t.Fatalf("assistant weights %v skipped %v", assistants, st.TeacherSkipped)
	}
	o.TeacherOK = []string{"teacher-x"}
	out, st = run(t, []export.Source{src}, o)
	w = ints(decode(t, out)[0]["weights"])
	sum := 0
	for _, x := range w {
		sum += x
	}
	if sum != 4 || st.TeacherUsed["teacher-x"] != 1 {
		t.Fatalf("with TeacherOK the teacher's turn is a target: %v used %v", w, st.TeacherUsed)
	}
}

func TestGroupsRecords(t *testing.T) {
	srcs := group(t, 4)
	out, st := run(t, srcs, export.Options{Format: export.FormatGroups, DropFlagged: true})
	recs := decode(t, out)
	if len(recs) != 1 || st.ByUnit["group"] != 1 || st.Records != 1 {
		t.Fatalf("one record per group: %d", len(recs))
	}
	g := recs[0]
	if g["schema"] != export.SchemaGroup || g["task_id"] != "T" || g["group"] != "T@policy-1" {
		t.Fatalf("group header: %v", g)
	}
	trajs := g["trajectories"].([]any)
	if len(trajs) != 4 {
		t.Fatalf("%d trajectories", len(trajs))
	}
	first := asMap(trajs[0])
	if first["episode"] != "T/0" || first["role"] != "backend" || first["reward"].(float64) != 1 || asMap(trajs[1])["reward"].(float64) != 0 {
		t.Fatalf("trajectory: %v", first)
	}
	if first["tools"] == nil || len(first["steps"].([]any)) != 2 {
		t.Fatalf("trajectory: %v", first)
	}
	mc := first["messages_and_choices"].([]any)
	// system, user(preamble+task), assistant choice, tool, assistant choice
	var choices []map[string]any
	for _, m := range mc {
		if mm := asMap(m); mm["finish_reason"] != nil {
			choices = append(choices, mm)
		} else if mm["role"] == nil {
			t.Fatalf("plain entries are wire messages: %v", mm)
		}
	}
	if len(choices) != 2 || asMap(choices[0]["message"])["role"] != "assistant" || choices[0]["finish_reason"] != "tool_calls" || choices[1]["finish_reason"] != "stop" {
		t.Fatalf("choices: %v", choices)
	}
	lp := asMap(choices[0]["logprobs"])
	content := lp["content"].([]any)
	tr := srcs[0].Episode.Agents[0].Steps[0].Tokens
	if len(content) != len(tr.CompletionIDs) {
		t.Fatalf("logprobs: %d entries for %d completion ids", len(content), len(tr.CompletionIDs))
	}
	e0 := asMap(content[0])
	if e0["token"] != fmt.Sprintf("token_id:%d", tr.CompletionIDs[0]) || e0["logprob"].(float64) != float64(tr.Logprobs[0]) {
		t.Fatalf("logprob entry: %v", e0)
	}
	// The choice's message is what the next prompt replays for that turn.
	next := 0
	for i, m := range mc {
		if asMap(m)["finish_reason"] != nil {
			if next == 0 {
				next = i
			}
		}
	}
	if asMap(mc[next-1])["role"] != "user" {
		t.Fatalf("the first choice follows the prompt: %v", asMap(mc[next-1]))
	}
	metrics := asMap(first["metrics"])
	if metrics["steps"].(float64) != 2 || metrics["reward/outcome"].(float64) != 1 || metrics["signal/steps"].(float64) != 2 {
		t.Fatalf("metrics: %v", metrics)
	}
	// Without traces the choices carry logprobs: null (never invented).
	plain := rollout(t, variant{sample: 0, plan: "A", reward: 1, pass: true, tokens: false})
	other := rollout(t, variant{sample: 1, plan: "B", reward: 0, pass: false, tokens: false})
	out, _ = run(t, []export.Source{plain, other}, export.Options{Format: export.FormatGroups, DropFlagged: true})
	if !strings.Contains(out, `"logprobs":null`) || strings.Contains(out, `"token_id:`) {
		t.Fatal("no trace, no logprobs")
	}
}

func TestGroupsSwarmTrajectories(t *testing.T) {
	src := swarmSource(t, true)
	out, st := run(t, []export.Source{src}, export.Options{Format: export.FormatGroups, KeepFlat: true, DropFlagged: true})
	g := decode(t, out)[0]
	trajs := g["trajectories"].([]any)
	type key struct {
		agent string
		seg   int
		steps int
	}
	var got []key
	for _, x := range trajs {
		m := asMap(x)
		got = append(got, key{m["agent"].(string), int(m["segment"].(float64)), len(m["steps"].([]any))})
	}
	want := []key{{"mgr", 0, 2}, {"mgr", 1, 2}, {"be-1", 0, 3}, {"be-1", 0, 1}, {"be-1", 1, 5}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("trajectories %v want %v (the reviewer runs on a teacher and has none; the fork is its own trajectory)", got, want)
	}
	if st.ByRole["compactor"] != 1 || st.ByRole["reviewer"] != 0 {
		t.Fatalf("roles: %v", st.ByRole)
	}
	// The hot-tail steps stay aligned with the messages the last prompt replays.
	if st.Drops["step:choice_misaligned"] != 0 {
		t.Fatalf("drops: %v", st.Drops)
	}
	// Choices in the worker's second trajectory: five model turns.
	for _, x := range trajs {
		m := asMap(x)
		if m["agent"] == "be-1" && m["segment"].(float64) == 1 {
			n := 0
			for _, e := range m["messages_and_choices"].([]any) {
				if asMap(e)["finish_reason"] != nil {
					n++
				}
			}
			if n != 5 {
				t.Fatalf("%d choices, want 5", n)
			}
		}
	}
}

func TestDPOPairs(t *testing.T) {
	srcs := group(t, 4)
	out, st := run(t, srcs, export.Options{Format: export.FormatDPO, DropFlagged: true})
	recs := decode(t, out)
	if len(recs) != 2 || st.ByUnit["pair"] != 2 {
		t.Fatalf("%d pairs: %v", len(recs), st.Drops)
	}
	step, ep := recs[0], recs[1]
	if step["unit"] != "step" || ep["unit"] != "episode" {
		t.Fatalf("units: %v %v", step["unit"], ep["unit"])
	}
	// Step-level: the identical first prompt, best vs worst by reward.
	if step["chosen_episode"] != "T/0" || step["rejected_episode"] != "T/3" || step["chosen_reward"].(float64) != 1 || step["rejected_reward"].(float64) != 0 {
		t.Fatalf("step pair: %v", step)
	}
	ch := asMap(step["chosen"].([]any)[0])
	rj := asMap(step["rejected"].([]any)[0])
	if ch["role"] != "assistant" || ch["content"] != "plan A" || rj["content"] != "plan D" {
		t.Fatalf("completions: %v / %v", ch, rj)
	}
	if len(step["prompt"].([]any)) != 2 || step["tools"] == nil || step["wire_hash"] == "" {
		t.Fatalf("prompt: %v", step["prompt"])
	}
	// Episode-level: the whole continuation after the shared first prompt.
	c := ep["chosen"].([]any)
	r := ep["rejected"].([]any)
	if len(c) != 3 || len(r) != 3 || asMap(c[2])["content"] != "fixed: plan A" || asMap(r[2])["content"] != "fixed: plan D" {
		t.Fatalf("episode pair: %v / %v", c, r)
	}
	if asMap(c[0])["role"] != "assistant" || asMap(c[1])["role"] != "tool" {
		t.Fatalf("continuation shape: %v", c)
	}
	// Determinism of ids.
	out2, _ := run(t, srcs, export.Options{Format: export.FormatDPO, DropFlagged: true})
	if out != out2 {
		t.Fatal("dpo output must be deterministic")
	}
}

func TestDPOEdgeCases(t *testing.T) {
	// No reward gap.
	flat := []export.Source{rollout(t, variant{sample: 0, plan: "A", reward: 1, pass: true}), rollout(t, variant{sample: 1, plan: "B", reward: 1, pass: true})}
	out, st := run(t, flat, export.Options{Format: export.FormatDPO, DropFlagged: true})
	if out != "" || st.Drops["pair:no_reward_gap_or_same_completion"] == 0 || st.Drops["pair:no_reward_gap"] != 1 {
		t.Fatalf("flat: %v", st.Drops)
	}
	// Same completion: no preference to learn.
	same := []export.Source{rollout(t, variant{sample: 0, plan: "A", reward: 1, pass: true}), rollout(t, variant{sample: 1, plan: "A", reward: 0, pass: false})}
	if out, _ := run(t, same, export.Options{Format: export.FormatDPO, DropFlagged: true}); strings.Contains(out, `"unit":"step"`) && strings.Count(out, `"unit":"step"`) > 1 {
		t.Fatal("identical completions form no step pair")
	}
	out, _ = run(t, same, export.Options{Format: export.FormatDPO, DropFlagged: true})
	for _, r := range decode(t, out) {
		if r["unit"] == "step" && r["wire_hash"] == same[0].Episode.Agents[0].Steps[0].Prompt.WireHash {
			t.Fatal("same completion on the same prompt is not a pair")
		}
	}
	// MinReward applies to the chosen side.
	mixed := group(t, 2)
	if out, st := run(t, mixed, export.Options{Format: export.FormatDPO, DropFlagged: true, MinReward: 2}); out != "" || st.Drops["pair:below_min_reward"] == 0 {
		t.Fatalf("min reward: %v", st.Drops)
	}
	// Different first prompts cannot be compared at episode level.
	a := rollout(t, variant{sample: 0, plan: "A", reward: 1, pass: true, user: "task one"})
	b := rollout(t, variant{sample: 1, plan: "B", reward: 0, pass: false, user: "task two"})
	out, st = run(t, []export.Source{a, b}, export.Options{Format: export.FormatDPO, DropFlagged: true})
	if out != "" || st.Drops["pair:prompt_differs"] != 1 {
		t.Fatalf("prompt_differs: %v", st.Drops)
	}
	// The fixture's agent rebases, so there is no whole-episode pair.
	fx, _ := fixture(t)
	fx2 := fx
	_, st = run(t, []export.Source{fx, fx2}, export.Options{Format: export.FormatDPO, DropFlagged: true, KeepWeak: true})
	if st.Drops["pair:multi_segment"] != 2 {
		t.Fatalf("multi_segment: %v", st.Drops)
	}
	// Teacher steps are not preference targets unless allowed.
	tch := rollout(t, variant{sample: 0, plan: "A", reward: 1, pass: true, model: "teacher-x"})
	tch.Episode.Agents[0].Steps[0].Trainable = false
	tch.Episode.Agents[0].Steps[1].Trainable = false
	own := rollout(t, variant{sample: 1, plan: "B", reward: 0, pass: false})
	if out, _ := run(t, []export.Source{tch, own}, export.Options{Format: export.FormatDPO, DropFlagged: true}); out != "" {
		t.Fatal("a teacher completion must not be a chosen side without TeacherOK")
	}
}

func TestKTOLabels(t *testing.T) {
	srcs := group(t, 2)
	out, _ := run(t, srcs, export.Options{Format: export.FormatKTO, DropFlagged: true})
	labels := map[string]bool{}
	for _, r := range decode(t, out) {
		if r["schema"] != export.SchemaKTO {
			t.Fatal("schema")
		}
		labels[r["id"].(string)] = r["label"].(bool)
		if len(r["completion"].([]any)) != 1 || asMap(r["completion"].([]any)[0])["role"] != "assistant" {
			t.Fatalf("completion: %v", r["completion"])
		}
	}
	if !labels["T/0#solo.1"] || labels["T/1#solo.1"] {
		t.Fatalf("labels come from the verifier verdict: %v", labels)
	}
	// Without a verdict the reward decides.
	fx, _ := fixture(t)
	fx.Episode.Reward.Total = 0.4
	for _, tc := range []struct {
		min  float64
		want bool
	}{{0.3, true}, {0.5, false}} {
		out, _ := run(t, []export.Source{fx}, export.Options{Format: export.FormatKTO, DropFlagged: true, KeepWeak: true, MinReward: tc.min, MaxSamples: 1})
		if got := decode(t, out)[0]["label"]; got != tc.want {
			t.Fatalf("min %v: label %v", tc.min, got)
		}
	}
}

func TestATIFStructure(t *testing.T) {
	src := swarmSource(t, true)
	out, st := run(t, []export.Source{src}, export.Options{Format: export.FormatATIF, DropFlagged: true})
	recs := decode(t, out)
	if len(recs) != 1 || st.ByUnit["episode"] != 1 {
		t.Fatalf("one trajectory per episode: %d", len(recs))
	}
	tr := recs[0]
	if tr["schema_version"] != export.ATIFVersion || tr["session_id"] != "swarm-1/0" || asMap(tr["agent"])["name"] != "sleipnir" || asMap(tr["agent"])["model_name"] != "policy-1" {
		t.Fatalf("header: %v", tr)
	}
	steps := tr["steps"].([]any)
	first := asMap(steps[0])
	if first["source"] != "user" || first["message"] != "Ship the API" || first["step_id"].(float64) != 1 {
		t.Fatalf("the trajectory opens with the instruction: %v", first)
	}
	var spawn map[string]any
	for i, s := range steps[1:] {
		m := asMap(s)
		if m["source"] != "agent" || m["step_id"].(float64) != float64(i+2) || m["timestamp"] == "" {
			t.Fatalf("agent step: %v", m)
		}
		met := asMap(m["metrics"])
		if met["prompt_tokens"] == nil || met["prompt_token_ids"] == nil || met["completion_token_ids"] == nil || met["logprobs"] == nil {
			t.Fatalf("metrics with token ids: %v", met)
		}
		if calls := asSlice(m["tool_calls"]); len(calls) > 0 {
			c := asMap(calls[0])
			if _, isObj := c["arguments"].(map[string]any); !isObj {
				t.Fatalf("arguments are JSON objects: %v", c)
			}
			if c["function_name"] == "spawn" {
				spawn = m
			}
		}
	}
	if spawn == nil {
		t.Fatal("no spawn step")
	}
	// The observation of the spawning call points at the children.
	res := asSlice(asMap(spawn["observation"])["results"])
	refs := asSlice(asMap(res[0])["subagent_trajectory_ref"])
	if fmt.Sprint(refs) != "[swarm-1/0/be-1 swarm-1/0/rv-1]" || asMap(res[0])["content"] != "started be-1" {
		t.Fatalf("spawn observation: %v", res)
	}
	subs := tr["subagent_trajectories"].([]any)
	if len(subs) != 2 || asMap(subs[0])["session_id"] != "swarm-1/0/be-1" || asMap(asMap(subs[0])["extra"])["parent"] != "mgr" {
		t.Fatalf("subagents: %v", subs)
	}
	// Segment boundaries are marked.
	worker := asMap(subs[0])
	var marks []string
	for _, s := range worker["steps"].([]any) {
		if cm := asMap(asMap(s)["context_management"]); cm != nil {
			marks = append(marks, fmt.Sprint(cm["type"], ":", cm["boundary"]))
		}
	}
	if fmt.Sprint(marks) != "[compact:1]" {
		t.Fatalf("context_management: %v", marks)
	}
	// The fork appears as a step of its agent, flagged by kind.
	var fork bool
	for _, s := range worker["steps"].([]any) {
		if asMap(asMap(s)["extra"])["kind"] == "compactor" {
			fork = true
		}
	}
	if !fork {
		t.Fatal("compactor step missing")
	}
	fm := asMap(tr["final_metrics"])
	if fm["total_steps"].(float64) != float64(len(steps)) || fm["total_prompt_tokens"].(float64) == 0 || fm["total_cost_usd"] == nil {
		t.Fatalf("final metrics: %v", fm)
	}
	ex := asMap(asMap(tr["extra"])["sleipnir"])
	if ex["episode"] != "swarm-1/0" || ex["reward"].(float64) != 0.9 {
		t.Fatalf("extra: %v", ex)
	}
	// Marshals back through encoding/json without loss of structure.
	var again any
	if err := json.Unmarshal([]byte(lines(out)[0]), &again); err != nil {
		t.Fatal(err)
	}
}

func TestSchemasAndStatsShape(t *testing.T) {
	src := swarmSource(t, true)
	for f, schema := range map[export.Format]string{
		export.FormatSteps: rl.SchemaStep, export.FormatKTO: export.SchemaKTO, export.FormatSFT: export.SchemaSFT,
		export.FormatTokens: export.SchemaTokens, export.FormatGroups: export.SchemaGroup,
	} {
		out, _ := run(t, []export.Source{src}, export.Options{Format: f, KeepFlat: true, DropFlagged: true})
		for _, r := range decode(t, out) {
			if r["schema"] != schema {
				t.Errorf("%s: schema %v want %s", f, r["schema"], schema)
			}
		}
	}
}
