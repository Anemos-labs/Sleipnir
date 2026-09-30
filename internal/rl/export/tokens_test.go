package export_test

import (
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
	"github.com/reee344/sleipnir/internal/rl/export"
	"github.com/reee344/sleipnir/internal/rl/traj/trajtest"
)

// chainRun records n main steps of one agent with a chain-consistent tokenizer and
// returns the episode, ready to export.
func chainRun(t testing.TB, n int, mutate func(i int, c *trajtest.Call)) export.Source {
	return rollout(t, variant{plan: "chain", reward: 1, pass: true, tokens: true, extra: func(a *trajtest.Agent) {
		for i := 0; i < n-2; i++ {
			c := trajtest.Call{Text: "step", Tool: "read", Input: `{"path":"f.go"}`, Result: "file content " + strings.Repeat("x ", i+1)}
			if mutate != nil {
				mutate(i, &c)
			}
			a.Step(c)
		}
	}})
}

func tokenOpts() export.Options {
	return export.Options{Format: export.FormatTokens, KeepFlat: true, DropFlagged: true, PackSegments: true}
}

func ints(v any) []int {
	var out []int
	for _, x := range v.([]any) {
		out = append(out, int(x.(float64)))
	}
	return out
}

func TestPackedSegmentLayout(t *testing.T) {
	src := chainRun(t, 5, nil) // 5 main steps
	out, st := run(t, []export.Source{src}, tokenOpts())
	recs := decode(t, out)
	if len(recs) != 1 || st.Packed != 1 || st.PackedSteps != 5 || len(st.PackFailed) != 0 || st.ByUnit["segment"] != 1 {
		t.Fatalf("records %d stats %+v", len(recs), st)
	}
	rec := recs[0]
	if rec["packed"] != true || rec["schema"] != export.SchemaTokens || rec["role"] != "backend" || rec["agent"] != "solo" {
		t.Fatalf("header: %v", rec)
	}
	steps := rec["steps"].([]any)
	if len(steps) != 5 || steps[0] != "solo.1" || steps[4] != "solo.5" {
		t.Fatalf("steps: %v", steps)
	}
	prompt, resp, mask := ints(rec["prompt_ids"]), ints(rec["response_ids"]), ints(rec["response_mask"])
	lp := rec["old_logprobs"].([]any)
	if len(resp) != len(mask) || len(resp) != len(lp) {
		t.Fatalf("response %d, mask %d, logprobs %d must align", len(resp), len(mask), len(lp))
	}
	// Reconstruct from the per-step traces: ids = P0 ++ C0 ++ obs0 ++ C1 ... ++ C4.
	var traces []*core.TokenTrace
	for i := range src.Episode.Agents[0].Steps {
		traces = append(traces, src.Episode.Agents[0].Steps[i].Tokens)
	}
	var want, wantMask []int
	var wantLP []float64
	for i, tr := range traces {
		for j, id := range tr.CompletionIDs {
			want = append(want, int(id))
			wantMask = append(wantMask, 1)
			wantLP = append(wantLP, float64(tr.Logprobs[j]))
		}
		if i+1 < len(traces) {
			for _, id := range traces[i+1].PromptIDs[len(tr.PromptIDs)+len(tr.CompletionIDs):] {
				want = append(want, int(id))
				wantMask = append(wantMask, 0)
				wantLP = append(wantLP, 0)
			}
		}
	}
	if len(prompt) != len(traces[0].PromptIDs) || fmtInts(resp) != fmtInts(want) || fmtInts(mask) != fmtInts(wantMask) {
		t.Fatalf("packed layout differs from P0 ++ C0 ++ obs0 ++ ... ++ C4:\n got %v\nwant %v", resp, want)
	}
	for i := range lp {
		if math.Abs(lp[i].(float64)-wantLP[i]) > 1e-6 {
			t.Fatalf("logprob %d: %v want %v (zero where masked)", i, lp[i], wantLP[i])
		}
	}
	// The whole sequence is exactly the last step's prompt followed by its completion.
	last := traces[4]
	full := append(append([]int(nil), prompt...), resp...)
	wantFull := ints32(last.PromptIDs)
	wantFull = append(wantFull, ints32(last.CompletionIDs)...)
	if fmtInts(full) != fmtInts(wantFull) {
		t.Fatal("prompt_ids ++ response_ids must equal the last prompt ++ its completion")
	}
	trained := 0
	for _, m := range mask {
		trained += m
	}
	if st.TrainedTokens != trained || st.ResponseTokens != len(resp) || st.PromptTokens != len(prompt) {
		t.Fatalf("token totals: %+v (trained %d)", st, trained)
	}
	// The same run unpacked: five per-step records, same ids.
	o := tokenOpts()
	o.PackSegments = false
	out, st = run(t, []export.Source{src}, o)
	if len(decode(t, out)) != 5 || st.Packed != 0 {
		t.Fatalf("unpacked: %d records", len(decode(t, out)))
	}
}

