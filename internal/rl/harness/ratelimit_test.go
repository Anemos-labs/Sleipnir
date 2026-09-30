package harness

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"
)

// Slots are handed out in order and never sooner than the interval: a timer cannot fire early, so the lower bounds hold
// however loaded the machine is.
func TestRateLimitSpacesRequestsAcrossCallers(t *testing.T) {
	rl := NewRateLimit(6000) // one every 10 ms
	const n = 12
	start := time.Now()
	var mu sync.Mutex
	var at []time.Duration
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := rl.Wait(context.Background()); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			at = append(at, time.Since(start))
			mu.Unlock()
		}()
	}
	wg.Wait()
	sort.Slice(at, func(i, j int) bool { return at[i] < at[j] })
	for i, d := range at {
		if min := time.Duration(i) * rl.interval * 9 / 10; d < min {
			t.Fatalf("caller %d was let through after %v, a slot of its own is at %v", i, d, min)
		}
	}
}

func TestNoRateLimitIsNoWait(t *testing.T) {
	var rl *RateLimit
	if err := rl.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if NewRateLimit(0) != nil || NewRateLimit(-5) != nil {
		t.Fatal("a rate that is not positive must mean no pacing")
	}
}

// A caller whose context ends stops waiting: a cancelled rollout must not sit in the queue for minutes.
func TestRateLimitWaitEndsWithTheContext(t *testing.T) {
	rl := NewRateLimit(1) // a minute between requests
	if err := rl.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- rl.Wait(ctx) }()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Wait returned nil although its context was cancelled")
		}
	case <-time.After(30 * time.Second): // a hang guard
		t.Fatal("Wait ignored its context")
	}
}
