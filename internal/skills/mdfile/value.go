package mdfile

import (
	"fmt"
	"strconv"
	"strings"
)

// Kind is the shape of a frontmatter Value.
type Kind uint8

const (
	// KindNull is an absent or empty value ("key:", "~", "null").
	KindNull Kind = iota
	// KindString is any scalar. Frontmatter scalars are kept as text; the typed
	// accessors of Fields decide what a value means (a string field may hold
	// "true", a boolean field may be written "yes").
	KindString
	KindList
	KindMap
)

func (k Kind) String() string {
	switch k {
	case KindNull:
		return "empty"
	case KindString:
		return "a string"
	case KindList:
		return "a list"
	case KindMap:
		return "a mapping"
	}
	return "unknown"
}

// Value is one node of a parsed frontmatter document.
type Value struct {
	Kind Kind
	// Str is the text of a KindString scalar after unquoting and folding.
	Str string
	// Quoted says the scalar was written in quotes.
	Quoted bool
	// Raw is the source text of a list or mapping written in flow style
	// ("[a, b]"); Fields.Str returns it for values such as `argument-hint:
	// [message]`, which YAML reads as a list but the author meant as text.
	Raw  string
	List []Value
	Keys []string // KindMap: keys in file order
	Vals []Value  // KindMap: values, parallel to Keys
	// Line is the 1-based file line the value starts on (0 when synthesised).
	Line int
}

// Lookup returns the value of an exact key of a KindMap value.
func (v Value) Lookup(key string) (Value, bool) {
	for i, k := range v.Keys {
		if k == key {
			return v.Vals[i], true
		}
	}
	return Value{}, false
}

// NormKey is the spelling-insensitive form of a key: lower case with "-", "_"
// and spaces removed, so allowed-tools, allowed_tools and allowedTools match.
func NormKey(k string) string {
	var b strings.Builder
	b.Grow(len(k))
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c == '-' || c == '_' || c == ' ':
		case c >= 'A' && c <= 'Z':
			b.WriteByte(c + 'a' - 'A')
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// Fields reads typed values out of a frontmatter mapping. Type errors are
// collected, not returned one by one: a definition with three wrong fields
// should say so once. Keys never asked for are reported by Unused, which is how
// loaders warn about typos such as "allowed-tool".
type Fields struct {
	m    Value
	idx  map[string]int // NormKey -> index into m.Keys
	used []bool
	errs []string
}

// NewFields wraps the frontmatter mapping m (an empty Value is an empty
// mapping). It fails when two keys differ only in spelling ("allowed-tools" and
// "allowed_tools"): which one wins would otherwise depend on file order.
func NewFields(m Value) (*Fields, error) {
	f := &Fields{m: m, idx: make(map[string]int, len(m.Keys)), used: make([]bool, len(m.Keys))}
	for i, k := range m.Keys {
		n := NormKey(k)
		if j, dup := f.idx[n]; dup {
			return nil, fmt.Errorf("line %d: key %q is the same field as %q on line %d", m.Vals[i].Line, k, m.Keys[j], m.Vals[j].Line)
		}
		f.idx[n] = i
	}
	return f, nil
}

func (f *Fields) errorf(v Value, key, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if v.Line > 0 {
		f.errs = append(f.errs, fmt.Sprintf("line %d: %s: %s", v.Line, key, msg))
		return
	}
	f.errs = append(f.errs, fmt.Sprintf("%s: %s", key, msg))
}

// get finds the field by its normalised spelling and marks it used.
func (f *Fields) get(key string) (Value, bool) {
	i, ok := f.idx[NormKey(key)]
	if !ok {
		return Value{}, false
	}
	f.used[i] = true
	return f.m.Vals[i], true
}

// Has reports whether the key is present, whatever its value, and marks it used.
func (f *Fields) Has(key string) bool {
	_, ok := f.get(key)
	return ok
}

// Value returns the raw value of a key and marks it used.
func (f *Fields) Value(key string) (Value, bool) { return f.get(key) }

// Str returns a string field. Empty values count as absent. A flow list such as
// "[message]" comes back as its source text; a block list or a mapping is an
// error.
func (f *Fields) Str(key string) (string, bool) {
	v, ok := f.get(key)
	if !ok || v.Kind == KindNull {
		return "", false
	}
	switch v.Kind {
	case KindString:
		return strings.TrimSpace(v.Str), true
	case KindList, KindMap:
		if v.Raw != "" {
			return strings.TrimSpace(v.Raw), true
		}
	}
	f.errorf(v, key, "expected text, found %s", v.Kind)
	return "", false
}

// Bool returns a boolean field. true/false in any case are accepted, and also
// yes/no/on/off, which YAML 1.1 tools treat as booleans and authors use.
func (f *Fields) Bool(key string) (val, ok bool) {
	v, present := f.get(key)
	if !present || v.Kind == KindNull {
		return false, false
	}
	if v.Kind == KindString {
		switch strings.ToLower(strings.TrimSpace(v.Str)) {
		case "true", "yes", "on":
			return true, true
		case "false", "no", "off":
			return false, true
		}
	}
	f.errorf(v, key, "expected true or false, found %s", describe(v))
	return false, false
}

// Int returns an integer field.
func (f *Fields) Int(key string) (int, bool) {
	v, present := f.get(key)
	if !present || v.Kind == KindNull {
		return 0, false
	}
	if v.Kind == KindString {
		if n, err := strconv.Atoi(strings.TrimSpace(v.Str)); err == nil {
			return n, true
		}
	}
	f.errorf(v, key, "expected a whole number, found %s", describe(v))
	return 0, false
}

// List returns a list-of-strings field. It accepts a YAML list (block or
// flow) of scalars and also one string ("Read, Grep, Bash(git diff:*)"), which
// is split on commas and white space outside parentheses.
func (f *Fields) List(key string) ([]string, bool) {
	v, present := f.get(key)
	if !present || v.Kind == KindNull {
		return nil, false
	}
	switch v.Kind {
	case KindString:
		return SplitList(v.Str), true
	case KindList:
		out := make([]string, 0, len(v.List))
		for _, e := range v.List {
			if e.Kind != KindString {
				f.errorf(e, key, "expected a list of strings, found %s in it", e.Kind)
				return nil, false
			}
			if s := strings.TrimSpace(e.Str); s != "" {
				out = append(out, s)
			}
		}
		return dedupe(out), true
	}
	f.errorf(v, key, "expected a list, found %s", v.Kind)
	return nil, false
}

func describe(v Value) string {
	if v.Kind == KindString {
		return strconv.Quote(clip(v.Str, 30))
	}
	return v.Kind.String()
}

// Err reports every type error found so far, or nil.
func (f *Fields) Err() error {
	if len(f.errs) == 0 {
		return nil
	}
	return fmt.Errorf("frontmatter: %s", strings.Join(f.errs, "; "))
}

// Unused lists the keys (as written) that no accessor asked for, in file order.
func (f *Fields) Unused() []string {
	var out []string
	for i, k := range f.m.Keys {
		if !f.used[i] {
			out = append(out, k)
		}
	}
	return out
}
