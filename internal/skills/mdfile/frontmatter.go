package mdfile

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// MaxFrontmatterBytes bounds the frontmatter block. Definitions are short
	// metadata; a megabyte of "frontmatter" is an attack on the parser.
	MaxFrontmatterBytes = 32 << 10
	maxFrontmatterLines = 1000
)

// Doc is a markdown file split into frontmatter and body.
type Doc struct {
	// Meta is the frontmatter as a KindMap value (empty when there is none).
	Meta Value
	// Body is the text after the frontmatter, exactly as written (see ReadDoc for
	// the cleaned form).
	Body string
	// Format is "yaml", "json" or "" when the file has no frontmatter.
	Format string
	// BodyLine is the 1-based file line the body starts on.
	BodyLine int
	// Notes are observations about the file that are not errors, for the loader
	// to turn into warnings.
	Notes []string
}

// Parse splits text (already normalised, see Normalize) into frontmatter and
// body.
//
//   - A file whose first line is "---" has YAML frontmatter up to the next line
//     that is "---" (or "..."). A frontmatter block that is never closed is an
//     error: silently treating it as body would put configuration in front of a
//     model as if it were instructions.
//   - A file that starts with "{" has JSON frontmatter when a JSON object
//     comes first; otherwise the brace is simply the start of the body, with a
//     note.
//   - Anything else has no frontmatter.
func Parse(text string) (Doc, error) {
	switch {
	case strings.HasPrefix(text, "---"):
		first, _, _ := strings.Cut(text, "\n")
		if strings.TrimRight(first, " \t") == "---" {
			return parseFenced(text)
		}
	case strings.HasPrefix(text, "{"):
		return parseJSONDoc(text)
	}
	return Doc{Meta: Value{Kind: KindMap}, Body: text, BodyLine: 1}, nil
}

func parseFenced(text string) (Doc, error) {
	unclosed := &SyntaxError{Line: 1, Msg: "the frontmatter opened by \"---\" is never closed"}
	nl := strings.IndexByte(text, '\n')
	if nl < 0 {
		return Doc{}, unclosed
	}
	rest := text[nl+1:]
	pos := 0 // start of the current line within rest
	for lineNo := 2; ; lineNo++ {
		if lineNo-2 >= maxFrontmatterLines || pos > MaxFrontmatterBytes {
			return Doc{}, &SyntaxError{Line: 1, Msg: fmt.Sprintf("the frontmatter is longer than %d KB or %d lines, or is never closed with \"---\"", MaxFrontmatterBytes>>10, maxFrontmatterLines)}
		}
		end := strings.IndexByte(rest[pos:], '\n')
		lineEnd := len(rest)
		if end >= 0 {
			lineEnd = pos + end
		}
		if t := strings.TrimRight(rest[pos:lineEnd], " \t"); t == "---" || t == "..." {
			region := ""
			if pos > 0 {
				region = rest[:pos-1] // without the line break before the fence
			}
			meta, err := parseYAML(region, 2)
			if err != nil {
				return Doc{}, err
			}
			body := ""
			if end >= 0 {
				body = rest[lineEnd+1:]
			}
			return Doc{Meta: meta, Body: body, Format: "yaml", BodyLine: lineNo + 1}, nil
		}
		if end < 0 {
			return Doc{}, unclosed
		}
		pos = lineEnd + 1
	}
}

func parseJSONDoc(text string) (Doc, error) {
	limit := text
	if len(limit) > MaxFrontmatterBytes {
		limit = limit[:MaxFrontmatterBytes]
	}
	dec := json.NewDecoder(strings.NewReader(limit))
	dec.UseNumber()
	nodes := 0
	v, err := decodeJSON(dec, 0, &nodes)
	if err != nil || v.Kind != KindMap {
		return Doc{
			Meta: Value{Kind: KindMap}, Body: text, BodyLine: 1,
			Notes: []string{"the file starts with \"{\" but not with a JSON object, so it has no frontmatter and the text is the body"},
		}, nil
	}
	consumed := int(dec.InputOffset())
	body := text[consumed:]
	line := 1 + strings.Count(text[:consumed], "\n")
	body = strings.TrimPrefix(body, "\n")
	line++
	return Doc{Meta: v, Body: body, Format: "json", BodyLine: line}, nil
}

// decodeJSON reads one JSON value into a Value, keeping key order and
// rejecting duplicate keys (encoding/json would silently keep the last one).
func decodeJSON(dec *json.Decoder, depth int, nodes *int) (Value, error) {
	if depth > maxDepth {
		return Value{}, fmt.Errorf("nested more than %d levels", maxDepth)
	}
	*nodes++
	if *nodes > maxNodes {
		return Value{}, fmt.Errorf("more than %d values", maxNodes)
	}
	tok, err := dec.Token()
	if err != nil {
		return Value{}, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			v := Value{Kind: KindMap, Line: 1}
			seen := map[string]bool{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return Value{}, err
				}
				key, ok := kt.(string)
				if !ok || key == "" || len(key) > maxKeyLen {
					return Value{}, fmt.Errorf("bad key %v", kt)
				}
				if seen[key] {
					return Value{}, fmt.Errorf("duplicate key %q", key)
				}
				seen[key] = true
				val, err := decodeJSON(dec, depth+1, nodes)
				if err != nil {
					return Value{}, err
				}
				v.Keys = append(v.Keys, key)
				v.Vals = append(v.Vals, val)
			}
			if _, err := dec.Token(); err != nil { // }
				return Value{}, err
			}
			return v, nil
		case '[':
			v := Value{Kind: KindList, Line: 1}
			for dec.More() {
				val, err := decodeJSON(dec, depth+1, nodes)
				if err != nil {
					return Value{}, err
				}
				v.List = append(v.List, val)
			}
			if _, err := dec.Token(); err != nil { // ]
				return Value{}, err
			}
			return v, nil
		}
		return Value{}, fmt.Errorf("unexpected %q", string(t))
	case string:
		return Value{Kind: KindString, Str: t, Quoted: true, Line: 1}, nil
	case json.Number:
		return Value{Kind: KindString, Str: t.String(), Line: 1}, nil
	case bool:
		if t {
			return Value{Kind: KindString, Str: "true", Line: 1}, nil
		}
		return Value{Kind: KindString, Str: "false", Line: 1}, nil
	case nil:
		return Value{Kind: KindNull, Line: 1}, nil
	}
	return Value{}, fmt.Errorf("unexpected JSON token %v", tok)
}
