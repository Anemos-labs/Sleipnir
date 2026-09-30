package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/config"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/mcp"
	"github.com/reee344/sleipnir/internal/session"
)

func init() {
	extraCommands["init"] = cmdInit
	extraCommands["config"] = cmdConfig
	extraCommands["sessions"] = cmdSessions
}

// cmdInit writes a starter project configuration and instruction file. It only
// ever creates files that do not exist, and merges through config.Save, which
// refuses to produce a configuration that would not load.
func cmdInit(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	user := fs.Bool("user", false, "write ~/.sleipnir/config.json instead of the project's")
	model := fs.String("model", "", "default model, e.g. heimdall/deepseek/deepseek-v4.1-flash")
	if err := fs.Parse(args); err != nil {
		return err
	}
	wd, _ := os.Getwd()
	root, _ := config.FindRoot(wd)
	home, _ := os.UserHomeDir()
	path := config.ProjectConfigPath(root)
	if *user {
		path = config.UserConfigPath(home)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("init: %s already exists; edit it, or use `sleipnir config` to inspect it", path)
	}
	def := *model
	if def == "" {
		switch {
		case os.Getenv("HEIMDALL_API_KEY") != "":
			def = "heimdall/deepseek/deepseek-v4.1-flash"
		case os.Getenv("OPENROUTER_API_KEY") != "":
			def = "openrouter/deepseek/deepseek-chat"
		case os.Getenv("OPENAI_API_KEY") != "":
			def = "openai/gpt-5-mini"
		}
	}
	// Project files are part of a repository and may be someone else's, so the
	// settings that decide where keys and prompts go (providers, permission
	// mode) are ignored there unless the project is trusted. They belong in the
	// user's own config; the project gets only what is safe to share.
	var patch map[string]any
	if *user {
		patch = map[string]any{
			"providers": map[string]any{
				// A self-hosted policy server (vLLM/SGLang): token capture makes rollouts RL-ready.
				"local": map[string]any{
					"base_url": "http://127.0.0.1:8000/v1",
					"options":  map[string]any{"capture_tokens": true},
				},
			},
			"permissions": map[string]any{"mode": "default"},
		}
		if def != "" {
			patch["models"] = map[string]any{"default": def}
		}
	} else {
		patch = map[string]any{
			"permissions": map[string]any{"deny": []string{"Read(./.env)", "Read(./secrets/**)"}},
			"swarm":       map[string]any{"max_agents": 12, "isolation": "none"},
		}
		// A project shares a model only when asked to: the detected default depends on
		// whose keys are in this shell.
		if *model != "" {
			patch["models"] = map[string]any{"default": *model}
		}
	}
	if err := config.Save(path, patch); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "wrote %s\n", path)
	if !*user {
		agents := filepath.Join(root, "AGENTS.md")
		if _, err := os.Stat(agents); os.IsNotExist(err) {
			body := "# Project instructions\n\nCommands to build, test and lint, conventions and things to avoid go here.\nSleipnir pins this file in the shared prompt layer, so keep it short and dense.\n\n- Build: \n- Test: \n- Lint: \n"
			if err := os.WriteFile(agents, []byte(body), 0o644); err == nil {
				fmt.Fprintf(os.Stderr, "wrote %s\n", agents)
			}
		}
		ignore := filepath.Join(root, ".gitignore")
		if b, _ := os.ReadFile(ignore); !strings.Contains(string(b), ".sleipnir/config.local.json") {
			if f, err := os.OpenFile(ignore, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
				fmt.Fprintln(f, "\n# Sleipnir local settings\n.sleipnir/config.local.json")
				f.Close()
			}
		}
	}
	if !*user {
		fmt.Fprintln(os.Stderr, "providers and the permission mode live in your user config: `sleipnir init --user` (project files cannot set them unless trusted)")
	}
	fmt.Fprintln(os.Stderr, "next: sleipnir doctor --model <model> --deep, then sleipnir chat")
	return nil
}

// cmdConfig shows the effective configuration and where each value came from.
func cmdConfig(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("config", flag.ExitOnError)
	trust := fs.Bool("trust-project", false, "apply security-sensitive settings from project files")
	asJSON := fs.Bool("json", false, "print the effective configuration as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, rep, err := config.Load(config.LoadOpts{UntrustedProject: !*trust})
	if rep != nil && !*asJSON {
		fmt.Fprint(os.Stderr, rep.String())
	}
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(redactedConfig(cfg))
	}
	if issues := cfg.Validate(); len(issues) > 0 {
		for _, is := range issues {
			fmt.Fprintln(os.Stderr, is.Error())
		}
	} else {
		fmt.Fprintln(os.Stderr, "configuration is valid")
	}
	for _, line := range swarmSettings(cfg, rep) {
		fmt.Fprintln(os.Stderr, line)
	}
	return nil
}

