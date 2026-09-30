package traj

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

// Error kinds a tool result can be classified as. The tool.result event carries no
// error kind yet, so classification uses meta["error_kind"] when a tool provides
// it and otherwise the wording of the harness's own error messages (see
// classify). The wording is matched only on results that are errors.
const (
	errStale       = "stale"
	errLease       = "lease"
	errScope       = "scope"
	errUnknownTool = "unknown_tool"
	errInvalid     = "invalid_input"
)

// classify names the kind of a failed tool result, or "" when it is not one of
// the protocol failures signals count.
func classify(t toolRun) string {
	if !t.isErr {
		return ""
	}
	if k, ok := t.meta["error_kind"].(string); ok && k != "" {
		switch strings.ToLower(k) {
		case "stale", "stale_write", "stale_read":
			return errStale
		case "lease", "lease_conflict":
			return errLease
		case "scope", "scope_violation":
			return errScope
		case "unknown_tool":
			return errUnknownTool
		case "invalid_input", "invalid_arguments", "invalid":
			return errInvalid
		}
	}
	txt := strings.ToLower(t.text)
	switch {
	case strings.Contains(txt, "changed since you last read it"), strings.Contains(txt, "has not been read by you yet"):
		return errStale
	case strings.Contains(txt, "is being edited by"):
		return errLease
	case strings.Contains(txt, "outside your scope"), strings.Contains(txt, "cannot widen scope"),
		strings.Contains(txt, "edit only inside your scope"), strings.Contains(txt, "overlaps t"):
		return errScope
	case strings.Contains(txt, "unknown tool"):
		return errUnknownTool
	case strings.Contains(txt, "not valid json"), strings.Contains(txt, "invalid arguments"),
		strings.Contains(txt, "input must be a json object"), strings.Contains(txt, "unknown action"):
		return errInvalid
	}
	return ""
}

var checkCmdRE = regexp.MustCompile(`(?i)\b(?:go\s+test|pytest|py\.test|(?:npm|yarn|pnpm)\s+(?:run\s+)?test|cargo\s+test|mvn\s+(?:-\S+\s+)*test|gradle\s+test|make\s+(?:test|check)|jest|vitest|rspec|tox|ctest|dotnet\s+test|phpunit|mix\s+test)\b`)

// coordinationTools are excluded from duplicate-work detection: several agents
// listing the board or waiting is coordination, not repeated work.
var coordinationTools = map[string]bool{"task": true, "mail": true, "note": true, "wait": true, "spawn": true, "recall": true}

// writeTools change files.
var writeTools = map[string]bool{"edit": true, "write": true, "apply_patch": true}

// deriveSignals fills the rl.Sig* vocabulary from the log. The derivations are
// documented in the package documentation.
func (b *builder) deriveSignals() {
	r, v := b.run, b.v
	s := map[string]float64{}
	b.signals = s

	// Requests.
	retries := 0
	for _, idxs := range r.modelErrs {
		for _, i := range idxs {
			var p struct {
				Attempt int `json:"attempt"`
			}
			if json.Unmarshal(r.evs[i].Data, &p) == nil && p.Attempt > 0 {
				retries++
			}
		}
	}
	s[rl.SigRequests] = float64(len(r.reqOrder) + retries)
	s[SigRequestRetry] = float64(retries)
	s[SigRequestErrors] = float64(b.requestErrors)
	s[rl.SigSteps] = float64(len(b.mains))
	workers := 0
	for _, st := range b.mains {
		if st.a.role != rl.RoleManager {
			workers++
		}
	}
	s[rl.SigWorkerSteps] = float64(workers)
	s[SigPrefixBreaks] = float64(b.prefixBreaks)
	s[rl.SigCriticalPath] = float64(b.criticalPath())

	// Compaction and cache.
	s[rl.SigCompactions] = float64(len(v.commits))
	s[rl.SigCompactRejects] = float64(v.rejects)
	s[rl.SigCacheAnomalies] = float64(v.anomalies)

	// Tools.
	b.toolSignals(s)

	// Mail.
	b.mailSignals(s)

	// Swarm shape.
	spawns := 0
	for _, sp := range v.spawns {
		if sp.parent != "" {
			spawns++
		}
	}
	s[rl.SigSpawns] = float64(spawns)
	s[rl.SigSpawnNoResult] = float64(b.spawnNoResult())
	s[rl.SigIdleMs] = float64(b.idleMs())
}

