package inspect

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
)

// This file builds realistic session logs for tests and for developing the UI:
// the same event types and payload shapes the producers emit (internal/agent,
// internal/swarm, internal/session), written through the real events.Log and
// DirBlobs so the inspector is exercised on exactly what it will read.

type synthCfg struct {
	Seed      int64
	Workers   int
	Steps     int // main requests per worker
	MgrSteps  int
	Start     time.Time
	Anomalies bool // inject a cold restart (low_hit) and a tool-list drift
	NoBlobs   bool // do not write blobs
	Model     string
	Session   string
	SoftLimit int // thread tokens that trigger compaction
}

func (c *synthCfg) fill() {
	if c.Seed == 0 {
		c.Seed = 1
	}
	if c.Steps == 0 {
		c.Steps = 30
	}
	if c.MgrSteps == 0 {
		c.MgrSteps = 12
	}
	if c.Start.IsZero() {
		c.Start = time.Date(2026, 9, 30, 9, 0, 0, 0, time.UTC)
	}
	if c.Model == "" {
		c.Model = "claude-sonnet-5-5"
	}
	if c.Session == "" {
		c.Session = "synth-session"
	}
	if c.SoftLimit == 0 {
		c.SoftLimit = 16000
	}
}

// synthResult reports what the generator injected, so tests can assert exact numbers.
type synthResult struct {
	Requests, Main, Side int
	Commits              int
	Drift, LowHit        int
	Spawns, MailSent     int
	Usage                core.Usage
	ReportedUSD          float64
	PerAgentMain         map[string]int
	FirstRequestReads    map[string]int
	LastSeq              uint64
}

type simAgent struct {
	id, role     string
	next         time.Time
	steps        int
	done         int
	thread       int
	turn         int
	from         int
	spine        int
	notesTok     int
	notesVer     int
	spineVer     int
	lastReq      time.Time
	prevChain    []int
	prevSig      []string
	prevFrom     int
	evictAt      int // step at which the provider loses the entry (a low_hit anomaly)
	commits      int
	toolsVar     int
	idleAt       int // step at which the agent idles past the TTL (0: never)
	task         string
	pinTok       int
	mgr          bool
	openCalls    int
	spawnedAt    time.Time
	firstReqRead int
}

type sim struct {
	t      testing.TB
	cfg    synthCfg
	log    *events.Log
	blobs  *events.DirBlobs
	now    time.Time
	rng    *rand.Rand
	agents []*simAgent
	res    synthResult
	seq    int
	warm   map[string]time.Time // shared prefix key -> last request
	tasks  int
	sysTxt string
	tools  []byte
}

func toks(chars int) int { return (chars*10 + 35) / 36 }

// writeSynth writes a session into dir and returns what it contains.
func writeSynth(t testing.TB, dir string, cfg synthCfg) synthResult {
	t.Helper()
	cfg.fill()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	log, err := events.Open(dir, cfg.Session)
	if err != nil {
		t.Fatal(err)
	}
	s := &sim{t: t, cfg: cfg, log: log, now: cfg.Start, rng: rand.New(rand.NewSource(cfg.Seed)), warm: map[string]time.Time{}}
	s.res.PerAgentMain, s.res.FirstRequestReads = map[string]int{}, map[string]int{}
	if !cfg.NoBlobs {
		s.blobs, err = events.NewDirBlobs(filepath.Join(dir, "blobs"))
		if err != nil {
			t.Fatal(err)
		}
	}
	log.SetClock(func() time.Time { return s.now })
	s.run()
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	return s.res
}

func (s *sim) emit(agent, typ string, data any) uint64 {
	seq, err := s.log.Emit(agent, typ, data)
	if err != nil {
		s.t.Fatal(err)
	}
	s.res.LastSeq = seq
	return seq
}

func (s *sim) advance(d time.Duration) { s.now = s.now.Add(d) }

