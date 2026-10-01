package traj

import (
	"encoding/json"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		run  toolRun
		want string
	}{
		{"stale", toolRun{isErr: true, text: "a.go changed since you last read it (modified by agent be-2); read it again before editing"}, errStale},
		{"never read", toolRun{isErr: true, text: "a.go has not been read by you yet; read it before modifying it"}, errStale},
		{"lease", toolRun{isErr: true, text: "a.go is being edited by be-2 (last write 4s ago). Work on something else"}, errLease},
		{"scope", toolRun{isErr: true, text: "cannot widen scope: scope \"api/\" overlaps T3"}, errScope},
		{"unknown tool", toolRun{isErr: true, text: `unknown tool "frobnicate"`}, errUnknownTool},
		{"invalid json", toolRun{isErr: true, text: "The arguments for edit were not valid JSON (cut off)"}, errInvalid},
		{"invalid args", toolRun{isErr: true, text: "invalid arguments: path must be a string"}, errInvalid},
		{"unknown action", toolRun{isErr: true, text: `unknown action "explode"`}, errInvalid},
		{"meta wins over text", toolRun{isErr: true, text: "whatever", meta: map[string]any{"error_kind": "lease_conflict"}}, errLease},
		{"meta kinds", toolRun{isErr: true, meta: map[string]any{"error_kind": "STALE_WRITE"}}, errStale},
		{"success mentioning the phrase", toolRun{isErr: false, text: "the file changed since you last read it, says the doc"}, ""},
		{"ordinary failure", toolRun{isErr: true, text: "exit status 1: tests failed"}, ""},
		{"empty", toolRun{isErr: true}, ""},
	}
	for _, c := range cases {
		if got := classify(c.run); got != c.want {
			t.Errorf("%s: classify = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSanitizeInput(t *testing.T) {
	if sanitizeInput(nil) != nil {
		t.Fatal("empty input stays empty")
	}
	if got := sanitizeInput(json.RawMessage(`{"a": 1}`)); string(got) != `{"a": 1}` {
		t.Fatalf("valid JSON is kept as recorded: %s", got)
	}
	got := sanitizeInput(json.RawMessage(`{"a": "cut`))
	if !json.Valid(got) || string(got) != `"{\"a\": \"cut"` {
		t.Fatalf("invalid JSON becomes a JSON string: %s", got)
	}
}

func tok(ids ...int32) []int32 { return ids }

func TestStrictPrefix(t *testing.T) {
	prev := &core.TokenTrace{PromptIDs: tok(1, 2, 3), CompletionIDs: tok(4, 5)}
	cases := []struct {
		name string
		next []int32
		want bool
	}{
		{"exact continuation", tok(1, 2, 3, 4, 5, 6, 7), true},
		{"nothing after", tok(1, 2, 3, 4, 5), true},
		{"completion retokenised", tok(1, 2, 3, 9, 6, 7), false},
		{"prompt drifted", tok(1, 9, 3, 4, 5, 6), false},
		{"shorter", tok(1, 2, 3, 4), false},
		{"prompt only", tok(1, 2, 3), false},
		{"empty", nil, false},
	}
	for _, c := range cases {
		if got := strictPrefix(prev, &core.TokenTrace{PromptIDs: c.next}); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if strictPrefix(&core.TokenTrace{CompletionIDs: tok(1)}, &core.TokenTrace{PromptIDs: tok(1, 2)}) {
		t.Error("a trace without prompt ids cannot anchor a chain")
	}
}

func TestParseOutcomeAndDenied(t *testing.T) {
	o := parseOutcome(evOf(`{"kind":"Verifier","pass":true,"verifier_version":"v9","detail":"abc","ms":5}`))
	if o.kind != "verifier" || !o.hasPass || !o.pass || o.hasScore || o.version != "v9" || o.detail != "abc" || o.ms != 5 {
		t.Fatalf("%+v", o)
	}
	o = parseOutcome(evOf(`{"kind":"review","score":0.5}`))
	if o.hasPass || !o.hasScore || o.score != 0.5 {
		t.Fatalf("%+v", o)
	}
	if o := parseOutcome(evOf(`not json`)); o.kind != "" {
		t.Fatalf("garbage: %+v", o)
	}
	for data, want := range map[string]bool{
		`{"allow":false}`: true, `{"allowed":true}`: false, `{"decision":"DENY"}`: true, `{"behavior":"allow"}`: false,
		`{"decision":"ask"}`: false, `{}`: false, `[]`: false, `"deny"`: false, `{"outcome":"blocked"}`: true,
	} {
		if got := denied(json.RawMessage(data)); got != want {
			t.Errorf("denied(%s) = %v, want %v", data, got, want)
		}
	}
}

func TestIsolatedContinuity(t *testing.T) {
	h := func(s ...string) []core.Hash {
		var out []core.Hash
		for _, x := range s {
			out = append(out, core.Hash(x))
		}
		return out
	}
	q := func(hot core.Hash, model string) *reqInfo {
		return &reqInfo{hot: hot, model: model, man: core.Manifest{Tools: "t", System: h("s")}}
	}
	b := &builder{}
	prev := &stepInfo{q: q("", "m"), msgs: h("a", "b", "c")}
	for name, tc := range map[string]struct {
		cur  *stepInfo
		want bool
	}{
		"append":           {&stepInfo{q: q("", "m"), msgs: h("a", "b", "c", "d", "e")}, true},
		"same prompt":      {&stepInfo{q: q("", "m"), msgs: h("a", "b", "c")}, true},
		"tail rewritten":   {&stepInfo{q: q("", "m"), msgs: h("a", "b", "X", "d")}, false},
		"prefix rewritten": {&stepInfo{q: q("", "m"), msgs: h("Z", "b", "c", "d")}, false},
		"shorter":          {&stepInfo{q: q("", "m"), msgs: h("a", "b")}, false},
		"model swap":       {&stepInfo{q: q("", "n"), msgs: h("a", "b", "c", "d")}, false},
		"missing hashes":   {&stepInfo{q: q("", "m")}, false},
		"tools changed":    {&stepInfo{q: &reqInfo{model: "m", man: core.Manifest{Tools: "u", System: h("s")}}, msgs: h("a", "b", "c", "d")}, false},
		"system changed":   {&stepInfo{q: &reqInfo{model: "m", man: core.Manifest{Tools: "t", System: h("s2")}}, msgs: h("a", "b", "c", "d")}, false},
	} {
		if got := b.continues(prev, tc.cur); got != tc.want {
			t.Errorf("%s: continues = %v, want %v", name, got, tc.want)
		}
	}
	// A previous request with a hot tail changed its last message by design.
	hotPrev := &stepInfo{q: q("hot", "m"), msgs: h("a", "b", "c+hot")}
	if !b.continues(hotPrev, &stepInfo{q: q("", "m"), msgs: h("a", "b", "c", "d", "e")}) {
		t.Error("the hot tail is not a rebase")
	}
	if b.continues(hotPrev, &stepInfo{q: q("", "m"), msgs: h("a", "X", "c", "d")}) {
		t.Error("a change below the hot tail is a rebase")
	}
}

func evOf(data string) events.Event {
	return events.Event{Type: "outcome", Data: json.RawMessage(data)}
}