// criticalPath is the longest chain of main steps through the DAG. Steps of one
// agent are a chain; a spawn edge makes the child's first step follow the
// spawning step; a mail edge makes the recipient's step follow the sender's.
// Agents run concurrently, so independent chains do not add.
func (b *builder) criticalPath() int {
	preds := map[string][]string{} // step id -> cross-agent predecessor step ids
	firstOf := map[string]string{} // agent id -> its first main step id
	for _, a := range b.order {
		if len(a.main) > 0 {
			firstOf[a.id] = a.main[0].q.id
		}
	}
	for _, e := range b.edges {
		switch e.Kind {
		case rl.EdgeSpawn:
			if to, ok := firstOf[e.To]; ok {
				preds[to] = append(preds[to], e.From)
			}
		case rl.EdgeMail:
			preds[e.To] = append(preds[e.To], e.From)
		}
	}
	dp := map[string]int{}
	prev := map[string]string{} // step id -> previous main step of the same agent
	for _, a := range b.order {
		for i := 1; i < len(a.main); i++ {
			prev[a.main[i].q.id] = a.main[i-1].q.id
		}
	}
	best := 0
	for _, st := range b.mains { // request order: every predecessor precedes its successor
		m := 0
		if p, ok := prev[st.q.id]; ok {
			m = dp[p]
		}
		for _, p := range preds[st.q.id] {
			if dp[p] > m {
				m = dp[p]
			}
		}
		dp[st.q.id] = m + 1
		if m+1 > best {
			best = m + 1
		}
	}
	return best
}

func (b *builder) toolSignals(s map[string]float64) {
	var toolErrs, invalid, stale, lease, scope, recalls, checks, claims, accepted, reReads, dup float64
	checkCmd := ""
	if b.task != nil {
		checkCmd = squash(b.task.Verifier.Cmd)
	}
	firstCaller := map[string]string{}      // canonical call -> first agent that made it
	editors := map[string]map[string]bool{} // path -> agents that edited it
	type readState struct{ lastRead, lastWrite core.TurnID }
	// Re-reads need the thread positions of calls and of each compaction's keep_from.
	turnOf := b.turnOfToolUse()
	folded := map[string]core.TurnID{} // agent -> highest keep_from committed so far
	commitsBy := map[string][]commitEv{}
	for _, c := range b.v.commits {
		commitsBy[c.agent] = append(commitsBy[c.agent], c)
	}
	reads := map[string]map[string]*readState{} // agent -> path -> state

	runs := append([]toolRun(nil), b.runs...)
	sort.SliceStable(runs, func(i, j int) bool { return runs[i].seq < runs[j].seq })
	for _, t := range runs {
		kind := classify(t)
		if t.isErr {
			toolErrs++
		}
		if t.invalid || kind == errUnknownTool || kind == errInvalid {
			invalid++
		}
		switch kind {
		case errStale:
			stale++
		case errLease:
			lease++
		case errScope:
			scope++
		}
		if t.name == "recall" {
			recalls++
		}
		in := parseObject(t.input)
		switch t.name {
		case "bash":
			cmd, _ := in["command"].(string)
			if cmd != "" && (checkCmdRE.MatchString(cmd) || checkCmd != "" && strings.Contains(squash(cmd), checkCmd)) {
				checks++
			}
		case "task":
			if act, _ := in["action"].(string); act == "done" {
				claims++
				if t.has && !t.isErr {
					accepted++
				}
			}
		}
		if !coordinationTools[t.name] {
			key := t.name + "\x00" + canonicalJSON(t.input)
			if first, ok := firstCaller[key]; !ok {
				firstCaller[key] = t.agent.id
			} else if first != t.agent.id {
				dup++
			}
		}
		if writeTools[t.name] && t.has && !t.isErr {
			for _, p := range editedPaths(t.name, in) {
				if editors[p] == nil {
					editors[p] = map[string]bool{}
				}
				editors[p][t.agent.id] = true
			}
		}
		// Reads after compaction.
		id := t.agent.id
		if reads[id] == nil {
			reads[id] = map[string]*readState{}
		}
		turn := turnOf[id+"\x00"+t.id]
		for _, c := range commitsBy[id] {
			if c.seq < t.seq && c.keepFrom > folded[id] {
				folded[id] = c.keepFrom
			}
		}
		switch {
		case t.name == "read":
			if p, _ := in["path"].(string); p != "" {
				p = filepath.Clean(p)
				st := reads[id][p]
				if st == nil {
					st = &readState{}
					reads[id][p] = st
				}
				if st.lastRead != 0 && st.lastRead < folded[id] && st.lastWrite < st.lastRead {
					reReads++
				}
				st.lastRead = turn
			}
		case writeTools[t.name] && t.has && !t.isErr:
			for _, p := range editedPaths(t.name, in) {
				if st := reads[id][filepath.Clean(p)]; st != nil {
					st.lastWrite = turn
				}
			}
		}
	}
	for _, agents := range editors {
		if len(agents) > 1 {
			dup += float64(len(agents) - 1)
		}
	}
	s[rl.SigToolErrors] = toolErrs
	s[rl.SigInvalidToolCalls] = invalid
	s[rl.SigStaleWrites] = stale
	s[rl.SigLeaseConflicts] = lease
	s[rl.SigScopeViolations] = scope
	s[rl.SigRecalls] = recalls
	s[rl.SigVerifierRuns] = checks
	s[rl.SigDoneClaims] = claims
	s[rl.SigDoneAccepted] = accepted
	s[rl.SigReReads] = reReads
	s[rl.SigDuplicateWork] = dup
}

