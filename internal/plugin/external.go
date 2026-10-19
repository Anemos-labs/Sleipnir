package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// An external plugin is a directory with a manifest and any of:
//
//	plugin.json           the manifest (or .claude-plugin/plugin.json, the Claude Code layout)
//	skills/<name>/SKILL.md
//	commands/<name>.md
//	agents/<name>.md
//	hooks.json            (or hooks/hooks.json) the "hooks" key's shape: events to matchers
//	mcp.json              (or .mcp.json) {"mcpServers": {...}} or the servers directly
//	web/card.js           a card for the web page (optional web/card.css), shown in a sandboxed frame
//
// The manifest names the plugin (id, or name in the Claude Code layout),
// says what it is (title, description, version) and may declare requires and
// a settings schema (config). External plugins are off until a person turns
// them on. They are read from <home>/.sleipnir/plugins/<dir>/ and, for a
// trusted project only, from <project>/.sleipnir/plugins/<dir>/.

// ManifestFile is the name of an external plugin's manifest.
const ManifestFile = "plugin.json"

// externalDoc is plugin.json as written.
type externalDoc struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Version     string   `json:"version"`
	Requires    []string `json:"requires"`
	Config      *Schema  `json:"config"`
}

// Dirs returns the directories external plugins are read from, user first.
func Dirs(home, root string, trustProject bool) []string {
	var out []string
	if home != "" {
		out = append(out, filepath.Join(home, ".sleipnir", "plugins"))
	}
	if root != "" && trustProject {
		out = append(out, filepath.Join(root, ".sleipnir", "plugins"))
	}
	return out
}

// UserDir is where `sleipnir plugins add` installs plugins.
func UserDir(home string) string { return filepath.Join(home, ".sleipnir", "plugins") }

// LoadDir reads one external plugin directory.
func LoadDir(dir string) (Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if errors.Is(err, os.ErrNotExist) {
		b, err = os.ReadFile(filepath.Join(dir, ".claude-plugin", ManifestFile))
	}
	if err != nil {
		return Manifest{}, err
	}
	var d externalDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return Manifest{}, fmt.Errorf("%s: %w", ManifestFile, err)
	}
	id := d.ID
	if id == "" {
		id = d.Name
	}
	id = strings.ToLower(strings.TrimSpace(id))
	if !ValidID(id) {
		return Manifest{}, fmt.Errorf("%s: id %q is not valid (lower-case letters, digits, - _ . :)", ManifestFile, id)
	}
	if strings.HasPrefix(id, "core:") || strings.HasPrefix(id, ViewPrefix) || strings.HasPrefix(id, RolePrefix) {
		return Manifest{}, fmt.Errorf("%s: id %q uses a namespace reserved for built-in plugins", ManifestFile, id)
	}
	m := Manifest{ID: id, Title: d.Title, Description: d.Description, Version: d.Version, Category: CatExternal,
		External: true, Dir: dir, Requires: d.Requires, Config: d.Config}
	if m.Title == "" {
		m.Title = id
	}
	if m.Description == "" {
		m.Description = "an installed plugin"
	}
	m.Contributes = scanContributions(dir)
	return m, nil
}

// scanContributions lists what a plugin directory contributes.
func scanContributions(dir string) []Contribution {
	var out []Contribution
	list := func(sub, kind string, isDir bool, suffix string) {
		ents, err := os.ReadDir(filepath.Join(dir, sub))
		if err != nil {
			return
		}
		for _, e := range ents {
			name := e.Name()
			if isDir != e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, suffix) {
				continue
			}
			out = append(out, Contribution{kind, strings.TrimSuffix(name, suffix)})
		}
	}
	list("skills", KindSkill, true, "")
	list("commands", KindSlash, false, ".md")
	list("agents", KindRole, false, ".md")
	if HooksFile(dir) != "" {
		out = append(out, Contribution{KindHook, "hooks.json"})
	}
	if servers, _ := MCPServers(Manifest{Dir: dir}, nil); len(servers) > 0 {
		names := make([]string, 0, len(servers))
		for n := range servers {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			out = append(out, Contribution{KindMCP, n})
		}
	}
	if fileExists(filepath.Join(dir, "web", "card.js")) {
		out = append(out, Contribution{KindCard, "card"})
	}
	return out
}

// fileExists reports whether p is a regular file.
func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// HooksFile is the plugin's hooks file, "" when it has none.
func HooksFile(dir string) string {
	for _, p := range []string{filepath.Join(dir, "hooks.json"), filepath.Join(dir, "hooks", "hooks.json")} {
		if fileExists(p) {
			return p
		}
	}
	return ""
}

