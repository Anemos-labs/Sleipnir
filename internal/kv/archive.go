package kv

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
)

// Archive keeps every turn an agent ever had, whether or not compaction has
// since folded it out of the prompt. This is what makes compaction reversible:
// a spine line points at turn ids, and recall pages the originals back in.
//
// Turns are stored content-addressed in the blob store; the in-memory index is
// only a search preview and is rebuilt from the event log on resume.
type Archive struct {
	blobs events.Blobs

	mu      sync.RWMutex
	byAgent map[string]*agentIndex
}

type agentIndex struct {
	entries map[core.TurnID]archiveEntry
	ids     []core.TurnID // ascending
}

type archiveEntry struct {
	ref     core.Hash
	preview string // lower-cased plain text, capped
}

const previewCap = 4096

// NewArchive returns an archive over a blob store.
func NewArchive(b events.Blobs) *Archive {
	return &Archive{blobs: b, byAgent: map[string]*agentIndex{}}
}

// Put stores a turn.
func (a *Archive) Put(agent string, t core.Turn) error {
	raw, err := core.MarshalStable(t)
	if err != nil {
		return err
	}
	ref, err := a.blobs.Put(raw)
	if err != nil {
		return err
	}
	pv := strings.ToLower(searchText(t))
	if len(pv) > previewCap {
		pv = pv[:previewCap]
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	ix := a.byAgent[agent]
	if ix == nil {
		ix = &agentIndex{entries: map[core.TurnID]archiveEntry{}}
		a.byAgent[agent] = ix
	}
	if _, seen := ix.entries[t.ID]; !seen {
		ix.ids = append(ix.ids, t.ID)
		sort.Slice(ix.ids, func(i, j int) bool { return ix.ids[i] < ix.ids[j] })
	}
	ix.entries[t.ID] = archiveEntry{ref: ref, preview: pv}
	return nil
}

// Range loads turns from..to (inclusive) that exist for the agent.
func (a *Archive) Range(agent string, from, to core.TurnID) ([]core.Turn, error) {
	a.mu.RLock()
	ix := a.byAgent[agent]
	if ix == nil {
		a.mu.RUnlock()
		return nil, nil
	}
	var refs []core.Hash
	for _, id := range ix.ids {
		if id >= from && id <= to {
			refs = append(refs, ix.entries[id].ref)
		}
	}
	a.mu.RUnlock()
	out := make([]core.Turn, 0, len(refs))
	for _, r := range refs {
		raw, err := a.blobs.Get(r)
		if err != nil {
			return nil, err
		}
		var t core.Turn
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// Hit is a search result.
type Hit struct {
	Turn    core.TurnID
	Score   int
	Snippet string
}

// Search finds archived turns mentioning the query terms, best first.
func (a *Archive) Search(agent, query string, limit int) []Hit {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	ix := a.byAgent[agent]
	if ix == nil {
		return nil
	}
	var hits []Hit
	for _, id := range ix.ids {
		pv := ix.entries[id].preview
		score := 0
		first := -1
		for _, t := range terms {
			if i := strings.Index(pv, t); i >= 0 {
				score++
				if first < 0 || i < first {
					first = i
				}
			}
		}
		if score == 0 {
			continue
		}
		lo := first - 60
		if lo < 0 {
			lo = 0
		}
		hi := first + 140
		if hi > len(pv) {
			hi = len(pv)
		}
		hits = append(hits, Hit{Turn: id, Score: score, Snippet: strings.Join(strings.Fields(pv[lo:hi]), " ")})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Turn > hits[j].Turn // newer first on ties
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}

// Len reports how many turns are archived for an agent.
func (a *Archive) Len(agent string) int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if ix := a.byAgent[agent]; ix != nil {
		return len(ix.ids)
	}
	return 0
}

func searchText(t core.Turn) string {
	var sb strings.Builder
	for _, b := range t.Blocks {
		switch b.Kind {
		case core.BlockText, core.BlockThinking:
			sb.WriteString(b.Text)
		case core.BlockToolUse:
			sb.WriteString(b.ToolName)
			sb.WriteByte(' ')
			sb.Write(b.Input)
		case core.BlockToolResult:
			sb.WriteString(b.PlainText())
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// FormatTurns renders archived turns as text for the recall tool, within
// maxChars. Tool results are shown in full up to the budget; the model asked
// for these turns specifically.
func FormatTurns(turns []core.Turn, maxChars int) string {
	var sb strings.Builder
	for _, t := range turns {
		fmt.Fprintf(&sb, "── t%d %s ──\n", t.ID, t.Role)
		for _, b := range t.Blocks {
			switch b.Kind {
			case core.BlockText:
				sb.WriteString(b.Text)
				sb.WriteByte('\n')
			case core.BlockToolUse:
				fmt.Fprintf(&sb, "[call %s %s]\n", b.ToolName, string(b.Input))
			case core.BlockToolResult:
				fmt.Fprintf(&sb, "[result]\n%s\n", b.PlainText())
			}
		}
		if maxChars > 0 && sb.Len() > maxChars {
			s := sb.String()[:maxChars]
			return s + "\n… [truncated; narrow the turn range]"
		}
	}
	return sb.String()
}
