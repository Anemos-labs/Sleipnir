package export_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/export"
)

func allOpts(f export.Format) export.Options {
	return export.Options{Format: f, KeepFlat: true, KeepWeak: true, DropFlagged: true}
}

// TestEndToEndStepsMatchTheWire is the fidelity test: fixture -> traj -> export
// steps -> parse the JSONL again, and check that every prompt is exactly what
// openaichat.Build renders from the resolved prompt (the bytes the endpoint got),
// up to the documented conversion of tool-call arguments to JSON objects.
func TestEndToEndStepsMatchTheWire(t *testing.T) {
	src, run := fixture(t)
	out, st := run2(t, []export.Source{src}, allOpts(export.FormatSteps))
	recs := decode(t, out)
	if len(recs) != 27 || st.Records != 27 || st.Kept != 1 {
		t.Fatalf("records %d stats %+v", len(recs), st)
	}
	_ = run
	byID := map[string]*rl.Step{}
	for ai := range src.Episode.Agents {
		for si := range src.Episode.Agents[ai].Steps {
			s := &src.Episode.Agents[ai].Steps[si]
			byID[src.Episode.ID+"#"+s.ID] = s
		}
	}
	for i, rec := range recs {
		id := rec["id"].(string)
		step := byID[id]
		if step == nil {
			t.Fatalf("record %d: unknown id %q", i, id)
		}
		if rec["schema"] != rl.SchemaStep || rec["task_id"] != "fixture" || rec["group_id"] != "fixture@mock-1" || rec["agent"] != "be-1" || rec["role"] != step.Role {
			t.Fatalf("%s: header %v", id, rec)
		}
		// The prompt equals Build(resolved prompt).
		p, err := src.Prompts.Prompt(src.Episode, step)
		if err != nil {
			t.Fatal(err)
		}
		body, err := openaichat.Build(p, openaichat.Options{}, false)
		if err != nil {
			t.Fatal(err)
		}
		var sent struct {
			Messages []json.RawMessage `json:"messages"`
			Tools    json.RawMessage   `json:"tools"`
		}
		if err := json.Unmarshal(body, &sent); err != nil {
			t.Fatal(err)
		}
		prompt := rec["prompt"].([]any)
		if len(prompt) != len(sent.Messages) {
			t.Fatalf("%s: %d prompt messages, endpoint received %d", id, len(prompt), len(sent.Messages))
		}
		for k := range prompt {
			if !wireEqual(t, prompt[k], sent.Messages[k]) {
				t.Fatalf("%s: message %d differs from what the endpoint received:\n%v\n%s", id, k, prompt[k], sent.Messages[k])
			}
		}
		var tools, sentTools any
		_ = json.Unmarshal(sent.Tools, &sentTools)
		tools = rec["tools"]
		jt, _ := json.Marshal(tools)
		js, _ := json.Marshal(sentTools)
		if string(jt) != string(js) {
			t.Fatalf("%s: tools differ:\n%s\n%s", id, jt, js)
		}
		if prompt[0].(map[string]any)["role"] != "system" {
			t.Fatalf("%s: the system prompt is the first wire message", id)
		}
		// The completion is one assistant message equal to the recorded turn.
		comp := rec["completion"].([]any)
		if len(comp) != 1 {
			t.Fatalf("%s: completion has %d messages", id, len(comp))
		}
		cm := comp[0].(map[string]any)
		if cm["role"] != "assistant" {
			t.Fatalf("%s: completion role %v", id, cm["role"])
		}
		turn := step.Completion.Turn
		var wantText strings.Builder
		for _, b := range turn.Blocks {
			if b.Kind == core.BlockText {
				wantText.WriteString(b.Text)
			}
		}
		if wantText.Len() > 0 && cm["content"] != wantText.String() {
			t.Fatalf("%s: completion content %v want %q", id, cm["content"], wantText.String())
		}
		if uses := turn.ToolCalls(); len(uses) > 0 {
			calls := cm["tool_calls"].([]any)
			if len(calls) != len(uses) {
				t.Fatalf("%s: %d tool calls, want %d", id, len(calls), len(uses))
			}
			fn := calls[0].(map[string]any)["function"].(map[string]any)
			args, isObject := fn["arguments"].(map[string]any)
			if !isObject || fn["name"] != uses[0].ToolName {
				t.Fatalf("%s: arguments must be a JSON object in the steps format: %v", id, fn)
			}
			var want map[string]any
			_ = json.Unmarshal(uses[0].Input, &want)
			ja, _ := json.Marshal(args)
			jw, _ := json.Marshal(want)
			if string(ja) != string(jw) {
				t.Fatalf("%s: arguments %s want %s", id, ja, jw)
			}
		}
		meta := rec["meta"].(map[string]any)
		if meta["wire_hash"] != string(step.Prompt.WireHash) || meta["req"] != step.ID || meta["shared_prefix"] != step.Prompt.SharedPrefix {
			t.Fatalf("%s: meta %v", id, meta)
		}
		if int(rec["segment"].(float64)) != step.Segment {
			t.Fatalf("%s: segment %v want %d", id, rec["segment"], step.Segment)
		}
	}
}

