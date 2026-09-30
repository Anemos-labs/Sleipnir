// Package demo runs a scripted team of agents through the real harness against
// the built-in cache-faithful mock endpoint, so anyone can see the layered cache
// at work without an API key.
//
// Everything except the model is real: the session assembly, the tools, the
// permission engine, the shared and role layers, the board, the mail router, the
// governor, the warm gate and the cost accounting. The model is a script: the
// manager decomposes the work, scouts read, writers write, a reviewer checks. It
// demonstrates the mechanics and the bill, not intelligence, and it says so.
package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/cost"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/provider/mock"
	"github.com/reee344/sleipnir/internal/provider/openaichat"
	"github.com/reee344/sleipnir/internal/session"
	"github.com/reee344/sleipnir/internal/swarm"
)

// Options configures a demo run.
type Options struct {
	// Scenario is what the team is asked to do: "handbook" (the default: survey a handbook and summarise it, a second or two) or
	// "shop" (build a small shop: about twenty seconds, with mail, a stuck agent, a cold cache and a compaction, a cache break and a
	// merge that is sent back; see shop_script.go).
	Scenario string
	// Scale stretches (above 1) or squeezes (below 1) the time the shop takes, and the lifetime of its cache with it (default 1).
	Scale float64
	// Topics is the number of documents to survey (default 8, at least 2, even); the handbook scenario only.
	Topics int
	// Dir is where the workspace and the recorded session go; empty makes a
	// directory under the system's temporary directory.
	Dir string
	// Out receives the report; nil discards it.
	Out io.Writer
	// Timeout bounds the run (default 2 minutes).
	Timeout time.Duration
}

// Report is what a demo run measured.
type Report struct {
	Dir, Workspace string
	Agents         int
	Requests       int
	TasksDone      int
	Tasks          int
	// FirstRequests counts workers' first requests, and FirstReads how many of
	// those were served mostly from cache: the shared prefix paid for by others.
	FirstRequests, FirstReads int
	Usage                     struct{ Input, CacheRead, CacheWrite, Output int }
	// CostUSD is what the run cost at the demo's prices. NoShareUSD is the same
	// run if every agent had kept a private cache (each first request pays full
	// price for the prefix the team shared), NoCacheUSD if nothing were cached.
	CostUSD, NoShareUSD, NoCacheUSD float64
	HitRatio                        float64
	Elapsed                         time.Duration
	Summaries                       []string
	// Scale is the scale the shop scenario ran at (zero for the handbook).
	Scale float64
}

// Prices used by the demo. They are round numbers in the range of current
// low-cost models, printed with the report so nobody mistakes them for a quote.
var (
	prices = cost.Price{InputPerM: 0.50, OutputPerM: 2.00, CacheReadPerM: 0.125, CacheWrite5mPerM: 0.50, CacheWrite1hPerM: 0.50}
	model  = cost.Model{ID: "demo-model", ContextTokens: 1_000_000, MaxOutput: 8192, Cache: cost.OpenAICacheModel(), Price: prices}
)

