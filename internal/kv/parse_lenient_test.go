package kv

import (
	"strings"
	"testing"
)

// What real models answered to a compaction request, on a benchmark of real tasks (docs/DOGFOOD.md): a patch that is right in every
// way but one field of the wrong type was refused whole, and the agent compacted mechanically instead of with the model's own digest of
// its work. A field of the wrong type costs that field, with a warning; the patch stays.
func TestAFieldOfTheWrongTypeCostsTheFieldNotThePatch(t *testing.T) {
	for _, c := range []struct {
		name, reply string
		spine, note int
		warn        string
		keepFrom    int64
	}{
		{"notes as one object", `{"keep_from":"t20","spine":[{"turns":"t1-t5","line":"read the parser"}],"notes":{"op":"add","key":"facts","text":"the page size is 20"}}`, 1, 1, "", 20},
		{"notes as an object of lists", `{"keep_from":"t20","spine":[{"turns":"t1-t5","line":"read the parser"}],"notes":{"facts":["a","b"]}}`, 1, 0, "notes", 20},
		{"notes as a string", `{"keep_from":"t20","spine":[{"turns":"t1-t5","line":"read the parser"}],"notes":"hops"}`, 1, 0, "notes", 20},
		{"keep_from as a number", `{"keep_from":20,"spine":[{"turns":"t1-t5","line":"x"}]}`, 1, 0, "", 20},
		{"a spine entry that is a string", `{"keep_from":"t20","spine":["t1-t5: read the parser",{"turns":"t6-t9","line":"ran the tests"}]}`, 1, 0, "spine[0]", 20},
		{"spine as one object", `{"keep_from":"t20","spine":{"turns":"t1-t5","line":"read the parser"}}`, 1, 0, "", 20},
		{"mask with a number in it", `{"keep_from":"t20","spine":[],"mask":["t3.0",4,"t5.1"]}`, 0, 0, "mask", 20},
		{"null fields", `{"keep_from":"t20","spine":null,"mask":null,"notes":null,"promote":null}`, 0, 0, "", 20},
	} {
		p, err := ParsePatch(c.reply)
		if err != nil {
			t.Errorf("%s: the patch was refused: %v", c.name, err)
			continue
		}
		if int64(p.KeepFrom) != c.keepFrom || len(p.Spine) != c.spine || len(p.Notes) != c.note {
			t.Errorf("%s: keep_from %d, %d spine lines, %d notes; want %d, %d, %d (%v)", c.name, p.KeepFrom, len(p.Spine), len(p.Notes), c.keepFrom, c.spine, c.note, p.Warnings)
		}
		if c.warn != "" && !strings.Contains(strings.Join(p.Warnings, "|"), c.warn) {
			t.Errorf("%s: no warning names %q: %v", c.name, c.warn, p.Warnings)
		}
	}
	// what is not a patch is still refused, with the reason that is true
	for reply, want := range map[string]string{
		`{"keep_from":["t1"],"spine":[]}`:        "keep_from",
		`{"keep_from":"","spine":[]}`:            "no keep_from",
		`{"spine":[{"turns":"t1","line":"x"}]}`:  "no keep_from",
		`{"keep_from":"t20","spine":[{"turns":`:  "unterminated",
		`{"keep_from":"t20","spine":[],}`:        "not valid JSON",
		`no json at all`:                         "no JSON object",
		`{"keep_from":"t3","spine":{"turns":"}}`: "",
	} {
		_, err := ParsePatch(reply)
		if err == nil {
			t.Errorf("%q must be refused", reply)
			continue
		}
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Errorf("%q: refused with %q, want a reason that says %q", reply, err, want)
		}
	}
}

