package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/plugin"
	"github.com/anemos-labs/sleipnir/internal/sched"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Host is the program a session runs in (the terminal chat, the web page), as the session's own agents reach it through the
// sleipnir tool: what the person could type there, done on the person's behalf. A headless run has none.
type Host interface {
	// Command runs a slash line that only looks (/status, /cost, /skills, ...) at once, beside the running turn, and returns
	// what it printed.
	Command(ctx context.Context, line string) (string, error)
	// Queue runs a slash line when the running turn is over, as if the person had typed it then, and returns what the agent is
	// told about it.
	Queue(line string) (string, error)
	// Reload starts the session again when the running turn is over, with its conversation, so that plugin and configuration
	// changes apply, and tells the agent to go on once it has; it returns what the agent is told about it.
	Reload(why string) (string, error)
}

// SetHost connects the session to the program it runs in (nil: none).
func (s *Session) SetHost(h Host) {
	s.mu.Lock()
	s.host = h
	s.mu.Unlock()
}

// currentHost is the host, or nil.
func (s *Session) currentHost() Host {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.host
}

// HarnessToolName is the name of the tool the agents drive Sleipnir's own features with.
const HarnessToolName = "sleipnir"

// harnessTool is the sleipnir tool: what a person does with slash commands, the Plugins page and the schedule, done by the agent
// they talk to when they ask for it. Every change goes through the permission engine as a change of state, so it asks the person
// unless the session's mode lets it through, and only the agent the person talks to (a single agent, or a team's manager) may
// use it.
type harnessTool struct{ s *Session }

const harnessDescription = "Sleipnir's own features, for when the person asks for one (\"run this every morning\", \"start a deep scan\", " +
	"\"turn on X\", \"make a plugin that...\"). Only the manager, or a single agent, uses it. Actions: " +
	"help (what this harness can do, and what is on); " +
	"run (line: a slash command as the person would type it: /scan [survey|standard|deep] [DIR], /research [quick|standard|deep] QUESTION, " +
	"/goal TEXT|pause|resume|clear, /delivery fix|merge|both|off, /jobs stop ID; /status, /cost, /skills, /mcp, /jobs and /agents answer at once, " +
	"the others run when your turn ends, so end it soon after); " +
	"schedule (op list|add|remove|pause|resume: a goal run headless on a cron, e.g. cron \"0 9 * * 1-5\"); " +
	"plugins (op list|check|install|enable|disable|reset|remove; install and check take source, a plugin directory or a git URL: load the " +
	"sleipnir-plugin skill before you write one); " +
	"reload (start the session again when your turn ends, with this conversation, so plugin and configuration changes apply; you are told " +
	"to go on after it). Changes ask the person unless the permission mode allows them."

const harnessSchema = `{"type":"object","properties":{` +
	`"action":{"type":"string","enum":["help","run","schedule","plugins","reload"]},` +
	`"line":{"type":"string","description":"run: the slash command"},` +
	`"op":{"type":"string","description":"schedule: list|add|remove|pause|resume; plugins: list|check|install|enable|disable|reset|remove"},` +
	`"id":{"type":"string","description":"schedule: the job (j3); plugins: the plugin id"},` +
	`"cron":{"type":"string","description":"schedule add: five fields minute hour day-of-month month day-of-week, or @hourly, @daily, @weekly"},` +
	`"goal":{"type":"string","description":"schedule add: what each run works on, complete in itself: nobody is there to ask"},` +
	`"model":{"type":"string","description":"schedule add: provider/model (default: this session's)"},` +
	`"mode":{"type":"string","enum":["default","accept-edits","plan"],"description":"schedule add: the run's permissions (default refuses what needs a person)"},` +
	`"budget_usd":{"type":"number","description":"schedule add: stop a run at this many dollars (default 1, 0: no limit)"},` +
	`"dir":{"type":"string","description":"schedule add: the folder the run works in (default: this project)"},` +
	`"source":{"type":"string","description":"plugins install, check: a plugin directory (relative to the project) or a git URL"},` +
	`"replace":{"type":"boolean","description":"plugins install: replace the installed plugin of the same id (an update of your own plugin)"},` +
	`"why":{"type":"string","description":"reload: what changed, in a line, shown to the person"}},` +
	`"required":["action"]}`

