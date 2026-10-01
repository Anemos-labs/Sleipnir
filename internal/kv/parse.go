package kv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// ParsePatch extracts and validates the compactor's patch from its reply.
//
// A reply is model output, and models wrap JSON in prose and code fences, quote what
// they read (a tool result may itself contain a patch-shaped object), and leave stray
// braces in their sentences. The rules that make that safe are fixed and simple:
//
//   - a candidate is a syntactically valid JSON object that has a "keep_from" key,
//     found anywhere in the reply; text between candidates, braces that do not start
//     a JSON object and objects without "keep_from" are skipped, so a stray "{my}"
//     costs nothing;
//   - a candidate in a fenced block that may hold JSON (no language, or json) beats
//     one outside a fence;
//   - among the preferred candidates the LAST one wins: the answer comes after
//     whatever the model quoted on the way to it;
//   - the chosen candidate is then held to the full schema, and a candidate that
//     fails is an error, never a reason to fall back to an older object: a broken
//     answer is refused (the caller falls back to a mechanical patch) rather than
//     replaced by something quoted earlier.
//
// Structural problems are errors; questionable content becomes warnings so one bad
// entry does not discard an otherwise good patch. Nothing here trusts the content: Apply
// vets every field against the agent's real thread.
func ParsePatch(reply string) (*Patch, error) {
	c, n, err := chooseCandidate(reply)
	if err != nil {
		return nil, err
	}
	rp, warns, err := decodeRaw(c)
	if err != nil {
		return nil, err
	}
	p, err := buildPatch(rp)
	if err != nil {
		return nil, err
	}
	p.Warnings = append(warns, p.Warnings...)
	if n > 1 {
		p.Warnings = append(p.Warnings, fmt.Sprintf("the reply held %d patch objects; the last preferred one was used", n))
	}
	return p, nil
}

// decodeRaw decodes the object field by field. A syntax error refuses the patch, as it always did. A field of the wrong type costs
// that field, and says so in a warning, instead of the whole patch: on real models "notes" arrived as one object instead of a list,
// "keep_from" as a number, a spine entry as a string, and each of those patches was refused for it, the agent compacting mechanically
// instead of with the model's own digest of its work. Nothing here trusts the content either way: Apply vets every field.
func decodeRaw(c []byte) (*rawPatch, []string, error) {
	m, err := objectFields(c)
	if err != nil {
		return nil, nil, fmt.Errorf("compaction patch is not valid JSON: %w", err)
	}
	rp := &rawPatch{}
	var warns []string
	warn := func(format string, a ...any) { warns = append(warns, fmt.Sprintf(format, a...)) }

	if b, ok := m["keep_from"]; ok && !isJSONNull(b) {
		switch s, ok := jsonScalar(b); {
		case ok:
			rp.KeepFrom = s
		default:
			return nil, nil, fmt.Errorf("keep_from: want a turn id such as \"t41\", got %s", cutRunes(string(b), 40))
		}
	}
	for i, b := range jsonElements(m["spine"]) {
		var e map[string]json.RawMessage
		if json.Unmarshal(b, &e) != nil {
			warn("spine[%d]: not an object, ignored", i)
			continue
		}
		turns, _ := jsonScalar(e["turns"])
		line, _ := jsonScalar(e["line"])
		rp.Spine = append(rp.Spine, struct {
			Turns string `json:"turns"`
			Line  string `json:"line"`
		}{turns, line})
	}
	for i, b := range jsonElements(m["mask"]) {
		if s, ok := jsonScalar(b); ok {
			rp.Mask = append(rp.Mask, s)
			continue
		}
		warn("mask[%d]: not a reference, ignored", i)
	}
	for i, b := range jsonElements(m["notes"]) {
		var n NoteOp
		if json.Unmarshal(b, &n) != nil {
			warn("notes[%d]: not an operation, ignored", i)
			continue
		}
		rp.Notes = append(rp.Notes, n)
	}
	for i, b := range jsonElements(m["promote"]) {
		var pr Promotion
		if json.Unmarshal(b, &pr) != nil {
			warn("promote[%d]: not a proposal, ignored", i)
			continue
		}
		rp.Promote = append(rp.Promote, pr)
	}
	for _, f := range []string{"spine", "mask", "notes", "promote"} {
		if b, ok := m[f]; ok && !isJSONNull(b) && len(jsonElements(b)) == 0 && !isEmptyJSONList(b) {
			warn("%s: not a list, ignored", f)
		}
	}
	return rp, warns, nil
}

