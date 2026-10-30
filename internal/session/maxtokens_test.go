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