func (s *sim) put(text string) string {
	if s.blobs == nil {
		return string(core.HashString(text))
	}
	h, err := s.blobs.Put([]byte(text))
	if err != nil {
		s.t.Fatal(err)
	}
	return string(h)
}

func words(seed, n int) string {
	vocab := []string{"handler", "cache", "layer", "spine", "route", "repo", "config", "retry", "token", "prefix", "commit", "epoch", "build", "test", "lint", "module", "session", "swarm", "board", "lease"}
	var sb strings.Builder
	for i := 0; i < n; i++ {
		sb.WriteString(vocab[(seed*7+i*13+i*i)%len(vocab)])
		sb.WriteByte(' ')
	}
	return sb.String()
}

func (s *sim) layerText(kind string, ver int, chars int) string {
	body := words(ver*31+len(kind), chars/8)
	switch kind {
	case "shared":
		return "<shared-context>\n## project\n" + body + "\n</shared-context>"
	case "role":
		return "<role-context>\n" + body + "\n</role-context>"
	case "notes":
		return "<my-notes>\n## facts\n" + body + "\n</my-notes>"
	default:
		return "<history>\n" + body + "\n</history>"
	}
}

func (s *sim) toolsBlob(variant int) string {
	specs := []map[string]any{}
	for i, n := range []string{"bash", "edit", "glob", "grep", "mail", "note", "read", "recall", "spawn", "task", "wait", "write"} {
		desc := "tool " + n + " " + words(i, 20)
		if variant > 0 && n == "grep" {
			desc += fmt.Sprintf(" variant %d", variant) // a per-request tweak: the classic silent regression
		}
		specs = append(specs, map[string]any{"name": n, "description": desc, "input_schema": map[string]any{"type": "object"}})
	}
	b, _ := core.MarshalStable(specs)
	return s.put(string(b))
}

func (s *sim) run() {
	cfg := s.cfg
	sys, _ := core.MarshalStable(core.Text("You are Sleipnir. " + words(3, 900)))
	s.sysTxt = s.put(string(sys))
	s.emit("", events.TypeSessionStart, map[string]any{
		"version": "test", "model": cfg.Model, "provider": "heimdall", "dialect": "openai-chat", "swarm": cfg.Workers > 0,
		"root": "/work/project", "cwd": "/work/project", "renderer": "sleipnir-kv/1", "recon_tokens": 5400, "shared_hash": "aaaaaaaaaaaa",
	})
	s.advance(50 * time.Millisecond)

	mgr := &simAgent{id: "mgr", role: "manager", steps: cfg.MgrSteps, mgr: true, next: s.now, pinTok: 0}
	s.agents = append(s.agents, mgr)
	s.emit("swarm", events.TypeAgentSpawn, map[string]any{"id": "mgr", "role": "manager", "model": cfg.Model})
	s.emit("", events.TypeUserInput, map[string]any{"text": "Add retry with backoff to the API client and cover it with tests"})
	roles := []string{"backend", "frontend", "reviewer", "tester"}
	for i := 0; i < cfg.Workers; i++ {
		r := roles[i%len(roles)]
		a := &simAgent{id: fmt.Sprintf("%s-%d", map[string]string{"backend": "be", "frontend": "fe", "reviewer": "rv", "tester": "te"}[r], i/len(roles)+1), role: r, steps: cfg.Steps, pinTok: 1800 + 300*i}
		if cfg.Anomalies && i == 1 {
			a.idleAt = cfg.Steps / 2
		}
		if cfg.Anomalies && i == 2 {
			a.evictAt = cfg.Steps / 3
		}
		s.agents = append(s.agents, a)
	}
	// Workers are spawned by the manager at staggered times.
	spawnAt := s.now.Add(20 * time.Second)
	for i, a := range s.agents[1:] {
		a.next = spawnAt.Add(time.Duration(i) * 45 * time.Second)
		a.spawnedAt = a.next
	}
	var spawned = map[string]bool{"mgr": true}
	for {
		var pick *simAgent
		for _, a := range s.agents {
			if a.done >= a.steps {
				continue
			}
			if pick == nil || a.next.Before(pick.next) {
				pick = a
			}
		}
		if pick == nil {
			break
		}
		if pick.next.After(s.now) {
			s.now = pick.next
		}
		if !spawned[pick.id] {
			s.spawn(mgr, pick)
			spawned[pick.id] = true
		}
		s.step(pick)
	}
	for _, a := range s.agents[1:] {
		s.advance(400 * time.Millisecond)
		s.emit("swarm", events.TypeAgentEnd, map[string]any{"id": a.id, "state": "idle", "evidence": "edited 3 (client.go, client_test.go, retry.go); last test \"go test ./...\" passed"})
		s.advance(200 * time.Millisecond)
	}
	s.advance(time.Second)
	s.emit("", events.TypeSessionEnd, map[string]any{"cost_usd": s.res.ReportedUSD})
}

