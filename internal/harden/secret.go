package harden

import "regexp"

// What counts as a credential. The heuristics are shared: the shell tool withholds
// such variables from the commands a model runs, and Process erases them from the
// environment block /proc/<pid>/environ serves.
//
// Scrubbing is a heuristic and is layered so that each layer catches what the
// previous one cannot: names first (the obvious ones), then values (a
// DATABASE_URL that embeds a password has no telling name), then the one
// non-secret variable that is a capability all the same. It is not a boundary.

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

// LooksSecret reports whether an environment variable holds a credential by its
// name or its value, and so should be withheld from commands a model runs.
func LooksSecret(name, value string) bool {
	if secretName.MatchString(name) {
		return true
	}
	return !proxyName.MatchString(name) && secretValue.MatchString(value)
}

// eraseName is the wider net for erasing values from the environment block in
// memory. Erasing changes nothing the harness or its children see (os.Getenv
// reads the runtime's own copy), so a false positive costs only a blank in
// /proc/<pid>/environ, and the pattern can afford to catch every name that could
// carry a credential, including the ones LooksSecret leaves to commands on
// purpose (AWS_ACCESS_KEY_ID is half of a credential pair).
var eraseName = regexp.MustCompile(`(?i)(key|token|secret|passw|credential|auth|cookie|bearer|dsn|signature|(^|[_-])pat$|[_-]pwd$)`)

// shouldErase reports whether the value of an initial-environment entry is
// erased from memory.
func shouldErase(name, value string) bool {
	return eraseName.MatchString(name) || LooksSecret(name, value)
}
