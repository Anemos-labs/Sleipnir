package openaichat

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

func fp(v float64) *float64 { return &v }
func tp(v int) *tokens      { t := tokens(v); return &t }

func TestUsageNormalizeClampsEveryCounter(t *testing.T) {
	for _, tc := range []struct {
		name string
		u    usage
		want core.Usage
	}{
		{"ordinary", usage{PromptTokens: 1000, CompletionTokens: 200}, core.Usage{InputTokens: 1000, OutputTokens: 200}},
		{"cached tokens come out of the input", usage{PromptTokens: 1000, CompletionTokens: 5, PromptTokensDetails: &struct {
			CachedTokens     tokens `json:"cached_tokens"`
			CacheWriteTokens tokens `json:"cache_write_tokens"`
		}{CachedTokens: 600, CacheWriteTokens: 100}}, core.Usage{InputTokens: 300, CacheReadTokens: 600, CacheWrite5mTokens: 100, OutputTokens: 5}},
		{"DeepSeek native counters", usage{PromptTokens: 500, PromptCacheHitTokens: tp(400)}, core.Usage{InputTokens: 100, CacheReadTokens: 400}},
		{"negative completion tokens", usage{PromptTokens: 100, CompletionTokens: -5_000_000}, core.Usage{InputTokens: 100}},
		{"negative prompt tokens", usage{PromptTokens: -100, CompletionTokens: 7}, core.Usage{OutputTokens: 7}},
		{"absurd prompt tokens are capped", usage{PromptTokens: math.MaxInt64, CompletionTokens: math.MaxInt64},
			core.Usage{InputTokens: provider.MaxUsageTokens, OutputTokens: provider.MaxUsageTokens}},
		{"cached beyond the prompt does not go negative", usage{PromptTokens: 100, PromptCacheHitTokens: tp(900)}, core.Usage{CacheReadTokens: 900}},
		{"negative cached", usage{PromptTokens: 100, PromptTokensDetails: &struct {
			CachedTokens     tokens `json:"cached_tokens"`
			CacheWriteTokens tokens `json:"cache_write_tokens"`
		}{CachedTokens: -50, CacheWriteTokens: -1}}, core.Usage{InputTokens: 100}},
		{"reasoning is a share of the output", usage{CompletionTokens: 10, CompletionTokensDetails: &struct {
			ReasoningTokens tokens `json:"reasoning_tokens"`
		}{ReasoningTokens: 9_999_999}}, core.Usage{OutputTokens: 10, ReasoningTokens: 10}},
		{"negative reasoning", usage{CompletionTokens: 10, CompletionTokensDetails: &struct {
			ReasoningTokens tokens `json:"reasoning_tokens"`
		}{ReasoningTokens: -3}}, core.Usage{OutputTokens: 10}},
		{"nothing reported", usage{}, core.Usage{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := tc.u
			got := u.normalize()
			if got != tc.want {
				t.Fatalf("normalize = %+v, want %+v", got, tc.want)
			}
			for _, n := range []int{got.InputTokens, got.CacheReadTokens, got.CacheWrite5mTokens, got.CacheWrite1hTokens, got.OutputTokens, got.ReasoningTokens} {
				if n < 0 || n > provider.MaxUsageTokens {
					t.Fatalf("counter %d outside 0..%d", n, provider.MaxUsageTokens)
				}
			}
		})
	}
	var none *usage
	if got := none.normalize(); got != (core.Usage{}) {
		t.Fatalf("nil usage: %+v", got)
	}
}

// Summing what normalize returns over many responses can neither overflow nor go negative.
func TestNormalizedUsageCannotOverflowAnAccumulator(t *testing.T) {
	u := usage{PromptTokens: math.MaxInt64, CompletionTokens: math.MaxInt64}
	total := core.Usage{}
	for i := 0; i < 1_000_000; i++ {
		total = total.Add(u.normalize())
	}
	if total.InputTokens < 0 || total.OutputTokens < 0 {
		t.Fatalf("the accumulator wrapped: %+v", total)
	}
}

func TestUsageNormalizeDropsAnUnusableCost(t *testing.T) {
	for name, tc := range map[string]struct {
		in   *float64
		keep bool
	}{
		"typical":       {fp(0.0123), true},
		"free":          {fp(0), true},
		"the ceiling":   {fp(provider.MaxRequestCostUSD), true},
		"negative":      {fp(-1e6), false},
		"tiny negative": {fp(-1e-9), false},
		"NaN":           {fp(math.NaN()), false},
		"+Inf":          {fp(math.Inf(1)), false},
		"-Inf":          {fp(math.Inf(-1)), false},
		"absurd":        {fp(1e12), false},
		"absent":        {nil, false},
	} {
		u := usage{PromptTokens: 100, CompletionTokens: 10, Cost: tc.in}
		u.normalize()
		if (u.Cost != nil) != tc.keep {
			t.Errorf("%s: Cost after normalize = %v, keep = %v", name, u.Cost, tc.keep)
		}
		if u.Cost != nil && (math.IsNaN(*u.Cost) || *u.Cost < 0) {
			t.Errorf("%s: an unusable cost survived: %v", name, *u.Cost)
		}
	}
}

