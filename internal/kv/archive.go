package kv

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
)

// Archive keeps every turn an agent ever had, whether or not compaction has
// since folded it out of the prompt. This is what makes compaction reversible:
// a spine line points at turn ids, and recall pages the originals back in.
//
// Turns are stored content-addressed in the blob store and read back on demand. The
// in-memory index holds one small fixed-size entry per turn (id, role, blob
// reference, stored size and a capped search preview), never the turn: a long
// session keeps a few hundred bytes per turn in RAM however large its tool output
// was. The index is append-ordered (turn ids arrive ascending), so a Put is O(1);
// it is rebuilt by whoever replays the event log on resume, and released when its
// agent is retired (Release) or the session ends (Close).
type Archive struct {
	blobs events.Blobs

	mu      sync.RWMutex
	byAgent map[string]*agentIndex
}

// agentIndex is one agent's entries in ascending turn-id order.
type agentIndex struct {
	entries []archiveEntry
}

// archiveEntry is everything the archive keeps in memory about a turn.
type archiveEntry struct {
	id   core.TurnID
	ref  core.Hash // the stored turn in the blob store
	role core.Role
	// size is the length of the stored turn in bytes: what reading it costs, known
	// before it is read, so a Range can stay inside its budget without decoding.
	size    uint32
	preview string // lower-cased search text, capped and cut from the ends of the turn
}

const (
	// previewHead and previewTail are how many bytes of a turn's text (from its start
	// and from its end, where results and errors usually are) the search index keeps.
	previewHead = 320
	previewTail = 96

	// MaxRangeTurns and MaxRangeBytes bound what one Range call reads: a request for
	// "t1-t99999999" is answered with the first turns that fit, not with the whole
	// archive, and the caller is told there is more (RangeLimit).
	MaxRangeTurns = 200
	MaxRangeBytes = 1 << 20
)

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
	e := archiveEntry{id: t.ID, ref: ref, role: t.Role, size: sizeU32(len(raw)), preview: buildPreview(searchText(t))}
	a.mu.Lock()
	defer a.mu.Unlock()
	ix := a.byAgent[agent]
	if ix == nil {
		ix = &agentIndex{}
		a.byAgent[agent] = ix
	}
	n := len(ix.entries)
	if n == 0 || t.ID > ix.entries[n-1].id {
		ix.entries = append(ix.entries, e) // the ordinary case: ids arrive ascending
		return nil
	}
	pos := sort.Search(n, func(i int) bool { return ix.entries[i].id >= t.ID })
	if pos < n && ix.entries[pos].id == t.ID {
		ix.entries[pos] = e // idempotent re-put
		return nil
	}
	ix.entries = append(ix.entries, archiveEntry{})
	copy(ix.entries[pos+1:], ix.entries[pos:])
	ix.entries[pos] = e
	return nil
}

func sizeU32(n int) uint32 {
	if n < 0 {
		return 0
	}
	if n > 1<<32-1 {
		return 1<<32 - 1
	}
	return uint32(n)
}

// Release drops the index of one agent (a retired agent has no further use for it).
// The turns stay in the blob store; only the in-memory entries go. A Put for the same
// agent afterwards starts a new index, so a run that was still winding down when its
// agent was retired costs a few entries, not a leak.
func (a *Archive) Release(agent string) {
	a.mu.Lock()
	delete(a.byAgent, agent)
	a.mu.Unlock()
}

// Close drops every index, at the end of a session.
func (a *Archive) Close() {
	a.mu.Lock()
	a.byAgent = map[string]*agentIndex{}
	a.mu.Unlock()
}

// Range loads the turns from..to (inclusive) that exist for the agent, at most
// MaxRangeTurns of them and MaxRangeBytes of stored turn (always at least one), oldest
// first. A range that holds more is cut where the budget ends; use RangeLimit to be
// told.
func (a *Archive) Range(agent string, from, to core.TurnID) ([]core.Turn, error) {
	turns, _, err := a.RangeLimit(agent, from, to, MaxRangeTurns, MaxRangeBytes)
	return turns, err
}

// RangeLimit is Range with the caller's budget. It counts the stored size of each turn
// before reading it, so a request over a huge range reads only what fits: at most
// maxTurns turns and, after the first, no more than maxBytes bytes of stored turns
// (a value <= 0 means the package default). more is true when turns in the range
// were left out, and the next call should start after the last turn returned.
func (a *Archive) RangeLimit(agent string, from, to core.TurnID, maxTurns, maxBytes int) (turns []core.Turn, more bool, err error) {
	if maxTurns <= 0 {
		maxTurns = MaxRangeTurns
	}
	if maxBytes <= 0 {
		maxBytes = MaxRangeBytes
	}
	a.mu.RLock()
	ix := a.byAgent[agent]
	if ix == nil {
		a.mu.RUnlock()
		return nil, false, nil
	}
	var refs []core.Hash
	used := 0
	i := sort.Search(len(ix.entries), func(k int) bool { return ix.entries[k].id >= from })
	for ; i < len(ix.entries) && ix.entries[i].id <= to; i++ {
		e := ix.entries[i]
		if len(refs) >= maxTurns || (len(refs) > 0 && used+int(e.size) > maxBytes) {
			more = true
			break
		}
		refs = append(refs, e.ref)
		used += int(e.size)
	}
	a.mu.RUnlock()
	turns = make([]core.Turn, 0, len(refs))
	for _, r := range refs {
		raw, err := a.blobs.Get(r)
		if err != nil {
			return nil, false, err
		}
		var t core.Turn
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, false, err
		}
		turns = append(turns, t)
	}
	return turns, more, nil
}

