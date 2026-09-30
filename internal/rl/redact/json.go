package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
)

// JSON redacts the string values of a JSON document. Keys, numbers, literals and
// layout are left exactly as they were: only the bytes of a string value that
// redaction changes are rewritten, so a document with nothing to redact comes back
// byte for byte, and one with a secret differs only at the secret. Input that is
// not valid JSON is returned unchanged.
func (r *Redactor) JSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return raw
	}
	n := len(raw)
	var out []byte
	last := 0
	for i := 0; i < n; {
		if raw[i] != '"' {
			i++
			continue
		}
		j := i + 1
		escaped := false
		for j < n && raw[j] != '"' {
			if raw[j] == '\\' {
				escaped = true
				j++
			}
			j++
		}
		end := j + 1 // one past the closing quote
		if end > n || isJSONKey(raw, end) {
			i = end
			continue
		}
		var s string
		if escaped {
			if err := json.Unmarshal(raw[i:end], &s); err != nil {
				i = end
				continue
			}
		} else {
			s = string(raw[i+1 : j])
		}
		red, changed := r.jsonValue(raw, i, s)
		if changed {
			if out == nil {
				out = make([]byte, 0, n+64)
			}
			out = append(out, raw[last:i]...)
			out = append(out, encodeString(red)...)
			last = end
		}
		i = end
	}
	if out == nil {
		return raw
	}
	return append(out, raw[last:]...)
}

// isJSONKey reports whether the string ending just before position end is an
// object key, i.e. the next significant byte is a colon. The document is known
// to be valid, so a string followed by a colon can only be a key.
func isJSONKey(raw []byte, end int) bool {
	for k := end; k < len(raw); k++ {
		switch raw[k] {
		case ' ', '\t', '\r', '\n':
			continue
		case ':':
			return true
		default:
			return false
		}
	}
	return false
}

// encodeString renders s as a JSON string without HTML escaping, so the
// replacement tokens and code punctuation stay readable.
func encodeString(s string) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return bytes.TrimRight(buf.Bytes(), "\n")
}

// secretKeyRE matches object keys that name a secret, by the same rule as the
// key = value detector: the keyword must end the name.
var secretKeyRE = regexp.MustCompile(`(?i)` + kvKeyword + `$`)

// jsonValue redacts one string value. On top of the text rules it uses the
// object key as context, because in JSON the key and the value are separate
// strings: {"db_password": "hunter2hunter2"} has nothing secret-looking in the
// value alone. The key itself is never modified.
func (r *Redactor) jsonValue(raw []byte, start int, s string) (string, bool) {
	if r.enabled[KindSecret] && len(s) >= 12 && !tokenRE.MatchString(s) {
		if key := keyBefore(raw, start); key != "" && secretKeyRE.MatchString(key) && kvValueOK(s, true, key, ":") && !r.allowed(s, s) {
			r.mu.Lock()
			r.stats[KindSecret]++
			r.mu.Unlock()
			return r.token(KindSecret, s), true
		}
	}
	return r.Changed(s)
}

// keyBefore returns the object key whose value starts at raw[start], or "" when
// the string is an array element or a top-level value.
func keyBefore(raw []byte, start int) string {
	k := start - 1
	for k >= 0 && isJSONSpace(raw[k]) {
		k--
	}
	if k < 0 || raw[k] != ':' {
		return ""
	}
	k--
	for k >= 0 && isJSONSpace(raw[k]) {
		k--
	}
	if k < 0 || raw[k] != '"' {
		return ""
	}
	closing := k
	for k--; k >= 0; k-- {
		if raw[k] != '"' {
			continue
		}
		bs := 0
		for m := k - 1; m >= 0 && raw[m] == '\\'; m-- {
			bs++
		}
		if bs%2 == 0 {
			break
		}
	}
	if k < 0 {
		return ""
	}
	var key string
	if err := json.Unmarshal(raw[k:closing+1], &key); err != nil {
		return ""
	}
	return key
}

func isJSONSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }
