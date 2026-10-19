package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/plugin"
	"github.com/anemos-labs/sleipnir/internal/session"
)

// init registers plugin management with the CLI dispatcher.
func init() { extraCommands["plugins"] = cmdPlugins }

const pluginsUsage = `usage: sleipnir plugins [command] [flags]

Everything Sleipnir does is a plugin that can be switched on and off: the tools, skills, commands,
hooks, the team and its roles, standing goals, GitHub delivery and every view of the web page. A
plugin that is on but needs one that is off is "pending". A few core pieces are always on.

Choices are written to your user configuration (~/.sleipnir/config.json), or with --project to the
project's (.sleipnir/config.json) or with --local to .sleipnir/config.local.json, as a minimal
difference from the profile. They take effect when a session starts (in a running chat or page:
when its team starts again).

commands:
  (none), list         every plugin: state, category, which layer decided, what it adds
  info ID              one plugin in full
  enable ID...         turn plugins on
  disable ID...        turn plugins off
  reset ID...          back to what the profile says
  profile [NAME]       show the profiles, or choose one (standard, minimal, team, headless)
  set ID KEY=VALUE...  change a plugin's settings (KEY= removes a setting)
  add PATH|GIT-URL     install a plugin directory into ~/.sleipnir/plugins (it stays off until enabled)
  remove ID            uninstall a plugin from ~/.sleipnir/plugins
  needs                the programs and tool servers the plugins that are on use, and whether each is found
  locate NEED PATH     use this file for a program a plugin needs (when it is not found by itself)
  forget NEED          drop a located file
  setup NEED [METHOD]  install a need with one of its install methods: shows the command and asks first

flags:
  --cwd DIR            the project to look at (default: the current directory)
  --project            write to the project's configuration instead of yours
  --local              write to the project's git-ignored local configuration
  --trust-project      also read the project's own plugins and settings
  --json               list, info and needs: machine-readable output
  --yes                setup: run without asking (scripts: you have read the command in "needs")
`

