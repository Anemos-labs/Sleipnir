package kv

import (
	"fmt"
	"regexp"
	"strconv"

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

	// Target, when set by the harness (MechanicalPatch, never by a parsed reply), is the
	// size in tokens the retained thread must fit: Apply may then excerpt the
	// oversized tool results of the newest units, which a model-written patch can
	// never touch (S05, S06).
	Target int `json:"-"`

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
	// Unverified is set by Apply on every proposal it lets through: the text comes from
	// a model that may have been steered by whatever it read, and nothing has checked it
	// against the repository. It is never read from the compactor's reply.
	Unverified bool `json:"-"`
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
