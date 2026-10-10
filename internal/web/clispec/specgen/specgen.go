// Package specgen builds the CLI spec of `sleipnir web` (internal/web/clispec/clispec.json): every `sleipnir` command with its usage,
// summary, positionals and typed flags, the mode the web runs it in, the chat's slash commands and the exit codes. The flags are read
// from docs/CLI.md, whose <!-- flags: NAME --> blocks are the binary's own -h output (scripts/gen-cli-docs.sh keeps them current);
// the slash commands from the chatHelp text and the chatCommands table of cmd/sleipnir; what the help text states only in prose
// (positionals, enumerations, the subcommands of mcp, trust, models fav and schedule) is curated here, each fact next to the command it
// belongs to. The output is deterministic: the same inputs give the same bytes.
//
// It is the port of the mock's data/gen-cli-spec.mjs, plus the modes of CONTRACT.md 18.2.
package specgen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Inputs are the files the spec is built from.
type Inputs struct {
	// CLIDoc is docs/CLI.md.
	CLIDoc []byte
	// Sources are the non-test Go files of cmd/sleipnir, by base name (chatHelp, chatCommands and the usage text are read from them).
	Sources map[string][]byte
}

// ReadInputs reads the inputs from a checkout whose root is repo.
func ReadInputs(repo string) (Inputs, error) {
	in := Inputs{Sources: map[string][]byte{}}
	var err error
	if in.CLIDoc, err = os.ReadFile(filepath.Join(repo, "docs", "CLI.md")); err != nil {
		return in, err
	}
	files, err := filepath.Glob(filepath.Join(repo, "cmd", "sleipnir", "*.go"))
	if err != nil {
		return in, err
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			return in, err
		}
		in.Sources[filepath.Base(f)] = b
	}
	if len(in.Sources) == 0 {
		return in, fmt.Errorf("no Go file in %s", filepath.Join(repo, "cmd", "sleipnir"))
	}
	return in, nil
}

// Generator names the generator in the spec.
const Generator = "go generate ./internal/web/clispec (internal/web/clispec/specgen)"

// section is a part of CLI.md under one heading: its prose lines and its flag blocks.
type section struct {
	level  int
	title  string
	prose  []string
	blocks []block
}

// block is one <!-- flags: NAME --> listing.
type block struct {
	name string
	text []string
}

var (
	headingRE = regexp.MustCompile(`^(#{2,4}) (.+)$`)
	markerRE  = regexp.MustCompile(`^<!-- flags: (.+) -->$`)
)

// sections splits CLI.md by heading and collects the flag blocks of each.
func sections(md []string) ([]*section, error) {
	var out []*section
	var cur *section
	for i := 0; i < len(md); i++ {
		if h := headingRE.FindStringSubmatch(md[i]); h != nil {
			cur = &section{level: len(h[1]), title: strings.TrimSpace(h[2])}
			out = append(out, cur)
			continue
		}
		if b := markerRE.FindStringSubmatch(md[i]); b != nil && cur != nil {
			name := strings.TrimSpace(b[1])
			j := i + 1
			if j >= len(md) || !strings.HasPrefix(md[j], "```") {
				return nil, fmt.Errorf("docs/CLI.md:%d: a flags block without a fence", i+1)
			}
			var body []string
			for j++; j < len(md) && !strings.HasPrefix(md[j], "```"); j++ {
				body = append(body, md[j])
			}
			cur.blocks = append(cur.blocks, block{name: name, text: body})
			i = j
			continue
		}
		if cur != nil {
			cur.prose = append(cur.prose, md[i])
		}
	}
	return out, nil
}

var (
	flagLineRE = regexp.MustCompile(`^ {2}--?([A-Za-z][\w-]*)(?: (.+))?$`)
	flagDescRE = regexp.MustCompile(`^ {8}(.*)$`)
	defaultRE  = regexp.MustCompile(`\s*\(default (.+)\)$`)
	numberRE   = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)(?:;\s*(.*))?$`)
	quotedRE   = regexp.MustCompile(`^"(.*)"$`)
	wordRE     = regexp.MustCompile(`^[^\s,]+$`)
	noteEndRE  = regexp.MustCompile(`\s*\(default: ([^)]*)\)\s*$`)
	noteRE     = regexp.MustCompile(`\s*\(default: ([^)]*)\)`)
)

// parseFlagLines reads one listing of Go's flag package: "  -name type" then "        description"; the type word "value" is a
// repeatable flag (a custom flag.Value), no type word is a bool, any other word a string placeholder.
func parseFlagLines(lines []string) []wire.CLIFlag {
	type raw struct{ name, typeWord, desc string }
	var flags []*raw
	var f *raw
	for _, ln := range lines {
		if m := flagLineRE.FindStringSubmatch(ln); m != nil {
			f = &raw{name: m[1], typeWord: m[2]}
			flags = append(flags, f)
			continue
		}
		if m := flagDescRE.FindStringSubmatch(ln); m != nil && f != nil {
			if f.desc == "" {
				f.desc = m[1]
			} else {
				f.desc += " " + m[1]
			}
		}
	}
	out := make([]wire.CLIFlag, 0, len(flags))
	for _, r := range flags {
		out = append(out, normFlag(r.name, r.typeWord, r.desc))
	}
	return out
}

// normFlag types a flag and splits its default out of its description.
func normFlag(name, tw, desc string) wire.CLIFlag {
	arg, repeatable := "bool", false
	switch tw {
	case "":
	case "string", "int", "uint", "float", "duration":
		arg = tw
	case "value":
		arg, repeatable = "string", true
	default:
		arg = "string" // a backquoted placeholder, e.g. "sleipnir models"
	}
	var def any
	noteFromDefault := ""
	if dm := defaultRE.FindStringSubmatchIndex(desc); dm != nil {
		v := desc[dm[2]:dm[3]]
		desc = desc[:dm[0]]
		switch {
		case arg == "int" || arg == "uint" || arg == "float":
			// "(default 2; -1 disables)": the number is the default, the remark stays in the description
			if nm := numberRE.FindStringSubmatch(v); nm != nil {
				n, _ := strconv.ParseFloat(nm[1], 64)
				def = n
				if nm[2] != "" {
					desc += " (" + nm[2] + ")"
				}
			} else {
				noteFromDefault = v
			}
		case arg == "bool":
			def = v == "true"
		case quotedRE.MatchString(v):
			def = quotedRE.FindStringSubmatch(v)[1]
		case wordRE.MatchString(v):
			def = v
		default:
			noteFromDefault = v
		}
	}
	note := ""
	// "(default: the configured model)" is prose, not a value
	if dn := noteEndRE.FindStringSubmatch(desc); dn != nil {
		note = dn[1]
	} else if dn := noteRE.FindStringSubmatch(desc); dn != nil {
		note = dn[1]
	}
	if noteFromDefault != "" {
		note = noteFromDefault
	}
	return wire.CLIFlag{Name: name, Arg: arg, Default: def, DefaultNote: note, Repeatable: repeatable, Desc: strings.TrimSpace(desc)}
}

// pos is a curated positional.
func pos(name string, required bool, desc string) wire.CLIPositional {
	return wire.CLIPositional{Name: name, Required: required, Desc: desc}
}

// many is a curated positional that takes the remaining words.
func many(name string, required bool, desc string) wire.CLIPositional {
	return wire.CLIPositional{Name: name, Required: required, Desc: desc, Variadic: true}
}

const (
	sessionArg = "a session id (or the start of one), a session directory, an events.jsonl, or latest"
	goalArg    = "the goal: the remaining words, or - to read stdin"
	tasksFile  = "tasks file (JSON lines)"
)

// positionals are the positionals CLI.md states in prose, per command path.
var positionals = map[string][]wire.CLIPositional{
	"init": {}, "config": {}, "sessions": {}, "sessions prune": {}, "chat": {}, "web": {},
	"run":      {many("PROMPT", false, goalArg)},
	"swarm":    {pos("N", true, "number of workers (1 or more; the manager comes on top)"), many("GOAL", false, goalArg)},
	"recon":    {pos("DIR", false, "project directory (default: the current one)")},
	"inspect":  {pos("SESSION|DIR", false, "a session id (or the start of one), or a session directory; default: the newest session")},
	"watch":    {pos("SESSION", false, sessionArg)},
	"replay":   {pos("SESSION", false, sessionArg)},
	"doctor":   {},
	"models":   {many("WORDS", false, "each word narrows the search (all must appear, any case)")},
	"update":   {},
	"login":    {pos("PROVIDER", false, "heimdall, openrouter, openai, anthropic, chatgpt, ...")},
	"logout":   {pos("PROVIDER", true, "the provider whose key (or ChatGPT sign-in) to remove")},
	"schedule": {}, "schedule add": {many("GOAL", true, "the goal the scheduled run works on")},
	"schedule rm": {pos("ID", true, "the job id `sleipnir schedule` lists")},
	"daemon":      {}, "demo": {}, "mock": {}, "sim": {},
	"friction": {many("PATH", true, "a session directory, a directory of sessions, or a benchmark run directory")},
	"version":  {}, "mcp list": {},
	"mcp approve": {pos("NAME", true, "the project server to approve")},
	"mcp revoke":  {pos("NAME", true, "the approval to forget")},
	"mcp test":    {many("NAME", false, "servers to start (default: all)")},
	"trust":       {}, "trust add": {}, "trust forget": {}, "trust list": {},
	"models fav list": {}, "models fav add": {many("REF", true, "provider/model")}, "models fav rm": {many("REF", true, "provider/model")},
	"rl taskgen git": {}, "rl taskgen mutate": {}, "rl taskgen recall": {}, "rl taskgen fixture": {}, "rl rollout": {}, "rl eval": {}, "rl serve": {},
	"rl taskgen composite": {pos("TASKS", true, "the tasks file to combine")},
	"rl tasks validate":    {pos("FILE", true, tasksFile)},
	"rl tasks stats":       {pos("FILE", true, tasksFile)},
	"rl tasks filter":      {pos("FILE", true, tasksFile)},
	"rl tasks split":       {pos("FILE", true, tasksFile)},
	"rl tasks check":       {pos("FILE", true, tasksFile)},
	"rl reward":            {many("RUN_DIR", true, "run directories to re-score")},
	"rl report":            {many("RUN_DIR|REPORT.json", true, "run directories or saved reports")},
	"rl compare":           {pos("A", true, "a run directory or a saved report"), pos("B", true, "a run directory or a saved report")},
	"rl export":            {many("RUN_DIR", true, "run directories to export")},
	"rl expand":            {pos("EXPORT.jsonl", true, "a canonical export")},
	"rl verify":            {many("RUN_DIR", true, "run directories whose recorded prompts to replay")},
	"rl show":              {pos("RUN_DIR", true, "a run directory"), pos("TASK/SAMPLE", false, "one episode of it")},
}

// choices are the enumerations of flag values.
var choices = map[string][]string{
	"mode":           {"default", "accept-edits", "plan", "bypass", "yolo"},
	"perm-mode":      {"accept-edits", "default", "plan", "bypass", "yolo"},
	"isolation":      {"none", "worktree"},
	"view":           {"cockpit", "cache", "mail", "board"},
	"scenario":       {"handbook", "shop"},
	"provider":       {"heimdall", "openrouter", "openai", "custom"},
	"verify-policy":  {"all", "any", "majority"},
	"advantage":      {"grpo", "rloo", "broadcast", "anchor", "none"},
	"group-by":       {"task", "task+policy", "group"},
	"reasoning":      {"drop", "field", "keep"},
	"workspace-mode": {"export", "clone"},
	"category":       {"permission", "tool", "agent", "request", "cache", "compaction", "file", "call"},
}

// choicesFor is the enumeration of one flag of one command, or nil.
func choicesFor(path string, name string) []string {
	switch name {
	case "mode":
		switch path {
		case "sim":
			return []string{"compare", "scenarios", "pins", "agents"}
		case "rl rollout", "rl eval":
			return nil // single | swarm:N | empty
		}
	case "provider":
		if path == "sim" {
			return []string{"anthropic", "marketplace"}
		}
	case "format":
		switch path {
		case "rl export":
			return []string{"steps", "tokens", "groups", "sft", "dpo", "kto", "atif", "canonical"}
		case "rl report":
			return []string{"table", "md", "json"}
		case "rl compare":
			return []string{"table", "json"}
		}
		return nil
	}
	return choices[name]
}

// Modes of CONTRACT.md 18.2.
const (
	ModeRun     = "run"
	ModeNet     = "net"
	ModePriv    = "priv"
	ModeServer  = "server"
	ModeTTYOnly = "tty_only"
)

// modeOf is how the web runs a command: its mode with no flag set, the sentence that explains a refusal or a condition, and the
// flags that change the mode.
type modeOf struct {
	mode string
	why  string
	when []wire.CLIModeRule
}

// dangerous are the permission modes that need a confirmation in a run of the runner.
var dangerous = []string{"bypass", "yolo"}

// The flags that make any command that has them privileged (internal/web/runner decides on them in Go as well, on the parsed
// argument vector; these rules tell the page).
var (
	// keyRouteFlags name an endpoint or a key variable of the run's own: the run gets none of the held keys.
	keyRouteFlags = []string{"base-url", "api-key-env", "policy-host", "policy-key-env", "allow-insecure-http"}
	// wideningFlags let a run do more than its mode.
	wideningFlags = []string{"allow", "trust-project", "no-net-isolation", "pass-env", "set-env", "verify"}
	// modeFlags are permission modes.
	modeFlags = []string{"mode", "perm-mode"}
)

// privRules are a command's rules: its own, then the privileged flags it has (a command that needs a terminal gets none: no flag
// makes it run here, except its own rules).
func privRules(md modeOf, flags []wire.CLIFlag) []wire.CLIModeRule {
	rules := append([]wire.CLIModeRule(nil), md.when...)
	if md.mode == ModeTTYOnly {
		return rules
	}
	has, ruled := map[string]bool{}, map[string]bool{}
	for _, f := range flags {
		has[f.Name] = true
	}
	for _, r := range rules {
		ruled[r.Flag] = true
	}
	add := func(r wire.CLIModeRule) {
		if has[r.Flag] && !ruled[r.Flag] {
			rules = append(rules, r)
			ruled[r.Flag] = true
		}
	}
	for _, name := range keyRouteFlags {
		add(wire.CLIModeRule{Flag: name, Mode: ModePriv, NoKeys: true})
	}
	for _, name := range wideningFlags {
		add(wire.CLIModeRule{Flag: name, Mode: ModePriv})
	}
	for _, name := range modeFlags {
		add(wire.CLIModeRule{Flag: name, Values: dangerous, Mode: ModePriv})
	}
	add(wire.CLIModeRule{Flag: "addr", Mode: ModePriv, NonLoopback: true})
	return rules
}

// modes is the mode table of CONTRACT.md 18.2, with PARITY.md A30.
var modes = map[string]modeOf{
	"config": {mode: ModeRun}, "sessions": {mode: ModeRun},
	"sessions prune": {mode: ModeRun, why: "with --yes it deletes sessions: it asks for a confirmation first",
		when: []wire.CLIModeRule{{Flag: "yes", Mode: ModePriv}}},
	"models fav list": {mode: ModeRun}, "recon": {mode: ModeRun}, "sim": {mode: ModeRun}, "friction": {mode: ModeRun},
	"inspect": {mode: ModeTTYOnly, why: "inspect serves its own dashboard: use the Cache view here, or --json",
		when: []wire.CLIModeRule{{Flag: "json", Mode: ModeRun}}},
	"replay": {mode: ModeTTYOnly, why: "replay plays in a terminal: use the Replay view here, or --final or --record",
		when: []wire.CLIModeRule{{Flag: "final", Mode: ModeRun}, {Flag: "record", Mode: ModeRun}}},
	"trust": {mode: ModeRun}, "trust list": {mode: ModeRun},
	"trust add":    {mode: ModePriv, why: "it trusts this project's files: it asks for a confirmation first (--yes is implied)"},
	"trust forget": {mode: ModePriv, why: "it forgets a trust decision: it asks for a confirmation first"},
	"mcp":          {mode: ModeRun}, "mcp list": {mode: ModeRun},
	"mcp approve": {mode: ModePriv, why: "it lets a project's tool server start: it asks for a confirmation first (--yes is implied)"},
	"mcp revoke":  {mode: ModePriv, why: "it forgets an approval: it asks for a confirmation first"},
	"mcp test":    {mode: ModeNet},
	"schedule":    {mode: ModeRun},
	"schedule add": {mode: ModePriv, why: "it adds a job the daemon runs unattended: it asks for a confirmation first; bypass and yolo cannot be scheduled from here",
		when: []wire.CLIModeRule{{Flag: "mode", Values: dangerous, Mode: ModeTTYOnly}}},
	"schedule rm": {mode: ModePriv, why: "it removes a job: it asks for a confirmation first"},
	"version":     {mode: ModeRun}, "help": {mode: ModeRun},
	"rl": {mode: ModeRun}, "rl taskgen": {mode: ModeRun}, "rl tasks": {mode: ModeRun},
	"rl show": {mode: ModeRun}, "rl report": {mode: ModeRun}, "rl compare": {mode: ModeRun}, "rl expand": {mode: ModeRun},
	"rl tasks stats": {mode: ModeRun}, "rl tasks validate": {mode: ModeRun}, "rl tasks filter": {mode: ModeRun},
	"rl tasks split": {mode: ModeRun}, "rl verify": {mode: ModeRun},
	"models": {mode: ModeNet}, "doctor": {mode: ModeNet},
	"update": {mode: ModePriv, why: "it replaces this program with the latest release: it asks for a confirmation first; --check only looks",
		when: []wire.CLIModeRule{{Flag: "check", Mode: ModeNet}}},
	"rl eval":              {mode: ModeNet},
	"rl rollout":           {mode: ModeNet},
	"rl taskgen composite": {mode: ModeNet}, "rl taskgen fixture": {mode: ModeNet}, "rl taskgen git": {mode: ModeNet},
	"rl taskgen mutate": {mode: ModeNet}, "rl taskgen recall": {mode: ModeNet}, "rl tasks check": {mode: ModeNet},
	"rl reward": {mode: ModeNet}, "rl export": {mode: ModeNet},
	"run":            {mode: ModeNet},
	"swarm":          {mode: ModeNet},
	"demo":           {mode: ModeNet},
	"init":           {mode: ModePriv, why: "it writes configuration files: it asks for a confirmation first"},
	"logout":         {mode: ModePriv, why: "it removes a stored key or a sign-in: it asks for a confirmation first"},
	"models fav add": {mode: ModePriv, why: "it changes your configuration: it asks for a confirmation first"},
	"models fav rm":  {mode: ModePriv, why: "it changes your configuration: it asks for a confirmation first"},
	"mock":           {mode: ModeServer, why: "a server: it runs until it is stopped"},
	"rl serve":       {mode: ModeServer, why: "a server: it runs until it is stopped"},
	"daemon":         {mode: ModeServer, why: "it runs until it is stopped; the Schedule view starts and stops the daemon of this page"},
	"chat":           {mode: ModeTTYOnly, why: "chat needs a terminal: + New session starts the same session here"},
	"watch":          {mode: ModeTTYOnly, why: "watch needs a terminal: the Cockpit shows it live"},
	"login":          {mode: ModeTTYOnly, why: "login needs a terminal: Settings › Providers & login shows the command to run"},
	"web":            {mode: ModeTTYOnly, why: "you are in it: sleipnir web serves this page"},
}

// spec collects the commands as they are built.
type spec struct {
	cmds []wire.CLICommand
	seen map[string]bool
}

// add appends a command; a path seen twice is an error.
func (s *spec) add(c wire.CLICommand) error {
	key := strings.Join(c.Path, " ")
	if s.seen[key] {
		return fmt.Errorf("duplicate command %s", key)
	}
	s.seen[key] = true
	s.cmds = append(s.cmds, c)
	return nil
}

var (
	sentenceRE = regexp.MustCompile(`^(.+?[.!?])(?:\s|$)`)
	usageRE    = regexp.MustCompile(`(?i)^usage: (.+)$`)
	bracketRE  = regexp.MustCompile(`\[[^\]]*\]`)
	reqFlagRE  = regexp.MustCompile(`--?([a-z][\w-]*)\s+(?:[A-Za-z_.<][^\s]*)`)
	// partialWordRE is the cut word at the end of a shortened sentence.
	partialWordRE = regexp.MustCompile(`\s+\S*$`)
)

// firstSentence is the first sentence of some prose, without markdown, at most 220 characters.
func firstSentence(lines []string) string {
	text := strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
	s := text
	if m := sentenceRE.FindStringSubmatch(text); m != nil {
		s = m[1]
	}
	s = strings.NewReplacer("`", "", "**", "").Replace(s)
	if utf8.RuneCountInString(s) > 220 {
		s = partialWordRE.ReplaceAllString(string([]rune(s)[:217]), "") + "..."
	}
	return s
}

// proseOf is the first paragraph of a section, without fenced examples, lists and tables.
func proseOf(sec *section) []string {
	var out []string
	fence := false
	for _, l := range sec.prose {
		if strings.HasPrefix(l, "```") {
			fence = !fence
			continue
		}
		if fence {
			continue
		}
		if strings.HasPrefix(l, "<!--") || strings.TrimSpace(l) == "" {
			if len(out) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(l, "|") || strings.HasPrefix(l, "* ") || strings.HasPrefix(l, "- ") {
			if len(out) > 0 {
				break
			}
			continue
		}
		out = append(out, strings.TrimSpace(l))
	}
	return out
}

// usageLine is the "usage:" line of a listing, or "".
func usageLine(text []string) string {
	for _, l := range text {
		if m := usageRE.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			return strings.TrimSpace(m[1])
		}
	}
	return ""
}

// requiredFromUsage lists the flags a usage line says are required: "--repo PATH" outside brackets.
func requiredFromUsage(u string) map[string]bool {
	out := map[string]bool{}
	for _, m := range reqFlagRE.FindAllStringSubmatch(bracketRE.ReplaceAllString(u, " "), -1) {
		out[m[1]] = true
	}
	return out
}

// finish completes a command: its usage, the enumerations and required marks of its flags, its positionals and its mode.
func (s *spec) finish(path []string, usage, summary string, flags []wire.CLIFlag, sec *section, extra wire.CLICommand) error {
	key := strings.Join(path, " ")
	req := requiredFromUsage(usage)
	out := make([]wire.CLIFlag, 0, len(flags))
	for _, f := range flags {
		if ch := choicesFor(key, f.Name); ch != nil {
			f.Choices = ch
		}
		if req[f.Name] && f.Default == nil && f.DefaultNote == "" && !strings.Contains(f.Desc, "(default") {
			f.Required = true
		}
		out = append(out, f)
	}
	ps, ok := positionals[key]
	if !ok {
		return fmt.Errorf("no positional entry for %s (curate it in specgen)", key)
	}
	md, ok := modes[key]
	if !ok {
		return fmt.Errorf("no mode for %s (CONTRACT.md 18.2; add it in specgen)", key)
	}
	switch {
	case usage == "":
		usage = "sleipnir " + key
		if len(out) > 0 {
			usage += " [flags]"
		}
	case !strings.HasPrefix(usage, "sleipnir"):
		usage = "sleipnir " + usage
	}
	c := extra
	c.Path, c.Usage, c.Summary, c.Positional, c.Flags = path, usage, summary, append([]wire.CLIPositional{}, ps...), out
	c.Source = "docs/CLI.md (prose)"
	if sec != nil {
		c.Source = sourceOf(sec)
	}
	c.Mode, c.Why, c.When = md.mode, md.why, privRules(md, out)
	return s.add(c)
}

// sourceOf names the section of docs/CLI.md a command comes from by its heading, not its line, so that an edit of the document's
// prose elsewhere does not change the spec.
func sourceOf(sec *section) string {
	return "docs/CLI.md § " + strings.ReplaceAll(sec.title, "`", "")
}

// boolFlag is a curated bool flag.
func boolFlag(name, desc string) wire.CLIFlag {
	return wire.CLIFlag{Name: name, Arg: "bool", Desc: desc}
}

// cwdFlag is the --cwd flag of mcp and trust.
func cwdFlag(desc string) wire.CLIFlag {
	return wire.CLIFlag{Name: "cwd", Arg: "string", DefaultNote: "the current one", Desc: desc}
}

// Generate builds the spec from the inputs.
func Generate(in Inputs) (*wire.CLISpec, error) {
	md := strings.Split(strings.ReplaceAll(string(in.CLIDoc), "\r\n", "\n"), "\n")
	secs, err := sections(md)
	if err != nil {
		return nil, err
	}
	s := &spec{seen: map[string]bool{}}
	byTitle := func(t string) *section {
		for _, sec := range secs {
			if sec.title == t {
				return sec
			}
		}
		return nil
	}
	for _, sec := range secs {
		if !strings.HasPrefix(sec.title, "`sleipnir ") || len(sec.title) < 11 || sec.title[10] < 'a' || sec.title[10] > 'z' {
			continue
		}
		cmd := strings.TrimPrefix(strings.ReplaceAll(sec.title, "`", ""), "sleipnir ")
		parts := strings.Split(cmd, " ")
		prose := firstSentence(proseOf(sec))
		if parts[0] == "rl" && len(parts) == 1 {
			continue // the family heading: an index, no flags
		}
		first := func() (block, error) {
			if len(sec.blocks) == 0 {
				return block{}, fmt.Errorf("section %s: no flags block", cmd)
			}
			return sec.blocks[0], nil
		}
		b, err := first()
		if err != nil {
			return nil, err
		}
		switch cmd {
		case "sessions":
			err = s.finish([]string{"sessions"}, "sleipnir sessions [flags]", "Lists recorded sessions newest first: id, model, cost, first prompt; an arrow marks one that --resume can continue.", parseFlagLines(b.text), sec, wire.CLICommand{})
		case "sessions prune":
			err = s.finish([]string{"sessions", "prune"}, usageLine(b.text), "Deletes recorded sessions that are old (default: older than 30 days, except the newest 20); without --yes it lists what would go.", parseFlagLines(b.text), sec, wire.CLICommand{})
		case "mcp":
			err = s.mcp(sec, b)
		case "trust":
			err = s.trust(sec, b)
		case "run":
			fl := parseFlagLines(b.text)
			if err = s.finish([]string{"run"}, usageLine(b.text), "Runs one goal and exits: the remaining words, or what is piped in, or both.", fl, sec, wire.CLICommand{}); err != nil {
				break
			}
			// swarm is documented inside the run section: "sleipnir swarm N "goal" [flags]" is run --swarm N
			var swarmFl []wire.CLIFlag
			for _, f := range fl {
				if f.Name != "swarm" {
					swarmFl = append(swarmFl, f)
				}
			}
			err = s.finish([]string{"swarm"}, `sleipnir swarm N "goal" [flags]`, "Shorthand for run --swarm: a manager and up to N workers. N counts workers and must be 1 or more.", swarmFl, sec, wire.CLICommand{AliasOf: "run --swarm N"})
		case "models":
			fl := parseFlagLines(b.text)
			for _, c := range []struct {
				path       []string
				usage, sum string
				flags      []wire.CLIFlag
				parent     string
			}{
				{[]string{"models"}, "sleipnir models [words...] [flags]", "Prints the catalogue of an OpenAI-style marketplace with context size and prices per million tokens; favourites come first, marked *.", fl, ""},
				{[]string{"models", "fav", "list"}, "sleipnir models fav list", "Lists the favourite models kept as models.favorites in your user configuration.", nil, "models"},
				{[]string{"models", "fav", "add"}, "sleipnir models fav add provider/model...", "Star one or more models.", nil, "models"},
				{[]string{"models", "fav", "rm"}, "sleipnir models fav rm provider/model...", "Unstar one or more models.", nil, "models"},
			} {
				if err = s.finish(c.path, c.usage, c.sum, c.flags, sec, wire.CLICommand{Parent: c.parent}); err != nil {
					break
				}
			}
		case "login":
			if err = s.finish([]string{"login"}, usageLine(b.text), "Asks which provider you have a key for and for the key, which it does not echo, and keeps it in ~/.sleipnir/auth.json (mode 0600).", nil, sec, wire.CLICommand{}); err != nil {
				break
			}
			err = s.finish([]string{"logout"}, "sleipnir logout <provider>", "Removes a stored key, or revokes the ChatGPT sign-in.", nil, sec, wire.CLICommand{Parent: "login"})
		case "schedule":
			fl := parseFlagLines(b.text)
			for i := range fl {
				if fl[i].Name == "cron" {
					fl[i].Required = true // the usage line: schedule add --cron EXPR
				}
			}
			if err = s.finish([]string{"schedule"}, "sleipnir schedule [list]", "Lists the scheduled goals with the next and the last run.", nil, sec, wire.CLICommand{}); err != nil {
				break
			}
			if err = s.finish([]string{"schedule", "add"}, "sleipnir schedule add --cron EXPR [flags] <goal>", "Adds a goal to run on a schedule; a run is a headless sleipnir run in --cwd with the job's model, mode and budget (US$1 by default).", fl, sec, wire.CLICommand{Parent: "schedule"}); err != nil {
				break
			}
			err = s.finish([]string{"schedule", "rm"}, "sleipnir schedule rm <id>", "Removes a scheduled goal.", nil, sec, wire.CLICommand{Parent: "schedule"})
		case "rl tasks":
			summaries := map[string]string{
				"validate": "Checks every task in FILE and reports all problems.",
				"stats":    "Counts tasks by kind, tag, language and repository.",
				"filter":   "Writes the tasks that match tags, ids or a deterministic sample.",
				"split":    "Assigns whole repositories to named splits.",
				"check":    "Proves each task sound: the verifier fails on the start and passes with the reference solution.",
			}
			for _, b := range sec.blocks {
				sub := strings.TrimPrefix(b.name, "rl tasks ")
				if err = s.finish([]string{"rl", "tasks", sub}, usageLine(b.text), summaries[sub], parseFlagLines(b.text), sec, wire.CLICommand{Parent: "rl tasks"}); err != nil {
					break
				}
			}
		case "web":
			// refused in the page (PARITY.md A30): its flags are not offered, so that they need no form
			err = s.finish(parts, usageLine(b.text), "Serves the same sessions in a browser on this machine: this page.", nil, sec, wire.CLICommand{})
		default:
			if strings.HasPrefix(cmd, "rl ") {
				summary := prose
				if summary == "" {
					summary = map[string]string{
						"rl eval": "Runs held-out tasks and reports pass@k, cost and protocol quality.", "rl serve": "HTTP rollout server for a trainer.",
						"rl export": "Writes trainer-ready data from run directories.", "rl expand": "Turns a deduplicated canonical export back into inline form.",
						"rl verify": "Replays every recorded prompt and checks it against its wire hash.", "rl show": "Summarises a run directory, or one episode of it.",
						"rl reward": "Re-scores a run directory with different reward weights.",
					}[cmd]
				}
				err = s.finish(parts, usageLine(b.text), summary, parseFlagLines(b.text), sec, wire.CLICommand{})
				break
			}
			if len(sec.blocks) != 1 {
				return nil, fmt.Errorf("section %s: expected one flags block, found %d", cmd, len(sec.blocks))
			}
			summaries := map[string]string{
				"init":   "Writes a starter .sleipnir/config.json and AGENTS.md (or, with --user, your ~/.sleipnir/config.json); never overwrites.",
				"config": "Shows the effective configuration and where each value came from; --json prints the merged configuration.",
				"chat":   "Interactive session. On a terminal, a manager and eight workers; --swarm N sets the number of workers, --swarm 0 is a single agent.",
				"update": "Installs the latest release over the program that is running; --check only says whether a newer release is out.",
				"daemon": "Starts the scheduled goals that are due, one at a time, looking every half minute; --once starts what is due now and exits.",
				"mock":   "Serves the built-in deterministic mock provider (OpenAI-style chat completions with an automatic prefix cache).",
			}
			summary := summaries[cmd]
			if summary == "" {
				summary = prose
			}
			fl := parseFlagLines(b.text)
			if cmd == "update" && len(fl) == 0 {
				// the help of update has a usage line and no listing (PARITY.md A30)
				fl = []wire.CLIFlag{boolFlag("check", "only say whether a newer release is out")}
			}
			u := usageLine(b.text)
			if u == "" {
				u = "sleipnir " + cmd + " [flags]"
			}
			err = s.finish(parts, u, summary, fl, sec, wire.CLICommand{})
		}
		if err != nil {
			return nil, err
		}
	}
	line := func(title string) string {
		if sec := byTitle(title); sec != nil {
			return sourceOf(sec)
		}
		return "docs/CLI.md"
	}
	for _, c := range []wire.CLICommand{
		{Path: []string{"rl"}, Usage: "sleipnir rl <command> [flags]", Summary: "The RL environment: generate tasks, roll a policy out on them, score, export.", Source: line("`sleipnir rl`"), Index: true},
		{Path: []string{"mcp"}, Usage: "sleipnir mcp <command> [flags]", Summary: "Tool servers (the Model Context Protocol): list, approve, revoke, test, without starting a session.", Source: line("`sleipnir mcp`"), Index: true},
		{Path: []string{"rl", "taskgen"}, Usage: "sleipnir rl taskgen <generator>", Summary: "Make tasks from history (git), authored fixtures (fixture), mutations (mutate) or recall questions (recall), or compose swarm tasks (composite).", Source: "docs/CLI.md (rl command list)", Index: true},
		{Path: []string{"rl", "tasks"}, Usage: "sleipnir rl tasks <command> FILE", Summary: "Validate, filter, split and check task files.", Source: "docs/CLI.md (rl command list)", Index: true},
		{Path: []string{"version"}, Usage: "sleipnir version", Summary: "Prints sleipnir <version> (<commit>); also --version and -v.", Source: "docs/CLI.md (commands table)"},
		{Path: []string{"help"}, Usage: "sleipnir help", Summary: "Prints the command list (also --help); every command takes -h.", Source: "cmd/sleipnir/main.go"},
	} {
		md := modes[strings.Join(c.Path, " ")]
		c.Mode, c.Why, c.When = md.mode, md.why, md.when
		c.Positional, c.Flags = []wire.CLIPositional{}, []wire.CLIFlag{}
		if err := s.add(c); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(s.cmds, func(i, j int) bool { return strings.Join(s.cmds[i].Path, " ") < strings.Join(s.cmds[j].Path, " ") })
	slash, err := chatSlash(in.Sources)
	if err != nil {
		return nil, err
	}
	out := &wire.CLISpec{GeneratedFrom: "docs/CLI.md", Generator: Generator, Commands: s.cmds, ChatSlash: slash, ExitCodes: exitCodes}
	return out, nil
}

// mcp adds the subcommands of `sleipnir mcp`, checked against its listing so that a change of the CLI fails the generator.
func (s *spec) mcp(sec *section, b block) error {
	trust := boolFlag("trust-project", "read the project's .sleipnir/config.json and .mcp.json entries (each still needs your approval)")
	cwd := cwdFlag("working directory")
	sub := []struct {
		name, usage, sum string
		flags            []wire.CLIFlag
	}{
		{"list", "sleipnir mcp list [--trust-project]", "The servers a session here would consider, where each came from and whether it may start.", []wire.CLIFlag{trust, cwd}},
		{"approve", "sleipnir mcp approve NAME [--yes]", "Remember a project entry for this project (shows what it would run and asks first).", []wire.CLIFlag{boolFlag("yes", "do not ask: you have read what it would run"), cwd}},
		{"revoke", "sleipnir mcp revoke NAME", "Forget an approval.", []wire.CLIFlag{cwd}},
		{"test", "sleipnir mcp test [NAME...] [--trust-project]", "Start the servers and list the tools they offer (project entries still need approval).", []wire.CLIFlag{trust, cwd}},
	}
	for _, c := range sub {
		found := false
		for _, l := range b.text {
			found = found || strings.HasPrefix(strings.TrimSpace(l), c.name+" ")
		}
		if !found {
			return fmt.Errorf("mcp %s is missing from the listing of docs/CLI.md", c.name)
		}
		if err := s.finish([]string{"mcp", c.name}, c.usage, c.sum, c.flags, sec, wire.CLICommand{Parent: "mcp"}); err != nil {
			return err
		}
	}
	return nil
}

var trustFlagRE = regexp.MustCompile(`^ {2}--([a-z]+)(?: ([A-Z]+))?\s{2,}(.+)$`)

// trust adds `sleipnir trust` and its subcommands, with the flags of its listing.
func (s *spec) trust(sec *section, b block) error {
	flags := map[string]wire.CLIFlag{}
	in := false
	for _, l := range b.text {
		if strings.TrimSpace(l) == "flags:" {
			in = true
			continue
		}
		if !in {
			continue
		}
		if m := trustFlagRE.FindStringSubmatch(l); m != nil {
			arg := "bool"
			if m[2] != "" {
				arg = "string"
			}
			flags[m[1]] = wire.CLIFlag{Name: m[1], Arg: arg, Desc: strings.TrimSpace(m[3])}
		}
	}
	cwd, okc := flags["cwd"]
	yes, oky := flags["yes"]
	all, oka := flags["all"]
	if !okc || !oky || !oka {
		return errors.New("trust: --cwd, --yes and --all are not all in the listing of docs/CLI.md")
	}
	cwd.DefaultNote = "the current one"
	cwd.Desc = strings.TrimSuffix(cwd.Desc, " (default: the current one)")
	for _, c := range []struct {
		path       []string
		usage, sum string
		flags      []wire.CLIFlag
	}{
		{[]string{"trust"}, "sleipnir trust [--cwd DIR]", "Shows what in this directory's project trust would unlock, and whether you said yes to exactly that.", []wire.CLIFlag{cwd}},
		{[]string{"trust", "add"}, "sleipnir trust add [--yes] [--cwd DIR]", "Say yes to those files as they are now (shows them and asks first).", []wire.CLIFlag{cwd, yes}},
		{[]string{"trust", "forget"}, "sleipnir trust forget [--all] [--cwd DIR]", "Forget the answer for this directory, or for every project.", []wire.CLIFlag{cwd, all}},
		{[]string{"trust", "list"}, "sleipnir trust list", "The directories you said yes to, and whether the files in each still are the ones you saw.", nil},
	} {
		if err := s.finish(c.path, c.usage, c.sum, c.flags, sec, wire.CLICommand{Parent: "trust"}); err != nil {
			return err
		}
	}
	return nil
}

// exitCodes are the exit statuses of the program (docs/CLI.md "Exit codes").
var exitCodes = []wire.ExitCode{
	{Code: 0, Meaning: "success; also -h/--help, version, and a chat the person ends"},
	{Code: 1, Meaning: "the command failed: any error is printed as `sleipnir: <error>` on stderr"},
	{Code: 2, Meaning: "usage: no command, an unknown command, or an unknown or malformed flag"},
	{Code: 3, Meaning: "unfinished: a swarm's manager stopped with work left undone"},
	{Code: 130, Meaning: "interrupted by Ctrl-C (143 for SIGTERM)"},
	{Code: 75, Meaning: "try again later (EX_TEMPFAIL): rl rollout or rl eval in which no rollout completed"},
}

var (
	chatHelpRE  = regexp.MustCompile("(?s)const chatHelp = `([^`]+)`")
	chatCmdRE   = regexp.MustCompile(`\{"([a-z]+)", "([^"]*)", "((?:[^"\\]|\\.)*)"\}`)
	chatTableRE = regexp.MustCompile(`(?m)^var chatCommands = \[\]chatCommand\{$`)
)

// sortedNames lists the keys of the sources in order, so that a search over them is deterministic.
func sortedNames(src map[string][]byte) []string {
	names := make([]string, 0, len(src))
	for n := range src {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// chatSlash reads the slash commands: chatHelp (the /help text, grouped, two columns) joined with chatCommands (the arguments and
// descriptions the completion menu shows).
func chatSlash(src map[string][]byte) ([]wire.SlashEntry, error) {
	var help string
	var table []byte
	for _, n := range sortedNames(src) {
		if m := chatHelpRE.FindSubmatch(src[n]); m != nil && help == "" {
			help = string(m[1])
		}
		if loc := chatTableRE.FindIndex(src[n]); loc != nil && table == nil {
			rest := src[n][loc[1]:]
			if end := bytes.Index(rest, []byte("\n}\n")); end >= 0 {
				rest = rest[:end]
			}
			table = rest
		}
	}
	if help == "" {
		return nil, errors.New("chatHelp not found in cmd/sleipnir")
	}
	if table == nil {
		return nil, errors.New("chatCommands not found in cmd/sleipnir")
	}
	palette := map[string][2]string{}
	var palNames []string
	for _, m := range chatCmdRE.FindAllSubmatch(table, -1) {
		desc := strings.ReplaceAll(string(m[3]), `\"`, `"`)
		palette[string(m[1])] = [2]string{string(m[2]), desc}
		palNames = append(palNames, string(m[1]))
	}
	var out []wire.SlashEntry
	group := ""
	for _, l := range strings.Split(help, "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if !strings.HasPrefix(l, "/") {
			group = strings.TrimSpace(l)
			continue
		}
		// chatHelp is a fixed two-column text: the command and its arguments in the first 19 columns, the description after
		r := []rune(l)
		cut := min(19, len(r))
		left := strings.Fields(string(r[:cut]))
		e := wire.SlashEntry{Cmd: left[0], Args: strings.Join(left[1:], " "), Desc: strings.TrimSpace(string(r[cut:])), Group: group}
		e.PaletteArgs, e.PaletteDesc = e.Args, e.Desc
		if p, ok := palette[strings.TrimPrefix(e.Cmd, "/")]; ok {
			e.PaletteArgs, e.PaletteDesc = p[0], p[1]
		}
		out = append(out, e)
	}
	have := map[string]bool{}
	for _, e := range out {
		have[strings.TrimPrefix(e.Cmd, "/")] = true
	}
	var missing []string
	for _, n := range palNames {
		if !have[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("commands in chatCommands but not in chatHelp: %s", strings.Join(missing, ","))
	}
	return out, nil
}

var (
	usageTextRE = regexp.MustCompile("(?s)func usage\\(w io\\.Writer\\) \\{\\s*fmt\\.Fprint\\(w, `([^`]*)`")
	usageCmdRE  = regexp.MustCompile(`^  ([a-z][a-z0-9-]*) {2,}\S`)
)

// HelpCommands lists the commands `sleipnir --help` names (the usage text of cmd/sleipnir/main.go, from "Every day:" on).
func HelpCommands(src map[string][]byte) ([]string, error) {
	for _, n := range sortedNames(src) {
		m := usageTextRE.FindSubmatch(src[n])
		if m == nil {
			continue
		}
		text := string(m[1])
		i := strings.Index(text, "Every day:")
		if i < 0 {
			return nil, errors.New("the usage text has no \"Every day:\" section")
		}
		var out []string
		for _, l := range strings.Split(text[i:], "\n") {
			if c := usageCmdRE.FindStringSubmatch(l); c != nil {
				out = append(out, c[1])
			}
		}
		return out, nil
	}
	return nil, errors.New("the usage text (func usage) was not found in cmd/sleipnir")
}

// Check verifies a spec against the program: every command `sleipnir --help` names has an entry, and every entry has a mode.
func Check(spec *wire.CLISpec, src map[string][]byte) error {
	have := map[string]bool{}
	for _, c := range spec.Commands {
		if c.Mode == "" {
			return fmt.Errorf("%s has no mode", strings.Join(c.Path, " "))
		}
		have[strings.Join(c.Path, " ")] = true
	}
	names, err := HelpCommands(src)
	if err != nil {
		return err
	}
	if len(names) < 10 {
		return fmt.Errorf("only %d commands found in the usage text", len(names))
	}
	for _, n := range names {
		if !have[n] {
			return fmt.Errorf("sleipnir --help lists %s, which has no entry in the spec", n)
		}
	}
	return nil
}

// Encode writes a spec deterministically, readable in a diff: one command per block, one flag per line.
func Encode(spec *wire.CLISpec) ([]byte, error) {
	enc := func(v any) (string, error) {
		var b bytes.Buffer
		e := json.NewEncoder(&b)
		e.SetEscapeHTML(false)
		if err := e.Encode(v); err != nil {
			return "", err
		}
		return strings.TrimSuffix(b.String(), "\n"), nil
	}
	var b strings.Builder
	head := []struct {
		k string
		v any
	}{{"generatedFrom", spec.GeneratedFrom}, {"generator", spec.Generator}, {"exitCodes", spec.ExitCodes}}
	b.WriteString("{\n")
	for _, h := range head {
		s, err := enc(h.v)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%q:%s,\n", h.k, s)
	}
	b.WriteString("\"commands\":[\n")
	for i, c := range spec.Commands {
		flags := c.Flags
		c.Flags = nil
		s, err := enc(c)
		if err != nil {
			return nil, err
		}
		// the command without its flags, then the flags one per line
		s = strings.TrimSuffix(s, "}")
		s = strings.Replace(s, `"flags":null,`, "", 1)
		s = strings.Replace(s, `,"flags":null`, "", 1)
		b.WriteString(s)
		b.WriteString(`,"flags":[`)
		for j, f := range flags {
			fs, err := enc(f)
			if err != nil {
				return nil, err
			}
			if j > 0 {
				b.WriteString(",")
			}
			b.WriteString("\n  " + fs)
		}
		if len(flags) > 0 {
			b.WriteString("\n ")
		}
		b.WriteString("]}")
		if i < len(spec.Commands)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("],\n\"chatSlash\":[\n")
	for i, e := range spec.ChatSlash {
		s, err := enc(e)
		if err != nil {
			return nil, err
		}
		b.WriteString(s)
		if i < len(spec.ChatSlash)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("]\n}\n")
	out := []byte(b.String())
	var check wire.CLISpec
	if err := json.Unmarshal(out, &check); err != nil {
		return nil, fmt.Errorf("the encoded spec is not valid JSON: %w", err)
	}
	return out, nil
}