// cmdPlugins lists, switches, configures, installs and removes plugins (internal/plugin).
func cmdPlugins(ctx context.Context, args []string) error {
	sub, rest := "list", args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, rest = args[0], args[1:]
	}
	if sub == "help" {
		printHelp(os.Stderr, pluginsUsage)
		return nil
	}
	fs := newFlagSet("plugins", flag.ContinueOnError)
	fs.Usage = func() { printHelp(os.Stderr, pluginsUsage) }
	cwd := fs.String("cwd", "", "the project to look at (default: the current directory)")
	project := fs.Bool("project", false, "write to the project's configuration")
	local := fs.Bool("local", false, "write to the project's local configuration")
	trustProject := fs.Bool("trust-project", false, "also read the project's own plugins and settings")
	asJSON := fs.Bool("json", false, "machine-readable output")
	yes := fs.Bool("yes", false, "setup: run without asking")
	pos, err := parseInterspersed(fs, rest)
	if err != nil {
		return err
	}
	dir := *cwd
	if dir == "" {
		if dir, err = os.Getwd(); err != nil {
			return err
		}
	}
	if dir, err = filepath.Abs(dir); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	root, _ := config.FindRoot(dir)
	target := config.UserConfigPath(home)
	switch {
	case *project && *local:
		return errors.New("plugins: --project and --local are exclusive")
	case *project:
		target = config.ProjectConfigPath(root)
	case *local:
		target = config.LocalConfigPath(root)
	}
	p := pluginCLI{out: os.Stdout, home: home, cwd: dir, root: root, trust: *trustProject, target: target, json: *asJSON}
	switch sub {
	case "list", "ls":
		return p.list()
	case "info", "show":
		if len(pos) != 1 {
			return errors.New("usage: sleipnir plugins info ID")
		}
		return p.info(pos[0])
	case "enable", "on", "disable", "off", "reset":
		if len(pos) == 0 {
			return fmt.Errorf("usage: sleipnir plugins %s ID...", sub)
		}
		return p.toggle(sub, pos)
	case "profile", "profiles":
		return p.profile(pos)
	case "set", "config":
		if len(pos) < 2 {
			return errors.New("usage: sleipnir plugins set ID KEY=VALUE...")
		}
		return p.set(pos[0], pos[1:])
	case "add", "install":
		if len(pos) != 1 {
			return errors.New("usage: sleipnir plugins add PATH|GIT-URL")
		}
		m, err := plugin.Install(ctx, home, pos[0])
		if err != nil {
			return fmt.Errorf("plugins: %w", err)
		}
		fmt.Fprintf(p.out, "installed %s (%s) in %s\n", m.ID, m.Title, m.Dir)
		if s := plugin.Summary(m.Contributes); s != "" {
			fmt.Fprintf(p.out, "  adds: %s\n", s)
		}
		fmt.Fprintf(p.out, "It is off. Turn it on with: sleipnir plugins enable %s\n", m.ID)
		return nil
	case "remove", "rm", "uninstall":
		if len(pos) != 1 {
			return errors.New("usage: sleipnir plugins remove ID")
		}
		dir, err := plugin.Remove(home, pos[0])
		if err != nil {
			return fmt.Errorf("plugins: %w", err)
		}
		fmt.Fprintf(p.out, "removed %s (%s)\n", pos[0], dir)
		return nil
	case "needs":
		return p.needs()
	case "locate":
		if len(pos) != 2 {
			return errors.New("usage: sleipnir plugins locate NEED PATH")
		}
		return p.locate(ctx, pos[0], pos[1])
	case "forget":
		if len(pos) != 1 {
			return errors.New("usage: sleipnir plugins forget NEED")
		}
		return p.forget(pos[0])
	case "setup":
		if len(pos) < 1 || len(pos) > 2 {
			return errors.New("usage: sleipnir plugins setup NEED [METHOD]")
		}
		method := ""
		if len(pos) == 2 {
			method = pos[1]
		}
		return p.setup(ctx, pos[0], method, *yes, os.Stdin)
	}
	printHelp(os.Stderr, pluginsUsage)
	return fmt.Errorf("plugins: unknown command %q", sub)
}

// pluginCLI carries what every plugins subcommand needs.
type pluginCLI struct {
	out             io.Writer
	home, cwd, root string
	trust, json     bool
	target          string
}

// load resolves the plugin set for the directory.
func (p pluginCLI) load() (*plugin.Set, []string, error) {
	set, _, warns, err := plugin.LoadSet(p.cwd, p.root, p.home, p.trust)
	if err != nil {
		return nil, nil, fmt.Errorf("plugins: %w", err)
	}
	return set, warns, nil
}