func fmtInts(v []int) string { return fmt.Sprint(v) }

func ints32(v []int32) []int {
	out := make([]int, len(v))
	for i, x := range v {
		out[i] = int(x)
	}
	return out
}

func TestPackFailureReasons(t *testing.T) {
	// The mutations edit the episode's traces, so each case builds its own.
	cases := []struct {
		name   string
		mutate func(src export.Source)
		reason string
		// perStep is how many per-step records the fallback must still produce.
		perStep int
	}{
		{"prefix mismatch", func(src export.Source) {
			src.Episode.Agents[0].Steps[1].Tokens = cloneTrace(src.Episode.Agents[0].Steps[1].Tokens, func(tr *core.TokenTrace) { tr.PromptIDs[len(tr.PromptIDs)-3]++ })
		}, "prefix_mismatch", 4},
		{"completion retokenised", func(src export.Source) {
			// The next prompt does not contain the sampled completion ids.
			src.Episode.Agents[0].Steps[2].Tokens = cloneTrace(src.Episode.Agents[0].Steps[2].Tokens, func(tr *core.TokenTrace) {
				n := len(src.Episode.Agents[0].Steps[1].Tokens.PromptIDs)
				tr.PromptIDs[n] = 999999
			})
		}, "prefix_mismatch", 4},
		{"not trainable", func(src export.Source) { src.Episode.Agents[0].Steps[2].Trainable = false }, "not_trainable", 3},
		{"missing trace", func(src export.Source) { src.Episode.Agents[0].Steps[1].Tokens = nil }, "no_trace", 3},
		{"inconsistent trace", func(src export.Source) {
			src.Episode.Agents[0].Steps[1].Tokens = cloneTrace(src.Episode.Agents[0].Steps[1].Tokens, func(tr *core.TokenTrace) { tr.Logprobs = tr.Logprobs[:1] })
		}, "no_trace", 3},
		{"nan logprob", func(src export.Source) {
			src.Episode.Agents[0].Steps[1].Tokens = cloneTrace(src.Episode.Agents[0].Steps[1].Tokens, func(tr *core.TokenTrace) { tr.Logprobs[0] = float32(math.NaN()) })
		}, "no_trace", 3},
		{"no prompt ids", func(src export.Source) {
			src.Episode.Agents[0].Steps[1].Tokens = cloneTrace(src.Episode.Agents[0].Steps[1].Tokens, func(tr *core.TokenTrace) { tr.PromptIDs = nil })
		}, "no_trace", 3},
		{"logprobs on some steps only", func(src export.Source) {
			src.Episode.Agents[0].Steps[1].Tokens = cloneTrace(src.Episode.Agents[0].Steps[1].Tokens, func(tr *core.TokenTrace) { tr.Logprobs = nil })
		}, "logprobs_partial", 4},
		{"tokenizer changed", func(src export.Source) {
			src.Episode.Agents[0].Steps[2].Tokens = cloneTrace(src.Episode.Agents[0].Steps[2].Tokens, func(tr *core.TokenTrace) { tr.Tokenizer = "other" })
		}, "tokenizer_changed", 4},
		{"model version changed", func(src export.Source) {
			src.Episode.Agents[0].Steps[2].Tokens = cloneTrace(src.Episode.Agents[0].Steps[2].Tokens, func(tr *core.TokenTrace) { tr.ModelVersion = "ckpt-2" })
		}, "model_version_changed", 4},
		{"reward varies inside the segment", func(src export.Source) { src.Episode.Agents[0].Steps[3].Reward = 0.25 }, "credit_varies", 4},
		{"advantage varies inside the segment", func(src export.Source) { src.Episode.Agents[0].Steps[3].Advantage = 0.5 }, "credit_varies", 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := chainRun(t, 4, nil)
			c.mutate(src)
			out, st := run(t, []export.Source{src}, tokenOpts())
			if st.Packed != 0 || st.PackFailed[c.reason] != 1 || len(st.PackFailed) != 1 {
				t.Fatalf("PackFailed = %v, want exactly %s (packed %d)", st.PackFailed, c.reason, st.Packed)
			}
			recs := decode(t, out)
			if len(recs) != c.perStep {
				t.Fatalf("fallback produced %d per-step records, want %d", len(recs), c.perStep)
			}
			for _, r := range recs {
				if r["packed"] != false || len(r["steps"].([]any)) != 1 {
					t.Fatalf("fallback records are per step: %v", r["steps"])
				}
				mask := ints(r["response_mask"])
				for _, m := range mask {
					if m != 1 {
						t.Fatal("per-step masks are all ones")
					}
				}
			}
		})
	}
}

