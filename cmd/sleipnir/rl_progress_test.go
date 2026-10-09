package main

import (
	"bytes"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

func TestRolloutProgressPrintsScoreAndRewardApart(t *testing.T) {
	pass, score, reward := true, 0.429, 0.228
	var buf bytes.Buffer
	progressPrinter(&buf)(env.Progress{Type: "rollout.done", Done: 1, Total: 4, Task: "t", Sample: 2, Status: "ok",
		Pass: &pass, Score: &score, Reward: &reward})
	if got, want := buf.String(), "[1/4] t/2 ok pass=true score=0.429 reward=0.228\n"; got != want {
		t.Fatalf("progress line %q, want %q", got, want)
	}

	buf.Reset()
	progressPrinter(&buf)(env.Progress{Type: "rollout.done", Done: 2, Total: 4, Task: "t", Sample: 3, Status: "infra", Error: "boom"})
	if got, want := buf.String(), "[2/4] t/3 infra: boom\n"; got != want {
		t.Fatalf("progress line %q, want %q", got, want)
	}
}