// list prints every plugin by category.
func (p pluginCLI) list() error {
	set, warns, err := p.load()
	if err != nil {
		return err
	}
	if p.json {
		return writeJSON(p.out, map[string]any{"profile": set.Profile, "plugins": set.List(), "warnings": warns})
	}
	fmt.Fprintf(p.out, "profile: %s\n", set.Profile)
	tw := tabwriter.NewWriter(p.out, 0, 2, 2, ' ', 0)
	cat := ""
	for _, st := range set.List() {
		if st.Category != cat {
			cat = st.Category
			fmt.Fprintf(tw, "\n%s\n", strings.ToUpper(cat))
		}
		state := st.State
		if st.Reason != "" && st.State != plugin.StateOn {
			state += " (" + st.Reason + ")"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n", st.ID, state, st.Layer, st.Description)
	}
	tw.Flush()
	for _, w := range warns {
		fmt.Fprintln(p.out, "warning:", w)
	}
	return nil
}

// info prints one plugin.
func (p pluginCLI) info(id string) error {
	set, _, err := p.load()
	if err != nil {
		return err
	}
	st, ok := set.Get(id)
	if !ok {
		return fmt.Errorf("plugins: no plugin %q; `sleipnir plugins` lists them", id)
	}
	if p.json {
		return writeJSON(p.out, st)
	}
	fmt.Fprintf(p.out, "%s: %s\n  %s\n", st.ID, st.Title, st.Description)
	fmt.Fprintf(p.out, "  state:    %s", st.State)
	if st.Reason != "" {
		fmt.Fprintf(p.out, " (%s)", st.Reason)
	}
	fmt.Fprintf(p.out, "\n  decided:  %s (profile default: %s)\n  category: %s\n", st.Layer, onOff(st.Default), st.Category)
	if st.Core {
		fmt.Fprintf(p.out, "  core:     always on: %s\n", st.CoreReason)
	}
	if len(st.Requires) > 0 {
		fmt.Fprintf(p.out, "  needs:    %s\n", strings.Join(st.Requires, ", "))
	}
	if s := plugin.Summary(st.Contributes); s != "" {
		fmt.Fprintf(p.out, "  adds:     %s\n", s)
	}
	if lp := plugin.LegacyPaths()[st.ID]; lp != "" {
		fmt.Fprintf(p.out, "  also set by: %s (older setting)\n", lp)
	}
	if st.Dir != "" {
		fmt.Fprintf(p.out, "  directory: %s\n", st.Dir)
	}
	if schema := st.Manifest.Config; !schema.Empty() {
		keys := make([]string, 0, len(schema.Properties))
		for k := range schema.Properties {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintln(p.out, "  settings:")
		for _, k := range keys {
			f := schema.Properties[k]
			v, set := st.Config[k]
			cur := "(default)"
			if set {
				cur = fmt.Sprint(v)
			}
			fmt.Fprintf(p.out, "    %s = %s  %s: %s\n", k, cur, f.Type, f.Description)
		}
	}
	return nil
}

// onOff words a boolean.
func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// toggle enables, disables or resets plugins in the target file.
func (p pluginCLI) toggle(verb string, ids []string) error {
	set, _, err := p.load()
	if err != nil {
		return err
	}
	profile := plugin.ConfiguredProfile(p.cwd, p.root, p.home)
	for _, id := range ids {
		st, ok := set.Get(id)
		if !ok {
			return fmt.Errorf("plugins: no plugin %q; `sleipnir plugins` lists them", id)
		}
		if st.Core {
			return fmt.Errorf("plugins: %s is core and always on: %s", id, st.CoreReason)
		}
		var v bool
		switch verb {
		case "enable", "on":
			v = true
		case "disable", "off":
			v = false
		default:
			v = st.Default
		}
		if err := plugin.Save(p.target, profile, id, &v, nil); err != nil {
			return fmt.Errorf("plugins: %w", err)
		}
	}
	after, _, err := p.load()
	if err != nil {
		return err
	}
	for _, id := range ids {
		st, _ := after.Get(id)
		line := fmt.Sprintf("%s: %s", id, st.State)
		if st.Reason != "" {
			line += " (" + st.Reason + ")"
		}
		if st.Layer != plugin.LayerUser && st.Layer != plugin.LayerProject && st.Layer != plugin.LayerLocal && st.Layer != plugin.LayerProfile && st.Layer != plugin.LayerDefault {
			line += "; note: the " + st.Layer + " layer decides it, over the file just written"
		}
		fmt.Fprintln(p.out, line)
	}
	fmt.Fprintf(p.out, "written to %s; a running session takes it when its team starts again\n", p.target)
	return nil
}

// profile shows the profiles or writes one.
func (p pluginCLI) profile(args []string) error {
	if len(args) == 0 {
		cur := plugin.ConfiguredProfile(p.cwd, p.root, p.home)
		if cur == "" {
			cur = plugin.DefaultProfile
		}
		for _, pr := range plugin.Profiles() {
			mark := " "
			if pr.ID == cur {
				mark = "*"
			}
			fmt.Fprintf(p.out, "%s %-9s %s\n", mark, pr.ID, pr.Description)
		}
		return nil
	}
	if _, ok := plugin.ProfileByID(args[0]); !ok {
		return fmt.Errorf("plugins: no profile %q", args[0])
	}
	if err := plugin.SaveProfile(p.target, args[0]); err != nil {
		return fmt.Errorf("plugins: %w", err)
	}
	fmt.Fprintf(p.out, "profile: %s (written to %s)\n", args[0], p.target)
	return nil
}

// set writes KEY=VALUE settings of one plugin; values are read as JSON when they parse, else as strings.
func (p pluginCLI) set(id string, kvs []string) error {
	set, _, err := p.load()
	if err != nil {
		return err
	}
	st, ok := set.Get(id)
	if !ok {
		return fmt.Errorf("plugins: no plugin %q", id)
	}
	cur := map[string]any{}
	for k, v := range st.Config {
		cur[k] = v
	}
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" {
			return fmt.Errorf("plugins: %q is not KEY=VALUE", kv)
		}
		if v == "" {
			delete(cur, k)
			continue
		}
		cur[k] = parseSetting(v)
	}
	if err := plugin.Save(p.target, "", id, nil, cur); err != nil {
		return fmt.Errorf("plugins: %w", err)
	}
	fmt.Fprintf(p.out, "%s settings written to %s\n", id, p.target)
	return nil
}

// parseSetting reads a command-line value: a number, true/false, JSON, or a string.
func parseSetting(v string) any {
	if b, err := strconv.ParseBool(v); err == nil {
		return b
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return f
	}
	var j any
	if json.Unmarshal([]byte(v), &j) == nil {
		return j
	}
	return v
}

// writeJSON prints v as indented JSON.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// pluginsSlash answers /plugins in a chat (the terminal's and the page's): with no argument the plugins of this session and
// what changed since it started; "enable ID", "disable ID" and "reset ID" write the user's configuration, which the session
// takes when its team starts again (/restart).
func pluginsSlash(s *session.Session, arg string) string {
	f := strings.Fields(arg)
	home, cwd, root := s.Home(), s.Cwd(), s.Root()
	trust := s.TrustProject()
	var b strings.Builder
	if len(f) >= 2 {
		verb := strings.ToLower(f[0])
		p := pluginCLI{out: &b, home: home, cwd: cwd, root: root, trust: trust, target: config.UserConfigPath(home)}
		switch verb {
		case "enable", "on", "disable", "off", "reset":
			if err := p.toggle(verb, f[1:]); err != nil {
				return err.Error()
			}
			b.WriteString("/restart starts the team again with it\n")
			return b.String()
		}
		return "usage: /plugins [enable|disable|reset ID...]"
	}
	if len(f) == 1 {
		return "usage: /plugins [enable|disable|reset ID...]"
	}
	next, _, warns, err := plugin.LoadSet(cwd, root, home, trust)
	if err != nil {
		return "plugins: " + err.Error()
	}
	marked, changed := plugin.MarkRestart(s.Plugins(), next)
	fmt.Fprintf(&b, "profile %s. Off in this session: ", marked.Profile)
	var off, pending, restart []string
	for _, st := range marked.List() {
		switch st.State {
		case plugin.StateOff:
			off = append(off, st.ID)
		case plugin.StatePending, plugin.StateError:
			pending = append(pending, st.ID+" ("+st.Reason+")")
		case plugin.StateRestart:
			restart = append(restart, st.ID+": "+st.Reason)
		}
	}
	if len(off) == 0 {
		b.WriteString("none")
	} else {
		b.WriteString(strings.Join(off, ", "))
	}
	b.WriteString(".\n")
	if len(pending) > 0 {
		fmt.Fprintf(&b, "Waiting on another plugin: %s.\n", strings.Join(pending, "; "))
	}
	if changed {
		fmt.Fprintf(&b, "Changed since this session started (/restart applies them): %s.\n", strings.Join(restart, "; "))
	}
	for _, w := range warns {
		fmt.Fprintf(&b, "warning: %s\n", w)
	}
	b.WriteString("/plugins enable ID or /plugins disable ID switches one; `sleipnir plugins` lists them all.")
	return b.String()
}
