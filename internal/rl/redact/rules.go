package redact

import (
	"math"
	"net/netip"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// detector finds spans to replace. s is the original text and work is the same
// text with existing redaction tokens blanked out (byte for byte, so offsets
// agree); detectors match on work and read secrets from s.
type detector func(r *Redactor, s, work string) []span

// detectors is written once at init and only read afterwards.
var detectors = map[string]detector{
	KindPrivateKey:  detectPrivateKey,
	KindJWT:         regexKind(jwtRE, "eyJ"),
	KindAWS:         regexKind(awsRE, "AKIA", "ASIA", "AROA", "AIDA", "AGPA", "ANPA", "ANVA", "AIPA"),
	KindGitHub:      regexKind(githubRE, "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_"),
	KindGitLab:      regexKind(gitlabRE, "glpat-"),
	KindSlack:       regexKind(slackRE, "xox", "xapp-", "hooks.slack.com"),
	KindGoogle:      regexKind(googleRE, "AIza"),
	KindStripe:      regexKind(stripeRE, "sk_", "rk_"),
	KindNPM:         regexKind(npmRE, "npm_"),
	KindPyPI:        regexKind(pypiRE, "pypi-"),
	KindLLM:         detectLLM,
	KindHuggingFace: regexKind(hfRE, "hf_"),
	KindURLCred:     detectURLCred,
	KindBearer:      detectBearer,
	KindSecret:      detectKV,
	KindEntropy:     detectEntropy,
	KindEmail:       detectEmail,
	KindIPv4:        detectIPv4,
	KindIPv6:        detectIPv6,
	KindPath:        detectPath,
}

// regexKind builds a detector for a self-validating token pattern. hints are
// literal substrings at least one of which must occur for the pattern to be worth
// running, which keeps the common no-secret case at memchr speed.
func regexKind(re *regexp.Regexp, hints ...string) detector {
	return func(_ *Redactor, s, work string) []span {
		if !containsAny(work, hints) {
			return nil
		}
		var out []span
		for _, loc := range re.FindAllStringIndex(work, -1) {
			out = append(out, span{start: loc[0], end: loc[1], secret: s[loc[0]:loc[1]]})
		}
		return out
	}
}

func containsAny(s string, subs []string) bool {
	for _, x := range subs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// ---- provider tokens --------------------------------------------------------

var (
	jwtRE    = regexp.MustCompile(`\beyJ[A-Za-z0-9_\-]{8,}\.eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]*`)
	awsRE    = regexp.MustCompile(`\b(?:AKIA|ASIA|AROA|AIDA|AGPA|ANPA|ANVA|AIPA)[0-9A-Z]{16}\b`)
	githubRE = regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36,255}|github_pat_[A-Za-z0-9_]{22,255})\b`)
	gitlabRE = regexp.MustCompile(`\bglpat-[A-Za-z0-9_\-]{20,}`)
	slackRE  = regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9\-]{10,}|\bxapp-\d-[A-Za-z0-9\-]{10,}|https://hooks\.slack\.com/services/T[A-Za-z0-9]+/B[A-Za-z0-9]+/[A-Za-z0-9]{16,}`)
	googleRE = regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}\b`)
	stripeRE = regexp.MustCompile(`\b(?:sk|rk)_(?:live|test)_[0-9A-Za-z]{16,}\b`)
	npmRE    = regexp.MustCompile(`\bnpm_[A-Za-z0-9]{36}\b`)
	pypiRE   = regexp.MustCompile(`\bpypi-AgEIcHlwaS5vcmc[A-Za-z0-9_\-]{50,}`)
	llmRE    = regexp.MustCompile(`\bsk-(?:ant-|proj-|or-|svcacct-)?[A-Za-z0-9_\-]{20,}`)
	hfRE     = regexp.MustCompile(`\bhf_[A-Za-z0-9]{34,}\b`)
)

// detectLLM finds provider API keys of the sk-... family. The prefix alone is
// common in slugs ("sk-learn-..."), so a real key must also contain a digit.
func detectLLM(_ *Redactor, s, work string) []span {
	if !strings.Contains(work, "sk-") {
		return nil
	}
	var out []span
	for _, loc := range llmRE.FindAllStringIndex(work, -1) {
		m := work[loc[0]:loc[1]]
		if !strings.ContainsAny(m, "0123456789") {
			continue
		}
		out = append(out, span{start: loc[0], end: loc[1], secret: s[loc[0]:loc[1]]})
	}
	return out
}

// ---- PEM private keys -------------------------------------------------------

var (
	pemFullRE = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----[\s\S]*?-----END (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----`)
	// pemOpenRE covers a block whose END line was lost, typically because tool
	// output was truncated. It needs at least one base64 body line, so prose or code
	// that merely mentions the BEGIN line is left alone.
	pemOpenRE = regexp.MustCompile(`-----BEGIN (?:[A-Z0-9]+ )*PRIVATE KEY(?: BLOCK)?-----(?:[ \t]*\r?\n[ \t]*(?:[A-Za-z0-9+/=]{16,}|[A-Za-z][A-Za-z0-9-]*: [^\r\n]*))+`)
)

