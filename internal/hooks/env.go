package hooks

import (
	"os"
	"path"
	"regexp"
	"strings"
)

// secretName matches environment variable names that very likely hold
// credentials. A hook is a program somebody wrote for their own machine; it is
// not entitled to the harness's provider keys, and a repository's hook (from a
// trusted project, but still someone else's code) least of all.
//
// The pattern is wider than the shell tool's: besides *_API_KEY, *_SECRET,
// *_TOKEN and *_PASSWORD it catches key ids, signing keys, PATs, DSNs, cookies,
// authorization headers, the SSH agent socket, database and broker URLs and
// connection strings, which the review of this harness found leaking through
// the narrower list. Name matching can never be complete, so Runner.DenyEnv adds
// the exact names the caller knows (the providers' key variables).
var secretName = regexp.MustCompile(`(?i)(` +
	`api[_-]?key|secret|token|passw(or)?d|credential|private[_-]?key|access[_-]?key|signing[_-]?key|` +
	`connection[_-]?string|bearer|cookie|authorization|` +
	`(^|_)(pat|dsn|auth)($|_)|_pwd$|_key$|^key$|` +
	`(^|_)(database|db|redis|mongo|mongodb|amqp|broker)_(url|uri)$` +
	`)`)

// urlCredentials matches a URL with a user and password ("postgres://u:p@host"),
// which makes any variable holding one a secret whatever it is called.
var urlCredentials = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^/\s:@]+:[^/\s@]+@`)

// scrubEnv returns base ("K=V" entries) without secret-looking variables. pass
// names variables exempt from the name pattern (path.Match wildcards, case
// insensitive); deny names further variables to drop. Entries are also dropped
// when they are not "K=V", when the name is empty or contains NUL, and when the
// value has a URL with embedded credentials.
func scrubEnv(base, pass, deny []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" || strings.IndexByte(kv, 0) >= 0 {
			continue
		}
		if listed(name, deny) {
			continue
		}
		if !listed(name, pass) && (secretName.MatchString(name) || urlCredentials.MatchString(value)) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// listed reports whether name matches an entry of list: case-insensitively,
// with path.Match wildcards.
func listed(name string, list []string) bool {
	for _, p := range list {
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

// hookEnv builds the environment of one hook process: the scrubbed base, minus
// anything the harness sets itself, plus the variables that describe the event.
func (r *Runner) hookEnv(event string, ev Event) []string {
	base := r.Env
	if base == nil {
		base = os.Environ()
	}
	env := scrubEnv(base, r.PassEnv, r.DenyEnv)

	forced := []string{
		"SLEIPNIR_PROJECT_DIR=" + noNUL(r.Dir),
		"CLAUDE_PROJECT_DIR=" + noNUL(r.Dir),
		"SLEIPNIR_HOOK_EVENT=" + event,
		"NO_COLOR=1",
		"GIT_TERMINAL_PROMPT=0",
	}
	sid := ev.SessionID
	if sid == "" {
		sid = r.SessionID
	}
	for _, kv := range [][2]string{{"SLEIPNIR_SESSION_ID", sid}, {"SLEIPNIR_AGENT", ev.Agent}, {"SLEIPNIR_ROLE", ev.Role}} {
		if kv[1] != "" {
			forced = append(forced, kv[0]+"="+noNUL(kv[1]))
		}
	}
	override := make(map[string]bool, len(forced))
	for _, kv := range forced {
		name, _, _ := strings.Cut(kv, "=")
		override[strings.ToUpper(name)] = true
	}
	// Names the harness owns are dropped from the inherited environment so that a
	// stale value can never win: SLEIPNIR_AGENT belongs to this event, not to
	// whichever agent started the process.
	kept := env[:0]
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if override[strings.ToUpper(name)] || strings.EqualFold(name, "SLEIPNIR_AGENT") ||
			strings.EqualFold(name, "SLEIPNIR_ROLE") || strings.EqualFold(name, "SLEIPNIR_SESSION_ID") {
			continue
		}
		kept = append(kept, kv)
	}
	return append(kept, forced...)
}

func noNUL(s string) string { return strings.ReplaceAll(s, "\x00", "") }
