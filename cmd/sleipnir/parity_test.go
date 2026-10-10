package main

// The parity guard of the commands and the slash commands, which need package main: the commands the program dispatches, the table of
// the chat's slash commands, and a real session behind the web server. Each lists what the terminal interface has from the code and
// what the web interface has from the inventory of the page (internal/parity/webui.json, made by internal/web/uidev/inventory.mjs),
// and compares them with parity.Differences.Check; the contracts are internal/parity/contract/{commands,slash}.json.
// The flags, the views and the settings are compared in internal/parity.

import (
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/parity"
	"github.com/anemos-labs/sleipnir/internal/web/clispec"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// pageInventory is the part of webui.json these tests read.
type pageInventory struct {
	Palette []struct {
		Name    string   `json:"name"`
		Kind    string   `json:"kind"`
		Effects []string `json:"effects"`
	} `json:"palette"`
	Slash struct {
		Handlers map[string][]string `json:"handlers"`
		Menu     []string            `json:"menu"`
	} `json:"slash"`
	NewSession struct {
		Flags []string `json:"flags"`
	} `json:"newSession"`
	RunLine struct {
		Flags []string `json:"flags"`
	} `json:"runLine"`
	Settings []struct {
		ID       string `json:"id"`
		Controls []struct {
			ID   string `json:"id"`
			Flag string `json:"flag"`
		} `json:"controls"`
	} `json:"settings"`
	Runner struct {
		Commands map[string]struct {
			Mode     string            `json:"mode"`
			ModeWith map[string]string `json:"modeWith"`
		} `json:"commands"`
	} `json:"runner"`
}

// loadPageInventory reads webui.json.
func loadPageInventory(t testing.TB) pageInventory {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(parity.Dir(), "..", "webui.json"))
	if err != nil {
		t.Fatalf("%v: generate the inventory of the page with sh scripts/gen-webui-inventory.sh", err)
	}
	var ui pageInventory
	if err := json.Unmarshal(data, &ui); err != nil {
		t.Fatalf("webui.json: %v", err)
	}
	return ui
}

// clonePageInventory copies the inventory through JSON, so that a test may change the copy.
func clonePageInventory(t testing.TB, ui pageInventory) pageInventory {
	t.Helper()
	b, _ := json.Marshal(ui)
	var out pageInventory
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// checkContract reports every problem of a comparison against the contract of a dimension and logs its gaps.
func checkContract(t *testing.T, dimension string, terminal, web []string) {
	t.Helper()
	var d parity.Differences
	if err := parity.Load(dimension, &d); err != nil {
		t.Fatalf("contract/%s.json: %v", dimension, err)
	}
	for _, p := range d.Check(dimension, terminal, web) {
		t.Error(p)
	}
	for _, g := range d.GapLines(dimension) {
		t.Log(g)
	}
}

// ---- the commands ----------------------------------------------------------------------------------------------------------

// dispatchedCommands are the commands the program runs: the entries of extraCommands (which the init functions of the command files
// fill) and the cases of the switch in main, read from main.go.
func dispatchedCommands(t testing.TB) []string {
	t.Helper()
	var out []string
	for name := range extraCommands {
		out = append(out, name)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "main.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "main" {
			return true
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			sw, ok := n.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			if id, ok := sw.Tag.(*ast.Ident); !ok || id.Name != "cmd" {
				return true
			}
			found = true
			for _, c := range sw.Body.List {
				for _, e := range c.(*ast.CaseClause).List {
					if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						if s, err := strconv.Unquote(lit.Value); err == nil && !strings.HasPrefix(s, "-") {
							out = append(out, s)
						}
					}
				}
			}
			return false
		})
		return false
	})
	if !found {
		t.Fatal("main.go: the switch on cmd in func main was not found; the guard reads the commands the program dispatches from it")
	}
	return out
}

// commandItems are the commands of the terminal interface: every command path of the CLI spec and every command the program
// dispatches (a command the dispatcher has and the spec lacks is a hidden tool).
func commandItems(spec *wire.CLISpec, dispatched []string) []string {
	var out []string
	for _, c := range spec.Commands {
		out = append(out, strings.Join(c.Path, " "))
	}
	return append(out, dispatched...)
}

