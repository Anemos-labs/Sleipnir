package reward

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

// Compaction fidelity probes. A compactor's job is to fold old turns into short
// digests without losing what the agent will need again: which files it edited,
// which command failed and how, which constant or message it saw. Probes turn
// that into a deterministic quiz with no model call:
//
//  1. take the main steps the compaction folded (the agent's steps since its
//     previous rebase, minus the newest ones the compactor kept verbatim);
//  2. derive facts from their tool calls and results: files written, commands
//     run (and whether they failed), failing test names and error messages, and
//     quoted constants and messages seen in tool output;
//  3. sample a few facts per compaction with a hash of (episode id, step id, fact),
//     so a policy cannot know in advance which ones it will be asked;
//  4. score the fraction still recoverable from the prompt of the first main
//     step after the rebase, by exact or normalised substring match.
//
// Guards against gaming: copying the whole thread scores full recall but is
// charged by the size component; facts that already appear in the agent's
// initial prompt (pins, task statement) are dropped because finding them proves
// nothing; and when a resolver is available only facts the compactor could
// actually see in its own prompt are asked about.

// PromptText resolves the exact text a step's model call saw (system blocks,
// pins, thread), for fidelity probes. Implementations expand the step's prompt
// manifest; the reward package itself never touches the blob store.
type PromptText func(ep *rl.Episode, st *rl.Step) (string, error)

// PromptSource may be implemented by a DiffSource that can also resolve prompt
// text, so a single blob-backed object serves both needs.
type PromptSource interface {
	PromptText(ep *rl.Episode, st *rl.Step) (string, error)
}

// ErrNoPromptText is returned by InlinePromptText for a step whose prompt was
// not inlined.
var ErrNoPromptText = errors.New("reward: step has no inlined prompt")

// InlinePromptText resolves prompt text from Step.Inline, for episodes exported
// with inlined prompts.
func InlinePromptText(_ *rl.Episode, st *rl.Step) (string, error) {
	if st == nil || st.Inline == nil {
		return "", ErrNoPromptText
	}
	return promptToText(st.Inline), nil
}

// promptToText flattens a prompt to searchable text: system blocks, then every
// message's text, tool calls (name and arguments) and tool results.
func promptToText(p *core.Prompt) string {
	var b strings.Builder
	var walk func(blocks []core.Block)
	walk = func(blocks []core.Block) {
		for _, blk := range blocks {
			switch blk.Kind {
			case core.BlockToolUse:
				b.WriteString(blk.ToolName)
				b.WriteByte(' ')
				b.Write(blk.Input)
				b.WriteByte('\n')
			case core.BlockToolResult:
				walk(blk.Result)
			default:
				b.WriteString(blk.Text)
				b.WriteByte('\n')
			}
		}
	}
	walk(p.System)
	for _, m := range p.Messages {
		walk(m.Blocks)
	}
	return b.String()
}

// Fact kinds.
const (
	FactFile    = "file"
	FactCommand = "command"
	FactTest    = "test"
	FactFailure = "failure"
	FactLiteral = "literal"
)

// Fact is one thing a good compaction should keep recoverable.
type Fact struct {
	Kind string `json:"kind"`
	// Text is the canonical form; Alts are other accepted renderings (a path's
	// basename, a command's first words).
	Text     string   `json:"text"`
	Alts     []string `json:"alts,omitempty"`
	Question string   `json:"question"`
	// Step is the id of the folded step the fact came from.
	Step string `json:"step"`
}

// Probe is the quiz for one compaction.
type Probe struct {
	Agent      string `json:"agent"`
	AgentIndex int    `json:"agent_index"`
	// Compactor is the id of the compactor step; StepIndex its index in the agent.
	Compactor string `json:"compactor"`
	StepIndex int    `json:"step_index"`
	// Next is the first main step after the rebase, whose prompt is searched; empty
	// when the run ended before the compaction took effect.
	Next      string `json:"next,omitempty"`
	NextIndex int    `json:"next_index"`
	Facts     []Fact `json:"facts"`
}

