package redact

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzRedact checks the contract on arbitrary text: no panic, determinism,
// idempotence, and that a planted secret never survives.
func FuzzRedact(f *testing.F) {
	seeds := []string{
		"", "password=hunter2hunter2", "Authorization: Bearer abc123def456ghi789jkl",
		pemBegin("") + "\n" + pemBody + "\n", "/home/alice/x C:\\Users\\Bob\\y",
		"alice@corp.io 8.8.4.4 2a00:1450:4001:81b::200e", "⟦redacted:secret:abcdef⟧password=⟦redacted:secret:abcdef⟧",
		"key=\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00", "std::vector a::b ::1 fe80::1", `{"token":"abcdefghijklmnop"}`,
		"the api key is Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl", jwtTok,
		"https://u:p4ssw0rd@host/x -H 'X-Api-Key: 7f3a9c1e5b2d4f6a8c0e'", "\xff\xfe\xc3\x28",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	planted := awsKey
	f.Fuzz(func(t *testing.T, in string) {
		r1, r2 := New(Config{Salt: "fz"}), New(Config{Salt: "fz"})
		out := r1.String(in)
		if out2 := r2.String(in); out2 != out {
			t.Fatalf("not deterministic:\n%q\n%q", out, out2)
		}
		if again := r1.String(out); again != out {
			t.Fatalf("not idempotent:\n in: %q\n1: %q\n2: %q", in, out, again)
		}
		if utf8.ValidString(in) && !utf8.ValidString(out) {
			t.Fatalf("valid UTF-8 became invalid: %q -> %q", in, out)
		}
		// A key on its own lines is found whatever surrounds it.
		wrapped := in + "\n" + planted + "\n" + in
		if strings.Contains(r1.String(wrapped), planted) {
			t.Fatalf("planted key survived in %q", wrapped)
		}
	})
}

// FuzzRedactJSON checks that JSON redaction keeps documents valid, keeps every
// key, and is idempotent.
func FuzzRedactJSON(f *testing.F) {
	for _, s := range []string{
		`{}`, `[]`, `"x"`, `{"password":"hunter2hunter2","n":[1,2,{"a":"/home/alice/x"}]}`,
		`{"a":"\u0000\ud83d\ude00","b":"line\nbreak ` + awsKey + `"}`, ` [ "a" , "b" ] `, `{"k":"v"`, `{"db_password":"x"}`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		r := New(Config{Salt: "fz"})
		out := r.JSON(json.RawMessage(in))
		if !json.Valid([]byte(in)) {
			if string(out) != in {
				t.Fatalf("invalid JSON must come back unchanged: %q -> %q", in, out)
			}
			return
		}
		if !json.Valid(out) {
			t.Fatalf("redaction broke JSON: %q -> %q", in, out)
		}
		if keys(in) != keys(string(out)) {
			t.Fatalf("keys changed: %q -> %q", in, out)
		}
		if again := r.JSON(out); string(again) != string(out) {
			t.Fatalf("not idempotent: %q -> %q -> %q", in, out, again)
		}
	})
}

// keys lists the object keys of a document in order.
func keys(doc string) string {
	type frame struct{ object, expectKey bool }
	dec := json.NewDecoder(strings.NewReader(doc))
	var ks []string
	var stack []frame
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].object {
			stack[n-1].expectKey = true
		}
	}
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				stack = append(stack, frame{object: true, expectKey: true})
			case '[':
				stack = append(stack, frame{})
			default:
				stack = stack[:len(stack)-1]
				valueDone()
			}
		case string:
			if n := len(stack); n > 0 && stack[n-1].object && stack[n-1].expectKey {
				ks = append(ks, v)
				stack[n-1].expectKey = false
				continue
			}
			valueDone()
		default:
			valueDone()
		}
	}
	return strings.Join(ks, "\x00")
}
