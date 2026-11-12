package scan

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

// A seam is where two parts of the code that different agents read meet: the edges the mappers recorded between one shard and
// another (or, in a review or a scan of part of the repository, between a shard and code outside every shard). A hunter reads
// one shard; a hole that lives in the contract between two (one side assumes a value is bounded, non-empty, escaped, already
// authorized, in one unit or encoding, and the other side does not give it) is in neither shard alone. So the harness turns
// the edges that cross shards into audit targets of their own, one per pair of sides, and holds the hunt open until each is
// checked by an agent who read both sides and quoted what each one assumes and gives.
//
// Seams are made from edges as they are recorded and kept in the ledger, so their ids never change. A ledger written before
// seams existed has none and needs none.

// Seam is the meeting of two sides: shards (S#), or a directory outside every shard ("dir/").
type Seam struct {
	ID string `json:"id"`
	A  string `json:"a"`
	B  string `json:"b"`
	// Edges are the recorded edges between the two sides, in the order they were recorded.
	Edges   []string `json:"edges"`
	Claimed string   `json:"claimed,omitempty"`
	// Checked is the note of the agent who checked the contract, with CheckedBy, and Contract the code it quoted from each side.
	// Late says the seam was made after the hunt was complete: listed, never due, so that a scan that ended stays ended.
	Late      bool   `json:"late,omitempty"`
	Checked   string `json:"checked,omitempty"`
	CheckedBy string `json:"checked_by,omitempty"`
	Contract  []Cite `json:"contract,omitempty"`
}

// minSeamNoteRunes is the shortest note that closes a seam: what each side assumes and gives, not "looks fine".
const minSeamNoteRunes = 80

// minSeamsDue is the fewest seams a scan checks when it has that many; past max(minSeamsDue, shards), the seams with the most
// edges are due and the rest are listed as optional.
const minSeamsDue = 3

// Seam returns the seam with this id, or nil.
func (r *Run) Seam(id string) *Seam {
	for i := range r.Seams {
		if strings.EqualFold(r.Seams[i].ID, id) {
			return &r.Seams[i]
		}
	}
	return nil
}

// endFile is the file an end of an edge names: a point's file, or the path of path/file.go:Name.
func (r *Run) endFile(end string) string {
	end = strings.TrimSpace(end)
	if isID(end, 'P') {
		if p := r.Point(end); p != nil {
			return p.File
		}
		return ""
	}
	f, _, _ := strings.Cut(end, ":")
	c, err := CleanPath(f)
	if err != nil {
		return ""
	}
	return c
}

// partial reports whether the scan's shards hold only part of the repository: a review, or a scan below a folder. Code outside
// every shard is then a side of a seam too.
func (r *Run) partial() bool { return r.IsReview() || r.Focus != "" }

// side is the side of a seam a file is on: its shard, or for a partial scan the directory of a file outside every shard that
// exists at the commit (exists says; nil when the caller checked it already).
func (r *Run) side(file string, exists func(string) bool) string {
	if file == "" {
		return ""
	}
	if s := r.ShardOf(file); s != "" {
		return s
	}
	if r.partial() && (exists == nil || exists(file)) {
		return dirSide(file)
	}
	return ""
}

func dirSide(file string) string {
	d := path.Dir(file)
	if d == "." {
		return "./"
	}
	return d + "/"
}

// edgeSides is the two sides an edge joins, ordered, or ok false when it stays on one side or an end cannot be placed.
func (r *Run) edgeSides(e Edge, exists func(string) bool) (a, b string, ok bool) {
	if e.Relation == "imports" { // an import says the code can meet, not that a value crosses
		return "", "", false
	}
	ff, tf := r.endFile(e.From), r.endFile(e.To)
	if ff == "" && e.Cite != nil {
		ff = e.Cite.File
	}
	a, b = r.side(ff, exists), r.side(tf, exists)
	if a == "" || b == "" || a == b {
		return "", "", false
	}
	sa, sb := isID(a, 'S'), isID(b, 'S')
	switch {
	case !sa && !sb: // two places outside every shard: nobody hunts either
		return "", "", false
	case !sa, sa && sb && a > b:
		a, b = b, a
	}
	return a, b, true
}

