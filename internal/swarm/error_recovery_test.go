package swarm

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
)

func TestRejectDuringFailedFinalAnswerRestartsWorker(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	r := newRVRig(t, Config{}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Role != "reviewer" {
			return rvReply{Text: "ok"}
		}
		if c.Sees("T1 was sent back by the manager: check the missing case") {
			return rvReply{Text: "checked the missing case"}
		}
		if c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1", "text": "reviewed"}}}}
		}
		close(entered)
		rvBlock(ctx, release)
		return rvReply{Err: errors.New("final response failed")}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: "review", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "worker's final request", func() bool {
		select {
		case <-entered:
			return true
		default:
			return false
		}
	})
	res := r.callTool(context.Background(), "task", "mgr", "manager", map[string]any{"action": "reject", "id": "T1", "text": "check the missing case"})
	if res.IsError {
		t.Fatal(res.Text)
	}
	close(release)
	rvWait(t, "rejected work to finish after the old request fails", func() bool {
		task, _ := r.sw.Board.Snapshot().Task("T1")
		return r.idle(id) && task.Status == StatusReview && task.Result == "checked the missing case"
	})
	task, _ := r.sw.Board.Snapshot().Task("T1")
	if task.Attempts != 0 {
		t.Fatalf("old failure counted against the new assignment: %+v", task)
	}
	if r.sw.get(id).a.PendingInbox() != 0 {
		t.Fatal("recovery mail was not consumed")
	}
}

func TestWorkerFailureAfterSubmissionNotifiesManager(t *testing.T) {
	r := newRVRig(t, Config{}, func(ctx context.Context, c *rvCall) rvReply {
		if c.Assistants == 0 {
			return rvReply{Tools: []rvToolCall{{"task", map[string]any{"action": "done", "id": "T1", "text": "reviewed"}}}}
		}
		return rvReply{Err: errors.New("final response failed")}
	})
	r.sw.StartManager()
	id, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: "review", By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	rvWait(t, "failed worker to settle and notify the manager", func() bool {
		return len(r.log.OfType(events.TypeAgentEnd)) == 1 && r.idle(id) && mailSent(r, "final response failed") == 1
	})
	task, _ := r.sw.Board.Snapshot().Task("T1")
	if task.Status != StatusReview || task.Attempts != 0 {
		t.Fatalf("submitted work changed: %+v", task)
	}
	notices := 0
	for _, e := range r.log.OfType(events.TypeMailSend) {
		if strings.Contains(string(e.Data), "final response failed") {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("manager received %d failure notices, want 1", notices)
	}
}

func TestNewMailDuringFailureWakesOnceWithoutReplayingUnreadMail(t *testing.T) {
	for _, sender := range []string{"mgr", "sc-9"} {
		t.Run(sender, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			r := newRVRig(t, Config{MaxMailWakes: 1}, func(ctx context.Context, c *rvCall) rvReply {
				close(entered)
				rvBlock(ctx, release)
				return rvReply{Err: errors.New("request failed")}
			})
			r.sw.StartManager()
			id, err := r.sw.Spawn(SpawnReq{Role: "reviewer", Title: "review", By: "mgr"})
			if err != nil {
				t.Fatal(err)
			}
			rvWait(t, "provider request", func() bool {
				select {
				case <-entered:
					return true
				default:
					return false
				}
			})
			if err := r.sw.deliver(Message{From: sender, To: id, Text: "new information"}); err != nil {
				t.Fatal(err)
			}
			// Closing makes the next run fail before reading the queued mail.
			// Existing unread mail must not keep restarting that closed agent.
			if err := r.sw.get(id).a.Close(); err != nil {
				t.Fatal(err)
			}
			close(release)
			rvWait(t, "one bounded recovery run", func() bool {
				return r.idle(id) && len(r.log.OfType(events.TypeAgentEnd)) >= 2 && mailSent(r, "agent closed") == 1
			})
			r.sw.Shutdown()
			if ends := len(r.log.OfType(events.TypeAgentEnd)); ends != 2 {
				t.Fatalf("got %d runs; want initial run and one mail wake", ends)
			}
			if r.prov.callsFor(id) != 1 {
				t.Fatal("a closed agent called the provider")
			}
			if r.sw.get(id).a.PendingInbox() != 1 {
				t.Fatal("closed agent unexpectedly drained its inbox")
			}
		})
	}
}
