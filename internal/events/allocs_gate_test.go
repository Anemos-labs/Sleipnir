//go:build !race

package events_test

import (
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// Every request, response, tool call and result of every agent is an event: what emitting one allocates is held to what it is, with a
// little to spare (measured: 7). The number does not depend on the machine; BenchmarkEmit's time does.
func TestEmitAllocationsAreHeld(t *testing.T) {
	l, err := events.Open(t.TempDir(), "gate")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	p := payload{Req: "be-1.12", Model: "deepseek/deepseek-v4-flash", HitRatio: 0.93, Expected: 21000, TotalMs: 8400, Stop: "tool_use"}
	got := testing.AllocsPerRun(200, func() {
		if _, err := l.Emit("be-1", events.TypeModelResponse, p); err != nil {
			t.Fatal(err)
		}
	})
	if got > 10 {
		t.Errorf("Emit allocates %.0f times, more than the 10 it is held to", got)
	}
}
