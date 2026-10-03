package web

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/tools"
)

type contentKind int

const (
	kindUnsupported contentKind = iota
	kindHTML
	kindJSON
	kindText
)

// textualApps are application/* types that are really text.
var textualApps = map[string]bool{
	"application/xml": true, "application/javascript": true, "application/x-javascript": true,
	"application/ecmascript": true, "application/x-yaml": true, "application/yaml": true,
	"application/toml": true, "application/x-sh": true, "application/sql": true,
	"application/x-ndjson": true, "application/graphql": true, "application/x-httpd-php": true,
}

func classifyType(mt string) contentKind {
	switch {
	case mt == "text/html" || mt == "application/xhtml+xml":
		return kindHTML
	case mt == "application/json" || mt == "text/json" || strings.HasSuffix(mt, "+json"):
		return kindJSON
	case strings.HasPrefix(mt, "text/") || textualApps[mt] || strings.HasSuffix(mt, "+xml"):
		return kindText
	}
	return kindUnsupported
}

// convert turns a response body into a document, or explains why it cannot.
//
// The bytes come from the open internet, and a panic in a tool takes down every
// agent of the session, so a bug in any converter degrades into an error result
// instead.
func (f *fetcher) convert(body []byte, hdr http.Header, final *url.URL) (doc *document, err error) {
	defer func() {
		if r := recover(); r != nil {
			doc, err = nil, fmt.Errorf("the page could not be converted to text (internal error: %v)", r)
		}
	}()
	mt, params, _ := mime.ParseMediaType(hdr.Get("Content-Type"))
	mt = strings.ToLower(mt)
	sniffed := false
	switch mt {
	case "", "application/octet-stream", "binary/octet-stream", "unknown/unknown":
		// A missing or useless type: look at the bytes, as browsers do.
		mt, _, _ = mime.ParseMediaType(http.DetectContentType(body[:min(len(body), 512)]))
		sniffed = true
	}
	kind := classifyType(mt)
	if sniffed && kind == kindText && looksJSON(body) {
		mt, kind = "application/json", kindJSON
	}
	if kind == kindUnsupported {
		return nil, &unsupportedError{ctype: mt}
	}

	text := decodeText(body, params["charset"], kind == kindHTML)
	doc = &document{ctype: mt}
	switch kind {
	case kindHTML:
		doc.text, doc.title = htmlToText(text, final)
	case kindJSON:
		doc.text = prettyJSON(text)
	default:
		doc.text = cleanText(text)
	}
	doc.text = stripInvisible(doc.text)
	doc.title = stripInvisible(doc.title)
	doc.runes = utf8.RuneCountInString(doc.text)
	return doc, nil
}

// stripInvisible removes characters that display as nothing (or only reorder
// text) but reach the model intact: Unicode tag characters, bidirectional
// overrides and isolates, zero-width spaces and word joiners. They are the
// standard carrier for hidden instructions in a fetched page ("ASCII
// smuggling"), which no human reviewing the transcript would ever see.
// Zero-width (non-)joiners and the left/right marks stay: emoji sequences and
// several scripts need them.
func stripInvisible(s string) string {
	hidden := false
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			hidden = true
			break
		}
	}
	if !hidden {
		return s
	}
	return strings.Map(func(r rune) rune {
		if invisible(r) {
			return -1
		}
		return r
	}, s)
}

// invisible uses the shared tool-output classification for invisible Unicode characters.
func invisible(r rune) bool { return tools.Invisible(r) }

// looksJSON accepts syntactically valid JSON arrays and objects, excluding scalar JSON values.
func looksJSON(body []byte) bool {
	t := bytes.TrimSpace(body)
	return len(t) > 0 && (t[0] == '{' || t[0] == '[') && json.Valid(t)
}

// prettyJSON indents valid JSON, preserving key order and number spelling
// (re-encoding through a map would lose both); anything else is returned as text.
func prettyJSON(s string) string {
	s = strings.TrimPrefix(s, string(rune(0xFEFF)))
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", "  "); err != nil {
		return cleanText(s)
	}
	return strings.TrimSpace(buf.String()) // Indent keeps whatever surrounded the value
}

// cleanText makes text safe to hand to a model: LF line endings, no control
// characters other than tab and newline. s must already be valid UTF-8.
func cleanText(s string) string {
	dirty := false
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 && c != '\n' && c != '\t' || c == 0x7f {
			dirty = true
			break
		}
	}
	if !dirty {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\r':
			if i+1 < len(s) && s[i+1] == '\n' {
				continue
			}
			sb.WriteByte('\n')
		case c < 0x20 && c != '\n' && c != '\t', c == 0x7f:
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}

var metaCharsetRe = regexp.MustCompile(`(?i)<meta[^>]+charset\s*=\s*["']?\s*([A-Za-z0-9_.:-]+)`)

// decodeText converts a body to UTF-8. golang.org/x/text (needed by
// x/net/html/charset) is not a dependency of this module, so this covers what
// the web actually serves: UTF-8, UTF-16 with a BOM, and the Latin-1 family
// (which browsers treat as windows-1252). Other charsets are decoded as UTF-8
// with replacement characters, which keeps ASCII-heavy pages readable.
func decodeText(b []byte, label string, isHTML bool) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return toValidUTF8(b[3:])
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return decodeUTF16(b[2:], binary.LittleEndian)
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return decodeUTF16(b[2:], binary.BigEndian)
	}
	label = strings.ToLower(strings.TrimSpace(label))
	if label == "" && isHTML {
		head := b[:min(len(b), 4096)]
		if m := metaCharsetRe.FindSubmatch(head); m != nil {
			label = strings.ToLower(string(m[1]))
		}
	}
	switch label {
	case "utf-8", "utf8":
		return toValidUTF8(b)
	case "utf-16le":
		return decodeUTF16(b, binary.LittleEndian)
	case "utf-16be", "utf-16":
		return decodeUTF16(b, binary.BigEndian)
	case "windows-1252", "cp1252", "iso-8859-1", "iso8859-1", "iso_8859-1", "latin1", "l1", "us-ascii", "ascii":
		return decodeWindows1252(b)
	case "":
		if utf8.Valid(b) {
			return string(b)
		}
		return decodeWindows1252(b) // undeclared and not UTF-8: the web's legacy default
	}
	return toValidUTF8(b)
}

// toValidUTF8 replaces invalid UTF-8 sequences with the Unicode replacement character.
func toValidUTF8(b []byte) string { return strings.ToValidUTF8(string(b), "�") }

// decodeUTF16 decodes complete byte pairs in the supplied endianness and ignores a trailing
// unpaired byte.
func decodeUTF16(b []byte, order binary.ByteOrder) string {
	u := make([]uint16, 0, len(b)/2)
	for i := 0; i+1 < len(b); i += 2 {
		u = append(u, order.Uint16(b[i:]))
	}
	return string(utf16.Decode(u))
}

// windows1252High maps bytes 0x80-0x9F; the rest of Latin-1 is identity.
var windows1252High = [32]rune{
	0x20AC, 0x0081, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x008D, 0x017D, 0x008F,
	0x0090, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x009D, 0x017E, 0x0178,
}

func decodeWindows1252(b []byte) string {
	ascii := true
	for _, c := range b {
		if c >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b) + len(b)/4)
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xA0:
			sb.WriteRune(windows1252High[c-0x80])
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}
