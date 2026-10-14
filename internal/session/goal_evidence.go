package session

import (
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
	Verdict string `json:"verdict"`
	Query   string `json:"query"`
	Record  uint64 `json:"record"`
	Offset  int    `json:"offset"`
}

// evidenceText extracts canonical tool evidence; it never relies on compacted
// agent context. Claims in assistant prose are deliberately excluded.
func evidenceText(e events.Event) string {
	switch e.Type {
	case events.TypeToolCall, events.TypeToolResult, events.TypeAgentSpawn, events.TypeAgentRestore, events.TypeTaskMerge, events.TypeMergeMerged, events.TypeMergeVerifyFail, events.TypeToolJob:
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

// goalEvidence pages or searches the durable log through a fixed cutoff. Search
// snippets include matches in the middle of long output, and record reads retain
// pagination rather than silently throwing away the rest of a result.
func (s *Session) goalEvidence(g *goal.State, cutoff uint64, req evidenceRequest) (string, error) {
	if req.Offset < 0 {
		return "offset must be nonnegative", nil
	}
	var out strings.Builder
	matched, shown := 0, 0
	err := events.Scan(filepath.Join(s.Dir, "events.jsonl"), func(e events.Event) error {
		if e.Seq <= g.StartSeq || e.Seq > cutoff || (req.Record != 0 && req.Record != e.Seq) {
			return nil
		}
		body := evidenceText(e)
		if body == "" {
			return nil
		}
		{
			// A spilled result retains its full content-addressed output alongside the
			// metadata. Only hashes from real tool.result records can be dereferenced.
			if e.Type == events.TypeToolResult {
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
		if req.Record != 0 {
			r := []rune(body)
			if req.Offset > len(r) {
				fmt.Fprintf(&out, "offset past end (%d runes)", len(r))
				return nil
			}
			end := req.Offset + min(8000, len(r)-req.Offset)
			fmt.Fprintf(&out, "record=%d agent=%s type=%s runes=%d:%d/%d\n%s", e.Seq, e.Agent, e.Type, req.Offset, end, len(r), string(r[req.Offset:end]))
			shown++
			return nil
		}
		folded := strings.ToLower(body)
		at := strings.Index(folded, strings.ToLower(req.Query))
		if at < 0 {
			return nil
		}
		matched++
		if matched <= req.Offset || shown >= 20 {
			return nil
		}
		// Slice by runes so a match next to Unicode cannot produce invalid UTF-8.
		start := max(0, len([]rune(folded[:at]))-80)
		r := []rune(body)
		snippet := string(r[start:min(start+300, len(r))])
		fmt.Fprintf(&out, "record=%d agent=%s type=%s %s\n", e.Seq, e.Agent, e.Type, strings.Join(strings.Fields(snippet), " "))
		shown++
		return nil
	})
	if err != nil {
		return "", err
	}
	if req.Record == 0 {
		fmt.Fprintf(&out, "matching records=%d shown=%d offset=%d; next offset=%d\n", matched, shown, req.Offset, req.Offset+shown)
	}
	if shown == 0 {
		out.WriteString("No matching evidence.\n")
	}
	return out.String(), nil
}

// goalEvidenceCutoff flushes pending results before establishing an immutable
// view. The marker also makes successive judgment progress measurable on resume.
func (s *Session) goalEvidenceCutoff() (uint64, error) {
	seq, err := s.Log.Emit("", "goal.evidence", nil)
	if err != nil {
		return 0, err
	}
	return seq, s.Log.Flush()
}
