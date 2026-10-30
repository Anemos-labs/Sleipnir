package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/skills/mdfile"
)

// The plugin API is the contract an external plugin is written against: the manifest's fields, the layout of its directory
// (skills, commands, agents, hooks, MCP servers, the web card), the placeholders its files may use, and the ids of the built-in
// plugins it may require. A plugin says which version it was written for with "api" in plugin.json; one that says nothing was
// written for version 1.
//
// Sleipnir reads every version from MinAPIVersion to APIVersion. A change that would make a plugin of an older version load
// differently is not made to that version: it gets a new version, and the older one keeps working, through a shim when it must.
// The plugins of internal/plugin/testdata/contract hold each version to what it promised (TestPluginContract).
const (
	// APIVersion is the newest plugin API this Sleipnir reads.
	APIVersion = 1
	// MinAPIVersion is the oldest plugin API this Sleipnir still reads.
	MinAPIVersion = 1
)

// RenamedPlugins maps a built-in plugin id that was renamed to the id it has now. A plugin's requires naming an old id is read
// as the new one, so a rename never leaves an installed plugin pending.
var RenamedPlugins = map[string]string{}

// manifestFields are the fields of plugin.json the API knows, by name; Check reports any other as unknown (a typo, or a field of
// a newer API), and loading ignores it.
var manifestFields = map[string]bool{
	"id": true, "name": true, "title": true, "description": true, "version": true, "api": true, "requires": true,
	"config": true, "needs": true,
	// fields of the Claude Code layout that the plugin API reads nothing from
	"author": true, "homepage": true, "repository": true, "license": true, "keywords": true,
}

// apiError says why a plugin of API version v cannot run here ("" when it can).
func apiError(v int) string {
	switch {
	case v > APIVersion:
		return fmt.Sprintf("written for plugin API %d; this Sleipnir reads API %d and older: update Sleipnir", v, APIVersion)
	case v < MinAPIVersion:
		return fmt.Sprintf("written for plugin API %d, which this Sleipnir no longer reads (API %d to %d): update the plugin", v, MinAPIVersion, APIVersion)
	}
	return ""
}

// renamedRequires is requires with each renamed built-in id replaced by its current one.
func renamedRequires(reqs []string) []string {
	if len(reqs) == 0 || len(RenamedPlugins) == 0 {
		return reqs
	}
	out := make([]string, len(reqs))
	for i, r := range reqs {
		if to, ok := RenamedPlugins[r]; ok {
			r = to
		}
		out[i] = r
	}
	return out
}

// Problem is one finding of Check: an error stops the plugin from loading or from doing part of what it declares; a warning is
// something it does that a later Sleipnir may read differently, or that is probably a mistake.
type Problem struct {
	Error bool   `json:"error"`
	Where string `json:"where"`
	What  string `json:"what"`
}

// String is the problem on one line.
func (p Problem) String() string {
	kind := "warning"
	if p.Error {
		kind = "error"
	}
	return kind + ": " + p.Where + ": " + p.What
}

// Check reads a plugin directory as Sleipnir would and reports what would go wrong: the manifest, its API version, its requires
// against the catalogue known (ids that do not exist; nil skips that), its hooks and MCP servers files, and the front matter of its
// skills, commands and agents. It returns the manifest when it loads.
func Check(dir string, known []string) (Manifest, []Problem) {
	var ps []Problem
	add := func(isErr bool, where, format string, args ...any) {
		ps = append(ps, Problem{Error: isErr, Where: where, What: fmt.Sprintf(format, args...)})
	}
	m, err := LoadDir(dir)
	if err != nil {
		add(true, ManifestFile, "%v", err)
		return Manifest{}, ps
	}
	if b, err := manifestBytes(dir); err == nil {
		var raw map[string]json.RawMessage
		if json.Unmarshal(b, &raw) == nil {
			var unknown []string
			for k := range raw {
				if !manifestFields[k] {
					unknown = append(unknown, k)
				}
			}
			sort.Strings(unknown)
			for _, k := range unknown {
				add(false, ManifestFile, "unknown field %q is ignored (a typo, or a field of a newer plugin API)", k)
			}
			if _, ok := raw["api"]; !ok {
				add(false, ManifestFile, `no "api": it is read as plugin API 1; write "api": %d to say which contract it was written for`, APIVersion)
			}
		}
	}
	if why := apiError(m.API); why != "" {
		add(true, ManifestFile, "%s", why)
	}
	if known != nil {
		have := map[string]bool{}
		for _, id := range known {
			have[id] = true
		}
		for _, r := range m.Requires {
			if !have[r] {
				add(true, ManifestFile, "requires %q, which is not a plugin here", r)
			}
		}
	}
	if m.Config != nil {
		names := make([]string, 0, len(m.Config.Properties))
		for name := range m.Config.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			f := m.Config.Properties[name]
			switch {
			case f.Type == "string" || f.Type == "number" || f.Type == "integer" || f.Type == "boolean":
			case len(f.Enum) > 0: // a choice, which the Plugins page shows whatever its type says
			default:
				add(true, ManifestFile, "setting %q has type %q; a setting is string, number, integer or boolean, or a choice with an enum list", name, f.Type)
			}
		}
	}
	if _, err := HookEntries(m, nil); err != nil {
		add(true, "hooks", "%v", err)
	}
	if _, err := MCPServers(m, nil); err != nil {
		add(true, "mcp", "%v", err)
	}
	for _, kind := range []string{"skills", "commands", "agents"} {
		ps = append(ps, checkDefinitions(dir, kind)...)
	}
	return m, ps
}

// manifestBytes reads the plugin's manifest as written, in either layout.
func manifestBytes(dir string) ([]byte, error) {
	b, err := readDoc(dir, ManifestFile)
	if errors.Is(err, os.ErrNotExist) {
		b, err = readDoc(dir, filepath.Join(".claude-plugin", ManifestFile))
	}
	return b, err
}

// checkDefinitions parses the front matter of a plugin's skills (skills/<name>/SKILL.md), commands or agents (<kind>/<name>.md).
func checkDefinitions(dir, kind string) []Problem {
	ents, err := os.ReadDir(filepath.Join(dir, kind))
	if err != nil {
		return nil
	}
	var ps []Problem
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		rel := filepath.Join(kind, name)
		if kind == "skills" {
			if !e.IsDir() {
				continue
			}
			rel = filepath.Join(rel, "SKILL.md")
		} else if e.IsDir() || !strings.HasSuffix(name, ".md") {
			continue
		}
		b, err := readDoc(dir, rel)
		if err != nil {
			ps = append(ps, Problem{Error: true, Where: filepath.ToSlash(rel), What: err.Error()})
			continue
		}
		doc, err := mdfile.Parse(mdfile.Normalize(b))
		desc, _ := doc.Meta.Lookup("description")
		switch {
		case err != nil:
			ps = append(ps, Problem{Error: true, Where: filepath.ToSlash(rel), What: err.Error()})
		case kind == "skills" && strings.TrimSpace(desc.Str) == "":
			ps = append(ps, Problem{Error: true, Where: filepath.ToSlash(rel), What: "a skill needs a description in its front matter: it is the line the agents choose it by"})
		}
	}
	return ps
}
