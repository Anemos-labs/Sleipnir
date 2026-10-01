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
// was not what was wrong with it: the model wrote "keep_from" and then broke the JSON. The reason names the break.
func TestABrokenPatchIsRefusedAsBrokenNotAsMissingAKey(t *testing.T) {
	reply := `{"keep_from":"t26","spine":[{"turns":"t2-t9","line":"Located workdir via pwd":"guides the JSON layering"},{"turns":"t10-t25","line":"ran the tests"}],"notes":[]}`
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
