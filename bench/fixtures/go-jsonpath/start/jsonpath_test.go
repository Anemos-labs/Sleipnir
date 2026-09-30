package jsonpath

import (
	"encoding/json"
	"reflect"
	"testing"
)

func decode(t *testing.T, text string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("bad test document %s: %v", text, err)
	}
	return v
}

// The examples of README.md.
func TestReadmeExamples(t *testing.T) {
	store := decode(t, `{
		"store": {
			"book": [
				{"title": "Sayings", "price": 8.95},
				{"title": "Sword", "price": 12.99, "isbn": "0-553-21311-3"}
			],
			"bicycle": {"color": "red", "price": 19.95}
		}
	}`)
	tests := []struct {
		doc  any
		path string
		want string // the expected result, as a JSON array
	}{
		{store, "$.store.bicycle.color", `["red"]`},
		{store, "$['store']['book'][1].title", `["Sword"]`},
		{store, "$.store.book[*].title", `["Sayings", "Sword"]`},
		{store, "$.store.book[*].isbn", `["0-553-21311-3"]`},
		{store, "$..price", `[19.95, 8.95, 12.99]`},
		{store, "$.store.book[5]", `[]`},
		{store, "$.nothing.here", `[]`},
		{decode(t, `{"a": {"x": 5}, "x": 6}`), "$..x", `[6, 5]`},
		{decode(t, `{"a": {"a": {"b": 1}}}`), "$..a..b", `[1, 1]`},
	}
	for _, tc := range tests {
		got, err := Eval(tc.doc, tc.path)
		if err != nil {
			t.Errorf("Eval(%q): unexpected error %v", tc.path, err)
			continue
		}
		want := decode(t, tc.want).([]any)
		if len(got) != len(want) || (len(want) > 0 && !reflect.DeepEqual(got, want)) {
			t.Errorf("Eval(%q)\n got  %v\n want %v", tc.path, got, want)
		}
	}
}

func TestReadmeWholeDocument(t *testing.T) {
	doc := decode(t, `{"a": [1, 2]}`)
	got, err := Eval(doc, "$")
	if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], doc) {
		t.Errorf(`Eval(doc, "$") = %v, %v; want [doc], nil`, got, err)
	}
}

func TestReadmeSyntaxError(t *testing.T) {
	got, err := Eval(decode(t, `{"store": {}}`), "$.store[")
	if err == nil || len(got) != 0 {
		t.Errorf(`Eval(doc, "$.store[") = %v, %v; want an empty result and an error`, got, err)
	}
}