// objectFields is the members of a JSON object by lower-cased name, as encoding/json matches field names to a struct's: without
// regard to case, and the last of two members of one name wins.
func objectFields(c []byte) (map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(c))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		if err == nil {
			err = errors.New("not an object")
		}
		return nil, err
	}
	m := map[string]json.RawMessage{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := kt.(string)
		if !ok {
			return nil, errors.New("not an object")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		m[strings.ToLower(key)] = raw
	}
	return m, nil
}

func isJSONNull(b json.RawMessage) bool { return strings.TrimSpace(string(b)) == "null" }

func isEmptyJSONList(b json.RawMessage) bool {
	var l []json.RawMessage
	return json.Unmarshal(b, &l) == nil && len(l) == 0
}

// jsonElements is a field's list, or the one value it is when a model wrote an object where a list belongs; nothing for a null, an
// absent field or a value that is neither (a string, a number).
func jsonElements(b json.RawMessage) []json.RawMessage {
	b = json.RawMessage(strings.TrimSpace(string(b)))
	if len(b) == 0 {
		return nil
	}
	switch b[0] {
	case '[':
		var l []json.RawMessage
		if json.Unmarshal(b, &l) != nil {
			return nil
		}
		return l
	case '{':
		return []json.RawMessage{b}
	}
	return nil
}

// jsonScalar is a string or a number as text: a turn id is "t41" or 41, a range "t3-t9".
func jsonScalar(b json.RawMessage) (string, bool) {
	b = json.RawMessage(strings.TrimSpace(string(b)))
	if len(b) == 0 {
		return "", false
	}
	switch b[0] {
	case '"':
		var s string
		return s, json.Unmarshal(b, &s) == nil
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		var n json.Number
		if json.Unmarshal(b, &n) == nil {
			return n.String(), true
		}
	}
	return "", false
}

// buildPatch validates the decoded wire form.
func buildPatch(rp *rawPatch) (*Patch, error) {
	p := &Patch{}
	if rp.KeepFrom == "" {
		return nil, fmt.Errorf("compaction patch has no keep_from")
	}
	kf, err := ParseTurnID(rp.KeepFrom)
	if err != nil {
		return nil, fmt.Errorf("keep_from: %w", err)
	}
	p.KeepFrom = kf
	for i, s := range rp.Spine {
		from, to, err := parseRange(s.Turns)
		if err != nil || to < from {
			p.Warnings = append(p.Warnings, fmt.Sprintf("spine[%d]: bad range %q ignored", i, cutRunes(s.Turns, 40)))
			continue
		}
		line := strings.TrimSpace(strings.ReplaceAll(s.Line, "\n", " "))
		if line == "" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("spine[%d]: empty line ignored", i))
			continue
		}
		p.Spine = append(p.Spine, SpineEntry{From: from, To: to, Line: line})
	}
	for i, m := range rp.Mask {
		mm := maskRef.FindStringSubmatch(m)
		if mm == nil {
			p.Warnings = append(p.Warnings, fmt.Sprintf("mask[%d]: bad ref %q ignored", i, cutRunes(m, 40)))
			continue
		}
		t, _ := strconv.ParseInt(mm[1], 10, 64)
		ix, _ := strconv.Atoi(mm[2])
		p.Mask = append(p.Mask, MaskRef{Turn: core.TurnID(t), Index: ix})
	}
	for i, n := range rp.Notes {
		n.Op = strings.ToLower(strings.TrimSpace(n.Op))
		switch n.Op {
		case "add", "set", "replace", "remove":
		default:
			p.Warnings = append(p.Warnings, fmt.Sprintf("notes[%d]: unknown op %q ignored", i, cutRunes(n.Op, 20)))
			continue
		}
		if strings.TrimSpace(n.Key) == "" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("notes[%d]: missing key ignored", i))
			continue
		}
		p.Notes = append(p.Notes, n)
	}
	for i, pr := range rp.Promote {
		pr.Scope = strings.ToLower(strings.TrimSpace(pr.Scope))
		if (pr.Scope != "shared" && pr.Scope != "role") || strings.TrimSpace(pr.Text) == "" {
			p.Warnings = append(p.Warnings, fmt.Sprintf("promote[%d]: ignored", i))
			continue
		}
		pr.Unverified = false // never taken from the reply
		p.Promote = append(p.Promote, pr)
	}
	return p, nil
}