// ProbeResult is a scored probe.
type ProbeResult struct {
	Probe
	Found   []bool   `json:"found"`
	Missing []string `json:"missing,omitempty"`
	// Recall is the fraction of facts recovered; 1 when there was nothing to keep.
	Recall float64 `json:"recall"`
	// Scored is false when there was no next step to search.
	Scored bool `json:"scored"`
}

// Probes derives fidelity probes for every compactor step of the episode.
//
// resolve is optional. With it, facts are restricted to those visible in the
// compactor's own prompt and not already present in the agent's initial prompt;
// without it every extracted fact is kept.
func Probes(ep *rl.Episode, resolve PromptText) []Probe {
	return probes(ep, resolve, int(defaultCaps()[CapProbeFacts]))
}

func probes(ep *rl.Episode, resolve PromptText, perProbe int) []Probe {
	if ep == nil {
		return nil
	}
	var out []Probe
	for ai := range ep.Agents {
		a := &ep.Agents[ai]
		var initial string // agent's first main prompt, resolved lazily
		haveInitial := false
		for si := range a.Steps {
			st := &a.Steps[si]
			if stepRole(a, st) != rl.RoleCompactor {
				continue
			}
			p := Probe{Agent: a.ID, AgentIndex: ai, Compactor: st.ID, StepIndex: si, NextIndex: -1}
			if ni := nextRebase(ep, ai, si); ni >= 0 {
				p.Next, p.NextIndex = a.Steps[ni].ID, ni
			}
			window := foldedWindow(a, si)
			cands := extractFacts(ep, ai, window)
			// The filters read whole prompts, which can be megabytes, so they run lazily
			// on the few candidates the sampler reaches, not on all of them.
			var keep func(Fact) bool
			if resolve != nil {
				visible, haveVisible := "", false
				if text, err := resolve(ep, st); err == nil {
					visible, haveVisible = normalizeText(text), true
				}
				if !haveInitial {
					haveInitial = true
					if first := firstMain(a); first >= 0 {
						if text, err := resolve(ep, &a.Steps[first]); err == nil {
							initial = normalizeText(text)
						}
					}
				}
				ini := initial
				keep = func(f Fact) bool {
					if haveVisible && !factFound(visible, f) {
						return false // the compactor could not see it: not its fault
					}
					// Already in the agent's initial prompt: recovering it proves nothing.
					return ini == "" || !strings.Contains(ini, normalizeText(f.Text))
				}
			}
			p.Facts = sampleFactsIf(cands, ep.ID, st.ID, perProbe, keep)
			out = append(out, p)
		}
	}
	return out
}

// ScoreProbes scores probes against the prompts that followed each compaction.
func ScoreProbes(ep *rl.Episode, ps []Probe, resolve PromptText) ([]ProbeResult, error) {
	if ep == nil {
		return nil, errors.New("reward: nil episode")
	}
	if resolve == nil {
		return nil, errors.New("reward: ScoreProbes needs a PromptText resolver")
	}
	out := make([]ProbeResult, 0, len(ps))
	for _, p := range ps {
		r := ProbeResult{Probe: p, Found: make([]bool, len(p.Facts)), Recall: 1}
		if p.Next == "" || p.AgentIndex >= len(ep.Agents) || p.NextIndex < 0 || p.NextIndex >= len(ep.Agents[p.AgentIndex].Steps) {
			out = append(out, r)
			continue
		}
		st := &ep.Agents[p.AgentIndex].Steps[p.NextIndex]
		text, err := resolve(ep, st)
		if err != nil {
			return nil, fmt.Errorf("reward: probe %s: prompt of %s: %w", p.Compactor, p.Next, err)
		}
		norm := normalizeText(text)
		r.Scored = true
		hits := 0
		for i, f := range p.Facts {
			if factFound(norm, f) {
				r.Found[i] = true
				hits++
			} else {
				r.Missing = append(r.Missing, f.Text)
			}
		}
		if len(p.Facts) > 0 {
			r.Recall = float64(hits) / float64(len(p.Facts))
		}
		out = append(out, r)
	}
	return out, nil
}

