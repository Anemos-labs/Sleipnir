package parity

// The keys dimension. Which keys do something is not in a table in either interface: the terminal's keys are a switch in the chat
// model, a table in the prompt's editor and a switch in the watch program, the page's are handlers of the document, of the message
// box, of the session tabs and of the views. So each interface's keys are found by pressing every candidate key in a set of neutral
// states and recording which ones did something:
//
//   - internal/tui/app/keys_parity_test.go writes testdata/keys-terminal.json (go test ./internal/tui/app -run TestKeysParityTerminal -update);
//   - internal/web/uidev/test/keys-parity.test.mjs writes testdata/keys-web.json (node internal/web/uidev/test/keys-parity.test.mjs --update).
//
// Each of those tests fails when its file is stale. This file reads both files and compares them with Differences.Check: a key that
// one interface has and the other lacks fails until it is built there or entered in contract/keys.json with the reason it differs.
// The contract also names the keys that are one action under two spellings (equivalent) and the keys that both interfaces bind to
// different actions (different_meaning). A second test compares the keys of the page with the Keys table of docs/UX.md.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// keysRecord is a testdata/keys-*.json file: what one interface's sweep found.
type keysRecord struct {
	Dimension         string              `json:"dimension"`
	Interface         string              `json:"interface"`
	About             string              `json:"about"`
	Candidates        int                 `json:"candidates"`
	CandidatesSHA256  string              `json:"candidates_sha256"`
	Indistinguishable map[string]string   `json:"indistinguishable,omitempty"`
	States            []string            `json:"states"`
	Keys              map[string][]string `json:"keys"`
}

// keysGroup is several items of one heading that differ for one reason.
type keysGroup struct {
	// Side is the heading the items belong under: terminal_only, web_only or gaps.
	Side   string   `json:"side"`
	Items  []string `json:"items"`
	Reason string   `json:"reason"`
}

// keysEquivalent says that a key of one interface is the other's key under another spelling: the same action. The key that only one
// interface has is then not a difference.
type keysEquivalent struct {
	Terminal string `json:"terminal"`
	Web      string `json:"web"`
	Reason   string `json:"reason"`
}

// keysDifferent names a key that both interfaces bind, to different actions: a set of keys cannot show it, so it is written down.
type keysDifferent struct {
	Item     string `json:"item"`
	Terminal string `json:"terminal"`
	Web      string `json:"web"`
}

// keysContract is contract/keys.json: Differences, and what only keys need.
type keysContract struct {
	Differences
	Groups           []keysGroup      `json:"groups,omitempty"`
	Equivalent       []keysEquivalent `json:"equivalent,omitempty"`
	DifferentMeaning []keysDifferent  `json:"different_meaning,omitempty"`
}

// keysRoot is the root of the repository.
func keysRoot() string { return filepath.Join(Dir(), "..", "..", "..") }

// readKeysRecord reads testdata/<name>.json, refusing a field it does not know.
func readKeysRecord(t testing.TB, name string) keysRecord {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(Dir(), "..", "testdata", name+".json"))
	if err != nil {
		t.Fatalf("%v (written by the test of that interface, see the head of keys_test.go)", err)
	}
	var r keysRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		t.Fatalf("%s.json: %v", name, err)
	}
	return r
}

