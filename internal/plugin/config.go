package plugin

import (
	"sort"

	"github.com/anemos-labs/sleipnir/internal/config"
)

// A project file that is not trusted may turn off a built-in plugin, but neither the hooks plugin nor an external one: either
// may run the user's guards as hooks.
func init() {
	builtin := map[string]bool{}
	for _, m := range Builtin() {
		builtin[m.ID] = true
	}
	config.KeepEnabledAgainstProject = func(id string) bool { return id == Hooks || !builtin[id] }
}

// legacy maps settings that switched a feature on or off before plugins to
// the plugin that now owns the feature. They keep working: a value set in a
// layer counts as a choice of that layer, and the higher layer wins over a
// plugins.enabled row.
var legacy = []struct {
	path string
	id   string
	get  func(*config.Config) (bool, bool) // value, and whether it is meaningful
}{
	{"swarm.mailman", Mailman, func(c *config.Config) (bool, bool) { return c.Swarm.Mailman, true }},
	{"swarm.auto_docs", AutoDocs, func(c *config.Config) (bool, bool) {
		if c.Swarm.AutoDocs == nil {
			return false, false
		}
		return *c.Swarm.AutoDocs, true
	}},
	{"swarm.push_progress", PushProgress, func(c *config.Config) (bool, bool) {
		if c.Swarm.PushProgress == nil {
			return false, false
		}
		return *c.Swarm.PushProgress, true
	}},
	{"tools.builtin_skills", BuiltinSkills, func(c *config.Config) (bool, bool) { return c.Tools.BuiltinSkills, true }},
	{"permissions.manager_approves", ManagerApproves, func(c *config.Config) (bool, bool) {
		if c.Permissions.ManagerApproves == nil {
			return false, false
		}
		return *c.Permissions.ManagerApproves, true
	}},
}

// LegacyPaths lists the older settings each plugin also answers to, by plugin id.
func LegacyPaths() map[string]string {
	out := map[string]string{}
	for _, l := range legacy {
		out[l.id] = l.path
	}
	return out
}

// layerOf maps a configuration layer kind to a plugin layer ("" when unset).
func layerOf(kind string) string {
	switch kind {
	case "user":
		return LayerUser
	case "project":
		return LayerProject
	case "local":
		return LayerLocal
	case "env":
		return LayerEnv
	case "overrides":
		return LayerFlag
	}
	return ""
}

// FromConfig builds the Input of a loaded configuration: its profile, its
// plugins.enabled rows and the older settings that map to plugins, each with
// the layer that set it (rep may be nil: every value then counts as the
// user's).
func FromConfig(cfg *config.Config, rep *config.Report) Input {
	in := Input{}
	if cfg == nil {
		return in
	}
	in.Profile = cfg.Plugins.Profile
	in.Settings = cfg.Plugins.Settings
	layer := func(path string) string {
		if rep == nil {
			return LayerUser
		}
		src, ok := rep.Origins[path]
		if !ok {
			return ""
		}
		return layerOf(rep.LayerKind(src))
	}
	for _, l := range legacy {
		v, ok := l.get(cfg)
		if !ok {
			continue
		}
		if ly := layer(l.path); ly != "" {
			in.Add(l.id, v, ly)
		}
	}
	ids := make([]string, 0, len(cfg.Plugins.Enabled))
	for id := range cfg.Plugins.Enabled {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		ly := layer(config.Path("plugins", "enabled", id))
		if ly == "" {
			ly = LayerUser
		}
		in.Add(id, cfg.Plugins.Enabled[id], ly)
	}
	return in
}

// Save writes one plugin choice to the configuration file at path as a
// minimal difference: a choice equal to what the file's profile (or, without
// one, the configured profile) already gives is removed instead of written,
// and an older setting for the same feature in that file is folded into the
// row (removed), so the file holds one answer. enabled nil leaves the on/off
// row alone; settings nil leaves the settings alone, an empty map removes them.
func Save(path, profile, id string, enabled *bool, settings map[string]any) error {
	return config.Save(path, choicePatch(profile, id, enabled, settings))
}

// SaveWithin is Save for a project's file (path, under root): config.SaveWithin keeps every read and write within root.
func SaveWithin(root, path, profile, id string, enabled *bool, settings map[string]any) error {
	return config.SaveWithin(root, path, choicePatch(profile, id, enabled, settings))
}

// choicePatch is the configuration patch of one plugin choice (Save).
func choicePatch(profile, id string, enabled *bool, settings map[string]any) map[string]any {
	patch := map[string]any{}
	plugins := map[string]any{}
	if enabled != nil {
		set, _ := Resolve(Builtin(), Input{Profile: profile})
		st, known := set.Get(id) // an external plugin is not in Builtin: its row is always written
		if known && *enabled == st.Default {
			plugins["enabled"] = map[string]any{id: nil}
		} else {
			plugins["enabled"] = map[string]any{id: *enabled}
		}
		for _, l := range legacy {
			if l.id == id {
				sec, key, _ := cut(l.path)
				patch[sec] = map[string]any{key: nil}
			}
		}
	}
	if settings != nil {
		if len(settings) == 0 {
			plugins["settings"] = map[string]any{id: nil}
		} else {
			plugins["settings"] = map[string]any{id: settings}
		}
	}
	patch["plugins"] = plugins
	return patch
}

// SaveProfile writes the profile choice to the file at path ("" removes it).
func SaveProfile(path, profile string) error { return config.Save(path, profilePatch(profile)) }

// SaveProfileWithin is SaveProfile for a project's file (path, under root), as SaveWithin.
func SaveProfileWithin(root, path, profile string) error {
	return config.SaveWithin(root, path, profilePatch(profile))
}

// profilePatch is the configuration patch of a profile choice.
func profilePatch(profile string) map[string]any {
	var v any = profile
	if profile == "" || profile == DefaultProfile {
		v = nil
	}
	return map[string]any{"plugins": map[string]any{"profile": v}}
}

// cut splits "section.key" at its first dot.
func cut(p string) (string, string, bool) {
	for i := 0; i < len(p); i++ {
		if p[i] == '.' {
			return p[:i], p[i+1:], true
		}
	}
	return p, "", false
}

// LoadSet loads the configuration for a directory and resolves the plugin set
// it gives, with the external plugins found for it: what a session started
// there now would have. It returns the set, the configuration report and the
// warnings of discovery and resolution.
func LoadSet(cwd, root, home string, trustProject bool) (*Set, *config.Report, []string, error) {
	if root == "" {
		root, _ = config.FindRoot(cwd)
	}
	cfg, rep, err := config.Load(config.LoadOpts{Cwd: cwd, Root: root, Home: home, UntrustedProject: !trustProject})
	if err != nil {
		return nil, nil, nil, err
	}
	ext, warns := Discover(Dirs(home, root, trustProject))
	cat := append(Builtin(), ext...)
	sortManifests(cat)
	set, rwarns := Resolve(cat, FromConfig(cfg, rep))
	return set, rep, append(warns, rwarns...), nil
}

// ConfiguredProfile is the profile a configuration names ("" for the default).
func ConfiguredProfile(cwd, root, home string) string {
	if root == "" {
		root, _ = config.FindRoot(cwd)
	}
	cfg, _, err := config.Load(config.LoadOpts{Cwd: cwd, Root: root, Home: home, UntrustedProject: true})
	if err != nil {
		return ""
	}
	return cfg.Plugins.Profile
}
