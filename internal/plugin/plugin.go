// Package plugin is Sleipnir's plugin model: every capability of the harness
// is a plugin with a manifest, and a person switches plugins on and off in the
// configuration, the command line, the chat (/plugins) and the web page.
//
// The model follows three rules:
//
//  1. A plugin names the services it needs (Manifest.Requires) and the
//     services it provides (its id and Manifest.Provides). A plugin that is on
//     but whose requirement is off or missing is pending: a normal state that
//     is shown with the missing requirement, not an error. It activates once
//     the requirement is on.
//  2. Everything a plugin registers returns a disposer kept in the plugin's
//     Scope; turning the plugin off runs them in reverse order (Kernel).
//  3. The enabled set is computed from layers (built-in default, profile,
//     user, project, local, environment, flags) and a change is written as a
//     minimal difference from the profile, so upgrades keep their defaults.
//
// Go code cannot be unloaded from a running process, so a built-in plugin
// that is off is one that was never activated. Changes take effect when a
// session's team starts again (the same restart that applies a model change),
// except for plugins that declare Live.
//
// A few pieces have nothing below them and are core: they are listed in the
// catalogue, always on, with the reason (Manifest.Core).
//
// Plugin order is deterministic (requirements first, then by id), so anything
// a plugin adds to a prompt lands in the same bytes on every run.
package plugin

import (
	"sort"
	"strings"
)

// Categories group plugins on the Plugins page and in `sleipnir plugins`.
const (
	CatTools      = "tools"      // tools the agents call
	CatExtensions = "extensions" // skills, commands, agents, hooks, instruction files
	CatTeam       = "team"       // the swarm and what runs around it
	CatInterface  = "interface"  // views of the web page and panels of the terminal
	CatCore       = "core"       // always on
	CatExternal   = "external"   // installed from a directory
)

// CategoryOrder is the order categories are shown in.
var CategoryOrder = []string{CatTools, CatExtensions, CatTeam, CatInterface, CatExternal, CatCore}

// Contribution kinds: what a plugin adds to the harness.
const (
	KindTool     = "tool"
	KindSlash    = "slash"
	KindCLI      = "cli"
	KindSkill    = "skill"
	KindRole     = "role"
	KindHook     = "hook"
	KindMCP      = "mcp"
	KindPrompt   = "prompt"
	KindProvider = "provider"
	KindView     = "web.view"
	KindCard     = "web.card"
	KindSetting  = "web.setting"
	KindNav      = "web.nav"
	KindPanel    = "tui.panel"
	KindEvent    = "event"
)

// Contribution is one thing a plugin adds: a tool, a slash command, a view...
type Contribution struct {
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// Field is one setting of a plugin's configuration schema: a JSON Schema
// subset of string, number, integer and boolean fields, optionally an enum.
type Field struct {
	Type        string   `json:"type"`
	Title       string   `json:"title,omitempty"`
	Description string   `json:"description,omitempty"`
	Default     any      `json:"default,omitempty"`
	Enum        []string `json:"enum,omitempty"`
}

// Schema is a plugin's configuration schema: an object of Fields.
type Schema struct {
	Type       string           `json:"type"`
	Properties map[string]Field `json:"properties,omitempty"`
}

// Empty reports whether the schema declares no settings.
func (s *Schema) Empty() bool { return s == nil || len(s.Properties) == 0 }

// Manifest describes a plugin. Built-in manifests are in Builtin; an external
// plugin's manifest is its plugin.json (see Load in external.go).
type Manifest struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"`
	// Core plugins are always on; CoreReason says why they cannot be turned off.
	Core       bool   `json:"core,omitempty"`
	CoreReason string `json:"core_reason,omitempty"`
	// Off marks a plugin that is off unless a profile or a person turns it on.
	Off bool `json:"off,omitempty"`
	// Experimental plugins are off by default and shown as such.
	Experimental bool `json:"experimental,omitempty"`
	// Requires lists the ids (or provided service names) this plugin needs.
	Requires []string `json:"requires,omitempty"`
	// Provides lists service names this plugin offers besides its id.
	Provides    []string       `json:"provides,omitempty"`
	Contributes []Contribution `json:"contributes,omitempty"`
	Config      *Schema        `json:"config_schema,omitempty"`
	// Live plugins take a change at once, without the team starting again.
	Live bool `json:"live,omitempty"`
	// External is set for a plugin loaded from a directory; Dir is that directory.
	External bool   `json:"external,omitempty"`
	Dir      string `json:"dir,omitempty"`
	Version  string `json:"version,omitempty"`
}

// DefaultOn reports whether the plugin is on when no profile or layer says otherwise.
func (m Manifest) DefaultOn() bool {
	return m.Core || (!m.Off && !m.Experimental && !m.External)
}

// sortManifests orders manifests by category (CategoryOrder) and then id.
func sortManifests(ms []Manifest) {
	rank := map[string]int{}
	for i, c := range CategoryOrder {
		rank[c] = i
	}
	sort.SliceStable(ms, func(i, j int) bool {
		ri, okI := rank[ms[i].Category]
		rj, okJ := rank[ms[j].Category]
		if !okI {
			ri = len(CategoryOrder)
		}
		if !okJ {
			rj = len(CategoryOrder)
		}
		if ri != rj {
			return ri < rj
		}
		return ms[i].ID < ms[j].ID
	})
}

// ValidID reports whether id can name a plugin: lower-case letters, digits, '-',
// '_', '.' and ':' (for namespaces such as view:cache), 1 to 64 characters, not
// starting with a separator.
func ValidID(id string) bool {
	if id == "" || len(id) > 64 || strings.ContainsAny(id[:1], "-_.:") {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == ':':
		default:
			return false
		}
	}
	return true
}

// Summary describes a plugin's contributions in a few words, such as
// "tools: read, write; view: cache".
func Summary(cs []Contribution) string {
	var order []string
	by := map[string][]string{}
	for _, c := range cs {
		k := strings.TrimPrefix(strings.TrimPrefix(c.Kind, "web."), "tui.")
		if _, ok := by[k]; !ok {
			order = append(order, k)
		}
		by[k] = append(by[k], c.Name)
	}
	parts := make([]string, 0, len(order))
	for _, k := range order {
		parts = append(parts, k+": "+strings.Join(by[k], ", "))
	}
	return strings.Join(parts, "; ")
}