// Spec implements tools.Tool. The tool changes no file of the project: what it changes is the person's settings, schedule and
// plugins, under ~/.sleipnir.
func (harnessTool) Spec() core.ToolSpec {
	return core.ToolSpec{Name: HarnessToolName, Description: harnessDescription, InputSchema: json.RawMessage(harnessSchema), NoTreeWrites: true}
}

type harnessInput struct {
	Action    string   `json:"action"`
	Line      string   `json:"line"`
	Op        string   `json:"op"`
	ID        string   `json:"id"`
	Cron      string   `json:"cron"`
	Goal      string   `json:"goal"`
	Model     string   `json:"model"`
	Mode      string   `json:"mode"`
	BudgetUSD *float64 `json:"budget_usd"`
	Dir       string   `json:"dir"`
	Source    string   `json:"source"`
	Replace   bool     `json:"replace"`
	Why       string   `json:"why"`
}

// Run implements tools.Tool.
func (t harnessTool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	env := c.Env
	if env == nil {
		env = &tools.Env{}
	}
	env = env.Defaults()
	if env.Agent != "" && env.Agent != t.s.mainAgent() {
		return tools.Errorf("only the agent the person talks to (the manager) uses the sleipnir tool: tell the manager what the person needs"), nil
	}
	var in harnessInput
	if err := json.Unmarshal(c.Input, &in); err != nil {
		return tools.Errorf("sleipnir: the input is not valid JSON: %v", err), nil
	}
	cwd := firstNonEmpty(env.Cwd, t.s.Cwd())
	ask := func(summary string) *tools.Result {
		d := env.Perm.Check(ctx, perm.Request{Agent: env.Agent, Role: env.Role, Tool: HarnessToolName, Input: c.Input, Summary: summary, Writes: true, Risk: perm.RiskMedium})
		if d.Allow {
			return nil
		}
		reason := strings.TrimSpace(d.Reason)
		if reason == "" {
			reason = "not allowed by the permission policy"
		}
		return tools.Errorf("permission denied (%s): %s", summary, reason)
	}
	switch strings.ToLower(strings.TrimSpace(in.Action)) {
	case "help", "":
		return &tools.Result{Text: t.help()}, nil
	case "run":
		return t.run(ctx, in.Line, ask), nil
	case "schedule":
		return t.schedule(in, cwd, ask), nil
	case "plugins":
		return t.plugins(ctx, in, cwd, ask), nil
	case "reload":
		return t.reload(in.Why, ask), nil
	}
	return tools.Errorf("sleipnir: action is help, run, schedule, plugins or reload"), nil
}

// lookLines are the slash commands that only look: the host answers them at once, beside the running turn.
var lookLines = map[string]bool{"/status": true, "/cost": true, "/skills": true, "/agents": true, "/mcp": true, "/jobs": true}

// queuedLines are the slash commands the agent may have run when its turn is over: they start work the person asked for.
var queuedLines = map[string]bool{"/scan": true, "/research": true, "/goal": true, "/delivery": true, "/jobs": true}

// personOnly are the slash commands that stay the person's: they raise what the session may do or spend, change who works and on
// which model, or end the conversation. The tool says so rather than calling them unknown.
var personOnly = map[string]string{
	"/mode": "the permission mode", "/allow": "the permission rules", "/permissions": "the permission rules", "/trust": "trusting the project's files",
	"/budget": "the budget", "/model": "the model", "/roles": "the roles' models", "/effort": "the reasoning effort", "/login": "signing in",
	"/swarm": "the team's shape", "/team": "the team's shape", "/restart": "a restart with other flags (reload applies changed settings)",
	"/new": "starting the conversation over", "/clear": "starting the conversation over", "/resume": "switching sessions",
	"/rewind": "undoing files", "/exit": "closing the session", "/quit": "closing the session", "/compact": "folding the conversation",
}

// run runs a slash line through the host: at once when it only looks, else when the turn is over.
func (t harnessTool) run(ctx context.Context, line string, ask func(string) *tools.Result) *tools.Result {
	line = strings.Join(strings.Fields(line), " ")
	if !strings.HasPrefix(line, "/") || len(line) < 2 {
		return tools.Errorf(`sleipnir run: line is a slash command, such as "/scan standard"`)
	}
	if strings.ContainsAny(line, "\r\n") || len(line) > 4000 {
		return tools.Errorf("sleipnir run: one line of at most 4000 characters")
	}
	f := strings.Fields(line)
	name := strings.ToLower(f[0])
	if what, ok := personOnly[name]; ok {
		return tools.Errorf("%s is the person's to run (%s): ask them, or tell them the command", name, what)
	}
	if name == "/plugins" {
		return tools.Errorf("use action plugins (op list, enable, disable, install ...)")
	}
	if msg, off := t.s.SlashOff(name); off {
		return tools.Errorf("%s; with the person's go-ahead, use action plugins (op enable) and then reload", msg)
	}
	h := t.s.currentHost()
	if h == nil {
		return tools.Errorf("this session has no interactive chat (a headless run, or the line chat): %s cannot be run from here", name)
	}
	if lookLines[name] && len(f) == 1 {
		out, err := h.Command(ctx, line)
		if err != nil {
			return tools.Errorf("%s: %v", name, err)
		}
		return &tools.Result{Text: strings.TrimSpace(tools.SanitizeForTerminal(out))}
	}
	if !queuedLines[name] {
		return tools.Errorf("%s is not run through this tool; the commands are /scan, /research, /goal, /delivery, /jobs stop ID, and /status, /cost, /skills, /mcp, /jobs, /agents", name)
	}
	if r := ask("run " + clipLine(line, 200) + " when this turn ends"); r != nil {
		return r
	}
	msg, err := h.Queue(line)
	if err != nil {
		return tools.Errorf("%s: %v", name, err)
	}
	return &tools.Result{Text: msg}
}

// reload asks the host to start the session again when the turn is over.
func (t harnessTool) reload(why string, ask func(string) *tools.Result) *tools.Result {
	h := t.s.currentHost()
	if h == nil {
		return tools.Errorf("this session has no interactive chat (a headless run, or the line chat): a change applies when it starts again")
	}
	if n := t.s.runningWorkers(); n > 0 {
		return tools.Errorf("%d worker(s) are at work: a reload would stop them. Wait until they are done, then reload", n)
	}
	why = clipLine(why, 300)
	if r := ask("start the session again to apply: " + firstNonEmpty(why, "changed settings")); r != nil {
		return r
	}
	msg, err := h.Reload(why)
	if err != nil {
		return tools.Errorf("reload: %v", err)
	}
	return &tools.Result{Text: msg}
}

// runningWorkers counts a team's workers that are running now.
func (s *Session) runningWorkers() int {
	if s.Swarm == nil {
		return 0
	}
	n := 0
	for _, a := range s.Swarm.Board.Snapshot().Agents {
		if a.State == "running" && a.ID != s.mainAgent() {
			n++
		}
	}
	return n
}

