// Package kv is Sleipnir's multi-layer prompt-cache engine.
//
// A provider's prompt cache is a byte-prefix match: the first changed byte
// invalidates everything after it. kv therefore orders every agent's prompt by
// volatility, most stable first, and treats each layer as an immutable,
// versioned value that is only ever replaced at a declared, priced boundary:
//
//	G0 constitution + tools   frozen for the session, identical for every agent
//	G1 shared pin             project knowledge, identical for every agent
//	G2 role pin               conventions shared by all agents of one role
//	G3 notes                  one agent's tenured facts/decisions
//	G4 spine                  one agent's append-only one-line history
//	G5 thread                 verbatim recent turns, append-only
//	G6 hot                    always-fresh board/mailbox view (see HotMode)
//
// Pins and spine are labelled context, not instructions: only G0 is rendered in
// the system role, so text distilled from untrusted tool output never gains
// operator authority.
//
// G6 is delivered one of three ways, chosen per route (ResolveHot): inline
// (appended after the last cache marker and rebuilt every request: cheap, but it
// rewrites a message an assistant turn was already produced against), persisted
// on change (a frozen notice block in the thread: append-only, safe for
// preserved thinking), or as a turn-scoped system message (persisted, cleared by
// the provider after its turn).
package kv

import (
	"sort"
	"strings"

	"github.com/reee344/sleipnir/internal/core"
)

// Kind names a layer's position in the stack.
type Kind uint8

const (
	KindConst Kind = iota
	KindShared
	KindRole
	KindNotes
	KindSpine
)

func (k Kind) String() string {
	switch k {
	case KindConst:
		return "const"
	case KindShared:
		return "shared"
	case KindRole:
		return "role"
	case KindNotes:
		return "notes"
	case KindSpine:
		return "spine"
	}
	return "?"
}

// Volatility orders segments inside a layer from least to most likely to
// change, so an edit lands as late in the layer as its change rate allows and
// invalidates as little of the layer as possible.
type Volatility uint8

const (
	VolFrozen Volatility = iota // never edited once written
	VolEpoch                    // edited only at declared epochs
	VolSlow                     // edited at major compactions
	VolFast                     // edited or appended at minor compactions
)

// Segment is a named block of layer text. Key identifies the segment across
// versions so edit operations can target it without positional guesswork.
type Segment struct {
	Key  string     `json:"key"`
	Text string     `json:"text"`
	Vol  Volatility `json:"vol"`
}

// Layer is an immutable, versioned run of segments. Version is bookkeeping for
// logs and compare-and-swap commits; it is deliberately absent from the
// rendered text, because putting it there would change the bytes (and so
// invalidate every cached prefix) on every commit even when content is equal.
type Layer struct {
	ID       string
	Kind     Kind
	Version  uint64
	Segments []Segment

	text string
	hash core.Hash
}

// NewLayer builds a layer, ordering segments by volatility (stable sort, so
// insertion order is preserved within a class).
func NewLayer(id string, kind Kind, version uint64, segs []Segment) *Layer {
	cp := append([]Segment(nil), segs...)
	sort.SliceStable(cp, func(i, j int) bool { return cp[i].Vol < cp[j].Vol })
	l := &Layer{ID: id, Kind: kind, Version: version, Segments: cp}
	l.text = renderSegments(kind, id, cp)
	l.hash = core.HashString(l.text)
	return l
}

// Text is the deterministic rendering of the layer.
func (l *Layer) Text() string {
	if l == nil {
		return ""
	}
	return l.text
}

// Hash is the content hash of Text. Equal hashes mean equal cached bytes.
func (l *Layer) Hash() core.Hash {
	if l == nil {
		return ""
	}
	return l.hash
}

// Empty reports whether the layer renders nothing.
func (l *Layer) Empty() bool { return l == nil || len(l.Segments) == 0 }

// Segment returns the segment with the given key.
func (l *Layer) Segment(key string) (Segment, bool) {
	if l == nil {
		return Segment{}, false
	}
	for _, s := range l.Segments {
		if s.Key == key {
			return s, true
		}
	}
	return Segment{}, false
}

// With returns a new layer at version+1 with the segments replaced.
func (l *Layer) With(segs []Segment) *Layer {
	return NewLayer(l.ID, l.Kind, l.Version+1, segs)
}

// Tokens estimates the layer's size.
func (l *Layer) Tokens(est core.Estimator) int {
	if l.Empty() {
		return 0
	}
	return est.Tokens(l.text)
}

// renderSegments produces the labelled text block for a pinned layer. The tag
// names double as instructions to the model about what the content is (see the
// constitution), so they are part of the protocol, not decoration.
func renderSegments(kind Kind, id string, segs []Segment) string {
	if len(segs) == 0 {
		return ""
	}
	if kind == KindConst {
		// The constitution is the system prompt proper: raw text, no wrapper.
		parts := make([]string, len(segs))
		for i, s := range segs {
			parts[i] = strings.TrimRight(s.Text, "\n")
		}
		return strings.Join(parts, "\n\n")
	}
	tag := tagFor(kind)
	var sb strings.Builder
	sb.WriteString("<")
	sb.WriteString(tag)
	sb.WriteString(">\n")
	for i, s := range segs {
		if i > 0 {
			sb.WriteString("\n")
		}
		if s.Key != "" {
			// A key is one line of header text: it may not carry a newline, a tag or a
			// marker, whoever chose it.
			sb.WriteString("## ")
			sb.WriteString(EscapeLine(s.Key, 64))
			sb.WriteString("\n")
		}
		sb.WriteString(strings.TrimRight(segmentText(kind, s.Text), "\n"))
		sb.WriteString("\n")
	}
	sb.WriteString("</")
	sb.WriteString(tag)
	sb.WriteString(">")
	return sb.String()
}

// segmentText is what a segment's text becomes inside its layer's frame. Whoever wrote
// it (a compactor, a tool, a peer, a repository file), it cannot close the frame or
// open another one: structural tags, harness markers and hidden characters are
// defused (see EscapeUntrusted). The private layers (notes, spine) also defuse lines
// that look like a section header, since their sections are "## key" blocks; the
// shared and role pins are markdown documents whose own headers must stay. Text with
// nothing to defuse renders byte for byte as written.
func segmentText(kind Kind, text string) string {
	if kind == KindNotes || kind == KindSpine {
		return EscapeUntrusted(text)
	}
	return escapeProtocol(text)
}

func tagFor(k Kind) string {
	switch k {
	case KindShared:
		return "shared-context"
	case KindRole:
		return "role-context"
	case KindNotes:
		return "my-notes"
	case KindSpine:
		return "history"
	}
	return "context"
}
