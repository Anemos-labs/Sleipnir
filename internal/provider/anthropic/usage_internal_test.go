package anthropic

import (
	"math"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider"
)

// wireUsage decodes leniently (see tokens), and normalize clamps again: the second is
// what protects a wireUsage that was built in code, or merged from several reports.
func TestWireUsageNormalizeClampsWhatItIsGiven(t *testing.T) {
	five := func(v int) *struct {
		Ephemeral5m tokens `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h tokens `json:"ephemeral_1h_input_tokens"`
	} {
		return &struct {
			Ephemeral5m tokens `json:"ephemeral_5m_input_tokens"`
			Ephemeral1h tokens `json:"ephemeral_1h_input_tokens"`
		}{Ephemeral5m: tokens(v), Ephemeral1h: tokens(v)}
	}
	for name, tc := range map[string]struct {
		u    wireUsage
		want core.Usage
	}{
		"ordinary":  {wireUsage{InputTokens: 25, CacheReadInputTokens: 100, OutputTokens: 6}, core.Usage{InputTokens: 25, CacheReadTokens: 100, OutputTokens: 6}},
		"negative":  {wireUsage{InputTokens: -1, CacheCreationInputTokens: -2, CacheReadInputTokens: -3, OutputTokens: -5_000_000}, core.Usage{}},
		"absurd":    {wireUsage{InputTokens: math.MaxInt64, CacheReadInputTokens: math.MaxInt64, OutputTokens: math.MaxInt64}, core.Usage{InputTokens: provider.MaxUsageTokens, CacheReadTokens: provider.MaxUsageTokens, OutputTokens: provider.MaxUsageTokens}},
		"write TTL": {wireUsage{CacheCreationInputTokens: 100, CacheCreation: five(30)}, core.Usage{CacheWrite5mTokens: 70, CacheWrite1hTokens: 30}},
		"negative write split": {wireUsage{CacheCreationInputTokens: 100, CacheCreation: five(-5)},
			core.Usage{CacheWrite5mTokens: 100}},
		"absurd write split": {wireUsage{CacheCreation: five(math.MaxInt64)},
			core.Usage{CacheWrite5mTokens: provider.MaxUsageTokens, CacheWrite1hTokens: provider.MaxUsageTokens}},
		"reasoning share is capped": {wireUsage{OutputTokens: 10, OutputTokensDetails: &struct {
			ThinkingTokens tokens `json:"thinking_tokens"`
		}{ThinkingTokens: math.MaxInt64}}, core.Usage{OutputTokens: 10, ReasoningTokens: 10}},
		"negative reasoning": {wireUsage{OutputTokens: 10, OutputTokensDetails: &struct {
			ThinkingTokens tokens `json:"thinking_tokens"`
		}{ThinkingTokens: -4}}, core.Usage{OutputTokens: 10}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.u.normalize(); got != tc.want {
				t.Fatalf("normalize = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestMergedUsageReportsStayClamped(t *testing.T) {
	var u wireUsage
	u.merge(wireUsage{InputTokens: 10, OutputTokens: 1})
	u.merge(wireUsage{InputTokens: -50, OutputTokens: math.MaxInt64}) // a later report cannot lower the input, and cannot overflow the output
	got := u.normalize()
	if got.InputTokens != 10 || got.OutputTokens != provider.MaxUsageTokens {
		t.Fatalf("merged usage = %+v", got)
	}
}
