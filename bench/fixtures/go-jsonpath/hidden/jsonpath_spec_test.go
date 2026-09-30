package jsonpath

import (
	"encoding/json"
	"math/rand"
	"reflect"
	"strings"
	"testing"
)

func mustDecode(t *testing.T, text string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("bad test JSON %s: %v", text, err)
	}
	return v
}

// check evaluates path on doc and compares the result with want, written as a JSON array.
func check(t *testing.T, doc any, path, want string) {
	t.Helper()
	got, err := Eval(doc, path)
	if err != nil {
		t.Errorf("Eval(%q): unexpected error: %v", path, err)
		return
	}
	w := mustDecode(t, want).([]any)
	if len(got) != len(w) || (len(w) > 0 && !reflect.DeepEqual(got, w)) {
		t.Errorf("Eval(%q)\n got  %#v\n want %#v", path, got, w)
	}
}

type pathCase struct{ path, want string }

func checkAll(t *testing.T, docText string, cases []pathCase) {
	t.Helper()
	doc := mustDecode(t, docText)
	for _, c := range cases {
		check(t, doc, c.path, c.want)
	}
}

const storeJSON = `{
  "store": {
    "name": "Corner Books",
    "book": [
      {"title": "Sayings", "price": 8.95, "tags": ["short", "wise"]},
      {"title": "Sword", "price": 12.99, "isbn": "0-553-21311-3"},
      {"title": "Moby", "price": 8.99, "isbn": "0-553-21311-4"}
    ],
    "bicycle": {"color": "red", "price": 19.95}
  },
  "expensive": 10
}`

func TestSpecWholeDocument(t *testing.T) {
	for _, text := range []string{storeJSON, `[1, 2]`, `"str"`, `42`, `true`, `null`, `{}`, `[]`} {
		doc := mustDecode(t, text)
		got, err := Eval(doc, "$")
		if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], doc) {
			t.Errorf("Eval(%s, %q) = %v, %v; want [doc], nil", text, "$", got, err)
		}
	}
}

func TestSpecChildAndIndex(t *testing.T) {
	checkAll(t, storeJSON, []pathCase{
		{"$.expensive", `[10]`},
		{"$.store.name", `["Corner Books"]`},
		{"$['store']['name']", `["Corner Books"]`},
		{"$.store['book'][0].title", `["Sayings"]`},
		{"$['store'].book[2]['isbn']", `["0-553-21311-4"]`},
		{"$.store.book[3]", `[]`},
		{"$.store.book[0].tags[1]", `["wise"]`},
		{"$.store.bicycle", `[{"color": "red", "price": 19.95}]`},
		{"$.store.book[1]", `[{"title": "Sword", "price": 12.99, "isbn": "0-553-21311-3"}]`},
	})
}

func TestSpecWildcard(t *testing.T) {
	checkAll(t, storeJSON, []pathCase{
		{"$.store.book[*].title", `["Sayings", "Sword", "Moby"]`},
		{"$.store.book[*].isbn", `["0-553-21311-3", "0-553-21311-4"]`},
		{"$.store.book[0].tags[*]", `["short", "wise"]`},
		{"$.store.book[*].tags[*]", `["short", "wise"]`},
		{"$.store.bicycle[*]", `["red", 19.95]`},
		{"$.store.name[*]", `[]`},
		{"$.expensive[*]", `[]`},
		{"$.store[*].price", `[19.95]`},
		{"$.store[*][0]", `[{"title": "Sayings", "price": 8.95, "tags": ["short", "wise"]}]`},
		// every book is an object, whose values come in key order: isbn, price, tags, title
		{"$.store.book[*][*]", `[8.95, ["short", "wise"], "Sayings", "0-553-21311-3", 12.99, "Sword", "0-553-21311-4", 8.99, "Moby"]`},
	})
}