// Run executes the demo and prints its report to o.Out.
func Run(ctx context.Context, o Options) (*Report, error) {
	switch o.Scenario {
	case "", "handbook":
	case "shop":
		return runShop(ctx, o)
	default:
		return nil, fmt.Errorf("demo: unknown scenario %q (handbook or shop)", o.Scenario)
	}
	if o.Topics < 2 {
		o.Topics = 8
	}
	if o.Topics%2 == 1 {
		o.Topics++
	}
	if o.Timeout <= 0 {
		o.Timeout = 2 * time.Minute
	}
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	dir := o.Dir
	if dir == "" {
		d, err := os.MkdirTemp("", "sleipnir-demo-")
		if err != nil {
			return nil, err
		}
		dir = d
	}
	ws := filepath.Join(dir, "handbook")
	if err := writeWorkspace(ws, o.Topics); err != nil {
		return nil, err
	}

	script := newScript(o.Topics)
	srv := mock.New(mock.Config{Engine: mock.EngineConfig{BlockTokens: 16, MinCacheTokens: 64}, Price: prices}, script.respond)
	ts := srv.Start()
	defer ts.Close()
	prof := openaichat.DefaultProfile("demo", ts.URL)
	client := openaichat.New(openaichat.Config{Name: "demo", BaseURL: ts.URL, Profile: &prof,
		Options: openaichat.Options{SessionHeader: true, CacheKeyBody: true}})

	sdir := filepath.Join(dir, "session")
	m := model
	s, err := session.New(ctx, session.Options{
		Cwd: ws, Root: ws, Home: filepath.Join(dir, "home"), Dir: sdir,
		Provider: client, ModelInfo: &m, Model: m.ID,
		Mode: perm.ModeBypass, NoWeb: true, TrustProject: true, Offline: true,
		Swarm: true, MaxAgents: o.Topics + 8,
	})
	if err != nil {
		return nil, err
	}
	defer s.Close()

	fmt.Fprintf(out, "Sleipnir demo: a scripted team on a %d-topic handbook, against a cache-faithful mock endpoint.\n", o.Topics)
	fmt.Fprintf(out, "Everything but the model is real: tools, permissions, layers, board, governor, warm gate, accounting.\n\n")

	rctx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	start := time.Now()
	res, err := s.Run(rctx, "The handbook has one document per topic in docs/. Survey every topic, write a short summary of each into summaries/, and have the result reviewed.")
	if err != nil {
		return nil, fmt.Errorf("demo run: %w", err)
	}
	rep := &Report{Dir: sdir, Workspace: ws, Elapsed: time.Since(start)}
	snap := s.Swarm.Board.Snapshot()
	rep.Tasks = len(snap.Tasks)
	for _, t := range snap.Tasks {
		if t.Status == swarm.StatusDone {
			rep.TasksDone++
		}
	}
	if err := s.Close(); err != nil {
		return nil, err
	}
	if err := analyse(sdir, rep); err != nil {
		return nil, err
	}
	if files, err := filepath.Glob(filepath.Join(ws, "summaries", "*.md")); err == nil {
		for _, f := range files {
			rep.Summaries = append(rep.Summaries, filepath.Base(f))
		}
		sort.Strings(rep.Summaries)
	}
	rep.print(out, res.Text)
	return rep, nil
}

// writeWorkspace creates the handbook the team works on.
func writeWorkspace(root string, topics int) error {
	files := map[string]string{
		"README.md": "# Handbook\n\nOne document per topic under docs/. Summaries go to summaries/, one file per topic.\n",
		"AGENTS.md": "# Conventions\n\n- A summary is three bullet points, present tense, no marketing words.\n- Never edit docs/; write only under summaries/.\n",
	}
	for i := 1; i <= topics; i++ {
		files[fmt.Sprintf("docs/topic-%d.md", i)] = fmt.Sprintf("# Topic %d\n\nTopic %d explains how area %d of the system works: its inputs, its guarantees and the mistakes people make.\n\n%s\n", i, i, i, filler(i))
	}
	for name, body := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			return err
		}
	}
	return os.MkdirAll(filepath.Join(root, "summaries"), 0o755)
}

func filler(i int) string {
	var b strings.Builder
	for j := 1; j <= 6; j++ {
		fmt.Fprintf(&b, "Rule %d.%d: callers of area %d must handle case %d explicitly, and must not assume the previous call succeeded.\n", i, j, i, j)
	}
	return b.String()
}

// ---- the script ----

var (
	whoRe      = regexp.MustCompile(`you: (\S+) \((\w+)\)`)
	taskRe     = regexp.MustCompile(`task (T\d+)`)
	docRe      = regexp.MustCompile(`docs/topic-(\d+)\.md`)
	topicsRe   = regexp.MustCompile(`\[topics: ([0-9 ]+)\]`)
	numbersRe  = regexp.MustCompile(`\d+`)
	maxWriters = 4 // the harness allows four concurrent writers in a shared tree
)

type script struct {
	topics int
	mu     sync.Mutex
}

func newScript(topics int) *script { return &script{topics: topics} }

func call(id, name string, args any) mock.ToolCall {
	b, _ := json.Marshal(args)
	return mock.ToolCall{ID: id, Name: name, Args: string(b)}
}