// help says what the tool can do in this session.
func (t harnessTool) help() string {
	var b strings.Builder
	state := func(id string) string {
		if st, ok := t.s.plugins.Get(id); ok && st.State != plugin.StateOn {
			return " (plugin " + id + " is " + st.State + " here: plugins op enable, then reload)"
		}
		return ""
	}
	b.WriteString("What you can do for the person with this tool:\n")
	b.WriteString("- Deep scan: run /scan [survey|standard|deep] [DIR]: a team maps the repository at HEAD, hunts it for holes and reports what it proved" + state(plugin.Scan) + ". /scan alone lists the scans.\n")
	b.WriteString("- Deep research: run /research [quick|standard|deep] QUESTION: a team researches on the web and publishes a wiki" + state(plugin.Research) + ".\n")
	b.WriteString("- Standing goal: run /goal TEXT: the session keeps working until a judge finds the goal met on evidence" + state(plugin.Goal) + ".\n")
	b.WriteString("- GitHub delivery: run /delivery fix|merge|both|off: CI and review fixes, and merging, for this objective" + state(plugin.Delivery) + ".\n")
	b.WriteString("- Schedules: schedule op add with cron and goal: a headless run on that schedule (the web page and `sleipnir daemon` start the due ones).\n")
	b.WriteString("- Plugins: plugins op list|enable|disable|reset, then reload. A plugin of your own (skills, slash commands, roles, hooks, tool servers, a web card): load the sleipnir-plugin skill, write it in the project or $TMPDIR, plugins op check, op install, then reload.\n")
	b.WriteString("- Look: run /status, /cost, /skills, /mcp, /jobs, /agents.\n")
	b.WriteString("The person's own: the permission mode and rules, models and roles, the team's shape, the budget, sign-in, project trust, and starting the conversation over.\n")
	if h := t.s.currentHost(); h == nil {
		b.WriteString("This session has no interactive chat: run and reload are not available here; schedule and plugins are.\n")
	}
	if next, _, _, err := plugin.LoadSet(t.s.Cwd(), t.s.Root(), t.s.Home(), t.s.TrustProject()); err == nil {
		if marked, changed := plugin.MarkRestart(t.s.Plugins(), next); changed {
			var pend []string
			for _, st := range marked.List() {
				if st.State == plugin.StateRestart {
					pend = append(pend, st.ID+" ("+st.Reason+")")
				}
			}
			b.WriteString("Changed since this session started (reload applies them): " + strings.Join(pend, "; ") + ".\n")
		}
	}
	return b.String()
}

// scheduleStore is the person's schedule file, the one `sleipnir schedule` and the web page's schedule use.
func (s *Session) scheduleStore() sched.Store {
	return sched.Store{Path: filepath.Join(StateRoot(s.Home()), "schedule.json")}
}