// chooseCandidate finds the patch object in a reply (see ParsePatch) and reports how
// many candidates the reply held.
func chooseCandidate(reply string) ([]byte, int, error) {
	fences := jsonFences(reply)
	inFence := func(pos int) bool {
		for _, f := range fences {
			if pos >= f.from && pos < f.to {
				return true
			}
		}
		return false
	}

	type cand struct {
		raw    []byte
		fenced bool
		at     int
	}
	var cands []cand
	var lastErr, brokenErr error
	sawObject := false
	broken := -1 // where the last attempt that looked like a patch but did not decode began
	// A hostile or garbled reply must not cost more than a constant number of passes:
	// every decode attempt is charged for the bytes it read, and a reply that spends the
	// allowance is refused (the caller falls back to a mechanical patch) rather than
	// judged from part of it.
	budget := 16*len(reply) + 1<<20
	for i := 0; i < len(reply); {
		j := strings.IndexByte(reply[i:], '{')
		if j < 0 {
			break
		}
		i += j
		if !startsObject(reply, i) {
			i++
			continue
		}
		if budget <= 0 {
			return nil, 0, fmt.Errorf("compaction reply is too tangled to parse")
		}
		raw, n, err := decodeObject(reply[i:])
		budget -= max(n, 16)
		if err != nil {
			lastErr = err
			if strings.Contains(strings.ToLower(reply[i:min(len(reply), i+n+16)]), "keep_from") {
				broken, brokenErr = i, err
			}
			i++
			continue
		}
		sawObject = true
		var probe struct {
			KeepFrom json.RawMessage `json:"keep_from"`
		}
		if json.Unmarshal(raw, &probe) == nil && len(probe.KeepFrom) > 0 {
			cands = append(cands, cand{raw: raw, fenced: inFence(i), at: i})
			i += n // a candidate's own contents are not candidates
			continue
		}
		i++ // valid JSON without keep_from: an object inside it may still be the patch
	}
	if len(cands) == 0 {
		switch {
		case lastErr != nil && errors.Is(lastErr, io.ErrUnexpectedEOF):
			return nil, 0, fmt.Errorf("unterminated JSON object in compaction reply")
		case brokenErr != nil:
			// the model wrote "keep_from" and then broke the JSON: objects inside it that decode (the spine's entries) are not the
			// patch, and "no keep_from" would be a false thing to say about it
			return nil, 0, fmt.Errorf("compaction patch is not valid JSON: %w", brokenErr)
		case sawObject:
			return nil, 0, fmt.Errorf("compaction patch has no keep_from")
		case lastErr != nil:
			return nil, 0, fmt.Errorf("compaction patch is not valid JSON: %w", lastErr)
		}
		return nil, 0, fmt.Errorf("no JSON object in compaction reply")
	}
	best := -1
	for i, c := range cands {
		if c.fenced {
			best = i // the last fenced candidate
		}
	}
	if best < 0 {
		best = len(cands) - 1
	}
	// An attempt that looked like a patch and failed to decode, further on than the
	// chosen one, is the model's real answer cut short or garbled: refuse rather than
	// answer with something it quoted earlier.
	if broken > cands[best].at {
		return nil, 0, fmt.Errorf("the compaction reply ends with an incomplete patch object")
	}
	return cands[best].raw, len(cands), nil
}

