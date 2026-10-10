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
	"time"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/harden"
	"github.com/anemos-labs/sleipnir/internal/provider"
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
		labels, keys := modelTable(chat, fav), make([]string, len(chat))
		for i, r := range chat {
			keys[i] = r.Ref
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
		for i, label := range modelTable(shown, fav) {
			fmt.Fprintf(out, "  %2d. %s\n", i+1, label)
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
	typedKey := false
	typedRef := "" // a model the person typed, for a provider that lists none
	if len(sources) == 0 {
		fmt.Fprintln(out, "Welcome to Sleipnir. It needs a model provider, and none has a key yet.")
		local := localSources(ctx, cfg)
		name, err := login(ctx, in, out, secret, cfg, "", local)
		if err != nil {
			return err
		}
		typedKey = name != chatgptName // a sign-in has no key to try
		for _, l := range local {
			if l.name == name { // a local server was chosen: no key to keep
				sources, typedKey = []modelSource{l}, false
			}
		}
		if len(sources) == 0 {
			if sources = usableSources(cfg, false); len(sources) == 0 {
				if !typedKey {
					return errors.New("the key was saved, but no provider with a model list is ready: `sleipnir models --provider NAME` shows why")
				}
				// The provider just chosen lists no models here (Anthropic's own protocol, say): the person types the model, and the key and the
				// model are tried together by the one small request below.
				if typedRef, err = askModelID(in, out, name); err != nil {
					return err
				}
			}
		}
	}
	home, _ := os.UserHomeDir()
	cfgPath := config.UserConfigPath(home)
	_, statErr := os.Stat(cfgPath)
	firstTime := errors.Is(statErr, os.ErrNotExist)
	if firstTime {
		fmt.Fprintln(out, wrapFor(out, "First-time setup: choose a model; your settings are kept in "+tildePath(cfgPath)+"."))
	}
	ref := typedRef
	if ref == "" {
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
		if ref, err = pickModel(in, out, rows, favoriteSet(cfg)); err != nil {
			return err
		}
	}
	if typedKey {
		if err := checkKey(ctx, cfg, ref, out); err != nil {
			return err
		}
	}
	patch := map[string]any{"models": map[string]any{"default": ref}}
	if firstTime {
		patch["permissions"] = map[string]any{"mode": "default"} // asks before it changes anything; `sleipnir config` shows the rest
	}
	if err := config.Save(cfgPath, patch); err != nil {
		fmt.Fprintf(out, "(not saved: %v)\n", err)
	} else if firstTime {
		fmt.Fprintln(out, wrapFor(out, fmt.Sprintf("Wrote %s. Using %s; change it with /model, edit the file, or see `sleipnir config`.", tildePath(cfgPath), ref)))
	} else {
		fmt.Fprintln(out, wrapFor(out, fmt.Sprintf("Using %s, and keeping it as your default in %s (change it with /model).", ref, tildePath(cfgPath))))
	}
	*model = ref
	return nil
}

// askModelID asks for the id of a model on a provider that lists none here, and returns it as provider/model. An empty line is no choice.
func askModelID(in *bufio.Reader, out io.Writer, provider string) (string, error) {
	fmt.Fprint(out, wrapFor(out, provider+" lists no models here. Type the id of the model to use, as its documentation spells it (an empty line stops):")+" ")
	line, _ := in.ReadString('\n')
	id := strings.TrimSpace(line)
	if id == "" {
		return "", errors.New("no model chosen: run `sleipnir --model " + provider + "/MODEL` when you know which")
	}
	if strings.HasPrefix(id, provider+"/") {
		return id, nil
	}
	return provider + "/" + id, nil
}

// modelTable is the rows of a model menu: each reference, the context window, the output price and whether it takes tools, in columns as
// wide as the longest of the rows needs (a reference past 48 characters sticks out of its column rather than push every other row's).
func modelTable(rows []modelRow, fav map[string]bool) []string {
	refW, priceW := 0, 0
	for _, r := range rows {
		refW, priceW = min(max(refW, len(r.Ref)), 48), max(priceW, len(priceOut(r)))
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		tools := ""
		if r.SupportsTools() {
			tools = "  tools"
		}
		star := ""
		if fav[r.Ref] {
			star = " *"
		}
		out[i] = strings.TrimRight(fmt.Sprintf("%-*s %6s ctx  %-*s%s%s", refW, r.Ref, human(r.Model.ContextTokens), priceW, priceOut(r), tools, star), " ")
	}
	return out
}

// modelLine is one row of the model menu by itself.
func modelLine(r modelRow, fav map[string]bool) string { return modelTable([]modelRow{r}, fav)[0] }

// checkKey sends one small request with the key just typed, because a catalogue is often public and answers whatever the key is. Only
// a refusal of the key itself (401, 403) counts: the key is then forgotten and the person told, so a typo is found here and not at the
// first goal. Any other failure (a slow network, a busy model) says nothing about the key and is let through. A variable of the key's name
// in the environment wins over the stored key, so the request then carries the variable's value: a refusal is reported as the variable's,
// and the stored key, which was not tried, is kept.
func checkKey(ctx context.Context, cfg *config.Config, ref string, out io.Writer) error {
	mr, err := session.ResolveModel(cfg, ref)
	if err != nil {
		return nil
	}
	p, _, err := session.BuildProvider(cfg, mr, session.ProviderOptions{})
	if err != nil {
		return nil
	}
	fmt.Fprint(out, "Checking the key... ")
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	prompt := &core.Prompt{Model: mr.Model, Params: core.Params{MaxTokens: 8}, Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{core.Text("ping")}}}}
	_, err = p.Do(ctx, &provider.Request{Prompt: prompt, Label: "login:check", NoStream: true}, nil)
	var pe *provider.Error
	if errors.As(err, &pe) && pe.Kind == provider.ErrAuth {
		if _, env, ok := session.ProviderInfo(cfg, mr.Provider); ok && env != "" {
			if harden.SourceOf(env) == harden.SourceEnvironment {
				// the environment wins, so the request carried its value and not the key just typed, which was never tried
				fmt.Fprintln(out, "refused.")
				return fmt.Errorf("%s did not accept the key in %s (%v); that variable takes precedence over the key just stored, which was kept: correct or unset %s", mr.Provider, env, pe.Message, env)
			}
			_ = config.SaveStoredKey(userHome(), env, "")
			harden.Provide(env, "")
		}
		fmt.Fprintln(out, "refused.")
		return fmt.Errorf("%s did not accept that key (%v); it was not kept: run `sleipnir login %s` to try again", mr.Provider, pe.Message, mr.Provider)
	}
	if err != nil {
		fmt.Fprintln(out, "could not check it now.")
		return nil
	}
	fmt.Fprintln(out, "ok.")
	return nil
}