// run2 is run() for the e2e tests, kept separate so helpers stay unexported.
func run2(t *testing.T, srcs []export.Source, o export.Options) (string, export.Stats) {
	t.Helper()
	return run(t, srcs, o)
}

func TestStepsRecordFields(t *testing.T) {
	src := swarmSource(t, false)
	out, st := run(t, []export.Source{src}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true, Roles: []string{"backend", "compactor"}})
	recs := decode(t, out)
	if st.ByRole["backend"] != 8 || st.ByRole["compactor"] != 1 || len(recs) != 9 {
		t.Fatalf("roles: %+v (%d records)", st.ByRole, len(recs))
	}
	if st.Drops["step:role_filtered"] != 6 { // mgr.1-4 and rv-1.1-2
		t.Fatalf("drops: %v", st.Drops)
	}
	// Without the role filter the reviewer's teacher-model steps are not trainable.
	_, all := run(t, []export.Source{src}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true})
	if all.Drops["step:not_trainable"] != 2 || all.Records != 13 || all.ByRole["manager"] != 4 {
		t.Fatalf("stats without a role filter: %+v", all)
	}
	r := recs[0]
	if r["reward"].(float64) != 0.95 {
		t.Fatalf("a worker step trains against the worker's reward: %v", r["reward"])
	}
	comps := r["reward_components"].(map[string]any)
	if comps["evidence"].(float64) != 0.05 {
		t.Fatalf("components: %v", comps)
	}
	if _, has := r["advantage"]; has {
		t.Fatalf("without an advantage hook the field must be absent, not zero: %v", r["advantage"])
	}
	meta := r["meta"].(map[string]any)
	pol := meta["policy"].(map[string]any)
	if pol["model"] != "policy-1" || pol["checkpoint"] != "ckpt-7" || meta["harness"].(map[string]any)["version"] != "0.1.0" {
		t.Fatalf("meta: %v", meta)
	}
	if meta["trainable"] != true || meta["kind"] != "main" || meta["model"] != "policy-1" {
		t.Fatalf("meta: %v", meta)
	}
	// The compactor record.
	var fork map[string]any
	for _, rec := range recs {
		if rec["role"] == "compactor" {
			fork = rec
		}
	}
	if fork == nil || fork["id"] != "swarm-1/0#be-1.c1" || fork["segment"].(float64) != 0 {
		t.Fatalf("compactor record: %v", fork)
	}
	// The fork's prompt ends with the compactor instruction the model saw.
	prompt := fork["prompt"].([]any)
	last := prompt[len(prompt)-1].(map[string]any)
	txt, _ := json.Marshal(last["content"])
	if !strings.Contains(string(txt), "compactor-task") {
		t.Fatalf("the fork's exact prompt must include its instruction: %s", txt)
	}
}