// A reply whose outer object is malformed but holds objects that decode (the spine's entries) was refused as having "no keep_from", which
// was not what was wrong with it: the model wrote "keep_from" and then broke the JSON. The reason names the break. (The example used to
// be "line":"Located workdir via pwd":"guides …", an unescaped quote, which is now read as text: a missing comma stays broken.)
func TestABrokenPatchIsRefusedAsBrokenNotAsMissingAKey(t *testing.T) {
	reply := `{"keep_from":"t26","spine":[{"turns":"t2-t9" "line":"Located workdir via pwd"},{"turns":"t10-t25","line":"ran the tests"}],"notes":[]}`
	_, err := ParsePatch(reply)
	if err == nil {
		t.Fatal("a broken patch was accepted")
	}
	if strings.Contains(err.Error(), "no keep_from") || !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("%v: the patch has a keep_from and is broken; say so", err)
	}
	// and what it was before still is: valid JSON that never names where to keep from
	if _, err := ParsePatch(`{"spine":[{"turns":"t1","line":"x"}],"notes":[]}`); err == nil || !strings.Contains(err.Error(), "no keep_from") {
		t.Errorf("valid JSON without keep_from: %v", err)
	}
}

// The commonest broken patch of a 32-worker run (about 70 of 260 refusals): a quote inside a string the model did not escape, as in
// "line": "fixed the "foo" handler". Each such quote is read as text and the patch kept, with a warning; JSON broken any other way is
// still refused, and so is a reply that would need more repairs than the bound.
func TestAnUnescapedQuoteInsideAStringIsReadAsText(t *testing.T) {
	for _, c := range []struct {
		name, reply, line string
		mask, fixes       int
	}{
		{"a quoted word", `{"keep_from":"t9","spine":[{"turns":"t1-t8","line":"fixed the "foo" handler"}]}`, `fixed the "foo" handler`, 0, 2},
		{"a colon after it", `{"keep_from":"t9","spine":[{"turns":"t1-t8","line":"set "port": 8080 in config"}]}`, `set "port": 8080 in config`, 0, 2},
		{"a dash after it", `{"keep_from":"t9","spine":[{"turns":"t1-t8","line":"ran "go test" — 3 failures"}]}`, `ran "go test" — 3 failures`, 0, 2},
		{"after an escaped backslash", `{"keep_from":"t9","spine":[{"turns":"t1-t8","line":"path C:\\"tmp" done"}]}`, `path C:\"tmp" done`, 0, 2},
		{"keep_from after a long line", `{"spine":[{"turns":"t1-t8","line":"ran "go vet" over every package of the module, then the race tests, and wrote down what failed"}],"keep_from":"t9"}`, `ran "go vet" over every package of the module, then the race tests, and wrote down what failed`, 0, 2},
		{"in two entries", `{"keep_from":"t9","spine":[{"turns":"t1-t8","line":"x"}],"mask":["t3.0"],"notes":[{"op":"add","text":"the "api" key is in .env"}]}`, "x", 1, 2},
		{"in a fence, after prose", "Here is the patch:\n```json\n" + `{"keep_from":"t9","spine":[{"turns":"t1-t8","line":"read "a" and "b""}]}` + "\n```", `read "a" and "b"`, 0, 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := ParsePatch(c.reply)
			if err != nil {
				t.Fatal(err)
			}
			if p.KeepFrom != 9 || len(p.Spine) != 1 || p.Spine[0].Line != c.line || len(p.Mask) != c.mask {
				t.Fatalf("parsed %+v", p)
			}
			if w := strings.Join(p.Warnings, "; "); !strings.Contains(w, "unescaped quote") {
				t.Errorf("no warning for the repair: %q", w)
			}
		})
	}
	if p, err := ParsePatch(`{"keep_from":"t9","spine":[{"turns":"t1-t8","line":"fine"}]}`); err != nil || strings.Contains(strings.Join(p.Warnings, ";"), "unescaped") {
		t.Errorf("a valid patch: %v %v", err, p)
	}
	for _, bad := range []string{
		`{"keep_from":"t9","spine":[{"turns":"t1-t8","line":"x"},]}`,
		`{"keep_from":"t9","spine":[{"turns":"t1-t8" "line":"x"}]}`, // a missing comma is no quote of the model's text
		`{"keep_from":"t9","spine":[{"turns":"t1-t8","line":"` + strings.Repeat(`a "b" `, 20) + `"}]}`,
	} {
		if p, err := ParsePatch(bad); err == nil {
			t.Errorf("%q was accepted: %+v", bad, p)
		}
	}
}