func TestSpecMemberOrder(t *testing.T) {
	// A Go map has no order; the spec sorts the keys. Repeat, so that an
	// implementation that depends on map iteration order cannot pass by luck.
	for i := 0; i < 20; i++ {
		checkAll(t, `{"h": 8, "g": 7, "f": 6, "e": 5, "d": 4, "c": 3, "b": 2, "a": 1}`, []pathCase{
			{"$[*]", `[1, 2, 3, 4, 5, 6, 7, 8]`},
		})
		// byte order: "B" < "C" < "a" < "b"
		checkAll(t, `{"b": 2, "a": 1, "C": 3, "B": 4}`, []pathCase{
			{"$[*]", `[4, 3, 1, 2]`},
		})
		// byte order again: "a" < "z" < "é"
		checkAll(t, `{"é": 1, "z": 2, "a": 3}`, []pathCase{
			{"$[*]", `[3, 2, 1]`},
		})
		checkAll(t, `{"b": {"k": 1}, "a": {"k": 2}, "c": {"k": 3}}`, []pathCase{
			{"$..k", `[2, 1, 3]`},
			{"$[*].k", `[2, 1, 3]`},
		})
		checkAll(t, storeJSON, []pathCase{
			{"$..price", `[19.95, 8.95, 12.99, 8.99]`},
		})
	}
}

func TestSpecWildcardsCompose(t *testing.T) {
	checkAll(t, `[[1, 2], [3], [], [4, 5]]`, []pathCase{
		{"$[*][*]", `[1, 2, 3, 4, 5]`},
		{"$[*][0]", `[1, 3, 4]`},
		{"$[*][1]", `[2, 5]`},
		{"$[2][*]", `[]`},
		{"$[3][1]", `[5]`},
	})
	// "x" is an object: its values come in key order (a, then b); then "y" follows.
	checkAll(t, `{"x": {"b": [1, 2], "a": [3]}, "y": [4]}`, []pathCase{
		{"$[*][*]", `[[3], [1, 2], 4]`},
		{"$[*][*][*]", `[3, 1, 2]`},
	})
}

func TestSpecRecursiveDescent(t *testing.T) {
	checkAll(t, storeJSON, []pathCase{
		{"$..price", `[19.95, 8.95, 12.99, 8.99]`},
		{"$..title", `["Sayings", "Sword", "Moby"]`},
		{"$..isbn", `["0-553-21311-3", "0-553-21311-4"]`},
		{"$..name", `["Corner Books"]`},
		{"$..nothing", `[]`},
		{"$..book[1].title", `["Sword"]`},
		{"$..book[*].price", `[8.95, 12.99, 8.99]`},
		{"$..tags[0]", `["short"]`},
		{"$..bicycle.color", `["red"]`},
		{"$.store..price", `[19.95, 8.95, 12.99, 8.99]`},
		{"$.store.book..price", `[8.95, 12.99, 8.99]`},
		{"$.store.book[2]..price", `[8.99]`},
		{"$.expensive..price", `[]`},
	})
}

func TestSpecRecursiveDescentIsPreOrder(t *testing.T) {
	// A value's own member comes before the ones inside it, even though "a" sorts
	// before "x": the top-level "x" is found first, then the one inside "a".
	checkAll(t, `{"a": {"x": 5}, "x": 6}`, []pathCase{
		{"$..x", `[6, 5]`},
	})
	// Depth first: items[0] and everything below it before items[1]. (Breadth
	// first would give [1, 3, 2].)
	checkAll(t, `{"items": [{"id": 1, "sub": [{"id": 2}]}, {"id": 3}]}`, []pathCase{
		{"$..id", `[1, 2, 3]`},
	})
	// visit(root) selects the outer "a"; then visit(outer "a") selects the middle one, and so on.
	// "a" is visited before "b", each subtree in full. (Breadth first would give [A1, A2, 2, 1].)
	checkAll(t, `{"a": {"a": {"a": 1}}, "b": {"a": 2}}`, []pathCase{
		{"$..a", `[{"a": {"a": 1}}, {"a": 1}, 1, 2]`},
	})
	// A root array has no members of its own; its elements are visited in order.
	checkAll(t, `[{"a": 1}, {"b": {"a": 2}}, {"a": 3}]`, []pathCase{
		{"$..a", `[1, 2, 3]`},
		{"$..b", `[{"a": 2}]`},
	})
	// Only objects have members: array elements are not found by "name", not even digits.
	checkAll(t, `{"a": [5, 6], "b": {"0": "zero"}}`, []pathCase{
		{"$..0", `["zero"]`},
		{"$.a.0", `[]`},
		{"$.b.0", `["zero"]`},
		{"$.b[0]", `[]`},
		{"$.a[0]", `[5]`},
		{"$.a['0']", `[]`},
		{"$.b['0']", `["zero"]`},
	})
}

