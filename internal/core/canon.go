package core

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Canonical re-encodes JSON with sorted object keys, no insignificant
// whitespace and no HTML escaping, so equal documents always yield equal
// bytes. Prompt caching is a byte-prefix match; anything that feeds the prefix
// (tool schemas above all) must pass through here.
func Canonical(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("canonical json: %w", err)
	}
	return marshalNoEscape(v)
}

// MustCanonical is Canonical for values known to be valid JSON.
func MustCanonical(raw json.RawMessage) json.RawMessage {
	out, err := Canonical(raw)
	if err != nil {
		panic(err)
	}
	return out
}

// MarshalStable marshals v without HTML escaping. Go already sorts map keys
// and emits struct fields in declaration order, so the output is stable for
// stable inputs.
func MarshalStable(v any) ([]byte, error) { return marshalNoEscape(v) }

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return replacementAsCharacter(bytes.TrimRight(buf.Bytes(), "\n")), nil
}

// replacementEscape is how the encoding/json of Go 1.26 and before wrote each byte of invalid UTF-8 in a string. Go 1.27's writes
// the character itself (the bytes EF BF BD), as every version does for a U+FFFD that was in the string to begin with. These bytes
// are hashed into cache keys and replay checks, so they must not depend on the Go version that built the binary: the character is
// the canonical form (Canonical, which decodes first, has always produced it), and an escape that an older Go wrote is rewritten.
const replacementEscape = `\ufffd`

// replacementAsCharacter rewrites every \ufffd escape of b as the character. It reads b as escapes, each taken whole, so that the
// backslash of a string that holds a backslash followed by the letters "ufffd" (written \\ufffd) is not mistaken for the start of one.
func replacementAsCharacter(b []byte) []byte {
	if !bytes.Contains(b, []byte(replacementEscape)) {
		return b
	}
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		if b[i] != '\\' {
			out = append(out, b[i])
			i++
			continue
		}
		if bytes.HasPrefix(b[i:], []byte(replacementEscape)) {
			out = append(out, "\xef\xbf\xbd"...)
			i += len(replacementEscape)
			continue
		}
		out = append(out, b[i])
		if i+1 < len(b) {
			out = append(out, b[i+1])
		}
		i += 2
	}
	return out
}

// Hash is a content hash rendered as lowercase hex.
type Hash string

// HashBytes returns the SHA-256 of b.
func HashBytes(b []byte) Hash {
	sum := sha256.Sum256(b)
	return Hash(hex.EncodeToString(sum[:]))
}

// HashString returns the SHA-256 of s.
func HashString(s string) Hash { return HashBytes([]byte(s)) }

// Short is a 12-character prefix suitable for logs and one-line summaries.
func (h Hash) Short() string {
	if len(h) < 12 {
		return string(h)
	}
	return string(h[:12])
}
