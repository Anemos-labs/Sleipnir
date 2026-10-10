package parity

// The parity guard of the flags, the views and the settings: each lists what the terminal interface has from the code (the CLI spec,
// the terminal program's views, the configuration schema) and what the web interface has from the inventory of the page
// (webui.json, made by internal/web/uidev/inventory.mjs), and compares them with Differences.Check. The commands and the slash commands
// need package main and are compared in cmd/sleipnir/parity_test.go.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/tui/app"
	"github.com/anemos-labs/sleipnir/internal/web/clispec"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// ---- the inventory of the page ----------------------------------------------------------------------------------------------

// viewEntry is a view the page registers.
type viewEntry struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Nav   bool   `json:"nav"`
}

// settingControl is a control of a Settings page and what it is: the scope says what it changes (config, flag, state, view, browser).
type settingControl struct {
	ID    string   `json:"id"`
	Scope string   `json:"scope"`
	Keys  []string `json:"keys"`
	Flag  string   `json:"flag"`
	Note  string   `json:"note"`
}

// uiInventory is webui.json: what the page offers, as internal/web/uidev/inventory.mjs lists it.
type uiInventory struct {
	Generated         string      `json:"generated"`
	SpecGeneratedFrom string      `json:"specGeneratedFrom"`
	Views             []viewEntry `json:"views"`
	Nav               []struct {
		ID    string  `json:"id"`
		View  *string `json:"view"`
		Tab   *string `json:"tab"`
		Label string  `json:"label"`
		Key   string  `json:"key"`
		Radio bool    `json:"radio"`
	} `json:"nav"`
	Palette []struct {
		Group   string   `json:"group"`
		Name    string   `json:"name"`
		Kind    string   `json:"kind"`
		Live    bool     `json:"live"`
		Effects []string `json:"effects"`
	} `json:"palette"`
	Slash struct {
		Handlers map[string][]string `json:"handlers"`
		Menu     []string            `json:"menu"`
	} `json:"slash"`
	Settings []struct {
		ID       string           `json:"id"`
		Title    string           `json:"title"`
		Group    string           `json:"group"`
		Summary  string           `json:"summary"`
		Controls []settingControl `json:"controls"`
	} `json:"settings"`
	UnclassifiedSettings []string `json:"unclassifiedSettings"`
	NewSession           struct {
		Title           string   `json:"title"`
		Labels          []string `json:"labels"`
		Checkboxes      []string `json:"checkboxes"`
		ControlIDs      []string `json:"controlIds"`
		CommandLine     string   `json:"commandLine"`
		Flags           []string `json:"flags"`
		FlagsMailmanOff []string `json:"flagsMailmanOff"`
		LabelFlags      []string `json:"labelFlags"`
		ControlFlags    []string `json:"controlFlags"`
		Modes           []string `json:"modes"`
	} `json:"newSession"`
	Resume struct {
		Title          string   `json:"title"`
		Flags          []string `json:"flags"`
		LatestButton   bool     `json:"latestButton"`
		RowButton      bool     `json:"rowButton"`
		SettingsButton bool     `json:"settingsButton"`
	} `json:"resume"`
	RunLine struct {
		Line  string   `json:"line"`
		Flags []string `json:"flags"`
	} `json:"runLine"`
	Runner struct {
		Listed   []string `json:"listed"`
		Commands map[string]struct {
			Mode        string            `json:"mode"`
			Positionals []string          `json:"positionals"`
			Flags       []string          `json:"flags"`
			ModeWith    map[string]string `json:"modeWith"`
		} `json:"commands"`
	} `json:"runner"`
}

// loadPageUI reads webui.json, refusing a field this type does not have.
func loadPageUI(t testing.TB) uiInventory {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(Dir(), "..", "webui.json"))
	if err != nil {
		t.Fatalf("%v: generate the inventory of the page with sh scripts/gen-webui-inventory.sh", err)
	}
	var ui uiInventory
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ui); err != nil {
		t.Fatalf("webui.json: %v", err)
	}
	if len(ui.UnclassifiedSettings) > 0 {
		t.Fatalf("webui.json has Settings controls nobody classified: %v", ui.UnclassifiedSettings)
	}
	return ui
}

// loadCLISpec is the CLI spec of the checkout.
func loadCLISpec(t testing.TB) *wire.CLISpec {
	t.Helper()
	spec, err := clispec.Spec()
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

// checkContract loads the contract of a dimension, reports every problem of the comparison and logs the gaps.
func checkContract(t *testing.T, dimension string, terminal, web []string) {
	t.Helper()
	var d Differences
	if err := Load(dimension, &d); err != nil {
		t.Fatalf("contract/%s.json: %v", dimension, err)
	}
	for _, p := range d.Check(dimension, terminal, web) {
		t.Error(p)
	}
	for _, g := range d.GapLines(dimension) {
		t.Log(g)
	}
}

// inList reports whether list holds s.
func inList(list []string, s string) bool { return slices.Contains(list, s) }

// ---- the flags -------------------------------------------------------------------------------------------------------------

// flagItems are the flags and the positionals of every command of the spec, as "sleipnir <path> --flag" and "<path> NAME" items.
func flagItems(spec *wire.CLISpec) []string {
	var out []string
	for _, c := range spec.Commands {
		key := strings.Join(c.Path, " ")
		for _, f := range c.Flags {
			out = append(out, key+" --"+f.Name)
		}
		for _, p := range c.Positional {
			out = append(out, key+" "+p.Name)
		}
	}
	return out
}

// flagMirror is a flag that the page offers in a place other than a form of the command: the item, what the place is, and the
// evidence in the inventory that the place is there.
type flagMirror struct {
	item  string
	where string
	ok    func(ui *uiInventory) bool
}

// needHandler is the evidence that the page has a handler for a slash command.
func needHandler(cmd string) func(ui *uiInventory) bool {
	return func(ui *uiInventory) bool { _, ok := ui.Slash.Handlers[cmd]; return ok }
}

// needControl is the evidence that a Settings page has a control.
func needControl(page, id string) func(ui *uiInventory) bool {
	return func(ui *uiInventory) bool {
		for _, p := range ui.Settings {
			if p.ID == page {
				for _, c := range p.Controls {
					if c.ID == id {
						return true
					}
				}
			}
		}
		return false
	}
}

// needViews is the evidence that the page registers the views.
func needViews(names ...string) func(ui *uiInventory) bool {
	return func(ui *uiInventory) bool {
		for _, n := range names {
			found := false
			for _, v := range ui.Views {
				found = found || v.Name == n
			}
			if !found {
				return false
			}
		}
		return true
	}
}

// allOf is the evidence that every one of the checks holds.
func allOf(fs ...func(ui *uiInventory) bool) func(ui *uiInventory) bool {
	return func(ui *uiInventory) bool {
		for _, f := range fs {
			if !f(ui) {
				return false
			}
		}
		return true
	}
}

// flagMirrors are the flags of the commands that the page cannot run (chat, watch) whose function the page has in another place.
// A flag is the page's only when the evidence is in the inventory: a removed control makes it a difference again.
var flagMirrors = []flagMirror{
	{"chat --resume", "the Resume buttons of the Resume dialog and its \"with settings\" button, which opens New session for the recorded session", func(ui *uiInventory) bool {
		return ui.Resume.RowButton && ui.Resume.SettingsButton && inList(ui.NewSession.Flags, "resume")
	}},
	{"chat --continue", "the Resume dialog's --continue button", func(ui *uiInventory) bool { return ui.Resume.LatestButton && inList(ui.Resume.Flags, "continue") }},
	{"chat --verbose", "the /verbose handler of the composer", needHandler("/verbose")},
	{"chat --no-anim", "Settings > Appearance & motion and /anim", allOf(needHandler("/anim"), needControl("look", "seg:motion"))},
	{"watch --no-anim", "Settings > Appearance & motion and /anim", allOf(needHandler("/anim"), needControl("look", "seg:motion"))},
	{"watch --view", "the Cockpit, Cache, Mail and Board views of the rail", needViews("cockpit", "cache", "mail", "board")},
	{"watch --agent", "the agent rows of the Cache view", needViews("cache")},
	{"watch SESSION", "the Sessions view, which opens a recorded session or follows one another process writes", needViews("sessions")},
	{"login PROVIDER", "the Sign in button of each provider in Settings > Providers & login, which shows the command for that provider", needControl("providers", "prov*")},
}

// runnable reports whether the page's runner can run the command: in its mode with no flag set, or with a flag that changes it.
func runnable(mode string, with map[string]string) bool {
	if mode != "tty_only" {
		return true
	}
	for _, m := range with {
		if m != "tty_only" {
			return true
		}
	}
	return false
}

// flagWeb are the flags and positionals the page can set: the controls of the form of every command the runner can run (the form is
// built by the page from the spec), the flags the New session dialog builds for `sleipnir chat`, and the mirrors.
func flagWeb(ui *uiInventory) []string {
	var out []string
	for key, c := range ui.Runner.Commands {
		if !runnable(c.Mode, c.ModeWith) {
			continue
		}
		for _, f := range c.Flags {
			out = append(out, key+" --"+f)
		}
		for _, p := range c.Positionals {
			out = append(out, key+" "+p)
		}
	}
	for _, f := range ui.NewSession.ControlFlags {
		if inList(ui.NewSession.Flags, f) {
			out = append(out, "chat --"+f)
		}
	}
	for _, m := range flagMirrors {
		if m.ok(ui) {
			out = append(out, m.item)
		}
	}
	return out
}

// chatFlagNames are the flags `sleipnir chat` has, from the spec.
func chatFlagNames(spec *wire.CLISpec) []string {
	for _, c := range spec.Commands {
		if len(c.Path) == 1 && c.Path[0] == "chat" {
			var out []string
			for _, f := range c.Flags {
				out = append(out, f.Name)
			}
			return out
		}
	}
	return nil
}

// flagControlProblems are the flags that a hand-built control of the page names and `sleipnir chat` does not have.
func flagControlProblems(spec *wire.CLISpec, ui *uiInventory) []string {
	chat := chatFlagNames(spec)
	var out []string
	for _, f := range ui.NewSession.Flags {
		if !inList(chat, f) {
			out = append(out, fmt.Sprintf("the New session dialog builds --%s, which `sleipnir chat` does not have", f))
		}
	}
	for _, f := range ui.NewSession.ControlFlags {
		if !inList(ui.NewSession.Flags, f) {
			out = append(out, fmt.Sprintf("a control of the New session dialog sets --%s but its command line never builds it", f))
		}
	}
	for _, f := range ui.NewSession.Flags {
		if f != "resume" && !inList(ui.NewSession.ControlFlags, f) {
			out = append(out, fmt.Sprintf("the command line of the New session dialog builds --%s but no control of the dialog sets it", f))
		}
	}
	for _, f := range ui.RunLine.Flags {
		if !inList(chat, f) {
			out = append(out, fmt.Sprintf("the Run settings command line has --%s, which `sleipnir chat` does not have", f))
		}
	}
	for _, p := range ui.Settings {
		for _, c := range p.Controls {
			if c.Flag != "" && !inList(chat, c.Flag) {
				out = append(out, fmt.Sprintf("Settings > %s: %s sets --%s, which `sleipnir chat` does not have", p.Title, c.ID, c.Flag))
			}
		}
	}
	return out
}

// The flags: every flag and positional of every command has a control on the page or is listed with its reason. The forms of the runner
// are built from the spec by the page, so what this guards is that the page still builds them, that a command the runner can only
// refuse (chat, watch) has its flags somewhere else, and that every flag the hand-built controls name is a real flag.
func TestParityFlags(t *testing.T) {
	spec, ui := loadCLISpec(t), loadPageUI(t)
	for _, p := range flagControlProblems(spec, &ui) {
		t.Error(p)
	}
	for _, m := range flagMirrors {
		if m.ok(&ui) {
			t.Logf("flags: %s is offered by %s", m.item, m.where)
		}
	}
	checkContract(t, "flags", flagItems(spec), flagWeb(&ui))
}

// A flag the spec gains, a form the page stops building, a control that names a flag nobody has, and a mirror whose control is gone
// each fail the comparison.
func TestParityFlagsFailOnDrift(t *testing.T) {
	spec, ui := loadCLISpec(t), loadPageUI(t)
	var d Differences
	if err := Load("flags", &d); err != nil {
		t.Fatal(err)
	}
	run := func(spec *wire.CLISpec, ui *uiInventory) string {
		return strings.Join(append(flagControlProblems(spec, ui), d.Check("flags", flagItems(spec), flagWeb(ui))...), "\n")
	}
	if got := run(spec, &ui); got != "" {
		t.Fatalf("the real data does not pass: %s", got)
	}

	fake := *spec
	fake.Commands = append(slices.Clone(spec.Commands), wire.CLICommand{Path: []string{"sessions", "prune"}, Mode: "run", Flags: []wire.CLIFlag{{Name: "zzz-new-flag"}}})
	fake.Commands[len(fake.Commands)-1].Path = []string{"zzzfake"}
	if got := run(&fake, &ui); !strings.Contains(got, `"zzzfake --zzz-new-flag" exists in the terminal interface and not in the web interface`) {
		t.Errorf("a new CLI flag passed the comparison:\n%s", got)
	}

	noForm := cloneInventory(t, &ui)
	delete(noForm.Runner.Commands, "sessions prune")
	if got := run(spec, &noForm); !strings.Contains(got, `"sessions prune --keep"`) {
		t.Errorf("a form the page no longer builds passed:\n%s", got)
	}

	noChatFlag := cloneInventory(t, &ui)
	noChatFlag.NewSession.Flags = dropFirst(noChatFlag.NewSession.Flags, "budget-usd")
	if got := run(spec, &noChatFlag); !strings.Contains(got, `"chat --budget-usd"`) {
		t.Errorf("a chat flag the New session dialog's command line lost passed:\n%s", got)
	}

	noControl := cloneInventory(t, &ui)
	noControl.NewSession.ControlFlags = dropFirst(noControl.NewSession.ControlFlags, "commit")
	if got := run(spec, &noControl); !strings.Contains(got, `"chat --commit" exists in the terminal interface`) || !strings.Contains(got, "builds --commit but no control of the dialog sets it") {
		t.Errorf("a chat flag whose control the New session dialog lost passed:\n%s", got)
	}

	phantom := cloneInventory(t, &ui)
	phantom.NewSession.Flags = append(phantom.NewSession.Flags, "no-such-flag")
	if got := run(spec, &phantom); !strings.Contains(got, "--no-such-flag, which `sleipnir chat` does not have") {
		t.Errorf("a control for a flag nobody has passed:\n%s", got)
	}

	noMirror := cloneInventory(t, &ui)
	delete(noMirror.Slash.Handlers, "/verbose")
	if got := run(spec, &noMirror); !strings.Contains(got, `"chat --verbose" exists in the terminal interface and not in the web interface`) {
		t.Errorf("a mirror whose control is gone passed:\n%s", got)
	}
}

// cloneInventory copies the inventory through JSON, so that a test may change the copy.
func cloneInventory(t testing.TB, ui *uiInventory) uiInventory {
	t.Helper()
	b, err := json.Marshal(ui)
	if err != nil {
		t.Fatal(err)
	}
	var out uiInventory
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// dropFirst is list with the first occurrence of s removed.
func dropFirst(list []string, s string) []string {
	out := slices.Clone(list)
	if i := slices.Index(out, s); i >= 0 {
		out = slices.Delete(out, i, i+1)
	}
	return out
}

// ---- the views -------------------------------------------------------------------------------------------------------------

// chatPages are the pages of the whole screen that the chat program draws on the alternate screen, with the view of the page that
// stands for each. The terminal draws them from a slash command or a key; the page has them as views.
var chatPages = map[string]string{"chat:stats": "cache", "chat:cockpit": "cockpit"}

// chatPageCalls counts the places in the chat program that put a page of the whole screen up, from the source of internal/tui/app:
// every call of SetFull with something other than nil. A new page there is one more than the table above lists.
func chatPageCalls(t testing.TB) int {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(Dir(), "..", "..", "tui", "app", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("the source of internal/tui/app was not found: %v", err)
	}
	re := regexp.MustCompile(`\.SetFull\(([^)]*)\)`)
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(src), -1) {
			if strings.TrimSpace(m[1]) != "nil" {
				n++
			}
		}
	}
	return n
}

// viewItems are the screens of the terminal interface: the views of the watch and replay programs (from app.View), the pages of the
// chat program, and the replay screen when the CLI has a replay command.
func viewItems(spec *wire.CLISpec) []string {
	var out []string
	for i := 0; i < 255; i++ {
		name := app.View(i).String()
		if strings.HasPrefix(name, "view") {
			break
		}
		out = append(out, name)
	}
	for page := range chatPages {
		out = append(out, page)
	}
	for _, c := range spec.Commands {
		if len(c.Path) == 1 && c.Path[0] == "replay" {
			out = append(out, "replay")
		}
	}
	sort.Strings(out)
	return out
}

// viewWeb are the screens of the page: every registered view, the Radio rail, the four tabs of the Workspace, and the chat pages of
// the terminal whose view the page has.
func viewWeb(ui *uiInventory) []string {
	var out []string
	registered := map[string]bool{}
	for _, v := range ui.Views {
		out = append(out, v.Name)
		registered[v.Name] = true
	}
	for _, n := range ui.Nav {
		if n.Radio {
			out = append(out, n.ID)
		}
		if n.Tab != nil {
			out = append(out, *n.View+"/"+*n.Tab)
		}
	}
	for page, view := range chatPages {
		if registered[view] {
			out = append(out, page)
		}
	}
	return out
}

// viewReachability are the registered views that neither the rail nor the palette opens, and the entries that lead to nothing.
func viewReachability(ui *uiInventory) []string {
	registered := map[string]bool{}
	for _, v := range ui.Views {
		registered[v.Name] = true
	}
	var out []string
	for _, v := range ui.Views {
		found := false
		for _, n := range ui.Nav {
			found = found || (n.View != nil && *n.View == v.Name)
		}
		for _, p := range ui.Palette {
			found = found || inList(p.Effects, "view:"+v.Name)
		}
		if !found {
			out = append(out, fmt.Sprintf("the view %q is registered but neither the rail nor the palette opens it", v.Name))
		}
	}
	for _, n := range ui.Nav {
		if n.View != nil && !registered[*n.View] {
			out = append(out, fmt.Sprintf("the rail item %q opens the view %q, which is not registered", n.ID, *n.View))
		}
	}
	for _, p := range ui.Palette {
		for _, e := range p.Effects {
			if v, ok := strings.CutPrefix(e, "view:"); ok && !registered[v] {
				out = append(out, fmt.Sprintf("the palette entry %q opens the view %q, which is not registered", p.Name, v))
			}
		}
	}
	return out
}

// The views: every screen of the terminal interface (the four views of watch and replay, the stats page and the cockpit of the chat,
// the replay screen) is a view of the page or is listed; every view of the page is reachable and is a screen of the terminal or listed.
func TestParityViews(t *testing.T) {
	spec, ui := loadCLISpec(t), loadPageUI(t)
	if n := chatPageCalls(t); n != len(chatPages) {
		t.Errorf("the chat program puts up %d pages of the whole screen (SetFull in internal/tui/app) and this test knows %d (chatPages): a new page is a new view; give it a counterpart on the page or list it in contract/views.json", n, len(chatPages))
	}
	for _, p := range viewReachability(&ui) {
		t.Error(p)
	}
	checkContract(t, "views", viewItems(spec), viewWeb(&ui))
}

// A view the terminal gains, a view or a rail item the page loses, and a view nobody can open each fail the comparison.
func TestParityViewsFailOnDrift(t *testing.T) {
	spec, ui := loadCLISpec(t), loadPageUI(t)
	var d Differences
	if err := Load("views", &d); err != nil {
		t.Fatal(err)
	}
	run := func(spec *wire.CLISpec, ui *uiInventory) string {
		return strings.Join(append(viewReachability(ui), d.Check("views", viewItems(spec), viewWeb(ui))...), "\n")
	}
	if got := run(spec, &ui); got != "" {
		t.Fatalf("the real data does not pass: %s", got)
	}

	noMail := cloneInventory(t, &ui)
	noMail.Views = slices.DeleteFunc(noMail.Views, func(v viewEntry) bool { return v.Name == "mail" })
	if got := run(spec, &noMail); !strings.Contains(got, `"mail" exists in the terminal interface and not in the web interface`) {
		t.Errorf("a view the page lost passed:\n%s", got)
	}

	extra := cloneInventory(t, &ui)
	extra.Views = append(extra.Views, viewEntry{"zzz", "Zzz", true})
	got := run(spec, &extra)
	if !strings.Contains(got, `"zzz" exists in the web interface and not in the terminal interface`) || !strings.Contains(got, `the view "zzz" is registered but neither the rail nor the palette opens it`) {
		t.Errorf("a view only the page has, and that nobody opens, passed:\n%s", got)
	}

	// a terminal view with no counterpart: the names the terminal program knows are the names of app.View; one more would be listed
	fake := append(viewItems(spec), "zzz-terminal-view")
	if got := strings.Join(d.Check("views", fake, viewWeb(&ui)), "\n"); !strings.Contains(got, `"zzz-terminal-view" exists in the terminal interface and not in the web interface`) {
		t.Errorf("a terminal view with no counterpart passed:\n%s", got)
	}
}

// ---- the settings ----------------------------------------------------------------------------------------------------------

// schemaKeys are the settings of the configuration, from the type config.Config and its json tags: every field of a section, with a
// name or a star for the entries of a map (providers.*.base_url, models.roles.*).
func schemaKeys() []string {
	var out []string
	var walk func(t reflect.Type, prefix string)
	walk = func(t reflect.Type, prefix string) {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if !f.IsExported() || tag == "-" {
				continue
			}
			name, _, _ := strings.Cut(tag, ",")
			if name == "" {
				name = f.Name
			}
			path := name
			if prefix != "" {
				path = prefix + "." + name
			}
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			switch {
			case ft.Kind() == reflect.Struct:
				walk(ft, path)
			case ft.Kind() == reflect.Map && derefType(ft.Elem()).Kind() == reflect.Struct:
				walk(ft.Elem(), path+".*")
			case ft.Kind() == reflect.Map:
				out = append(out, path+".*")
			default:
				out = append(out, path)
			}
		}
	}
	walk(reflect.TypeOf(config.Config{}), "")
	sort.Strings(out)
	return out
}

// derefType is t without its pointers.
func derefType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// settingItems are the settings of the configuration, each a key of the schema.
func settingItems() []string { return schemaKeys() }

// settingWeb are what the Settings pages change: the keys of the controls that change configuration, and the controls that change
// state that is not configuration or this browser's own settings ("state:" and "browser:" items, which the terminal does not have).
func settingWeb(ui *uiInventory) []string {
	var out []string
	for _, p := range ui.Settings {
		for _, c := range p.Controls {
			switch c.Scope {
			case "config":
				out = append(out, c.Keys...)
			case "state", "browser":
				out = append(out, c.Scope+":"+p.ID+"."+c.ID)
			}
		}
	}
	return out
}

// The settings: every key of the configuration that the page can change is a key that exists, and every key it cannot is listed with
// the reason; the settings of the page that are no key of the configuration are listed too. A control of the page that sets a flag
// names a flag of `sleipnir chat` (TestParityFlags).
func TestParitySettings(t *testing.T) {
	ui := loadPageUI(t)
	checkContract(t, "settings", settingItems(), settingWeb(&ui))
	for _, p := range ui.Settings {
		if len(p.Controls) == 0 {
			t.Errorf("Settings > %s has no control the inventory could find", p.Title)
		}
	}
}

// A key the configuration gains, a control the page loses, and a control that names a key nobody has each fail the comparison.
func TestParitySettingsFailOnDrift(t *testing.T) {
	ui := loadPageUI(t)
	var d Differences
	if err := Load("settings", &d); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.Check("settings", settingItems(), settingWeb(&ui)), "\n"); got != "" {
		t.Fatalf("the real data does not pass: %s", got)
	}
	if got := strings.Join(d.Check("settings", append(settingItems(), "swarm.zzz_new_key"), settingWeb(&ui)), "\n"); !strings.Contains(got, `"swarm.zzz_new_key" exists in the terminal interface and not in the web interface`) {
		t.Errorf("a new configuration key passed:\n%s", got)
	}

	lost := cloneInventory(t, &ui)
	for pi := range lost.Settings {
		lost.Settings[pi].Controls = slices.DeleteFunc(lost.Settings[pi].Controls, func(c settingControl) bool {
			return c.ID == "do:budget" || c.ID == "do:budget-off" || c.ID == "id:bIn"
		})
	}
	if got := strings.Join(d.Check("settings", settingItems(), settingWeb(&lost)), "\n"); !strings.Contains(got, `"swarm.budget_usd" exists in the terminal interface and not in the web interface`) {
		t.Errorf("a Budget page without its controls passed:\n%s", got)
	}

	phantom := cloneInventory(t, &ui)
	phantom.Settings[0].Controls = append(phantom.Settings[0].Controls, settingControl{ID: "do:zzz", Scope: "config", Keys: []string{"swarm.no_such_key"}})
	if got := strings.Join(d.Check("settings", settingItems(), settingWeb(&phantom)), "\n"); !strings.Contains(got, `"swarm.no_such_key" exists in the web interface and not in the terminal interface`) {
		t.Errorf("a control for a key nobody has passed:\n%s", got)
	}
}

// The schema is read from the type: a field added to a section is a key, a map of structs is a key per field, and a field that is not
// in the file format is none.
func TestSchemaKeysAreReadFromTheConfigType(t *testing.T) {
	keys := schemaKeys()
	for _, want := range []string{"swarm.max_workers", "swarm.budget_usd", "models.roles.*", "models.favorites", "providers.*.base_url", "permissions.roles.*.mode", "hooks.*", "mcp.*", "cache.shared_ttl", "tools.web_allow_hosts"} {
		if !inList(keys, want) {
			t.Errorf("the schema has no %q: %v", want, keys)
		}
	}
	for _, k := range keys {
		if strings.Contains(k, "Extra") || strings.HasPrefix(k, "extra") {
			t.Errorf("a field that is not in the file format is a key: %q", k)
		}
	}
}