func assistantTurns(c *mock.Call) int {
	n := 0
	for _, m := range c.Messages {
		if m.Role == "assistant" {
			n++
		}
	}
	return n
}

// respond plays whichever agent is asking. The agent's identity, role and task
// are read from the prompt it sent, exactly as a model would.
func (s *script) respond(c *mock.Call) mock.Reply {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, role, tid, text := "", "", "", ""
	for i := len(c.Messages) - 1; i >= 0; i-- {
		if c.Messages[i].Role != "user" {
			continue
		}
		body := c.Messages[i].Content
		if m := whoRe.FindStringSubmatch(body); m != nil && id == "" {
			id, role = m[1], m[2]
		}
		if m := taskRe.FindStringSubmatch(body); m != nil && tid == "" {
			tid = m[1]
			text = body
		}
	}
	n := assistantTurns(c)
	switch role {
	case "manager":
		return s.manager(c, n)
	case "scout":
		return s.scout(n, id, tid, text)
	case "docs":
		return s.writer(n, id, tid, text)
	case "reviewer":
		return s.reviewer(n, id, tid)
	}
	return mock.Reply{Text: "nothing to do"}
}

// chunks splits the topics 1..n among the writers, in order.
func chunks(n, writers int) [][]int {
	out := make([][]int, writers)
	for t := 1; t <= n; t++ {
		w := (t - 1) * writers / n
		out[w] = append(out[w], t)
	}
	return out
}

func (s *script) writers() int { return min(maxWriters, s.topics/2) }

func (s *script) manager(c *mock.Call, n int) mock.Reply {
	writers := s.writers()
	switch n {
	case 0: // survey every topic in parallel: scouts are read-only, so there is no writer cap to hit
		var calls []mock.ToolCall
		var ids []string
		for i := 1; i <= s.topics; i++ {
			calls = append(calls, call(fmt.Sprintf("t%d", i), "task", map[string]any{"action": "create", "title": fmt.Sprintf("Survey topic %d (docs/topic-%d.md)", i, i), "role": "scout"}))
			ids = append(ids, "T"+strconv.Itoa(i))
		}
		for i := 1; i <= s.topics; i++ {
			calls = append(calls, call(fmt.Sprintf("s%d", i), "spawn", map[string]any{"role": "scout", "task": "T" + strconv.Itoa(i)}))
		}
		calls = append(calls, call("w", "wait", map[string]any{"until": ids, "timeout_sec": 90}))
		return mock.Reply{Text: fmt.Sprintf("Splitting the survey into %d scouting tasks.", s.topics), ToolCalls: calls}
	case 1: // accept the surveys, then hand each writer its own topics and its own files
		var calls []mock.ToolCall
		for i := 1; i <= s.topics; i++ {
			calls = append(calls, call(fmt.Sprintf("a%d", i), "task", map[string]any{"action": "accept", "id": "T" + strconv.Itoa(i)}))
		}
		var ids []string
		for j, topics := range chunks(s.topics, writers) {
			var nums, files []string
			for _, t := range topics {
				nums = append(nums, strconv.Itoa(t))
				files = append(files, fmt.Sprintf("summaries/topic-%d.md", t))
			}
			calls = append(calls, call(fmt.Sprintf("c%d", j+1), "task", map[string]any{"action": "create",
				"title": fmt.Sprintf("Write the summaries for topics %s [topics: %s]", strings.Join(nums, ", "), strings.Join(nums, " ")),
				"role":  "docs", "files": files}))
			ids = append(ids, "T"+strconv.Itoa(s.topics+j+1))
		}
		return mock.Reply{Text: "Surveys accepted; assigning writers.", ToolCalls: append(calls, spawnAll(ids, "docs", 60)...)}
	case 2:
		var calls []mock.ToolCall
		for j := 1; j <= writers; j++ {
			calls = append(calls, call(fmt.Sprintf("b%d", j), "task", map[string]any{"action": "accept", "id": "T" + strconv.Itoa(s.topics+j)}))
		}
		rid := "T" + strconv.Itoa(s.topics+writers+1)
		calls = append(calls, call("rv", "task", map[string]any{"action": "create", "title": "Review every summary against the conventions", "role": "reviewer"}))
		return mock.Reply{Text: "Summaries accepted; asking for a review.", ToolCalls: append(calls, spawnAll([]string{rid}, "reviewer", 60)...)}
	case 3:
		rid := "T" + strconv.Itoa(s.topics+writers+1)
		return mock.Reply{Text: "Reviewed.", ToolCalls: []mock.ToolCall{call("ar", "task", map[string]any{"action": "accept", "id": rid})}}
	}
	return mock.Reply{Text: fmt.Sprintf("Done: %d topics surveyed and summarised, and the summaries were reviewed.", s.topics)}
}