// expand replaces, in a plugin file's JSON text, ${CLAUDE_PLUGIN_ROOT} and
// ${SLEIPNIR_PLUGIN_ROOT} with the plugin's directory and ${setting:NAME}
// with the value of the plugin's setting NAME (plugins.settings.<id>.NAME, or
// the schema's default; "" when neither), each escaped as JSON string text.
func expand(text, dir string, schema *Schema, settings map[string]any) string {
	esc := func(v string) string {
		q, _ := json.Marshal(v)
		return string(q[1 : len(q)-1])
	}
	pairs := []string{"${CLAUDE_PLUGIN_ROOT}", esc(dir), "${SLEIPNIR_PLUGIN_ROOT}", esc(dir)}
	names := map[string]any{}
	if schema != nil {
		for k, f := range schema.Properties {
			names[k] = f.Default
		}
	}
	for k, v := range settings {
		names[k] = v
	}
	for k, v := range names {
		str := ""
		if v != nil {
			str = fmt.Sprint(v)
		}
		pairs = append(pairs, "${setting:"+k+"}", esc(str))
	}
	return strings.NewReplacer(pairs...).Replace(text)
}

// HookEntries reads the plugin's hooks file as the "hooks" configuration key's
// entries (event -> matchers), expanded as expand says. A file with a
// top-level "hooks" object (the Claude Code layout) is read from inside it.
func HookEntries(m Manifest, settings map[string]any) (map[string]json.RawMessage, error) {
	dir := m.Dir
	p := HooksFile(dir)
	if p == "" {
		return nil, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	s := expand(string(b), dir, m.Config, settings)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(p), err)
	}
	if h, ok := doc["hooks"]; ok {
		var m map[string]json.RawMessage
		if err := json.Unmarshal(h, &m); err != nil {
			return nil, fmt.Errorf("%s: hooks: %w", filepath.Base(p), err)
		}
		return m, nil
	}
	return doc, nil
}

// MCPServers reads the plugin's MCP servers (mcp.json or .mcp.json; either
// {"mcpServers": {...}} or the servers directly), by server name, expanded as
// expand says.
func MCPServers(m Manifest, settings map[string]any) (map[string]json.RawMessage, error) {
	dir := m.Dir
	for _, name := range []string{"mcp.json", ".mcp.json"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		quoted, _ := json.Marshal(dir)
		inner := string(quoted[1 : len(quoted)-1])
		s := strings.NewReplacer("${CLAUDE_PLUGIN_ROOT}", inner, "${SLEIPNIR_PLUGIN_ROOT}", inner).Replace(string(b))
		var doc map[string]json.RawMessage
		if err := json.Unmarshal([]byte(s), &doc); err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if inner, ok := doc["mcpServers"]; ok {
			var m map[string]json.RawMessage
			if err := json.Unmarshal(inner, &m); err != nil {
				return nil, fmt.Errorf("%s: mcpServers: %w", name, err)
			}
			return m, nil
		}
		return doc, nil
	}
	return nil, nil
}

// Discover reads every external plugin in dirs. A directory that is not a
// plugin is skipped with a warning; the first plugin of an id wins (the
// user's over the project's), and a built-in id cannot be taken.
func Discover(dirs []string) ([]Manifest, []string) {
	var out []Manifest
	var warns []string
	seen := map[string]string{}
	for _, m := range Builtin() {
		seen[m.ID] = "built-in"
	}
	for _, base := range dirs {
		ents, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			dir := filepath.Join(base, e.Name())
			m, err := LoadDir(dir)
			if err != nil {
				warns = append(warns, fmt.Sprintf("plugins: %s: %v", dir, err))
				continue
			}
			if prev, dup := seen[m.ID]; dup {
				warns = append(warns, fmt.Sprintf("plugins: %s: id %q is already taken by %s; skipped", dir, m.ID, prev))
				continue
			}
			seen[m.ID] = dir
			out = append(out, m)
		}
	}
	return out, warns
}

// Catalogue is the built-in plugins and the external ones found for home and
// root (the project's only when trusted), in catalogue order. Discovery
// warnings are dropped here; `sleipnir plugins` shows them.
func Catalogue(home, root string, trustProject bool) []Manifest {
	ext, _ := Discover(Dirs(home, root, trustProject))
	ms := append(Builtin(), ext...)
	sortManifests(ms)
	return ms
}
