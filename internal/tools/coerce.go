package tools

import (
	"bytes"
	"encoding/json"
	"strings"
)

// CoerceLists returns in with each top-level argument that schema declares a list of strings, but that came as one string, made
// into the list it stands for: a string that holds a JSON array of strings ("[\"a.go\", \"b.go\"]") is that array, an empty
// string is an empty list, and any other string is a list of that one string. Models often send a list of one as the item
// itself, or the list as its JSON text, and a call refused for it costs a whole request. A string is never split, because
// what separates items is the tool's to know (a glob holds commas, a path spaces). changed reports whether anything was made;
// when it is false, in is returned as it was.
func CoerceLists(schema, in json.RawMessage) (out json.RawMessage, changed bool) {
	if len(bytes.TrimSpace(in)) == 0 || !bytes.Contains(schema, []byte(`"array"`)) {
		return in, false
	}
	var s struct {
		Properties map[string]struct {
			Type  any `json:"type"`
			Items struct {
				Type any `json:"type"`
			} `json:"items"`
		} `json:"properties"`
	}
	if json.Unmarshal(schema, &s) != nil || len(s.Properties) == 0 {
		return in, false
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(in, &m) != nil {
		return in, false
	}
	for k, v := range m {
		p, ok := s.Properties[k]
		if !ok || p.Type != "array" || p.Items.Type != "string" {
			continue
		}
		var one string
		if json.Unmarshal(v, &one) != nil {
			continue
		}
		list := []string{}
		if t := strings.TrimSpace(one); strings.HasPrefix(t, "[") {
			if json.Unmarshal([]byte(t), &list) != nil {
				list = []string{one}
			}
		} else if t != "" {
			list = []string{one}
		}
		b, err := json.Marshal(list)
		if err != nil {
			continue
		}
		m[k], changed = b, true
	}
	if !changed {
		return in, false
	}
	b, err := json.Marshal(m)
	if err != nil {
		return in, false
	}
	return b, true
}