// ---- through the wire ---------------------------------------------------------------------------

func TestHostileUsageFrameDoesNotDropTheReport(t *testing.T) {
	// Every one of these members would make a plain struct decode fail, and a frame that
	// fails to decode is a frame dropped: usage 0, cost 0, budget never trips.
	hostile := `{"id":"g","choices":[],"usage":{"prompt_tokens":1e30,"completion_tokens":"lots","total_tokens":-5,` +
		`"prompt_tokens_details":{"cached_tokens":-7,"cache_write_tokens":"x"},"completion_tokens_details":"weird","cost":1e999}}`
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sse(w)
		fmt.Fprint(w, frame("answer"), rawFrame(hostile), "data: [DONE]\n\n")
	})
	resp, err := call(t, s.URL, Config{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Turn.PlainText() != "answer" {
		t.Fatalf("the content was lost with the frame: %q", resp.Turn.PlainText())
	}
	if resp.Usage.InputTokens != provider.MaxUsageTokens || resp.Usage.OutputTokens != 0 || resp.Usage.CacheReadTokens != 0 {
		t.Fatalf("the absurd prompt counter must be capped (fail closed), the rest read as zero: %+v", resp.Usage)
	}
	if resp.CostUSD != nil {
		t.Fatalf("an unreadable cost must fall back to the computed price, got %v", *resp.CostUSD)
	}
	// The audit record keeps what the server actually sent.
	if !strings.Contains(string(resp.RawUsage), "1e30") || !strings.Contains(string(resp.RawUsage), "1e999") {
		t.Fatalf("RawUsage must be the verbatim object: %s", resp.RawUsage)
	}
}

func TestNegativeGatewayCostFallsBackToTheComputedPrice(t *testing.T) {
	for name, cost := range map[string]string{"negative": "-1e6", "absurd": "1e9", "string": `"cheap"`, "null": "null"} {
		t.Run(name, func(t *testing.T) {
			s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
				sse(w)
				fmt.Fprint(w, frame("x"), rawFrame(`{"id":"g","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":-5000000,"cost":`+cost+`}}`), "data: [DONE]\n\n")
			})
			resp, err := call(t, s.URL, Config{}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.CostUSD != nil {
				t.Fatalf("CostUSD = %v: the agent would add it to its spend as the exact charge", *resp.CostUSD)
			}
			if resp.Usage.OutputTokens != 0 || resp.Usage.InputTokens != 100 {
				t.Fatalf("usage = %+v", resp.Usage)
			}
		})
	}
}

func TestGatewayCostThatMakesSenseIsKept(t *testing.T) {
	for _, stream := range []bool{true, false} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
				usage := `"usage":{"prompt_tokens":100,"completion_tokens":10,"cost":0.0123}`
				if stream {
					sse(w)
					fmt.Fprint(w, frame("x"), rawFrame(`{"id":"g","choices":[],`+usage+`}`), "data: [DONE]\n\n")
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"g","choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}],`+usage+`}`)
			})
			cfg := Config{Name: "t", BaseURL: s.URL}
			resp, err := New(cfg).Do(context.Background(), &provider.Request{Prompt: secRevPrompt("hi"), NoStream: !stream}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.CostUSD == nil || *resp.CostUSD != 0.0123 || resp.Usage.InputTokens != 100 || resp.Usage.OutputTokens != 10 {
				t.Fatalf("cost %v usage %+v", resp.CostUSD, resp.Usage)
			}
			var raw map[string]any
			if err := json.Unmarshal(resp.RawUsage, &raw); err != nil || raw["cost"] != 0.0123 {
				t.Fatalf("RawUsage = %s (%v)", resp.RawUsage, err)
			}
		})
	}
	// A free model reports 0, which is a price, not a missing one.
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		sse(w)
		fmt.Fprint(w, frame("x"), rawFrame(`{"id":"g","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1,"cost":0}}`), "data: [DONE]\n\n")
	})
	resp, err := call(t, s.URL, Config{}, nil)
	if err != nil || resp.CostUSD == nil || *resp.CostUSD != 0 {
		t.Fatalf("a zero cost is a real cost: %v %v", resp, err)
	}
}

func TestNonStreamingHostileUsageIsNormalisedToo(t *testing.T) {
	s := newScripted(t, func(w http.ResponseWriter, fl http.Flusher, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"g","choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":-1,"completion_tokens":1e40,"cost":-3}}`)
	})
	resp, err := New(Config{Name: "t", BaseURL: s.URL}).Do(context.Background(), &provider.Request{Prompt: secRevPrompt("hi"), NoStream: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 0 || resp.Usage.OutputTokens != provider.MaxUsageTokens || resp.CostUSD != nil {
		t.Fatalf("usage %+v cost %v", resp.Usage, resp.CostUSD)
	}
}
