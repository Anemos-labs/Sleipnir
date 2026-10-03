package provider_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/provider/anthropic"
	"github.com/anemos-labs/sleipnir/internal/provider/openairesp"
)

func TestEffortChoosesNearestAcceptedLevel(t *testing.T) {
	for _, tc := range []struct{ model, want, got string }{
		{"deepseek-v4.1-flash", "medium", "low"},
		{"deepseek-v4.1-flash", "xhigh", "high"},
		{"deepseek-v4.1-flash", "max", "max"},
		{"gpt-6-astra", "off", "low"},
		{"gpt-6.1-sol", "max", "max"},
		{"gpt-6-luna", "off", "none"},
		{"unknown-model", "ultra", "max"},
		{"unknown-model", "typo", ""},
	} {
		t.Run(tc.model+"/"+tc.want, func(t *testing.T) {
			var e provider.EffortSetting
			e.Set(tc.want)
			p := openairesp.New(openairesp.Config{})
			got := e.Params(p, tc.model, core.Params{MaxTokens: 123})
			if got.Effort != tc.got || got.MaxTokens != 123 {
				t.Fatalf("mapped params = %+v, want effort %q", got, tc.got)
			}
		})
	}
	var e provider.EffortSetting
	e.Set("max")
	p := anthropic.New(anthropic.Config{})
	if got := e.Params(p, "claude-opus-4-5", core.Params{}).Effort; got != "high" {
		t.Fatalf("bounded Claude effort = %q", got)
	}
	if got := e.Params(p, "claude-haiku-4-5", core.Params{}).Effort; got != "" {
		t.Fatalf("model without effort got %q", got)
	}
}

func TestEffortLearnsEndpointConstraintsWithoutChangingOtherRoutes(t *testing.T) {
	var e provider.EffortSetting
	e.Set("max")
	p := openairesp.New(openairesp.Config{BaseURL: "http://localhost:1111"})
	other := openairesp.New(openairesp.Config{BaseURL: "http://localhost:2222"})
	err := &provider.Error{Kind: provider.ErrBadRequest, Status: 400, Message: "Unsupported reasoning.effort: 'max'. Supported values: 'low', 'medium', 'high'."}
	if !e.Recover(p, "model", "max", err) || e.Params(p, "model", core.Params{}).Effort != "high" {
		t.Fatal("did not learn the endpoint's accepted range")
	}
	for _, got := range []string{e.Params(other, "model", core.Params{}).Effort, e.Params(p, "other-model", core.Params{}).Effort} {
		if got != "max" {
			t.Fatal("changed a different route")
		}
	}
	err.Message = "Unknown parameter: reasoning.effort"
	if !e.Recover(p, "model", "high", err) || e.Params(p, "model", core.Params{}).Effort != "" {
		t.Fatal("unsupported parameter was not omitted")
	}
	if e.Recover(p, "model", "", err) {
		t.Fatal("must stop retrying when effort was already omitted")
	}
	for _, err := range []error{
		fmt.Errorf("reasoning effort invalid"),
		&provider.Error{Kind: provider.ErrAuth, Status: 401, Message: "invalid effort"},
		&provider.Error{Kind: provider.ErrBadRequest, Status: 400, Message: "tool arguments invalid"},
		&provider.Error{Kind: provider.ErrThinkingBinding, Status: 400, Message: "effort changed thinking binding"},
	} {
		if e.Recover(p, "other-model", "max", err) {
			t.Fatalf("unrelated failure recovered: %v", err)
		}
	}
}

func TestEffortUpdatesAreSafeDuringRequests(t *testing.T) {
	var e provider.EffortSetting
	p := openairesp.New(openairesp.Config{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				e.Set("high")
				params := e.Params(p, "model", core.Params{MaxTokens: 99})
				e.Set("low")
				if params.MaxTokens != 99 || (params.Effort != "high" && params.Effort != "low") {
					t.Errorf("inconsistent request params: %+v", params)
				}
			}
		}()
	}
	wg.Wait()
}
