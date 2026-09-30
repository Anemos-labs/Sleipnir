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
	{"training"},     // exports conversations
	{"ui", "editor"}, // runs a command
}

// isSensitive reports whether segs is exactly one of the sensitive paths.
func isSensitive(segs []string) bool {
	for _, pat := range sensitivePaths {
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
	return risks
}
