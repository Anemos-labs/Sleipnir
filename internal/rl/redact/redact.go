// Package redact removes secrets and personal data from text that is about to
// become training data.
//
// # Contract
//
// A [Redactor] is deterministic and idempotent:
//
//   - The same input text always produces the same output. Redaction looks at one
//     string at a time and never at its neighbours, so a message that appears in
//     the prompts of a hundred steps is redacted identically in all of them and
//     prefix sharing between prompts survives.
//   - Equal secrets get equal replacement tokens, ⟦redacted:<kind>:<6 hex of
//     sha256(salt+secret)>⟧, so a model still sees "the same identifier again"
//     without ever seeing the value. Different salts give unrelated tokens,
//     which keeps tokens from being joined across datasets.
//   - String(String(x)) == String(x). String runs its rules to a fixed point and
//     never rewrites an existing token, so exporting already redacted data is
//     safe.
//
// # Rules
//
// Every rule is a detector plus validators that keep it away from ordinary
// code. Rules are listed in priority order; when two detectors overlap, the
// leftmost match wins, then the higher-priority rule, then the longer match.
//
//	kind         what it matches                                     what it leaves alone
//	aws          AKIA/ASIA/AROA/AIDA/... access key ids              lower-case or shorter look-alikes
//	github       gh[pousr]_… and github_pat_… tokens                 "ghp_" alone, short suffixes
//	gitlab       glpat-… tokens
//	slack        xox[baprs]-…, xapp-…, hooks.slack.com/services/…
//	google       AIza… API keys
//	stripe       sk_/rk_ live and test secret keys                   pk_ publishable keys
//	npm          npm_… tokens
//	pypi         pypi-AgEIcHlwaS5vcmc… upload tokens
//	llm          sk-… / sk-ant-… / sk-proj-… provider keys (≥ 20)    "risk-…", "sk-learn"
//	huggingface  hf_… tokens
//	jwt          three-part JWTs (eyJ….eyJ….sig)                     lone base64 that merely starts with eyJ
//	private_key  PEM private key blocks (also unterminated ones      a mention of the BEGIN line in prose or code
//	             whose middle was elided by output truncation)
//	url_cred     the password in scheme://user:password@host         user:user, user:password, ${VAR}, <placeholder>
//	bearer       Authorization header values and "Bearer <token>"    "Bearer $TOKEN", "Bearer {token}", short words
//	secret       key = value where the key ends in api_key, secret,  values that are variables, calls, paths, URLs,
//	             token, password, passwd, pwd or credential(s) and   sentences, placeholders, identifiers such as
//	             the value is a literal of at least 12 characters    "access_token", "GITHUB_TOKEN"
//	entropy      a ≥ 20 character high-entropy token, only when it   git SHAs, other hex hashes, UUIDs, paths,
//	             directly follows a key-like word (key, secret,      identifiers, and every base64 blob that is
//	             token, password, auth, bearer, ...)                 not next to such a word (test data)
//	email        addresses                                           git@github.com, noreply@…, example.com, npm
//	                                                                 "pkg@1.2.3", matrix products such as a@b.T
//	ipv4         public unicast addresses                            loopback, private, link-local, CGNAT, multicast,
//	                                                                 reserved, documentation ranges, versions
//	                                                                 (Chrome/120.0.0.0, 1.2.3.4.5), netmasks
//	ipv6         global unicast (2000::/3) addresses                 loopback, link-local, ULA, 2001:db8::/32, C++
//	                                                                 "std::x", MAC addresses, timestamps
//	path         the user name in /home/<u>, /Users/<u>, C:\Users\<u> replaced by the placeholder (default "user")
//
// Rules only see plain text. [Redactor.JSON] applies them to the string values of
// a JSON document and nothing else.
//
// The detectors are heuristics. They are tuned to prefer leaving an ambiguous
// string alone over garbling code, because a training set full of mangled
// identifiers teaches the model to write mangled identifiers. Callers that need
// a stricter policy should run a dedicated scanner first; this package is the
// last line of defence that is cheap enough to run on every prompt.
package redact

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Kind names accepted in [Config.Kinds] and reported by [Redactor.Stats].
const (
	KindAWS         = "aws"
	KindGitHub      = "github"
	KindGitLab      = "gitlab"
	KindSlack       = "slack"
	KindGoogle      = "google"
	KindStripe      = "stripe"
	KindNPM         = "npm"
	KindPyPI        = "pypi"
	KindLLM         = "llm"
	KindHuggingFace = "huggingface"
	KindJWT         = "jwt"
	KindPrivateKey  = "private_key"
	KindURLCred     = "url_cred"
	KindBearer      = "bearer"
	KindSecret      = "secret"
	KindEntropy     = "entropy"
	KindEmail       = "email"
	KindIPv4        = "ipv4"
	KindIPv6        = "ipv6"
	KindPath        = "path"
)

// GroupTokens is a Config.Kinds alias for the provider-token families (aws,
// github, gitlab, slack, google, stripe, npm, pypi, llm, huggingface).
const GroupTokens = "tokens"

// AllKinds lists every valid kind in rule-priority order.
func AllKinds() []string {
	out := make([]string, len(ruleOrder))
	copy(out, ruleOrder)
	return out
}

var ruleOrder = []string{
	KindPrivateKey, KindJWT, KindAWS, KindGitHub, KindGitLab, KindSlack, KindGoogle, KindStripe, KindNPM,
	KindPyPI, KindLLM, KindHuggingFace, KindURLCred, KindBearer, KindSecret, KindEntropy, KindEmail,
	KindIPv4, KindIPv6, KindPath,
}

var tokenFamilies = []string{KindAWS, KindGitHub, KindGitLab, KindSlack, KindGoogle, KindStripe, KindNPM, KindPyPI, KindLLM, KindHuggingFace}

// Config configures a Redactor.
type Config struct {
	// Salt is mixed into every replacement token. Use one salt per dataset: tokens
	// are stable inside it and unlinkable across datasets.
	Salt string
	// Kinds restricts the active rules; nil enables all of them. Names are
	// case-insensitive; GroupTokens enables every token family. Unknown names are
	// ignored, so callers that take kinds from user input should check them with
	// Validate first.
	Kinds []string
	// Allow lists patterns that must never be redacted (documentation example
	// keys, your own service account ids). A match is skipped when a pattern
	// matches the secret itself or the whole matched span.
	Allow []*regexp.Regexp
	// PathPlaceholder replaces user names in home-directory paths (default "user").
	PathPlaceholder string
}

// Validate reports unknown kind names.
func (c Config) Validate() error {
	var bad []string
	for _, k := range c.Kinds {
		if !validKind(strings.ToLower(strings.TrimSpace(k))) {
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		return &UnknownKindError{Kinds: bad}
	}
	return nil
}

// UnknownKindError lists kind names that no rule implements.
type UnknownKindError struct{ Kinds []string }

// Error lists unknown redaction kinds and the supported group and rule names.
func (e *UnknownKindError) Error() string {
	return "redact: unknown kinds " + strings.Join(e.Kinds, ", ") + " (valid: " + strings.Join(append([]string{GroupTokens}, ruleOrder...), ", ") + ")"
}

// validKind accepts the token group or an explicitly supported redaction rule name.
func validKind(k string) bool {
	if k == GroupTokens {
		return true
	}
	for _, r := range ruleOrder {
		if r == k {
			return true
		}
	}
	return false
}

// Redactor applies the rules. It is safe for concurrent use.
type Redactor struct {
	salt        string
	enabled     map[string]bool
	allow       []*regexp.Regexp
	placeholder string

	// memoKey keys memoDigest: random for each Redactor, so the memo's digests are neither joinable
	// between Redactors nor predictable by whoever supplies the texts.
	memoKey [sha256.Size]byte

	mu     sync.Mutex
	stats  map[string]int
	cache  map[[sha256.Size]byte]cacheEntry
	cacheB int
}

type cacheEntry struct {
	out    string
	counts map[string]int
}

// Bounds of the per-Redactor memo: exports redact the same message once per
// step that repeats it, and this turns that into one regex pass per unique text.
const (
	cacheMaxEntries = 1 << 16
	cacheMaxBytes   = 64 << 20
	cacheMinLen     = 64 // shorter strings are cheaper to redact than to hash
)

// New builds a Redactor from c.
func New(c Config) *Redactor {
	r := &Redactor{
		salt:        c.Salt,
		enabled:     map[string]bool{},
		allow:       c.Allow,
		placeholder: c.PathPlaceholder,
		stats:       map[string]int{},
		cache:       map[[sha256.Size]byte]cacheEntry{},
	}
	_, _ = rand.Read(r.memoKey[:]) // never fails (Go 1.24): it ends the process when the system has no randomness
	if r.placeholder == "" {
		r.placeholder = "user"
	}
	if c.Kinds == nil {
		for _, k := range ruleOrder {
			r.enabled[k] = true
		}
		return r
	}
	for _, k := range c.Kinds {
		k = strings.ToLower(strings.TrimSpace(k))
		switch {
		case k == GroupTokens:
			for _, f := range tokenFamilies {
				r.enabled[f] = true
			}
		case validKind(k):
			r.enabled[k] = true
		}
	}
	return r
}

// Stats returns how many replacements were made per kind since New. The map is a
// copy.
func (r *Redactor) Stats() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]int, len(r.stats))
	for k, v := range r.stats {
		out[k] = v
	}
	return out
}

// Total is the sum of Stats.
func (r *Redactor) Total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, v := range r.stats {
		n += v
	}
	return n
}

// String redacts s.
func (r *Redactor) String(s string) string {
	out, _ := r.redactCounted(s)
	return out
}

// Changed reports whether redaction alters s, and returns the redacted text.
func (r *Redactor) Changed(s string) (string, bool) {
	out, counts := r.redactCounted(s)
	return out, len(counts) > 0
}

