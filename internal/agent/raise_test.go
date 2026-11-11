package agent_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
)

// maxTokensOf is the output limit a request asked for.
func maxTokensOf(t *testing.T, c *mock.Call) int {
	t.Helper()
	var n int
	if err := json.Unmarshal(c.Raw["max_tokens"], &n); err != nil {
		t.Fatalf("max_tokens %s: %v", c.Raw["max_tokens"], err)
	}
	return n
}

// A response cut off at the output limit raises the limit of the agent's later requests to its ceiling: a reasoning model that
// thought past the limit on a hard step, asked to carry on at the same limit, reasoned again from the top and was cut off again.
func TestAResponseCutOffAtTheLimitRaisesItForTheNextRequests(t *testing.T) {
	var mu sync.Mutex
	var limits []int
	r := newRig(t, rigOpts{noCompact: true, ceiling: 2048}, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		limits = append(limits, maxTokensOf(t, c))
		switch len(limits) {
		case 1:
			return mock.Reply{Text: "thinking, and thinking", Finish: "length"}
		case 2:
			return mock.Reply{Text: "done."}
		}
		return mock.Reply{Text: "again."}
	})
	if _, err := r.agent.Run(context.Background(), "solve it"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.agent.Run(context.Background(), "and the next one"); err != nil {
		t.Fatal(err)
	}
	if len(limits) != 3 || limits[0] != 512 || limits[1] != 2048 || limits[2] != 2048 {
		t.Fatalf("limits asked %v, want [512 2048 2048]: the first at the configured limit, then the ceiling for the rest of the agent's life", limits)
	}
}

// Without a ceiling above the limit (a model whose maximum is not known, or a limit the person set), the limit stays.
func TestWithoutACeilingTheLimitIsNotRaised(t *testing.T) {
	var mu sync.Mutex
	var limits []int
	r := newRig(t, rigOpts{noCompact: true}, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		limits = append(limits, maxTokensOf(t, c))
		if len(limits) == 1 {
			return mock.Reply{Text: "cut", Finish: "length"}
		}
		return mock.Reply{Text: "done."}
	})
	if _, err := r.agent.Run(context.Background(), "solve it"); err != nil {
		t.Fatal(err)
	}
	if len(limits) != 2 || limits[1] != 512 {
		t.Fatalf("limits asked %v, want the configured 512 throughout", limits)
	}
}

// A raised limit is lowered to the room the latest prompt leaves in the window: the prompt and the limit must still fit in it.
func TestARaisedLimitLeavesThePromptItsRoom(t *testing.T) {
	m, _ := cost.Defaults().Lookup("mock-1")
	m.Cache = cost.OpenAICacheModel()
	m.ContextTokens = 4000
	var mu sync.Mutex
	var limits []int
	r := newRig(t, rigOpts{noCompact: true, ceiling: 1 << 20, model: &m}, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		limits = append(limits, maxTokensOf(t, c))
		if len(limits) == 1 {
			return mock.Reply{Text: "cut", Finish: "length"}
		}
		return mock.Reply{Text: "done."}
	})
	if _, err := r.agent.Run(context.Background(), "solve it"); err != nil {
		t.Fatal(err)
	}
	last := r.agent.PromptTokens() // the provider's count of the second request
	if len(limits) != 2 || limits[1] <= 512 || limits[1]+last > m.ContextTokens {
		t.Fatalf("limits asked %v with a second prompt of %d tokens: want a raised limit that still fits the %d-token window", limits, last, m.ContextTokens)
	}
}

// The room is that of the prompt about to be sent, not the last one: a large tool result between the two takes its share.
func TestARaisedLimitLeavesRoomForWhatTheLastToolAdded(t *testing.T) {
	m, _ := cost.Defaults().Lookup("mock-1")
	m.Cache = cost.OpenAICacheModel()
	m.ContextTokens = 6000
	var mu sync.Mutex
	var limits []int
	r := newRig(t, rigOpts{noCompact: true, ceiling: 1 << 20, model: &m}, func(c *mock.Call) mock.Reply {
		mu.Lock()
		defer mu.Unlock()
		limits = append(limits, maxTokensOf(t, c))
		switch len(limits) {
		case 1:
			return mock.Reply{Text: "cut", Finish: "length"}
		case 2:
			return mock.Reply{Text: "building", ToolCalls: []mock.ToolCall{{ID: "call_1", Name: "big", Args: `{}`}}}
		}
		return mock.Reply{Text: "done."}
	})
	if _, err := r.agent.Run(context.Background(), "solve it"); err != nil {
		t.Fatal(err)
	}
	last := r.agent.PromptTokens() // the provider's count of the request sent after the tool result
	if len(limits) != 3 || limits[2]+last > m.ContextTokens {
		t.Fatalf("limits asked %v with a last prompt of %d tokens: the third request does not fit its %d-token window", limits, last, m.ContextTokens)
	}
}
