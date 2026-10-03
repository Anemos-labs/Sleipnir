package session_test

import (
	"context"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

func TestEffortRestoresPreferenceIncludingDefault(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(*mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	o := opts(t, repo, client, model)
	for i, want := range []string{"", "high", ""} {
		s, err := session.New(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := s.Effort()
		if got != want {
			t.Fatalf("resume %d effort=%q, want %q", i, got, want)
		}
		if _, err := s.Run(context.Background(), "hello"); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			s.SetEffort("high")
		} else {
			s.SetEffort("default")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		o.Resume = s.Dir
	}
}
