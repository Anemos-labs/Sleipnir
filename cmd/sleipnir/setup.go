package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tui/state"
)

// init registers project initialization, configuration, and session-listing commands.
func init() {
	extraCommands["init"] = cmdInit
	extraCommands["config"] = cmdConfig
	extraCommands["sessions"] = cmdSessions
}

// cmdInit writes a starter project configuration and instruction file. It only
// ever creates files that do not exist, and merges through config.Save, which
// refuses to produce a configuration that would not load.
func cmdInit(ctx context.Context, args []string) error {
	fs := newFlagSet("init", flag.ExitOnError)
	user := fs.Bool("user", false, "write ~/.sleipnir/config.json instead of the project's")
	model := fs.String("model", "", "default model as provider/model (see `sleipnir models`); without it the chat asks your provider on its first run")
	localURL := fs.String("local-url", "", "with --user: also add a provider named local at this URL, any server that speaks the OpenAI chat API: vLLM, SGLang, a proxy or gateway (http://127.0.0.1:8000/v1); then --model local/NAME. It records token ids for RL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *localURL != "" && !*user {
		return errors.New("init: --local-url goes with --user (providers are set in your own config, not in a project's)")
	}
	wd, _ := os.Getwd()
	root, _ := config.FindRoot(wd)
	home, _ := os.UserHomeDir()
	path := config.ProjectConfigPath(root)
	if *user {
		path = config.UserConfigPath(home)
	} else if err := config.CheckProjectTarget(root, path); err != nil {
		return fmt.Errorf("init: %w", err)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("init: %s already exists; edit it, or use `sleipnir config` to inspect it", path)
	}
	if *user && *model == "" && *localURL == "" && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		// On a terminal this is the first-time setup the chat runs by itself: ask the provider, write the file (pick.go).
		var chosen string
		in := bufio.NewReader(os.Stdin)
		if err := ensureModel(ctx, &chosen, in, os.Stderr, func() (string, error) { return readSecret(ctx, in) }, true); err != nil {
			return err
		}
		if chosen != "" {
			return nil
		}
	}
	// No model name is written into the harness (they change every month): without --model, the chat asks the provider what it has on
	// the first run and keeps the answer (pick.go).
	def := *model
	if def == "" && *user {
		fmt.Fprintln(os.Stderr, wrapFor(os.Stderr, "no model is set: `sleipnir` asks your provider which models it has on its first run and keeps your choice; or pass --model provider/model here"))
	}
	// Project files are part of a repository and may be someone else's, so the
	// settings that decide where keys and prompts go (providers, permission
	// mode) are ignored there unless the project is trusted. They belong in the
	// user's own config; the project gets only what is safe to share.
	var patch map[string]any
	if *user {
		patch = map[string]any{"permissions": map[string]any{"mode": "default"}}
		if *localURL != "" {
			// A self-hosted policy server (vLLM/SGLang): token capture makes rollouts RL-ready.
			// Only on request: a provider named local would become the only configured one,
			// and a bare model id would then be sent to it instead of to a marketplace.
			patch["providers"] = map[string]any{
				"local": map[string]any{
					"base_url": *localURL,
					"options":  map[string]any{"capture_tokens": true},
				},
			}
		}
		if def != "" {
			patch["models"] = map[string]any{"default": def}
		}
	} else {
		patch = map[string]any{
			"permissions": map[string]any{"deny": []string{"Read(./.env)", "Read(./secrets/**)"}},
			"swarm":       map[string]any{"max_workers": 12, "isolation": "none"},
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
		// The checkout may be someone else's: both files are written inside it, never through a link that leads out, and
		// AGENTS.md only when there is nothing of that name.
		if r, err := os.OpenRoot(root); err == nil {
			agents := filepath.Join(root, "AGENTS.md")
			if _, err := r.Lstat("AGENTS.md"); os.IsNotExist(err) {
				body := "# Project instructions\n\nSleipnir reads this file into the prompt that every agent shares, when it is run with --trust-project.\nKeep it short: the commands and conventions an agent cannot work out from the code.\n\n- Build: \n- Test: \n- Lint: \n- Conventions and things to avoid: \n\n## Capabilities\n\nOne line per system the project already has: what it does, where it lives, and the doc that explains it (path#heading).\nAgents check this list before they build something new.\n\n- \n"
				if f, err := r.OpenFile("AGENTS.md", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err == nil {
					_, werr := f.WriteString(body)
					if f.Close() == nil && werr == nil {
						fmt.Fprintf(os.Stderr, "wrote %s\n", agents)
					}
				}
			}
			if fi, err := r.Lstat(".gitignore"); err != nil || fi.Mode().IsRegular() {
				if b, _ := r.ReadFile(".gitignore"); !strings.Contains(string(b), ".sleipnir/config.local.json") {
					if f, err := r.OpenFile(".gitignore", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
						fmt.Fprintln(f, "\n# Sleipnir local settings\n.sleipnir/config.local.json")
						f.Close()
					}
				}
			}
			r.Close()
		}
	}
	if !*user {
		fmt.Fprintln(os.Stderr, wrapFor(os.Stderr, "providers and the permission mode live in your user config: `sleipnir init --user` (project files cannot set them unless trusted)"))
	}
	fmt.Fprintln(os.Stderr, "next: sleipnir doctor --model <model> --deep, then sleipnir chat")
	return nil
}

// cmdConfig shows the effective configuration and where each value came from.
func cmdConfig(_ context.Context, args []string) error {
	fs := newFlagSet("config", flag.ExitOnError)
	trust := fs.Bool("trust-project", false, "apply security-sensitive settings from project files")
	asJSON := fs.Bool("json", false, "print the effective configuration as JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, rep, err := config.Load(config.LoadOpts{UntrustedProject: !*trust})
	if rep != nil && !*asJSON {
		printHelp(os.Stderr, rep.String())
	}
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(redactedConfig(cfg))
	}
	// Load has validated the merged configuration and listed its warnings above, with
	// the file and line of each; an error would have stopped it.
	warnings := 0
	if rep != nil {
		warnings = len(rep.Warnings)
	}
	if warnings == 0 {
		fmt.Fprintln(os.Stderr, "configuration is valid")
	} else {
		fmt.Fprintf(os.Stderr, "configuration is valid, with %d warning(s) listed above\n", warnings)
	}
	for _, line := range swarmSettings(cfg, rep) {
		fmt.Fprintln(os.Stderr, wrapBlockFor(os.Stderr, line))
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
	mail, mailNote := "off", "worker mail is delivered at once"
	if cfg.Swarm.Mailman {
		mail, mailNote = "on", "worker mail goes through a mailman agent that digests bursts (its model: --role-model mailman=<model>)"
	}
	return []string{
		fmt.Sprintf("swarm.isolation: %s (%s): %s", iso, origin("swarm.isolation"), note),
		fmt.Sprintf("swarm.mailman: %s (%s): %s", mail, origin("swarm.mailman"), mailNote),
	}
}

// redactedConfig is cfg as `config --json` prints it: the output ends up in bug reports, so it is the configuration-wide redaction
// (config.Redact): provider headers and options, hooks and MCP entries, and anything else that looks like a credential.
func redactedConfig(cfg *config.Config) any { return config.Redact(cfg) }

// cmdSessions lists recorded sessions, newest first; `sessions prune` deletes the old ones.
func cmdSessions(_ context.Context, args []string) error {
	if len(args) > 0 && args[0] == "prune" {
		return cmdSessionsPrune(os.Stdout, os.Stderr, args[1:], time.Now())
	}
	fs := newFlagSet("sessions", flag.ExitOnError)
	dir := fs.String("dir", "", "the directory that holds the sessions (default <state>/sessions, <state> being $SLEIPNIR_HOME or ~/.sleipnir)")
	n := fs.Int("n", 20, "how many to list")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return printSessions(os.Stdout, os.Stderr, *dir, *n)
}

// printSessions lists the newest n sessions of the directory dir ("" for the default one), their lines to out and the note to errw.
func printSessions(out, errw io.Writer, dir string, n int) error {
	root := dir
	if root == "" {
		root = session.SessionsDir("")
	}
	ents, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		fmt.Fprintln(errw, "no sessions yet")
		return nil
	}
	if err != nil {
		return err
	}
	type row struct {
		id, model, prompt string
		when              time.Time
		cost              float64
		resumable, plan   bool
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
	// Read the logs of the rows shown, not of every session ever recorded; a session in which nothing was asked (a chat opened and closed, what
	// /new leaves behind) has nothing to resume or to read, and is not listed.
	shown := rows[:0:0]
	for i := range rows {
		if len(shown) == n || i >= 4*n+20 {
			break
		}
		r := rows[i]
		d := filepath.Join(root, r.id)
		summarize(filepath.Join(d, "events.jsonl"), &r.model, &r.prompt, &r.cost, &r.plan)
		if r.prompt == "" {
			continue
		}
		r.resumable = session.Resumable(d)
		shown = append(shown, r)
	}
	rows = shown
	width := termWidth(out)
	plans := false
	for _, r := range rows {
		mark := " "
		if r.resumable {
			mark = "↺"
		}
		// What a recording says is text an earlier run took in: one line, without terminal controls.
		clean := func(s string) string { return strings.Join(strings.Fields(tools.SanitizeForTerminal(s)), " ") }
		r.id, r.model, r.prompt = clean(r.id), clean(r.model), clean(r.prompt)
		model := fmt.Sprintf("%-28s ", r.model)
		if width > 0 && width < 110 {
			model = "" // the widest column: a narrow terminal keeps the id, the cost and what was asked
		}
		spent := fmt.Sprintf("$%-8.4f", r.cost)
		if r.plan {
			spent, plans = fmt.Sprintf("≈$%-7.4f", r.cost), true // what a subscription plan covered, at the API list price
		}
		head := fmt.Sprintf("%s %s  %-9s %s%s ", mark, r.id, ago(time.Since(r.when)), model, spent)
		prompt := r.prompt
		if width > 0 {
			prompt = fit(prompt, width-1-utf8.RuneCountInString(head))
		}
		fmt.Fprintln(out, head+prompt)
	}
	if len(rows) == 0 {
		fmt.Fprintln(errw, "no sessions yet") // a directory that exists and holds nothing worth listing is the same as none
	} else {
		fmt.Fprintln(errw, wrapBlockFor(errw, "↺ can be continued: sleipnir chat --resume <id>   (or --continue for this project's newest)"))
		if plans {
			fmt.Fprintln(errw, wrapBlockFor(errw, "≈ "+state.PlanHelp))
		}
	}
	return nil
}

// summarize reads just enough of a log for a listing line (session.Summarize): the model from session.start, the first user input,
// and the cost from session.end (with the responses of a run that wrote none added); for a session on a subscription plan the cost is
// the bill plus what the plan covered at the API list price, and plan is set.
func summarize(path string, model, prompt *string, usd *float64, plan *bool) {
	sum := session.Summarize(path)
	*model, *usd, *plan = sum.Model, sum.CostUSD+sum.ListUSD, sum.Plan
	if sum.First != "" {
		*prompt = oneLineCLI(sum.First, 70)
	}
}

// oneLineCLI collapses whitespace and bounds text by rune count, appending an ellipsis when
// truncated; n must be nonnegative.
func oneLineCLI(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}
