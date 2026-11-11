package session

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
)

// A model with a large known window gets room to reason before it writes; a small, guessed or unknown window keeps 16000,
// and the model's own maximum, when one is reported, always wins.
func TestDefaultMaxTokens(t *testing.T) {
	for _, c := range []struct {
		name string
		m    cost.Model
		want int
	}{
		{"a 1M-token window", cost.Model{ContextTokens: 1_048_576, MaxOutput: 65536}, 32000},
		{"a 128k window", cost.Model{ContextTokens: 128_000, MaxOutput: 65536}, 32000},
		{"a 64k window", cost.Model{ContextTokens: 65536, MaxOutput: 65536}, 16000},
		{"a guessed window", cost.Model{ContextTokens: 1_000_000, WindowGuessed: true, MaxOutput: 65536}, 16000},
		{"the model's maximum is smaller", cost.Model{ContextTokens: 1_000_000, MaxOutput: 8192}, 8192},
		{"no maximum reported, a 1M-token window", cost.Model{ContextTokens: 1_000_000}, 32000},
		{"no maximum reported, a guessed window", cost.Model{ContextTokens: 200_000, WindowGuessed: true}, 16000},
	} {
		if got := defaultMaxTokens(c.m); got != c.want {
			t.Errorf("%s: defaultMaxTokens = %d, want %d", c.name, got, c.want)
		}
	}
}

// A team member follows its own model's default only while the session names no limit.
func TestMemberMaxTokens(t *testing.T) {
	if f := memberMaxTokens(core.Params{MaxTokens: 4096}); f != nil {
		t.Fatal("a session's own limit was replaced for its members")
	}
	f := memberMaxTokens(core.Params{})
	if f == nil || f(cost.Model{ContextTokens: 1_000_000, MaxOutput: 20000}) != 20000 || f(cost.Model{ContextTokens: 64000, MaxOutput: 65536}) != 16000 {
		t.Fatal("a member's default does not follow its model")
	}
}

// A response cut off at the limit raises it to the model's reported maximum, up to 64000 tokens and a quarter of a known window;
// a model whose maximum or window nothing reports is never raised.
func TestMaxTokensCeiling(t *testing.T) {
	for _, c := range []struct {
		name string
		m    cost.Model
		want int
	}{
		{"a 1M-token window, 65536 out", cost.Model{ContextTokens: 1_048_576, MaxOutput: 65536}, 64000},
		{"a 1M-token window, 131072 out", cost.Model{ContextTokens: 1_048_576, MaxOutput: 131072}, 64000},
		{"a 128k window", cost.Model{ContextTokens: 131_072, MaxOutput: 32768}, 32768},
		{"a 64k window", cost.Model{ContextTokens: 65536, MaxOutput: 65536}, 16384},
		{"a guessed window", cost.Model{ContextTokens: 200_000, WindowGuessed: true, MaxOutput: 16000}, 0},
		{"no maximum reported", cost.Model{ContextTokens: 1_000_000}, 0},
	} {
		if got := maxTokensCeiling(c.m); got != c.want {
			t.Errorf("%s: maxTokensCeiling = %d, want %d", c.name, got, c.want)
		}
	}
	if memberMaxTokensCeiling(core.Params{MaxTokens: 4096}) != nil {
		t.Error("a limit the session names must never be raised")
	}
}

// A judge reasons over the evidence before its verdict: 12000 tokens, or its model's own limit if smaller.
func TestJudgeOutputTokens(t *testing.T) {
	for _, c := range []struct {
		name string
		m    cost.Model
		want int
	}{
		{"a 1M-token window", cost.Model{ContextTokens: 1_048_576, MaxOutput: 65536}, 12000},
		{"the model's maximum is smaller", cost.Model{ContextTokens: 1_000_000, MaxOutput: 8192}, 8192},
		{"a guessed window", cost.Model{ContextTokens: 32768, WindowGuessed: true, MaxOutput: 65536}, 12000},
	} {
		if got := judgeOutputTokens(c.m); got != c.want {
			t.Errorf("%s: judgeOutputTokens = %d, want %d", c.name, got, c.want)
		}
	}
}

// The judge's limit gives way to the evidence, down to the 4000 tokens it had: whatever fitted then still reaches the judge.
func TestJudgeRoomKeepsTheEvidenceRoomItHad(t *testing.T) {
	for _, c := range []struct {
		name         string
		limit, in, w int
		guessed      bool
		want         int
		ok           bool
	}{
		{"room to spare", 12000, 10000, 1_000_000, false, 12000, true},
		{"a 32k window and 17000 tokens of evidence", 12000, 17000, 32768, false, 7576, true},
		{"a guessed 32k window does not lower the limit", 12000, 17000, 32768, true, 12000, true},
		{"a guessed window still says when the evidence is too much", 12000, 21000, 32768, true, 0, false},
		{"evidence that fitted beside 4000 tokens", 12000, 20500, 32768, false, 4076, true},
		{"evidence that did not", 12000, 21000, 32768, false, 0, false},
		{"a model limit under the floor", 2000, 22000, 32768, false, 2000, true},
	} {
		got, ok := judgeRoom(c.limit, c.in, c.w, c.guessed)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: judgeRoom = %d, %v, want %d, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}