// ---- windows and next steps ------------------------------------------------------------

func firstMain(a *rl.Agent) int {
	for i := range a.Steps {
		if isMain(&a.Steps[i]) {
			return i
		}
	}
	return -1
}

func prevMain(a *rl.Agent, si int) int {
	for i := si - 1; i >= 0; i-- {
		if isMain(&a.Steps[i]) {
			return i
		}
	}
	return -1
}

// nextRebase finds the first main step after the compaction took effect. The
// recorded compact edge wins; otherwise the first main step whose segment is
// beyond the segment the compactor ran in; otherwise the first main step whose
// prompt shrank.
func nextRebase(ep *rl.Episode, ai, si int) int {
	a := &ep.Agents[ai]
	id := a.Steps[si].ID
	for _, e := range ep.Edges {
		if e.Kind != rl.EdgeCompact || e.From != id {
			continue
		}
		for j := range a.Steps {
			if a.Steps[j].ID == e.To && j > si && isMain(&a.Steps[j]) {
				return j
			}
		}
	}
	ref := a.Steps[si].Segment
	epoch := a.Steps[si].Epoch
	if pm := prevMain(a, si); pm >= 0 {
		ref, epoch = a.Steps[pm].Segment, a.Steps[pm].Epoch
	}
	for j := si + 1; j < len(a.Steps); j++ {
		if isMain(&a.Steps[j]) && (a.Steps[j].Segment > ref || a.Steps[j].Epoch > epoch) {
			return j
		}
	}
	prev := -1
	if pm := prevMain(a, si); pm >= 0 {
		prev = promptTokens(&a.Steps[pm])
	}
	for j := si + 1; j < len(a.Steps); j++ {
		if !isMain(&a.Steps[j]) {
			continue
		}
		if p := promptTokens(&a.Steps[j]); prev > 0 && p > 0 && p < prev {
			return j
		}
		if p := promptTokens(&a.Steps[j]); p > 0 {
			prev = p
		}
	}
	return -1
}

var keepFromRe = regexp.MustCompile(`(?i)^\s*t?(\d+)\s*$`)

// compactionPatch is the part of a compactor's reply the scorer needs.
type compactionPatch struct {
	KeepFrom json.RawMessage `json:"keep_from"`
}

// parsePatch validates a compactor reply the way kv.ParsePatch does, without
// importing kv: the first balanced JSON object must carry a keep_from turn
// reference. It returns the turn id and a reason when the reply is unusable.
func parsePatch(reply string) (keepFrom int64, reason string) {
	obj, ok := firstJSONObject(reply)
	if !ok {
		return 0, "no JSON object in the reply"
	}
	var p compactionPatch
	if err := json.Unmarshal(obj, &p); err != nil {
		return 0, "reply is not valid JSON"
	}
	if len(p.KeepFrom) == 0 || string(p.KeepFrom) == "null" {
		return 0, "no keep_from"
	}
	var s string
	if err := json.Unmarshal(p.KeepFrom, &s); err != nil {
		var n int64
		if err := json.Unmarshal(p.KeepFrom, &n); err != nil {
			return 0, "keep_from is neither a string nor a number"
		}
		return n, ""
	}
	m := keepFromRe.FindStringSubmatch(s)
	if m == nil {
		return 0, "keep_from is not a turn reference"
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, "keep_from is out of range"
	}
	return n, ""
}

// firstJSONObject finds the first balanced {...} in s, skipping braces in strings
// (models wrap the object in prose or code fences).
func firstJSONObject(s string) ([]byte, bool) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return nil, false
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return []byte(s[start : i+1]), true
			}
		}
	}
	return nil, false
}

