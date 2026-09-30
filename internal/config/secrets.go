package config

import (
	"regexp"
	"strconv"
	"strings"
)

// Configuration files get committed, pasted into issues and synced between
// machines. Secrets belong in the environment, so anything in a file that looks
// like a credential earns a warning (never the value itself).

var secretValuePatterns = []*regexp.Regexp{
	regexp.MustCompile(`^sk-[A-Za-z0-9_-]{8,}`),                   // OpenAI, Anthropic (sk-ant-...), OpenRouter (sk-or-...)
	regexp.MustCompile(`^(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{20,}`), // GitHub tokens
	regexp.MustCompile(`^github_pat_[A-Za-z0-9_]{20,}`),
	regexp.MustCompile(`^xox[abprs]-[A-Za-z0-9-]{10,}`),         // Slack
	regexp.MustCompile(`^AKIA[0-9A-Z]{16}$`),                    // AWS access key id
	regexp.MustCompile(`^AIza[0-9A-Za-z_-]{30,}`),               // Google API key
	regexp.MustCompile(`(?i)^bearer\s+[A-Za-z0-9._~+/=-]{20,}`), // Authorization header value
}

var (
	secretNameRE  = regexp.MustCompile(`(?i)(key|token|secret|passw(or)?d|credential|authorization)`)
	secretCharsRE = regexp.MustCompile(`^[A-Za-z0-9+/=_.-]+$`)
)

const secretAdvice = "looks like a secret. Keep credentials out of configuration files: put it in an environment variable and, for a provider, name that variable in api_key_env"

// scanSecrets walks a parsed document and warns about string values that look
// like credentials.
func scanSecrets(file string, src []byte, root *node) []Issue {
	c := &checker{file: file, src: src}
	var walk func(n *node, segs []string)
	walk = func(n *node, segs []string) {
		switch n.kind {
		case nObject:
			for _, k := range n.keys {
				walk(n.mem[k].val, cloneSegs(segs, k))
			}
		case nArray:
			for i, e := range n.arr {
				walk(e, cloneSegs(segs, "["+strconv.Itoa(i)+"]"))
			}
		case nString:
			if looksSecret(segs, n.str) {
				c.report(SeverityWarning, n.off, segs, "%s", secretAdvice)
			}
		}
	}
	walk(root, nil)
	return c.issues
}

// looksSecret applies the value patterns to any string, and the name-plus-shape
// heuristic to strings under keys named like key, token or secret.
func looksSecret(segs []string, v string) bool {
	for _, re := range secretValuePatterns {
		if re.MatchString(v) {
			return true
		}
	}
	name := ""
	for i := len(segs) - 1; i >= 0; i-- {
		if !isIndexSeg(segs[i]) {
			name = strings.ToLower(segs[i])
			break
		}
	}
	// api_key_env and friends hold the NAME of a variable, not a secret.
	if name == "" || strings.HasSuffix(name, "_env") || !secretNameRE.MatchString(name) {
		return false
	}
	if len(v) < 20 || !secretCharsRE.MatchString(v) {
		return false
	}
	hasDigit, hasLetter := false, false
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			hasLetter = true
		}
	}
	return hasDigit && hasLetter
}
