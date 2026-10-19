package plugin

// Built-in plugin ids that other packages switch on.
const (
	FS              = "fs"
	Shell           = "shell"
	Web             = "web"
	Recall          = "recall"
	SkillTool       = "skill"
	MemoryTool      = "memory"
	PlanTool        = "plan"
	MCP             = "mcp"
	Skills          = "skills"
	BuiltinSkills   = "builtin-skills"
	Commands        = "commands"
	Agents          = "agents"
	Hooks           = "hooks"
	Instructions    = "instructions"
	Swarm           = "swarm"
	Mailman         = "mailman"
	AutoDocs        = "auto-docs"
	ManagerApproves = "manager-approves"
	PushProgress    = "push-progress"
	Goal            = "goal"
	Delivery        = "delivery"
	Checkpoints     = "checkpoints"
	Recon           = "recon"
	UpdateCheck     = "update-check"
	RolePrefix      = "role:"
	ViewPrefix      = "view:"
)

// WorkerRoles are the built-in worker roles, each a plugin "role:<name>".
var WorkerRoles = []string{"architect", "scout", "backend", "frontend", "devops", "tester", "docs", "reviewer"}

// WebViews are the web page's views that are plugins, "view:<name>", with the
// rail entries they own. Settings is core.
var WebViews = []struct{ Name, Title, Description string }{
	{"cockpit", "Cockpit", "the team at a glance: who works on what, the task board, the merge queue, mail and rate limits"},
	{"workspace", "Workspace", "the project's files, the changes, the checkpoints and the merge queue"},
	{"cache", "Cache", "how much of each prompt was read from the provider's cache, and what it cost"},
	{"mail", "Mail", "the messages the team members send each other"},
	{"board", "Board", "the team's tasks by state"},
	{"replay", "Replay", "play a recorded session back"},
	{"sessions", "Sessions", "recorded sessions: resume, inspect, prune"},
	{"tools", "Tools", "every sleipnir command, with a form and its output; doctor and schedule"},
	{"plugins", "Plugins", "switch plugins on and off and change their settings"},
}

// Builtin returns the manifests of the plugins compiled into Sleipnir, in
// catalogue order. The slice is new on every call.
func Builtin() []Manifest {
	tool := func(id, title, desc string, names ...string) Manifest {
		m := Manifest{ID: id, Title: title, Description: desc, Category: CatTools}
		for _, n := range names {
			m.Contributes = append(m.Contributes, Contribution{KindTool, n})
		}
		return m
	}
	ms := []Manifest{
		tool(FS, "Files", "read, write, edit and search the project's files", "read", "write", "edit", "apply_patch", "ls", "glob", "grep"),
		tool(Shell, "Shell", "run commands in the project, in the foreground or the background", "bash", "bash_output", "bash_kill"),
		tool(Web, "Web", "fetch pages and, with a search key, search the web", "web_fetch", "web_search"),
		tool(Recall, "Recall", "read back tool output that was folded out of the conversation", "recall"),
		tool(SkillTool, "Skill tool", "load a skill's instructions on demand", "skill"),
		tool(MemoryTool, "Memory notes", "save short notes for later sessions (~/.sleipnir/MEMORY.md), after you approve them", "memory"),
		tool(PlanTool, "Plan", "keep each agent's plan in front of it", "plan"),
		{ID: MCP, Title: "MCP servers", Description: "tool servers (Model Context Protocol) from the configuration", Category: CatTools,
			Contributes: []Contribution{{KindMCP, "*"}, {KindSlash, "mcp"}}},

		{ID: Skills, Title: "Skills", Description: "SKILL.md files from your skills directories and the project's", Category: CatExtensions,
			Requires: []string{SkillTool}, Contributes: []Contribution{{KindSkill, "*"}, {KindSlash, "skills"}, {KindPrompt, "skills listing"}}},
		{ID: BuiltinSkills, Title: "Built-in skills", Description: "the skills that ship with Sleipnir, loaded on demand", Category: CatExtensions,
			Requires: []string{Skills}, Contributes: []Contribution{{KindSkill, "built-in"}}},
		{ID: Commands, Title: "Custom commands", Description: "slash commands from markdown templates in your commands directories", Category: CatExtensions,
			Contributes: []Contribution{{KindSlash, "*"}}},
		{ID: Agents, Title: "Agent definitions", Description: "role definitions from markdown files in your agents directories", Category: CatExtensions,
			Contributes: []Contribution{{KindRole, "*"}, {KindSlash, "agents"}}},
		{ID: Hooks, Title: "Hooks", Description: "commands that run around tool calls and session events (the hooks key)", Category: CatExtensions,
			Contributes: []Contribution{{KindHook, "*"}, {KindSlash, "hooks"}}},
		{ID: Instructions, Title: "Instruction files", Description: "AGENTS.md, CLAUDE.md and SLEIPNIR.md in the shared prompt", Category: CatExtensions,
			Contributes: []Contribution{{KindPrompt, "instructions"}, {KindSlash, "memory"}}},

		{ID: Swarm, Title: "Team", Description: "a manager and workers that share one prompt cache; off, every session is a single agent", Category: CatTeam,
			Contributes: []Contribution{{KindRole, "manager"}, {KindSlash, "swarm"}, {KindPanel, "cockpit"}}},
		{ID: Mailman, Title: "Mailman", Description: "a small agent that turns bursts of worker mail into short digests", Category: CatTeam, Off: true,
			Requires: []string{Swarm}, Contributes: []Contribution{{KindRole, "mailman"}}},
		{ID: AutoDocs, Title: "Docs on autopilot", Description: "the docs worker catches up after every accepted task without being asked", Category: CatTeam,
			Requires: []string{Swarm, RolePrefix + "docs"}},
		{ID: ManagerApproves, Title: "Manager approves", Description: "a worker's routine permission question goes to the manager before you", Category: CatTeam,
			Requires: []string{Swarm}},
		{ID: PushProgress, Title: "Push progress", Description: "with worktree isolation, push the integration branch after every merge", Category: CatTeam,
			Requires: []string{Swarm}},
		{ID: Goal, Title: "Standing goals", Description: "/goal: work until a goal is met, judged on evidence", Category: CatTeam,
			Contributes: []Contribution{{KindSlash, "goal"}, {KindEvent, "goal"}}},
		{ID: Delivery, Title: "GitHub delivery", Description: "Auto-fix CI & reviews and Auto-merge for each objective (needs gh)", Category: CatTeam,
			Contributes: []Contribution{{KindSlash, "delivery"}}},
		{ID: Checkpoints, Title: "Checkpoints", Description: "snapshots of the files before each change, for /rewind", Category: CatTeam,
			Contributes: []Contribution{{KindSlash, "rewind"}}},
		{ID: Recon, Title: "Project survey", Description: "a short survey of the project (layout, build and test commands) in the shared prompt", Category: CatTeam,
			Contributes: []Contribution{{KindPrompt, "recon"}, {KindSlash, "recon"}}},
		{ID: UpdateCheck, Title: "Update check", Description: "say in the chat when a newer release is out", Category: CatTeam},
	}
	for _, r := range WorkerRoles {
		ms = append(ms, Manifest{ID: RolePrefix + r, Title: "Role: " + r, Description: roleDesc[r], Category: CatTeam,
			Requires: []string{Swarm}, Contributes: []Contribution{{KindRole, r}}})
	}
	for _, v := range WebViews {
		m := Manifest{ID: ViewPrefix + v.Name, Title: v.Title, Description: v.Description, Category: CatInterface,
			Contributes: []Contribution{{KindView, v.Name}, {KindNav, v.Name}}}
		if v.Name == "cockpit" {
			m.Requires = []string{Swarm}
			m.Contributes = append(m.Contributes, Contribution{KindCard, "*"})
		}
		ms = append(ms, m)
	}
	ms = append(ms,
		Manifest{ID: "core:kernel", Title: "Plugin kernel", Description: "loads, orders and switches the plugins", Category: CatCore, Core: true,
			CoreReason: "it is what turns the others on and off"},
		Manifest{ID: "core:config", Title: "Configuration", Description: "the layered settings files", Category: CatCore, Core: true,
			CoreReason: "plugin choices are stored in it"},
		Manifest{ID: "core:session-log", Title: "Session log", Description: "the event log every view and replay reads", Category: CatCore, Core: true,
			CoreReason: "resume, replay and every view are built from it"},
		Manifest{ID: "core:agent-loop", Title: "Agent loop", Description: "sends the prompt, runs the tools, repeats", Category: CatCore, Core: true,
			CoreReason: "without it nothing runs"},
		Manifest{ID: "core:providers", Title: "Model providers", Description: "reaching the models: Heimdallm, OpenAI, Anthropic and the others", Category: CatCore, Core: true,
			CoreReason: "a session needs a model"},
		Manifest{ID: "core:web-shell", Title: "Web shell", Description: "the page's header, rail, chat and Settings", Category: CatCore, Core: true,
			CoreReason: "the Plugins page lives in it", Contributes: []Contribution{{KindView, "settings"}}},
	)
	sortManifests(ms)
	return ms
}