func detectPrivateKey(_ *Redactor, s, work string) []span {
	if !strings.Contains(work, "-----BEGIN") {
		return nil
	}
	var out []span
	for _, loc := range pemFullRE.FindAllStringIndex(work, -1) {
		out = append(out, span{start: loc[0], end: loc[1], secret: s[loc[0]:loc[1]]})
	}
	for _, loc := range pemOpenRE.FindAllStringIndex(work, -1) {
		out = append(out, span{start: loc[0], end: loc[1], secret: s[loc[0]:loc[1]]})
	}
	return out
}

// ---- credentials in URLs ----------------------------------------------------

var urlCredRE = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.\-]{1,20}://([^\s/:@'"<>]+):([^\s/@'"<>]{3,})@`)

func detectURLCred(_ *Redactor, s, work string) []span {
	if !strings.Contains(work, "://") {
		return nil
	}
	var out []span
	for _, m := range urlCredRE.FindAllStringSubmatchIndex(work, -1) {
		user, pass := work[m[2]:m[3]], work[m[4]:m[5]]
		if !urlPasswordOK(user, pass) {
			continue
		}
		out = append(out, span{start: m[4], end: m[5], secret: s[m[4]:m[5]]})
	}
	return out
}

// urlPasswordOK rejects placeholders and the throwaway credentials that every
// docker-compose file contains (postgres:postgres@localhost).
func urlPasswordOK(user, pass string) bool {
	if isReference(pass) || isPlaceholder(pass) {
		return false
	}
	if strings.EqualFold(user, pass) {
		return false
	}
	switch strings.ToLower(pass) {
	case "password", "pass", "passwd", "secret", "changeme", "example", "test", "guest", "admin", "root", "user":
		return false
	}
	return true
}

// ---- Authorization headers and bearer tokens --------------------------------

var (
	authHeaderRE = regexp.MustCompile(`(?i)\b(?:proxy-)?authorization["']?\s*[:=]\s*["']?(?:(?:bearer|basic|token|digest|negotiate|apikey|api-key)\s+)?([^\s"',;<>{}()\[\]]{8,})`)
	bearerRE     = regexp.MustCompile(`(?i)\bbearer\s+([A-Za-z0-9._~+/\-]{20,}={0,2})`)
)

func detectBearer(_ *Redactor, s, work string) []span {
	var out []span
	if containsFold(work, "authorization") {
		for _, m := range authHeaderRE.FindAllStringSubmatchIndex(work, -1) {
			v := work[m[2]:m[3]]
			if !credentialValueOK(v) {
				continue
			}
			out = append(out, span{start: m[2], end: m[3], secret: s[m[2]:m[3]]})
		}
	}
	if containsFold(work, "bearer") {
		for _, m := range bearerRE.FindAllStringSubmatchIndex(work, -1) {
			v := work[m[2]:m[3]]
			if !credentialValueOK(v) || !strings.ContainsAny(v, "0123456789") || shannon(v) < 3.0 {
				continue
			}
			out = append(out, span{start: m[2], end: m[3], secret: s[m[2]:m[3]]})
		}
	}
	return out
}

// containsFold is a cheap prefilter for a lower-case ASCII word: it accepts the
// lower-case, upper-case and capitalised spellings, which covers every spelling
// that occurs in practice; the regexes themselves are case-insensitive.
func containsFold(s, word string) bool {
	return strings.Contains(s, word) || strings.Contains(s, strings.ToUpper(word)) ||
		strings.Contains(s, strings.ToUpper(word[:1])+word[1:])
}

