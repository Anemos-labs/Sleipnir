package shell

import (
	"path"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/harden"
)

// Commands run by a model are prompt-injectable (a fetched web page or a repo
// file can tell the agent to `env | curl ...`), so credentials that the harness
// process itself holds (provider keys above all) are not handed to child
// processes unless the operator lists them in Options.PassEnv.
//
// Scrubbing is a heuristic (harden.LooksSecret: names first, then values, then
// the one non-secret variable that is a capability all the same) and it is not a
// boundary. A same-user process can read the harness's own /proc/<pid>/environ;
// harden.Process, called first thing in main, closes that, and docs/SECURITY.md
// says what is and is not covered.

// looksSecret reports whether a variable should be withheld from commands.
func looksSecret(name, value string) bool { return harden.LooksSecret(name, value) }

// commandEnv builds the environment of a shell command from base ("K=V"
// entries): secret-looking variables are dropped unless allowed, then the
// variables that keep commands non-interactive and attributable are forced.
// PWD is set to dir (when known) because exec.Cmd only does that for an
// implicit environment, and a stale inherited PWD misleads programs that trust
// it instead of asking the kernel.
func commandEnv(base []string, agent, dir string, passEnv []string) []string {
	forced := []string{
		"TERM=dumb",
		"NO_COLOR=1",
		"GIT_TERMINAL_PROMPT=0",
		"PAGER=cat",
		"GIT_PAGER=cat",
		"SLEIPNIR_AGENT=" + strings.Map(dropNUL, agent),
	}
	if dir != "" {
		forced = append(forced, "PWD="+strings.Map(dropNUL, dir))
	}
	override := make(map[string]struct{}, len(forced))
	for _, kv := range forced {
		name, _, _ := strings.Cut(kv, "=")
		override[strings.ToUpper(name)] = struct{}{}
	}

	out := make([]string, 0, len(base)+len(forced))
	for _, kv := range base {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" {
			// Windows keeps entries such as "=C:=C:\dir"; pass them through.
			out = append(out, kv)
			continue
		}
		if _, forcedName := override[strings.ToUpper(name)]; forcedName {
			continue
		}
		if looksSecret(name, value) && !passAllowed(name, passEnv) {
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

// dropNUL removes NUL runes when used with strings.Map and preserves other runes.
func dropNUL(r rune) rune {
	if r == 0 {
		return -1
	}
	return r
}