// ttyEquivalents are the commands that need a terminal and whose function the page has in a dialog, not in a view: the command and
// the effect of the palette entry that opens the dialog.
var ttyEquivalents = map[string]string{"chat": "dialog:newSession"}

// commandWeb are the commands the page can do something with: it runs them in a form of the runner (any command it can run in its own
// mode), or it opens a screen of its own for the command (the palette entry of the program opens a view or a Settings page), or the
// command is the New session dialog.
func commandWeb(spec *wire.CLISpec, ui *pageInventory) []string {
	effects := map[string][]string{}
	var all []string
	for _, p := range ui.Palette {
		all = append(all, p.Effects...)
		if p.Kind == "program" {
			effects[strings.TrimPrefix(p.Name, "sleipnir ")] = p.Effects
		}
	}
	var out []string
	for _, c := range spec.Commands {
		key := strings.Join(c.Path, " ")
		rc, listed := ui.Runner.Commands[key]
		screen := false
		for _, e := range effects[key] {
			screen = screen || strings.HasPrefix(e, "view:") || strings.HasPrefix(e, "settings:")
		}
		switch {
		case listed && rc.Mode != "tty_only", screen, ttyEquivalents[key] != "" && slices.Contains(all, ttyEquivalents[key]):
			out = append(out, key)
		}
	}
	return out
}

// The commands: every command the program dispatches is a command of the CLI spec, and the page can run it, open a screen of its own
// for it, or has it listed with the reason.
func TestParityCommands(t *testing.T) {
	spec, err := clispec.Spec()
	if err != nil {
		t.Fatal(err)
	}
	ui := loadPageInventory(t)
	checkContract(t, "commands", commandItems(spec, dispatchedCommands(t)), commandWeb(spec, &ui))
}

// A command the program gains, a command the page cannot reach, and a screen the page loses each fail the comparison.
func TestParityCommandsFailOnDrift(t *testing.T) {
	spec, err := clispec.Spec()
	if err != nil {
		t.Fatal(err)
	}
	ui := loadPageInventory(t)
	var d parity.Differences
	if err := parity.Load("commands", &d); err != nil {
		t.Fatal(err)
	}
	dispatched := dispatchedCommands(t)
	run := func(spec *wire.CLISpec, dispatched []string, ui *pageInventory) string {
		return strings.Join(d.Check("commands", commandItems(spec, dispatched), commandWeb(spec, ui)), "\n")
	}
	if got := run(spec, dispatched, &ui); got != "" {
		t.Fatalf("the real data does not pass: %s", got)
	}

	if got := run(spec, append(slices.Clone(dispatched), "zzz-new-command"), &ui); !strings.Contains(got, `"zzz-new-command" exists in the terminal interface and not in the web interface`) {
		t.Errorf("a command the program dispatches and the spec lacks passed:\n%s", got)
	}

	fake := *spec
	fake.Commands = append(slices.Clone(spec.Commands), wire.CLICommand{Path: []string{"zzzfake"}, Mode: "tty_only", Why: "needs a terminal"})
	if got := run(&fake, dispatched, &ui); !strings.Contains(got, `"zzzfake" exists in the terminal interface and not in the web interface`) {
		t.Errorf("a command that needs a terminal and has no equivalent passed:\n%s", got)
	}

	noScreen := clonePageInventory(t, ui)
	for i, p := range noScreen.Palette {
		if p.Name == "sleipnir login" {
			noScreen.Palette[i].Effects = []string{"runner:login"}
		}
	}
	if got := run(spec, dispatched, &noScreen); !strings.Contains(got, `"login" exists in the terminal interface and not in the web interface`) {
		t.Errorf("a command whose screen the page lost passed:\n%s", got)
	}

	noDialog := clonePageInventory(t, ui)
	for i, p := range noDialog.Palette {
		noDialog.Palette[i].Effects = slices.DeleteFunc(slices.Clone(p.Effects), func(e string) bool { return e == "dialog:newSession" })
	}
	if got := run(spec, dispatched, &noDialog); !strings.Contains(got, `"chat" exists in the terminal interface and not in the web interface`) {
		t.Errorf("chat without the New session dialog passed:\n%s", got)
	}
}

