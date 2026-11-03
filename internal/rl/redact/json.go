package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// JSON redacts the string values of a JSON document. Keys, numbers, literals and
// layout are left exactly as they were: only the bytes of a string value that
// redaction changes are rewritten, so a document with nothing to redact comes back
// byte for byte, and one with a secret differs only at the secret. Input that is
// not valid JSON is returned unchanged.
func (r *Redactor) JSON(raw json.RawMessage) json.RawMessage { return r.jsonDoc(raw, false) }

// jsonDoc is JSON; with keys, an object key that redaction changes is rewritten too (a document inside a string, where a key is
// text that a scan of the string would have read).
func (r *Redactor) jsonDoc(raw json.RawMessage, keys bool) json.RawMessage {
	if len(raw) == 0 || !json.Valid(raw) {
		return raw
	}
	n := len(raw)
	var out []byte
	last := 0
	// arrays holds, for each open array or object, the key an array is the value of ("" for an object or an
	// anonymous array), so an element of {"Authorization": ["Basic ..."]} keeps its header name.
	var arrays []string
	pending := ""
	for i := 0; i < n; {
		if raw[i] != '"' {
			switch raw[i] {
			case '[':
				arrays = append(arrays, pending)
				pending = ""
			case '{':
				arrays = append(arrays, "")
				pending = ""
			case ']', '}':
				if len(arrays) > 0 {
					arrays = arrays[:len(arrays)-1]
				}
			}
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
		if end > n {
			i = end
			continue
		}
		if isJSONKey(raw, end) {
			pending = ""
			if err := json.Unmarshal(raw[i:end], &pending); err != nil {
				pending = ""
			}
			if keys && pending != "" {
				if red, changed := r.Changed(pending); changed {
					if out == nil {
						out = make([]byte, 0, n+64)
					}
					out = append(out, raw[last:i]...)
					out = append(out, encodeString(red)...)
					last = end
				}
			}
			i = end
			continue
		}
		pending = ""
		var s string
		if escaped {
			if err := json.Unmarshal(raw[i:end], &s); err != nil {
				i = end
				continue
			}
		} else {
			s = string(raw[i+1 : j])
		}
		elemKey := ""
		if len(arrays) > 0 {
			elemKey = arrays[len(arrays)-1]
		}
		red, changed := "", false
		if nested, ok := r.nestedJSON(s); ok {
			red, changed = nested, nested != s
		} else {
			red, changed = r.jsonValue(raw, i, s, elemKey)
		}
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

// nestedJSON is s redacted as JSON when s is itself a JSON object or array (the arguments of a tool call in a provider's wire
// format are a JSON document inside a string): its keys then count, and a value it escapes (a private key's newlines) is read
// decoded, and its keys are redacted as text. It reports false for any other string, which is redacted as text. Each level of
// nesting escapes the one inside, so the depth is bounded by the logarithm of the length.
func (r *Redactor) nestedJSON(s string) (string, bool) {
	t := strings.Trim(s, " \t\r\n") // JSON's own blanks only: the bytes judged are the bytes redacted
	if len(t) < 2 || (t[0] != '{' || t[len(t)-1] != '}') && (t[0] != '[' || t[len(t)-1] != ']') || !json.Valid([]byte(s)) {
		return s, false
	}
	red := string(r.jsonDoc(json.RawMessage(s), true))
	if strings.Contains(red, "-----BEGIN") && pemFullRE.MatchString(red) {
		// a private key split across values (["-----BEGIN ... KEY-----", "MII...", "-----END ... KEY-----"]) is in none of them:
		// the document is redacted as text, as a scan of the whole string reads the key from BEGIN to END
		if out, changed := r.Changed(s); changed {
			return out, true
		}
	}
	return red, true
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
// value alone. The key itself is never modified. arrayKey is the key of the
// array s is an element of, if any.
func (r *Redactor) jsonValue(raw []byte, start int, s, arrayKey string) (string, bool) {
	key := keyBefore(raw, start)
	if r.enabled[KindSecret] && len(s) >= 12 && !tokenRE.MatchString(s) {
		if key != "" && secretKeyRE.MatchString(key) && kvValueOK(s, true, key, ":") && !r.allowed(s, s) {
			r.mu.Lock()
			r.stats[KindSecret]++
			r.mu.Unlock()
			return r.token(KindSecret, s), true
		}
	}
	header := key
	if header == "" {
		header = arrayKey // headers serialized as lists: {"Authorization": ["Basic ..."]}
	}
	if head, ok := authHeads[strings.ToLower(header)]; ok {
		// The header rules read "Authorization: Basic ..." as one line; in JSON the name is the key, so it is put back in
		// front of the value for them, in the spelling the rules recognize.
		if out, ok := r.Changed(head + s); ok && strings.HasPrefix(out, head) {
			return strings.TrimPrefix(out, head), true
		}
	}
	return r.Changed(s)
}

// authHeads maps the lower-cased credential header names to the line prefix the
// header rules recognize.
var authHeads = map[string]string{
	"authorization":       "Authorization: ",
	"proxy-authorization": "Proxy-Authorization: ",
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

// isJSONSpace recognizes exactly the four whitespace bytes permitted by JSON.
func isJSONSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\r' || b == '\n' }