// foldedWindow lists the main steps a compaction at step si folded: the agent's
// steps since its previous rebase. When the compactor's keep_from and the steps'
// turn ids are known, exactly the steps before keep_from; otherwise all but the
// newest quarter, which compactors keep verbatim (facts there would be found
// trivially).
func foldedWindow(a *rl.Agent, si int) []int {
	pm := prevMain(a, si)
	if pm < 0 {
		return nil
	}
	seg, epoch := a.Steps[pm].Segment, a.Steps[pm].Epoch
	var idx []int
	for j := pm; j >= 0; j-- {
		st := &a.Steps[j]
		if !isMain(st) {
			continue
		}
		if st.Segment != seg || st.Epoch != epoch {
			break
		}
		idx = append(idx, j)
	}
	// idx is newest first; restore order.
	for i, j := 0, len(idx)-1; i < j; i, j = i+1, j-1 {
		idx[i], idx[j] = idx[j], idx[i]
	}
	if keep, reason := parsePatch(a.Steps[si].Completion.Turn.PlainText()); reason == "" {
		known := 0
		for _, j := range idx {
			if a.Steps[j].Completion.Turn.ID > 0 {
				known++
			}
		}
		if known == len(idx) && known > 0 {
			var folded []int
			for _, j := range idx {
				if int64(a.Steps[j].Completion.Turn.ID) < keep {
					folded = append(folded, j)
				}
			}
			return folded
		}
	}
	if n := len(idx); n >= 4 {
		return idx[:n-(n+3)/4]
	}
	return idx
}

// ---- fact extraction ---------------------------------------------------------------------

var (
	goFailRe     = regexp.MustCompile(`--- FAIL: ((?:Test|Benchmark|Example)[\p{L}\p{N}_/.\-]*)`)
	goErrLineRe  = regexp.MustCompile(`(?m)^\s*(\S+\.go:\d+(?::\d+)?): (.{6,140})$`)
	pyFailRe     = regexp.MustCompile(`FAILED ([\p{L}\p{N}_./\-]+)::([\p{L}\p{N}_\[\]:.\-]+)`)
	pyErrRe      = regexp.MustCompile(`(?m)^E\s+(\w*(?:Error|Exception)\b.{0,120})$`)
	jsFailRe     = regexp.MustCompile(`(?m)^\s*(?:●|✕|×)\s+(.{4,120})$`)
	panicRe      = regexp.MustCompile(`(?m)^(?:panic|fatal error): (.{4,140})$`)
	errMsgRe     = regexp.MustCompile(`(?m)(?:^|\s)(?:Error|error|ERROR|Exception|AssertionError|TypeError|ValueError): (.{6,140})$`)
	quotedRe     = regexp.MustCompile("\"([^\"\\n]{8,80})\"|'([^'\\n]{8,80})'|`([^`\\n]{8,80})`")
	exitCodeRe   = regexp.MustCompile(`\[exit code (-?\d+)\]\s*$`)
	failWordRe   = regexp.MustCompile(`(?m)^(?:--- FAIL|FAIL\b|FAILED|panic:|Traceback|npm ERR!|E {2,}|error(?:\[|:)|Error:)`)
	trivialCmdRe = regexp.MustCompile(`^(?:ls|cd|pwd|cat|echo|head|tail|wc|true|false|clear|which|date|sleep|git (?:status|diff|log|add)|mkdir|touch)\b`)
)

func normPath(p string) string {
	p = slashPath(p)
	for strings.HasPrefix(p, "./") {
		p = p[2:]
	}
	return p
}

func lastSegments(p string, n int) string {
	segs := splitSegs(p)
	if len(segs) <= n {
		return strings.Join(segs, "/")
	}
	return strings.Join(segs[len(segs)-n:], "/")
}

func baseOf(p string) string {
	segs := splitSegs(p)
	if len(segs) == 0 {
		return ""
	}
	return segs[len(segs)-1]
}

