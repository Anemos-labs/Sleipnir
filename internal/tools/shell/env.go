package shell

import (
	"path"
	"regexp"
	"strings"
)

// Commands run by a model are prompt-injectable (a fetched web page or a repo
// file can tell the agent to `env | curl ...`), so credentials that the harness
// process itself holds (provider keys above all) are not handed to child
// processes unless the operator lists them in Options.PassEnv.
//
// Scrubbing is a heuristic and is layered so that each layer catches what the
// previous one cannot: names first (the obvious ones), then values (a
// DATABASE_URL that embeds a password has no telling name), then the one
// non-secret variable that is a capability all the same.
//
// It is not a boundary. A same-user process can read the harness's own
// /proc/<pid>/environ; HardenProcess closes that.

// secretName matches variable names that very likely hold credentials.
var secretName = regexp.MustCompile(`(?i)(` +
	`api[_-]?key|secret|token|password|passwd|credential|private[_-]?key` + // anywhere in the name
	`|(^|[_-])(key|pass|passphrase|pat|dsn|auth|authorization|cookies?)$` + // as the last word
	`|[_-]pwd$` + // MYSQL_PWD, but not PWD itself, which is the working directory
	`|^ssh_auth_sock$` + // not a secret itself, but lets any command use the developer's SSH identities
	`)`)

// secretValue matches values that are credentials whatever the variable is
// called: URLs with a password in the userinfo, PEM private keys, and the
// well-known token formats (OpenAI/Anthropic-style sk-, Stripe, GitHub, Slack,
// AWS access key ids, Google API keys, JWTs).
var secretValue = regexp.MustCompile(`(?i)(` +
	`^[a-z][a-z0-9+.-]*://[^/\s:@]*:[^/\s@]+@` +
	`|-----BEGIN [A-Z ]*` + `PRIVATE KEY-----` +
	`|^sk-[a-z0-9_-]{20,}$|^sk_(live|test)_[a-z0-9]{10,}$` +
	`|^gh[pousr]_[a-z0-9]{30,}$|^github_pat_[a-z0-9_]{30,}$` +
	`|^xox[abprs]-[a-z0-9-]{10,}$` +
	`|^akia[0-9a-z]{16}$|^aiza[0-9a-z_-]{35}$` +
	`|^eyj[a-z0-9_-]{10,}\.eyj[a-z0-9_-]{10,}\.[a-z0-9_-]*$` +
	`)`)

// proxyName marks proxy settings, which routinely carry credentials in a URL
// and which commands need to reach the network at all in sandboxes that force a
// proxy; they are exempt from the value check (not from the name check).
var proxyName = regexp.MustCompile(`(?i)^(https?|all|ftp|no)_proxy$`)

// looksSecret reports whether a variable should be withheld from commands.
func looksSecret(name, value string) bool {
	if secretName.MatchString(name) {
		return true
	}
	return !proxyName.MatchString(name) && secretValue.MatchString(value)
}

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

func dropNUL(r rune) rune {
	if r == 0 {
		return -1
	}
	return r
}
