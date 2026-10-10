package translate

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// A checkpoint event that counts its files (count) instead of listing them gives the same
// ckpt event as one that lists them.
func TestCheckpointEventWithACount(t *testing.T) {
	start := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	h := newHarness(t, Config{StartedAt: start})
	data, _ := json.Marshal(map[string]any{"id": "cp_0003", "label": "turn 3", "count": 3000, "file": "gen/f2999.txt", "agents": []string{"main"}, "time": start.Format(time.RFC3339Nano)})
	h.feed(events.Event{Seq: 1, TS: start.Add(time.Second), Type: "checkpoint", Data: data})
	cks := ofKind(h.decoded(), "ckpt")
	if len(cks) != 1 || cks[0]["files"] != float64(3000) || cks[0]["skipped"] == true || cks[0]["cid"] != "c03" {
		t.Fatalf("ckpt events: %v", cks)
	}
}