func cloneTrace(tr *core.TokenTrace, edit func(*core.TokenTrace)) *core.TokenTrace {
	c := *tr
	c.PromptIDs = append([]int32(nil), tr.PromptIDs...)
	c.CompletionIDs = append([]int32(nil), tr.CompletionIDs...)
	c.Logprobs = append([]float32(nil), tr.Logprobs...)
	edit(&c)
	return &c
}

func TestHotTailSegmentsCannotBePacked(t *testing.T) {
	src := swarmSource(t, true)
	out, st := run(t, []export.Source{src}, tokenOpts())
	recs := decode(t, out)
	// Manager: two segments of two steps, worker segment 0 (three steps), all pack.
	// The worker's second segment carries a hot tail and cannot chain.
	if st.Packed != 3 || st.PackedSteps != 7 || st.PackFailed["prefix_mismatch"] != 1 {
		t.Fatalf("stats: %+v", st)
	}
	var packedSteps, perStep int
	roles := map[string]int{}
	for _, r := range recs {
		roles[r["role"].(string)]++
		if r["packed"] == true {
			packedSteps += len(r["steps"].([]any))
		} else {
			perStep++
		}
	}
	// 7 packed steps in 3 records, then per-step: worker segment 1 (5 steps) and the fork.
	if len(recs) != 3+5+1 || packedSteps != 7 || perStep != 6 {
		t.Fatalf("records %d, packed steps %d, per-step %d", len(recs), packedSteps, perStep)
	}
	if roles["backend"] != 1+5 || roles["manager"] != 2 || roles["compactor"] != 1 || roles["reviewer"] != 0 {
		t.Fatalf("roles %v (the reviewer runs on a teacher and is never a token sample)", roles)
	}
	// The fork's record is its own sample with its own prompt ids.
	for _, r := range recs {
		if r["role"] == "compactor" && (r["packed"] != false || r["steps"].([]any)[0] != "be-1.c1") {
			t.Fatalf("compactor record: %v", r)
		}
	}
	// Rewards: the worker's steps train against the worker's reward, the manager's
	// against the episode's; a compactor call against its agent's.
	for _, r := range recs {
		want := 0.9
		if r["agent"] == "be-1" {
			want = 0.95
		}
		if math.Abs(r["reward"].(float64)-want) > 1e-9 {
			t.Fatalf("%v reward %v want %v", r["id"], r["reward"], want)
		}
	}
}

func TestTokensNeedNoResolverWithoutRedaction(t *testing.T) {
	src := chainRun(t, 3, nil)
	noRes := export.Source{Episode: src.Episode}
	out, _ := run(t, []export.Source{noRes}, tokenOpts())
	if len(decode(t, out)) != 1 {
		t.Fatal("token records come from the traces; prompts are only needed to check redaction")
	}
	_, st := run(t, []export.Source{noRes}, export.Options{Format: export.FormatTokens, KeepFlat: true, DropFlagged: true, PackSegments: true, Redactor: newRedactor()})
	if st.Records != 0 || st.Drops["step:no_prompt"] == 0 {
		t.Fatalf("with a redactor the prompts must be checkable: %+v", st)
	}
}

func TestNoTraceMeansNoTokenRecord(t *testing.T) {
	src := rollout(t, variant{plan: "A", reward: 1, pass: true, tokens: false})
	out, st := run(t, []export.Source{src}, tokenOpts())
	if out != "" || st.Drops["step:no_tokens"] != 2 {
		t.Fatalf("text-only steps are skipped in the token format, never fabricated: %v", st.Drops)
	}
	// The same steps are fine in the text formats.
	if out, _ := run(t, []export.Source{src}, export.Options{Format: export.FormatSteps, KeepFlat: true, DropFlagged: true}); out == "" {
		t.Fatal("steps")
	}
}

func TestFixtureTokensFallBackPerStep(t *testing.T) {
	src, _ := fixture(t)
	out, st := run(t, []export.Source{src}, export.Options{Format: export.FormatTokens, KeepFlat: true, KeepWeak: true, DropFlagged: true, PackSegments: true})
	// The mock's completion ids never appear in the next prompt, so all four
	// multi-step segments fail the strict prefix property; the single-step last
	// segment packs. Every trace still exports per step, plus the four forks.
	if st.Packed != 1 || st.PackFailed["prefix_mismatch"] != 4 || st.Records != 1+(9+3+5+5)+4 {
		t.Fatalf("stats: %+v", st)
	}
	for _, r := range decode(t, out) {
		if r["role"] != "backend" && r["role"] != "compactor" {
			t.Fatalf("role %v", r["role"])
		}
		if len(r["prompt_ids"].([]any)) == 0 || len(r["response_ids"].([]any)) == 0 {
			t.Fatalf("empty ids in %v", r["id"])
		}
	}
	_ = rl.RoleWorker
}