// schedule lists and changes the scheduled goals.
func (t harnessTool) schedule(in harnessInput, cwd string, ask func(string) *tools.Result) *tools.Result {
	st := t.s.scheduleStore()
	now := time.Now()
	switch op := strings.ToLower(strings.TrimSpace(in.Op)); op {
	case "", "list":
		jobs, err := st.List()
		if err != nil {
			return tools.Errorf("schedule: %v", err)
		}
		if len(jobs) == 0 {
			return &tools.Result{Text: "no scheduled goals"}
		}
		var b strings.Builder
		for _, j := range jobs {
			next := "paused"
			if n, ok := sched.Next(j, now); ok {
				next = "next " + n.Format("2006-01-02 15:04")
			}
			last := "never ran"
			if !j.LastRun.IsZero() {
				last = "last " + j.LastRun.Format("2006-01-02 15:04") + " " + j.LastExit
			}
			fmt.Fprintf(&b, "%s  %q  %s; %s; mode %s; %s\n  %s\n", j.ID, j.Cron, next, last, firstNonEmpty(j.Mode, "default"), j.Dir, clipLine(j.Goal, 300))
		}
		return &tools.Result{Text: b.String()}
	case "add":
		j := sched.Job{Cron: strings.TrimSpace(in.Cron), Goal: strings.TrimSpace(in.Goal), Model: strings.TrimSpace(in.Model), Mode: strings.TrimSpace(in.Mode), BudgetUSD: 1}
		if in.BudgetUSD != nil {
			j.BudgetUSD = *in.BudgetUSD
		}
		if _, err := sched.ParseCron(j.Cron); err != nil {
			return tools.Errorf("schedule add: cron: %v", err)
		}
		if j.Goal == "" {
			return tools.Errorf("schedule add: goal is what each run works on")
		}
		if math.IsNaN(j.BudgetUSD) || math.IsInf(j.BudgetUSD, 0) || j.BudgetUSD < 0 {
			return tools.Errorf("schedule add: budget_usd is a number of dollars, 0 for no limit")
		}
		switch j.Mode {
		case "":
			j.Mode = "default"
		case "default", "accept-edits", "plan":
		default:
			return tools.Errorf("schedule add: mode is default, accept-edits or plan: a run nobody watches is not given bypass or yolo from here")
		}
		if j.Model == "" {
			j.Model = t.s.ModelRef()
		}
		if strings.HasPrefix(j.Model, "-") || strings.ContainsAny(j.Model, " \t\n") {
			return tools.Errorf("schedule add: model is written provider/model")
		}
		j.Dir = t.s.Root()
		if in.Dir != "" {
			j.Dir = in.Dir
			if !filepath.IsAbs(j.Dir) {
				j.Dir = filepath.Join(cwd, j.Dir)
			}
		}
		if fi, err := os.Stat(j.Dir); err != nil || !fi.IsDir() {
			return tools.Errorf("schedule add: %s is not a folder", j.Dir)
		}
		if r := ask(fmt.Sprintf("schedule %q at %q in %s (mode %s, budget %s)", clipLine(j.Goal, 160), j.Cron, j.Dir, j.Mode, budgetText(j.BudgetUSD))); r != nil {
			return r
		}
		j, err := st.Add(j, now)
		if err != nil {
			return tools.Errorf("schedule add: %v", err)
		}
		next, _ := sched.Next(j, now)
		return &tools.Result{Text: fmt.Sprintf("scheduled %s: %q, next run %s. The web page's Tools › Schedule and `sleipnir schedule` list it; `sleipnir web` or `sleipnir daemon` must be running for it to start.", j.ID, j.Cron, next.Format("2006-01-02 15:04"))}
	case "remove", "rm", "pause", "resume":
		if in.ID == "" {
			return tools.Errorf("schedule %s: id names the job (schedule op list shows them)", op)
		}
		if r := ask("schedule " + op + " " + in.ID); r != nil {
			return r
		}
		var err error
		if op == "remove" || op == "rm" {
			err = st.Remove(in.ID)
		} else {
			_, err = st.Update(in.ID, func(j *sched.Job) { j.Paused = op == "pause" })
		}
		if err != nil {
			return tools.Errorf("schedule %s: %v", op, err)
		}
		return &tools.Result{Text: "schedule: " + in.ID + " " + map[string]string{"remove": "removed", "rm": "removed", "pause": "paused", "resume": "resumed"}[op]}
	}
	return tools.Errorf("schedule: op is list, add, remove, pause or resume")
}

// budgetText is a run's budget as the person reads it.
func budgetText(usd float64) string {
	if usd == 0 {
		return "no limit"
	}
	return fmt.Sprintf("US$%.2f", usd)
}

