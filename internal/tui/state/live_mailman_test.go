package state

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// The mailman session is a team with the mailman on (docs/SWARM-PROTOCOL.md section 5): four workers each send three progress
// messages to the manager, which reach it as digests that a harness agent, the mailman, writes. Its log holds the events that only
// that mode writes (mail.route, mail.batch, mail.digest, mail.direct) and the agent that is a service of the harness.

var mailmanRecording recording

// mailmanLog is the log of the mailman session.
func mailmanLog(t testing.TB) []byte {
	t.Helper()
	return mailmanRecording.get(t, "mailman", recordMailman)
}

var reRecipient = regexp.MustCompile(` To (\S+) \((\d+)\):`)

func recordMailman() ([]byte, error) {
	root, cleanup, err := tempRoot("state-mailman-")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	ws := filepath.Join(root, "work")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		return nil, err
	}
	sc := &mailmanScript{turn: map[string]int{}}
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}}, sc.respond)
	ts := srv.Start()
	defer ts.Close()
	prof := openaichat.DefaultProfile("mm", ts.URL)
	client := openaichat.New(openaichat.Config{Name: "mm", BaseURL: ts.URL, Profile: &prof,
		Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}})
	model := cost.Model{ID: "mm-model", ContextTokens: 1_000_000, MaxOutput: 8192, Cache: cost.OpenAICacheModel(),
		Price: cost.Price{InputPerM: 3, OutputPerM: 12, CacheReadPerM: 0.3, CacheWrite5mPerM: 3, CacheWrite1hPerM: 3}}
	ctx, cancel := context.WithTimeout(context.Background(), recordingGuard)
	defer cancel()
	yes := true
	sdir := filepath.Join(root, "session")
	s, err := session.New(ctx, session.Options{
		Cwd: ws, Root: ws, Home: filepath.Join(root, "home"), Dir: sdir,
		Provider: client, ModelInfo: &model, Model: model.ID,
		Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, Offline: true, NoMCP: true,
		Swarm: true, Workers: 7, Mailman: &yes,
	})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if _, err := s.Run(ctx, "add four notes"); err != nil {
		return nil, fmt.Errorf("the mailman session: %w", err)
	}
	if err := s.Close(); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(sdir, "events.jsonl"))
}

// mailmanScript plays every agent of the mailman session.
type mailmanScript struct {
	mu       sync.Mutex
	turn     map[string]int
	accepted bool
}

func (sc *mailmanScript) respond(c *mock.Call) mock.Reply {
	id, role, tid := whoIs(c)
	sc.mu.Lock()
	k := sc.turn[id]
	sc.turn[id]++
	sc.mu.Unlock()
	switch role {
	case "manager":
		return sc.manager(c, k)
	case "mailman":
		return sc.mailman(c)
	}
	switch k { // a worker
	case 0:
		return mock.Reply{Text: "on it", ToolCalls: []mock.ToolCall{
			call("m1", "mail", map[string]any{"to": "manager", "text": "progress " + id + " step 1"}),
			call("m2", "mail", map[string]any{"to": "manager", "text": "progress " + id + " step 2"}),
			call("m3", "mail", map[string]any{"to": "manager", "text": "progress " + id + " step 3"}),
			call("w", "write", map[string]any{"path": "note" + tid[1:] + ".txt", "content": "by " + id + "\n"}),
		}}
	case 1:
		return mock.Reply{Text: "finishing", ToolCalls: []mock.ToolCall{call("d", "task", map[string]any{"action": "done", "id": tid, "text": "added a note"})}}
	}
	return mock.Reply{Text: "summary from " + id}
}

func (sc *mailmanScript) manager(c *mock.Call, k int) mock.Reply {
	ids := []string{"T1", "T2", "T3", "T4"}
	if k == 0 {
		var calls []mock.ToolCall
		for i, id := range ids {
			calls = append(calls,
				call(fmt.Sprintf("c%d", i), "task", map[string]any{"action": "create", "title": "Add note " + id, "role": "backend", "files": []string{"note" + id[1:] + ".txt"}}),
				call(fmt.Sprintf("s%d", i), "spawn", map[string]any{"role": "backend", "task": id}))
		}
		return mock.Reply{Text: "plan", ToolCalls: append(calls, call("w", "wait", map[string]any{"until": ids, "timeout_sec": 60}))}
	}
	settled := strings.Contains(toolResults(c), "all awaited tasks settled")
	digest := false
	for _, m := range c.Messages {
		if strings.Contains(m.Content, "via mm-1 from") {
			digest = true
		}
	}
	sc.mu.Lock()
	done := sc.accepted
	if settled && !done {
		sc.accepted = true
	}
	sc.mu.Unlock()
	switch {
	case settled && !done:
		var calls []mock.ToolCall
		for i, id := range ids {
			calls = append(calls, call(fmt.Sprintf("a%d", i), "task", map[string]any{"action": "accept", "id": id}))
		}
		return mock.Reply{Text: "accepting", ToolCalls: calls}
	case !done && k < 30:
		return mock.Reply{Text: "waiting", ToolCalls: []mock.ToolCall{call(fmt.Sprintf("w%d", k), "wait", map[string]any{"until": ids, "timeout_sec": 60})}}
	case !digest && k < 40:
		// The workers' mail is still with the mailman, which waits for the burst to end: sleep until it arrives, as a manager does.
		return mock.Reply{Text: "waiting for the team's news", ToolCalls: []mock.ToolCall{call(fmt.Sprintf("n%d", k), "wait", map[string]any{"timeout_sec": 20})}}
	}
	return mock.Reply{Text: "all four notes are in"}
}

// mailman digests the batch it was handed: one message to each recipient, saying how many updates it stands for.
func (sc *mailmanScript) mailman(c *mock.Call) mock.Reply {
	batch, answered := "", 0
	last := -1
	for i, m := range c.Messages {
		if strings.Contains(m.Content, "Digest these parcels") {
			last, batch = i, m.Content
		}
	}
	for i := last + 1; last >= 0 && i < len(c.Messages); i++ {
		if c.Messages[i].Role == "assistant" {
			answered++
		}
	}
	if batch == "" || answered > 0 {
		return mock.Reply{Text: "done"}
	}
	var calls []mock.ToolCall
	for _, m := range reRecipient.FindAllStringSubmatch(batch, -1) {
		calls = append(calls, call("d"+m[1], "mail", map[string]any{"to": m[1], "text": "progress from the team: " + m[2] + " updates"}))
	}
	return mock.Reply{Text: "digesting", ToolCalls: calls}
}