// commandCore reduces a shell command to the part a summary would name: the
// segment that runs a test/build/tool, stripped of "cd x &&" prefixes and of
// output plumbing.
func commandCore(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	parts := splitCommand(cmd)
	best := ""
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || strings.HasPrefix(p, "cd ") {
			continue
		}
		if testyCmdRe.MatchString(p) {
			best = p
			break
		}
		if best == "" {
			best = p
		}
	}
	if best == "" {
		best = cmd
	}
	best = strings.Join(stripRedirects(strings.Fields(best)), " ")
	r := []rune(best)
	if len(r) > 120 {
		best = string(r[:120])
	}
	return best
}

var redirectRe = regexp.MustCompile(`^\d*(?:[<>]{1,2}|&>)&?\S*$`)

// stripRedirects drops output plumbing ("2>&1", "> out.txt") from command words.
func stripRedirects(words []string) []string {
	var out []string
	for i := 0; i < len(words); i++ {
		w := words[i]
		if redirectRe.MatchString(w) {
			// A bare operator takes the next word as its target.
			if strings.Trim(w, "0123456789") == ">" || strings.Trim(w, "0123456789") == ">>" || w == "<" || w == "&>" {
				i++
			}
			continue
		}
		out = append(out, w)
	}
	return out
}

func splitCommand(cmd string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	rs := []rune(cmd)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case quote != 0:
			cur.WriteRune(r)
			if r == quote {
				quote = 0
			}
		case r == '\'' || r == '"':
			quote = r
			cur.WriteRune(r)
		case r == '&' && i+1 < len(rs) && rs[i+1] == '&', r == '|' && i+1 < len(rs) && rs[i+1] == '|':
			out = append(out, cur.String())
			cur.Reset()
			i++
		case r == ';' || r == '|' || r == '\n':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	out = append(out, cur.String())
	return out
}

func failed(c toolCall) bool {
	if c.isError {
		return true
	}
	out := c.output
	if len(out) > 1<<16 {
		out = out[len(out)-1<<16:]
	}
	if m := exitCodeRe.FindStringSubmatch(out); m != nil {
		return m[1] != "0"
	}
	return failWordRe.MatchString(out)
}