// plugins lists, checks, installs and switches plugins. Every change is written where `sleipnir plugins` writes it (the person's
// own configuration and plugin directory) and applies when the session starts again (reload).
func (t harnessTool) plugins(ctx context.Context, in harnessInput, cwd string, ask func(string) *tools.Result) *tools.Result {
	home := t.s.Home()
	load := func() (*plugin.Set, error) {
		set, _, _, err := plugin.LoadSet(t.s.Cwd(), t.s.Root(), home, t.s.TrustProject())
		return set, err
	}
	source := func() (string, error) {
		src := strings.TrimSpace(in.Source)
		switch {
		case src == "":
			return "", errors.New("source is the plugin's directory or a git URL")
		case plugin.IsGitSource(src) || filepath.IsAbs(src):
			return src, nil
		}
		return filepath.Join(cwd, src), nil
	}
	op := strings.ToLower(strings.TrimSpace(in.Op))
	switch op {
	case "", "list":
		set, err := load()
		if err != nil {
			return tools.Errorf("plugins: %v", err)
		}
		marked, _ := plugin.MarkRestart(t.s.Plugins(), set)
		var b strings.Builder
		fmt.Fprintf(&b, "profile %s; plugin API %d\n", marked.Profile, plugin.APIVersion)
		for _, st := range marked.List() {
			if st.Core {
				continue
			}
			line := fmt.Sprintf("%s  %s  %s", st.ID, st.State, st.Description)
			if st.Reason != "" {
				line += " [" + st.Reason + "]"
			}
			if st.External {
				line += fmt.Sprintf(" (installed in %s, api %d)", st.Dir, st.API)
			}
			b.WriteString(clipLine(line, 300) + "\n")
		}
		return &tools.Result{Text: b.String()}
	case "check":
		src, err := source()
		if err != nil {
			return tools.Errorf("plugins check: %v", err)
		}
		if plugin.IsGitSource(src) {
			return tools.Errorf("plugins check reads a directory: clone it first, or install it and read what install reports")
		}
		return &tools.Result{Text: checkReport(src, t.knownIDs())}
	case "install":
		src, err := source()
		if err != nil {
			return tools.Errorf("plugins install: %v", err)
		}
		report := ""
		if !plugin.IsGitSource(src) {
			m, problems := plugin.Check(src, t.knownIDs())
			for _, p := range problems {
				if p.Error {
					return tools.Errorf("plugins install: %s is not ready:\n%s", src, checkReport(src, t.knownIDs()))
				}
			}
			report = checkReport(src, t.knownIDs())
			if in.Replace && m.ID != "" {
				in.ID = m.ID
			}
		}
		what := "install the plugin " + src + " and turn it on"
		if in.Replace {
			what = "install the plugin " + src + " over the installed one of the same id, and turn it on"
		}
		if r := ask(what); r != nil {
			return r
		}
		known := t.knownIDs()
		check := func(dir string) error { // what is fetched is what is checked: a git source is read here for the first time
			_, problems := plugin.Check(dir, known)
			for _, p := range problems {
				if p.Error {
					return fmt.Errorf("%s is not ready:\n%s", src, checkReport(dir, known))
				}
			}
			report = checkReport(dir, known)
			return nil
		}
		m, err := plugin.InstallWith(ctx, home, src, plugin.InstallOptions{Replace: in.Replace, Check: check})
		var dup *plugin.InstalledError
		if errors.As(err, &dup) {
			return tools.Errorf("plugins install: a plugin %q is already installed in %s; to update it, install again with replace: true (the installed copy stays if the new one is refused)", dup.ID, dup.Dir)
		}
		if err != nil {
			return tools.Errorf("plugins install: %v", err)
		}
		if err := t.toggle(m.ID, "enable"); err != nil {
			return tools.Errorf("plugins install: installed %s in %s, but turning it on failed: %v", m.ID, m.Dir, err)
		}
		return &tools.Result{Text: fmt.Sprintf("installed %s %s in %s and turned it on (adds %s). It starts with the session: reload when you are ready.\n%s",
			m.ID, m.Version, m.Dir, firstNonEmpty(plugin.Summary(m.Contributes), "nothing yet"), report)}
	case "enable", "disable", "reset", "remove":
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return tools.Errorf("plugins %s: id names the plugin (plugins op list shows them)", op)
		}
		set, err := load()
		if err != nil {
			return tools.Errorf("plugins: %v", err)
		}
		st, ok := set.Get(id)
		switch {
		case !ok:
			return tools.Errorf("plugins %s: no plugin %q (plugins op list shows them)", op, id)
		case st.Core:
			return tools.Errorf("plugins %s: %s is core and always on: %s", op, id, st.CoreReason)
		case op == "remove" && !st.External:
			return tools.Errorf("plugins remove: %s is built in; disable it instead", id)
		}
		if r := ask(op + " the plugin " + id); r != nil {
			return r
		}
		if op == "remove" {
			dir, err := plugin.Remove(home, id)
			if err != nil {
				return tools.Errorf("plugins remove: %v", err)
			}
			// The person's choice for it goes with it: a choice for a plugin that is not there is a warning at every start.
			_ = config.Save(config.UserConfigPath(home), map[string]any{"plugins": map[string]any{"enabled": map[string]any{id: nil}}})
			return &tools.Result{Text: "removed " + id + " (" + dir + "); reload to stop it in this session"}
		}
		if err := t.toggle(id, op); err != nil {
			return tools.Errorf("plugins %s: %v", op, err)
		}
		after, err := load()
		if err != nil {
			return tools.Errorf("plugins: %v", err)
		}
		now, _ := after.Get(id)
		line := id + ": " + now.State
		if now.Reason != "" {
			line += " (" + now.Reason + ")"
		}
		if now.Layer != plugin.LayerUser && now.Layer != plugin.LayerProfile && now.Layer != plugin.LayerDefault {
			line += "; the " + now.Layer + " layer decides it, over the person's own settings"
		}
		return &tools.Result{Text: line + ". It applies when the session starts again: reload."}
	}
	return tools.Errorf("plugins: op is list, check, install, enable, disable, reset or remove")
}

// toggle writes a plugin choice to the person's own configuration, as `sleipnir plugins enable` does.
func (t harnessTool) toggle(id, verb string) error {
	set, _, _, err := plugin.LoadSet(t.s.Cwd(), t.s.Root(), t.s.Home(), t.s.TrustProject())
	if err != nil {
		return err
	}
	st, ok := set.Get(id)
	if !ok {
		return fmt.Errorf("no plugin %q", id)
	}
	v := verb == "enable"
	if verb == "reset" {
		v = st.Default
	}
	return plugin.Save(config.UserConfigPath(t.s.Home()), plugin.ConfiguredProfile(t.s.Cwd(), t.s.Root(), t.s.Home()), id, &v, nil)
}

// knownIDs are the plugin ids of the catalogue a plugin may require.
func (t harnessTool) knownIDs() []string {
	var out []string
	for _, m := range plugin.Catalogue(t.s.Home(), t.s.Root(), t.s.TrustProject()) {
		out = append(out, m.ID)
		out = append(out, m.Provides...)
	}
	sort.Strings(out)
	return out
}

// checkReport is plugin.Check's findings as text.
func checkReport(dir string, known []string) string {
	m, problems := plugin.Check(dir, known)
	var b strings.Builder
	if m.ID != "" {
		fmt.Fprintf(&b, "%s %s (plugin API %d; this Sleipnir reads %d to %d): %s\n", m.ID, m.Version, m.API, plugin.MinAPIVersion, plugin.APIVersion, firstNonEmpty(plugin.Summary(m.Contributes), "contributes nothing"))
	}
	if len(problems) == 0 {
		b.WriteString("no problems found\n")
	}
	for _, p := range problems {
		b.WriteString(p.String() + "\n")
	}
	return b.String()
}

// clipLine is s on one line, at most n runes.
func clipLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// PluginTrouble says, in a line, which plugins the person turned on did not start in this session and why ("" when all did): what
// an agent that just reloaded its own plugin needs to hear first.
func (s *Session) PluginTrouble() string {
	var out []string
	for _, st := range s.plugins.List() {
		if st.Enabled && (st.State == plugin.StateError || st.State == plugin.StatePending) {
			out = append(out, st.ID+" is "+st.State+" ("+st.Reason+")")
		}
	}
	return strings.Join(out, "; ")
}
