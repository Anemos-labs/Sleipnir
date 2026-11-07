package session_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// peerHost is a host that runs other sessions beside this one (session.Peers) and records what the tool asks of them.
type peerHost struct {
	fakeHost
	mu     sync.Mutex
	sent   []string
	spawns []session.PeerSpawn
}

func (h *peerHost) PeerList() []session.PeerInfo {
	return []session.PeerInfo{{ID: "main", Name: "main", Self: true, State: "working"},
		{ID: "api", Name: "api", Dir: "/p/api", State: "idle", Workers: 8, Model: "m/x", Goal: "ship the API", Last: "the schema is in"}}
}

func (h *peerHost) PeerSend(id, text string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, id+": "+text)
	return "delivered", nil
}

func (h *peerHost) PeerRead(id string, n int) (string, error) { return "rows of " + id, nil }

func (h *peerHost) PeerWait(_ context.Context, id string, d time.Duration) (string, error) {
	return fmt.Sprintf("answer of %s within %s", id, d), nil
}

func (h *peerHost) PeerSpawn(req session.PeerSpawn) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.spawns = append(h.spawns, req)
	return "started", nil
}

// The sessions action lists, reads, writes to and starts the sessions of a host that runs several; it checks its input, and a
// message or a new session is a change the permission mode judges. A host of one session says where sessions that talk run.
func TestHarnessToolReachesOtherSessions(t *testing.T) {
	s, call := harnessSession(t)
	s.SetHost(&fakeHost{})
	if res := call(map[string]any{"action": "sessions"}); !res.IsError || !strings.Contains(res.Text, "sleipnir web") {
		t.Errorf("a host of one session: %+v", res)
	}
	h := &peerHost{}
	s.SetHost(h)
	res := call(map[string]any{"action": "sessions", "op": "list"})
	for _, want := range []string{`main (this session: you)  "main"`, `api  "api"  idle; team of 8 workers`, "goal: ship the API", "last said: the schema is in"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("list lacks %q:\n%s", want, res.Text)
		}
	}
	if res := call(map[string]any{"action": "sessions", "op": "read", "id": "api"}); res.Text != "rows of api" {
		t.Errorf("read: %+v", res)
	}
	for _, bad := range []map[string]any{
		{"action": "sessions", "op": "send", "text": "x"},
		{"action": "sessions", "op": "send", "id": "api"},
		{"action": "sessions", "op": "send", "id": "api", "text": strings.Repeat("é", 4001)},
		{"action": "sessions", "op": "spawn"},
		{"action": "sessions", "op": "spawn", "text": "x", "workers": -1},
		{"action": "sessions", "op": "nap"},
	} {
		if res := call(bad); !res.IsError {
			t.Errorf("%v is accepted: %+v", bad, res)
		}
	}
	if res := call(map[string]any{"action": "sessions", "op": "wait", "id": "api"}); res.Text != "answer of api within 5m0s" {
		t.Errorf("wait: %+v", res)
	}
	if res := call(map[string]any{"action": "sessions", "op": "wait", "id": "api", "timeout_sec": 9999}); res.Text != "answer of api within 10m0s" {
		t.Errorf("a wait is bounded: %+v", res)
	}
	if res := call(map[string]any{"action": "sessions", "op": "send", "id": "api", "text": "the schema changed"}); res.Text != "delivered" {
		t.Errorf("send: %+v", res)
	}
	zero := 0
	if res := call(map[string]any{"action": "sessions", "op": "spawn", "goal": "write the docs", "dir": "docs", "workers": zero}); res.Text != "started" ||
		len(h.spawns) != 1 || h.spawns[0].Dir != "docs" || h.spawns[0].Workers == nil || *h.spawns[0].Workers != 0 {
		t.Errorf("spawn: %+v %+v", res, h.spawns)
	}
	s.Perm.SetMode(perm.ModePlan)
	if res := call(map[string]any{"action": "sessions", "op": "send", "id": "api", "text": "x"}); !strings.Contains(res.Text, "permission denied") {
		t.Errorf("plan mode: %+v", res)
	}
	if res := call(map[string]any{"action": "sessions", "op": "list"}); res.IsError {
		t.Errorf("list only looks: %+v", res)
	}
	if len(h.sent) != 1 {
		t.Errorf("sent %v", h.sent)
	}
}

// A turn on another session's message hands the agent the message as mail: what the model reads is the framed message, and a
// message without the frame is refused.
func TestRunMailHandsTheAgentTheMessage(t *testing.T) {
	t.Setenv("SLEIPNIR_HOME", "")
	repo := newRepo(t)
	var mu sync.Mutex
	var got string
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		mu.Lock()
		got = c.LastUser()
		mu.Unlock()
		return mock.Reply{Text: "pong"}
	})
	s, err := session.New(context.Background(), opts(t, repo, client, model))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.RunMail(context.Background(), "hello"); err == nil {
		t.Error("a message without the frame is run")
	}
	res, err := s.RunMail(context.Background(), session.MailPrefix+`api "api"] the schema changed`)
	if err != nil || res.Text != "pong" {
		t.Fatalf("RunMail = %+v %v", res, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(got, "the schema changed") {
		t.Errorf("the model read %q", got)
	}
}