// redactCounted is String plus the per-kind counts of this call, served from the
// memo when the text was seen before.
func (r *Redactor) redactCounted(s string) (string, map[string]int) {
	if len(s) == 0 {
		return s, nil
	}
	var key [sha256.Size]byte
	memo := len(s) >= cacheMinLen
	if memo {
		key = r.memoDigest(s)
		r.mu.Lock()
		if e, ok := r.cache[key]; ok {
			for k, n := range e.counts {
				r.stats[k] += n
			}
			r.mu.Unlock()
			return e.out, e.counts
		}
		r.mu.Unlock()
	}
	out, counts := r.run(s)
	r.mu.Lock()
	for k, n := range counts {
		r.stats[k] += n
	}
	if memo {
		if len(r.cache) >= cacheMaxEntries || r.cacheB+len(out) > cacheMaxBytes {
			r.cache = map[[sha256.Size]byte]cacheEntry{}
			r.cacheB = 0
		}
		r.cache[key] = cacheEntry{out: out, counts: counts}
		r.cacheB += len(out)
	}
	r.mu.Unlock()
	return out, counts
}

// memoDigest names a text in the memo: the HMAC-SHA256 of s under the Redactor's random memoKey. A memo hit returns the
// redaction of the text the digest names, so the digest must be collision resistant; it is keyed because it only has to
// mean something inside this Redactor. It is an index, not a stored credential: it never leaves the process, and the
// texts are prompts and tool output whose secrets are keys and tokens (long random values), not passwords a person
// chose, so no password-stretching function is called for.
func (r *Redactor) memoDigest(s string) (d [sha256.Size]byte) {
	mac := hmac.New(sha256.New, r.memoKey[:])
	_, _ = io.WriteString(mac, s)
	mac.Sum(d[:0])
	return d
}

// tokenRE matches replacement tokens this package produced. They are masked
// before matching so a token is never re-redacted (idempotence).
var tokenRE = regexp.MustCompile(`⟦redacted:[a-z0-9_]+:[0-9a-f]{6}⟧`)

// maxPasses bounds the fixed-point loop. Every pass turns at least one span of
// plain text into an inert token, so it always terminates far earlier.
const maxPasses = 16

// run repeats redaction until no matches remain or the pass limit is reached, accumulating
// per-kind replacement counts.
func (r *Redactor) run(s string) (string, map[string]int) {
	var counts map[string]int
	for pass := 0; pass < maxPasses; pass++ {
		out, n := r.once(s)
		if len(n) == 0 {
			return s, counts
		}
		if counts == nil {
			counts = map[string]int{}
		}
		for k, v := range n {
			counts[k] += v
		}
		s = out
	}
	return s, counts
}

// span is one detected region to replace.
type span struct {
	start, end int
	kind       string
	secret     string // hashed into the token
	repl       string // verbatim replacement (paths); empty: build a token
	prio       int
}

// once finds every match in s on the token-masked view, resolves overlaps and
// rewrites.
func (r *Redactor) once(s string) (string, map[string]int) {
	work := s
	if strings.Contains(s, "⟦") {
		work = tokenRE.ReplaceAllStringFunc(s, func(m string) string { return strings.Repeat("\x00", len(m)) })
	}
	var spans []span
	for prio, kind := range ruleOrder {
		if !r.enabled[kind] {
			continue
		}
		det := detectors[kind]
		for _, sp := range det(r, s, work) {
			sp.kind, sp.prio = kind, prio
			spans = append(spans, sp)
		}
	}
	if len(spans) == 0 {
		return s, nil
	}
	sort.SliceStable(spans, func(i, j int) bool {
		a, b := spans[i], spans[j]
		if a.start != b.start {
			return a.start < b.start
		}
		if a.prio != b.prio {
			return a.prio < b.prio
		}
		return a.end-a.start > b.end-b.start
	})
	var sb strings.Builder
	sb.Grow(len(s))
	counts := map[string]int{}
	last := 0
	for _, sp := range spans {
		if sp.start < last || sp.end <= sp.start {
			continue
		}
		// A span that reaches into a masked token would re-redact the token itself.
		if strings.IndexByte(work[sp.start:sp.end], 0) >= 0 {
			continue
		}
		if r.allowed(s[sp.start:sp.end], sp.secret) {
			// An allowed value is protected from every rule, not just the one that
			// found it first.
			sb.WriteString(s[last:sp.end])
			last = sp.end
			continue
		}
		sb.WriteString(s[last:sp.start])
		if sp.repl != "" {
			sb.WriteString(sp.repl)
		} else {
			sb.WriteString(r.token(sp.kind, sp.secret))
		}
		last = sp.end
		counts[sp.kind]++
	}
	if len(counts) == 0 {
		return s, nil
	}
	sb.WriteString(s[last:])
	return sb.String(), counts
}

// allowed exempts a redaction when any allow pattern matches either the secret or its enclosing
// span.
func (r *Redactor) allowed(span, secret string) bool {
	for _, re := range r.allow {
		if re.MatchString(secret) || re.MatchString(span) {
			return true
		}
	}
	return false
}

// token renders the replacement for a secret of the given kind.
func (r *Redactor) token(kind, secret string) string {
	sum := sha256.Sum256([]byte(r.salt + secret))
	return "⟦redacted:" + kind + ":" + hex.EncodeToString(sum[:3]) + "⟧"
}