// keyList is the keys of a record that did something, in order.
func (r keysRecord) keyList() []string {
	out := make([]string, 0, len(r.Keys))
	for k, states := range r.Keys {
		if len(states) > 0 {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// has reports whether a record has the key.
func (r keysRecord) has(key string) bool { return len(r.Keys[key]) > 0 }

// keysProblems compares the two records under the contract and returns every problem and the gaps that are open.
func keysProblems(c keysContract, term, web keysRecord) (problems, gaps []string) {
	add := func(format string, args ...any) { problems = append(problems, "keys: "+fmt.Sprintf(format, args...)) }
	for _, r := range []struct {
		rec  keysRecord
		want string
	}{{term, "terminal"}, {web, "web"}} {
		if r.rec.Dimension != "keys" || r.rec.Interface != r.want {
			add("keys-%s.json says it is the %s file of the %s interface; it must be the keys file of the %s interface", r.want, r.rec.Dimension, r.rec.Interface, r.want)
		}
	}
	if term.CandidatesSHA256 != web.CandidatesSHA256 || term.Candidates != web.Candidates {
		add("the terminal pressed %d candidate keys (%s) and the page %d (%s): both must press the same list. Edit keyCandidates in internal/tui/app/keys_parity_test.go and candidates() in internal/web/uidev/test/keys-parity.test.mjs together, then write both files again", term.Candidates, term.CandidatesSHA256, web.Candidates, web.CandidatesSHA256)
	}
	tl, wl := term.keyList(), web.keyList()

	d := c.Differences
	for _, g := range c.Groups {
		if len(g.Items) == 0 {
			add("a group under %q has no items", g.Side)
		}
		for _, item := range g.Items {
			e := Entry{Item: item, Reason: g.Reason}
			switch g.Side {
			case "terminal_only":
				d.TerminalOnly = append(d.TerminalOnly, e)
			case "web_only":
				d.WebOnly = append(d.WebOnly, e)
			case "gaps":
				d.Gaps = append(d.Gaps, e)
			default:
				add("a group names the side %q: it must be terminal_only, web_only or gaps", g.Side)
			}
		}
	}

	// A key that is the other interface's key under another spelling is no difference: it is taken out of the list of its side.
	drop := func(list []string, key string) []string {
		out := list[:0:0]
		for _, k := range list {
			if k != key {
				out = append(out, k)
			}
		}
		return out
	}
	for _, e := range c.Equivalent {
		switch {
		case len(strings.TrimSpace(e.Reason)) < minReason:
			add("the equivalent %q = %q needs a reason (at least %d characters)", e.Terminal, e.Web, minReason)
		case !term.has(e.Terminal) || !web.has(e.Web):
			add("the equivalent %q = %q is stale: %s", e.Terminal, e.Web, map[bool]string{true: "the terminal no longer has " + e.Terminal, false: "the page no longer has " + e.Web}[!term.has(e.Terminal)]+"; remove it")
		case web.has(e.Terminal) && term.has(e.Web):
			add("the equivalent %q = %q is stale: both interfaces have both keys; remove it", e.Terminal, e.Web)
		}
		if !web.has(e.Terminal) {
			tl = drop(tl, e.Terminal)
		}
		if !term.has(e.Web) {
			wl = drop(wl, e.Web)
		}
	}
	for _, e := range c.DifferentMeaning {
		switch {
		case len(strings.TrimSpace(e.Terminal)) < minReason || len(strings.TrimSpace(e.Web)) < minReason:
			add("%q is said to mean different things; say what it does in each interface (at least %d characters each)", e.Item, minReason)
		case !term.has(e.Item) || !web.has(e.Item):
			add("%q under different_meaning is stale: it is not a key of both interfaces any more; remove it", e.Item)
		}
	}
	problems = append(problems, d.Check("keys", tl, wl)...)
	return problems, d.GapLines("keys")
}

// loadKeysContract reads contract/keys.json.
func loadKeysContract(t testing.TB) keysContract {
	t.Helper()
	var c keysContract
	if err := Load("keys", &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// The keys that do something in the terminal interface and in the web interface are the same, but for the differences the contract
// lists, each with its reason. The open gaps are logged on every run.
func TestKeysTerminalAndWebAgree(t *testing.T) {
	term, web := readKeysRecord(t, "keys-terminal"), readKeysRecord(t, "keys-web")
	c := loadKeysContract(t)
	problems, gaps := keysProblems(c, term, web)
	for _, g := range gaps {
		t.Log(g)
	}
	for _, p := range problems {
		t.Error(p)
	}
	t.Logf("keys: %d in the terminal interface, %d in the web interface, %d in both", len(term.keyList()), len(web.keyList()), keysBoth(term.keyList(), web.keyList()))
}

// keysBoth counts the keys two sorted lists share.
func keysBoth(a, b []string) int {
	set := map[string]bool{}
	for _, k := range a {
		set[k] = true
	}
	n := 0
	for _, k := range b {
		if set[k] {
			n++
		}
	}
	return n
}

// ---- the Keys table of docs/UX.md ----

var (
	keysSpan  = regexp.MustCompile("`([^`]+)`|\\bthen\\b|\\bto\\b")
	keysArrow = strings.NewReplacer("↑", "up", "↓", "down", "←", "left", "→", "right")
)

// documentedKeys reads the first column of the Keys table of a Markdown document: each key is a code span, spelled as people write it
// (Enter, ctrl+K, shift+←); "`alt+1` to `alt+9`" is the nine keys between, and the spans after "`g` then" are chords of that prefix
// ("g c"). It returns the keys canonically spelled (lowercase, arrows by name) and what in the table it could not read.
func documentedKeys(md string) (keys []string, problems []string) {
	lines := strings.Split(md, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "### Keys" {
			start = i + 1
		}
	}
	if start < 0 {
		return nil, []string{"there is no \"### Keys\" heading"}
	}
	seen := map[string]bool{}
	add := func(k string) {
		if !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	rows := 0
	for _, l := range lines[start:] {
		l = strings.TrimSpace(l)
		if l == "" && rows == 0 {
			continue
		}
		if !strings.HasPrefix(l, "|") {
			break
		}
		cells := strings.Split(strings.Trim(l, "|"), "|")
		first := strings.TrimSpace(cells[0])
		rows++
		if rows <= 2 || strings.Trim(first, "-: ") == "" { // the header and its rule
			continue
		}
		var prev, prefix string
		inRange := false
		for _, m := range keysSpan.FindAllStringSubmatch(first, -1) {
			switch m[0] {
			case "then":
				prefix = prev
			case "to":
				inRange = true
			default:
				span := strings.TrimSpace(m[1])
				if strings.ContainsAny(span, " \t") {
					problems = append(problems, fmt.Sprintf("%q holds several keys: put each in code spans of its own", span))
					continue
				}
				k := strings.ToLower(keysArrow.Replace(span))
				if inRange {
					inRange = false
					a, b := prev, k
					if len(a) < 2 || len(b) < 2 || a[:len(a)-1] != b[:len(b)-1] || a[len(a)-1] > b[len(b)-1] {
						problems = append(problems, fmt.Sprintf("%q to %q is not a range of keys that differ in the last character", prev, k))
						continue
					}
					for c := a[len(a)-1]; c <= b[len(b)-1]; c++ {
						add(a[:len(a)-1] + string(c))
					}
					prev = k
					continue
				}
				if prefix != "" && k != prefix {
					add(prefix + " " + k)
				} else {
					add(k)
				}
				prev = k
			}
		}
	}
	if len(keys) == 0 {
		problems = append(problems, "the Keys table has no keys in code spans")
	}
	sort.Strings(keys)
	return keys, problems
}

// docsProblems compares the keys the page does something with and the keys its documentation lists.
func docsProblems(web keysRecord, md string) []string {
	doc, problems := documentedKeys(md)
	inDoc := map[string]bool{}
	var unbound, undocumented []string
	for _, k := range doc {
		inDoc[k] = true
		if !web.has(k) {
			unbound = append(unbound, fmt.Sprintf("%q", k))
		}
	}
	for _, k := range web.keyList() {
		if !inDoc[k] {
			undocumented = append(undocumented, fmt.Sprintf("%q", k))
		}
	}
	if len(unbound) > 0 {
		problems = append(problems, "the Keys table of docs/UX.md lists keys the page does nothing with, in any state of internal/parity/testdata/keys-web.json: "+strings.Join(unbound, " ")+". Bind them, or take them out of the table")
	}
	if len(undocumented) > 0 {
		problems = append(problems, "the page does something with keys that the Keys table of docs/UX.md does not list (internal/parity/testdata/keys-web.json): "+strings.Join(undocumented, " ")+". Add them to the table, each in a code span of its own, spelled as people write it (Enter, ctrl+K, shift+←)")
	}
	return problems
}

// Every key the page does something with is in the Keys table of docs/UX.md, and every key in the table is one the page does something
// with.
func TestKeysOfThePageAreDocumented(t *testing.T) {
	md, err := os.ReadFile(filepath.Join(keysRoot(), "docs", "UX.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range docsProblems(readKeysRecord(t, "keys-web"), string(md)) {
		t.Error(p)
	}
}

// ---- the tests of the test ----

// keysRec makes a record of the given keys.
func keysRec(iface string, keys ...string) keysRecord {
	r := keysRecord{Dimension: "keys", Interface: iface, Candidates: 3, CandidatesSHA256: "abc", States: []string{"s"}, Keys: map[string][]string{}}
	for _, k := range keys {
		r.Keys[k] = []string{"s"}
	}
	return r
}

// keysJoined is the problems, one to a line.
func keysJoined(p []string) string { return strings.Join(p, "\n") }

// A key that one interface has and the other lacks fails and names the file to edit; the contract settles it, with a reason; and a
// listed key that no longer differs fails.
func TestKeysCheckOnSyntheticSets(t *testing.T) {
	term, web := keysRec("terminal", "a", "b", "t"), keysRec("web", "a", "b", "w")
	got, _ := keysProblems(keysContract{}, term, web)
	for _, want := range []string{`"t" exists in the terminal interface and not in the web interface`, `"w" exists in the web interface and not in the terminal interface`, "contract/keys.json"} {
		if !strings.Contains(keysJoined(got), want) {
			t.Errorf("missing %q in:\n%s", want, keysJoined(got))
		}
	}
	c := keysContract{
		Differences: Differences{Gaps: []Entry{{"w", "the terminal has to learn this key still"}}},
		Groups:      []keysGroup{{Side: "terminal_only", Items: []string{"t"}, Reason: "the terminal edits text itself"}},
	}
	if got, gaps := keysProblems(c, term, web); len(got) != 0 || len(gaps) != 1 {
		t.Errorf("a grouped difference and a gap: problems %v, gaps %v", got, gaps)
	}
	c.Groups[0].Items = []string{"t", "a"}
	if got, _ := keysProblems(c, term, web); !strings.Contains(keysJoined(got), `"a" under terminal_only is stale`) {
		t.Errorf("a key both have is no difference: %v", got)
	}
	c.Groups[0] = keysGroup{Side: "elsewhere", Items: []string{"t"}, Reason: "a heading that is not one"}
	if got, _ := keysProblems(c, term, web); !strings.Contains(keysJoined(got), "must be terminal_only, web_only or gaps") {
		t.Errorf("an unknown side: %v", got)
	}
}

// Equivalent spellings: the key only one interface has is no difference when it is the other's key under another name; an entry that
// names a key nobody has, or two keys both have, is refused.
func TestKeysEquivalentSpellings(t *testing.T) {
	term, web := keysRec("terminal", "ctrl+g"), keysRec("web", "ctrl+g", "alt+g")
	if got, _ := keysProblems(keysContract{}, term, web); !strings.Contains(keysJoined(got), `"alt+g" exists in the web interface`) {
		t.Fatalf("without the entry alt+g is a difference: %v", got)
	}
	c := keysContract{Equivalent: []keysEquivalent{{Terminal: "ctrl+g", Web: "alt+g", Reason: "browsers keep ctrl+g on some platforms"}}}
	if got, _ := keysProblems(c, term, web); len(got) != 0 {
		t.Errorf("an equivalent spelling is no difference: %v", got)
	}
	c.Equivalent[0].Web = "alt+x"
	if got, _ := keysProblems(c, term, web); !strings.Contains(keysJoined(got), "the page no longer has alt+x") {
		t.Errorf("a key nobody has: %v", got)
	}
	c.Equivalent[0] = keysEquivalent{Terminal: "ctrl+g", Web: "ctrl+g", Reason: "the same key is not a spelling"}
	if got, _ := keysProblems(c, term, web); !strings.Contains(keysJoined(got), "both interfaces have both keys") {
		t.Errorf("a key both have: %v", got)
	}
	c.Equivalent[0].Reason = "no"
	if got, _ := keysProblems(c, term, web); !strings.Contains(keysJoined(got), "needs a reason") {
		t.Errorf("a reason is needed: %v", got)
	}
}

// The two sweeps must have pressed the same candidates; a key bound to different actions is written down and must be a key of both.
func TestKeysRecordsMustMatchAndMeaningsAreStillTrue(t *testing.T) {
	term, web := keysRec("terminal", "a"), keysRec("web", "a")
	web.CandidatesSHA256 = "def"
	if got, _ := keysProblems(keysContract{}, term, web); !strings.Contains(keysJoined(got), "both must press the same list") {
		t.Errorf("different candidates: %v", got)
	}
	web.CandidatesSHA256 = "abc"
	web.Interface = "terminal"
	if got, _ := keysProblems(keysContract{}, term, web); !strings.Contains(keysJoined(got), "keys-web.json says it is the keys file of the terminal interface") {
		t.Errorf("a file of the wrong interface: %v", got)
	}
	web.Interface = "web"
	c := keysContract{DifferentMeaning: []keysDifferent{{Item: "a", Terminal: "the terminal does one thing", Web: "the page does another"}}}
	if got, _ := keysProblems(c, term, web); len(got) != 0 {
		t.Errorf("a key both bind: %v", got)
	}
	c.DifferentMeaning[0].Item = "z"
	if got, _ := keysProblems(c, term, web); !strings.Contains(keysJoined(got), `"z" under different_meaning is stale`) {
		t.Errorf("a key of neither: %v", got)
	}
}

// The reader of the Keys table: spans, ranges, chords, arrows; and the comparison with the page in both directions.
func TestKeysTableReader(t *testing.T) {
	md := "# x\n\n### Keys\n\n| Keys | Action |\n|---|---|\n| `Enter`, `shift+Tab` | send |\n| `↑` `↓`, `ctrl+K` | history |\n| `alt+1` to `alt+9` | sessions |\n| `g` then `c` `r` | views |\n| `shift+←` | seek |\n\nprose with `code` after the table\n"
	got, problems := documentedKeys(md)
	want := []string{"alt+1", "alt+2", "alt+3", "alt+4", "alt+5", "alt+6", "alt+7", "alt+8", "alt+9", "ctrl+k", "down", "enter", "g", "g c", "g r", "shift+left", "shift+tab", "up"}
	if len(problems) != 0 || strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("keys %v, problems %v\nwant %v", got, problems, want)
	}
	if _, p := documentedKeys("no table"); len(p) == 0 {
		t.Error("a document without the table")
	}
	if _, p := documentedKeys("### Keys\n\n| Keys | Action |\n|---|---|\n| `ctrl+K and more` | x |\n"); !strings.Contains(keysJoined(p), "holds several keys") {
		t.Errorf("a span of several keys: %v", p)
	}
	if _, p := documentedKeys("### Keys\n\n| Keys | Action |\n|---|---|\n| `alt+1` to `ctrl+9` | x |\n"); !strings.Contains(keysJoined(p), "not a range") {
		t.Errorf("a range of different keys: %v", p)
	}
	web := keysRec("web", "enter", "up", "ctrl+k", "f9")
	p := keysJoined(docsProblems(web, "### Keys\n\n| Keys | Action |\n|---|---|\n| `Enter`, `Up`, `ctrl+K`, `F5` | x |\n"))
	for _, w := range []string{`does nothing with`, `"f5"`, `does not list`, `"f9"`} {
		if !strings.Contains(p, w) {
			t.Errorf("missing %q in:\n%s", w, p)
		}
	}
	if strings.Contains(p, `"enter"`) || strings.Contains(p, `"up"`) {
		t.Errorf("a documented key was reported:\n%s", p)
	}
}
