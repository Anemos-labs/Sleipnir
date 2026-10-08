package session_test

// Mailman mode through the assembled session: a real permission engine, real tools, the
// swarm's own mail router, a scripted model behind the mock endpoint.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

var mmRecipient = regexp.MustCompile(` To (\S+) \((\d+)\):`)

// toolCalled reports whether the thread already holds a call of tool name whose arguments
// contain sub.
func toolCalled(c *mock.Call, name, sub string) bool {
	for _, m := range c.Messages {
		for _, tc := range m.ToolCalls {
			if tc.Name == name && strings.Contains(tc.Args, sub) {
				return true
			}
		}
	}
	return false
}

// batchOf is the newest batch the mailman was handed and how many of its own turns
// follow it.
func batchOf(c *mock.Call) (batch string, answered int) {
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
	return
}

// Four workers each send three progress messages to the manager. With the mailman on they
// arrive as digests that name their senders; the mailman is held to its one tool by the
// real permission engine (its attempt to read a file is refused), and nothing else about
// the run changes.
func TestMailmanSessionDigestsWorkerMailForTheManager(t *testing.T) {
	repo := newRepo(t)
	var mu sync.Mutex
	var mailmanSaw []string // what came back for the mailman's forbidden read
	managerSawDigest := false
	client, model := startMock(t, func(c *mock.Call) mock.Reply {
		id, role, tid := whoIs(c)
		n := assistantTurns(c)
		switch role {
		case "manager":
			if sawText(c, "via mm-1 from") {
				mu.Lock()
				managerSawDigest = true
				mu.Unlock()
			}
			if n == 0 {
				var calls []mock.ToolCall
				var ids []string
				for i := 1; i <= 4; i++ {
					tid := "T" + itoa(i)
					ids = append(ids, tid)
					calls = append(calls,
						call("c"+itoa(i), "task", map[string]any{"action": "create", "title": "Add note " + itoa(i), "role": "backend", "files": []string{"note" + itoa(i) + ".txt"}}),
						call("s"+itoa(i), "spawn", map[string]any{"role": "backend", "task": tid}))
				}
				return mock.Reply{Text: "plan", ToolCalls: append(calls, call("w", "wait", map[string]any{"until": ids, "timeout_sec": 60}))}
			}
			if !toolCalled(c, "task", `"accept"`) {
				if strings.Contains(joined(toolResults(c)), "all awaited tasks settled") {
					var calls []mock.ToolCall
					for i := 1; i <= 4; i++ {
						calls = append(calls, call("a"+itoa(i), "task", map[string]any{"action": "accept", "id": "T" + itoa(i)}))
					}
					return mock.Reply{Text: "accepting", ToolCalls: calls}
				}
				// Mail arrived (a digest) before everything settled: wait again.
				return mock.Reply{Text: "still waiting", ToolCalls: []mock.ToolCall{call("w"+itoa(n), "wait", map[string]any{"until": []string{"T1", "T2", "T3", "T4"}, "timeout_sec": 60})}}
			}
			if !sawText(c, "via mm-1 from") && n < 12 {
				// The workers' mail is still with the mailman (it waits for the burst to end): sleep
				// until it arrives, as a manager does.
				return mock.Reply{Text: "waiting for the team's news", ToolCalls: []mock.ToolCall{call("wm"+itoa(n), "wait", map[string]any{"timeout_sec": 20})}}
			}
			return mock.Reply{Text: "all four notes are in"}
		case "mailman":
			batch, answered := batchOf(c)
			if batch == "" || answered > 0 {
				mu.Lock()
				mailmanSaw = append(mailmanSaw, toolResults(c)...)
				mu.Unlock()
				return mock.Reply{Text: "done"}
			}
			calls := []mock.ToolCall{call("x", "read", map[string]any{"path": "main.go"})} // not its tool
			for _, m := range mmRecipient.FindAllStringSubmatch(batch, -1) {
				calls = append(calls, call("d"+m[1], "mail", map[string]any{"to": m[1], "text": "progress from the team: " + m[2] + " updates"}))
			}
			return mock.Reply{Text: "digesting", ToolCalls: calls}
		}
		switch n { // a worker
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
	})
	yes := true
	o := opts(t, repo, client, model)
	o.Swarm, o.Workers, o.Mailman = true, 7, &yes
	sink := &notices{}
	o.Sink = sink
	o.NewSink = func(string) agent.Sink { return sink }
	s, err := session.New(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.Swarm.MailmanEnabled() {
		t.Fatal("Options.Mailman did not turn the mailman on")
	}
	res, err := s.Run(context.Background(), "add four notes")
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "all four notes are in" {
		t.Fatalf("manager result %q", res.Text)
	}
	for _, tid := range []string{"T1", "T2", "T3", "T4"} {
		if tk, _ := s.Swarm.Board.Snapshot().Task(tid); tk.Status != swarm.StatusDone {
			t.Errorf("%s = %s", tid, tk.Status)
		}
	}
	st := s.Swarm.MailmanStats()
	if st.Parcels != 12 || st.Digested+st.Direct != 12 || st.Pending != 0 {
		t.Fatalf("mailman stats %+v: twelve messages must be accounted for", st)
	}
	digests := 0
	for _, e := range readEvents(t, s.Dir) {
		if e.Type != events.TypeMailDigest {
			continue
		}
		digests++
		var d struct {
			To, Frame string
			Senders   []string
		}
		_ = json.Unmarshal(e.Data, &d)
		if d.To != "mgr" || !strings.Contains(d.Frame, " via mm-1 from ") || len(d.Senders) == 0 {
			t.Errorf("digest event %s", e.Data)
		}
		for _, s := range d.Senders {
			if !regexp.MustCompile(`^be-\d`).MatchString(s) {
				t.Errorf("digest names sender %q, which is no worker", s)
			}
		}
	}
	if digests == 0 || digests != st.Digests {
		t.Fatalf("%d mail.digest events, %d digests counted (%+v)", digests, st.Digests, st)
	}
	mu.Lock()
	saw, sawDigest := strings.Join(mailmanSaw, "\n"), managerSawDigest
	mu.Unlock()
	if !strings.Contains(saw, "only delivers mail") {
		t.Fatalf("the mailman's read of a file was not refused by the engine: %q", saw)
	}
	if !sawDigest {
		t.Fatal("the manager never received a digest")
	}
	// The mailman is a real agent with its own role, and it sends the tool list every agent sends.
	var spawned, tools = map[string]string{}, map[core.Hash]bool{}
	for _, e := range readEvents(t, s.Dir) {
		switch e.Type {
		case events.TypeAgentSpawn:
			var sp struct{ ID, Role string }
			_ = json.Unmarshal(e.Data, &sp)
			spawned[sp.ID] = sp.Role
		case events.TypeModelRequest:
			var q struct {
				Manifest core.Manifest `json:"manifest"`
			}
			_ = json.Unmarshal(e.Data, &q)
			tools[q.Manifest.Tools] = true
		}
	}
	if spawned["mm-1"] != "mailman" {
		t.Fatalf("spawn events: %v", spawned)
	}
	if len(tools) != 1 {
		t.Fatalf("the mailman must send the same tools as everyone: %d distinct tool lists", len(tools))
	}
	for _, id := range s.Swarm.Board.Snapshot().Agents {
		if id.ID == "mm-1" {
			t.Fatal("the mailman is on the board")
		}
	}
}

// The option beats the configuration in both directions, a single agent has no mailman,
// and a project cannot define an agent the harness would take for it.
func TestMailmanOptionConfigurationAndReservedName(t *testing.T) {
	repo := newRepo(t)
	client, model := startMock(t, func(c *mock.Call) mock.Reply { return mock.Reply{Text: "ok"} })
	on, off := true, false
	cfgOn := config.Defaults()
	cfgOn.Swarm.Mailman = true
	for _, tc := range []struct {
		name  string
		swarm bool
		cfg   *config.Config
		opt   *bool
		want  bool
	}{
		{"default", true, config.Defaults(), nil, false},
		{"configuration on", true, cfgOn, nil, true},
		{"option on over configuration off", true, config.Defaults(), &on, true},
		{"option off over configuration on", true, cfgOn, &off, false},
		{"a single agent has none", false, cfgOn, nil, false},
	} {
		o := opts(t, repo, client, model)
		o.Swarm, o.Config, o.Mailman = tc.swarm, tc.cfg, tc.opt
		s, err := session.New(context.Background(), o)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		got := s.Swarm != nil && s.Swarm.MailmanEnabled()
		s.Close()
		if got != tc.want {
			t.Errorf("%s: mailman %v, want %v", tc.name, got, tc.want)
		}
	}

	// A project definition called mailman: refused (and said so) while the mode is on, an
	// ordinary role while it is off.
	def := "---\nname: mailman\ndescription: my own courier\n---\nYou carry parcels.\n"
	writeFile(t, filepath.Join(repo, ".sleipnir", "agents", "mailman.md"), def)
	for _, tc := range []struct {
		name string
		opt  *bool
		want bool
	}{{"mode on", &on, false}, {"mode off", &off, true}} {
		o := opts(t, repo, client, model)
		o.Swarm, o.Mailman, o.TrustProject = true, tc.opt, true
		sink := &notices{}
		o.Sink = sink
		s, err := session.New(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		_, defined := s.Roles["mailman"]
		told := sink.has("agents:") && sink.has("mailman")
		s.Close()
		if defined != tc.want || (!tc.want && !told) {
			t.Errorf("%s: project role defined=%v (want %v), refusal reported=%v: %v", tc.name, defined, tc.want, told, sink.logs)
		}
	}
	// session.start records that the mode is on, and says nothing when it is not.
	for _, tc := range []struct {
		opt  *bool
		want bool
	}{{&on, true}, {nil, false}} {
		o := opts(t, repo, client, model)
		o.Swarm, o.Mailman = true, tc.opt
		s, err := session.New(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Run(context.Background(), "hello"); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, e := range readEvents(t, s.Dir) {
			if e.Type == events.TypeSessionStart {
				found = strings.Contains(string(e.Data), `"mailman":true`)
			}
		}
		s.Close()
		if found != tc.want {
			t.Errorf("session.start mentions the mailman = %v, want %v", found, tc.want)
		}
	}
	_ = time.Second
}
