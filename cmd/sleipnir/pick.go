package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// The harness knows no model names: they change every month, and a name written into the code is a bug in waiting. When nothing
// says which model to use (no --model, no configuration, no SLEIPNIR_MODEL), the person is asked, from what the provider itself lists
// right now, and the answer is kept as models.default in their own configuration, so they are asked once.

// pickRows is how many matches the prompt shows at a time.
const pickRows = 12

// pickModel asks which of rows to use. Words narrow the list (all must appear in the reference), a number chooses one of the rows shown,
// q gives up. Favorites come first, then references in alphabetical order. It returns the reference chosen.
func pickModel(in *bufio.Reader, out io.Writer, rows []modelRow, fav map[string]bool) (string, error) {
	f := modelFilter{}
	var chat []modelRow
	for _, r := range rows {
		if f.keep(r, fav) {
			chat = append(chat, r)
		}
	}
	sort.SliceStable(chat, func(i, j int) bool {
		if fi, fj := fav[chat[i].Ref], fav[chat[j].Ref]; fi != fj {
			return fi
		}
		return chat[i].Ref < chat[j].Ref
	})
	if len(chat) == 0 {
		return "", errors.New("the provider lists no chat models")
	}
	if arrowOK() {
		labels, keys := make([]string, len(chat)), make([]string, len(chat))
		for i, r := range chat {
			labels[i], keys[i] = modelLine(r, fav), r.Ref
		}
		i, err := selectRows(in, out, "Which model?", labels, keys, true, pickRows)
		if err != nil {
			return "", errors.New("no model chosen")
		}
		return chat[i].Ref, nil
	}
	words := []string(nil)
	for {
		var shown []modelRow
		for _, r := range chat {
			if (modelFilter{Words: words}).keep(r, fav) {
				shown = append(shown, r)
			}
		}
		total := len(shown)
		if len(shown) > pickRows {
			shown = shown[:pickRows]
		}
		for i, r := range shown {
			fmt.Fprintf(out, "  %2d. %s\n", i+1, modelLine(r, fav))
		}
		switch {
		case total == 0:
			fmt.Fprintln(out, "  nothing matches")
		case total > len(shown):
			fmt.Fprintf(out, "  ... and %d more\n", total-len(shown))
		}
		fmt.Fprint(out, "Type a number to choose, words to search, q to quit: ")
		line, err := in.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" && err != nil {
			return "", errors.New("no model chosen")
		}
		var n int
		switch _, serr := fmt.Sscanf(line, "%d", &n); {
		case strings.EqualFold(line, "q"):
			return "", errors.New("no model chosen")
		case serr == nil && n >= 1 && n <= len(shown) && fmt.Sprint(n) == line:
			return shown[n-1].Ref, nil
		default:
			words = strings.Fields(line)
		}
	}
}

// ensureModel makes sure a model is named before a chat starts on a terminal. When none is (session.ErrNoModel) and a provider with a
// key is there to ask, it lists that provider's models, lets the person choose, and keeps the answer as models.default in their own
// configuration. Anywhere it cannot ask (not a terminal, no key, a catalogue that does not answer) it changes nothing, and the session
// then fails with its usual explanation.
func ensureModel(ctx context.Context, model *string, in *bufio.Reader, out io.Writer, secret func() (string, error), tty bool) error {
	if *model != "" || !tty {
		return nil
	}
	cfg, _, err := config.Load(config.LoadOpts{UntrustedProject: true})
	if err != nil {
		return nil
	}
	if _, err := session.ResolveModel(cfg, ""); !errors.Is(err, session.ErrNoModel) {
		return nil
	}
	sources := usableSources(cfg, false)
	if len(sources) == 0 {
		fmt.Fprintln(out, "Welcome to Sleipnir. It needs a model provider, and none has a key yet.")
		local := localSources(ctx, cfg)
		name, err := login(in, out, secret, cfg, "", local)
		if err != nil {
			return err
		}
		for _, l := range local {
			if l.name == name { // a local server was chosen: no key to keep
				sources = []modelSource{l}
			}
		}
		if len(sources) == 0 {
			if sources = usableSources(cfg, false); len(sources) == 0 {
				return errors.New("the key was saved, but no provider with a model list is ready: `sleipnir models --provider NAME` shows why")
			}
		}
	}
	home, _ := os.UserHomeDir()
	cfgPath := config.UserConfigPath(home)
	_, statErr := os.Stat(cfgPath)
	firstTime := errors.Is(statErr, os.ErrNotExist)
	if firstTime {
		fmt.Fprintln(out, "First-time setup: choose a model; your settings are kept in "+cfgPath+".")
	}
	names := make([]string, len(sources))
	for i, src := range sources {
		names[i] = src.name
	}
	fmt.Fprintf(out, "Asking %s what it offers...\n", strings.Join(names, ", "))
	rows, errs := fetchModels(ctx, sources)
	if len(rows) == 0 {
		for i, e := range errs {
			if e != nil {
				fmt.Fprintf(out, "%s: %v\n", names[i], e)
			}
		}
		return nil
	}
	ref, err := pickModel(in, out, rows, favoriteSet(cfg))
	if err != nil {
		return err
	}
	patch := map[string]any{"models": map[string]any{"default": ref}}
	if firstTime {
		patch["permissions"] = map[string]any{"mode": "default"} // asks before it changes anything; `sleipnir config` shows the rest
	}
	if err := config.Save(cfgPath, patch); err != nil {
		fmt.Fprintf(out, "(not saved: %v)\n", err)
	} else if firstTime {
		fmt.Fprintf(out, "Wrote %s. Using %s; change it with /model, edit the file, or see `sleipnir config`.\n", cfgPath, ref)
	} else {
		fmt.Fprintf(out, "Using %s, and keeping it as your default in %s (change it with /model).\n", ref, cfgPath)
	}
	*model = ref
	return nil
}

// modelLine is one row of the model menu: the reference, the context window, the output price, and whether it takes tools.
func modelLine(r modelRow, fav map[string]bool) string {
	tools := ""
	if r.SupportsTools() {
		tools = "  tools"
	}
	star := ""
	if fav[r.Ref] {
		star = " *"
	}
	return fmt.Sprintf("%-44s %6s ctx  $%.3g/M out%s%s", r.Ref, human(r.Model.ContextTokens), r.Model.Price.OutputPerM, tools, star)
}
