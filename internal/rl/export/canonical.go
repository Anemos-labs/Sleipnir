package export

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

// The canonical format is the lossless archive: one rl.Episode per line, with
// rewards and every step. Prompts are either embedded (Inline: every step carries
// its full core.Prompt) or, by default, deduplicated: the unique tools, system
// blocks and messages of the whole export are written once as a segment table and
// each step references them by content hash, delta-encoded against the previous
// step of the same agent (base, keep, add). A swarm's prompts share almost
// everything, so the deduplicated form is roughly the size of the unique content.
// Expand turns a deduplicated export back into the inline form, byte for byte.

// canonEpisode is rl.Episode with steps that may carry segment references. The
// shadowing Agents field makes encoding/json use the wider agent and step types.
type canonEpisode struct {
	rl.Episode
	Agents []canonAgent `json:"agents"`
}

type canonAgent struct {
	rl.Agent
	Steps []canonStep `json:"steps"`
}

type canonStep struct {
	rl.Step
	Segments *promptSegments `json:"segments,omitempty"`
}

// promptSegments references a step's prompt in the segment table: the request's
// model, the tools blob and system blobs, and the message list as a delta against
// the previous step of the same agent (Base is that step's id; its first Keep
// messages are reused, Add follows).
type promptSegments struct {
	Model  string      `json:"model"`
	Tools  core.Hash   `json:"tools,omitempty"`
	System []core.Hash `json:"system,omitempty"`
	Base   string      `json:"base,omitempty"`
	Keep   int         `json:"keep,omitempty"`
	Add    []core.Hash `json:"add,omitempty"`
}

// segmentEntry is one line of the segment table.
type segmentEntry struct {
	Schema string          `json:"schema"`
	Hash   core.Hash       `json:"hash"`
	Kind   string          `json:"kind"` // message | tools | system
	Body   json.RawMessage `json:"body"`
}

// table accumulates the unique parts of every prompt of an export.
type table struct {
	entries map[core.Hash]segmentEntry
}

func newTable() *table { return &table{entries: map[core.Hash]segmentEntry{}} }

func (t *table) add(kind string, body []byte) core.Hash {
	h := core.HashBytes(body)
	if _, ok := t.entries[h]; !ok {
		t.entries[h] = segmentEntry{Schema: SchemaSegment, Hash: h, Kind: kind, Body: append(json.RawMessage(nil), body...)}
	}
	return h
}

func (t *table) write(w io.Writer) error {
	hs := make([]string, 0, len(t.entries))
	for h := range t.entries {
		hs = append(hs, string(h))
	}
	sort.Strings(hs)
	for _, h := range hs {
		if err := writeLine(w, t.entries[core.Hash(h)]); err != nil {
			return err
		}
	}
	return nil
}