func (s *sim) spawn(mgr, a *simAgent) {
	s.tasks++
	a.task = fmt.Sprintf("T%d", s.tasks)
	title := []string{"Add retry helper", "Wire retry into client", "Review the retry change", "Write table tests"}[(s.tasks-1)%4]
	// The manager's spawn call, as swarm/tools.go logs it.
	s.advance(300 * time.Millisecond)
	call := fmt.Sprintf("call_spawn_%d", s.tasks)
	s.emit("mgr", events.TypeToolCall, map[string]any{"id": call, "name": "spawn", "input": map[string]any{"role": a.role, "task": title, "brief": "acceptance: tests pass", "files": []string{"internal/client/**"}}})
	s.emit("mgr", events.TypeBoardOp, map[string]any{"op": "create", "version": s.tasks*3 - 2})
	s.emit("mgr", events.TypeBoardOp, map[string]any{"op": "assign", "version": s.tasks*3 - 1})
	s.emit(a.id, events.TypeBoardOp, map[string]any{"op": "agent", "version": s.tasks * 3})
	s.emit("swarm", events.TypeAgentSpawn, map[string]any{"id": a.id, "role": a.role, "task": a.task, "by": "mgr", "parent": "mgr", "model": s.cfg.Model})
	s.res.Spawns++
	s.advance(20 * time.Millisecond)
	s.emit("mgr", events.TypeToolResult, map[string]any{"id": call, "name": "spawn", "error": false, "chars": 60, "ms": 12})
}

