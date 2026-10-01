package events_test

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/reee344/sleipnir/internal/events"
)

// Every agent emits an event for every request, response, tool call and result, and the log writes each to disk as a JSON line:
// this is the cost the harness adds to a step. A swarm of fifty agents is fifty of these at once.

type payload struct {
	Req       string  `json:"req"`
	Model     string  `json:"model"`
	HitRatio  float64 `json:"hit_ratio"`
	Expected  int     `json:"expected_read"`
	Missed    int     `json:"missed"`
	TotalMs   int64   `json:"total_ms"`
	Stop      string  `json:"stop"`
	CostUSD   float64 `json:"cost_usd"`
	HotMode   string  `json:"hot_mode"`
	Anomaly   bool    `json:"anomaly"`
	TTFBMs    int64   `json:"ttfb_ms"`
	Completed string  `json:"completion"`
}

func BenchmarkEmit(b *testing.B) {
	l, err := events.Open(b.TempDir(), "bench")
	if err != nil {
		b.Fatal(err)
	}
	defer l.Close()
	p := payload{Req: "be-1.12", Model: "deepseek/deepseek-v4-flash", HitRatio: 0.93, Expected: 21000, Missed: 120, TotalMs: 8400, Stop: "tool_use", CostUSD: 0.0021, HotMode: "inline", TTFBMs: 8400, Completed: "bcad573d5e93c86a0d4f1589364ee2923ac81dc1f7387c60a1471da33b75018c"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := l.Emit("be-1", events.TypeModelResponse, p); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkScan(b *testing.B) {
	dir := b.TempDir()
	l, err := events.Open(dir, "bench")
	if err != nil {
		b.Fatal(err)
	}
	const n = 10_000
	for i := 0; i < n; i++ {
		agent := fmt.Sprintf("w-%d", i%8)
		if _, err := l.Emit(agent, events.TypeModelResponse, payload{Req: fmt.Sprintf("%s.%d", agent, i), HitRatio: 0.9, TotalMs: 3000, Stop: "tool_use"}); err != nil {
			b.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		b.Fatal(err)
	}
	path := filepath.Join(dir, "events.jsonl")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		count := 0
		if err := events.Scan(path, func(events.Event) error { count++; return nil }); err != nil {
			b.Fatal(err)
		}
		if count < n {
			b.Fatalf("%d events read of %d", count, n)
		}
	}
}