// addEdgeToSeams records a new edge in the seam of the two sides it joins, making the seam when it is the first.
func (r *Run) addEdgeToSeams(e Edge, exists func(string) bool) *Seam {
	a, b, ok := r.edgeSides(e, exists)
	if !ok {
		return nil
	}
	for i := range r.Seams {
		if s := &r.Seams[i]; s.A == a && s.B == b {
			s.Edges = append(s.Edges, e.ID)
			return s
		}
	}
	late := r.HuntClosed()
	r.Seams = append(r.Seams, Seam{ID: fmt.Sprintf("X%d", len(r.Seams)+1), A: a, B: b, Edges: []string{e.ID}, Late: late})
	return &r.Seams[len(r.Seams)-1]
}

// seamCap is how many seams a scan must check at most.
func (r *Run) seamCap() int { return max(minSeamsDue, len(r.Shards)) }

// SeamsDue is the seams the hunt must check: every one already checked, then those with the most edges, up to seamCap. The rest
// are optional: worth a look, never blocking.
func (r *Run) SeamsDue() map[string]bool {
	due := map[string]bool{}
	var open []*Seam
	for i := range r.Seams {
		switch s := &r.Seams[i]; {
		case s.Checked != "":
			due[s.ID] = true
		case !s.Late:
			open = append(open, s)
		}
	}
	sort.SliceStable(open, func(i, j int) bool { return len(open[i].Edges) > len(open[j].Edges) })
	for _, s := range open {
		if len(due) >= r.seamCap() {
			break
		}
		due[s.ID] = true
	}
	return due
}

// seamReady reports whether both shard sides of a seam are mapped, so that it can be checked with their maps.
func (r *Run) seamReady(s *Seam) bool {
	for _, side := range []string{s.A, s.B} {
		if sh := r.Shard(side); sh != nil && sh.Mapped == "" {
			return false
		}
	}
	return true
}

// sideName is a side as a person reads it: a shard with its first file, or a directory outside the shards.
func (r *Run) sideName(side string) string {
	if s := r.Shard(side); s != nil {
		return s.ID + " (" + shardSpan(*s) + ")"
	}
	return "`" + side + "` (outside the shards)"
}

// seams lists the seams, or claims one.
func (t *Tool) seams(in input, agent string) (string, error) {
	if id := strings.TrimSpace(in.Claim); id != "" {
		var out string
		_, err := t.store.Update(in.Scan, func(r *Run) error {
			s := r.Seam(id)
			if s == nil {
				return fmt.Errorf("no seam %q (seams lists them)", id)
			}
			if s.Checked != "" {
				return fmt.Errorf("%s is already checked by %s", s.ID, s.CheckedBy)
			}
			if !r.seamReady(s) {
				return fmt.Errorf("%s joins a shard that is not mapped yet: map %s and %s first", s.ID, s.A, s.B)
			}
			if s.Claimed != "" && s.Claimed != agent {
				return fmt.Errorf("%s is claimed by %s; take another", s.ID, s.Claimed)
			}
			s.Claimed = agent
			out = seamText(r, s)
			return nil
		})
		return out, err
	}
	return t.view(in, func(r *Run) string {
		if len(r.Seams) == 0 {
			return "No seams yet: a seam is made when an edge joins two shards (scan edge between code of different shards)."
		}
		due := r.SeamsDue()
		var b strings.Builder
		fmt.Fprintf(&b, "%-5s %-6s %-14s %s\n", "seam", "edges", "state", "sides")
		for i := range r.Seams {
			s := &r.Seams[i]
			if i < in.Offset {
				continue
			}
			if i-in.Offset == 60 {
				fmt.Fprintf(&b, "… %d more: offset %d\n", len(r.Seams)-i, i)
				break
			}
			state := "free"
			switch {
			case s.Checked != "":
				state = "done"
			case !r.seamReady(s):
				state = "unmapped"
			case s.Claimed != "":
				state = "check:" + s.Claimed
			}
			if !due[s.ID] {
				state += " (optional)"
			}
			fmt.Fprintf(&b, "%-5s %-6d %-14s %s ↔ %s\n", s.ID, len(s.Edges), clip(state, 24), r.sideName(s.A), r.sideName(s.B))
		}
		b.WriteString("Claim one with seams claim=X#, then close it with seam id=X#, a note and cites from both sides.")
		return b.String()
	})
}

