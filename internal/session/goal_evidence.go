package session

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/goal"
)

// evidenceRequest retrieves immutable session records without granting the judge
// executable tools. Offset pages either matching records or runes in one record.
type evidenceRequest struct {
	Verdict string   `json:"verdict"`
	Query   string   `json:"query"`
	Record  uint64   `json:"record"`
	Records []uint64 `json:"records"`
	Offset  int      `json:"offset"`
}

// evidenceText extracts canonical tool evidence; it never relies on compacted
// agent context. Claims in assistant prose are deliberately excluded. calls names
// the tool call each result answers (callLabel), so that a result is found by the
// command that produced it.
func evidenceText(e events.Event, calls map[string]string) string {
	switch e.Type {
	case "goal.evidence", "delivery.evidence", events.TypeToolCall, events.TypeToolResult, events.TypeAgentSpawn, events.TypeAgentRestore, events.TypeTaskMerge, events.TypeMergeMerged, events.TypeMergeVerifyFail, events.TypeToolJob:
		return string(e.Data)
	case events.TypeTurnAppend:
		var t core.Turn
		if json.Unmarshal(e.Data, &t) != nil {
			return ""
		}
		var out []string
		for _, b := range t.Blocks {
			if b.Kind == core.BlockToolResult {
				of := ""
				if c := calls[b.ToolID]; c != "" {
					of = " of " + c
				}
				out = append(out, fmt.Sprintf("tool result %s%s error=%v\n%s", b.ToolID, of, b.IsError, b.PlainText()))
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// callLabel is how a tool call is named beside its result: the tool and the start of its input, the command for a shell. A judge
// that searched for "make cross" found the calls that ran it but never their output, which is in another record, and read one
// call three times looking for it.
func callLabel(data []byte) (id, label string) {
	var c struct {
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(data, &c) != nil || c.ID == "" {
		return "", ""
	}
	in := string(c.Input)
	var shell struct {
		Command string `json:"command"`
	}
	if json.Unmarshal(c.Input, &shell) == nil && shell.Command != "" {
		in = shell.Command
	}
	in = strings.Join(strings.Fields(in), " ")
	if r := []rune(in); len(r) > 200 {
		in = string(r[:200]) + "…"
	}
	return c.ID, strings.TrimSpace(c.Name + " " + in)
}

// coordinationTools are the tools whose calls and results carry what agents wrote to each other (task text and evidence lines,
// mail, plans, notes) rather than what ran. A search lists them after tool output, which holds the proof of what they claim.
var coordinationTools = map[string]bool{"task": true, "pool": true, "mail": true, "wait": true, "spawn": true, "plan": true, "note": true, "memory": true}

// coordinationTool reports whether a call label (callLabel) or tool name names a coordination tool. The name is read as the agent
// dispatches it (agent.BareToolName: "functions.task", "'task'", "task<|channel|>…" are the task tool), and "task.get" is "task"'s.
func coordinationTool(label string) bool {
	name, _, _ := strings.Cut(strings.TrimSpace(label), " ")
	name, _, _ = strings.Cut(agent.BareToolName(name), ".")
	return coordinationTools[name]
}

// relayed reports whether a record holds only coordination tools' calls or results (coordinationTools).
func relayed(e events.Event, calls map[string]string) bool {
	switch e.Type {
	case events.TypeToolCall, events.TypeToolResult:
		var d struct {
			Name string `json:"name"`
		}
		return json.Unmarshal(e.Data, &d) == nil && coordinationTool(d.Name)
	case events.TypeTurnAppend:
		var t core.Turn
		if json.Unmarshal(e.Data, &t) != nil {
			return false
		}
		results := 0
		for _, b := range t.Blocks {
			if b.Kind == core.BlockToolResult {
				if !coordinationTool(calls[b.ToolID]) {
					return false
				}
				results++
			}
		}
		return results > 0
	}
	return false
}

// matches keeps the newest keep search matches of one kind as the log is read, in a ring.
type matches struct {
	n    int
	ring []string
}

// add records a match: n counts every match added, and ring keeps the newest keep of them (keep is positive).
func (m *matches) add(line string, keep int) {
	if i := m.n % keep; i == len(m.ring) {
		m.ring = append(m.ring, line)
	} else {
		m.ring[i] = line
	}
	m.n++
}

// newest is the match back places before the newest; back is below keep.
func (m *matches) newest(back, keep int) string { return m.ring[(m.n-1-back)%keep] }

// resultMark stands in a shown tool call's line for the record of its result, which the log holds later; goalEvidence fills it
// in once the scan has passed it.
const resultMark = "\x00result:"

// goalEvidence pages or searches the durable log through a fixed cutoff. A search
// (an empty query pages every record) lists tool output before coordination tools'
// records (coordinationTools), each newest first, so the last test run and the last
// smoke check are what the judge sees before older ones and before the task lines
// that claim them; an offset counts down that order. Search snippets include matches in the middle
// of long output, and record reads retain pagination rather than silently throwing
// away the rest of a result.
func (s *Session) goalEvidence(ctx context.Context, g *goal.State, cutoff uint64, req evidenceRequest) (string, error) {
	if g == nil || g.StartSeq == 0 {
		return "", fmt.Errorf("goal has no durable evidence boundary; save it before starting work")
	}
	if req.Offset < 0 {
		return "offset must be nonnegative", nil
	}
	if len(req.Records) > 16 {
		return "request at most 16 records at a time", nil
	}
	wanted := map[uint64]bool{}
	var ordered []uint64
	found := map[uint64]bool{}
	if req.Record != 0 {
		wanted[req.Record] = true
		ordered = append(ordered, req.Record)
	}
	for _, id := range req.Records {
		if id == 0 {
			return "record IDs must be positive", nil
		}
		if !wanted[id] {
			ordered = append(ordered, id)
		}
		wanted[id] = true
	}
	reading := len(wanted) > 0
	var out strings.Builder
	shown, readRunes := 0, 0
	// A search lists tool output, then coordination: the last offset+20 matches of each are kept as the log is read.
	keep := req.Offset + 20
	var output, relay matches
	calls := map[string]string{}   // tool call id -> callLabel
	pending := map[string]bool{}   // the calls shown, whose result records are still to be found
	results := map[string]uint64{} // a shown call's id -> the record of its result
	err := events.Scan(filepath.Join(s.Dir, "events.jsonl"), func(e events.Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.Seq > cutoff {
			return nil
		}
		callID := ""
		switch e.Type {
		case events.TypeToolCall:
			var label string
			if callID, label = callLabel(e.Data); callID != "" {
				calls[callID] = label
			}
		case events.TypeTurnAppend:
			if len(pending) > 0 {
				var t core.Turn
				if json.Unmarshal(e.Data, &t) == nil {
					for _, b := range t.Blocks {
						if b.Kind == core.BlockToolResult && pending[b.ToolID] {
							delete(pending, b.ToolID)
							results[b.ToolID] = e.Seq
						}
					}
				}
			}
		}
		if e.Seq <= g.StartSeq || (reading && !wanted[e.Seq]) {
			return nil
		}
		body := evidenceText(e, calls)
		if body == "" {
			return nil
		}
		mark := ""
		if callID != "" {
			pending[callID] = true
			mark = resultMark + callID + "\x00"
		}
		{
			// A spilled result retains its full content-addressed output alongside the
			// metadata. Only hashes from real tool.result records can be dereferenced.
			if e.Type == events.TypeToolResult || e.Type == "delivery.evidence" {
				var d struct {
					Ref core.Hash `json:"ref"`
				}
				if json.Unmarshal(e.Data, &d) == nil && d.Ref != "" {
					if blobs, ok := s.Blobs.(interface {
						GetMax(core.Hash, int64) ([]byte, error)
					}); ok {
						b, err := blobs.GetMax(d.Ref, 8<<20)
						if err != nil {
							body += "\nFull output unavailable: " + err.Error()
						} else {
							body += "\n" + string(b)
						}
					}
				}
			}
		}
		if reading {
			found[e.Seq] = true
			r := []rune(body)
			if req.Offset > len(r) {
				fmt.Fprintf(&out, "record=%d offset past end (%d runes)\n", e.Seq, len(r))
				return nil
			}
			end := req.Offset + min(8000, len(r)-req.Offset)
			if readRunes+end-req.Offset > 24000 {
				fmt.Fprintf(&out, "record=%d omitted from this batch budget; read it in another request\n", e.Seq)
				return nil
			}
			readRunes += end - req.Offset
			fmt.Fprintf(&out, "record=%d agent=%s type=%s%s runes=%d:%d/%d\n%s\n", e.Seq, e.Agent, e.Type, mark, req.Offset, end, len(r), string(r[req.Offset:end]))
			shown++
			return nil
		}
		if e.Type == "goal.evidence" {
			return nil // an earlier judgment's roster; the current one comes with the request
		}
		folded := strings.ToLower(body)
		at := strings.Index(folded, strings.ToLower(req.Query))
		if at < 0 {
			return nil
		}
		// Slice by runes so a match next to Unicode cannot produce invalid UTF-8.
		start := max(0, len([]rune(folded[:at]))-80)
		r := []rune(body)
		snippet := string(r[start:min(start+300, len(r))])
		line := fmt.Sprintf("record=%d agent=%s type=%s%s %s\n", e.Seq, e.Agent, e.Type, mark, strings.Join(strings.Fields(snippet), " "))
		if relayed(e, calls) {
			relay.add(line, keep)
		} else {
			output.add(line, keep)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	for _, id := range ordered {
		if !found[id] {
			fmt.Fprintf(&out, "record=%d is not available within this objective and cutoff\n", id)
		}
	}
	if !reading {
		for back := req.Offset; back < output.n+relay.n && shown < 20; back++ {
			if back < output.n {
				out.WriteString(output.newest(back, keep))
			} else {
				out.WriteString(relay.newest(back-output.n, keep))
			}
			shown++
		}
		fmt.Fprintf(&out, "matching records=%d (%d tool output, then %d of coordination tools: task, pool, mail, plan…) shown=%d offset=%d (each newest first); next offset=%d\n",
			output.n+relay.n, output.n, relay.n, shown, req.Offset, req.Offset+shown)
	}
	if shown == 0 {
		out.WriteString("No matching evidence.\n")
	}
	return fillResultMarks(out.String(), results), nil
}

// fillResultMarks replaces each resultMark with the record of that call's result, or says the result is not in the archive.
func fillResultMarks(text string, results map[string]uint64) string {
	var b strings.Builder
	for {
		i := strings.Index(text, resultMark)
		if i < 0 {
			return b.String() + text
		}
		b.WriteString(text[:i])
		rest := text[i+len(resultMark):]
		j := strings.IndexByte(rest, 0)
		if j < 0 {
			return b.String() + rest
		}
		if seq, ok := results[rest[:j]]; ok {
			fmt.Fprintf(&b, " (its result: record=%d)", seq)
		} else {
			b.WriteString(" (no result within the cutoff)")
		}
		text = rest[j+1:]
	}
}

// goalEvidenceCutoff flushes pending results before establishing an immutable
// view. The marker also makes successive judgment progress measurable on resume.
func (s *Session) goalEvidenceCutoff() (uint64, error) {
	seq, err := s.Log.Emit("", "goal.evidence", map[string]any{"agents": s.goalRuntimeRoster()})
	if err != nil {
		return 0, err
	}
	return seq, s.Log.Flush()
}

// goalAgentIdentity records current harness identities without copying workers' conversations.
type goalAgentIdentity struct {
	ID    string `json:"id"`
	Role  string `json:"role"`
	Model string `json:"model"`
}

// goalRuntimeRoster supplies current model assignments even when the agents were
// created before this objective. Model labels are requested identities, not proof
// of implementation correctness or of a provider's internal routing.
func (s *Session) goalRuntimeRoster() []goalAgentIdentity {
	var roster []goalAgentIdentity
	if s.Swarm != nil {
		snapshot := s.Swarm.Board.Snapshot()
		for _, a := range s.Swarm.LiveAgents() {
			info, _ := snapshot.Agent(a.ID())
			roster = append(roster, goalAgentIdentity{a.ID(), info.Role, a.ModelID()})
		}
	} else if a := s.Main(); a != nil {
		roster = append(roster, goalAgentIdentity{a.ID(), "solo", a.ModelID()})
	}
	return roster
}