// extractFacts lists candidate facts from the folded steps, in a stable order.
func extractFacts(ep *rl.Episode, ai int, window []int) []Fact {
	a := &ep.Agents[ai]
	seen := map[string]bool{}
	var out []Fact
	add := func(f Fact) {
		key := f.Kind + "\x00" + normalizeText(f.Text)
		if f.Text == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, f)
	}
	lineCount := map[string]int{}
	type outputRef struct {
		step string
		text string
	}
	var outputs []outputRef
	for _, si := range window {
		st := &a.Steps[si]
		for _, c := range callsOf(ai, si, st) {
			switch {
			case c.isWrite() && !c.isError:
				for _, p := range writtenPaths(c) {
					p = normPath(p)
					if p == "" {
						continue
					}
					f := Fact{Kind: FactFile, Text: p, Step: st.ID, Question: fmt.Sprintf("Which file was modified? (%s)", p)}
					if s := lastSegments(p, 2); s != p {
						f.Alts = append(f.Alts, s)
					}
					if b := baseOf(p); len(b) >= 5 && b != p {
						f.Alts = append(f.Alts, b)
					}
					add(f)
				}
			case c.isShell():
				cmd := commandOf(c)
				cc := commandCore(cmd)
				if len(cc) >= 6 && !trivialCmdRe.MatchString(cc) {
					f := Fact{Kind: FactCommand, Text: cc, Step: st.ID}
					verdict := "ran"
					if failed(c) {
						verdict = "ran and failed"
					}
					f.Question = fmt.Sprintf("Which command was run? (%s: %s)", verdict, cc)
					if w := strings.Fields(cc); len(w) >= 3 {
						if three := strings.Join(w[:3], " "); len(three) >= 10 && three != cc {
							f.Alts = append(f.Alts, three)
						}
					}
					add(f)
				}
			}
			if c.output != "" {
				out := c.output
				if len(out) > 1<<18 {
					out = out[:1<<18]
				}
				outputs = append(outputs, outputRef{st.ID, out})
			}
		}
	}
	// Failures and literals from tool output.
	type cand struct {
		f     Fact
		count int
	}
	var lits []cand
	litIdx := map[string]int{}
	for _, o := range outputs {
		for _, line := range strings.Split(o.text, "\n") {
			if l := strings.TrimSpace(line); l != "" {
				lineCount[l]++
			}
		}
		for _, m := range goFailRe.FindAllStringSubmatch(o.text, 20) {
			add(Fact{Kind: FactTest, Text: m[1], Step: o.step, Question: "Which test failed? (" + m[1] + ")"})
		}
		for _, m := range pyFailRe.FindAllStringSubmatch(o.text, 20) {
			add(Fact{Kind: FactTest, Text: m[2], Step: o.step, Question: "Which test failed? (" + m[2] + ")"})
		}
		for _, m := range jsFailRe.FindAllStringSubmatch(o.text, 20) {
			add(Fact{Kind: FactTest, Text: strings.TrimSpace(m[1]), Step: o.step, Question: "Which test failed? (" + strings.TrimSpace(m[1]) + ")"})
		}
		for _, re := range []*regexp.Regexp{panicRe, pyErrRe} {
			for _, m := range re.FindAllStringSubmatch(o.text, 10) {
				add(Fact{Kind: FactFailure, Text: strings.TrimSpace(m[1]), Step: o.step, Question: "What was the error message? (" + clipText(strings.TrimSpace(m[1]), 60) + ")"})
			}
		}
		for _, m := range goErrLineRe.FindAllStringSubmatch(o.text, 10) {
			add(Fact{Kind: FactFailure, Text: strings.TrimSpace(m[2]), Step: o.step, Question: "What was the error at " + m[1] + "? (" + clipText(strings.TrimSpace(m[2]), 60) + ")"})
		}
		for _, m := range errMsgRe.FindAllStringSubmatch(o.text, 10) {
			add(Fact{Kind: FactFailure, Text: strings.TrimSpace(m[1]), Step: o.step, Question: "What was the error message? (" + clipText(strings.TrimSpace(m[1]), 60) + ")"})
		}
		for _, m := range quotedRe.FindAllStringSubmatch(o.text, 40) {
			q := m[1] + m[2] + m[3]
			if strings.Count(q, " ") < 1 && !strings.ContainsAny(q, "0123456789_-./") {
				continue // a bare identifier-ish word proves little
			}
			key := normalizeText(q)
			if i, ok := litIdx[key]; ok {
				lits[i].count++
				continue
			}
			litIdx[key] = len(lits)
			lits = append(lits, cand{Fact{Kind: FactLiteral, Text: q, Step: o.step, Question: "What was the quoted value or message? (" + clipText(q, 60) + ")"}, 1})
		}
	}
	// Distinctive literals first: those seen once, longest first.
	sort.SliceStable(lits, func(i, j int) bool {
		if (lits[i].count == 1) != (lits[j].count == 1) {
			return lits[i].count == 1
		}
		return len(lits[i].f.Text) > len(lits[j].f.Text)
	})
	for _, c := range lits {
		if lineCount[strings.TrimSpace(c.f.Text)] > 3 {
			continue // boilerplate repeated in many outputs
		}
		add(c.f)
	}
	return out
}

// maxFactChecks bounds how many candidates one probe tests against prompts. A
// test is a substring search through a prompt that may be megabytes long; with
// a healthy episode the first few candidates pass, and only a probe whose facts
// are mostly invisible to the compactor comes near the limit.
const maxFactChecks = 256

