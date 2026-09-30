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
		"-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcw\n", "/home/alice/x C:\\Users\\Bob\\y",
		"alice@corp.io 8.8.4.4 2a00:1450:4001:81b::200e", "⟦redacted:secret:abcdef⟧password=⟦redacted:secret:abcdef⟧",
		"key=\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00", "std::vector a::b ::1 fe80::1", `{"token":"abcdefghijklmnop"}`,
		"the api key is Zq8Yw3Xe6Rt1Uy4Io7Pa0Sd2Fg5Hj9Kl", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.abcdefghij",
		"https://u:p4ssw0rd@host/x -H 'X-Api-Key: 7f3a9c1e5b2d4f6a8c0e'", "\xff\xfe\xc3\x28",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	const planted = "AKIAIOSFODNN7EXAMPLE"
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
		`{"a":"\u0000\ud83d\ude00","b":"line\nbreak AKIAIOSFODNN7EXAMPLE"}`, ` [ "a" , "b" ] `, `{"k":"v"`, `{"db_password":"x"}`,
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
	dec := json.NewDecoder(strings.NewReader(doc))
	var ks []string
	depth := []bool{} // true: object, expecting key next
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch v := tok.(type) {
		case json.Delim:
			switch v {
			case '{':
				depth = append(depth, true)
			case '[':
				depth = append(depth, false)
			default:
				depth = depth[:len(depth)-1]
				if n := len(depth); n > 0 && depth[n-1] == false {
					// value finished inside array; nothing to toggle
				}
			}
		case string:
			if n := len(depth); n > 0 && depth[n-1] {
				ks = append(ks, v)
				depth[n-1] = false
				continue
			}
			if n := len(depth); n > 0 {
				// a value: the next token in an object is a key again
				if isObj := objectAt(depth, n-1); isObj {
					depth[n-1] = true
				}
			}
		default:
			if n := len(depth); n > 0 && objectAt(depth, n-1) {
				depth[n-1] = true
			}
		}
	}
	return strings.Join(ks, "\x00")
}

func objectAt(depth []bool, i int) bool { return true }
