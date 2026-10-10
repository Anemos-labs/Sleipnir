// Package seam holds the Go interfaces between the builders of `sleipnir web`: the session host (cmd/sleipnir), the translator
// (internal/web/translate) and the route packages (internal/web/wsvc, settings, runner). internal/web itself knows nothing about
// sessions; these interfaces do, so they live here.
package seam

import (
	"context"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Topic is the hub topic of the page's stream: every tab's frames and the global ones (one connection per page).
const Topic = "ui"

// Host is the set of live tabs, as the route packages see it. cmd/sleipnir implements it (B1).
type Host interface {
	// Tabs lists the live tabs in strip order.
	Tabs() []wire.TabSummary
	// Tab finds a live tab by its id.
	Tab(id string) (Tab, bool)
	// Active is the tab the page should show first.
	Active() string
	// Projects lists the directories a new session may start in.
	Projects(ctx context.Context) []wire.Project
	// Questions lists the open questions of every tab, oldest first.
	Questions() []wire.OpenQuestion
	// Publish sends a frame to every page (the host's adapter onto web.Hub.Publish(Topic, ...)).
	Publish(f wire.Frame)
}

// Tab is one live tab: a hosted session and its journal. All methods are safe for concurrent use; methods that change the
// session run one at a time per tab (the host serialises them with the running turn as the TUI does).
type Tab interface {
	// Summary describes the tab.
	Summary() wire.TabSummary
	// Snapshot returns the tab's state for a late joiner.
	Snapshot(ctx context.Context) (wire.TabSnapshot, error)
	// Send delivers a message now, or queues it behind the running turn.
	Send(ctx context.Context, req wire.MessageRequest) (wire.SendResult, error)
	// Command runs a slash line the page has no handler for.
	Command(ctx context.Context, req wire.CommandRequest) (wire.CommandResult, error)
	// Steer tells the running turn of the manager something without stopping it.
	Steer(ctx context.Context, text string) error
	// Interrupt cancels the running turn (target "turn"); the goal pauses.
	Interrupt(ctx context.Context, target string) error
	// Rename changes the tab's name (kept in the session's sidecar).
	Rename(ctx context.Context, name string) error
	// Restart is the restart family (/new, /clear, /swarm, /restart, a team's model or role change).
	Restart(ctx context.Context, req wire.RestartRequest) error
	// Goal sets, pauses, resumes or clears the standing goal.
	Goal(ctx context.Context, req wire.GoalRequest) error
	// SetMode changes the permission mode (the route checked the confirmation for bypass and yolo).
	SetMode(ctx context.Context, req wire.ModeRequest) error
	// SetModel changes the manager's model or a role's.
	SetModel(ctx context.Context, req wire.ModelRequest) error
	// SetEffort changes the reasoning effort.
	SetEffort(ctx context.Context, req wire.EffortRequest) (wire.EffortResult, error)
	// SetBudget changes the budget.
	SetBudget(ctx context.Context, req wire.BudgetRequest) error
	// Compact folds the manager's thread now.
	Compact(ctx context.Context, req wire.CompactRequest) error
	// StageLaunch stages flags for the next start of the team.
	StageLaunch(ctx context.Context, p wire.LaunchPatch) error
	// AddRule adds a session rule (allow, deny, ask; "tests" expands).
	AddRule(ctx context.Context, req wire.RuleRequest) (wire.RuleResult, error)
	// RemoveRule removes a session rule.
	RemoveRule(ctx context.Context, req wire.RuleRequest) (wire.RuleResult, error)
	// Rules lists the rules in force for the tab with their origins (configuration and this session).
	Rules(ctx context.Context) ([]wire.Rule, error)
	// Slash lists the slash commands of the tab: the built-ins, then its custom commands, skills and MCP prompts.
	Slash(ctx context.Context) ([]wire.SlashEntry, error)
	// Access gives the workspace and settings routes the current harness session of the tab.
	Access() SessionAccess
}

// SessionAccess is what the workspace and settings routes may use of a tab's session. The session changes on a restart, so the
// routes ask for it each time and never keep it.
type SessionAccess interface {
	// TabID names the tab.
	TabID() string
	// Session returns the current harness session (nil while the tab restarts).
	Session() *session.Session
	// Busy says whether a turn runs or any agent of the team is working.
	Busy() bool
	// Exclusive runs fn while no turn starts and no command of the tab runs; it fails with busy when a turn is running.
	Exclusive(ctx context.Context, fn func(s *session.Session) error) error
	// Emit adds host-originated UI events to the tab's journal (acknowledgment rows, checkpoint updates).
	Emit(evs ...wire.Event)
	// Notify sends a note to the agents that touched files, as /rewind does after a restore.
	Notify(text string)
	// Meta patches the tab's meta and publishes it.
	Meta(p wire.MetaPatch)
}

// Translator turns one tab generation's harness activity into UI events, keeps them in the tab's journal and publishes them
// (B2). The host (B1) creates one per generation with translate.New and installs its sinks before session.New.
type Translator interface {
	// Sink is the agent.Sink to install as session.Options.Sink.
	Sink() agent.Sink
	// NewSink is the per-agent sink constructor for session.Options.NewSink.
	NewSink(agentID string) agent.Sink
	// Attach follows a session's log until the returned function is called; the history of an existing log (VOCAB.md section
	// 12) is translated first.
	Attach(log *events.Log, dir string) (detach func())
	// Emit appends host-originated events (stamped with the current session time) and publishes them.
	Emit(evs ...wire.Event)
	// Question records an open question and emits its ask event.
	Question(q wire.Question)
	// Answered closes a question and emits its answer event.
	Answered(a wire.Answer)
	// Roster is the tab's roster now.
	Roster() []wire.RosterEntry
	// Journal returns the keyframe and the retained events (both encoded), the last seq and the session time now.
	Journal() (keyframe, events []wire.Raw, seq uint64, now float64)
	// Now is the session time now, in seconds.
	Now() float64
	// Close stops the translator; later calls do nothing.
	Close()
}
