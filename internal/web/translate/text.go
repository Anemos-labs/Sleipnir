package translate

import (
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/rl/redact"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Untrusted text (model text, tool output, file names, mail, notices) leaves the translator as plain strings that are sanitized for
// display (tools.SanitizeForTerminal: escape sequences, control, bidirectional and invisible characters removed), masked where a part
// looks like a secret (internal/rl/redact: provider keys, tokens, private keys, credentials in URLs, bearer values, key=value
// secrets, high-entropy values next to a key-like word) and cut to a bound. The page escapes every string it renders as well.

// The caps of VOCAB.md, in runes for one-line fields and in bytes for messages.
const (
	capArg     = 160   // tool.arg, refuse.arg
	capOut     = 200   // tool.out
	capReason  = 300   // tool.reason, refuse.reason, sys.text
	capDoing   = 120   // state.doing
	capMail    = 400   // mail.text
	capNote    = 160   // note.text, verdict left items
	capWhy     = 200   // break.why, stall.text, handover.error
	capPlan    = 160   // a plan step
	capID      = 64    // an id, a name, a kind
	capPath    = 300   // a file path
	capEvent   = 16384 // the text of one say, stream or more event
	capMessage = 65536 // the whole of one streamed message
	capCode    = 65536 // the content of a write shown as a code stream
)

var (
	redactorOnce sync.Once
	redactor     *redact.Redactor
)

// masker is the process's redactor (one, so that its bounded memo is shared by every tab). It masks secret-shaped substrings only:
// e-mail addresses, IP addresses and home-directory paths are left alone, because they are what a person reading a transcript of
// their own project expects to see.
func masker() *redact.Redactor {
	redactorOnce.Do(func() {
		redactor = redact.New(redact.Config{Salt: "sleipnir-web", Kinds: []string{redact.GroupTokens, redact.KindJWT, redact.KindPrivateKey,
			redact.KindURLCred, redact.KindBearer, redact.KindSecret, redact.KindEntropy}})
	})
	return redactor
}

// mask returns s with its secret-shaped substrings replaced by redaction tokens.
func mask(s string) string {
	if s == "" {
		return s
	}
	return masker().String(s)
}

// line makes untrusted text one line for display: sanitized, white space (newlines included) collapsed to single spaces, masked and
// cut to max runes with an ellipsis.
func line(s string, max int) string {
	if s == "" {
		return ""
	}
	if len(s) > 8*max+64 {
		s = cutBytes(s, 8*max+64) // what is cut anyway is not worth cleaning
	}
	s = tools.SanitizeForTerminal(s)
	s = strings.Join(strings.Fields(s), " ")
	return cutRunes(mask(s), max)
}

// text cleans untrusted multi-line text (a message, a file's content): sanitized, masked and cut to max bytes on a rune boundary.
// Newlines and tabs stay.
func text(s string, max int) string {
	if s == "" {
		return ""
	}
	if len(s) > max {
		s = cutBytes(s, max)
	}
	return cutBytes(mask(tools.SanitizeForTerminal(s)), max)
}

// cutRunes cuts s to at most max runes, the last one an ellipsis when it was cut.
func cutRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return strings.TrimRightFunc(string(r[:max-1]), unicode.IsSpace) + "…"
}

// cutBytes cuts s to at most max bytes without splitting a rune.
func cutBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	i := max
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

// firstLine is the first line of s that is not blank, trimmed.
func firstLine(s string) string {
	for s != "" {
		i := strings.IndexByte(s, '\n')
		var l string
		if i < 0 {
			l, s = s, ""
		} else {
			l, s = s[:i], s[i+1:]
		}
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// countLines is the number of lines of s (a last line without a newline counts; an empty string has none).
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// safeSplit splits pending streamed text where a secret cannot be cut in two: after the last white space. A secret-shaped token has
// none, so masking each part apart masks it whole. When there is no white space and the text is long, it is all ready.
func safeSplit(s string, force bool) (ready, rest string) {
	if force {
		return s, ""
	}
	i := strings.LastIndexAny(s, " \n\t")
	if i < 0 {
		if len(s) > 4096 {
			return s, ""
		}
		return "", s
	}
	return s[:i+1], s[i+1:]
}