// The commands the program dispatches that the CLI spec leaves out are the hidden tools and nothing else: a new command that is not in
// the spec fails here and in the comparison above.
func TestDispatchedCommandsAreInTheSpecOrHidden(t *testing.T) {
	spec, err := clispec.Spec()
	if err != nil {
		t.Fatal(err)
	}
	inSpec := map[string]bool{}
	for _, c := range spec.Commands {
		inSpec[c.Path[0]] = true
	}
	var missing []string
	for _, name := range dispatchedCommands(t) {
		if !inSpec[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	missing = slices.Compact(missing)
	var d parity.Differences
	if err := parity.Load("commands", &d); err != nil {
		t.Fatal(err)
	}
	var hidden []string
	for _, e := range d.TerminalOnly {
		if !strings.Contains(e.Item, " ") && !inSpec[e.Item] {
			hidden = append(hidden, e.Item)
		}
	}
	sort.Strings(hidden)
	if !slices.Equal(missing, hidden) {
		t.Errorf("the program dispatches %v and the spec does not list them; contract/commands.json lists %v as hidden tools", missing, hidden)
	}
}

// ---- the chat flags --------------------------------------------------------------------------------------------------------

// The flags of `sleipnir chat` in the spec are the flags the program registers, the New session dialog builds only those, and the
// arguments the host makes from a New session request are the flags the dialog's command line shows and parse as `sleipnir chat`
// parses them. The Run settings controls name flags the host stages or restarts with.
func TestParityChatFlagsAreTheCodes(t *testing.T) {
	spec, err := clispec.Spec()
	if err != nil {
		t.Fatal(err)
	}
	ui := loadPageInventory(t)

	fs := flag.NewFlagSet("chat", flag.ContinueOnError)
	registerChatFlags(fs)
	var code []string
	fs.VisitAll(func(f *flag.Flag) { code = append(code, f.Name) })
	sort.Strings(code)
	var inSpec []string
	for _, c := range spec.Commands {
		if len(c.Path) == 1 && c.Path[0] == "chat" {
			for _, f := range c.Flags {
				inSpec = append(inSpec, f.Name)
			}
		}
	}
	sort.Strings(inSpec)
	if !slices.Equal(code, inSpec) {
		t.Errorf("the flags `sleipnir chat` registers are %v and the CLI spec lists %v: regenerate it with sh scripts/gen-cli-docs.sh and sh scripts/gen-clispec.sh", code, inSpec)
	}
	for _, f := range ui.NewSession.Flags {
		if !slices.Contains(code, f) {
			t.Errorf("the New session dialog builds --%s, which `sleipnir chat` does not register", f)
		}
	}

	// the arguments the host makes of a request that sets every field are the flags of the dialog's command line, less --resume (a
	// field of its own in the request) and --mailman=false (the route adds it against a default of on)
	zero, usd := 3, 5.0
	args := argsFor(wire.NewSessionRequest{Cwd: "/p", Model: "a/m", Mode: "plan", Swarm: &zero, Isolation: "worktree", Verify: "go test {dirs}", Commit: true,
		Mailman: true, Budget: &usd, Rules: []string{"tests"}, TrustProject: true, NoMcp: true, RoleModels: map[string]string{"backend": "a/w"}})
	var made []string
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			made = append(made, strings.TrimPrefix(a, "--"))
		}
	}
	sort.Strings(made)
	var page []string
	for _, f := range ui.NewSession.Flags {
		if f != "resume" {
			page = append(page, f)
		}
	}
	sort.Strings(page)
	if !slices.Equal(made, page) {
		t.Errorf("the host makes the flags %v of a New session request and the dialog's command line has %v", made, page)
	}
	if _, err := parseChatFlags(args); err != nil {
		t.Errorf("`sleipnir chat` does not accept what the host makes of a New session request: %v", err)
	}

	// Run settings: a control that sets a flag is a field the host stages for the next start (wire.LaunchPatch), or the team size
	staged := map[string]bool{"isolation": true, "verify": true, "commit": true, "mailman": true, "no-mcp": true, "trust-project": true}
	var patch map[string]json.RawMessage
	b, _ := json.Marshal(wire.LaunchPatch{Isolation: new(string), Verify: new(string), Commit: new(bool), Mailman: new(bool), NoMcp: new(bool), TrustProject: new(bool)})
	_ = json.Unmarshal(b, &patch)
	camel := func(flag string) string {
		parts := strings.Split(flag, "-")
		for i := 1; i < len(parts); i++ {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
		return strings.Join(parts, "")
	}
	for _, p := range ui.Settings {
		for _, c := range p.Controls {
			if c.Flag == "" || c.Flag == "swarm" {
				continue
			}
			if _, ok := patch[camel(c.Flag)]; !ok || !staged[c.Flag] {
				t.Errorf("Settings > %s: %s sets --%s, which the host does not stage for the next start (wire.LaunchPatch)", p.ID, c.ID, c.Flag)
			}
		}
	}
	for _, f := range ui.RunLine.Flags {
		if !slices.Contains(code, f) {
			t.Errorf("the Run settings command line has --%s, which `sleipnir chat` does not register", f)
		}
	}
}

// ---- the slash commands ----------------------------------------------------------------------------------------------------

// slashItems are the slash commands of the terminal interface: the table of the chat (chatCommands), as "/name".
func slashItems() []string {
	var out []string
	for _, c := range chatCommands {
		out = append(out, "/"+c.name)
	}
	return out
}

// slashWeb are the slash commands the page can run: every command the server lists for a session (the built-ins, not the custom ones)
// that the page has a handler for or the server runs for it (POST .../command), and every command the page has a handler for.
func slashWeb(route []string, handlers map[string][]string, known func(cmd string) bool) []string {
	var out []string
	for _, c := range route {
		if _, ok := handlers[c]; ok || known(c) {
			out = append(out, c)
		}
	}
	for c := range handlers {
		out = append(out, c)
	}
	return out
}

// pointsToThePage are the slash commands that the server answers only by pointing at the page (a setting of the page, the Settings
// page, the tab's close button): the page has to have a handler for each, or typing one in the composer reaches a dead end.
var pointsToThePage = []string{"/verbose", "/anim", "/login", "/exit"}

// slashRoute is what a session of a real host lists for its / menu, and whether the host knows a command.
func slashRoute(t *testing.T) (builtin, custom []string, known func(string) bool, post func(line string) wire.CommandResult) {
	t.Helper()
	project := webWorld(t)
	_, base := newWebModel(t, sayScript("ok"))
	r := newWebRig(t, soloDefaults(project), base)
	tab := r.ready("")
	var body struct {
		Slash []wire.SlashEntry `json:"slash"`
	}
	r.json("GET", "/api/sessions/"+tab.ID+"/slash", nil, 200, &body)
	for _, e := range body.Slash {
		if e.Custom {
			custom = append(custom, e.Cmd)
		} else {
			builtin = append(builtin, e.Cmd)
		}
	}
	s := r.h.tab(tab.ID).session()
	if s == nil {
		t.Fatal("the session of the tab did not start")
	}
	known = func(cmd string) bool { return knownCommand(s, cmd) }
	post = func(line string) wire.CommandResult {
		var res wire.CommandResult
		r.json("POST", "/api/sessions/"+tab.ID+"/command", wire.CommandRequest{Line: line}, 200, &res)
		return res
	}
	return builtin, custom, known, post
}

// The slash commands: the / menu of a real session lists the commands of the chat, no more and no fewer; the page runs every one of
// them, with a handler of its own or through the server; and a command the server only points at the page has a handler there.
func TestParitySlash(t *testing.T) {
	ui := loadPageInventory(t)
	builtin, custom, known, post := slashRoute(t)
	t.Logf("the / menu of the session lists %d built-in commands and %d of your own", len(builtin), len(custom))

	checkContract(t, "slash", slashItems(), slashWeb(builtin, ui.Slash.Handlers, known))

	spec, err := clispec.Spec()
	if err != nil {
		t.Fatal(err)
	}
	var fallback []string
	for _, e := range spec.ChatSlash {
		fallback = append(fallback, e.Cmd)
	}
	sort.Strings(fallback)
	sorted := slices.Clone(builtin)
	sort.Strings(sorted)
	if !slices.Equal(fallback, sorted) {
		t.Errorf("the / menu the page shows before the server answers (chatSlash in clispec.json) is %v and the server lists %v: regenerate the spec with sh scripts/gen-clispec.sh", fallback, sorted)
	}
	for _, cmd := range pointsToThePage {
		res := post(cmd)
		if strings.TrimSpace(res.Output) == "" {
			t.Errorf("%s: the server says nothing", cmd)
		}
		if _, ok := ui.Slash.Handlers[cmd]; !ok {
			t.Errorf("%s: the server only points at the page (%q) and the page has no handler for it", cmd, res.Output)
		}
	}
	for cmd := range ui.Slash.Handlers {
		if !slices.Contains(slashItems(), cmd) {
			t.Errorf("the page has a handler for %s, which the chat does not have", cmd)
		}
	}
}

// A slash command the chat gains, one the server stops listing, and a handler the page loses each fail the comparison.
func TestParitySlashFailsOnDrift(t *testing.T) {
	ui := loadPageInventory(t)
	builtin, _, known, _ := slashRoute(t)
	var d parity.Differences
	if err := parity.Load("slash", &d); err != nil {
		t.Fatal(err)
	}
	run := func(terminal, route []string, handlers map[string][]string, known func(string) bool) string {
		return strings.Join(d.Check("slash", terminal, slashWeb(route, handlers, known)), "\n")
	}
	if got := run(slashItems(), builtin, ui.Slash.Handlers, known); got != "" {
		t.Fatalf("the real data does not pass: %s", got)
	}

	// a command added to chatCommands that the / menu (chatHelp) does not list and the page has no handler for
	saved := chatCommands
	chatCommands = append(slices.Clone(chatCommands), chatCommand{"zzfake", "", "a command only this test adds"})
	got := run(slashItems(), builtin, ui.Slash.Handlers, known)
	chatCommands = saved
	if !strings.Contains(got, `"/zzfake" exists in the terminal interface and not in the web interface`) {
		t.Errorf("a command added to chatCommands passed:\n%s", got)
	}

	// the server stops listing one
	if got := run(slashItems(), builtin[1:], withoutKey(ui.Slash.Handlers, builtin[0]), func(string) bool { return false }); !strings.Contains(got, `"`+builtin[0]+`" exists in the terminal interface and not in the web interface`) {
		t.Errorf("a command the server does not list and the page cannot run passed:\n%s", got)
	}

	// the page has a handler for a command the chat does not have
	extra := map[string][]string{"/zzpage": {"view:cache"}}
	for k, v := range ui.Slash.Handlers {
		extra[k] = v
	}
	if got := run(slashItems(), builtin, extra, known); !strings.Contains(got, `"/zzpage" exists in the web interface and not in the terminal interface`) {
		t.Errorf("a page handler for a command nobody has passed:\n%s", got)
	}
}

// withoutKey is m without key k.
func withoutKey(m map[string][]string, k string) map[string][]string {
	out := map[string][]string{}
	for key, v := range m {
		if key != k {
			out[key] = v
		}
	}
	return out
}

// The menu of the server is made from the text of /help and the palette's table: they list the same commands, so that a command added
// to one and not the other cannot reach one interface only.
func TestTheHelpTextAndTheTableListTheSameSlashCommands(t *testing.T) {
	var help []string
	for _, e := range builtinSlash() {
		help = append(help, e.Cmd)
	}
	sort.Strings(help)
	table := slashItems()
	sort.Strings(table)
	if !slices.Equal(help, table) {
		t.Errorf("/help lists %v and chatCommands lists %v", help, table)
	}
}
