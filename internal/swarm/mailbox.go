package swarm

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/events"
)

// Message is one mail between agents.
type Message struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
	// Kind types the message so recipients know what is expected of them:
	// info (no reply needed), request, blocker, answer, contract (an interface
	// changed).
	Kind string `json:"kind,omitempty"`
	Text string `json:"text"`
}

// Format renders the message as the text delivered into the recipient's thread.
// The format is fixed and short: it lands in cached history and is later
// compacted like any other turn.
func (m Message) Format() string {
	if m.Kind == "" || m.Kind == "info" {
		return fmt.Sprintf("[mail %s from %s] %s", m.ID, m.From, m.Text)
	}
	return fmt.Sprintf("[mail %s %s from %s] %s", m.ID, m.Kind, m.From, m.Text)
}

// Kinds lists the valid message kinds.
var Kinds = []string{"info", "request", "blocker", "answer", "contract"}

func validKind(k string) bool {
	for _, x := range Kinds {
		if x == k {
			return true
		}
	}
	return false
}

// RouterConfig bounds messaging so 50 agents cannot talk each other into a
// storm. Agents never write to each other's context directly: everything goes
// through the router, which validates, rate-limits, dedupes and audits.
type RouterConfig struct {
	MaxPerMinute     int           // per sender
	MaxPerPairPerMin int           // per sender->recipient
	MaxChars         int           // per message
	DedupeWindow     time.Duration // identical text to the same recipient
}

// DefaultRouterConfig returns conservative limits.
func DefaultRouterConfig() RouterConfig {
	return RouterConfig{MaxPerMinute: 8, MaxPerPairPerMin: 3, MaxChars: 600, DedupeWindow: 5 * time.Minute}
}

// Router validates and delivers mail (the Bifröst).
type Router struct {
	cfg     RouterConfig
	now     func() time.Time
	ev      events.Emitter
	roster  func() []string
	manager func() string
	deliver func(m Message)

	mu     sync.Mutex
	seq    int
	sender map[string][]time.Time
	pair   map[string][]time.Time
	recent map[string]time.Time
}

// NewRouter builds a router. roster lists valid recipient ids; deliver hands an
// accepted message to the runtime.
func NewRouter(cfg RouterConfig, ev events.Emitter, roster func() []string, manager func() string, deliver func(Message)) *Router {
	if ev == nil {
		ev = events.Discard{}
	}
	return &Router{cfg: cfg, now: time.Now, ev: ev, roster: roster, manager: manager, deliver: deliver,
		sender: map[string][]time.Time{}, pair: map[string][]time.Time{}, recent: map[string]time.Time{}}
}

func prune(ts []time.Time, cutoff time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}

// Send validates and routes a message. Errors are written for the sending
// model: they say what to do instead.
func (r *Router) Send(from, to, kind, text string) (Message, error) {
	text = strings.TrimSpace(text)
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "" {
		kind = "info"
	}
	if !validKind(kind) {
		return Message{}, fmt.Errorf("kind must be one of %s", strings.Join(Kinds, ", "))
	}
	to = strings.TrimSpace(to)
	if text == "" {
		return Message{}, fmt.Errorf("empty message")
	}
	switch strings.ToLower(to) {
	case "all", "*", "everyone", "everybody":
		return Message{}, fmt.Errorf("no broadcast: mail one agent, or use note for a fact every agent should know")
	case "manager", "lead":
		if m := r.manager(); m != "" {
			to = m
		}
	}
	if to == from {
		return Message{}, fmt.Errorf("you cannot mail yourself")
	}
	known := false
	ids := r.roster()
	for _, id := range ids {
		if id == to {
			known = true
		}
	}
	if !known {
		return Message{}, fmt.Errorf("no agent %q (agents: %s)", to, strings.Join(ids, ", "))
	}
	if n := len([]rune(text)); n > r.cfg.MaxChars {
		return Message{}, fmt.Errorf("message too long (%d chars, max %d): send the essential fact only", n, r.cfg.MaxChars)
	}
	now := r.now()
	r.mu.Lock()
	r.sender[from] = prune(r.sender[from], now.Add(-time.Minute))
	pk := from + "\x00" + to
	r.pair[pk] = prune(r.pair[pk], now.Add(-time.Minute))
	dk := pk + "\x00" + text
	if t, ok := r.recent[dk]; ok && now.Sub(t) < r.cfg.DedupeWindow {
		r.mu.Unlock()
		return Message{}, fmt.Errorf("you already sent %s exactly this message; wait for a reply instead of repeating it", to)
	}
	if len(r.sender[from]) >= r.cfg.MaxPerMinute {
		r.mu.Unlock()
		return Message{}, fmt.Errorf("too many messages this minute; combine them into one")
	}
	if len(r.pair[pk]) >= r.cfg.MaxPerPairPerMin {
		r.mu.Unlock()
		return Message{}, fmt.Errorf("you have mailed %s several times this minute; wait for their reply", to)
	}
	r.sender[from] = append(r.sender[from], now)
	r.pair[pk] = append(r.pair[pk], now)
	r.recent[dk] = now
	r.seq++
	m := Message{ID: fmt.Sprintf("m%d", r.seq), From: from, To: to, Kind: kind, Text: text}
	r.mu.Unlock()

	_, _ = r.ev.Emit(from, events.TypeMailSend, m)
	r.deliver(m)
	_, _ = r.ev.Emit(to, events.TypeMailDeliver, map[string]any{"id": m.ID, "from": from})
	return m, nil
}