// seamText is a seam as its checker gets it: the two sides, their maps and the edges between them.
func seamText(r *Run, s *Seam) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: where %s meets %s.", s.ID, r.sideName(s.A), r.sideName(s.B))
	if s.Claimed != "" {
		fmt.Fprintf(&b, " Claimed by %s.", s.Claimed)
	}
	b.WriteString("\n")
	for _, side := range []string{s.A, s.B} {
		if sh := r.Shard(side); sh != nil && sh.Mapped != "" {
			fmt.Fprintf(&b, "Map of %s: %s\n", sh.ID, sh.Mapped)
		}
	}
	b.WriteString("Edges:\n")
	for _, id := range s.Edges {
		for _, e := range r.Edges {
			if e.ID != id {
				continue
			}
			fmt.Fprintf(&b, "- %s: %s %s %s", e.ID, e.From, e.Relation, e.To)
			if e.Cite != nil {
				fmt.Fprintf(&b, " (%s:%d)", e.Cite.File, e.Cite.Line)
			}
			if e.Note != "" {
				fmt.Fprintf(&b, ": %s", oneLine(e.Note))
			}
			b.WriteString("\n")
		}
	}
	if s.Checked != "" {
		fmt.Fprintf(&b, "Checked by %s: %s\n", s.CheckedBy, s.Checked)
		for _, c := range s.Contract {
			fmt.Fprintf(&b, "- %s:%d %s\n", c.File, c.Line, oneLine(c.Quote))
		}
		return strings.TrimRight(b.String(), "\n")
	}
	b.WriteString("Read both sides of every edge. For each value that crosses, write down what the sender gives and what the receiver assumes: " +
		"its size and count bounds, empty, null, zero and sentinel values, units, encodings and escaping, ordering, error and partial results, " +
		"and who checks authorization. A hole is where one side assumes what the other does not give: file it as a lead with a trace through both sides. " +
		"Close the seam with seam id=" + s.ID + ", a note of the contract and what you found, and cites that quote each side.")
	return b.String()
}

// seam closes a seam: the contract between its sides, quoted from each.
func (t *Tool) seam(ctx context.Context, in input, agent string) (string, error) {
	note := strings.TrimSpace(in.Note)
	if len([]rune(note)) < minSeamNoteRunes {
		return "", fmt.Errorf("say what crosses the seam and what each side assumes and gives (sizes, empty and sentinel values, units, encodings, errors, who checks access), and the leads you filed (%d characters at least)", minSeamNoteRunes)
	}
	snap, err := t.snapshot(in)
	if err != nil {
		return "", err
	}
	s := snap.Seam(strings.TrimSpace(in.ID))
	if s == nil {
		return "", fmt.Errorf("no seam %q: name one (X#; seams lists them)", in.ID)
	}
	if !snap.seamReady(s) {
		return "", fmt.Errorf("%s joins a shard that is not mapped yet: map %s and %s first", s.ID, s.A, s.B)
	}
	if len(in.Cites) == 0 {
		return "", errors.New("cite the contract: at least one quote from each side (cites)")
	}
	var contract []Cite
	sides := map[string]bool{}
	for i, c := range in.Cites {
		cc, _, err := t.checked(ctx, snap, c)
		if err != nil {
			return "", fmt.Errorf("cite %d: %w", i+1, err)
		}
		side := snap.side(cc.File, nil)
		if side != s.A && side != s.B {
			return "", fmt.Errorf("cite %d: %s is on neither side of %s (%s, %s)", i+1, cc.File, s.ID, s.A, s.B)
		}
		sides[side] = true
		contract = append(contract, cc)
	}
	for _, side := range []string{s.A, s.B} {
		if !sides[side] {
			return "", fmt.Errorf("no cite is from %s: quote what that side assumes or gives too", snap.sideName(side))
		}
	}
	var out string
	_, err = t.store.Update(in.Scan, func(r *Run) error {
		cur := r.Seam(s.ID)
		if cur.Checked != "" {
			return fmt.Errorf("%s is already checked by %s", cur.ID, cur.CheckedBy)
		}
		if cur.Claimed != "" && cur.Claimed != agent {
			return fmt.Errorf("%s is claimed by %s", cur.ID, cur.Claimed)
		}
		cur.Checked, cur.CheckedBy, cur.Contract, cur.Claimed = note, agent, contract, ""
		out = fmt.Sprintf("%s checked (%d %s, %d %s quoted).", cur.ID, len(cur.Edges), plural(len(cur.Edges), "edge", "edges"), len(contract), plural(len(contract), "line", "lines"))
		return nil
	})
	return out, err
}
