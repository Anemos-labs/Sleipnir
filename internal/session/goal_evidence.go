package session

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

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
// agent context. Claims in assistant prose are deliberately excluded.
func evidenceText(e events.Event) string {
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
				out = append(out, fmt.Sprintf("tool result %s error=%v\n%s", b.ToolID, b.IsError, b.PlainText()))
			}
		}
		return strings.Join(out, "\n")
	}
	return ""
}

// goalEvidence pages or searches the durable log through a fixed cutoff. A search
// (an empty query pages every record) lists the newest matches first, so the last
// test run and the last smoke check are what the judge sees before older ones; an
// offset counts back from the newest. Search snippets include matches in the middle
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
	matched, shown, readRunes := 0, 0, 0
	// A search lists the newest matches first: the last offset+20 matches are kept as the log is read, in a ring.
	keep := req.Offset + 20
	var ring []string
	err := events.Scan(filepath.Join(s.Dir, "events.jsonl"), func(e events.Event) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.Seq <= g.StartSeq || e.Seq > cutoff || (reading && !wanted[e.Seq]) {
			return nil
		}
		body := evidenceText(e)
		if body == "" {
			return nil
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
			fmt.Fprintf(&out, "record=%d agent=%s type=%s runes=%d:%d/%d\n%s\n", e.Seq, e.Agent, e.Type, req.Offset, end, len(r), string(r[req.Offset:end]))
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
		matched++
		// Slice by runes so a match next to Unicode cannot produce invalid UTF-8.
		start := max(0, len([]rune(folded[:at]))-80)
		r := []rune(body)
		snippet := string(r[start:min(start+300, len(r))])
		line := fmt.Sprintf("record=%d agent=%s type=%s %s\n", e.Seq, e.Agent, e.Type, strings.Join(strings.Fields(snippet), " "))
		if i := (matched - 1) % keep; i == len(ring) {
			ring = append(ring, line)
		} else {
			ring[i] = line
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
		for back := req.Offset; back < matched && shown < 20; back++ {
			out.WriteString(ring[(matched-1-back)%keep])
			shown++
		}
		fmt.Fprintf(&out, "matching records=%d shown=%d offset=%d (newest first); next offset=%d\n", matched, shown, req.Offset, req.Offset+shown)
	}
	if shown == 0 {
		out.WriteString("No matching evidence.\n")
	}
	return out.String(), nil
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
