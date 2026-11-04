package tools

import (
	"encoding/json"
	"testing"
)

func TestCoerceListsMakesAStringIntoTheListItStandsFor(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"files":{"type":"array","items":{"type":"string"}},` +
		`"edits":{"type":"array","items":{"type":"object"}},"title":{"type":"string"}}}`)
	cases := []struct{ in, want string }{
		{`{"files":"a.go"}`, `{"files":["a.go"]}`},
		{`{"files":"[\"a.go\", \"b.go\"]"}`, `{"files":["a.go","b.go"]}`},
		{`{"files":"src/*.{js,ts}"}`, `{"files":["src/*.{js,ts}"]}`}, // never split: a glob holds commas
		{`{"files":""}`, `{"files":[]}`},
		{`{"files":"[not json"}`, `{"files":["[not json"]}`},
	}
	for _, c := range cases {
		out, changed := CoerceLists(schema, json.RawMessage(c.in))
		if !changed || !sameJSON(t, out, c.want) {
			t.Errorf("CoerceLists(%s) = %s, %v; want %s", c.in, out, changed, c.want)
		}
	}
	for _, in := range []string{
		`{"files":["a.go"]}`, // already a list
		`{"edits":"x"}`,      // a list of objects is not guessed at
		`{"title":"a.go"}`,   // a string argument stays one
		`{"other":"a.go"}`,   // not in the schema
		`{"files":3}`,        // not a string: the tool reports it
		`not json`, ``,
	} {
		if out, changed := CoerceLists(schema, json.RawMessage(in)); changed || string(out) != in {
			t.Errorf("CoerceLists(%s) = %s, %v; want it unchanged", in, out, changed)
		}
	}
}

func sameJSON(t *testing.T, got json.RawMessage, want string) bool {
	t.Helper()
	var a, b any
	if json.Unmarshal(got, &a) != nil || json.Unmarshal([]byte(want), &b) != nil {
		return false
	}
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
