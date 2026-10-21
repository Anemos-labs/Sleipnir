package session_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// After the person interrupts a turn, the next message is what the model answers: it is headed by a note that says so, because the
// conversation still ends with the work that was cut short. Only that one message is.
func TestTheMessageAfterAnInterruptIsHeadedByANote(t *testing.T) {
	repo := newRepo(t)
	var mu sync.Mutex
	var users []string
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		mu.Lock()
		users = append(users, c.LastUser())
		n := len(users)
		mu.Unlock()
		if n == 1 {
			started <- struct{}{}
			<-release // the first turn is still running when it is interrupted
		}
		return mock.Reply{Text: "ok"}
	})
	t.Cleanup(func() { close(release) })
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _, _ = s.Run(ctx, "refactor the whole package") }()
	<-started
	cancel()
	<-done
	if _, err := s.Run(context.Background(), "what time is it?"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), "and now?"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(users) != 3 {
		t.Fatalf("requests: %q", users)
	}
	// the interrupted message has no answer, so the provider is sent both in one user message: the note stands between them
	if !strings.HasSuffix(users[1], "only if it asks you to.]\n\nwhat time is it?") || !strings.Contains(users[1], "[The person interrupted your previous turn.") {
		t.Errorf("the message after the interrupt: %q", users[1])
	}
	if strings.Contains(users[2], "interrupted") || strings.Contains(users[0], "interrupted") || strings.Count(users[1], "[The person interrupted") != 1 {
		t.Errorf("a note where nothing was interrupted: %q", users)
	}
}
