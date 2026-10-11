package export

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// DropRule removes steps from the training formats by what they are, whoever
// produced them and whether or not they are trainable. A rule matches a step when
// every condition it sets holds (AND); a list condition holds when any of its
// patterns matches (OR). Conditions left empty (or false) are not checked, and a
// rule must set at least one.
//
// Every pattern is a glob compared without regard to case: '*' matches any run of
// characters, '/' included ("*deepseek*" matches "deepseek/deepseek-v4.1-flash"),
// '?' matches one character, and every other character matches itself.
type DropRule struct {
	// Name labels the rule in the statistics: a dropped step counts as
	// "step:rule:<name>". Several rules may share a name (a preset expands into
	// more than one rule to express an OR across conditions).
	Name string `json:"name"`
	// Models matches the step's model (rl.Step.Model).
	Models []string `json:"models,omitempty"`
	// Roles matches the step's role or the role of the agent that made the call, so
	// "frontend" also matches the compactor and mailman calls of a frontend agent.
	Roles []string `json:"roles,omitempty"`
	// Tags matches the episode's task tags (rl.EnvRef.Tags): the rule holds when any
	// tag matches any pattern.
	Tags []string `json:"tags,omitempty"`
	// TaskIDs matches the episode's task id.
	TaskIDs []string `json:"task_ids,omitempty"`
	// EditsUI holds when the step's tool calls write or edit a user-interface file
	// (rl.IsUIPath). The calls that count are those of the harness's file tools,
	// by name without regard to case: write and edit (their "path" or "file_path"
	// argument) and apply_patch (every "*** Add File:", "*** Update File:",
	// "*** Delete File:" and "*** Move to:" path of its "patch" or "input" text).
	// Arguments that are not valid JSON (a cut-off call) are searched for a
	// "path" or "file_path" value. Shell commands are not inspected.
	EditsUI bool `json:"edits_ui,omitempty"`
	// Providers matches the upstream that served the call (rl.Step.Provider). A
	// step whose provider was not recorded matches no provider pattern but "*".
	Providers []string `json:"providers,omitempty"`
}

// PresetDSFrontend is the name of the built-in rule set that removes every step a
// deepseek model wrote while doing front-end or visual work.
const PresetDSFrontend = "ds-frontend"

// dsFrontendTags are the task tags that mark front-end work for the ds-frontend preset.
var dsFrontendTags = []string{"frontend", "ui", "design", "frontend_correct", "frontend_design"}

// Preset returns the rules of a built-in preset, or false when there is none of
// that name. ds-frontend drops a step whose model matches "*deepseek*" and that is
// front-end work in any of three ways, one rule each: its role (or its agent's) is
// frontend; its task is tagged frontend, ui, design, frontend_correct or
// frontend_design; or it writes or edits a UI file (EditsUI).
func Preset(name string) ([]DropRule, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case PresetDSFrontend:
		ds := []string{"*deepseek*"}
		return []DropRule{
			{Name: PresetDSFrontend, Models: ds, Roles: []string{"frontend"}},
			{Name: PresetDSFrontend, Models: ds, Tags: append([]string(nil), dsFrontendTags...)},
			{Name: PresetDSFrontend, Models: ds, EditsUI: true},
		}, true
	}
	return nil, false
}

// Presets lists the names of the built-in presets.
func Presets() []string { return []string{PresetDSFrontend} }