func TestSpecNothingIsDeduplicated(t *testing.T) {
	// $..a selects A1 = {"a": {"b": 1}} and A2 = {"b": 1}; ..b finds the 1 below each of them.
	checkAll(t, `{"a": {"a": {"b": 1}}}`, []pathCase{
		{"$..a", `[{"a": {"b": 1}}, {"b": 1}]`},
		{"$..a..b", `[1, 1]`},
	})
	// $..a selects A1, A2 and 1; ..a applied to A1 gives A2 and 1, applied to A2 gives 1.
	checkAll(t, `{"a": {"a": {"a": 1}}}`, []pathCase{
		{"$..a..a", `[{"a": 1}, 1, 1]`},
	})
	checkAll(t, `[[1, 2], [1, 2]]`, []pathCase{
		{"$[*][*]", `[1, 2, 1, 2]`},
		{"$[0]", `[[1, 2]]`},
	})
}

func TestSpecNullIsAValue(t *testing.T) {
	checkAll(t, `{"a": null, "b": [null, 1], "c": {"a": null}}`, []pathCase{
		{"$.a", `[null]`},
		{"$.c.a", `[null]`},
		{"$.b[0]", `[null]`},
		{"$.b[*]", `[null, 1]`},
		{"$..a", `[null, null]`},
		{"$.a.x", `[]`},
		{"$.a[*]", `[]`},
		{"$.a[0]", `[]`},
		{"$.zzz", `[]`},
	})
	for path, want := range map[string]string{"$": `[null]`, "$.a": `[]`, "$[0]": `[]`, "$[*]": `[]`, "$..a": `[]`} {
		check(t, nil, path, want)
	}
}

func TestSpecMissingAndMismatchedTypes(t *testing.T) {
	checkAll(t, `{"n": 1, "s": "str", "b": true, "z": null, "arr": [1, 2], "obj": {"k": "v"}, "e": {}, "ea": []}`, []pathCase{
		{"$.n.x", `[]`},
		{"$.s.length", `[]`},
		{"$.b.x", `[]`},
		{"$.z.x", `[]`},
		{"$.arr.x", `[]`},
		{"$.arr['x']", `[]`},
		{"$.obj[0]", `[]`},
		{"$.s[0]", `[]`},
		{"$.n[*]", `[]`},
		{"$.b[*]", `[]`},
		{"$.z[*]", `[]`},
		{"$.s[*]", `[]`},
		{"$.e[*]", `[]`},
		{"$.ea[*]", `[]`},
		{"$.arr[2]", `[]`},
		{"$.arr[*].x", `[]`},
		{"$.arr[0].x", `[]`},
		{"$.missing.deeper[0][*].x..y", `[]`},
		{"$.missing", `[]`},
		{"$..missing", `[]`},
		{"$.e.x", `[]`},
		{"$.obj.k[0]", `[]`},
		{"$.obj.k.k", `[]`},
	})
}

func TestSpecRootsThatAreNotObjects(t *testing.T) {
	checkAll(t, `[10, 20, 30]`, []pathCase{
		{"$[0]", `[10]`},
		{"$[1]", `[20]`},
		{"$[2]", `[30]`},
		{"$[3]", `[]`},
		{"$[*]", `[10, 20, 30]`},
		{"$.a", `[]`},
		{"$['a']", `[]`},
		{"$..a", `[]`},
		{"$[99999999999999999999]", `[]`},
		{"$[18446744073709551616]", `[]`},
	})
	checkAll(t, `[{"a": 1}, {"a": 2}, {"b": 3}]`, []pathCase{
		{"$[*].a", `[1, 2]`},
		{"$[2].b", `[3]`},
		{"$[2].a", `[]`},
		{"$..a", `[1, 2]`},
		{"$..b", `[3]`},
	})
	checkAll(t, `"str"`, []pathCase{{"$[0]", `[]`}, {"$.a", `[]`}, {"$[*]", `[]`}, {"$..a", `[]`}})
	checkAll(t, `{}`, []pathCase{{"$[*]", `[]`}, {"$..a", `[]`}, {"$.a", `[]`}})
	checkAll(t, `[]`, []pathCase{{"$[*]", `[]`}, {"$..a", `[]`}, {"$[0]", `[]`}})
}

func TestSpecQuotedNames(t *testing.T) {
	checkAll(t, `{"a.b": 1, "a": {"b": 2}, "my key": 3, "": 4, "a]b": 5, "[*]": 6, "héllo": 7, "0": 8}`, []pathCase{
		{"$['a.b']", `[1]`},
		{"$.a.b", `[2]`},
		{"$['a']['b']", `[2]`},
		{"$['a'].b", `[2]`},
		{"$.a['b']", `[2]`},
		{"$['my key']", `[3]`},
		{"$['']", `[4]`},
		{"$['a]b']", `[5]`},
		{"$['[*]']", `[6]`},
		{"$['héllo']", `[7]`},
		{"$.0", `[8]`},
		{"$['0']", `[8]`},
		{"$[0]", `[]`},
		{"$['x']", `[]`},
		{"$['nope']['a.b']", `[]`},
	})
}

func TestSpecNames(t *testing.T) {
	checkAll(t, `{"_a1": 1, "A_Z": 2, "9lives": 3, "a": {"_": 4}}`, []pathCase{
		{"$._a1", `[1]`},
		{"$.A_Z", `[2]`},
		{"$.9lives", `[3]`},
		{"$.a._", `[4]`},
		{"$.a_z", `[]`},
		{"$.A_z", `[]`},
		{"$..A_Z", `[2]`},
		{"$..9lives", `[3]`},
	})
}

func TestSpecSyntaxErrors(t *testing.T) {
	doc := mustDecode(t, storeJSON)
	for _, path := range []string{
		"", "a", "store", ".a", " $", "$ ", "$ .a", "$.a ", "$$", "$.a$", "$a",
		"$.", "$.a.", "$.[0]", "$.*", "$..", "$..*", "$..[0]", "$..['a']", "$...a",
		"$[", "$[]", "$[*", "$[0", "$['a", "$['a'", "$['a'x]", "$[a]", "$[-1]", "$[1.5]", "$[1,2]", "$[0:2]",
		"$[?(@.a)]", "$[ 0 ]", "$[*] ", "$[**]", `$["a"]`, "$['it's']", "$[0]]", "$]", "$.a-b", "$.café",
	} {
		got, err := Eval(doc, path)
		if err == nil || len(got) != 0 {
			t.Errorf("Eval(doc, %q) = %v, %v; want an empty result and an error", path, got, err)
		}
		// The verdict depends on the path alone.
		if got, err := Eval(nil, path); err == nil || len(got) != 0 {
			t.Errorf("Eval(nil, %q) = %v, %v; want an empty result and an error", path, got, err)
		}
	}
}

func TestSpecWholePathIsCheckedFirst(t *testing.T) {
	doc := mustDecode(t, storeJSON)
	for _, path := range []string{"$.nope[abc]", "$.nope.[0]", "$.nope..*", "$.nope['unclosed", "$.store.nope[-1]", "$[5].x[", "$.expensive.a b"} {
		if got, err := Eval(doc, path); err == nil || len(got) != 0 {
			t.Errorf("Eval(doc, %q) = %v, %v; want an empty result and an error", path, got, err)
		}
	}
}

func TestSpecNeverPanics(t *testing.T) {
	doc := mustDecode(t, storeJSON)
	try := func(path string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("Eval(%q) panicked: %v", path, r)
			}
		}()
		if got, err := Eval(doc, path); err != nil && len(got) != 0 {
			t.Fatalf("Eval(%q) returned both a result and an error", path)
		}
	}
	// every prefix of some valid paths
	for _, full := range []string{"$..a['b c'][12][*].d", "$.store.book[*]['title']..x", "$['a'][0]..b", "$[99999999999999999999]"} {
		for i := 0; i <= len(full); i++ {
			try(full[:i])
		}
	}
	// random strings that mostly start like a path
	pieces := []string{"$", ".", "..", "[", "]", "'", "*", "a", "b", "0", "1", "9", "-", ",", ":", " ", "é", `"`, `\`, "(", ")", "?", "@"}
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 5000; i++ {
		var b strings.Builder
		b.WriteString("$")
		for j, n := 0, rng.Intn(10); j < n; j++ {
			b.WriteString(pieces[rng.Intn(len(pieces))])
		}
		try(b.String())
	}
}
