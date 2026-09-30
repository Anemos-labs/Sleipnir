package config

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Environment variables. Each SLEIPNIR_* variable that names a setting is one
// layer entry, above every file and below explicit overrides.
//
//	SLEIPNIR_MODEL              models.default
//	SLEIPNIR_COMPACTOR_MODEL    models.compactor
//	SLEIPNIR_MODEL_<ROLE>       models.roles.<role>       (role lower-cased)
//	SLEIPNIR_PERMISSION_MODE    permissions.mode
//	SLEIPNIR_<SECTION>_<FIELD>  any scalar or list field of a section, named by
//	                            its JSON path in upper case: SLEIPNIR_CACHE_SHARED_TTL,
//	                            SLEIPNIR_SWARM_MAX_AGENTS, SLEIPNIR_TOOLS_WEB_ALLOW_HOSTS, ...
//
// Booleans accept 1/true/yes/on and 0/false/no/off; lists are comma-separated;
// an empty value is treated as unset. Other SLEIPNIR_* variables are ignored
// (the harness has its own uses for some), and providers, hooks and MCP servers
// are not configurable from the environment: use a file.

// EnvVar describes one environment variable Load understands.
type EnvVar struct {
	Name string // the variable; "SLEIPNIR_MODEL_<ROLE>" is a pattern
	Path string // the setting it sets, as a dotted path
	Type string // "string", "bool", "integer", "number" or "list"
}

type envBinding struct {
	name string
	segs []string
	typ  reflect.Type
}

var (
	stringType = reflect.TypeOf("")
	listType   = reflect.TypeOf([]string(nil))
)

// envSupported reports whether a field type can be set from a variable.
func envSupported(t reflect.Type) bool {
	switch {
	case t.Kind() == reflect.String, t.Kind() == reflect.Bool,
		t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64,
		t.Kind() == reflect.Float32, t.Kind() == reflect.Float64:
		return true
	}
	return t == listType
}

func envType(t reflect.Type) string {
	switch {
	case t.Kind() == reflect.String:
		return "string"
	case t.Kind() == reflect.Bool:
		return "bool"
	case t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64:
		return "integer"
	case t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64:
		return "number"
	}
	return "list"
}

// envBindings derives the variable table from the Config type, so a new field
// is settable from the environment without touching this file.
func envBindings() []envBinding {
	var out []envBinding
	for _, sec := range structFields(reflect.TypeOf(Config{})) {
		if sec.typ.Kind() != reflect.Struct {
			continue
		}
		for _, f := range structFields(sec.typ) {
			if envSupported(f.typ) {
				out = append(out, envBinding{
					name: "SLEIPNIR_" + strings.ToUpper(sec.name) + "_" + strings.ToUpper(f.name),
					segs: []string{sec.name, f.name},
					typ:  f.typ,
				})
			}
		}
	}
	out = append(out,
		envBinding{"SLEIPNIR_MODEL", []string{"models", "default"}, stringType},
		envBinding{"SLEIPNIR_COMPACTOR_MODEL", []string{"models", "compactor"}, stringType},
		envBinding{"SLEIPNIR_PERMISSION_MODE", []string{"permissions", "mode"}, stringType},
	)
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

// EnvVars lists the environment variables Load understands.
func EnvVars() []EnvVar {
	var out []EnvVar
	for _, b := range envBindings() {
		out = append(out, EnvVar{Name: b.name, Path: strings.Join(b.segs, "."), Type: envType(b.typ)})
	}
	out = append(out, EnvVar{Name: envRolePrefix + "<ROLE>", Path: "models.roles.<role>", Type: "string"})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

const envRolePrefix = "SLEIPNIR_MODEL_"

// envLayers turns environment entries (KEY=VALUE) into one layer per variable,
// in name order so results do not depend on the environment's order.
func envLayers(environ []string) ([]*layer, []Issue) {
	vals := map[string]string{}
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && strings.HasPrefix(k, "SLEIPNIR_") {
			vals[k] = v
		}
	}
	var layers []*layer
	var issues []Issue
	bad := func(name string, segs []string, format string, args ...any) {
		issues = append(issues, Issue{Severity: SeverityError, Source: "env:" + name, Path: fmtPath(segs), Message: fmt.Sprintf(format, args...), segs: segs})
	}
	add := func(name string, segs []string, v any) {
		tree := map[string]any{}
		cur := tree
		for _, s := range segs[:len(segs)-1] {
			next := map[string]any{}
			cur[s] = next
			cur = next
		}
		cur[segs[len(segs)-1]] = v
		layers = append(layers, &layer{kind: "env", source: "env:" + name, found: true, tree: tree})
	}

	known := map[string]bool{}
	for _, b := range envBindings() {
		known[b.name] = true
		raw, ok := vals[b.name]
		if !ok || strings.TrimSpace(raw) == "" {
			continue
		}
		v, err := parseEnvValue(strings.TrimSpace(raw), b.typ)
		if err != nil {
			bad(b.name, b.segs, "%v", err)
			continue
		}
		add(b.name, b.segs, v)
	}
	for _, name := range sortedKeys(vals) {
		if known[name] || !strings.HasPrefix(name, envRolePrefix) || strings.TrimSpace(vals[name]) == "" {
			continue
		}
		role := strings.ToLower(strings.TrimPrefix(name, envRolePrefix))
		if role == "" {
			continue
		}
		add(name, []string{"models", "roles", role}, strings.TrimSpace(vals[name]))
	}
	sort.SliceStable(layers, func(i, j int) bool { return layers[i].source < layers[j].source })
	return layers, issues
}

func parseEnvValue(raw string, t reflect.Type) (any, error) {
	show := raw
	if len(show) > 40 {
		show = show[:40] + "..."
	}
	switch {
	case t.Kind() == reflect.String:
		return raw, nil
	case t.Kind() == reflect.Bool:
		switch strings.ToLower(raw) {
		case "1", "true", "yes", "on":
			return true, nil
		case "0", "false", "no", "off":
			return false, nil
		}
		return nil, fmt.Errorf("expected true or false (or 1/0, yes/no, on/off), got %q", show)
	case t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64:
		if _, err := strconv.ParseInt(raw, 10, t.Bits()); err != nil {
			return nil, fmt.Errorf("expected an integer, got %q", show)
		}
		return json.Number(raw), nil
	case t.Kind() == reflect.Float32 || t.Kind() == reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("expected a number, got %q", show)
		}
		return json.Number(strconv.FormatFloat(f, 'g', -1, 64)), nil
	}
	items := []any{}
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			items = append(items, part)
		}
	}
	return items, nil
}