// ParseDropRule reads one rule as the command line spells it: a preset name
// ("ds-frontend"), or conditions separated by ';', each "key=value[,value...]":
//
//	name=no-ds-docs;models=*deepseek*;roles=docs
//	name=ui-edits;edits-ui;providers=ImpCC
//
// The keys are name (required), models, roles, tags, task-ids, providers and
// edits-ui (alone, or =true / =false). A preset may expand into several rules.
func ParseDropRule(spec string) ([]DropRule, error) {
	spec = strings.TrimSpace(spec)
	if rules, ok := Preset(spec); ok {
		return rules, nil
	}
	if !strings.Contains(spec, "=") && !strings.Contains(spec, ";") {
		return nil, fmt.Errorf("drop rule %q: not a preset (%s) and not a list of key=value conditions", spec, strings.Join(Presets(), ", "))
	}
	var r DropRule
	for _, part := range strings.Split(spec, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		key, val, hasVal := strings.Cut(part, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		list := splitPatterns(val)
		switch key {
		case "name":
			r.Name = strings.TrimSpace(val)
		case "models", "model":
			r.Models = append(r.Models, list...)
		case "roles", "role":
			r.Roles = append(r.Roles, list...)
		case "tags", "tag":
			r.Tags = append(r.Tags, list...)
		case "task-ids", "task-id", "task_ids", "tasks":
			r.TaskIDs = append(r.TaskIDs, list...)
		case "providers", "provider":
			r.Providers = append(r.Providers, list...)
		case "edits-ui", "edits_ui":
			switch v := strings.ToLower(strings.TrimSpace(val)); {
			case !hasVal || v == "true":
				r.EditsUI = true
			case v == "false":
				r.EditsUI = false
			default:
				return nil, fmt.Errorf("drop rule %q: edits-ui takes no value, or true or false, not %q", spec, val)
			}
			continue
		default:
			return nil, fmt.Errorf("drop rule %q: unknown key %q (want name, models, roles, tags, task-ids, providers or edits-ui)", spec, key)
		}
		if key != "name" && len(list) == 0 {
			return nil, fmt.Errorf("drop rule %q: %s needs a value", spec, key)
		}
	}
	if err := r.validate(); err != nil {
		return nil, err
	}
	return []DropRule{r}, nil
}

// splitPatterns splits a comma-separated list, trimming blanks and dropping empty entries.
func splitPatterns(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// validate refuses a rule that has no name or no condition: an unnamed rule could
// not be told apart in the statistics, and a rule without conditions would drop
// every step.
func (r DropRule) validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("drop rule %+v has no name", r)
	}
	if len(r.Models)+len(r.Roles)+len(r.Tags)+len(r.TaskIDs)+len(r.Providers) == 0 && !r.EditsUI {
		return fmt.Errorf("drop rule %q sets no condition and would drop every step", r.Name)
	}
	return nil
}