// sampleFacts picks up to k facts, spread over kinds, ordered by a hash of the
// episode id, the compactor step id and the fact: deterministic, but not
// predictable by a policy that sees neither id.
func sampleFacts(cands []Fact, epID, stepID string, k int) []Fact {
	return sampleFactsIf(cands, epID, stepID, k, nil)
}

// sampleFactsIf is sampleFacts over the candidates that pass keep (nil keeps
// all). The result is the same as filtering first and sampling after; keep is
// only called on the candidates the sampling reaches, at most maxFactChecks.
func sampleFactsIf(cands []Fact, epID, stepID string, k int, keep func(Fact) bool) []Fact {
	if k <= 0 || len(cands) == 0 {
		return nil
	}
	type keyed struct {
		f     Fact
		h     uint64
		state int8 // 0 untested, 1 passes, 2 fails
		taken bool
	}
	checks := 0
	pass := func(c *keyed) bool {
		if keep == nil {
			return true
		}
		if c.state == 0 {
			if checks >= maxFactChecks {
				return false // out of budget: treated as failing
			}
			checks++
			c.state = 2
			if keep(c.f) {
				c.state = 1
			}
		}
		return c.state == 1
	}
	byKind := map[string][]*keyed{}
	for _, f := range cands {
		sum := sha256.Sum256([]byte(epID + "\x00" + stepID + "\x00" + f.Kind + "\x00" + f.Text))
		byKind[f.Kind] = append(byKind[f.Kind], &keyed{f: f, h: binary.BigEndian.Uint64(sum[:8])})
	}
	less := func(a, b *keyed) bool {
		if a.h != b.h {
			return a.h < b.h
		}
		return a.f.Text < b.f.Text
	}
	for _, list := range byKind {
		l := list
		sort.Slice(l, func(i, j int) bool { return less(l[i], l[j]) })
	}
	quota := map[string]int{FactFile: 3, FactCommand: 2, FactTest: 1, FactFailure: 1, FactLiteral: 1}
	scale := func(q int) int { return max(1, (q*k+7)/8) }
	var out []Fact
	order := []string{FactFile, FactTest, FactFailure, FactCommand, FactLiteral}
	for _, kind := range order {
		got := 0
		for _, c := range byKind[kind] {
			if got >= scale(quota[kind]) || len(out) >= k {
				break
			}
			if pass(c) {
				out = append(out, c.f)
				c.taken = true
				got++
			}
		}
	}
	// Fill any remaining slots from what is left, by hash.
	if len(out) < k {
		var rest []*keyed
		for _, kind := range order {
			for _, c := range byKind[kind] {
				if !c.taken {
					rest = append(rest, c)
				}
			}
		}
		sort.Slice(rest, func(i, j int) bool { return less(rest[i], rest[j]) })
		for _, c := range rest {
			if len(out) >= k {
				break
			}
			if pass(c) {
				out = append(out, c.f)
			}
		}
	}
	return out
}

// ---- matching ------------------------------------------------------------------------------

// normalizeText canonicalises text for substring matching: invisible format
// characters removed, fullwidth forms mapped to ASCII, quotes and backticks
// dropped, path separators unified, case folded, whitespace collapsed.
func normalizeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := true
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Cf, r):
			continue
		case r >= 0xFF01 && r <= 0xFF5E:
			r -= 0xFEE0
		case r == '\\':
			r = '/'
		case r == '"' || r == '\'' || r == '`' || r == '‘' || r == '’' || r == '“' || r == '”':
			continue
		}
		if unicode.IsSpace(r) {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		space = false
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.TrimRight(b.String(), " ")
}

func factFound(normPrompt string, f Fact) bool {
	if t := normalizeText(f.Text); t != "" && strings.Contains(normPrompt, t) {
		return true
	}
	for _, alt := range f.Alts {
		if t := normalizeText(alt); t != "" && strings.Contains(normPrompt, t) {
			return true
		}
	}
	return false
}
