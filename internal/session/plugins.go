package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/plugin"
	"github.com/anemos-labs/sleipnir/internal/skills/mdfile"
)

// resolvePlugins computes the session's plugin set from the configuration,
// its report (which layer set what) and the options that stand for flags
// (--no-web, --no-mcp, --mailman, --auto-docs, --manager-approves, no
// recon). Options are not changed: a restart parses its flags again and reads
// the configuration afresh, so a plugin changed on the Plugins page takes
// effect then.
func resolvePlugins(cfg *config.Config, rep *config.Report, o Options) (*plugin.Set, []string) {
	in := plugin.FromConfig(cfg, rep)
	flag := func(id string, on bool) { in.Add(id, on, plugin.LayerFlag) }
	if o.NoWeb {
		flag(plugin.Web, false)
	}
	if o.NoMCP {
		flag(plugin.MCP, false)
	}
	if o.NoRecon {
		flag(plugin.Recon, false)
	}
	if o.NoMemory {
		flag(plugin.Lessons, false) // the memory tool stays in the list and refuses (see build)
	}
	if o.Mailman != nil {
		flag(plugin.Mailman, *o.Mailman)
	}
	if o.AutoDocs != nil {
		flag(plugin.AutoDocs, *o.AutoDocs)
	}
	if o.ManagerApproves != nil {
		flag(plugin.ManagerApproves, *o.ManagerApproves)
	}
	return plugin.Resolve(plugin.Catalogue(o.Home, o.Root, o.TrustProject), in)
}

// Plugins is the session's plugin set: what was on when it started.
func (s *Session) Plugins() *plugin.Set { return s.plugins }

// pluginOn reports whether a plugin is on in this session (true without a set).
func (s *Session) pluginOn(id string) bool { return s.plugins.On(id) }

// emitPlugins writes one plugin.state event per plugin that is not simply on,
// and one summary event, so a replay and the interfaces can show the set.
func (s *Session) emitPlugins() {
	if s.plugins == nil || s.Log == nil {
		return
	}
	off := s.plugins.Off()
	s.Log.Emit("", "plugins", map[string]any{"profile": s.plugins.Profile, "off": off})
	for _, st := range s.plugins.List() {
		if st.State == plugin.StatePending || st.State == plugin.StateError {
			s.Log.Emit("", "plugin.state", map[string]any{"id": st.ID, "state": st.State, "reason": st.Reason})
		}
	}
}

// askedShape is the team the session's options asked for, kept when the team
// plugin is off so a restart that turns it back on brings the team back.
type askedShape struct {
	swarm       bool
	workers     int
	team        []TeamSlot
	defaultTeam bool
}

// externalDirs lists the <kind> directories (skills, commands, agents) of the
// external plugins that are on, namespaced by plugin id. The person turned each
// one on, so its definitions are trusted like their own.
func (s *Session) externalDirs(kind string) []mdfile.Extra {
	var out []mdfile.Extra
	for _, st := range s.plugins.List() {
		if !st.External || st.State != plugin.StateOn {
			continue
		}
		d := filepath.Join(st.Dir, kind)
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			out = append(out, mdfile.Extra{Dir: d, Namespace: st.ID, Trusted: true})
		}
	}
	return out
}

// emptyDir is a directory that holds no definitions: discovery pointed at it
// as "home" (with no project) finds nothing. It is the way a plugin that is
// off keeps the user's directories out.
func emptyDir(s *Session) string { return filepath.Join(s.Dir, "plugin-off") }

// externalHooks adds the hooks of the external plugins that are on to the
// configuration's, event by event, after the user's own.
func (s *Session) externalHooks(cfg *config.Config) (*config.Config, []string) {
	var warns []string
	merged := cfg
	for _, st := range s.plugins.List() {
		if !st.External || st.State != plugin.StateOn {
			continue
		}
		h, err := plugin.HookEntries(st.Manifest, st.Config)
		if err != nil {
			warns = append(warns, "plugins: "+st.ID+": hooks: "+err.Error())
			continue
		}
		if len(h) == 0 {
			continue
		}
		if merged == cfg {
			c := *cfg
			c.Hooks = map[string]json.RawMessage{}
			for k, v := range cfg.Hooks {
				c.Hooks[k] = v
			}
			merged = &c
		}
		for ev, raw := range h {
			merged.Hooks[ev] = appendJSONArray(merged.Hooks[ev], raw)
		}
	}
	return merged, warns
}

// appendJSONArray concatenates two JSON arrays (either may be empty); a value
// that is not an array is kept as it is, so the hooks parser reports it.
func appendJSONArray(a, b json.RawMessage) json.RawMessage {
	var x, y []json.RawMessage
	if len(a) == 0 {
		return b
	}
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return b
	}
	out, _ := json.Marshal(append(x, y...))
	return out
}

// SlashOff reports, for a chat command such as "/goal", whether the plugin that
// provides it is off in this session, and if so the line to show instead.
func (s *Session) SlashOff(cmd string) (string, bool) {
	if s == nil {
		return "", false
	}
	name := strings.TrimPrefix(strings.ToLower(cmd), "/")
	for _, st := range s.plugins.List() {
		if st.State == plugin.StateOn {
			continue
		}
		for _, c := range st.Contributes {
			if c.Kind == plugin.KindSlash && c.Name == name {
				return fmt.Sprintf("/%s belongs to the %s plugin (%s), which is %s in this session; turn it on with /plugins enable %s or on the Plugins page", name, st.Title, st.ID, st.State, st.ID), true
			}
		}
	}
	return "", false
}