// credentialValueOK rejects header values that are variables, placeholders,
// scheme names or identifiers rather than credentials.
func credentialValueOK(v string) bool {
	if len(v) < 8 || isReference(v) || isPlaceholder(v) || isIdentifierLike(v) {
		return false
	}
	switch strings.ToLower(v) {
	case "bearer", "basic", "digest", "negotiate", "token", "apikey", "api-key", "required", "none", "null":
		return false
	}
	return !strings.Contains(v, "...")
}

// ---- key = value secrets ----------------------------------------------------

// kvKeyword ends a secret-looking name. Requiring the keyword to END the name
// (secret_key, dbPassword, X-Api-Key) keeps secret_name, token_url, max_tokens,
// password_field and tokenizer out of the rule.
const kvKeyword = `(?:api[_\-]?key|apikey|(?:secret|access|signing|encryption|master|private)[_\-]?key|secret|token|passw(?:or)?d|passwd|pwd|credentials?)`

// kvValue alternatives: a double-quoted, single-quoted or bare literal of at
// least 12 characters.
const kvValue = `(?:"((?:[^"\\\r\n]|\\.){12,})"|'((?:[^'\\\r\n]|\\.){12,})'|([^\s"'` + "`" + `,;&)}\]<>]{12,}))`

var (
	kvRE     = regexp.MustCompile(`(?i)([A-Za-z0-9_.\-]*?` + kvKeyword + `)["']?[ \t]*(:=|=>|=|:)[ \t]*` + kvValue)
	kvFlagRE = regexp.MustCompile(`(?i)(?:^|[\s"'])--((?:password|passwd|pwd|secret|token|api[_\-]?key|access[_\-]?token|auth[_\-]?token|client[_\-]?secret))(?:=|[ \t]+)` + kvValue)
)

func detectKV(_ *Redactor, s, work string) []span {
	if !strings.ContainsAny(work, ":=") && !strings.Contains(work, "--") {
		return nil
	}
	var out []span
	for _, m := range kvRE.FindAllStringSubmatchIndex(work, -1) {
		key, sep := work[m[2]:m[3]], work[m[4]:m[5]]
		if sp, ok := kvSpan(s, work, m[6:12], key, sep); ok {
			out = append(out, sp)
		}
	}
	for _, m := range kvFlagRE.FindAllStringSubmatchIndex(work, -1) {
		if sp, ok := kvSpan(s, work, m[4:10], "--"+work[m[2]:m[3]], "="); ok {
			out = append(out, sp)
		}
	}
	return out
}

// kvSpan picks the value group that matched and validates it. g holds the
// (start,end) index pairs of the three value alternatives.
func kvSpan(s, work string, g []int, key, sep string) (span, bool) {
	for i := 0; i < 3; i++ {
		a, b := g[2*i], g[2*i+1]
		if a < 0 {
			continue
		}
		v := work[a:b]
		if !kvValueOK(v, i < 2, key, sep) {
			return span{}, false
		}
		return span{start: a, end: b, secret: s[a:b]}, true
	}
	return span{}, false
}