func spawnAll(ids []string, role string, waitSec int) []mock.ToolCall {
	var calls []mock.ToolCall
	for i, id := range ids {
		calls = append(calls, call(fmt.Sprintf("sp-%s-%d", role, i), "spawn", map[string]any{"role": role, "task": id}))
	}
	return append(calls, call("wait-"+role, "wait", map[string]any{"until": ids, "timeout_sec": waitSec}))
}

func (s *script) scout(n int, id, tid, text string) mock.Reply {
	topic := "1"
	if m := docRe.FindStringSubmatch(text); m != nil {
		topic = m[1]
	}
	switch n {
	case 0:
		return mock.Reply{Text: "reading", ToolCalls: []mock.ToolCall{call("r"+id, "read", map[string]any{"path": "docs/topic-" + topic + ".md"})}}
	case 1:
		return mock.Reply{Text: "reporting", ToolCalls: []mock.ToolCall{call("d"+id, "task", map[string]any{"action": "done", "id": tid,
			"text": "topic " + topic + ": six rules, each about handling one case explicitly"})}}
	}
	return mock.Reply{Text: "done " + id}
}

func (s *script) writer(n int, id, tid, text string) mock.Reply {
	var topics []string
	if m := topicsRe.FindStringSubmatch(text); m != nil {
		topics = numbersRe.FindAllString(m[1], -1)
	}
	switch n {
	case 0:
		var calls []mock.ToolCall
		for _, t := range topics {
			calls = append(calls, call("r"+id+t, "read", map[string]any{"path": "docs/topic-" + t + ".md"}))
		}
		return mock.Reply{Text: "reading the sources", ToolCalls: calls}
	case 1:
		var calls []mock.ToolCall
		for _, t := range topics {
			body := fmt.Sprintf("# Topic %s\n\n- Area %s has inputs, guarantees and known mistakes.\n- Callers handle each case explicitly.\n- Never assume the previous call succeeded.\n", t, t)
			calls = append(calls, call("w"+id+t, "write", map[string]any{"path": "summaries/topic-" + t + ".md", "content": body}))
		}
		return mock.Reply{Text: "writing", ToolCalls: calls}
	case 2:
		return mock.Reply{Text: "checking", ToolCalls: []mock.ToolCall{call("l"+id, "bash", map[string]any{"command": "wc -l summaries/*.md"})}}
	case 3:
		return mock.Reply{Text: "reporting", ToolCalls: []mock.ToolCall{call("d"+id, "task", map[string]any{"action": "done", "id": tid, "text": "wrote summaries for topics " + strings.Join(topics, ", ")})}}
	}
	return mock.Reply{Text: "done " + id}
}

func (s *script) reviewer(n int, id, tid string) mock.Reply {
	switch n {
	case 0:
		return mock.Reply{Text: "looking", ToolCalls: []mock.ToolCall{call("l"+id, "bash", map[string]any{"command": "cat summaries/*.md | wc -l"})}}
	case 1:
		return mock.Reply{Text: "reporting", ToolCalls: []mock.ToolCall{call("d"+id, "task", map[string]any{"action": "done", "id": tid, "text": "every summary has three bullets"})}}
	}
	return mock.Reply{Text: "done " + id}
}

// ---- measurement ----

