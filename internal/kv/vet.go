package kv

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// What a promotion is allowed to be.
//
// A compactor's "promote" entry proposes a fact for the shared layers, which every agent
// of the fleet reads. The compactor is a model that has just read the whole thread,
// tool output included, so what it proposes is only as trustworthy as the most hostile
// text it saw. The harness therefore treats a promotion as an unverified proposal and
// lets through only what looks like a fact about the repository ("tests: make
// test-unit"), never something that reads as an order, a claim of authority or a command
// to run. That reading is a screen, not a proof: a determined author can phrase an
// instruction as a fact, which is why nothing here installs anything. Proposals only
// reach the board as pending notes, they carry Unverified, they are few and short, and
// the step that would fold one into a shared layer (the curator, not built) is where an
// independent check belongs.

// promotionRules are the shapes refused, each with the name reported in the warning.
var promotionRules = []struct {
	name string
	re   *regexp.Regexp
}{
	// An imperative or modal opening addresses the reader: a fact does not.
	{"an order", regexp.MustCompile(`(?i)^\s*(?:(?:please|note|important|warning|reminder)\s*[:,-]?\s*)*(?:always|never|must|should|shall|do not|don't|dont|ensure|make sure|remember|ignore|disregard|forget|override|obey|from now on|whenever|every time|before (?:every|each|any|all)|after (?:every|each|any|all)|we must|everyone must|all agents|every agent|you|your)\b`)},
	// A claim that somebody with authority said so.
	{"a claim of authority", regexp.MustCompile(`(?i)\b(?:user|manager|operator|admin|administrator|system|owner|maintainers?|lead|boss)\b[^.;\n]{0,40}\b(?:wants?|said|says|asked|approved?|requires?|requested|instructs?|instructed|told|authori[sz]ed|permits?|confirmed|demands?|insists?|expects?)\b`)},
	{"a claim of authority", regexp.MustCompile(`(?i)\b(?:approved|authori[sz]ed|permitted|confirmed) (?:by|from) (?:the )?(?:user|manager|owner|admin|operator)\b`)},
	// An announcement of a rule, or of a message from the system.
	{"a new rule", regexp.MustCompile(`(?i)\b(?:new|standing|updated|important|urgent|critical|mandatory|required|special) (?:rule|instruction|policy|directive|order|requirement|notice)s?\b`)},
	{"a message from the system", regexp.MustCompile(`(?i)\b(?:system|admin|security|developer|operator) (?:notice|message|override|instruction|alert|prompt)s?\b`)},
	{"an instruction to forget", regexp.MustCompile(`(?i)\b(?:ignore|disregard|forget) (?:all |any |the |your |these )?(?:previous|prior|above|earlier|other|existing)\b`)},
	// A command to run: fetch-and-execute, privilege, destruction, remote shells.
	{"a pipe into a shell", regexp.MustCompile(`(?i)\|\s*(?:sudo\s+)?(?:ba|z|da|k|c)?sh\b`)},
	{"a fetch and execute", regexp.MustCompile(`(?i)\b(?:curl|wget)\b[^\n]{0,200}(?:;|&&)\s*(?:sudo\s+)?(?:ba|z|da|k|c)?sh\b`)},
	{"a command to run", regexp.MustCompile(`(?i)\b(?:eval|exec)\s*[(\x60$]|\bbase64\s+(?:-d|--decode)\b|\bsudo\b|\brm\s+-[a-z]*[rf][a-z]*\b|/dev/tcp/|\bnc\s+-[a-z]*e\b|\bnetcat\b|\|\s*(?:nc|ncat|socat|telnet)\b`)},
	// Getting around the harness's own checks.
	{"a way around a check", regexp.MustCompile(`(?i)\b(?:disable|bypass|skip|circumvent|turn off|ignore)\b[^.;\n]{0,30}\b(?:permissions?|approvals?|prompts?|verification|verifier|review|sandbox|safety|guard|hooks?|confirmation)\b`)},
	{"a way around a check", regexp.MustCompile(`(?i)--no-verify\b|--force\b|--dangerously|\bwithout (?:asking|approval|permission|review|confirmation|verifying)\b|\bno need to (?:ask|verify|confirm|check|review)\b`)},
	// Credentials do not belong on a board every agent reads.
	{"a credential", regexp.MustCompile(`://[^/\s:@]+:[^/\s@]+@|-----BEGIN [A-Z ]*PRIVATE KEY-----|\bAKIA[0-9A-Z]{16}\b|\bgh[pousr]_[A-Za-z0-9]{30,}|\bxox[baprs]-[A-Za-z0-9-]{10,}|\bsk-[A-Za-z0-9_-]{20,}|\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.`)},
}

// instructionLike reports why text reads as an order, a claim of authority or a command
// rather than a fact ("" when it does not).
func instructionLike(text string) string {
	for _, r := range promotionRules {
		if r.re.MatchString(text) {
			return r.name
		}
	}
	return ""
}

// vetPromotions decides which of a patch's promotions go on. Each must name a scope
// (shared or role), be one short line of at most MaxPromotionChars (a longer one is
// refused, not cut: half a fact misleads), read as a fact, not repeat an earlier one,
// and fit under MaxPromotions per patch. What passes is escaped, keyed by a valid
// section name (or none) and marked Unverified. Every refusal is a warning.
func vetPromotions(in []Promotion, pol ApplyPolicy, warns *[]string) []Promotion {
	if len(in) == 0 {
		return nil
	}
	var out []Promotion
	seen := map[string]bool{}
	for i, p := range in {
		if len(out) >= pol.MaxPromotions {
			*warns = append(*warns, fmt.Sprintf("promote: only the first %d proposals are kept; %d more ignored", pol.MaxPromotions, len(in)-i))
			break
		}
		scope := strings.ToLower(strings.TrimSpace(p.Scope))
		if scope != "shared" && scope != "role" {
			*warns = append(*warns, fmt.Sprintf("promote[%d]: scope %q ignored", i, EscapeLine(p.Scope, 20)))
			continue
		}
		text := EscapeLine(p.Text, 0)
		switch {
		case text == "":
			*warns = append(*warns, fmt.Sprintf("promote[%d]: empty text ignored", i))
			continue
		case utf8.RuneCountInString(text) > pol.MaxPromotionChars:
			*warns = append(*warns, fmt.Sprintf("promote[%d]: %d characters is too long for a fact (at most %d); ignored", i, utf8.RuneCountInString(text), pol.MaxPromotionChars))
			continue
		}
		if why := instructionLike(text); why != "" {
			*warns = append(*warns, fmt.Sprintf("promote[%d]: ignored, it reads as %s, not as a fact", i, why))
			continue
		}
		dup := scope + "|" + strings.ToLower(text)
		if seen[dup] {
			continue
		}
		seen[dup] = true
		key, _ := SectionKey(p.Key)
		out = append(out, Promotion{Scope: scope, Key: key, Text: text, Unverified: true})
	}
	return out
}