var (
	snakeWordsRE = regexp.MustCompile(`^[A-Za-z]+(?:[_\-.][A-Za-z]+)+$`)
	camelRE      = regexp.MustCompile(`^(?:[a-z]+(?:[A-Z][a-z]+)+|(?:[A-Z][a-z]+){2,})$`)
	identChainRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)+$`)
	screamingRE  = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// kvValueOK decides whether the literal after a secret-named key is a secret
// rather than a reference, a placeholder or a name.
func kvValueOK(v string, quoted bool, key, sep string) bool {
	if utf8.RuneCountInString(v) < 12 {
		return false
	}
	if isReference(v) || isPlaceholder(v) || isNumeric(v) || isURLWithoutCreds(v) || looksLikePath(v) || isIdentifierLike(v) {
		return false
	}
	if quoted {
		// A sentence is a UI string or an error message, not a password.
		return !strings.ContainsAny(v, " \t")
	}
	if strings.ContainsAny(v, "()[]{}\\") || strings.HasSuffix(v, "->") || identChainRE.MatchString(v) {
		return false
	}
	// A bare run of letters after "=" is a variable in code. In dotenv and shell
	// files (SCREAMING_KEY=value, no spaces) it is a value.
	if isLettersOnly(v) {
		return sep == "=" && screamingRE.MatchString(key)
	}
	return true
}

func isLettersOnly(v string) bool {
	for _, r := range v {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	return v != ""
}

var (
	shellVarRE = regexp.MustCompile(`^\$[A-Za-z_][A-Za-z0-9_]*$`)
	fmtVerbRE  = regexp.MustCompile(`^%(?:[sdvrq]|\(?[A-Za-z_][A-Za-z0-9_]*\)?[sdvrq]?)$`)
	ivarRE     = regexp.MustCompile(`^@[A-Za-z_][A-Za-z0-9_.]*$`)
	yamlRefRE  = regexp.MustCompile(`^[&*!][A-Za-z_][A-Za-z0-9_\-]*$`)
)

// isReference reports values that point at a secret instead of containing one:
// shell and template variables, format verbs, instance variables, YAML aliases
// and the configuration accessors of common languages. A password that merely
// starts with "$" or "@" is not a reference; it has to look like one.
func isReference(v string) bool {
	if v == "" {
		return true
	}
	for _, p := range []string{"${", "$(", "{{", "%(", "#{", "<%", "{", "<", "[", "("} {
		if strings.HasPrefix(v, p) {
			return true
		}
	}
	if strings.Contains(v, "${") || strings.Contains(v, "{{") || strings.Contains(v, "%(") {
		return true
	}
	if v == "|" || v == ">" || shellVarRE.MatchString(v) || fmtVerbRE.MatchString(v) || ivarRE.MatchString(v) || yamlRefRE.MatchString(v) {
		return true
	}
	lv := strings.ToLower(v)
	for _, p := range []string{"os.", "env.", "process.", "getenv", "config.", "settings.", "secrets.", "vault:", "ssm:", "ref+", "file://", "env:"} {
		if strings.HasPrefix(lv, p) {
			return true
		}
	}
	return false
}

var (
	placeholderPrefixes = []string{"your_", "your-", "your ", "insert_", "insert-", "<"}
	placeholderInside   = []string{"changeme", "change_me", "change-me", "replace_me", "replace-me", "replaceme", "placeholder", "redacted"}
)

// isPlaceholder reports documentation filler.
func isPlaceholder(v string) bool {
	lv := strings.ToLower(v)
	for _, p := range placeholderPrefixes {
		if strings.HasPrefix(lv, p) {
			return true
		}
	}
	for _, p := range placeholderInside {
		if strings.Contains(lv, p) {
			return true
		}
	}
	if strings.Contains(lv, "xxxxxx") || strings.Contains(v, "******") || strings.Contains(v, "......") {
		return true
	}
	first, _ := utf8.DecodeRuneInString(v)
	for _, r := range v {
		if r != first {
			return false
		}
	}
	return true
}

func isNumeric(v string) bool {
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return v != ""
}

func isURLWithoutCreds(v string) bool {
	lv := strings.ToLower(v)
	return (strings.HasPrefix(lv, "http://") || strings.HasPrefix(lv, "https://") || strings.HasPrefix(lv, "ws://") || strings.HasPrefix(lv, "wss://")) && !strings.Contains(v, "@")
}

// looksLikePath reports file-system paths and lower-case slash-separated names
// ("prod/db/password"), which name a secret rather than hold one. A base64
// secret such as wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY has upper-case
// segments and is not a path.
func looksLikePath(v string) bool {
	switch {
	case strings.HasPrefix(v, "/"), strings.HasPrefix(v, "./"), strings.HasPrefix(v, "../"), strings.HasPrefix(v, "~"), strings.HasPrefix(v, `\\`):
		return true
	case len(v) > 2 && v[1] == ':' && (v[2] == '\\' || v[2] == '/') && unicode.IsLetter(rune(v[0])):
		return true
	}
	if strings.Count(v, "/") >= 1 && strings.ToLower(v) == v && !strings.ContainsAny(v, "+=") {
		return true
	}
	return false
}

// isIdentifierLike reports names: snake_case, kebab-case, dotted, camelCase and
// SCREAMING_SNAKE made of letters only. Real secrets almost always contain a
// digit or irregular case.
func isIdentifierLike(v string) bool {
	if snakeWordsRE.MatchString(v) {
		return true
	}
	return camelRE.MatchString(v)
}

// ---- entropy near a key-like word -------------------------------------------

var (
	entCandRE = regexp.MustCompile(`[A-Za-z0-9+/_\-]{20,}={0,2}`)
	// entKeyBeforeRE requires a key-like word directly before the candidate, so the
	// rule can never fire on a base64 blob, hash or identifier that merely sits in
	// a file. The lower-case and upper-case forms must start a word; the capitalised
	// form may continue a camelCase name (apiKey, authToken).
	entKeyBeforeRE = regexp.MustCompile(`(?:(?:^|[^A-Za-z])(?:key|secret|token|passw(?:or)?d|passwd|pwd|credentials?|auth|authorization|bearer|KEY|SECRET|TOKEN|PASSWORD|PASSWD|PWD|CREDENTIALS?|AUTH|AUTHORIZATION|BEARER)|(?:Key|Secret|Token|Passw(?:or)?d|Credentials?|Auth|Authorization|Bearer))(?:[_\-][A-Za-z0-9_\-]{0,24}|[A-Z][A-Za-z0-9]{0,24})?["']?[ \t]*(?:is|are|was|=>|:=|->|[:=])?[ \t]*["'(\[]?$`)
	uuidRE         = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

func detectEntropy(_ *Redactor, s, work string) []span {
	if len(work) < 20 {
		return nil
	}
	var out []span
	for _, loc := range entCandRE.FindAllStringIndex(work, -1) {
		c := work[loc[0]:loc[1]]
		if !entropyCandidateOK(c) {
			continue
		}
		ls := strings.LastIndexByte(work[:loc[0]], '\n') + 1
		if loc[0]-ls > 64 {
			ls = loc[0] - 64
		}
		if !entKeyBeforeRE.MatchString(work[ls:loc[0]]) {
			continue
		}
		out = append(out, span{start: loc[0], end: loc[1], secret: s[loc[0]:loc[1]]})
	}
	return out
}

func entropyCandidateOK(c string) bool {
	var digit, letter bool
	for i := 0; i < len(c); i++ {
		switch b := c[i]; {
		case b >= '0' && b <= '9':
			digit = true
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z':
			letter = true
		}
	}
	if !digit || !letter {
		return false
	}
	if isHex(c) || uuidRE.MatchString(c) || strings.Count(c, "/") >= 3 {
		return false
	}
	if isIdentifierLike(c) || isPlaceholder(c) || isWordy(c) {
		return false
	}
	return shannon(c) >= 3.5
}

// isWordy reports strings made mostly of dictionary-length lower-case runs
// (getUserByIdV2AndValidateSession, my-service-name-2024-prod): identifiers and
// slugs with a digit in them. Random tokens alternate case and digits every
// character or two and score near zero.
func isWordy(c string) bool {
	total, run, inRun := 0, 0, false
	flush := func() {
		if run >= 3 {
			total += run
		}
		run, inRun = 0, false
	}
	for i := 0; i < len(c); i++ {
		b := c[i]
		switch {
		case b >= 'a' && b <= 'z':
			run++
			inRun = true
		case b >= 'A' && b <= 'Z':
			flush()
			run, inRun = 1, true // a capital starts a camelCase word
		default:
			flush()
		}
	}
	if inRun {
		flush()
	}
	return float64(total) >= 0.7*float64(len(c))
}

func isHex(c string) bool {
	if c == "" {
		return false
	}
	for i := 0; i < len(c); i++ {
		b := c[i]
		if !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F') {
			return false
		}
	}
	return true
}