// Match reports whether the rule matches a step of an episode. ag is the agent the
// step belongs to; nil checks Roles against the step's own role only.
func (r DropRule) Match(ep *rl.Episode, ag *rl.Agent, st *rl.Step) bool {
	if len(r.Models) > 0 && !anyGlob(r.Models, st.Model) {
		return false
	}
	if len(r.Roles) > 0 && !anyGlob(r.Roles, st.Role) && (ag == nil || !anyGlob(r.Roles, ag.Role)) {
		return false
	}
	if len(r.Tags) > 0 {
		hit := false
		for _, t := range ep.Env.Tags {
			if anyGlob(r.Tags, t) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	if len(r.TaskIDs) > 0 && !anyGlob(r.TaskIDs, ep.TaskID) {
		return false
	}
	if len(r.Providers) > 0 && !anyGlob(r.Providers, st.Provider) {
		return false
	}
	if r.EditsUI && !editsUI(st) {
		return false
	}
	return true
}

// editsUI reports whether any path the step writes, edits or patches is UI work.
func editsUI(st *rl.Step) bool {
	for _, p := range EditedPaths(st) {
		if rl.IsUIPath(p) {
			return true
		}
	}
	return false
}

// fileTools are the tool names, lower-cased, whose calls write or edit files.
var fileTools = map[string]bool{"write": true, "edit": true, "apply_patch": true}

// patchHeaders are the apply_patch lines that name a file the patch changes.
var patchHeaders = []string{"*** Add File:", "*** Update File:", "*** Delete File:", "*** Move to:"}

// rawPathRE finds a "path" or "file_path" string value in arguments that are not valid JSON.
var rawPathRE = regexp.MustCompile(`"(?:file_)?path"\s*:\s*"((?:[^"\\]|\\.)*)"`)

// EditedPaths lists, sorted and without duplicates, the paths a step's tool calls
// write, edit or patch: the tool_use blocks of its completion and the inputs of its
// recorded observations, for the tools DropRule.EditsUI documents.
func EditedPaths(st *rl.Step) []string {
	seen := map[string]bool{}
	add := func(name string, input json.RawMessage) {
		if !fileTools[strings.ToLower(name)] {
			return
		}
		for _, p := range pathsOf(strings.ToLower(name), input) {
			if p = strings.TrimSpace(p); p != "" {
				seen[p] = true
			}
		}
	}
	for _, b := range st.Completion.Turn.ToolCalls() {
		add(b.ToolName, b.Input)
	}
	for _, o := range st.Observations {
		add(o.Name, o.Input)
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// pathsOf extracts the file paths named by one file-tool call's arguments.
func pathsOf(tool string, input json.RawMessage) []string {
	var args map[string]json.RawMessage
	if err := json.Unmarshal(input, &args); err != nil {
		// A cut-off or malformed call is recorded as a JSON string of its raw text.
		raw := string(input)
		var s string
		if json.Unmarshal(input, &s) == nil {
			raw = s
		}
		if json.Unmarshal([]byte(raw), &args) != nil {
			var out []string
			for _, m := range rawPathRE.FindAllStringSubmatch(raw, -1) {
				var p string
				if json.Unmarshal([]byte(`"`+m[1]+`"`), &p) == nil {
					out = append(out, p)
				}
			}
			if tool == "apply_patch" {
				out = append(out, patchPaths(raw)...)
			}
			return out
		}
	}
	str := func(k string) string {
		var s string
		_ = json.Unmarshal(args[k], &s)
		return s
	}
	if tool == "apply_patch" {
		text := str("patch")
		if text == "" {
			text = str("input")
		}
		return patchPaths(text)
	}
	return []string{str("path"), str("file_path")}
}

// patchPaths lists the paths named by the file headers of apply_patch text.
func patchPaths(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		for _, h := range patchHeaders {
			if strings.HasPrefix(line, h) {
				out = append(out, strings.TrimSpace(strings.TrimPrefix(line, h)))
			}
		}
	}
	return out
}

// anyGlob reports whether s matches any of the patterns (see globMatch).
func anyGlob(patterns []string, s string) bool {
	for _, p := range patterns {
		if globMatch(p, s) {
			return true
		}
	}
	return false
}

// globMatch reports whether s matches pattern, ignoring case: '*' matches any run
// of characters ('/' included), '?' exactly one, every other character itself.
func globMatch(pattern, s string) bool {
	p, t := []rune(strings.ToLower(pattern)), []rune(strings.ToLower(s))
	pi, ti := 0, 0
	star, mark := -1, 0
	for ti < len(t) {
		switch {
		case pi < len(p) && (p[pi] == '?' || p[pi] == t[ti]):
			pi++
			ti++
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ti
			pi++
		case star >= 0:
			pi = star + 1
			mark++
			ti = mark
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// roleTeacherOK reports whether a step's model is allowed by every --role-teacher
// constraint that applies to it: the one for the step's role and the one for its
// agent's role (so a constraint on "frontend" also covers the compactor and
// mailman calls a frontend agent makes).
func (x *exporter) roleTeacherOK(c stepCtx) bool {
	if len(x.o.RoleTeachers) == 0 {
		return true
	}
	roles := []string{c.st.Role}
	if c.ag != nil && c.ag.Role != c.st.Role {
		roles = append(roles, c.ag.Role)
	}
	for _, role := range roles {
		for r, models := range x.o.RoleTeachers {
			if strings.EqualFold(r, role) && !anyGlob(models, c.st.Model) {
				return false
			}
		}
	}
	return true
}

// ruleMatch returns the name of the first drop rule matching the step, or "".
func (x *exporter) ruleMatch(c stepCtx) string {
	for _, r := range x.o.Drop {
		if r.Match(c.we.ep, c.ag, c.st) {
			return r.Name
		}
	}
	return ""
}