// analyse reads the recorded session and totals what the run cost, at the demo's
// prices (the mock's own price table is irrelevant to the comparison).
func analyse(dir string, rep *Report) error {
	agents := map[string]bool{}
	seenFirst := map[string]bool{}
	var firstReads float64
	err := events.Scan(filepath.Join(dir, "events.jsonl"), func(e events.Event) error {
		if e.Type != events.TypeModelResponse {
			return nil
		}
		var m struct {
			Req   string     `json:"req"`
			Hit   float64    `json:"hit_ratio"`
			Usage core.Usage `json:"usage"`
		}
		if err := json.Unmarshal(e.Data, &m); err != nil {
			return err
		}
		u := m.Usage
		rep.Requests++
		rep.Usage.Input += u.InputTokens
		rep.Usage.CacheRead += u.CacheReadTokens
		rep.Usage.CacheWrite += u.CacheWrite5mTokens + u.CacheWrite1hTokens
		rep.Usage.Output += u.OutputTokens
		rep.CostUSD += prices.USD(u)
		total := u.InputTokens + u.CacheReadTokens + u.CacheWrite5mTokens + u.CacheWrite1hTokens
		rep.NoCacheUSD += (float64(total)*prices.InputPerM + float64(u.OutputTokens)*prices.OutputPerM) / 1e6
		agents[e.Agent] = true
		if strings.HasSuffix(m.Req, ".1") && e.Agent != "" && !seenFirst[e.Agent] {
			seenFirst[e.Agent] = true
			if e.Agent != "mgr" {
				rep.FirstRequests++
				if m.Hit >= 0.5 {
					rep.FirstReads++
				}
			}
			// Without a shared prefix this request would have found nothing to read.
			firstReads += float64(u.CacheReadTokens) * (prices.InputPerM - prices.CacheReadPerM) / 1e6
		}
		return nil
	})
	if err != nil {
		return err
	}
	rep.Agents = len(agents)
	rep.NoShareUSD = rep.CostUSD + firstReads
	if t := rep.Usage.Input + rep.Usage.CacheRead + rep.Usage.CacheWrite; t > 0 {
		rep.HitRatio = float64(rep.Usage.CacheRead) / float64(t)
	}
	return nil
}

func (r *Report) print(w io.Writer, final string) {
	fmt.Fprintf(w, "manager: %s\n\n", strings.TrimSpace(final))
	fmt.Fprintf(w, "  agents             %d (1 manager, %d workers)\n", r.Agents, r.Agents-1)
	fmt.Fprintf(w, "  tasks              %d of %d accepted by the harness after its own gate\n", r.TasksDone, r.Tasks)
	fmt.Fprintf(w, "  summaries written  %d\n", len(r.Summaries))
	fmt.Fprintf(w, "  model requests     %d in %s\n", r.Requests, r.Elapsed.Round(time.Millisecond))
	fmt.Fprintf(w, "  input tokens       %d read from cache, %d written, %d uncached (hit ratio %.0f%%)\n", r.Usage.CacheRead, r.Usage.CacheWrite, r.Usage.Input, 100*r.HitRatio)
	fmt.Fprintf(w, "  workers' first requests served from the shared prefix   %d of %d\n", r.FirstReads, r.FirstRequests)
	fmt.Fprintf(w, "  cost               $%.4f\n", r.CostUSD)
	fmt.Fprintf(w, "    if every agent kept a private cache   $%.4f (%.0f%% less with the shared prefix)\n", r.NoShareUSD, pctLess(r.CostUSD, r.NoShareUSD))
	fmt.Fprintf(w, "    with no caching at all                $%.4f (%.0f%% less)\n", r.NoCacheUSD, pctLess(r.CostUSD, r.NoCacheUSD))
	fmt.Fprintf(w, "\nPrices are round demo numbers ($%.2f/M input, $%.3f/M cached, $%.2f/M output); the model is a script.\n", prices.InputPerM, prices.CacheReadPerM, prices.OutputPerM)
	fmt.Fprintf(w, "Workspace: %s\nRecorded session: %s\nSee it again: sleipnir replay %s\n", r.Workspace, r.Dir, r.Dir)
}

func pctLess(actual, base float64) float64 {
	if base <= 0 {
		return 0
	}
	return 100 * (1 - actual/base)
}
