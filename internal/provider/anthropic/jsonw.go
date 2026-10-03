package anthropic

import (
	"strconv"
	"unicode/utf8"
)

// jw is a tiny append-only JSON writer.
//
// Bodies are written by hand rather than marshalled from structs for one
// reason: raw values (tool schemas, tool_use input, thinking blocks replayed
// from Block.Wire) must reach the wire byte for byte. encoding/json compacts
// every json.RawMessage it emits, which is harmless for canonical input but
// would silently rewrite anything else, and a rewritten thinking block or tool
// schema is exactly the kind of "helpful" normalisation that destroys a prefix
// cache or a preserved-thinking binding.
type jw struct{ b []byte }

// raw appends already-encoded bytes to the JSON output without validation or escaping.
func (w *jw) raw(p []byte) { w.b = append(w.b, p...) }

// lit appends a JSON literal verbatim; the caller supplies valid JSON syntax.
func (w *jw) lit(s string) { w.b = append(w.b, s...) }

// putc appends one syntax byte to the JSON output buffer.
func (w *jw) putc(c byte) { w.b = append(w.b, c) }

// str appends a quoted and escaped JSON string to the output buffer.
func (w *jw) str(s string) { w.b = appendJSONString(w.b, s) }

// num appends an integer in base ten without allocating an intermediate string.
func (w *jw) num(n int) { w.b = strconv.AppendInt(w.b, int64(n), 10) }

// bytes returns the writer's current backing slice without copying it.
func (w *jw) bytes() []byte { return w.b }

// member starts an object member; first says whether it is the object's first.
func (w *jw) member(first bool, key string) {
	if !first {
		w.putc(',')
	}
	w.str(key)
	w.putc(':')
}

const hexDigits = "0123456789abcdef"

// appendJSONString appends s as a JSON string without HTML escaping (so "<"
// stays "<", matching core.MarshalStable). Invalid UTF-8 becomes U+FFFD and
// U+2028/U+2029 are escaped, exactly as encoding/json does with HTML escaping
// off.
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if c >= 0x20 && c != '"' && c != '\\' {
				i++
				continue
			}
			dst = append(dst, s[start:i]...)
			switch c {
			case '"', '\\':
				dst = append(dst, '\\', c)
			case '\n':
				dst = append(dst, '\\', 'n')
			case '\r':
				dst = append(dst, '\\', 'r')
			case '\t':
				dst = append(dst, '\\', 't')
			case '\b':
				dst = append(dst, '\\', 'b')
			case '\f':
				dst = append(dst, '\\', 'f')
			default:
				dst = append(dst, '\\', 'u', '0', '0', hexDigits[c>>4], hexDigits[c&0xf])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, "\\"+"ufffd"...)
			i += size
			start = i
			continue
		}
		if r == 0x2028 || r == 0x2029 {
			dst = append(dst, s[start:i]...)
			dst = append(dst, '\\', 'u', '2', '0', '2', hexDigits[r&0xf])
			i += size
			start = i
			continue
		}
		i += size
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}