// roleDesc says in one line what each built-in worker role does.
var roleDesc = map[string]string{
	"architect": "plans interfaces, data shapes and acceptance criteria; writes no code",
	"scout":     "maps the code and outside sources",
	"backend":   "APIs, data, services and their tests",
	"frontend":  "screens, state, styling and their tests",
	"devops":    "build, CI, dependencies, configuration",
	"tester":    "tests against the agreement and exact bug reports",
	"docs":      "README, docs/ and examples that match the code",
	"reviewer":  "reviews each handed-in task",
}

// Profile is a named starting point: which plugins are on before a person's
// own choices. A plugin not named keeps its manifest default.
type Profile struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	On          []string `json:"on,omitempty"`
	Off         []string `json:"off,omitempty"`
	// Only, when set, turns every plugin off except these (and core).
	Only []string `json:"only,omitempty"`
}

// DefaultProfile is the profile used when the configuration names none.
const DefaultProfile = "standard"

// Profiles returns the built-in profiles.
func Profiles() []Profile {
	return []Profile{
		{ID: "standard", Title: "Standard", Description: "everything Sleipnir does by default"},
		{ID: "minimal", Title: "Minimal", Description: "one agent with the file and shell tools: nothing else in the prompt",
			Only: []string{FS, Shell, PlanTool, Checkpoints, ViewPrefix + "workspace", ViewPrefix + "sessions", ViewPrefix + "tools", ViewPrefix + "plugins"}},
		{ID: "team", Title: "Team", Description: "standard, with the mailman digesting worker mail", On: []string{Mailman}},
		{ID: "headless", Title: "Headless", Description: "standard without the web views, for scripts and CI",
			Off: viewIDs()},
	}
}

// viewIDs lists the ids of every web view plugin.
func viewIDs() []string {
	out := make([]string, 0, len(WebViews))
	for _, v := range WebViews {
		out = append(out, ViewPrefix+v.Name)
	}
	return out
}

// ProfileByID returns the built-in profile with that id.
func ProfileByID(id string) (Profile, bool) {
	for _, p := range Profiles() {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}