// Hit is a search result.
type Hit struct {
	Turn    core.TurnID
	Score   int
	Snippet string
}

// Search finds archived turns mentioning the query terms, best first. It looks at the
// capped preview of each turn (its beginning and its end), not at the whole text.
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
	for _, e := range ix.entries {
		pv := e.preview
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
		for lo > 0 && !utf8.RuneStart(pv[lo]) {
			lo--
		}
		for hi < len(pv) && !utf8.RuneStart(pv[hi]) {
			hi++
		}
		hits = append(hits, Hit{Turn: e.id, Score: score, Snippet: strings.Join(strings.Fields(pv[lo:hi]), " ")})
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
		return len(ix.entries)
	}
	return 0
}

// buildPreview is the search text kept for a turn: the lower-cased, white-space
// collapsed beginning and end of its text, in an allocation of its own. Cutting a
// substring of the lower-cased text would keep the whole text alive behind it.
func buildPreview(full string) string {
	if len(full) <= 4*(previewHead+previewTail) {
		p := collapsed(full)
		if len(p) <= previewHead+previewTail {
			return strings.Clone(p)
		}
		return strings.Clone(headBytes(p, previewHead) + " … " + tailBytes(p, previewTail))
	}
	// Only the ends of a long text are looked at, with slack for the white space that
	// collapsing removes.
	head := collapsed(headBytes(full, 4*previewHead))
	tail := collapsed(tailBytes(full, 4*previewTail))
	return strings.Clone(headBytes(head, previewHead) + " … " + tailBytes(tail, previewTail))
}

// collapsed lower-cases s and folds every run of white space to one space.
func collapsed(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), " ") }

// headBytes returns at most the first n bytes of s, ending on a character boundary.
func headBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// tailBytes returns at most the last n bytes of s, starting on a character boundary.
func tailBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	from := len(s) - n
	for from < len(s) && !utf8.RuneStart(s[from]) {
		from++
	}
	return s[from:]
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

// clipper appends text to a builder up to a character budget and remembers that it
// ran out, so a huge turn is never rendered in full only to be cut afterwards.
type clipper struct {
	sb   strings.Builder
	max  int // 0: unbounded
	full bool
}

func (c *clipper) add(s string) {
	if c.full {
		return
	}
	if c.max > 0 && c.sb.Len()+len(s) > c.max {
		room := c.max - c.sb.Len()
		for room > 0 && room < len(s) && !utf8.RuneStart(s[room]) {
			room--
		}
		if room > 0 {
			c.sb.WriteString(s[:room])
		}
		c.full = true
		return
	}
	c.sb.WriteString(s)
}

// FormatTurns renders archived turns as text for the recall tool, within
// maxChars. Tool results are shown in full up to the budget; the model asked
// for these turns specifically.
func FormatTurns(turns []core.Turn, maxChars int) string {
	c := &clipper{max: maxChars}
	for _, t := range turns {
		c.add(fmt.Sprintf("── t%d %s ──\n", t.ID, t.Role))
		for _, b := range t.Blocks {
			switch b.Kind {
			case core.BlockText:
				c.add(b.Text)
				c.add("\n")
			case core.BlockToolUse:
				c.add(fmt.Sprintf("[call %s %s]\n", b.ToolName, string(b.Input)))
			case core.BlockToolResult:
				c.add("[result]\n")
				c.add(b.PlainText())
				c.add("\n")
			}
		}
		if c.full {
			return c.sb.String() + "\n… [truncated; narrow the turn range]"
		}
	}
	return c.sb.String()
}

// ErrNoSuchResult is returned by FormatResult for a tool-result index the turn does
// not have.
var ErrNoSuchResult = fmt.Errorf("no such tool result")

// FormatResult renders the idx-th tool result of an archived turn, the one a mask or
// excerpt placeholder names ("recall t13.7"), in full up to maxChars.
func FormatResult(t core.Turn, idx, maxChars int) (string, error) {
	n := 0
	for _, b := range t.Blocks {
		if b.Kind != core.BlockToolResult {
			continue
		}
		if n == idx {
			c := &clipper{max: maxChars}
			c.add(fmt.Sprintf("── t%d.%d [result] ──\n", t.ID, idx))
			c.add(b.PlainText())
			if c.full {
				return c.sb.String() + "\n… [truncated; the result is longer than the output limit]", nil
			}
			return c.sb.String(), nil
		}
		n++
	}
	return "", fmt.Errorf("%w: t%d has %d tool result(s)", ErrNoSuchResult, t.ID, n)
}
