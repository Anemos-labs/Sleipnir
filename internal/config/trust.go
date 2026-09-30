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
	{"providers", "*", "base_url"},    // redirects requests, and the key that signs them
	{"providers", "*", "api_key_env"}, // chooses which environment variable is sent
	{"providers", "*", "headers"},     // can add credentials or route requests
	{"providers", "*", "options"},     // provider-specific behaviour, may hold URLs
	{"permissions", "mode"},           // "bypass" turns off every prompt
	{"permissions", "allow"},          // pre-approves actions
	{"permissions", "roles", "*", "mode"},
	{"permissions", "roles", "*", "allow"},
	{"hooks"},                      // runs commands
	{"mcp"},                        // starts servers
	{"tools", "web_allow_private"}, // reaches internal networks
	{"tools", "web_allow_hosts"},
	{"swarm", "budget_usd"}, // a repository could lift the built-in cap on what a swarm may spend
}

// userOnlyPaths are settings that only the user's own file may make. A project
// file that carries one has it dropped, whether or not the project is trusted:
// they are how the user tells the harness "this provider's key may go to that
// host", and a repository must not be able to say that on the user's behalf. (The
// sensitive paths above are what a trusted project may set; these it never can.)
var userOnlyPaths = [][]string{
	{"providers", "*", "allow_hosts"},
	{"providers", "*", "allow_insecure_http"},
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
			if isSensitive(p) {
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