// shannon is the Shannon entropy of s in bits per byte.
func shannon(s string) float64 {
	if s == "" {
		return 0
	}
	var freq [256]int
	for i := 0; i < len(s); i++ {
		freq[s[i]]++
	}
	n := float64(len(s))
	h := 0.0
	for _, f := range freq {
		if f == 0 {
			continue
		}
		p := float64(f) / n
		h -= p * math.Log2(p)
	}
	return h
}

// ---- e-mail addresses -------------------------------------------------------

var emailRE = regexp.MustCompile(`\b[A-Za-z0-9][A-Za-z0-9._%+\-]*@[A-Za-z0-9](?:[A-Za-z0-9\-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9\-]*[A-Za-z0-9])?)*\.[A-Za-z]{2,24}\b`)

var commonTLD = map[string]bool{
	"com": true, "org": true, "net": true, "edu": true, "gov": true, "mil": true, "io": true, "co": true, "dev": true,
	"ai": true, "app": true, "me": true, "us": true, "uk": true, "de": true, "fr": true, "jp": true, "cn": true,
	"ru": true, "in": true, "br": true, "ca": true, "au": true, "nl": true, "se": true, "no": true, "fi": true,
	"es": true, "it": true, "ch": true, "at": true, "be": true, "pl": true, "cz": true, "kr": true, "info": true,
	"biz": true, "xyz": true, "tech": true, "cloud": true, "online": true, "site": true, "tv": true, "sh": true,
}

