package config

import "strings"

// Project files are part of a repository, and repositories can be someone
// else's. A few settings are dangerous in an untrusted file: they can send API
// keys to a different host, run commands, or widen what the agent may do
// without asking. Load reports every such setting a project-level file makes
// (Report.ProjectRisks) so the caller can ask the user whether to trust the
// project, and with LoadOpts.UntrustedProject drops them.

// sensitivePaths are the field paths ("*" matches any one segment) treated as
// dangerous when they come from a project-level file.
var sensitivePaths = [][]string{
	{"providers", "*", "base_url"},           // redirects requests, and the key that signs them
	{"providers", "*", "api_key_env"},        // chooses which environment variable is sent
	{"providers", "*", "headers"},            // can add credentials or route requests
	{"providers", "*", "options"},            // provider-specific behaviour, may hold URLs
	{"providers", "*", "auth"},               // chooses which credential is sent (a ChatGPT sign-in)
	{"providers", "*", "dialect"},            // chooses how, and so which credential store, a request is signed
	{"permissions", "mode"},                  // "bypass" and "yolo" turn off the prompts
	{"permissions", "allow"},                 // pre-approves actions
	{"permissions", "manager_approves"},      // the older spelling of plugins.enabled.manager-approves
	{"permissions", "manager_answers_after"}, // lets the manager answer questions the person was meant to
	{"permissions", "manager_answers_up_to"},
	{"permissions", "roles", "*", "mode"},
	{"permissions", "roles", "*", "allow"},
	{"hooks"},                      // runs commands
	{"mcp"},                        // starts servers
	{"tools", "web_allow_private"}, // reaches internal networks
	{"tools", "web_allow_hosts"},
	{"swarm", "budget_usd"},      // a repository could lift the built-in cap on what a swarm may spend
	{"swarm", "push_progress"},   // the older spelling of plugins.enabled.push-progress: pushes with the user's credentials
	{"plugins", "enabled", "*"},  // turning a plugin on (an installed one runs code); turning one off is not risky (harmlessValue)
	{"plugins", "settings", "*"}, // a plugin's settings are written into the commands of its hooks and servers
	{"plugins", "profile"},       // a profile turns plugins off, the user's hooks among them (harmlessValue)
}

// hooksPlugin is the plugin that runs the user's hooks: a repository that turns it off turns off the user's own guards.
const hooksPlugin = "hooks"

// harmlessProfiles are the profiles that leave the hooks on (plugin.Profiles): only these may a project file choose untrusted.
var harmlessProfiles = map[string]bool{"standard": true, "team": true, "headless": true}

// KeepEnabledAgainstProject reports whether a project file needs trust to turn plugin id off. Without it, only the plugin
// that runs the user's hooks is kept; the plugin package, which knows the plugins, also keeps every external one, whose
// hooks may be the user's guards.
var KeepEnabledAgainstProject = func(id string) bool { return id == hooksPlugin }

// harmlessValue reports whether the value v at a sensitive path segs is in fact
// harmless: a project file may turn a plugin off, except one that may hold the
// user's guards (KeepEnabledAgainstProject), and may turn off the manager's
// approvals and the pushing of progress.
func harmlessValue(segs []string, v any) bool {
	switch {
	case len(segs) == 3 && segs[0] == "plugins" && segs[1] == "enabled":
		b, ok := v.(bool)
		return ok && !b && !KeepEnabledAgainstProject(segs[2])
	case len(segs) == 2 && (segs[0] == "permissions" && segs[1] == "manager_approves" || segs[0] == "swarm" && segs[1] == "push_progress"):
		b, ok := v.(bool)
		return ok && !b
	case len(segs) == 2 && segs[0] == "plugins" && segs[1] == "profile":
		p, ok := v.(string)
		return ok && (p == "" || harmlessProfiles[p])
	}
	return false
}

// userOnlyPaths are settings that only the user's own file may make. A project
// file that carries one has it dropped, whether or not the project is trusted:
// they are how the user tells the harness "this provider's key may go to that
// host" and "this much of my machine is the agents'", and a repository must not be able to say that on the user's behalf. (The
// sensitive paths above are what a trusted project may set; these it never can.)
var userOnlyPaths = [][]string{
	{"providers", "*", "allow_hosts"},
	{"providers", "*", "allow_insecure_http"},
	{"resources", "cpus"},
	{"resources", "heavy_slots"},
	{"resources", "memory_reserve_mb"},
	{"resources", "priority"},
	{"resources", "unlimited"},
}

// matchPath reports whether segs is exactly one of the patterns ("*" matches any
// one segment).
func matchPath(patterns [][]string, segs []string) bool {
	for _, pat := range patterns {
		if len(pat) != len(segs) {
			continue
		}
		match := true
		for i := range pat {
			if pat[i] != "*" && pat[i] != segs[i] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// isSensitive reports whether segs is exactly one of the sensitive paths.
func isSensitive(segs []string) bool { return matchPath(sensitivePaths, segs) }

// holdsSensitive reports whether a sensitive path lies strictly under segs.
func holdsSensitive(segs []string) bool {
	for _, pat := range sensitivePaths {
		if len(pat) <= len(segs) {
			continue
		}
		if matchPath([][]string{pat[:len(segs)]}, segs) {
			return true
		}
	}
	return false
}

// UserOnlyPaths lists the settings Load honours only in the user-level file, as
// dotted paths with "*" for any name (documentation and tests).
func UserOnlyPaths() []string {
	out := make([]string, len(userOnlyPaths))
	for i, p := range userOnlyPaths {
		out[i] = strings.Join(p, ".")
	}
	return out
}

// dropUserOnly removes the user-only settings from a project-level layer and
// reports each one. It is applied to every project-level layer, trusted or not.
func (l *layer) dropUserOnly() []Issue {
	var risks []Issue
	var walk func(m map[string]any, segs []string)
	walk = func(m map[string]any, segs []string) {
		for _, k := range sortedKeys(m) {
			p := cloneSegs(segs, k)
			if matchPath(userOnlyPaths, p) {
				is := Issue{Severity: SeverityWarning, Source: l.source, Path: fmtPath(p), segs: p}
				is.Message = "can only be set in your user configuration file (~/.sleipnir/config.json); a project file cannot grant it, trusted or not, so it was ignored"
				l.position(&is)
				risks = append(risks, is)
				delete(m, k)
				continue
			}
			if sub, ok := m[k].(map[string]any); ok {
				walk(sub, p)
			}
		}
	}
	walk(l.tree, nil)
	return risks
}

// SensitivePaths lists the settings Load treats as risky in project files, as
// dotted paths with "*" for any name (documentation and tests).
func SensitivePaths() []string {
	out := make([]string, len(sensitivePaths))
	for i, p := range sensitivePaths {
		out[i] = strings.Join(p, ".")
	}
	return out
}

// collectRisks finds the sensitive settings in a layer's tree. With drop set it
// also removes them from the tree, so they never take effect.
func (l *layer) collectRisks(drop bool) []Issue {
	var risks []Issue
	var walk func(m map[string]any, segs []string)
	walk = func(m map[string]any, segs []string) {
		for _, k := range sortedKeys(m) {
			p := cloneSegs(segs, k)
			_, object := m[k].(map[string]any)
			// null, or any value that is not an object, in place of a section that holds a sensitive setting replaces the
			// whole section, the user's choices in it included (swarm: null restores the default budget).
			if isSensitive(p) && !harmlessValue(p, m[k]) || !object && holdsSensitive(p) {
				is := Issue{Severity: SeverityWarning, Source: l.source, Path: fmtPath(p), segs: p}
				is.Message = "is security-sensitive and comes from a project file; project files can be untrusted"
				if drop {
					is.Message = "is security-sensitive and comes from a project file that is not trusted; it was ignored"
				}
				l.position(&is)
				risks = append(risks, is)
				if drop {
					delete(m, k)
				}
				continue
			}
			if sub, ok := m[k].(map[string]any); ok {
				walk(sub, p)
			}
		}
	}
	walk(l.tree, nil)
	if drop {
		// Nothing about a provider is a repository's to say: not its URL or headers
		// (dropped above), and not the rest of an entry either (a dialect, a model
		// table, or an empty entry that changes which provider counts as configured).
		delete(l.tree, "providers")
	}
	return risks
}
