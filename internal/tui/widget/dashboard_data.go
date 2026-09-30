// What the swarm cockpit shows: the data model of Dashboard, small and documented. The maintainer adapts it from the session's
// events (the reducer of docs/UX.md); nothing here knows where the numbers come from.

package widget

import (
	"time"
)

// AgentRow is one agent of the swarm.
type AgentRow struct {
	// ID is the short name (m0, w1), Role the role's name (manager, backend) and RoleColor its colour, an index into
	// Palette.RoleColors.
	ID, Role  string
	RoleColor int
	State     AgentState
	// Doing is what it is doing in a few words: the command, the file, who it waits for.
	Doing string
	// Shared is how many tokens of the prefix the agent shares with the whole swarm and Own how many are its own (notes, thread):
	// the two parts of its prompt bar. Their sum is what the TOK column says.
	Shared, Own int
	// Hit is the share of its last request that was served from the cache, 0..1. A dark tail in the agent's bar is what it lost.
	Hit  float64
	Cost float64 // dollars spent so far
	// Lease is what it owns (orders/*).
	Lease string
	// Levels is the agent's activity for the gantt, a bucket a second, 0 to 8; Levels[i] is the bucket LevelsFrom+i (see Lane).
	Levels     []uint8
	LevelsFrom int
	Marks      []LaneMark
}

// Governor is the rate and budget governor of the swarm.
type Governor struct {
	// RPM is the requests a minute now and RPMLimit the most the provider allows (0 when unknown).
	RPM, RPMLimit int
	// Err429 is the rate-limit answers so far and Retries the retries.
	Err429, Retries int
	// Speedup is how many times faster than one agent alone the swarm is (0 when unknown).
	Speedup float64
}

// MailStats counts what the mail router did.
type MailStats struct {
	Routed, Delivered, Dup, Ignored int
}

// FeedKind is what sort of event a line of the live feed is.
type FeedKind uint8

const (
	// FeedInfo is an event without a mood (·).
	FeedInfo FeedKind = iota
	// FeedOK is something that went well (✓).
	FeedOK
	// FeedWarn is something that went badly (⚠).
	FeedWarn
	// FeedCompact is a compaction (◆).
	FeedCompact
	// FeedMail is mail (✉).
	FeedMail
)

// FeedLine is one line of the live feed.
type FeedLine struct {
	// At is when, since the start of the swarm.
	At time.Duration
	// Agent is who, in its role's colour.
	Agent string
	Color int
	Kind  FeedKind
	Text  string
	// Tail is the small print at the right edge (what it cost).
	Tail string
}

// DashboardData is everything the cockpit shows.
type DashboardData struct {
	// Title is the swarm's task.
	Title string
	// Elapsed is the wall time; Spend and Budget are in dollars (Budget 0 is no budget); HitRatio is the swarm's cache hit ratio.
	Elapsed       time.Duration
	Spend, Budget float64
	HitRatio      float64
	// Shared is the prefix every agent shares (G0 to G2), PrefixLeft how long the provider will still keep it of PrefixTTL.
	Shared                []Layer
	PrefixLeft, PrefixTTL time.Duration
	// Agents are in the order they are shown; with more than fit, the ones that need attention are kept.
	Agents []AgentRow
	// NowBucket is the newest bucket of the gantt (the agents' Levels are in buckets).
	NowBucket int
	Kanban    []KanbanCol
	Merge     []MergeItem
	Mail      []Mail
	MailStats MailStats
	Governor  Governor
	Feed      []FeedLine
	// NoAnim shows the standing horse instead of the galloping one.
	NoAnim bool
	// Hints are the keys shown in the bottom row; nil shows the keys of the design (a program shows the keys it has).
	Hints []Hint
	// Status is how the screen relates to the session ("● live", "▶ replay 4×", "⏸ paused"): the first thing in the title bar's
	// stats, the last to be dropped when the width is short.
	Status string
}
