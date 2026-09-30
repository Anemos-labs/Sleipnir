package shell

import (
	"path"
	"regexp"
	"strings"
)

// secretName matches environment variable names that very likely hold
// credentials. Commands run by a model are prompt-injectable (a fetched web
// page or a repo file can tell the agent to `env | curl ...`), so credentials
// that the harness process itself needs (provider keys above all) are not
// handed to child processes unless the operator lists them in Options.PassEnv.
var secretName = regexp.MustCompile(`(?i)(api[_-]?key|secret|token|password|passwd|credential)`)

// commandEnv builds the environment of a shell command from base ("K=V"
// entries): secret-looking variables are dropped unless allowed, then the
// variables that keep commands non-interactive and attributable are forced.
func commandEnv(base []string, agent string, passEnv []string) []string {
	forced := []string{
		"TERM=dumb",
		"NO_COLOR=1",
		"GIT_TERMINAL_PROMPT=0",
		"PAGER=cat",
		"GIT_PAGER=cat",
		"SLEIPNIR_AGENT=" + strings.Map(dropNUL, agent),
	}
	override := make(map[string]struct{}, len(forced))
	for _, kv := range forced {
		name, _, _ := strings.Cut(kv, "=")
		override[strings.ToUpper(name)] = struct{}{}
	}

	out := make([]string, 0, len(base)+len(forced))
	for _, kv := range base {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			// Windows keeps entries such as "=C:=C:\dir"; pass them through.
			out = append(out, kv)
			continue
		}
		if _, forcedName := override[strings.ToUpper(name)]; forcedName {
			continue
		}
		if secretName.MatchString(name) && !passAllowed(name, passEnv) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, forced...)
}

// passAllowed reports whether name is exempted from scrubbing. Entries are
// case-insensitive names and may use path.Match wildcards ("GITHUB_*").
func passAllowed(name string, pass []string) bool {
	for _, p := range pass {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.EqualFold(p, name) {
			return true
		}
		if ok, err := path.Match(strings.ToUpper(p), strings.ToUpper(name)); err == nil && ok {
			return true
		}
	}
	return false
}

func dropNUL(r rune) rune {
	if r == 0 {
		return -1
	}
	return r
}