func detectEmail(_ *Redactor, s, work string) []span {
	if !strings.Contains(work, "@") {
		return nil
	}
	var out []span
	for _, loc := range emailRE.FindAllStringIndex(work, -1) {
		m := work[loc[0]:loc[1]]
		if !emailOK(m) || loc[0] >= 3 && work[loc[0]-3:loc[0]] == "://" {
			continue // user@host in a URL is userinfo, not an address
		}
		// Normalise the secret so Alice@X.com and alice@x.com share a token.
		out = append(out, span{start: loc[0], end: loc[1], secret: strings.ToLower(s[loc[0]:loc[1]])})
	}
	return out
}

func emailOK(m string) bool {
	at := strings.LastIndexByte(m, '@')
	local, domain := strings.ToLower(m[:at]), strings.ToLower(m[at+1:])
	switch local {
	case "git", "hg", "svn", "ssh", "noreply", "no-reply", "donotreply", "do-not-reply":
		return false
	}
	labels := strings.Split(domain, ".")
	tld := labels[len(labels)-1]
	switch tld {
	case "invalid", "test", "example", "localhost", "local", "internal", "lan":
		return false
	}
	if len(labels) >= 2 {
		reg := labels[len(labels)-2]
		if reg == "example" && (tld == "com" || tld == "org" || tld == "net" || tld == "edu") {
			return false
		}
	}
	// a@b.shape, W@x.T: matrix products and method chains, not addresses.
	if len(labels) == 2 && len(labels[0]) <= 2 && !commonTLD[tld] {
		return false
	}
	if len(local) <= 2 && !commonTLD[tld] {
		return false
	}
	return true
}

// ---- IP addresses -----------------------------------------------------------

var ipv4RE = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(ss))
	for i, s := range ss {
		out[i] = netip.MustParsePrefix(s)
	}
	return out
}

// nonPublicV4 lists the ranges that are never worth redacting: they identify no
// one and appear constantly in code and docs.
var nonPublicV4 = mustPrefixes(
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "192.168.0.0/16", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4",
)

func detectIPv4(_ *Redactor, s, work string) []span {
	if !strings.Contains(work, ".") {
		return nil
	}
	var out []span
	for _, loc := range ipv4RE.FindAllStringIndex(work, -1) {
		a, b := loc[0], loc[1]
		if a >= 2 && work[a-1] == '.' && isDigit(work[a-2]) || b+1 < len(work) && work[b] == '.' && isDigit(work[b+1]) {
			continue // part of a longer dotted number (OID, version)
		}
		if a >= 2 && work[a-1] == '/' && isLetter(work[a-2]) {
			continue // Chrome/120.0.0.0
		}
		if a >= 2 && work[a-1] == '-' && isLetter(work[a-2]) || b+1 < len(work) && work[b] == '-' && isLetter(work[b+1]) ||
			a >= 1 && work[a-1] == '_' || b < len(work) && work[b] == '_' {
			continue // build-1.2.3.4, 1.2.3.4-beta
		}
		if b+1 < len(work) && work[b] == '/' && isDigit(work[b+1]) {
			continue // 8.8.8.0/24 names a network
		}
		addr, err := netip.ParseAddr(work[a:b])
		if err != nil || !publicV4(addr) {
			continue
		}
		out = append(out, span{start: a, end: b, secret: s[a:b]})
	}
	return out
}