// step performs one turn of an agent: boundary, request, tools.
func (s *sim) step(a *simAgent) {
	cfg := s.cfg
	a.done++
	a.turn++
	first := a.done == 1
	// Idle gap: a worker waits on something for longer than the cache lifetime.
	if a.idleAt > 0 && a.done == a.idleAt {
		s.advance(7 * time.Minute)
	}
	// ---- boundary: compaction ----
	if !a.mgr && a.thread > cfg.SoftLimit {
		s.compact(a)
	}
	// ---- layers ----
	shared := s.layerText("shared", 1, 21000)
	role := s.layerText("role", len(a.role), a.pinTok*36/10)
	var notes, spine string
	if a.notesVer > 0 {
		notes = s.layerText("notes", a.notesVer, a.notesTok*36/10)
	}
	if a.spineVer > 0 {
		spine = s.layerText("spine", a.spineVer, a.spine*36/10)
	}
	toolsHash := s.toolsBlob(a.toolsVar)
	hotText := ""
	hotTok := 0
	if len(s.agents) > 1 {
		hotTok = 300 + s.rng.Intn(500)
		hotText = "<live>\n" + words(a.done, hotTok*36/80) + "\n</live>"
	}
	hotHash := ""
	if hotText != "" {
		hotHash = s.put(hotText)
	}
	g0 := toks(len(s.sysTxt)) + 2400 // system + tool schemas
	g1, g2 := toks(len(shared)), toks(len(role))
	g3, g4 := toks(len(notes)), toks(len(spine))
	if a.mgr {
		g2 = 0
		role = ""
	}
	a.thread += 600 + s.rng.Intn(2600)
	chain := []int{g0, g1, g2, g3, g4, a.thread}
	sigs := []string{toolsHash, s.hashOf(shared), s.hashOf(role), s.hashOf(notes), s.hashOf(spine), fmt.Sprintf("th%d", a.from)}

	// ---- drift injection: the tool list is silently rewritten ----
	drift := false
	if cfg.Anomalies && a.id == "be-1" && a.done == cfg.Steps*2/3 {
		a.toolsVar++
		toolsHash = s.toolsBlob(a.toolsVar)
		sigs[0] = toolsHash
		drift = true
	}

	// ---- cache model (explicit breakpoints, 5 minute TTL) ----
	// expected is what the drift guard would report: the exact prefix shared with
	// the agent's previous request, in estimated tokens.
	warmSelf := !first && s.now.Sub(a.lastReq) < 5*time.Minute
	shareKey := cfg.Model + "/g01"
	expected := 0
	if !first {
		for i := range chain {
			if sigs[i] != a.prevSig[i] || (i == 5 && a.from != a.prevFrom) {
				break
			}
			if i == 5 {
				expected += a.prevChain[5]
			} else {
				expected += chain[i]
			}
		}
	}
	prefix := 0
	evicted := cfg.Anomalies && a.evictAt > 0 && a.done == a.evictAt
	switch {
	case drift || evicted:
		prefix = 0
		if evicted {
			prefix = g0 // the provider kept only the very front
		}
	case warmSelf:
		prefix = expected
	default:
		// Cold: only what other agents keep warm.
		if t, ok := s.warm[shareKey]; ok && s.now.Sub(t) < 5*time.Minute {
			prefix = g0 + g1
		}
	}
	prompt := g0 + g1 + g2 + g3 + g4 + a.thread + hotTok
	read := prefix / 16 * 16
	if read > prompt-hotTok {
		read = (prompt - hotTok) / 16 * 16
	}
	write := prompt - hotTok - read
	fresh := hotTok
	out := 40 + s.rng.Intn(180)
	price := struct{ in, out, read, write float64 }{2, 10, 0.2, 2.5}
	usd := (float64(fresh)*price.in + float64(read)*price.read + float64(write)*price.write + float64(out)*price.out) / 1e6

	// ---- events: request ----
	reqID := fmt.Sprintf("%s.%d", a.id, a.done+a.commits)
	sections := []map[string]any{}
	add := func(name, text string, tok int, bp bool) {
		if text == "" {
			return
		}
		sec := map[string]any{"name": name, "hash": s.put(text), "tokens": tok}
		if bp {
			sec["bp"] = true
		}
		sections = append(sections, sec)
	}
	add("shared", shared, g1, true)
	add("role", role, g2, g2 > 1500)
	add("notes", notes, g3, false)
	add("spine", spine, g4, false)
	s.advance(15 * time.Millisecond)
	if drift {
		s.emit(a.id, events.TypeCacheAnomaly, map[string]any{"kind": "drift", "diverged": "tools", "shared_blocks": 0})
		s.res.Drift++
	}
	man := map[string]any{"model": cfg.Model, "tools": toolsHash, "system": []string{s.sysTxt}, "wire": string(core.HashString(reqID))}
	s.emit(a.id, events.TypeModelRequest, map[string]any{
		"req": reqID, "agent": a.id, "role": a.role, "kind": "main", "model": cfg.Model, "provider": "heimdall", "dialect": "openai-chat",
		"sections": sections, "thread_from": a.from + 1, "thread_to": a.turn * 2, "hot": hotHash,
		"cache_key": "sl:synth:e98d13b1d2fd:0", "prefix_key": "b89a9c17bb940e87", "params": map[string]any{"max_tokens": 8192},
		"breakpoints": []any{map[string]any{"label": "thread"}}, "shared_blocks": len(sections), "shared_tokens": expected,
		"tools": 12, "renderer": "sleipnir-kv/1", "manifest": man, "wire_hash": string(core.HashString(reqID)),
	})
	ttfb := 300 + s.rng.Intn(900)
	lat := ttfb + 800 + s.rng.Intn(3000)
	s.advance(time.Duration(ttfb) * time.Millisecond)
	warmBefore := warmSelf
	anomaly := warmBefore && expected >= 1024 && read < expected*7/10
	if anomaly {
		s.emit(a.id, events.TypeCacheAnomaly, map[string]any{"kind": "low_hit", "req": reqID, "expected_read": expected, "actual_read": read, "diverged": ""})
		s.res.LowHit++
	}
	s.advance(time.Duration(lat-ttfb) * time.Millisecond)
	u := core.Usage{InputTokens: fresh, CacheReadTokens: read, CacheWrite5mTokens: write, OutputTokens: out}
	hit := u.HitRatio()
	comp, _ := core.MarshalStable(core.Turn{ID: core.TurnID(a.turn*2 + 1), Role: core.RoleAssistant, Blocks: []core.Block{core.Text("ok")}})
	s.emit(a.id, events.TypeModelResponse, map[string]any{
		"req": reqID, "id": "gen-" + reqID, "model": cfg.Model, "provider": "heimdall", "usage": u, "cost_usd": usd, "gateway_cost": true,
		"hit_ratio": hit, "expected_read": expected, "anomaly": anomaly, "stop": "tool_use", "ttfb_ms": ttfb, "total_ms": lat,
		"completion": s.put(string(comp)),
	})
	s.res.Requests++
	s.res.Main++
	s.res.PerAgentMain[a.id]++
	if first {
		s.res.FirstRequestReads[a.id] = read
	}
	s.res.Usage = s.res.Usage.Add(u)
	s.res.ReportedUSD += usd
	s.warm[shareKey] = s.now
	a.lastReq = s.now
	a.prevChain, a.prevSig, a.prevFrom = chain, sigs, a.from

	// ---- tools ----
	s.tools_(a)
}

