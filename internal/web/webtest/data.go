package webtest

import (
	_ "embed"
	"encoding/json"
	"strings"
	"sync"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// specJSON is the mock's CLI spec (its generator reads docs/CLI.md); CLISpec adds the mode of each command.
//
//go:embed testdata/cli-spec.json
var specJSON []byte

var (
	specOnce sync.Once
	spec     wire.CLISpec
)

// CLISpec returns the CLI spec with the run mode of every command (CONTRACT.md section 18.2). The value is shared: do not modify it.
func CLISpec() wire.CLISpec {
	specOnce.Do(func() {
		if err := json.Unmarshal(specJSON, &spec); err != nil {
			panic("webtest: testdata/cli-spec.json: " + err.Error())
		}
		for i := range spec.Commands {
			c := &spec.Commands[i]
			c.Mode, c.Why = modeOf(c.Path)
		}
	})
	return spec
}

// modeOf is the run mode of a command (CONTRACT.md 18.2) and, for the refusals, the sentence that names the web equivalent.
func modeOf(path []string) (mode, why string) {
	p := strings.Join(path, " ")
	switch p {
	case "chat":
		return "tty_only", "+ New session starts the same session here"
	case "login":
		return "tty_only", "Settings > Providers & login"
	case "watch":
		return "tty_only", "the Cockpit shows it live"
	case "mock", "rl serve", "daemon":
		return "server", ""
	case "inspect":
		return "run", "only with --json; use the Cache view here"
	case "replay":
		return "run", "only with --final or --record"
	case "trust add", "trust forget", "mcp approve", "mcp revoke", "schedule add", "schedule rm", "models fav add", "models fav rm", "init", "logout":
		return "priv", ""
	case "models", "doctor", "update", "run", "swarm", "demo", "rl eval", "rl rollout", "rl reward", "rl export", "rl tasks check":
		return "net", ""
	}
	if strings.HasPrefix(p, "rl taskgen ") && p != "rl taskgen" {
		return "net", ""
	}
	return "run", ""
}

// Slash returns the slash commands of a tab: the built-ins of the CLI spec, then two custom commands.
func Slash() []wire.SlashEntry {
	out := append([]wire.SlashEntry{}, CLISpec().ChatSlash...)
	return append(out,
		wire.SlashEntry{Cmd: "/review", Args: "[PATH]", Desc: "review the diff of the working tree", Group: "custom", Custom: true},
		wire.SlashEntry{Cmd: "/changelog", Args: "", Desc: "draft a changelog entry from the commits since the last tag", Group: "custom", Custom: true},
	)
}

// TestsPreset is the rules the "tests" name stands for.
func TestsPreset() []string {
	return []string{"Bash(go test:*)", "Bash(go build:*)", "Bash(go vet:*)", "Bash(make test:*)", "Bash(npm test:*)"}
}

// Projects lists the directories a new session may start in.
func Projects() []wire.Project {
	return []wire.Project{
		{Dir: Cwd, Root: Cwd, Name: "shop", Trust: "trusted", Files: 5, Default: true},
		{Dir: "/home/me/projects/orders", Root: "/home/me/projects/orders", Name: "orders", Trust: "not trusted", Files: 3},
		{Dir: "/home/me/projects/docs", Root: "/home/me/projects/docs", Name: "docs", Trust: "changed", Files: 2},
	}
}

// Recorded lists the recorded sessions no tab hosts.
func Recorded() []wire.RecordedSession {
	return []wire.RecordedSession{
		{ID: "20261008-101500-4be2f1", First: "Migrate the web page to fetch()", Model: "anthropic/claude-haiku-5-5", Cost: 0.84, MB: 1.2, AgeS: 86400, Agents: 5, Resumable: true, Dur: 1260, Cwd: Cwd, LastWritten: 1759922100000},
		{ID: "20261007-153000-91ac03", First: "Fix the failing test in the slugify package", Model: "anthropic/claude-sonnet-5-5", Cost: 0.21, MB: 0.4, AgeS: 172800, Agents: 1, Resumable: true, Dur: 240, Interrupted: true, Cwd: "/home/me/projects/orders", LastWritten: 1759851000000},
		{ID: "20261001-090000-0a7d55", Name: "docs sweep", First: "Check the links in docs/", Model: "heimdall/demo-model", Cost: 0.05, MB: 0.1, AgeS: 777600, Agents: 3, Resumable: false, Dur: 95, Cwd: "/home/me/projects/docs", LastWritten: 1759309200000},
	}
}

// f64 returns a pointer to a price.
func f64(v float64) *float64 { return &v }

// Models is the model catalogue view.
func Models() wire.ModelsView {
	return wire.ModelsView{
		Models: []wire.ModelRow{
			{Ref: "anthropic/claude-sonnet-5-5", Provider: "anthropic", Ctx: 200000, In: f64(3), Out: f64(15), Cached: f64(0.3), Tools: true, Reasoning: true, Fav: true, PriceKnown: true},
			{Ref: "anthropic/claude-haiku-5-5", Provider: "anthropic", Ctx: 200000, In: f64(0.8), Out: f64(4), Cached: f64(0.08), Tools: true, Reasoning: false, Fav: true, PriceKnown: true},
			{Ref: "heimdall/demo-model", Provider: "heimdall", Ctx: 128000, In: f64(0.5), Out: f64(2), Cached: f64(0.1), Tools: true, Reasoning: true, PriceKnown: true},
			{Ref: "openai/gpt-5", Provider: "openai", Ctx: 400000, In: f64(1.25), Out: f64(10), Cached: f64(0.125), Tools: true, Reasoning: true, PriceKnown: true},
			{Ref: "chatgpt/gpt-5-plan", Provider: "chatgpt", Ctx: 272000, Tools: true, Reasoning: true, Plan: true},
			{Ref: "local/qwen3-coder", Provider: "local", Ctx: 32000, Tools: true},
		},
		Favs: []string{"anthropic/claude-sonnet-5-5", "anthropic/claude-haiku-5-5"},
		Roles: []wire.RoleInfo{
			{Name: "manager", Code: "mgr", Desc: "plans, delegates and reviews; edits no file"},
			{Name: "scout", Code: "sc", RO: true, Desc: "reads and reports; cannot write"},
			{Name: "backend", Code: "be", Desc: "writes server code"},
			{Name: "frontend", Code: "fe", Desc: "writes the page"},
			{Name: "tester", Code: "te", Desc: "writes and runs tests"},
		},
		RoleOrder:  []string{"scout", "backend", "frontend", "tester"},
		RoleModels: map[string]string{"scout": "anthropic/claude-haiku-5-5"},
		Efforts:    []string{"default", "minimal", "low", "medium", "high", "xhigh"},
		FetchedAt:  1760048000000,
	}
}

// Providers is the providers view.
func Providers() wire.ProvidersView {
	return wire.ProvidersView{
		Providers: []wire.ProviderRow{
			{ID: "heimdall", Name: "Heimdall", Base: "https://api.heimdall.example/v1", Key: "stored", State: "ready", Recommended: true, KeyWhere: "auth.json", Dialect: "chat", UsedBy: "the default"},
			{ID: "anthropic", Name: "Anthropic", Base: "https://api.anthropic.com", Key: "env", Env: "ANTHROPIC_API_KEY", State: "ready", KeyWhere: "the environment", Dialect: "messages", UsedBy: "manager"},
			{ID: "openai", Name: "OpenAI", Base: "https://api.openai.com/v1", Key: "none", State: "no key", Dialect: "chat"},
			{ID: "chatgpt", Name: "ChatGPT plan", Base: "https://chatgpt.com", Key: "signed in", State: "ready", SignedIn: true, Who: "me@example.com", Dialect: "responses"},
			{ID: "local", Name: "Local server", Base: "http://127.0.0.1:8000/v1", Key: "none", State: "no key needed", Dialect: "chat"},
		},
		Note: "Keys are read from the environment first, then from ~/.sleipnir/auth.json. This page never shows a key.",
	}
}

// Permissions is the permissions view for a mode.
func Permissions(mode string) wire.PermissionsView {
	return wire.PermissionsView{
		Mode: mode,
		Modes: []wire.PermMode{
			{ID: "default", Cycle: true, Text: "asks before anything that writes, runs a program outside the allow rules, or reaches the network"},
			{ID: "accept-edits", Cycle: true, Text: "also allows edits inside the project and the build and test commands"},
			{ID: "plan", Cycle: true, Text: "read-only: nothing is written and nothing runs that is not read-only"},
			{ID: "bypass", Danger: true, Text: "asks for nothing except the hard denies"},
			{ID: "yolo", Danger: true, Text: "asks for nothing at all"},
		},
		Order:         []string{"hard denies", "deny rules", "ask rules", "allow rules", "the mode"},
		ManagerWrites: wire.ManagerWrites{Text: "the manager edits no file: spawn a worker", Note: "in a team the manager's write tools are refused whatever the mode"},
		TestsPreset:   wire.TestsPreset{Name: "tests", Summary: "go, cargo, npm and make: test, build, check, lint and vet; never install or run", Rules: TestsPreset()},
		Rules: map[string][]wire.Rule{
			"allow": {{Effect: "allow", Rule: "Bash(go test:*)", Origin: "--allow flag"}},
			"deny":  {{Effect: "deny", Rule: "Read(./.env)", Origin: "project config", File: ".sleipnir/config.json"}, {Effect: "deny", Rule: "Read(~/.ssh/**)", Origin: "built-in protection", Fixed: true}},
			"ask":   {{Effect: "ask", Rule: "Edit(.github/**)", Origin: "user config", File: "~/.sleipnir/config.json"}},
		},
		Session: []wire.Rule{},
	}
}

// Trust is the trust view of the canned project.
func Trust() wire.TrustView {
	v := wire.TrustView{
		Project: wire.TrustProject{Dir: Cwd, State: "trusted", SavedDay: "2026-10-08", Digest: "9f2c4a1b7d3e8a60", Unlocks: "its AGENTS.md, skills, commands and hooks"},
		Files: []wire.TrustFile{
			{Path: "AGENTS.md", Kind: "instructions", Bytes: 1840, Hash: "a1b2c3d4"},
			{Path: ".sleipnir/config.json", Kind: "config", Bytes: 412, Hash: "e5f6a7b8"},
			{Path: ".sleipnir/skills/release/SKILL.md", Kind: "skill", Bytes: 960, Hash: "c9d0e1f2"},
		},
		Covers: "what the repository brings as text or settings; not its code",
		Ledger: []wire.TrustLedgerRow{
			{Dir: Cwd, Saved: "2026-10-08", Files: 3, Now: "3 files", State: "trusted"},
			{Dir: "/home/me/projects/docs", Saved: "2026-09-30", Files: 2, Now: "2 files, one changed", State: "changed: AGENTS.md"},
		},
	}
	v.Question.Options = []string{"Trust these files", "Not now"}
	return v
}

// MCP is the MCP servers view.
func MCP() wire.MCPView {
	return wire.MCPView{
		Servers: []wire.MCPServer{
			{Name: "fs", Origin: "user config", From: "~/.sleipnir/config.json", Transport: "stdio", State: "running", Tools: []string{"read_file", "list_dir"}, Command: "mcp-fs", Args: []string{"--root", "."}, Approved: true},
			{Name: "tracker", Origin: "project config", From: ".mcp.json", Transport: "http", State: "needs approval", Tools: []string{}, Server: "https://tracker.example/mcp", Describe: "issue tracker", Note: "a project entry needs your approval before it starts", EnvRefs: []string{"TRACKER_TOKEN"}},
		},
		SessionNote: "the tool list of a running session is fixed: changes apply when the team starts again",
		FrozenTools: []string{"read_file", "list_dir"},
	}
}

// Skills is the skills, commands and hooks view.
func Skills() wire.SkillsView {
	return wire.SkillsView{
		Skills: []wire.Skill{
			{Name: "release", Summary: "cut a release: changelog, tag, notes", Source: ".sleipnir/skills/release/SKILL.md", Scope: "project"},
			{Name: "triage", Summary: "label and route new issues", Source: "~/.sleipnir/skills/triage/SKILL.md", Scope: "user", YouOnly: true, Note: "only you can invoke it"},
		},
		SkillsBudget: &wire.SkillsBudget{Tokens: 8000, Used: 640, Note: "descriptions are in the prompt; bodies load on demand"},
		Commands:     []wire.CustomCommand{{Name: "review", Description: "review the diff of the working tree", ArgumentHint: "[PATH]", Source: ".sleipnir/commands/review.md", Scope: "project"}},
		Hooks: &wire.HooksView{
			Events:     []string{"PreToolUse", "PostToolUse", "SessionStart", "Stop"},
			Configured: []wire.Hook{{Event: "PostToolUse", Matcher: "Edit|Write", Command: "gofmt -l .", Timeout: 10, Origin: "project config", Purpose: "keeps files formatted"}},
		},
	}
}

// Config is the configuration layers view.
func Config() wire.ConfigView {
	return wire.ConfigView{
		Layers: []wire.ConfigLayer{
			{Kind: "defaults", Source: "built in", State: "applied"},
			{Kind: "user", Source: "~/.sleipnir/config.json", State: "applied"},
			{Kind: "project", Source: Cwd + "/.sleipnir/config.json", State: "applied", Trusted: true},
			{Kind: "local", Source: Cwd + "/.sleipnir/config.local.json", State: "absent"},
		},
		Precedence: "defaults, then user, then project, then local; flags last",
		Effective: []wire.ConfigValue{
			{Key: "models.default", Value: Model, Layer: "user", File: "~/.sleipnir/config.json"},
			{Key: "swarm.max_workers", Value: 8, Layer: "defaults", File: "built in"},
			{Key: "swarm.isolation", Value: "worktree", Layer: "project", File: ".sleipnir/config.json", Below: map[string]string{"defaults": "none"}},
			{Key: "permissions.mode", Value: "default", Layer: "defaults", File: "built in"},
		},
		Warnings: []string{},
	}
}

// Schedule is the schedule view.
func Schedule() wire.ScheduleView {
	return wire.ScheduleView{
		Jobs: []wire.ScheduleJob{
			{ID: "j1", Cron: "0 7 * * 1-5", Goal: "Run the test suite and summarise failures", Dir: Cwd, Model: Model, Mode: "plan", BudgetUSD: 1, Created: "2026-10-01", LastRun: "2026-10-09 07:00", LastExit: "0", Log: "j1.log", Next: "2026-10-12 07:00"},
			{ID: "j2", Cron: "30 22 * * *", Goal: "Draft release notes from the day's commits", Dir: Cwd, Model: "anthropic/claude-haiku-5-5", Mode: "default", BudgetUSD: 0.5, Created: "2026-10-03", LastRun: "never", LastExit: "-", Next: "2026-10-09 22:30", Paused: true},
		},
		Daemon: wire.DaemonState{Running: false, Owner: "none", Every: "1m", Timeout: "30m", Line: "sleipnir daemon --every 1m"},
		Logs:   []wire.JobLog{{Job: "j1", File: "j1.log", Exit: "0", Text: "ok  shop/api/catalog 0.4s\nok  shop/api/cart 0.2s\n"}},
	}
}

// DoctorEndpoints lists the endpoints the doctor page offers.
func DoctorEndpoints() []wire.DoctorEndpoint {
	return []wire.DoctorEndpoint{
		{Ref: "anthropic/claude-sonnet-5-5", Where: "Anthropic"},
		{Ref: "heimdall/demo-model", Where: "Heimdall"},
		{Ref: "local/qwen3-coder", Where: "http://127.0.0.1:8000/v1"},
	}
}

// Update is the update status.
func Update() wire.UpdateStatus {
	return wire.UpdateStatus{Current: "v0.9.0", Latest: "v0.9.1", Available: true, Notes: "fixes the resume of a team whose manager was interrupted"}
}

// ---- the Workspace -----------------------------------------------------------------------------------------------------------

// WsIndex is the Workspace index of the canned tab: three checkpoints over four files.
func WsIndex() wire.WsIndex {
	return wire.WsIndex{
		Root: Cwd, Isolation: "worktree",
		Base: wire.WsBase{ID: "base", Label: "before the session", Time: "22:15:30"},
		Cps: []wire.WsCheckpoint{
			{ID: "c01", Time: "22:15:42", At: 1760048142000, Label: "turn 1: build the shop", Files: []string{"api/catalog/items.go", "web/cart/Cart.tsx"}, Agents: []string{"be-1", "fe-1"}, Tasks: []string{"T2", "T3"}, Added: 85, NFiles: 2,
				Changes: []wire.WsChange{
					{Path: "api/catalog/items.go", Status: "added", Added: 31, Agents: []string{"be-1"}, Task: "T2"},
					{Path: "web/cart/Cart.tsx", Status: "added", Added: 54, Agents: []string{"fe-1"}, Task: "T3"},
				}},
			{ID: "c02", Time: "22:16:05", At: 1760048165000, Label: "turn 2: page size", Files: []string{"api/catalog/items.go"}, Agents: []string{"be-1"}, Tasks: []string{"T2"}, Added: 6, Removed: 2, NFiles: 1,
				Changes: []wire.WsChange{{Path: "api/catalog/items.go", Status: "modified", Added: 6, Removed: 2, Agents: []string{"be-1"}, Task: "T2"}}},
			{ID: "c03", Time: "22:16:20", At: 1760048180000, Label: "turn 3: tests", Skipped: true, Files: []string{}, Agents: []string{}, Tasks: []string{}, Changes: []wire.WsChange{}},
		},
		Tree: []wire.WsFile{
			{Path: "api/catalog/items.go", Dir: "api/catalog", Name: "items.go", Kind: "go", Status: "modified", Owner: "be-1", Task: "T2", Cp: "c02", Lease: &wire.WsLease{Agent: "be-1", Task: "T2", Glob: "api/catalog/**"}, Add: 37, Del: 2, Size: 1210, Exists: true},
			{Path: "api/server.go", Dir: "api", Name: "server.go", Kind: "go", Status: "unchanged", Size: 5200, Exists: true},
			{Path: "web/cart/Cart.tsx", Dir: "web/cart", Name: "Cart.tsx", Kind: "ts", Status: "added", Owner: "fe-1", Task: "T3", Cp: "c01", Add: 54, Size: 1900, Exists: true},
			{Path: ".env", Dir: "", Name: ".env", Kind: "text", Status: "unchanged", Protected: &wire.WsProtect{Rule: "Read(./.env)", Origin: "project config", Tier: "deny", Why: "secrets live here"}, Size: 80, Exists: true},
			{Path: ".github/workflows/ci.yml", Dir: ".github/workflows", Name: "ci.yml", Kind: "yaml", Status: "unchanged", Ask: &wire.WsAsk{Rule: "Edit(.github/**)", Why: "workflow changes need a person"}, Size: 640, Exists: true},
		},
		Reviewed: map[string]string{},
		Version:  "v1",
	}
}

// WsContent is a file's text at a point, with authorship.
func WsContent(path, at string) wire.WsContent {
	text := "package catalog\n\n// Page is one page of items.\ntype Page struct {\n\tItems []Item `json:\"items\"`\n\tPage  int    `json:\"page\"`\n}\n\n// Size is the page size, at most 50.\nconst Size = 12\n"
	if strings.HasSuffix(path, ".tsx") {
		text = "export function Cart({ items }) {\n  return items.map(i => <li key={i.id}>{i.name}</li>)\n}\n"
	}
	return wire.WsContent{
		Path: path, At: firstNonEmpty(at, "live"), Exists: true, Text: text, Size: int64(len(text)), Exact: true,
		Blame: []wire.BlameRun{{Line: 1, Count: 7, Ag: "be-1", ID: "c01", Task: "T2"}, {Line: 8, Count: 3, Ag: "be-1", ID: "c02", Task: "T2"}},
	}
}

// WsDiff is the diff of a file between two points: one hunk.
func WsDiff(path, from, to string) wire.WsDiff {
	return wire.WsDiff{
		Path: path, From: firstNonEmpty(from, "base"), To: firstNonEmpty(to, "live"), Added: 3, Removed: 1,
		Hunks: []wire.Hunk{{OldStart: 7, OldLines: 2, NewStart: 7, NewLines: 4, Section: "func (p Page)", Lines: []wire.HunkLine{
			{T: " ", S: "}"}, {T: "-", S: "const Size = 100"}, {T: "+", S: "// Size is the page size, at most 50."}, {T: "+", S: "const Size = 12"}, {T: "+", S: ""},
		}}},
	}
}