func publicV4(a netip.Addr) bool {
	if !a.Is4() {
		return false
	}
	for _, p := range nonPublicV4 {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

var (
	globalV6 = netip.MustParsePrefix("2000::/3")
	docV6    = netip.MustParsePrefix("2001:db8::/32")
)

func detectIPv6(_ *Redactor, s, work string) []span {
	if strings.Count(work, ":") < 2 {
		return nil
	}
	var out []span
	n := len(work)
	for i := 0; i < n; {
		if !isV6Char(work[i]) {
			i++
			continue
		}
		j := i
		for j < n && isV6Char(work[j]) {
			j++
		}
		a, b := i, j
		i = j
		// Trim punctuation that belongs to the sentence, not the address: a single
		// leading colon ("addr:2a00::1"), trailing dots and a single trailing colon.
		// A double colon at either end is part of the address ("::1", "2a00::").
		if b-a >= 2 && work[a] == ':' && work[a+1] != ':' {
			a++
		}
		for b > a && (work[b-1] == ':' || work[b-1] == '.') && !(work[b-1] == ':' && b-2 >= a && work[b-2] == ':') {
			b--
		}
		if strings.Count(work[a:b], ":") < 2 {
			continue
		}
		if a > 0 && (isAlnum(work[a-1]) || work[a-1] == '_') || b < n && (isAlnum(work[b]) || work[b] == '_') {
			continue // std::vector, foo::bar, hex glued to a word
		}
		if b+1 < n && work[b] == '/' && isDigit(work[b+1]) {
			continue // 2a00:1450::/32 names a network
		}
		addr, err := netip.ParseAddr(work[a:b])
		if err != nil {
			continue
		}
		if addr.Is4In6() {
			continue
		}
		if !globalV6.Contains(addr) || docV6.Contains(addr) {
			continue
		}
		out = append(out, span{start: a, end: b, secret: strings.ToLower(s[a:b])})
	}
	return out
}

func isV6Char(b byte) bool {
	return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F' || b == ':' || b == '.'
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }
func isLetter(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
func isAlnum(b byte) bool { return isDigit(b) || isLetter(b) }

// ---- home directories -------------------------------------------------------

var (
	homeUnixRE = regexp.MustCompile(`/(?:home|Users)/([A-Za-z0-9_][A-Za-z0-9_.\-]*)`)
	homeWinRE  = regexp.MustCompile(`(?i)[A-Za-z]:(?:\\{1,2}|/)Users(?:\\{1,2}|/)([^\\/\s"'<>|:*?]+)`)
)

func detectPath(r *Redactor, s, work string) []span {
	var out []span
	if strings.Contains(work, "/home/") || strings.Contains(work, "/Users/") {
		for _, m := range homeUnixRE.FindAllStringSubmatchIndex(work, -1) {
			if m[0] >= 1 && work[m[0]-1] == '.' {
				continue // ../home/x is a relative path through a directory that happens to be called home
			}
			if fileLike(work[m[2]:m[3]]) && (m[3] >= len(work) || work[m[3]] != '/') {
				continue // /home/ok.md is a file directly under /home, not a user
			}
			out = r.pathSpan(out, s, m[2], m[3])
		}
	}
	if strings.Contains(work, "Users") || strings.Contains(work, "users") || strings.Contains(work, "USERS") {
		for _, m := range homeWinRE.FindAllStringSubmatchIndex(work, -1) {
			out = r.pathSpan(out, s, m[2], m[3])
		}
	}
	return out
}

// fileLike reports names that end in a common file extension.
func fileLike(name string) bool {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return false
	}
	switch strings.ToLower(name[i+1:]) {
	case "md", "txt", "go", "json", "yaml", "yml", "toml", "sh", "py", "js", "ts", "log", "conf", "cfg", "ini", "lock", "sock", "pid", "xml", "html", "csv", "sql", "rs", "c", "h", "cc", "java", "rb", "zip", "gz", "tar", "pem", "key", "crt":
		return true
	}
	return false
}

func (r *Redactor) pathSpan(out []span, s string, a, b int) []span {
	// A sentence-ending dot or dash is not part of the user name.
	for b > a+1 && (s[b-1] == '.' || s[b-1] == '-') {
		b--
	}
	name := s[a:b]
	switch strings.ToLower(name) {
	case "shared", "public", "default", "guest", "all", "deleted":
		return out // not a person
	}
	if name == r.placeholder {
		return out
	}
	return append(out, span{start: a, end: b, secret: name, repl: r.placeholder})
}