// turnOfToolUse maps "<agent>\x00<tool id>" to the thread turn id of the assistant
// turn that issued it.
func (b *builder) turnOfToolUse() map[string]core.TurnID {
	out := map[string]core.TurnID{}
	for agent, turns := range b.v.turns {
		for _, t := range turns {
			if t.turn.Role != core.RoleAssistant {
				continue
			}
			for _, blk := range t.turn.Blocks {
				if blk.Kind == core.BlockToolUse {
					out[agent+"\x00"+blk.ToolID] = t.turn.ID
				}
			}
		}
	}
	return out
}

func (b *builder) mailSignals(s map[string]float64) {
	v := b.v
	s[rl.SigMailSent] = float64(len(v.mails))
	seen := map[string]bool{}
	dup := 0
	for _, m := range v.mails {
		k := m.from + "\x00" + m.to + "\x00" + strings.TrimSpace(m.text)
		if seen[k] {
			dup++
		}
		seen[k] = true
	}
	s[rl.SigMailDuplicate] = float64(dup)
	// Ignored: delivered, and the recipient never made another main request.
	ignored := 0
	at := map[string]uint64{}
	for _, d := range v.delivers {
		if _, ok := at[d.id]; !ok {
			at[d.id] = d.seq
		}
	}
	for _, m := range v.mails {
		seq, ok := at[m.id]
		if !ok {
			seq = m.seq
		}
		if firstMainAfter(b.agents[m.to], seq) == nil {
			ignored++
		}
	}
	s[rl.SigMailIgnored] = float64(ignored)
}

// spawnNoResult counts spawned workers whose work cannot have reached the result:
// they made no main step, ended failed, or tried to change files and never
// succeeded nor had a done claim accepted. It is coarse; the log has no per-worker
// diff attribution.
func (b *builder) spawnNoResult() int {
	n := 0
	for _, a := range b.order {
		if !a.spawned || a.parent == "" {
			continue
		}
		if len(a.main) == 0 || a.ended && a.state == "failed" {
			n++
			continue
		}
		tried, ok, accepted := false, false, false
		for _, t := range b.runs {
			if t.agent != a {
				continue
			}
			if writeTools[t.name] {
				tried = true
				ok = ok || t.has && !t.isErr
			}
			if t.name == "task" && t.has && !t.isErr {
				if act, _ := parseObject(t.input)["action"].(string); act == "done" {
					accepted = true
				}
			}
		}
		if tried && !ok && !accepted {
			n++
		}
	}
	return n
}

// idleMs sums the time spawned workers sat ended before their next request (woken
// by mail, or reused for another task). Time after a worker's last end is not
// counted: the log does not say when, or whether, it would have been used.
func (b *builder) idleMs() int64 {
	var total int64
	for _, a := range b.order {
		if !a.spawned || a.parent == "" {
			continue
		}
		for _, e := range b.v.ends {
			if e.id != a.id {
				continue
			}
			for _, q := range a.reqs {
				if q.kind == rl.KindMain && q.seq > e.seq {
					if d := q.ts.Sub(e.ts).Milliseconds(); d > 0 {
						total += d
					}
					break
				}
			}
		}
	}
	return total
}

// editedPaths lists the files a write-class tool call touches.
func editedPaths(tool string, in map[string]any) []string {
	switch tool {
	case "edit", "write":
		if p, _ := in["path"].(string); p != "" {
			return []string{filepath.Clean(p)}
		}
	case "apply_patch":
		patch, _ := in["patch"].(string)
		var out []string
		for _, line := range strings.Split(patch, "\n") {
			for _, pre := range []string{"*** Update File: ", "*** Add File: ", "*** Delete File: "} {
				if strings.HasPrefix(line, pre) {
					out = append(out, filepath.Clean(strings.TrimSpace(strings.TrimPrefix(line, pre))))
				}
			}
		}
		return out
	}
	return nil
}

func parseObject(raw json.RawMessage) map[string]any {
	var m map[string]any
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}

func canonicalJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if c, err := core.Canonical(raw); err == nil {
		return string(c)
	}
	return string(raw)
}

// squash collapses whitespace so a command matches however it was wrapped.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }
