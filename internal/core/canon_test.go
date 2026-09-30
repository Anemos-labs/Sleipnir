package core_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/core"
)

// U+2028 and U+2029 as bytes, so that nothing between the author and the compiler can
// turn them into a different character (they are invisible in most editors).
const (
	lineSep = "\xe2\x80\xa8"
	paraSep = "\xe2\x80\xa9"
)

// goldenHeader is the first lines of every golden file of this package.
func goldenHeader(what string) string {
	return "# " + what + "\n" +
		"# columns: case, sha256 of the bytes, byte count, the bytes (Go-quoted with q: when they hold characters a reviewer cannot see)\n" +
		"# regenerate: go test ./internal/core -run TestCanonicalGolden -update   (read docs/BUILDING.md, \"Changing prompt bytes\", first)\n"
}

// goldenLine is one case of a golden file.
func goldenLine(name string, out []byte, err error) string {
	switch {
	case err != nil:
		return name + "\t-\t-\terror\n"
	case out == nil:
		return name + "\t-\t0\t(nil)\n"
	case len(out) == 0:
		return name + "\t-\t0\t(empty, not nil)\n"
	}
	return fmt.Sprintf("%s\t%x\t%d\t%s\n", name, sha256.Sum256(out), len(out), show(out))
}

// rawCase is one document given to Canonical as text.
type rawCase struct {
	name string
	in   json.RawMessage
	// wantErr: Canonical must fail with its own prefix (the wording after it is the
	// standard library's and is not part of the contract).
	wantErr bool
}

func rawCases() []rawCase {
	deep := func(n int) json.RawMessage {
		return json.RawMessage(strings.Repeat("[", n) + "1" + strings.Repeat("]", n))
	}
	return []rawCase{
		{name: "empty_object", in: json.RawMessage(`{}`)},
		{name: "empty_array", in: json.RawMessage(`[]`)},
		{name: "scalar_string", in: json.RawMessage(`"x"`)},
		{name: "scalar_number", in: json.RawMessage(`12`)},
		{name: "scalar_true", in: json.RawMessage(`true`)},
		{name: "scalar_null", in: json.RawMessage(`null`)},
		{name: "nil_passes_through", in: nil},
		{name: "empty_passes_through", in: json.RawMessage{}},

		// What makes bytes canonical: sorted keys at every depth, nothing between tokens.
		{name: "keys_sorted_nested", in: json.RawMessage(`{"b":1,"a":{"d":2,"c":[3,{"z":1,"y":2}]}}`)},
		{name: "keys_sorted_everywhere", in: json.RawMessage(`{"z":{"b":{"d":1,"c":2},"a":[{"y":1,"x":2}]},"a":{}}`)},
		{name: "keys_sorted_bytewise", in: json.RawMessage(`{"b":1,"B":2,"a":3,"A":4,"10":5,"9":6,"":7}`)},
		{name: "keys_sorted_by_utf8_bytes_not_utf16", in: json.RawMessage("{\"\xf0\x9f\x98\x80\":1,\"\xef\xbf\xbf\":2,\"\xc3\xa9\":3,\"z\":4}")},
		{name: "array_order_kept", in: json.RawMessage(`[3,1,2,{"b":1,"a":2}]`)},
		{name: "whitespace_removed", in: json.RawMessage("{ \"a\" : [ 1 , 2 ] ,\n\t\"b\" : { } }")},
		{name: "outer_whitespace_removed", in: json.RawMessage("  {\"a\":1}  \r\n")},
		{name: "tool_schema_shape", in: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"File path"},"limit":{"type":"integer"}},"required":["path"],"additionalProperties":false}`)},
		{name: "duplicate_keys_last_wins", in: json.RawMessage(`{"a":1,"a":2}`)},

		// Strings: what is escaped and what is not.
		{name: "html_not_escaped", in: json.RawMessage(`{"a":"<b>&</b>"}`)},
		{name: "html_escapes_decoded", in: json.RawMessage("{\"\\u003cx\\u003e\":\"\\u0026\"}")},
		{name: "slash_escape_decoded", in: json.RawMessage("{\"a\":\"\\/\"}")},
		{name: "unicode_escapes_decoded", in: json.RawMessage("{\"a\":\"\\u00e9\\u0041\"}")},
		{name: "surrogate_pair_joined", in: json.RawMessage("{\"a\":\"\\ud83d\\ude00\"}")},
		{name: "lone_surrogate_replaced", in: json.RawMessage("{\"a\":\"\\ud800\"}")},
		{name: "unicode_kept_raw", in: json.RawMessage(`{"a":"é😀 日本"}`)},
		{name: "line_and_paragraph_separators_escaped", in: json.RawMessage("{\"a\":\"x" + lineSep + "y" + paraSep + "z\"}")},
		{name: "control_characters", in: json.RawMessage("{\"a\":\"\\u0000\\u001f\\u007f\\u0085\\n\\t\\r\\b\\f\"}")},
		{name: "invalid_utf8_replaced", in: json.RawMessage("{\"a\":\"x\xffy\"}")},

		// Numbers are kept as written: Canonical sorts and compacts, it does not evaluate.
		{name: "numbers_kept_verbatim", in: json.RawMessage(`{"a":1.0,"b":1e2,"c":-0,"d":100000000000000000000,"e":0.10,"f":1E+2,"g":12345678901234567890123}`)},

		{name: "nesting_50_deep", in: deep(50)},

		// Known quirks, pinned so that a change to them is a decision and not an accident:
		// Canonical reads the first JSON value and ignores whatever follows it.
		{name: "quirk_second_value_ignored", in: json.RawMessage(`{"a":1} {"b":2}`)},
		{name: "quirk_trailing_garbage_ignored", in: json.RawMessage(`{"a":1} garbage`)},

		{name: "error_whitespace_only", in: json.RawMessage("  \n"), wantErr: true},
		{name: "error_truncated", in: json.RawMessage(`{"a":`), wantErr: true},
		{name: "error_trailing_comma", in: json.RawMessage(`{"a":1,}`), wantErr: true},
		{name: "error_single_quotes", in: json.RawMessage(`{'a':1}`), wantErr: true},
		{name: "error_nan", in: json.RawMessage(`{"a":NaN}`), wantErr: true},
		{name: "error_bare_word", in: json.RawMessage(`abc`), wantErr: true},
		{name: "error_byte_order_mark", in: json.RawMessage("\xef\xbb\xbf{}"), wantErr: true},
		{name: "error_nesting_beyond_the_decoder_limit", in: deep(10001), wantErr: true},
	}
}

// TestCanonicalGolden is the one test that pins the bytes core hands to everything
// else: what Canonical makes of a JSON document, what MarshalStable makes of every
// shape of block, message, tool, turn and prompt, the blobs and hashes BuildManifest
// derives from a prompt, and the hash primitives. Anything that feeds a cache key or
// a wire hash passes through these, so a silent change in any of them (a struct tag, an
// escaping rule, a new Go release) fails here and nowhere else in this package. The
// expected bytes are in testdata/golden; the cases are in canon_test.go and
// shapes_test.go.
func TestCanonicalGolden(t *testing.T) {
	t.Run("raw", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString(goldenHeader("core.Canonical applied to raw JSON"))
		for _, c := range rawCases() {
			out, err := core.Canonical(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("%s: error = %v, want an error: %v", c.name, err, c.wantErr)
			}
			if err != nil && !strings.HasPrefix(err.Error(), "canonical json: ") {
				t.Fatalf("%s: error %q does not carry the canonical json: prefix", c.name, err)
			}
			sb.WriteString(goldenLine(c.name, out, err))
		}
		golden(t, "canonical_raw.txt", []byte(sb.String()))
	})
	t.Run("shapes", func(t *testing.T) {
		var sb strings.Builder
		sb.WriteString(goldenHeader("core.MarshalStable applied to every shape the harness hashes or logs"))
		for _, c := range shapeCases() {
			out, err := core.MarshalStable(c.v)
			if (err != nil) != c.wantErr {
				t.Fatalf("%s: error = %v, want an error: %v", c.name, err, c.wantErr)
			}
			sb.WriteString(goldenLine(c.name, out, err))
		}
		golden(t, "canonical_shapes.txt", []byte(sb.String()))
	})
	t.Run("manifest", func(t *testing.T) {
		golden(t, "canonical_manifest.txt", []byte(manifestGolden(t)))
	})
	t.Run("hash", hashKnownAnswers)
}

// hashKnownAnswers: SHA-256 of fixed inputs and the wire-hash recipe on fixed parts,
// worked out with an independent implementation (sha256sum on the same bytes), so that
// they stay true whatever happens to the golden files.
func hashKnownAnswers(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []byte
		want core.Hash
	}{
		{"empty", nil, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"},
		{"abc", []byte("abc"), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{"binary", []byte{0, 1, 2, 0xff}, "3d1f57c984978ef98a18378c8166c1cb8ede02c03eeb6aee7e2f121dfeee3e56"},
	} {
		if got := core.HashBytes(c.in); got != c.want {
			t.Errorf("HashBytes(%s) = %s, want %s", c.name, got, c.want)
		}
		if got := core.HashString(string(c.in)); got != c.want {
			t.Errorf("HashString(%s) = %s, want %s", c.name, got, c.want)
		}
	}
	// WireHash hashes "model NL tools NL each-system-hash-and-comma NL each-message-hash-and-comma".
	for _, c := range []struct {
		name          string
		model         string
		tools         core.Hash
		system, msgs  []core.Hash
		want          core.Hash
		independentOf string
	}{
		{"parts", "m", "T", []core.Hash{"s1", "s2"}, []core.Hash{"m1", "m2"}, "0de6245b586f0977679cc2da27071fc976806f4d0b144503cb4fb0b89a50fad7", "printf 'm\\nT\\ns1,s2,\\nm1,m2,' | sha256sum"},
		{"model only", "m", "", nil, nil, "de09544fee6619db65b557b84de0b1e6b6d1766e8cf30068687735e149e5feba", "printf 'm\\n\\n\\n' | sha256sum"},
	} {
		if got := core.WireHash(c.model, c.tools, c.system, c.msgs); got != c.want {
			t.Errorf("WireHash(%s) = %s, want %s (%s)", c.name, got, c.want, c.independentOf)
		}
	}
}

// ---- properties: the encoding does not depend on how the input was spelled -------------

// randString draws from strings that stress escaping.
func randString(rng *rand.Rand) string {
	pool := []string{
		"", "a", "path", "<b>", "a&b", "x\"y", "back\\slash", "tab\there", "new\nline", "é", "日本語", "😀",
		"\x00ctl\x1f", "del\x7f", lineSep, paraSep, "\xc2\xa0nbsp", "</my-notes>", "[mail m1]", "## header",
	}
	return pool[rng.Intn(len(pool))]
}

// randNumber draws literals that mean the same and read the same whether they are kept as
// written or read as a float64 (1.0, 1e2 or a 20-digit integer would not). How Canonical
// spells numbers is pinned by TestCanonicalGolden (numbers_kept_verbatim); the properties
// below are about determinism and independence from the input's spelling, and hold either way.
func randNumber(rng *rand.Rand) json.Number {
	pool := []string{"0", "-1", "1", "100", "-0", "0.5", "3.14159", "42"}
	return json.Number(pool[rng.Intn(len(pool))])
}

// randTree builds JSON as Go data: map[string]any, []any, string, json.Number, bool, nil.
func randTree(rng *rand.Rand, depth int) any {
	kind := rng.Intn(7)
	if depth <= 0 && kind < 2 {
		kind = 2 + rng.Intn(5)
	}
	switch kind {
	case 0:
		m := map[string]any{}
		for n := rng.Intn(9); n > 0; n-- {
			m[randString(rng)+strconv.Itoa(rng.Intn(20))] = randTree(rng, depth-1)
		}
		return m
	case 1:
		a := make([]any, rng.Intn(6))
		for i := range a {
			a[i] = randTree(rng, depth-1)
		}
		return a
	case 2:
		return randString(rng)
	case 3:
		return randNumber(rng)
	case 4:
		return rng.Intn(2) == 0
	}
	return nil
}

// jsonString spells s as a JSON string in one of three ways that decode alike: the
// standard library's (HTML characters escaped), minimal escaping, and everything
// outside printable ASCII as uXXXX escapes (surrogate pairs above the BMP).
func jsonString(s string, style int) string {
	switch style {
	case 0:
		b, _ := json.Marshal(s)
		return string(b)
	case 1:
		var sb strings.Builder
		sb.WriteByte('"')
		for _, r := range s {
			switch {
			case r == '"' || r == '\\':
				sb.WriteByte('\\')
				sb.WriteRune(r)
			case r < 0x20:
				fmt.Fprintf(&sb, "\\u%04x", r)
			default:
				sb.WriteRune(r)
			}
		}
		sb.WriteByte('"')
		return sb.String()
	}
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			sb.WriteByte('\\')
			sb.WriteRune(r)
		case r < 0x20 || r > 0x7e:
			if r > 0xffff {
				r -= 0x10000
				fmt.Fprintf(&sb, "\\u%04x\\u%04x", 0xd800+(r>>10), 0xdc00+(r&0x3ff))
			} else {
				fmt.Fprintf(&sb, "\\u%04x", r)
			}
		default:
			sb.WriteRune(r)
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// emit writes v as JSON text with its object keys in a random order, random white
// space between tokens and a random spelling for every string: the same document as
// far as any JSON reader is concerned, never the same bytes.
func emit(sb *strings.Builder, v any, rng *rand.Rand) {
	ws := func() {
		sb.WriteString([]string{"", " ", "\n", "\t ", "\r\n  "}[rng.Intn(5)])
	}
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys) // a fixed start, so the shuffle below is a function of the seed alone
		rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
		sb.WriteByte('{')
		ws()
		for i, k := range keys {
			if i > 0 {
				ws()
				sb.WriteByte(',')
				ws()
			}
			sb.WriteString(jsonString(k, rng.Intn(3)))
			ws()
			sb.WriteByte(':')
			ws()
			emit(sb, x[k], rng)
		}
		ws()
		sb.WriteByte('}')
	case []any:
		sb.WriteByte('[')
		ws()
		for i, e := range x {
			if i > 0 {
				ws()
				sb.WriteByte(',')
				ws()
			}
			emit(sb, e, rng)
		}
		ws()
		sb.WriteByte(']')
	case string:
		sb.WriteString(jsonString(x, rng.Intn(3)))
	case json.Number:
		sb.WriteString(string(x))
	case bool:
		sb.WriteString(strconv.FormatBool(x))
	case nil:
		sb.WriteString("null")
	default:
		panic(fmt.Sprintf("emit: unexpected %T", v))
	}
}

// canonicalForm reports why b is not canonical JSON: it must be valid, compact, and
// have its object keys strictly ascending (bytewise) at every level.
func canonicalForm(b []byte) error {
	if !json.Valid(b) {
		return errors.New("not valid JSON")
	}
	var c bytes.Buffer
	if err := json.Compact(&c, b); err != nil {
		return err
	}
	if !bytes.Equal(c.Bytes(), b) {
		return errors.New("has insignificant white space")
	}
	type frame struct {
		object  bool
		wantKey bool
		keys    []string
	}
	var stack []*frame
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].wantKey = true
		}
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch x := tok.(type) {
		case json.Delim:
			switch x {
			case '{':
				stack = append(stack, &frame{object: true, wantKey: true})
			case '[':
				stack = append(stack, &frame{})
			default:
				f := stack[len(stack)-1]
				for i := 1; i < len(f.keys); i++ {
					if f.keys[i-1] >= f.keys[i] {
						return fmt.Errorf("object keys %q and %q are out of order or repeated", f.keys[i-1], f.keys[i])
					}
				}
				stack = stack[:len(stack)-1]
				valueDone()
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].wantKey {
				stack[n-1].keys = append(stack[n-1].keys, x)
				stack[n-1].wantKey = false
			} else {
				valueDone()
			}
		default:
			valueDone()
		}
	}
}

// TestCanonicalIgnoresKeyOrderAndSpelling: however a document is spelled (key order,
// white space, string escapes), Canonical yields the bytes MarshalStable gives the
// same data, and those bytes are a fixed point. Each document is tried with many seeds;
// the seeds are fixed, so a failure names the one that reproduces it.
func TestCanonicalIgnoresKeyOrderAndSpelling(t *testing.T) {
	type doc struct {
		name string
		v    any
	}
	docs := []doc{
		{"flat", map[string]any{"b": json.Number("1"), "a": "<x>", "": true, "é": nil, "z": []any{}}},
		{"nested", map[string]any{"z": map[string]any{"b": []any{map[string]any{"y": "1", "x": json.Number("2.5")}}, "a": map[string]any{}}, "a": "\x00"}},
		{"array_root", []any{map[string]any{"b": nil, "a": nil}, "s", json.Number("100")}},
		{"string_root", "<&>" + lineSep},
	}
	for seed := int64(1); seed <= 40; seed++ {
		docs = append(docs, doc{fmt.Sprintf("random_tree_%d", seed), randTree(rand.New(rand.NewSource(seed)), 4)})
	}

	for _, d := range docs {
		want, err := core.MarshalStable(d.v)
		if err != nil {
			t.Fatalf("%s: MarshalStable: %v", d.name, err)
		}
		if err := canonicalForm(want); err != nil {
			t.Fatalf("%s: MarshalStable gave non-canonical bytes %q: %v", d.name, want, err)
		}
		if again, err := core.Canonical(want); err != nil || !bytes.Equal(again, want) {
			t.Fatalf("%s: canonical bytes are not a fixed point:\n  %s\n  %s (%v)", d.name, want, again, err)
		}
		for seed := int64(0); seed < 25; seed++ {
			var sb strings.Builder
			emit(&sb, d.v, rand.New(rand.NewSource(seed)))
			got, err := core.Canonical(json.RawMessage(sb.String()))
			if err != nil {
				t.Fatalf("%s seed %d: Canonical(%q): %v", d.name, seed, sb.String(), err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s seed %d: the same document in another spelling gave other bytes\n  input: %q\n  got:   %q\n  want:  %q", d.name, seed, sb.String(), got, want)
			}
		}
	}
}

// TestCanonicalDoesNotDependOnMapIteration: Go randomises map iteration, and Canonical
// decodes into maps. One document, many runs, one answer. (With 64 keys a leak of the
// iteration order would change the bytes on all but one run in 64! orderings.)
func TestCanonicalDoesNotDependOnMapIteration(t *testing.T) {
	var sb strings.Builder
	sb.WriteByte('{')
	for i := 63; i >= 0; i-- {
		if i != 63 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `"k%02d":{"y":%d,"x":[%d,{"b":1,"a":2}]}`, i, i, i)
	}
	sb.WriteByte('}')
	in := json.RawMessage(sb.String())
	first, err := core.Canonical(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := canonicalForm(first); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(first), `{"k00":{"x":[0,{"a":2,"b":1}],"y":0},"k01"`) {
		t.Fatalf("keys are not sorted: %.120s", first)
	}
	for i := 0; i < 200; i++ {
		got, err := core.Canonical(in)
		if err != nil || !bytes.Equal(got, first) {
			t.Fatalf("run %d gave different bytes (err %v)", i, err)
		}
	}
}

// TestMarshalStableIgnoresMapInsertionOrder: MarshalStable of a map is the same bytes
// whatever order the entries were inserted in, at every nesting level.
func TestMarshalStableIgnoresMapInsertionOrder(t *testing.T) {
	const n = 48
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("key-%02d", i)
	}
	build := func(order []int) map[string]any {
		m := map[string]any{}
		for _, i := range order {
			inner := map[string]any{}
			for _, j := range order {
				inner[keys[j]] = i*n + j
			}
			m[keys[i]] = inner
		}
		return m
	}
	identity := make([]int, n)
	for i := range identity {
		identity[i] = i
	}
	want, err := core.MarshalStable(build(identity))
	if err != nil {
		t.Fatal(err)
	}
	if err := canonicalForm(want); err != nil {
		t.Fatal(err)
	}
	for seed := int64(0); seed < 30; seed++ {
		order := append([]int(nil), identity...)
		rand.New(rand.NewSource(seed)).Shuffle(n, func(i, j int) { order[i], order[j] = order[j], order[i] })
		got, err := core.MarshalStable(build(order))
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("seed %d: insertion order changed the bytes (err %v)", seed, err)
		}
	}
}

// ---- edge cases that are behaviour rather than bytes ------------------------------------

func TestCanonicalEdgeCases(t *testing.T) {
	t.Run("MustCanonical returns canonical bytes and panics on invalid JSON", func(t *testing.T) {
		if got := core.MustCanonical(json.RawMessage(`{ "b" : 1 , "a" : 2 }`)); string(got) != `{"a":2,"b":1}` {
			t.Fatalf("MustCanonical = %s", got)
		}
		defer func() {
			r := recover()
			err, ok := r.(error)
			if !ok || !strings.HasPrefix(err.Error(), "canonical json: ") {
				t.Fatalf("recovered %v (%T), want an error with the canonical json prefix", r, r)
			}
		}()
		core.MustCanonical(json.RawMessage(`{`))
		t.Fatal("MustCanonical did not panic on invalid JSON")
	})
	t.Run("errors wrap the decoder's error", func(t *testing.T) {
		_, err := core.Canonical(json.RawMessage(`{"a":1,}`))
		var syn *json.SyntaxError
		if !errors.As(err, &syn) {
			t.Fatalf("error %v does not wrap a *json.SyntaxError", err)
		}
	})
	t.Run("output and input never share memory", func(t *testing.T) {
		in := json.RawMessage(`{"b":1,"a":2}`)
		out, err := core.Canonical(in)
		if err != nil {
			t.Fatal(err)
		}
		want := string(out)
		for i := range in {
			in[i] = 'x'
		}
		if string(out) != want {
			t.Fatal("changing the input changed an earlier output")
		}
		again, _ := core.Canonical(json.RawMessage(`{"b":1,"a":2}`))
		for i := range again {
			again[i] = 'y'
		}
		if next, _ := core.Canonical(json.RawMessage(`{"b":1,"a":2}`)); string(next) != want {
			t.Fatal("changing an output changed a later one")
		}
	})
	t.Run("nesting far beyond the decoder's limit is an error, not a crash", func(t *testing.T) {
		if _, err := core.Canonical(json.RawMessage(strings.Repeat("[", 200000))); err == nil {
			t.Fatal("200000 open brackets were accepted")
		}
	})
	t.Run("huge documents", func(t *testing.T) {
		big := strings.Repeat("abc", 1<<20) // 3 MiB in one string
		in := json.RawMessage(`{"z":"` + big + `","a":[` + strings.Repeat("1,", 100000) + `1]}`)
		out, err := core.Canonical(in)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(out), `{"a":[1,1,`) || !strings.HasSuffix(string(out), `,1],"z":"`+big+`"}`) {
			t.Fatalf("unexpected output of %d bytes", len(out))
		}
	})
	t.Run("concurrent callers get the same bytes", func(t *testing.T) {
		in := json.RawMessage(`{"b":{"d":1,"c":2},"a":[3,2,1]}`)
		want, _ := core.Canonical(in)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for j := 0; j < 200; j++ {
					got, err := core.Canonical(in)
					if err != nil || !bytes.Equal(got, want) {
						t.Errorf("concurrent Canonical gave %q, %v", got, err)
						return
					}
				}
			}()
		}
		wg.Wait()
	})
}

// TestHashShort: Short is the first 12 characters, or the whole hash when it is shorter.
func TestHashShort(t *testing.T) {
	cases := []struct {
		in   core.Hash
		want string
	}{
		{"", ""},
		{"abc", "abc"},
		{"01234567890", "01234567890"},
		{"012345678901", "012345678901"},
		{"0123456789012", "012345678901"},
		{core.HashString("x"), string(core.HashString("x"))[:12]},
	}
	for _, c := range cases {
		if got := c.in.Short(); got != c.want {
			t.Errorf("Hash(%q).Short() = %q, want %q", c.in, got, c.want)
		}
	}
	if h := core.HashString("x"); len(h) != 64 || strings.ToLower(string(h)) != string(h) {
		t.Errorf("HashString must be 64 lowercase hex characters, got %q", h)
	}
}

// ---- fuzzing ----------------------------------------------------------------------------

// FuzzCanonical: for any input Canonical must not panic, must answer the same way
// twice, and must return valid, compact, key-sorted JSON that is a fixed point; and
// when the input is one valid document, every respelling of it (compacted, indented,
// re-marshalled with HTML escaping, keys and white space shuffled) must canonicalise
// to the same bytes.
func FuzzCanonical(f *testing.F) {
	for _, c := range rawCases() {
		f.Add([]byte(c.in))
	}
	for _, s := range []string{`{"a":1}`, `[`, `}`, ``, ` `, `"x"`, `{"a":{"b":{"c":{}}}}`, `{"x":1e999}`, "\x00", `{"a":"\u0000"}`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		raw := json.RawMessage(data)
		out, err := core.Canonical(raw)
		out2, err2 := core.Canonical(raw)
		if (err == nil) != (err2 == nil) || !bytes.Equal(out, out2) {
			t.Fatalf("not deterministic: %q, %v then %q, %v", out, err, out2, err2)
		}
		if err != nil {
			if !strings.HasPrefix(err.Error(), "canonical json: ") {
				t.Fatalf("error without the canonical json prefix: %v", err)
			}
			return
		}
		if len(data) == 0 {
			if len(out) != 0 {
				t.Fatalf("empty input gave %q", out)
			}
			return
		}
		if !utf8.Valid(out) {
			t.Fatalf("output is not valid UTF-8: %q", out)
		}
		if err := canonicalForm(out); err != nil {
			t.Fatalf("output %q of input %q is not canonical: %v", out, data, err)
		}
		if again, err := core.Canonical(out); err != nil || !bytes.Equal(again, out) {
			t.Fatalf("output is not a fixed point: %q -> %q (%v)", out, again, err)
		}

		if !json.Valid(data) {
			return // Canonical read the first value of a longer text; nothing to compare it with
		}
		variants := map[string][]byte{}
		var compact, indented bytes.Buffer
		if err := json.Compact(&compact, data); err != nil {
			t.Fatalf("Compact of valid JSON failed: %v", err)
		}
		variants["compact"] = compact.Bytes()
		// json.Indent is quadratic in nesting depth (2.6 s at depth 9999), which would
		// stall the fuzzer and say nothing about Canonical; deep inputs skip it.
		if bytes.Count(data, []byte("["))+bytes.Count(data, []byte("{")) <= 200 {
			if err := json.Indent(&indented, data, "\t", "  "); err != nil {
				t.Fatalf("Indent of valid JSON failed: %v", err)
			}
			variants["indent"] = indented.Bytes()
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("Decode of valid JSON failed: %v", err)
		}
		if b, err := json.Marshal(v); err == nil {
			variants["remarshal"] = b
		}
		h := fnv.New64a()
		h.Write(data)
		for i := 0; i < 3; i++ {
			var sb strings.Builder
			emit(&sb, v, rand.New(rand.NewSource(int64(h.Sum64())+int64(i))))
			variants["shuffle"+strconv.Itoa(i)] = []byte(sb.String())
		}
		names := make([]string, 0, len(variants))
		for n := range variants {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			got, err := core.Canonical(json.RawMessage(variants[n]))
			if err != nil || !bytes.Equal(got, out) {
				t.Fatalf("respelling %q of %q canonicalises differently:\n  variant: %q\n  got:  %q (%v)\n  want: %q", n, data, variants[n], got, err, out)
			}
		}
	})
}
