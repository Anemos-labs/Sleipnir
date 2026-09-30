package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

func TestCallID(t *testing.T) {
	tests := []struct {
		in   string
		want int64
		ok   bool
	}{
		{`5`, 5, true},
		{`"5"`, 5, true}, // numeric string: a common server bug, tolerated
		{`5.0`, 5, true},
		{` 7 `, 7, true},
		{`0`, 0, false},
		{`-3`, 0, false},
		{`1.5`, 0, false},
		{`"abc"`, 0, false},
		{`null`, 0, false},
		{`true`, 0, false},
		{`{}`, 0, false},
		{``, 0, false},
		{`"9999999999999999999999"`, 0, false},
	}
	for _, tt := range tests {
		got, ok := callID(json.RawMessage(tt.in))
		if got != tt.want || ok != tt.ok {
			t.Errorf("callID(%s) = %d,%v want %d,%v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestMarshalRequestIsStrict(t *testing.T) {
	b, err := marshalRequest(7, "tools/call", map[string]any{"name": "x"})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"x"}}` {
		t.Errorf("got %s", b)
	}
	b, _ = marshalRequest(1, "ping", nil)
	if string(b) != `{"jsonrpc":"2.0","id":1,"method":"ping"}` {
		t.Errorf("no params must mean no params member: %s", b)
	}
	// Pretty-printed raw params (a model's arguments) are compacted: one line on stdio.
	b, err = marshalRequest(2, "x", json.RawMessage("{\n  \"a\": 1,\n  \"b\": [1,\n 2]\n}"))
	if err != nil || bytes.ContainsRune(b, '\n') {
		t.Errorf("raw params not compacted: %q %v", b, err)
	}
	if _, err := marshalRequest(3, "x", json.RawMessage(`{"a":`)); err == nil {
		t.Error("invalid raw params accepted")
	}
	n, _ := marshalNotification("notifications/initialized", nil)
	if string(n) != `{"jsonrpc":"2.0","method":"notifications/initialized"}` {
		t.Errorf("notification: %s", n)
	}
	r, _ := marshalResult(json.RawMessage(`"abc"`), struct{}{})
	if string(r) != `{"jsonrpc":"2.0","id":"abc","result":{}}` {
		t.Errorf("result must echo the id verbatim and carry result: %s", r)
	}
	e := marshalError(json.RawMessage(` 12 `), -32601, "no \"such\" method\n")
	var env envelope
	if err := json.Unmarshal(e, &env); err != nil || env.Error == nil || env.Error.Code != -32601 || string(bytes.TrimSpace(env.ID)) != "12" {
		t.Errorf("error response: %s %v", e, err)
	}
	if e := marshalError(nil, -1, "x"); !strings.Contains(string(e), `"id":null`) {
		t.Errorf("missing id must be null: %s", e)
	}
}

func skim(s string) (json.RawMessage, bool, bool) {
	var k skimmer
	k.feed([]byte(s))
	return k.result()
}

func TestSkimmerFindsIDWhereverItIs(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		id         string
		hasMethod  bool
		wantNoID   bool
		wantNotObj bool
	}{
		{"id first", `{"jsonrpc":"2.0","id":5,"result":{"a":1}}`, `5`, false, false, false},
		{"id last (result first, like the TypeScript SDK)", `{"result":{"content":[{"type":"text","text":"x"}]},"jsonrpc":"2.0","id":42}`, `42`, false, false, false},
		{"id in the middle", `{"jsonrpc":"2.0","result":{"a":[1,2,{"id":99}]},"id":7,"x":1}`, `7`, false, false, false},
		{"string id", `{"jsonrpc":"2.0","id":"abc-1","result":{}}`, `"abc-1"`, false, false, false},
		{"string id with escape", `{"id":"a\"b","result":1}`, `"a\"b"`, false, false, false},
		{"nested id is not the id", `{"result":{"id":123},"jsonrpc":"2.0"}`, ``, false, true, false},
		{"id inside array of objects", `{"result":[{"id":1},{"id":2}],"id":3}`, `3`, false, false, false},
		{"braces inside strings", `{"result":{"text":"} ] { [ \" }"},"id":8}`, `8`, false, false, false},
		{"escaped backslash before quote", `{"result":{"t":"a\\"},"id":9}`, `9`, false, false, false},
		{"whitespace everywhere", "  {\n \"result\" :  { } ,\n\t\"id\" :\n 11 \n}", `11`, false, false, false},
		{"method present means not a response", `{"jsonrpc":"2.0","id":3,"method":"sampling/createMessage","params":{}}`, `3`, true, false, false},
		{"notification", `{"jsonrpc":"2.0","method":"notifications/x"}`, ``, true, true, false},
		{"null id", `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse"}}`, ``, false, true, false},
		{"object id is unusable", `{"id":{"a":1},"result":1}`, ``, false, true, false},
		{"array is not an object", `[{"id":1}]`, ``, false, true, true},
		{"garbage", `hello`, ``, false, true, true},
		{"negative number", `{"id":-5,"result":1}`, `-5`, false, false, false},
		{"float id", `{"id":1.5,"result":1}`, `1.5`, false, false, false},
		{"long string id is refused", `{"id":"` + strings.Repeat("a", 200) + `","result":1}`, ``, false, true, false},
		{"key that is a prefix of id", `{"idx":1,"id":2}`, `2`, false, false, false},
		{"escaped key is not treated as id", `{"id":1,"id":2}`, `2`, false, false, false},
		{"id at the very end without closing brace", `{"result":1,"id":6`, `6`, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, hasMethod, ok := skim(tt.in)
			if tt.wantNotObj {
				if ok {
					t.Fatalf("ok for a non-object")
				}
				return
			}
			if !ok {
				t.Fatal("not recognised as an object")
			}
			if hasMethod != tt.hasMethod {
				t.Errorf("hasMethod = %v", hasMethod)
			}
			if tt.wantNoID {
				if id != nil {
					t.Errorf("id = %s, want none", id)
				}
				return
			}
			if string(id) != tt.id {
				t.Errorf("id = %s, want %s", id, tt.id)
			}
		})
	}
}

func TestSkimmerAcceptsSplitInput(t *testing.T) {
	msg := `{"result":{"a":"} x {"},"jsonrpc":"2.0","id":314}`
	for chunk := 1; chunk <= len(msg); chunk++ {
		var k skimmer
		for i := 0; i < len(msg); i += chunk {
			k.feed([]byte(msg[i:min(i+chunk, len(msg))]))
		}
		id, _, ok := k.result()
		if !ok || string(id) != "314" {
			t.Fatalf("chunk %d: %s %v", chunk, id, ok)
		}
	}
}

// The skimmer must agree with a real JSON parser about the top-level id of any
// object, however the object is shaped.
func TestSkimmerAgreesWithEncodingJSON(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var gen func(depth int) any
	gen = func(depth int) any {
		switch n := rng.Intn(8); {
		case n == 0 && depth < 4:
			m := map[string]any{}
			for i := rng.Intn(4); i > 0; i-- {
				m[randKey(rng)] = gen(depth + 1)
			}
			return m
		case n == 1 && depth < 4:
			a := make([]any, rng.Intn(4))
			for i := range a {
				a[i] = gen(depth + 1)
			}
			return a
		case n == 2:
			return randString(rng)
		case n == 3:
			return rng.Intn(1000)
		case n == 4:
			return rng.Float64()
		case n == 5:
			return nil
		case n == 6:
			return rng.Intn(2) == 0
		}
		return randString(rng)
	}
	for i := 0; i < 3000; i++ {
		obj := map[string]any{}
		for j := rng.Intn(6); j > 0; j-- {
			obj[randKey(rng)] = gen(0)
		}
		switch rng.Intn(3) {
		case 0:
			obj["id"] = rng.Intn(100000)
		case 1:
			obj["id"] = randString(rng)
		}
		if rng.Intn(4) == 0 {
			obj["method"] = "m"
		}
		var b []byte
		if rng.Intn(2) == 0 {
			b, _ = json.Marshal(obj)
		} else {
			b, _ = json.MarshalIndent(obj, "", "  ")
		}
		id, hasMethod, ok := skim(string(b))
		if !ok {
			t.Fatalf("object not recognised: %s", b)
		}
		_, wantMethod := obj["method"]
		if hasMethod != wantMethod {
			t.Fatalf("hasMethod=%v want %v for %s", hasMethod, wantMethod, b)
		}
		want, has := obj["id"]
		if !has {
			if id != nil {
				t.Fatalf("invented id %s for %s", id, b)
			}
			continue
		}
		wantJSON, _ := json.Marshal(want)
		_, isStr := want.(string)
		isNum := false
		switch want.(type) {
		case int, float64:
			isNum = true
		}
		switch {
		case !isStr && !isNum:
			if id != nil {
				t.Fatalf("id %s kept for a non-scalar id in %s", id, b)
			}
			continue
		case isStr && len(wantJSON) > skimIDCap:
			if id != nil {
				t.Fatalf("over-long id kept: %s", id)
			}
			continue
		}
		var a, c any
		if err := json.Unmarshal(id, &a); err != nil {
			t.Fatalf("skimmer produced invalid JSON %q for %s", id, b)
		}
		_ = json.Unmarshal(wantJSON, &c)
		if fmt.Sprint(a) != fmt.Sprint(c) {
			t.Fatalf("id = %s, want %s in %s", id, wantJSON, b)
		}
	}
}

func randKey(rng *rand.Rand) string {
	keys := []string{"id", "method", "result", "error", "jsonrpc", "params", "idx", "i", "a\"b", "x{", "y]"}
	return keys[rng.Intn(len(keys))]
}

func randString(rng *rand.Rand) string {
	pieces := []string{"a", "b", `"`, `\`, "{", "}", "[", "]", ":", ",", " ", "id", "\n", "é"}
	var sb strings.Builder
	for i := rng.Intn(6); i > 0; i-- {
		sb.WriteString(pieces[rng.Intn(len(pieces))])
	}
	return sb.String()
}

func FuzzSkimmer(f *testing.F) {
	for _, s := range []string{`{"id":1}`, `{"result":{"id":2},"id":3}`, `[`, `{"id":"`, `{"id":1e5}`, "\x00\xff"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		id, _, _ := skim(s)
		if id != nil && !json.Valid(id) {
			t.Fatalf("invalid id %q from %q", id, s)
		}
	})
}
