package research

import (
	"html"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Quote bounds: a passage short enough to say nothing ("the") is no evidence, and one too long is a copy of the page.
const (
	minQuoteRunes = 20
	maxQuoteRunes = 700
	maxCodeRunes  = 4000
	minCodeRunes  = 30
)

// trackingParams are query parameters that say who sent a reader, not what the page is.
var trackingParams = map[string]bool{"fbclid": true, "gclid": true, "mc_cid": true, "mc_eid": true, "ref": true, "ref_src": true, "igshid": true, "si": true}

// Canonical is the form of a URL two citations of the same page share: the scheme and host lower-case, no "www.", no fragment,
// no tracking parameters, no trailing slash on a path, and the remaining parameters sorted.
func Canonical(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return "", errBadURL
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme == "http" {
		u.Scheme = "https"
	}
	u.Host = strings.TrimPrefix(strings.ToLower(u.Host), "www.")
	u.Fragment, u.RawFragment = "", ""
	u.User = nil
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(strings.ToLower(k), "utm_") || trackingParams[strings.ToLower(k)] {
			q.Del(k)
		}
	}
	u.RawQuery = q.Encode()
	if len(u.Path) > 1 {
		u.Path = strings.TrimRight(u.Path, "/")
		u.RawPath = ""
	}
	if u.Path == "/" {
		u.Path = ""
	}
	return u.String(), nil
}

// Site is the host a URL names, without "www." and port: what counts as one domain.
func Site(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

type urlError string

func (e urlError) Error() string { return string(e) }

const errBadURL = urlError("the url must be an absolute http or https URL")

// foldText reduces prose to what a quote must match: letters and digits, lower-case, every other run of characters one space.
// Entities are decoded and Markdown marks drop out with the punctuation, so a quote copied from the rendered page and the page as
// web_fetch converted it fold to the same words.
func foldText(s string) string {
	s = html.UnescapeString(s)
	var b strings.Builder
	b.Grow(len(s))
	space := true
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
			space = false
			continue
		}
		if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

// foldCode reduces code to what a snippet must match: every character but white space, so indentation and line wrapping do not
// matter and every symbol does.
func foldCode(s string) string {
	s = html.UnescapeString(s)
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if !unicode.IsSpace(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ellipsis splits a quote at its elisions ("...", "…", "[...]").
func ellipsis(q string) []string {
	q = strings.NewReplacer("[...]", "\x00", "[…]", "\x00", "...", "\x00", "…", "\x00").Replace(q)
	var parts []string
	for _, p := range strings.Split(q, "\x00") {
		if strings.TrimSpace(p) != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// matchQuote reports whether quote appears in page as whole words, its elided parts in order, with at least minQuoteRunes letters and digits. On a miss, near is the passage of the page that
// shares the longest run of the quote's first words, for the model to copy from.
func matchQuote(page, quote string) (ok bool, near string) {
	fp := foldText(page)
	padded := " " + fp + " " // a part matches whole words: "safe for ..." is not found in "unsafe for ..."
	parts := ellipsis(quote)
	if len(parts) == 0 {
		return false, ""
	}
	at, matched := 0, 0
	for _, part := range parts {
		fq := foldText(part)
		if fq == "" {
			continue
		}
		i := strings.Index(padded[at:], " "+fq+" ")
		if i < 0 {
			return false, nearPassage(page, fp, fq)
		}
		at += i + len(fq) + 1 // the space after the part is the next part's leading one
		matched += runes(fq)
	}
	// the letters and digits matched must be evidence themselves: punctuation or one common word proves nothing
	return matched >= minQuoteRunes, ""
}

// matchCode reports whether code appears on page, white space aside.
func matchCode(page, code string) bool {
	fc := foldCode(code)
	return fc != "" && strings.Contains(foldCode(page), fc)
}

// nearPassage finds where the page shares the longest run of the folded quote's first words and returns the page's own words
// there, about as long as the quote.
func nearPassage(page, fp, fq string) string {
	words := strings.Fields(fq)
	best := ""
	for n := len(words); n >= 3; n-- {
		probe := strings.Join(words[:n], " ")
		if i := strings.Index(fp, probe); i >= 0 {
			best = excerptAround(fp, i, len(fq)+40)
			break
		}
		if n > 12 {
			n = 13 // long quotes: try 12, 11, ... rather than every prefix
		}
	}
	if best == "" && len(words) >= 6 {
		// the start is wrong: try runs from the middle of the quote
		mid := words[len(words)/2:]
		for n := len(mid); n >= 4; n-- {
			if i := strings.Index(fp, strings.Join(mid[:n], " ")); i >= 0 {
				best = excerptAround(fp, max(0, i-len(fq)/2), len(fq)+40)
				break
			}
		}
	}
	return best
}

// excerptAround cuts n bytes of folded text from i, on word boundaries.
func excerptAround(fp string, i, n int) string {
	end := min(len(fp), i+n)
	for end < len(fp) && fp[end] != ' ' {
		end++
	}
	for i > 0 && fp[i-1] != ' ' {
		i--
	}
	return strings.TrimSpace(fp[i:end])
}

// cleanQuote trims a quote to one line of text, as it is kept.
func cleanQuote(q string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(q)), " ")
}

// runes counts the runes of s.
func runes(s string) int { return utf8.RuneCountInString(s) }

// clipText limits s to n runes, marking a cut with "…".
func clipText(s string, n int) string {
	if runes(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}
