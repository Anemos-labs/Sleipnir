package agent

import (
	"github.com/anemos-labs/sleipnir/internal/kv"
	"testing"
)

func TestCacheKeyIsolatesSessionsButSharesWithinATeam(t *testing.T) {
	stack := &kv.Stack{Model: "test"}
	manager := &Agent{cfg: Config{ID: "mgr", SessionID: "20000101-010000-first"}}
	worker := &Agent{cfg: Config{ID: "worker", SessionID: manager.cfg.SessionID}}
	other := &Agent{cfg: Config{ID: "mgr", SessionID: "20000101-020000-second"}}
	if manager.cacheKey(stack) != worker.cacheKey(stack) {
		t.Fatal("agents sharing a session and prefix must share the routing key")
	}
	if manager.cacheKey(stack) == other.cacheKey(stack) {
		t.Fatal("independent sessions on the same day must not share a routing key")
	}
}