func TestReasoningOptionAppliesToCompletionsOnly(t *testing.T) {
	// A thinking block with provider-native reasoning_details.
	src := rolloutWithThinking(t)
	for _, mode := range []string{"drop", "field", "keep"} {
		out, _ := run(t, []export.Source{src}, export.Options{Format: export.FormatSteps, KeepFlat: true, KeepWeak: true, DropFlagged: true, Reasoning: mode})
		recs := decode(t, out)
		first := recs[0]["completion"].([]any)[0].(map[string]any)
		switch mode {
		case "drop":
			if _, has := first["reasoning_details"]; has || first["reasoning_content"] != nil {
				t.Fatalf("drop: %v", first)
			}
		case "field":
			if first["reasoning_content"] != "thinking hard" || first["reasoning_details"] != nil {
				t.Fatalf("field: %v", first)
			}
		case "keep":
			if first["reasoning_details"] == nil {
				t.Fatalf("keep: %v", first)
			}
		}
		// The next prompt replays the assistant turn as the wire does, whatever the
		// option: the prompt is what the endpoint received.
		second := recs[1]["prompt"].([]any)
		var replay map[string]any
		for _, m := range second {
			if mm := m.(map[string]any); mm["role"] == "assistant" {
				replay = mm
			}
		}
		if replay == nil || replay["reasoning_details"] == nil {
			t.Fatalf("%s: prompts keep the provider-native reasoning the wire replays: %v", mode, replay)
		}
	}
	if _, err := export.Export(&strings.Builder{}, []export.Source{src}, export.Options{Format: export.FormatSteps, Reasoning: "bogus"}); err == nil {
		t.Fatal("unknown reasoning mode must be rejected")
	}
}

func TestOptionValidation(t *testing.T) {
	src, _ := fixture(t)
	for name, o := range map[string]export.Options{
		"no format":       {},
		"unknown format":  {Format: "parquet"},
		"bad split":       {Format: export.FormatSteps, Split: "train"},
		"zero weight":     {Format: export.FormatSteps, Split: "a:0,b:1"},
		"negative topk":   {Format: export.FormatSFT, TopK: -1},
		"negative cap":    {Format: export.FormatSFT, MaxSamples: -1},
		"duplicate split": {Format: export.FormatSteps, Split: "a:1,a:1"},
	} {
		var sb strings.Builder
		if _, err := export.Export(&sb, []export.Source{src}, o); err == nil || !strings.Contains(err.Error(), "bad options") {
			t.Errorf("%s: want an options error, got %v", name, err)
		}
		if sb.Len() != 0 {
			t.Errorf("%s: nothing may be written on a rejected option", name)
		}
	}
	// A nil episode and an empty list are harmless.
	out, st := run(t, []export.Source{{}}, export.Options{Format: export.FormatSteps})
	if out != "" || st.Episodes != 0 {
		t.Fatalf("out %q stats %+v", out, st)
	}
	out, st = run(t, nil, export.Options{Format: export.FormatCanonical, Inline: true})
	if out != "" || st.Records != 0 {
		t.Fatalf("out %q stats %+v", out, st)
	}
}

func TestDeterministicAcrossRunsAndSourceOrder(t *testing.T) {
	srcs := group(t, 4)
	rev := []export.Source{srcs[3], srcs[1], srcs[0], srcs[2]}
	for _, f := range export.Formats() {
		o := export.Options{Format: f, KeepFlat: true, KeepWeak: true, DropFlagged: true, PackSegments: true, Inline: true}
		a, _ := run(t, srcs, o)
		b, _ := run(t, srcs, o)
		c, _ := run(t, rev, o)
		if a != b {
			t.Errorf("%s: two runs differ", f)
		}
		if a != c {
			t.Errorf("%s: the order of the sources must not matter", f)
		}
		if a == "" {
			t.Errorf("%s: no output", f)
		}
	}
}

func TestStatsAreSortedJSON(t *testing.T) {
	src := swarmSource(t, false)
	_, st := run(t, []export.Source{src}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true, Roles: []string{"worker"}})
	b1, _ := json.Marshal(st)
	_, st2 := run(t, []export.Source{src}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true, Roles: []string{"worker"}})
	b2, _ := json.Marshal(st2)
	if string(b1) != string(b2) || !strings.Contains(string(b1), `"format":"steps"`) {
		t.Fatalf("stats must be deterministic:\n%s\n%s", b1, b2)
	}
	if st.ByRole["backend"] == 0 || st.ByRole["manager"] != 0 || st.ByRole["compactor"] != 0 || st.ByRole["reviewer"] != 0 {
		t.Fatalf("\"worker\" aliases every role that is not manager, reviewer, compactor or mailman: %v", st.ByRole)
	}
	_ = fmt.Sprint()
}
