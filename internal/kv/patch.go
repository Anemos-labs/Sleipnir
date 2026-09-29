package kv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/reee344/sleipnir/internal/core"
)

// Patch is a compactor's proposed edit to one agent's context.
//
// It is data, not code: the compactor (an LLM) emits JSON, the harness parses
// it, validates it against the agent's actual thread, and applies it
// atomically at a turn boundary. Nothing in a patch can delete history
// silently: every unit before KeepFrom is either summarised by a spine line
// or gets a mechanical one-liner, and the original turns stay in the archive.
type Patch struct {
	// KeepFrom is the first turn kept verbatim; everything older is folded
	// into the spine.
	KeepFrom core.TurnID `json:"-"`
	// Spine lists one-line digests of older runs of turns.
	Spine []SpineEntry `json:"-"`
	// Mask names tool results to replace with a placeholder (still recoverable
	// through recall).
	Mask []MaskRef `json:"-"`
	// Notes edits the agent's tenured notes (layer G3).
	Notes []NoteOp `json:"-"`
	// Promote proposes facts for the shared or role layer. Proposals are queued
	// for the curator; they never edit shared layers directly.
	Promote []Promotion `json:"-"`

	Warnings []string `json:"-"`
}

// SpineEntry digests turns From..To (inclusive) into one line.
type SpineEntry struct {
	From, To   core.TurnID
	Line       string
	Mechanical bool
}

// MaskRef addresses the Nth tool_result block of a turn.
type MaskRef struct {
	Turn  core.TurnID
	Index int
}

// NoteOp is one edit of a notes section.
type NoteOp struct {
	Op    string `json:"op"`              // add | set | replace | remove
	Key   string `json:"key"`             // section
	Match string `json:"match,omitempty"` // for replace/remove: substring identifying the line(s)
	Text  string `json:"text,omitempty"`
}

// Promotion proposes a fact for a shared layer.
type Promotion struct {
	Scope string `json:"scope"` // "shared" or "role"
	Key   string `json:"key"`
	Text  string `json:"text"`
}

// rawPatch is the wire form emitted by the compactor.
type rawPatch struct {
	KeepFrom string `json:"keep_from"`
	Spine    []struct {
		Turns string `json:"turns"`
		Line  string `json:"line"`
	} `json:"spine"`
	Mask    []string    `json:"mask"`
	Notes   []NoteOp    `json:"notes"`
	Promote []Promotion `json:"promote"`
}

var (
	turnRef  = regexp.MustCompile(`(?i)^\s*t?(\d+)\s*$`)
	rangeRef = regexp.MustCompile(`(?i)^\s*t?(\d+)\s*[-–—:]+\s*t?(\d+)\s*$`)
	maskRef  = regexp.MustCompile(`(?i)^\s*t?(\d+)\s*[.#:]\s*(\d+)\s*$`)
)

// ParseTurnID parses "t41" or "41".
func ParseTurnID(s string) (core.TurnID, error) {
	m := turnRef.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("bad turn id %q", s)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return core.TurnID(n), nil
}

func parseRange(s string) (from, to core.TurnID, err error) {
	if m := rangeRef.FindStringSubmatch(s); m != nil {
		a, _ := strconv.ParseInt(m[1], 10, 64)
		b, _ := strconv.ParseInt(m[2], 10, 64)
		return core.TurnID(a), core.TurnID(b), nil
	}
	id, err := ParseTurnID(s)
	return id, id, err
}

// ParsePatch extracts and validates the JSON object in a compactor's reply.
// Models wrap JSON in prose or code fences; the first complete top-level object
// is used. Structural problems are errors; questionable content becomes
// warnings so one bad entry does not discard an otherwise good patch.
func ParsePatch(reply string) (*Patch, error) {
	obj, err := firstJSONObject(reply)
	if err != nil {
		return nil, err
	}
	var rp rawPatch
	dec := json.NewDecoder(bytes.NewReader(obj))
	if err := dec.Decode(&rp); err != nil {
		return nil, fmt.Errorf("compaction patch is not valid JSON: %w", err)
	}
	p := &Patch{}
	if rp.KeepFrom == "" {
		return nil, fmt.Errorf("compaction patch has no keep_from")
	}
	kf, err := ParseTurnID(rp.KeepFrom)
	if err != nil {
		return nil, fmt.Errorf("keep_from: %w", err)
	}
	p.KeepFrom = kf
	for i, s := range rp.Spine {
		from, to, err := parseRange(s.Turns)
		if err != nil || to < from {
			p.Warnings = append(p.Warnings, fmt.Sprintf("spine[%d]: bad range %q ignored", i, s.Turns))
			continue
		}
		line := strings.TrimSpace(strings.ReplaceAll(s.Line, "\n", " "))
		if line == "" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("spine[%d]: empty line ignored", i))
			continue
		}
		p.Spine = append(p.Spine, SpineEntry{From: from, To: to, Line: line})
	}
	for i, m := range rp.Mask {
		mm := maskRef.FindStringSubmatch(m)
		if mm == nil {
			p.Warnings = append(p.Warnings, fmt.Sprintf("mask[%d]: bad ref %q ignored", i, m))
			continue
		}
		t, _ := strconv.ParseInt(mm[1], 10, 64)
		ix, _ := strconv.Atoi(mm[2])
		p.Mask = append(p.Mask, MaskRef{Turn: core.TurnID(t), Index: ix})
	}
	for i, n := range rp.Notes {
		n.Op = strings.ToLower(strings.TrimSpace(n.Op))
		switch n.Op {
		case "add", "set", "replace", "remove":
		default:
			p.Warnings = append(p.Warnings, fmt.Sprintf("notes[%d]: unknown op %q ignored", i, n.Op))
			continue
		}
		if strings.TrimSpace(n.Key) == "" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("notes[%d]: missing key ignored", i))
			continue
		}
		p.Notes = append(p.Notes, n)
	}
	for i, pr := range rp.Promote {
		pr.Scope = strings.ToLower(strings.TrimSpace(pr.Scope))
		if (pr.Scope != "shared" && pr.Scope != "role") || strings.TrimSpace(pr.Text) == "" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("promote[%d]: ignored", i))
			continue
		}
		p.Promote = append(p.Promote, pr)
	}
	return p, nil
}

// firstJSONObject finds the first balanced {...} in s, skipping braces inside
// strings.
func firstJSONObject(s string) ([]byte, error) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return nil, fmt.Errorf("no JSON object in compaction reply")
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
				return []byte(s[start : i+1]), nil
			}
		}
	}
	return nil, fmt.Errorf("unterminated JSON object in compaction reply")
}