func writeLine(w io.Writer, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func (x *exporter) writeCanonical(work []*workEpisode) error {
	tab := newTable()
	// Without a separate table writer the table leads the main stream, so the
	// episode lines are held until every prompt has been seen.
	var held bytes.Buffer
	sink := io.Writer(x.out)
	if x.o.Table == nil && !x.o.Inline {
		sink = &held
	}
	count := 0
	for _, we := range work {
		if x.o.MaxSamples > 0 && count >= x.o.MaxSamples {
			break
		}
		ce, err := x.canonOf(we, tab)
		if err != nil {
			return err
		}
		if err := writeLine(sink, ce); err != nil {
			return err
		}
		count++
		x.stats.Records++
		bump(&x.stats.ByUnit, "episode", 1)
		if s := x.splitOf(we.ep); s != "" {
			bump(&x.stats.BySplit, s, 1)
		}
		we.contributed = true
	}
	if x.o.Inline {
		return nil
	}
	if x.o.Table != nil {
		return tab.write(x.o.Table)
	}
	if err := tab.write(x.out); err != nil {
		return err
	}
	_, err := x.out.Write(held.Bytes())
	return err
}

// canonOf builds the canonical record of an episode: redacted, with prompts inline
// or referenced, and with token traces dropped from steps whose text redaction
// changed (the ids encode the original text).
func (x *exporter) canonOf(we *workEpisode, tab *table) (*canonEpisode, error) {
	ep := we.ep
	if x.red != nil {
		ep.Env.Repo, _ = x.redactText(ep.Env.Repo)
	}
	ce := &canonEpisode{Episode: *ep, Agents: make([]canonAgent, len(ep.Agents))}
	ce.Episode.Agents = nil
	for ai := range ep.Agents {
		ag := &ep.Agents[ai]
		ca := canonAgent{Agent: *ag, Steps: make([]canonStep, len(ag.Steps))}
		ca.Agent.Steps = nil
		var prevID string
		var prevMsgs []core.Hash
		for si := range ag.Steps {
			st := ag.Steps[si]
			c := stepCtx{we: we, ag: ag, ai: ai, st: &ag.Steps[si], si: si}
			cs := canonStep{Step: st}
			cs.Step.Inline = nil
			turn, tchanged := x.redactTurn(st.Completion.Turn)
			cs.Step.Completion.Turn = turn
			if len(st.Observations) > 0 && x.red != nil {
				obs := make([]rl.Observation, len(st.Observations))
				for i, o := range st.Observations {
					o.Output, _ = x.redactText(o.Output)
					o.Input, _ = x.redactRaw(o.Input)
					obs[i] = o
				}
				cs.Step.Observations = obs
			}
			p, err := x.promptFor(we, c.st)
			if err != nil {
				x.stats.drop("step:no_prompt")
				x.stats.warn(st.ID + ": " + err.Error())
				if tchanged {
					cs.Step.Tokens = nil
				}
				ca.Steps[si] = cs
				continue
			}
			rp, pchanged := x.redactPrompt(p)
			if (tchanged || pchanged) && cs.Step.Tokens != nil {
				cs.Step.Tokens = nil
				x.stats.drop("step:redacted_tokens")
			}
			if !st.Trainable {
				bump(&x.stats.TeacherUsed, "archived:"+st.Model, 1)
			}
			rp = normalizePrompt(rp)
			if x.o.Inline {
				cs.Step.Inline = rp
			} else {
				segs, msgs, err := x.segmentsOf(rp, tab, prevID, prevMsgs)
				if err != nil {
					return nil, err
				}
				cs.Segments = segs
				prevID, prevMsgs = st.ID, msgs
			}
			ca.Steps[si] = cs
		}
		ce.Agents[ai] = ca
	}
	return ce, nil
}

// normalizePrompt keeps exactly what the wire hash covers (model, tools, system,
// message roles and blocks): turn ids, breakpoints, cache keys and parameters are
// request mechanics, and dropping them makes the inline and the deduplicated forms
// of a prompt identical.
func normalizePrompt(p *core.Prompt) *core.Prompt {
	out := &core.Prompt{Model: p.Model, Tools: p.Tools, System: p.System, Messages: make([]core.Message, len(p.Messages))}
	for i, m := range p.Messages {
		out.Messages[i] = core.Message{Role: m.Role, Blocks: m.Blocks}
	}
	return out
}

// segmentsOf registers a prompt's parts in the table and returns its reference,
// delta-encoded against the previous step's message list.
func (x *exporter) segmentsOf(p *core.Prompt, tab *table, prevID string, prev []core.Hash) (*promptSegments, []core.Hash, error) {
	ref := &promptSegments{Model: p.Model}
	if len(p.Tools) > 0 {
		b, err := core.MarshalStable(p.Tools)
		if err != nil {
			return nil, nil, err
		}
		ref.Tools = tab.add("tools", b)
	}
	for _, blk := range p.System {
		b, err := core.MarshalStable(blk)
		if err != nil {
			return nil, nil, err
		}
		ref.System = append(ref.System, tab.add("system", b))
	}
	msgs := make([]core.Hash, len(p.Messages))
	for i, m := range p.Messages {
		b, err := messageBytes(m)
		if err != nil {
			return nil, nil, err
		}
		msgs[i] = tab.add("message", b)
	}
	keep := 0
	for keep < len(msgs) && keep < len(prev) && msgs[keep] == prev[keep] {
		keep++
	}
	if prevID != "" && keep > 0 {
		ref.Base, ref.Keep = prevID, keep
	} else {
		keep = 0
	}
	ref.Add = append([]core.Hash(nil), msgs[keep:]...)
	return ref, msgs, nil
}

// Expand reads a deduplicated canonical export (episode lines whose steps carry
// segment references) and writes the same episodes with every prompt inlined,
// byte for byte what Options.Inline would have produced. The segment table is read
// from table when given, else from the segment lines at the head of records. It
// returns the number of episodes written.
func Expand(w io.Writer, records io.Reader, table io.Reader) (int, error) {
	parts := map[core.Hash]segmentEntry{}
	readTable := func(line []byte) (bool, error) {
		var probe struct {
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			return false, err
		}
		if probe.Schema != SchemaSegment {
			return false, nil
		}
		var e segmentEntry
		if err := json.Unmarshal(line, &e); err != nil {
			return true, err
		}
		if core.HashBytes(e.Body) != e.Hash {
			// The body was written compact; a re-indented table is still valid if its
			// canonical form hashes right.
			if c, err := core.Canonical(e.Body); err != nil || core.HashBytes(c) != e.Hash {
				return true, fmt.Errorf("export: segment %s does not match its hash", e.Hash.Short())
			}
		}
		parts[e.Hash] = e
		return true, nil
	}
	if table != nil {
		if err := eachLine(table, func(line []byte) error {
			_, err := readTable(line)
			return err
		}); err != nil {
			return 0, fmt.Errorf("export: read segment table: %w", err)
		}
	}
	out := bufio.NewWriterSize(w, 1<<20)
	n := 0
	err := eachLine(records, func(line []byte) error {
		if isTable, err := readTable(line); err != nil {
			return err
		} else if isTable {
			return nil
		}
		var ce canonEpisode
		if err := json.Unmarshal(line, &ce); err != nil {
			return fmt.Errorf("episode %d: %w", n+1, err)
		}
		for ai := range ce.Agents {
			msgsOf := map[string][]core.Hash{}
			for si := range ce.Agents[ai].Steps {
				cs := &ce.Agents[ai].Steps[si]
				if cs.Segments == nil {
					continue
				}
				p, msgs, err := inflate(cs.Segments, parts, msgsOf)
				if err != nil {
					return fmt.Errorf("episode %s step %s: %w", ce.ID, cs.ID, err)
				}
				msgsOf[cs.ID] = msgs
				cs.Step.Inline, cs.Segments = p, nil
			}
		}
		n++
		return writeLine(out, &ce)
	})
	if err != nil {
		return n, err
	}
	return n, out.Flush()
}

// inflate rebuilds a step's prompt from its segment references.
func inflate(ref *promptSegments, parts map[core.Hash]segmentEntry, msgsOf map[string][]core.Hash) (*core.Prompt, []core.Hash, error) {
	p := &core.Prompt{Model: ref.Model}
	get := func(h core.Hash, kind string) (json.RawMessage, error) {
		e, ok := parts[h]
		if !ok || e.Kind != kind {
			return nil, fmt.Errorf("%s segment %s is not in the table", kind, h.Short())
		}
		return e.Body, nil
	}
	if ref.Tools != "" {
		b, err := get(ref.Tools, "tools")
		if err != nil {
			return nil, nil, err
		}
		if err := json.Unmarshal(b, &p.Tools); err != nil {
			return nil, nil, err
		}
	}
	for _, h := range ref.System {
		b, err := get(h, "system")
		if err != nil {
			return nil, nil, err
		}
		var blk core.Block
		if err := json.Unmarshal(b, &blk); err != nil {
			return nil, nil, err
		}
		p.System = append(p.System, blk)
	}
	var msgs []core.Hash
	if ref.Base != "" {
		base, ok := msgsOf[ref.Base]
		if !ok || ref.Keep > len(base) {
			return nil, nil, fmt.Errorf("base step %q is unknown or shorter than %d messages", ref.Base, ref.Keep)
		}
		msgs = append(msgs, base[:ref.Keep]...)
	}
	msgs = append(msgs, ref.Add...)
	for _, h := range msgs {
		b, err := get(h, "message")
		if err != nil {
			return nil, nil, err
		}
		var w wireMsg
		if err := json.Unmarshal(b, &w); err != nil {
			return nil, nil, err
		}
		p.Messages = append(p.Messages, core.Message{Role: w.Role, Blocks: w.Blocks})
	}
	return p, msgs, nil
}

// eachLine calls fn for every non-empty line of r, with no line length limit.
func eachLine(r io.Reader, fn func(line []byte) error) error {
	br := bufio.NewReaderSize(r, 1<<20)
	for {
		line, err := br.ReadBytes('\n')
		if t := bytes.TrimSpace(line); len(t) > 0 {
			if ferr := fn(t); ferr != nil {
				return ferr
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
