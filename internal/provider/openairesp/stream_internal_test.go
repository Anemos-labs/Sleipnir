package openairesp

import (
	"github.com/anemos-labs/sleipnir/internal/provider"
	"testing"
	"time"
)

func TestWarmGateWaitsForGeneration(t *testing.T) {
	for _, generated := range []string{"response.output_text.delta", "response.reasoning_summary_text.delta", "response.function_call_arguments.delta", "response.output_item.done", "response.completed"} {
		t.Run(generated, func(t *testing.T) {
			a := newAccumulator(provider.StreamLimits{})
			starts := 0
			on := func(e provider.Event) {
				if e.Kind == provider.EvStart {
					starts++
				}
			}
			begin := time.Now()
			for _, kind := range []string{"response.created", "response.queued", "response.in_progress", "response.output_item.added"} {
				if err := a.feed(&event{Type: kind}, begin, on); err != nil {
					t.Fatal(err)
				}
				if starts != 0 {
					t.Fatalf("%s released followers before generation", kind)
				}
			}
			if err := a.feed(&event{Type: generated}, begin, on); err != nil {
				t.Fatal(err)
			}
			if starts != 1 {
				t.Fatalf("first generation emitted %d starts", starts)
			}
			if err := a.feed(&event{Type: "response.completed"}, begin, on); err != nil {
				t.Fatal(err)
			}
			if starts != 1 {
				t.Fatal("generation released the gate more than once")
			}
		})
	}
}
