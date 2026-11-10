package webtest

import (
	"github.com/anemos-labs/sleipnir/internal/plugin"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Memory is the Memory page of the canned tab: a few notes of the user and of the project, the lore of two roles, the
// instruction files a session of the project reads (one of them not written yet) and two lessons.
func Memory() wire.MemoryView {
	return wire.MemoryView{
		Notes: wire.MemoryNotes{Path: "~/.sleipnir/MEMORY.md", Version: "n1", On: true, Max: 40, MaxLen: 400, Notes: []string{
			"Prefer table-driven tests in Go packages; one case per line.",
			"Run `go test ./...` before handing in a task that touched Go code.",
		}},
		Project: &wire.MemoryNotes{Path: ".sleipnir/MEMORY.local.md", Version: "p1", On: true, Max: 40, MaxLen: 400, Notes: []string{
			"The catalogue API pages with ?page=&size=; size is at most 50.",
			"web/ uses vitest; its tests live beside the components as *.test.tsx.",
		}},
		Lore: &wire.MemoryLore{Dir: "~/.sleipnir/state/shop/lore", On: true, Roles: []wire.MemoryLoreRole{
			{Role: "backend", Path: "backend.md", Items: []wire.MemoryLoreItem{
				{Stage: "canon", Confirmed: 3, Text: "Handlers return wire types; never marshal a map."},
				{Stage: "insight", Confirmed: 1, Text: "The seed data loads from api/seed/items.json at start."},
			}},
			{Role: "frontend", Path: "frontend.md", Items: []wire.MemoryLoreItem{
				{Stage: "insight", Confirmed: 2, Text: "Cart state lives in web/cart/store.ts; components only read it."},
			}},
		}},
		Files: []wire.MemoryFile{
			{Path: "AGENTS.md", Scope: "project", Exists: true, Loaded: true, Version: "f1", Editable: true,
				Text: "# shop\n\nA catalogue API (Go, api/) and a cart screen (React, web/).\n\n- Build: `go build ./...` and `npm --prefix web run build`\n- Test: `go test ./...` and `npm --prefix web test`\n"},
			{Path: "~/.sleipnir/AGENTS.md", Scope: "user", Exists: true, Loaded: true, Version: "f2", Editable: true,
				Text: "Write commit messages in the imperative mood.\n"},
			{Path: "web/AGENTS.md", Scope: "dir", Exists: false, Loaded: false, Editable: true, Why: "not written yet: a session reads it once it is"},
		},
		Lessons: wire.MemoryLessons{Path: "~/.sleipnir/lessons.json", On: true, Lessons: []wire.MemoryLesson{
			{ID: "a1b2c3d4e5f6", Kind: "strategy", Text: "Split a full-stack goal by directory so that no two workers edit the same file.", Task: "build", Uses: 6, Wins: 5, Losses: 1, Utility: 0.75, Created: "2026-10-02T09:12:00Z", Last: "2026-10-09T22:15:00Z"},
			{ID: "0f9e8d7c6b5a", Kind: "pitfall", Text: "A test that needs a network port must pick a free one; port 8080 is often taken.", Task: "test", Uses: 3, Wins: 2, Losses: 1, Utility: 0.6, Created: "2026-10-05T14:40:00Z", Last: "2026-10-08T11:03:00Z"},
		}},
		Root:    Cwd,
		Trusted: true,
	}
}

// UserMemory is the Memory page with no tab: the user's own notes, instruction file and lessons, and nothing of a project.
func UserMemory() wire.MemoryView {
	m := Memory()
	m.Project, m.Lore, m.Root, m.Trusted = nil, nil, "", false
	files := []wire.MemoryFile{}
	for _, f := range m.Files {
		if f.Scope == "user" {
			files = append(files, f)
		}
	}
	m.Files = files
	return m
}

// pluginRow is one plugin of the Plugins page, in the shape of GET /api/plugins.
type pluginRow struct {
	ID           string                `json:"id"`
	Title        string                `json:"title"`
	Description  string                `json:"description"`
	Category     string                `json:"category"`
	Core         bool                  `json:"core"`
	CoreReason   string                `json:"core_reason,omitempty"`
	Experimental bool                  `json:"experimental"`
	External     bool                  `json:"external"`
	Version      string                `json:"version,omitempty"`
	Default      bool                  `json:"default"`
	Enabled      bool                  `json:"enabled"`
	State        string                `json:"state"`
	Reason       string                `json:"reason"`
	Layer        string                `json:"layer"`
	Requires     []string              `json:"requires"`
	Contributes  []plugin.Contribution `json:"contributes"`
	ConfigSchema *plugin.Schema        `json:"config_schema,omitempty"`
	Config       map[string]any        `json:"config"`
}

// Plugins is the Plugins page: the built-in catalogue under the default profile, as a server with no configuration of its own
// reports it (no programs are checked, so the list of needs is empty).
func Plugins() map[string]any {
	set, warns := plugin.Resolve(plugin.Builtin(), plugin.Input{})
	rows := []pluginRow{}
	for _, st := range set.List() {
		rows = append(rows, pluginRow{
			ID: st.ID, Title: st.Title, Description: st.Description, Category: st.Category, Core: st.Core, CoreReason: st.CoreReason,
			Experimental: st.Experimental, External: st.External, Version: st.Version, Default: st.Default, Enabled: st.Enabled,
			State: st.State, Reason: st.Reason, Layer: st.Layer, Requires: append([]string{}, st.Requires...),
			Contributes: append([]plugin.Contribution{}, st.Contributes...), ConfigSchema: st.Manifest.Config, Config: map[string]any{},
		})
	}
	profile := set.Profile
	if profile == "" {
		profile = plugin.DefaultProfile
	}
	if warns == nil {
		warns = []string{}
	}
	return map[string]any{"profile": profile, "profiles": plugin.Profiles(), "restart": false, "plugins": rows, "needs": []any{}, "warnings": warns}
}

// ResearchView is what the Research page reads: the runs of the project, the depths a run can have, and whether the
// plugin is on.
type ResearchView struct {
	Runs   []wire.ResearchRun   `json:"runs"`
	Depths []wire.ResearchDepth `json:"depths"`
	On     bool                 `json:"on"`
}

// Research is the Research page of the canned tab: one run the team works on now, with a gap still open, and an earlier
// one that is published.
func Research() ResearchView {
	return ResearchView{On: true, Depths: session.ResearchDepths(), Runs: []wire.ResearchRun{
		{Slug: "cart-sync-offline", Question: "How do mature sync engines resolve conflicts when a cart is edited offline on two devices?",
			Depth: "standard", Mode: "report", Phase: "gather", Created: "2026-10-09T22:02:11Z", Problems: 3,
			Sources: 14, Domains: 9, Findings: 22, Snippets: 6, Pages: 4, Planned: 11, OpenGaps: 2, Active: true},
		{Slug: "catalogue-paging", Question: "Cursor or offset paging for a product catalogue of a few thousand items?",
			Depth: "quick", Mode: "kickoff", Phase: "done", Created: "2026-10-09T21:40:03Z", Built: true,
			Sources: 8, Domains: 6, Findings: 12, Pages: 5, Planned: 5, NoFactCheck: true},
	}}
}

// Worktrees are the trees of the canned team's workers: the scout's is clean (its task merged), the two builders have work
// in theirs, and the tester has not started.
func Worktrees() []wire.Worktree {
	tree := func(agent, head string, dirty bool, files ...string) wire.Worktree {
		return wire.Worktree{Agent: agent, Path: "~/.sleipnir/work/" + SID + "/" + agent, Branch: "sleipnir/" + SID + "/" + agent,
			Base: "ab12cd3", Head: head, Dirty: dirty, Files: files}
	}
	return []wire.Worktree{
		tree("be-1", "4e1f9a0", true, "api/catalog/items.go", "api/catalog/items_test.go"),
		tree("fe-1", "ab12cd3", true, "web/cart/Cart.tsx"),
		tree("sc-1", "c7d02b5", false),
		tree("te-1", "ab12cd3", false),
	}
}

// MergeQueue is the canned team's merge queue: the scout's map has merged, nothing waits.
func MergeQueue() wire.MergeQueueStatus {
	return wire.MergeQueueStatus{Branch: "sleipnir/" + SID + "/merge", Tip: "c7d02b5", Healthy: true, Waiting: []string{}, Verify: "go test {dirs}",
		Landed: []wire.LandedEntry{{Agent: "sc-1", Task: "T1", Commit: "c7d02b5", Files: []string{"docs/MAP.md"}}}}
}