// startsObject reports whether the '{' at s[i] starts the first member of a JSON
// object: blanks, a quoted key and a colon. It is the cheap filter that keeps prose
// braces ("{my}", "{}", "{{{", "{\"a\"}") from costing a decode attempt. The scan
// stops at the end of the key, so over a whole reply the filter reads each byte a
// bounded number of times.
func startsObject(s string, i int) bool {
	k := i + 1
	blank := func() {
		for k < len(s) && (s[k] == ' ' || s[k] == '\t' || s[k] == '\n' || s[k] == '\r') {
			k++
		}
	}
	blank()
	if k >= len(s) || s[k] != '"' {
		return false
	}
	for k++; k < len(s); k++ {
		switch s[k] {
		case '\\':
			k++ // whatever is escaped
		case '"':
			k++
			blank()
			return k < len(s) && s[k] == ':'
		}
	}
	return false
}

// decodeObject reads one JSON value from the start of s. n is how many bytes the
// attempt consumed (for the caller's allowance), on success and on failure.
func decodeObject(s string) (raw []byte, n int, err error) {
	dec := json.NewDecoder(strings.NewReader(s))
	var v json.RawMessage
	if err := dec.Decode(&v); err != nil {
		n = int(dec.InputOffset())
		var se *json.SyntaxError
		if errors.As(err, &se) && int(se.Offset) > n {
			n = int(se.Offset)
		}
		if errors.Is(err, io.EOF) { // input ended inside the value
			err = io.ErrUnexpectedEOF
		}
		return nil, max(n, 1), err
	}
	return v, int(dec.InputOffset()), nil
}

// span is a byte range of the reply.
type span struct{ from, to int }

// jsonFences returns the bodies of the fenced code blocks of s that may hold JSON:
// fences with no language or with json, jsonc or json5. An unterminated fence runs to
// the end of the reply (a reply cut by the output limit still has its block).
func jsonFences(s string) []span {
	var out []span
	var (
		in       bool
		char     byte
		width    int
		bodyFrom int
		isJSON   bool
	)
	for pos := 0; pos < len(s); {
		next := len(s)
		line := s[pos:]
		if e := strings.IndexByte(line, '\n'); e >= 0 {
			line, next = line[:e], pos+e+1
		}
		line = strings.TrimRight(strings.TrimLeft(line, " \t"), "\r")
		c, n := fenceRun(line)
		switch {
		case !in && n >= 3:
			info := strings.TrimSpace(line[n:])
			if c == '`' && strings.Contains(info, "`") {
				break // inline code, not a fence
			}
			in, char, width, bodyFrom = true, c, n, next
			lang := strings.ToLower(strings.TrimSpace(strings.SplitN(info+" ", " ", 2)[0]))
			isJSON = lang == "" || lang == "json" || lang == "jsonc" || lang == "json5"
		case in && n >= width && c == char && strings.TrimSpace(line[n:]) == "":
			if isJSON {
				out = append(out, span{bodyFrom, pos})
			}
			in = false
		}
		pos = next
	}
	if in && isJSON {
		out = append(out, span{bodyFrom, len(s)})
	}
	return out
}

// fenceRun returns the fence character at the start of line and the length of its run
// (0 when the line does not start with ``` or ~~~).
func fenceRun(line string) (byte, int) {
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return 0, 0
	}
	n := 1
	for n < len(line) && line[n] == line[0] {
		n++
	}
	return line[0], n
}
