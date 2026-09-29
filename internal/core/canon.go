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
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
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