// swarmSettings are the effective swarm switches that change what a run does to the
// person's files or how its agents talk, each with where its value came from: the
// keys are easy to set in one layer and forget in another.
func swarmSettings(cfg *config.Config, rep *config.Report) []string {
	origin := func(key string) string {
		if rep != nil {
			if o := rep.Origins[key]; o != "" {
				return o
			}
		}
		return "default"
	}
	iso := cfg.Swarm.IsolationMode()
	note := "every agent edits the one checkout"
	if iso == config.IsolationWorktree {
		note = "every writer gets its own git worktree; finished work is merged and verified, and applied to the checkout at the end"
	}
	return []string{fmt.Sprintf("swarm.isolation: %s (%s): %s", iso, origin("swarm.isolation"), note)}
}

// redactedConfig is cfg as `config --json` prints it: the output ends up in bug
// reports, and an MCP entry is where credentials live (headers, environment,
// tokens in a URL, arguments), so its values are replaced by their names or by
// counts; the rest of the configuration holds names of variables, never keys.
func redactedConfig(cfg *config.Config) any {
	if len(cfg.MCP) == 0 {
		return cfg
	}
	b, err := json.Marshal(cfg)
	if err != nil {
		return cfg
	}
	var doc map[string]any
	if json.Unmarshal(b, &doc) != nil {
		return cfg
	}
	shown := map[string]any{}
	servers, _ := mcp.ParseWith(cfg.MCP, mcp.ParseOptions{Scope: mcp.ScopeUser})
	for name := range cfg.MCP {
		c, ok := servers[name]
		if !ok {
			shown[name] = "(not a valid entry: see `sleipnir mcp list`)"
			continue
		}
		c = c.Redacted()
		e := map[string]any{"type": c.EffectiveType()}
		if c.Command != "" {
			e["command"] = c.Command
		}
		if n := len(c.Args); n > 0 {
			e["args"] = fmt.Sprintf("(%d arguments, not shown)", n)
		}
		if c.URL != "" {
			e["url"] = c.URL
		}
		if len(c.Env) > 0 {
			e["env"] = c.Env
		}
		if len(c.Headers) > 0 {
			e["headers"] = c.Headers
		}
		if c.Disabled {
			e["disabled"] = true
		}
		shown[name] = e
	}
	doc["mcp"] = shown
	return doc
}

// cmdSessions lists recorded sessions, newest first.
func cmdSessions(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("sessions", flag.ExitOnError)
	dir := fs.String("dir", "", "sessions directory (default ~/.sleipnir/sessions)")
	n := fs.Int("n", 20, "how many to list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root := *dir
	if root == "" {
		home := os.Getenv("SLEIPNIR_HOME")
		if home == "" {
			h, _ := os.UserHomeDir()
			home = filepath.Join(h, ".sleipnir")
		}
		root = filepath.Join(home, "sessions")
	}
	ents, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "no sessions yet")
		return nil
	}
	if err != nil {
		return err
	}
	type row struct {
		id, model, prompt string
		when              time.Time
		cost              float64
		resumable         bool
	}
	var rows []row
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		r := row{id: e.Name()}
		if fi, err := e.Info(); err == nil {
			r.when = fi.ModTime()
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].when.After(rows[j].when) })
	if len(rows) > *n {
		rows = rows[:*n]
	}
	for i := range rows { // read the logs of the rows shown, not of every session ever recorded
		r := &rows[i]
		d := filepath.Join(root, r.id)
		summarize(filepath.Join(d, "events.jsonl"), &r.model, &r.prompt, &r.cost)
		r.resumable = session.Resumable(d)
	}
	for _, r := range rows {
		mark := " "
		if r.resumable {
			mark = "↺"
		}
		fmt.Printf("%s %s  %-28s $%-8.4f %s\n", mark, r.id, r.model, r.cost, r.prompt)
	}
	if len(rows) > 0 {
		fmt.Fprintln(os.Stderr, "↺ can be continued: sleipnir chat --resume <id>   (or --continue for this project's newest)")
	}
	return nil
}

// summarize reads just enough of a log for a listing line: the model from
// session.start, the first user input, and the cost from session.end.
func summarize(path string, model, prompt *string, usd *float64) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e events.Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		switch e.Type {
		case events.TypeSessionStart:
			var d struct{ Model string }
			json.Unmarshal(e.Data, &d)
			*model = d.Model
		case events.TypeUserInput:
			if *prompt == "" {
				var d struct{ Text string }
				json.Unmarshal(e.Data, &d)
				*prompt = oneLineCLI(d.Text, 70)
			}
		case events.TypeSessionEnd:
			var d struct {
				Cost float64 `json:"cost_usd"`
			}
			json.Unmarshal(e.Data, &d)
			*usd = d.Cost
		}
	}
}

func oneLineCLI(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}
