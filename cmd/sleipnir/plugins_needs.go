package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/plugin"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// The needs subcommands of `sleipnir plugins` (plugin.Need): the same list, Locate and Install as the web page's Plugins view.

// needEnv is where needs are looked for (tests replace it).
var needEnv = func(home string) plugin.NeedEnv {
	return plugin.NeedEnv{State: session.StateRoot(home), Home: home}
}

// ownedNeeds are the needs of the plugins that are on here.
func (p pluginCLI) ownedNeeds() ([]plugin.OwnedNeed, error) {
	set, _, err := p.load()
	if err != nil {
		return nil, err
	}
	return plugin.Needs(set), nil
}

// need is one need by id, with a hint at the list when there is none.
func (p pluginCLI) need(id string) (plugin.OwnedNeed, error) {
	needs, err := p.ownedNeeds()
	if err != nil {
		return plugin.OwnedNeed{}, err
	}
	n, ok := plugin.FindNeed(needs, id)
	if !ok {
		return n, fmt.Errorf("plugins: no plugin that is on needs %q (see sleipnir plugins needs)", id)
	}
	return n, nil
}

// needs prints each need, found or not, with its install methods.
func (p pluginCLI) needs() error {
	needs, err := p.ownedNeeds()
	if err != nil {
		return err
	}
	sts := plugin.CheckNeeds(needEnv(p.home), needs)
	if p.json {
		return writeJSON(p.out, map[string]any{"needs": sts})
	}
	if len(sts) == 0 {
		fmt.Fprintln(p.out, "no plugin that is on needs a program outside Sleipnir")
		return nil
	}
	for _, st := range sts {
		state := "not found"
		switch {
		case st.Found:
			state = "found (" + st.Via + "): " + st.Path
		case st.Required:
			state = "missing, needed"
		}
		fmt.Fprintf(p.out, "%-13s %-22s %s  [%s]\n", st.ID, st.Title, state, st.Plugin)
		if st.Found {
			continue
		}
		for _, m := range st.Methods {
			line := "    sleipnir plugins setup " + st.ID + " " + m.ID + "   # " + m.Title
			if len(m.Argv) > 0 {
				line += ": " + strings.Join(m.Argv, " ")
			}
			if !m.Available {
				line += " (" + m.Reason + ")"
			}
			fmt.Fprintln(p.out, line)
		}
		if st.MCP == nil {
			fmt.Fprintf(p.out, "    sleipnir plugins locate %s PATH   # a copy you installed yourself\n", st.ID)
		}
	}
	return nil
}

// locate remembers a program's file.
func (p pluginCLI) locate(ctx context.Context, id, path string) error {
	n, err := p.need(id)
	if err != nil {
		return err
	}
	st, err := plugin.LocateNeed(ctx, needEnv(p.home), n.Need, path)
	if err != nil {
		return fmt.Errorf("plugins: %w", err)
	}
	fmt.Fprintf(p.out, "%s: using %s", n.Title, st.Path)
	if st.Version != "" {
		fmt.Fprintf(p.out, " (%s)", st.Version)
	}
	fmt.Fprintln(p.out, "; sessions started from now have it on their PATH")
	return nil
}

// forget drops a located file.
func (p pluginCLI) forget(id string) error {
	n, err := p.need(id)
	if err != nil {
		return err
	}
	if err := plugin.ForgetNeed(needEnv(p.home), n.Need); err != nil {
		return fmt.Errorf("plugins: %w", err)
	}
	fmt.Fprintf(p.out, "%s: forgot the located file\n", n.Title)
	return nil
}

// setup installs a need with a method (the only available one when none is named), after showing the command and asking.
func (p pluginCLI) setup(ctx context.Context, id, method string, yes bool, in io.Reader) error {
	n, err := p.need(id)
	if err != nil {
		return err
	}
	env := needEnv(p.home)
	st := plugin.CheckNeeds(env, []plugin.OwnedNeed{n})[0]
	var pick *plugin.MethodStatus
	var avail []string
	for i, m := range st.Methods {
		if m.Available {
			avail = append(avail, m.ID)
		}
		if m.ID == method {
			pick = &st.Methods[i]
		}
	}
	if method == "" {
		switch {
		case len(avail) > 1:
			return fmt.Errorf("plugins: %s can be installed with %s: name one (sleipnir plugins setup %s METHOD)", n.Title, strings.Join(avail, ", "), id)
		case len(avail) == 1:
			method = avail[0]
		case len(st.Methods) > 0:
			method = st.Methods[0].ID // its reason says what is missing
		}
		for i := range st.Methods {
			if st.Methods[i].ID == method {
				pick = &st.Methods[i]
			}
		}
	}
	switch {
	case pick == nil && method != "":
		return fmt.Errorf("plugins: %s has no install method %q here (see sleipnir plugins needs)", n.Title, method)
	case pick == nil:
		return fmt.Errorf("plugins: %s has no install method for this machine; install it yourself, then: sleipnir plugins locate %s PATH", n.Title, id)
	case !pick.Available:
		return fmt.Errorf("plugins: %s: %s", pick.Title, pick.Reason)
	}
	if len(pick.Argv) > 0 {
		fmt.Fprintf(p.out, "This runs, as you:\n  %s\n", strings.Join(pick.Argv, " "))
	}
	if pick.Connect != "" {
		fmt.Fprintf(p.out, "It %s.\n", pick.Connect)
	}
	if !yes {
		fmt.Fprint(p.out, "go ahead? [y/N] ")
		line, _ := bufio.NewReader(in).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("plugins: not installed")
		}
	}
	got, err := plugin.InstallNeed(ctx, env, n.Need, pick.ID, p.out)
	if err != nil {
		return fmt.Errorf("plugins: %w", err)
	}
	fmt.Fprintf(p.out, "%s is ready (%s)\n", n.Title, got.Path)
	if pick.Note != "" {
		fmt.Fprintln(p.out, "Next:", pick.Note)
	}
	return nil
}
