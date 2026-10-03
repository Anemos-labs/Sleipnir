package agent_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
)

func TestEffortFallbackPreservesConversationAndLearnsForLaterRequests(t *testing.T) {
	var effort provider.EffortSetting
	effort.Set("max")
	var sent []string
	r := newRig(t, rigOpts{effort: &effort}, func(c *mock.Call) mock.Reply {
		var level string
		_ = json.Unmarshal(c.Raw["reasoning_effort"], &level)
		sent = append(sent, level)
		if level == "max" {
			return mock.Reply{Fault: &mock.Fault{Status: 400, Message: "Invalid reasoning_effort. Supported values: 'low', 'medium', 'high'."}}
		}
		if c.N == 3 && len(c.Messages) < 4 {
			t.Error("the follow-up lost the conversation")
		}
		return mock.Reply{Text: "finished"}
	})
	for _, prompt := range []string{"first task", "follow-up"} {
		if _, err := r.agent.Run(context.Background(), prompt); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(sent, []string{"max", "high", "high"}) {
		t.Fatalf("effort requests: %v", sent)
	}
}

func TestEffortChangesApplyAfterTheInflightRequest(t *testing.T) {
	var effort provider.EffortSetting
	effort.Set("high")
	var sent []string
	r := newRig(t, rigOpts{effort: &effort}, func(c *mock.Call) mock.Reply {
		var level string
		_ = json.Unmarshal(c.Raw["reasoning_effort"], &level)
		sent = append(sent, level)
		if c.N == 1 {
			effort.Set("low")
			return mock.Reply{ToolCalls: []mock.ToolCall{{ID: "echo-1", Name: "echo", Args: `{}`}}}
		}
		return mock.Reply{Text: "finished"}
	})
	if _, err := r.agent.Run(context.Background(), "use echo, then finish"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sent, []string{"high", "low"}) {
		t.Fatalf("effort requests: %v", sent)
	}
	if len(r.log.OfType(events.TypeLayerCommit)) == 0 {
		t.Fatal("effort change did not declare its cache rebase")
	}
	for _, event := range r.log.OfType(events.TypeCacheAnomaly) {
		if strings.Contains(string(event.Data), `"kind":"drift"`) {
			t.Fatalf("declared effort change reported as silent drift: %s", event.Data)
		}
	}
}

func TestEffortUnsupportedParameterFallsBackOnceToDefault(t *testing.T) {
	var effort provider.EffortSetting
	effort.Set("high")
	var sent []string
	r := newRig(t, rigOpts{effort: &effort}, func(c *mock.Call) mock.Reply {
		var level string
		_ = json.Unmarshal(c.Raw["reasoning_effort"], &level)
		sent = append(sent, level)
		if level != "" {
			return mock.Reply{Fault: &mock.Fault{Status: 400, Message: "Unknown parameter: reasoning_effort"}}
		}
		return mock.Reply{Text: "finished"}
	})
	if _, err := r.agent.Run(context.Background(), "finish"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sent, []string{"high", ""}) {
		t.Fatalf("effort requests: %v", sent)
	}
}
