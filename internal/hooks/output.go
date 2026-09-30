package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/skills/mdfile"
)

const (
	// maxReason caps the reason one hook may give.
	maxReason = 2 << 10
	// truncMark ends text that was cut to a cap.
	truncMark = "\n[... hook output truncated ...]"
)

// cleanText makes hook output fit to show a person or a model: invalid UTF-8
// is replaced, terminal escape sequences (colours, cursor movement, OSC titles
// and clipboard writes) are removed whole rather than left as debris, and
// control characters and hidden Unicode are dropped. A hook writes for a
// terminal; the model reads bytes.
func cleanText(b []byte) string {
	s := stripEscapes(strings.ToValidUTF8(string(b), "�"))
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	s, _ = mdfile.Sanitize(s)
	return s
}

// stripEscapes removes ANSI escape sequences: CSI ("ESC [ ... final"), OSC and
// the other string sequences ("ESC ] ... BEL/ST"), and two-byte escapes.
func stripEscapes(s string) string {
	if strings.IndexByte(s, 0x1b) < 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c != 0x1b {
			b.WriteByte(c)
			i++
			continue
		}
		i++
		if i >= len(s) {
			break
		}
		switch s[i] {
		case '[': // CSI: parameters and intermediates, then a final byte 0x40-0x7e
			i++
			for n := 0; i < len(s) && n < 64; n++ {
				f := s[i]
				i++
				if f >= 0x40 && f <= 0x7e {
					break
				}
			}
		case ']', 'P', 'X', '^', '_': // string sequences end at BEL or ESC \
			i++
			for n := 0; i < len(s) && n < 4096; n++ {
				if s[i] == 0x07 {
					i++
					break
				}
				if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		default: // a two-byte escape such as ESC c
			i++
		}
	}
	return b.String()
}

// capText limits s to max bytes, on a rune boundary, and says so.
func capText(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 {
		if r, size := utf8.DecodeLastRuneInString(cut); r == utf8.RuneError && size <= 1 {
			cut = cut[:len(cut)-1] // the cap fell inside a character
			continue
		}
		break
	}
	return strings.TrimRight(cut, " \t\n") + truncMark
}

// parsed is the meaning of one hook's output.
type parsed struct {
	decision   Decision // permission answer (PreToolUse, PermissionRequest)
	reason     string
	block      bool // the event-level block ("decision": "block")
	updated    json.RawMessage
	context    string
	stop       bool
	stopReason string
	message    string
	problems   []string // fields that were present but unusable
}

// parseOutput interprets what a hook printed on stdout when it exited 0. ok is
// false when the text is not a JSON object, in which case the caller treats it
// as plain text. err is set when it looks like JSON but is not.
//
// It accepts both shapes Claude Code documents: the current
// {"hookSpecificOutput": {"permissionDecision": ..., "updatedInput": ...,
// "additionalContext": ...}} and the older top-level {"decision": ...,
// "reason": ...}, plus continue/stopReason/systemMessage/suppressOutput.
func parseOutput(event string, stdout []byte) (p parsed, ok bool, err error) {
	t := bytes.TrimSpace(stdout)
	if len(t) == 0 || t[0] != '{' {
		return parsed{}, false, nil
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(t, &top); err != nil || top == nil {
		return parsed{}, false, fmt.Errorf("stdout starts like JSON but does not parse: %v", jsonProblem(err))
	}
	special := map[string]json.RawMessage{}
	if raw, has := top["hookSpecificOutput"]; has {
		if err := json.Unmarshal(raw, &special); err != nil {
			p.problems = append(p.problems, "hookSpecificOutput is not an object")
			special = map[string]json.RawMessage{}
		}
	}
	str := func(key string, from ...map[string]json.RawMessage) (string, bool) {
		for _, m := range from {
			raw, has := m[key]
			if !has || isNull(raw) {
				continue
			}
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				p.problems = append(p.problems, key+" is not a string")
				continue
			}
			return s, true
		}
		return "", false
	}

	// Decision. hookSpecificOutput wins over the top level.
	word, _ := str("permissionDecision", special, top)
	if word == "" {
		if raw, has := top["decision"]; has {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				word = s
			}
		}
	}
	reason, _ := str("permissionDecisionReason", special, top)
	if reason == "" {
		reason, _ = str("reason", top)
	}
	updated := firstRaw("updatedInput", special, top)

	// PermissionRequest hooks answer with a decision object.
	if raw, has := special["decision"]; has && event == PermissionRequest {
		var d struct {
			Behavior     string          `json:"behavior"`
			Message      string          `json:"message"`
			UpdatedInput json.RawMessage `json:"updatedInput"`
		}
		if err := json.Unmarshal(raw, &d); err != nil {
			p.problems = append(p.problems, "decision is not an object")
		} else {
			if d.Behavior != "" {
				word = d.Behavior
			}
			if d.Message != "" && reason == "" {
				reason = d.Message
			}
			if len(d.UpdatedInput) > 0 && updated == nil {
				updated = d.UpdatedInput
			}
		}
	}

	switch strings.ToLower(strings.TrimSpace(word)) {
	case "allow", "approve":
		if hasDecision(event) {
			p.decision = Allow
		}
	case "ask":
		if hasDecision(event) {
			p.decision = Ask
		}
	case "deny", "block":
		switch {
		case hasDecision(event):
			p.decision = Deny
		case exit2Blocks(event):
			p.block = true
		}
	}
	p.reason = capText(cleanText([]byte(reason)), maxReason)

	if len(updated) > 0 && !isNull(updated) {
		if u := bytes.TrimSpace(updated); len(u) > 0 && u[0] == '{' && json.Valid(u) {
			p.updated = append(json.RawMessage(nil), u...)
		} else {
			p.problems = append(p.problems, "updatedInput is not a JSON object")
		}
	}
	if ctxText, has := str("additionalContext", special, top); has {
		p.context = cleanText([]byte(ctxText))
	}
	if raw, has := top["continue"]; has {
		var c bool
		if err := json.Unmarshal(raw, &c); err != nil {
			p.problems = append(p.problems, "continue is not true or false")
		} else if !c {
			p.stop = true
		}
	}
	if sr, has := str("stopReason", top); has {
		p.stopReason = capText(cleanText([]byte(sr)), maxReason)
	}
	if sm, has := str("systemMessage", top); has {
		p.message = capText(cleanText([]byte(sm)), maxReason)
	}
	return p, true, nil
}

// firstRaw returns the first non-null value of key in the maps.
func firstRaw(key string, from ...map[string]json.RawMessage) json.RawMessage {
	for _, m := range from {
		if raw, has := m[key]; has && !isNull(raw) {
			return raw
		}
	}
	return nil
}