func (s *sim) hashOf(text string) string {
	if text == "" {
		return ""
	}
	return string(core.HashString(text))
}

// tools_ runs the tool calls of one turn and the coordination they cause.
func (s *sim) tools_(a *simAgent) {
	type tc struct {
		name  string
		input map[string]any
		ms    int
		chars int
		err   bool
	}
	var calls []tc
	switch {
	case a.mgr:
		switch a.done % 4 {
		case 1:
			calls = append(calls, tc{"task", map[string]any{"action": "list"}, 40, 900, false})
		case 2:
			calls = append(calls, tc{"wait", map[string]any{"timeout_sec": 120}, 20000 + s.rng.Intn(30000), 700, false})
		case 3:
			calls = append(calls, tc{"task", map[string]any{"action": "accept", "id": "T1", "text": "verified"}, 30, 30, false})
		default:
			calls = append(calls, tc{"mail", map[string]any{"to": "be-1", "text": "please also cover the 429 path", "kind": "request"}, 25, 40, false})
		}
	default:
		names := []string{"read", "grep", "edit", "bash", "read", "edit", "bash", "glob"}
		n := names[(a.done+len(a.id))%len(names)]
		in := map[string]any{"path": "internal/client/" + []string{"client.go", "retry.go", "client_test.go"}[a.done%3]}
		if n == "bash" {
			in = map[string]any{"command": "go test ./internal/client/... -run Retry -count=1"}
		}
		ms := 20 + s.rng.Intn(300)
		if n == "bash" {
			ms = 1200 + s.rng.Intn(7000)
		}
		calls = append(calls, tc{n, in, ms, 400 + s.rng.Intn(5000), n == "bash" && s.rng.Intn(9) == 0})
		if a.done%9 == 0 {
			calls = append(calls, tc{"task", map[string]any{"action": "update", "id": a.task, "text": "retry helper written; wiring into client"}, 15, 30, false})
		}
		if a.done == a.steps {
			calls = append(calls, tc{"task", map[string]any{"action": "done", "id": a.task, "text": "retry with jitter added; tests pass"}, 900, 120, false})
		}
	}
	for i, c := range calls {
		id := fmt.Sprintf("call_%s_%d_%d", a.id, a.done, i)
		s.advance(30 * time.Millisecond)
		s.emit(a.id, events.TypeToolCall, map[string]any{"id": id, "name": c.name, "input": c.input})
		switch {
		case c.name == "task" && c.input["action"] == "update":
			s.emit(a.id, events.TypeBoardOp, map[string]any{"op": "update", "version": 100 + a.done})
		case c.name == "task" && c.input["action"] == "done":
			s.emit(a.id, events.TypeBoardOp, map[string]any{"op": "finish", "version": 200 + a.done})
		case c.name == "task" && c.input["action"] == "accept":
			s.emit("manager", events.TypeBoardOp, map[string]any{"op": "finish", "version": 300 + a.done})
		case c.name == "mail":
			mid := fmt.Sprintf("m%d", s.res.MailSent+1)
			s.emit(a.id, events.TypeMailSend, map[string]any{"id": mid, "from": a.id, "to": "be-1", "kind": "request", "text": "please also cover the 429 path"})
			s.emit("be-1", events.TypeMailDeliver, map[string]any{"id": mid, "from": a.id})
			s.res.MailSent++
		}
		s.advance(time.Duration(c.ms) * time.Millisecond)
		s.emit(a.id, events.TypeToolResult, map[string]any{"id": id, "name": c.name, "error": c.err, "chars": c.chars, "truncated": c.chars > 4000, "handle": "", "ms": c.ms, "meta": nil})
	}
	a.thread += 200
	// Occasional coordination noise from the harness.
	if a.done%11 == 0 && !a.mgr {
		s.emit("harness", events.TypeBoardOp, map[string]any{"op": "alert", "version": 400 + a.done})
	}
	if a.done%5 == 0 {
		mid := fmt.Sprintf("m%d", s.res.MailSent+1)
		s.emit(a.id, events.TypeMailSend, map[string]any{"id": mid, "from": a.id, "to": "mgr", "kind": "info", "text": "progress: " + words(a.done, 6)})
		s.emit("mgr", events.TypeMailDeliver, map[string]any{"id": mid, "from": a.id})
		s.res.MailSent++
	}
	a.next = s.now.Add(time.Duration(300+s.rng.Intn(1500)) * time.Millisecond)
	s.emit(a.id, events.TypeTurnAppend, map[string]any{"id": a.turn*2 + 1, "role": "user", "origin": "tool", "blocks": []any{}})
}

// compact emits a generational compaction the way agent/compact.go logs one.
func (s *sim) compact(a *simAgent) {
	total := a.thread
	removed := total * 70 / 100
	retained := total - removed
	spineAdd := 60 + s.rng.Intn(40)
	s.advance(10 * time.Millisecond)
	s.emit(a.id, events.TypeCompactPlan, map[string]any{"decision": "start", "mode": "fork", "reason": fmt.Sprintf("thread %dk over soft limit %dk", total/1000, s.cfg.SoftLimit/1000), "warm": true, "thread_tokens": total})
	// The compactor is a fork of the agent's own request: it reads almost everything.
	s.advance(20 * time.Millisecond)
	cid := fmt.Sprintf("%s.c%d", a.id, a.commits+1)
	prompt := 2400 + 1800 + 21000/4 + a.spine + a.notesTok + total
	s.emit(a.id, events.TypeModelRequest, map[string]any{"req": cid, "agent": a.id, "role": "compactor", "kind": "compactor", "model": s.cfg.Model, "provider": "heimdall", "sections": nil, "thread_from": a.from + 1, "thread_to": a.turn * 2, "tools": 12, "renderer": "sleipnir-kv/1"})
	s.emit(a.id, events.TypeCompactPatch, map[string]any{"stage": "request", "reason": "thread pressure", "thread_from": a.from + 1, "thread_to": a.turn * 2})
	s.advance(3500 * time.Millisecond)
	u := core.Usage{InputTokens: 420, CacheReadTokens: prompt - 420, OutputTokens: 130}
	usd := (float64(u.InputTokens)*2 + float64(u.CacheReadTokens)*0.2 + float64(u.OutputTokens)*10) / 1e6
	s.emit(a.id, events.TypeModelResponse, map[string]any{"req": cid, "id": "gen-" + cid, "model": s.cfg.Model, "usage": u, "cost_usd": usd, "hit_ratio": u.HitRatio(), "side": true, "stop": "end_turn"})
	s.res.Requests++
	s.res.Side++
	s.res.Usage = s.res.Usage.Add(u)
	s.res.ReportedUSD += usd
	s.emit(a.id, events.TypeCompactPatch, map[string]any{"stage": "ready", "keep_from": a.turn, "removed_tokens": removed, "retained_tokens": retained, "spine_added": spineAdd, "masked": 2, "mechanical_lines": 0, "fallback": false, "warnings": nil, "cost_ite": 4200})
	s.advance(2 * time.Second)
	s.emit(a.id, events.TypeCompactPlan, map[string]any{"decision": "commit?", "yes": true, "net_ite": 9000 + s.rng.Intn(9000), "reason": "pays back within 3 turns", "warm": true, "thread_tokens": total, "age_ms": 2000})
	s.emit(a.id, events.TypeCompactCommit, map[string]any{
		"reason": "pays back within 3 turns", "keep_from": a.turn, "removed_turns": 12, "removed_tokens": removed, "retained_tokens": retained,
		"spine_added": spineAdd, "masked": 2, "mechanical_lines": 0, "notes_changed": a.commits%2 == 1, "notes_over_budget": false, "fallback": false, "warnings": nil, "held_ms": 2000,
	})
	a.commits++
	s.res.Commits++
	a.thread = retained
	a.from = a.turn - 3
	a.spine += spineAdd
	a.spineVer++
	if a.commits%2 == 1 {
		a.notesVer++
		a.notesTok += 120
	}
	s.emit(a.id, events.TypeLayerCommit, map[string]any{"scope": "agent", "spine": "abc", "spine_version": a.spineVer, "notes": "def", "notes_changed": a.commits%2 == 1})
}

// synthDir is a convenience for tests: a temp dir with a swarm session.
func synthDir(t testing.TB, cfg synthCfg) (string, synthResult) {
	dir := filepath.Join(t.TempDir(), "session")
	res := writeSynth(t, dir, cfg)
	return dir, res
}

// TestWriteSynthSession writes a session to $INSPECT_SYNTH_DIR for developing the
// UI by hand: INSPECT_SYNTH_DIR=/tmp/s go test -run WriteSynthSession ./internal/inspect
func TestWriteSynthSession(t *testing.T) {
	dir := os.Getenv("INSPECT_SYNTH_DIR")
	if dir == "" {
		t.Skip("set INSPECT_SYNTH_DIR to write a synthetic session")
	}
	workers := 4
	res := writeSynth(t, filepath.Join(dir, "swarm"), synthCfg{Workers: workers, Steps: 40, Anomalies: true})
	writeSynth(t, filepath.Join(dir, "solo"), synthCfg{Workers: 0, Steps: 60, MgrSteps: 1, Session: "solo-session"})
	b, _ := json.MarshalIndent(res, "", " ")
	t.Logf("%s", b)
}
