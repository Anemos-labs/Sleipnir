package state

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/state/statetest"
)

// The golden tests pin what the State makes of two logs, at three moments of each: the recording of the repository's own demo
// (statetest.DemoLog, a real session of eight agents and 312 events) and the hand-built swarm of scenario_test.go (86 events that
// touch every area: mail, merge queue, leases, permissions, anomalies, compactions, a mailman that goes down). A golden file is
// the whole Snapshot as JSON, laid out for a reader: a change of anything a screen shows fails one named test, and its diff is the
// change. An intended change means reading docs/BUILDING.md's rules for goldens (read the diff; never update to turn a test
// green), and then
//
//	go test ./internal/tui/state -run Golden -update
//
// The recorded demo is a fixture (statetest/demo-4.events.jsonl, how to record it again is written there), so these tests make no
// session and depend on no clock: the same events fold to the same bytes on every machine.
var update = flag.Bool("update", false, "rewrite golden files under testdata/golden")

// golden compares got with testdata/golden/<name> (or rewrites it with -update).
func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", "golden", filepath.FromSlash(name))
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file %s (create it with: go test ./internal/tui/state -run Golden -update): %v", path, err)
	}
	if !bytes.Equal(want, got) {
		t.Fatalf("what the State makes of the log changed: output differs from %s\n%s\n"+
			"If this change is intended, read the diff of testdata/golden, then run: go test ./internal/tui/state -run Golden -update",
			path, goldenDiff(want, got))
	}
}

// goldenDiff names the first line that differs, as it was and as it is, and how many lines differ in all.
func goldenDiff(want, got []byte) string {
	wl, gl := strings.Split(string(want), "\n"), strings.Split(string(got), "\n")
	first, differing := -1, 0
	for i := 0; i < len(wl) || i < len(gl); i++ {
		var w, g string
		if i < len(wl) {
			w = wl[i]
		}
		if i < len(gl) {
			g = gl[i]
		}
		if w != g {
			if first < 0 {
				first = i
			}
			differing++
		}
	}
	if first < 0 {
		return "  the lines are the same but the ends differ"
	}
	at := func(ls []string) string {
		if first < len(ls) {
			return strings.TrimSpace(ls[first])
		}
		return "(end of file)"
	}
	return fmt.Sprintf("  first difference, line %d:\n    was: %s\n    now: %s\n  %d line(s) differ in all", first+1, at(wl), at(gl), differing)
}

// snapshotAt folds the events up to the seq (0: all of them) and renders the Snapshot for a golden file.
func snapshotAt(t *testing.T, evs []events.Event, until uint64) []byte {
	t.Helper()
	st := New()
	for _, e := range evs {
		if until != 0 && e.Seq > until {
			break
		}
		st.Apply(e)
	}
	if p := st.Stats().Panics; p != 0 {
		t.Fatalf("%d panics: %s", p, st.Stats().LastPanic)
	}
	return prettyJSON(t, st.Snapshot())
}

func TestGoldenSnapshotsOfTheRecordedDemoSession(t *testing.T) {
	evs := statetest.DemoEvents()
	for _, c := range []struct {
		name  string
		until uint64
	}{{"demo-4-seq-50.json", 50}, {"demo-4-seq-200.json", 200}, {"demo-4-end.json", 0}} {
		t.Run(strings.TrimSuffix(c.name, ".json"), func(t *testing.T) { golden(t, c.name, snapshotAt(t, evs, c.until)) })
	}
}

func TestGoldenSnapshotsOfTheHandBuiltSwarm(t *testing.T) {
	evs := handBuiltSession()
	for _, c := range []struct {
		name  string
		until uint64
	}{{"handbuilt-seq-30.json", 30}, {"handbuilt-seq-60.json", 60}, {"handbuilt-end.json", 0}} {
		t.Run(strings.TrimSuffix(c.name, ".json"), func(t *testing.T) { golden(t, c.name, snapshotAt(t, evs, c.until)) })
	}
}

// The three moments are three different states (a golden of a state that never moves pins nothing), and the last of the demo is
// the state the loader reaches from the file: Fold, Replay and plain Apply agree on the recording.
func TestTheRecordedDemoFoldsTheSameWayWhateverReadsIt(t *testing.T) {
	evs := statetest.DemoEvents()
	seen := map[string]bool{}
	for _, until := range []uint64{50, 200, 0} {
		seen[string(snapshotAt(t, evs, until))] = true
	}
	if len(seen) != 3 {
		t.Fatalf("%d distinct states at seq 50, 200 and the end", len(seen))
	}
	want := compactJSON(mustSnapshot(t, evs, 0))
	path := writeLog(t, string(statetest.DemoLog()))
	if st, err := Fold(path); err != nil || snapJSON(st) != want {
		t.Errorf("Fold of the file: %v", err)
	}
	for _, until := range []uint64{50, 200} {
		st, err := FoldUntil(path, until)
		if err != nil || snapJSON(st) != compactJSON(mustSnapshot(t, evs, until)) {
			t.Errorf("FoldUntil(%d): %v", until, err)
		}
	}
	// Replay with a virtual clock, in frames of a tenth of a second, to the end, and to seq 200.
	for _, until := range []uint64{0, 200} {
		r, err := Replay(path, ReplayOptions{Speed: 1, Until: until})
		if err != nil {
			t.Fatal(err)
		}
		for n := 0; !r.Done(); n++ {
			if n > 1_000_000 {
				t.Fatal("the replay does not end")
			}
			r.Advance(DefaultFrame)
		}
		if err := r.Err(); err != nil || snapJSON(r.State()) != compactJSON(mustSnapshot(t, evs, until)) {
			t.Errorf("Replay until %d: %v", until, err)
		}
		_ = r.Close()
	}
}

func mustSnapshot(t *testing.T, evs []events.Event, until uint64) *Snapshot {
	t.Helper()
	st := New()
	for _, e := range evs {
		if until != 0 && e.Seq > until {
			break
		}
		st.Apply(e)
	}
	return st.Snapshot()
}

// ---- the layout of a golden file --------------------------------------------------------------------------------------

// prettyJSON lays a value out as JSON for a reader who will read a diff of it: one key to a line, but a short object or a list of
// numbers on one line (a section of a prompt, a hit-ratio history), so that a change shows on the line that holds it. The layout is
// only layout: the output parses to the same value as json.Marshal of the value.
func prettyJSON(t testing.TB, v any) []byte {
	t.Helper()
	var compact bytes.Buffer
	enc := json.NewEncoder(&compact)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(compact.Bytes()))
	dec.UseNumber()
	root, err := parseNode(dec)
	if err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	root.render(&sb, 0, 0)
	sb.WriteByte('\n')
	return []byte(sb.String())
}

// lineWidth is how wide a line of a golden file may be before a list is broken across lines.
const lineWidth = 200

// node is a JSON value with its keys in the order they were written.
type node struct {
	kind byte // '{', '[' or 'v' (a scalar)
	raw  string
	keys []string
	kids []*node
}

func parseNode(dec *json.Decoder) (*node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	d, ok := tok.(json.Delim)
	if !ok {
		var b bytes.Buffer
		enc := json.NewEncoder(&b)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(tok); err != nil {
			return nil, err
		}
		return &node{kind: 'v', raw: strings.TrimSpace(b.String())}, nil
	}
	n := &node{kind: byte(d)}
	for dec.More() {
		if n.kind == '{' {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			n.keys = append(n.keys, kt.(string))
		}
		kid, err := parseNode(dec)
		if err != nil {
			return nil, err
		}
		n.kids = append(n.kids, kid)
	}
	if _, err := dec.Token(); err != nil && err != io.EOF {
		return nil, err
	}
	return n, nil
}

func (n *node) scalars() bool {
	for _, k := range n.kids {
		if k.kind != 'v' {
			return false
		}
	}
	return true
}

// inline is the node on one line.
func (n *node) inline() string {
	if n.kind == 'v' {
		return n.raw
	}
	var sb strings.Builder
	open, end := "[", "]"
	if n.kind == '{' {
		open, end = "{", "}"
	}
	sb.WriteString(open)
	for i, k := range n.kids {
		if i > 0 {
			sb.WriteString(", ")
		}
		if n.kind == '{' {
			sb.WriteString(fmt.Sprintf("%q: ", n.keys[i]))
		}
		sb.WriteString(k.inline())
	}
	sb.WriteString(end)
	return sb.String()
}

// render writes the node at an indent (in steps of two spaces); used is how many columns the line already holds before the node.
func (n *node) render(sb *strings.Builder, indent, used int) {
	if n.kind == 'v' || len(n.kids) == 0 {
		sb.WriteString(n.inline())
		return
	}
	pad := strings.Repeat("  ", indent)
	flat := n.inline()
	if n.scalars() && n.kind == '{' && used+len(flat) <= lineWidth {
		sb.WriteString(flat)
		return
	}
	if n.scalars() && n.kind == '[' {
		// A list of scalars stays on one line if it fits, else is filled line by line.
		if used+len(flat) <= lineWidth {
			sb.WriteString(flat)
			return
		}
		sb.WriteString("[\n")
		col := 0
		sb.WriteString(pad + "  ")
		for i, k := range n.kids {
			item := k.raw
			if i < len(n.kids)-1 {
				item += ","
			}
			if col > 0 && len(pad)+2+col+1+len(item) > lineWidth {
				sb.WriteString("\n" + pad + "  ")
				col = 0
			} else if col > 0 {
				sb.WriteByte(' ')
				col++
			}
			sb.WriteString(item)
			col += len(item)
		}
		sb.WriteString("\n" + pad + "]")
		return
	}
	open, end := "[", "]"
	if n.kind == '{' {
		open, end = "{", "}"
	}
	sb.WriteString(open + "\n")
	for i, k := range n.kids {
		sb.WriteString(pad + "  ")
		prefix := 0
		if n.kind == '{' {
			key := fmt.Sprintf("%q: ", n.keys[i])
			sb.WriteString(key)
			prefix = len(key)
		}
		k.render(sb, indent+1, len(pad)+2+prefix)
		if i < len(n.kids)-1 {
			sb.WriteByte(',')
		}
		sb.WriteByte('\n')
	}
	sb.WriteString(pad + end)
}

func TestPrettyJSONIsOnlyLayout(t *testing.T) {
	for _, v := range []any{
		map[string]any{"a": 1, "b": []int{}, "c": map[string]any{}, "d": "x<y&z \"q\" \u2192", "e": nil, "f": true},
		map[string]any{"list": []any{map[string]any{"name": "shared", "tokens": 88}, map[string]any{"name": "role", "tokens": 274, "deep": []int{1, 2}}}},
		map[string]any{"nums": func() []float64 {
			var f []float64
			for i := 0; i < 60; i++ {
				f = append(f, float64(i)/7)
			}
			return f
		}()},
		[]any{[]any{1, 2}, []any{}, "s"},
		mustSnapshot(t, handBuiltSession(), 0),
	} {
		got := prettyJSON(t, v)
		var a, b any
		if err := json.Unmarshal(got, &a); err != nil {
			t.Fatalf("not JSON: %v\n%s", err, got)
		}
		c, _ := json.Marshal(v)
		if err := json.Unmarshal(c, &b); err != nil || !reflect.DeepEqual(a, b) {
			t.Fatalf("the layout changed the value:\n%s", got)
		}
		for _, line := range strings.Split(string(got), "\n") {
			if len(line) > lineWidth+40 { // a long string is not broken
				t.Errorf("a line of %d columns: %.80s…", len(line), line)
			}
		}
		if !bytes.HasSuffix(got, []byte("\n")) {
			t.Error("no final newline")
		}
	}
	got := string(prettyJSON(t, map[string]any{"name": "shared", "tokens": 88, "list": []int{1, 2, 3}, "o": []any{map[string]any{"a": 1}}}))
	want := "{\n  \"list\": [1, 2, 3],\n  \"name\": \"shared\",\n  \"o\": [\n    {\"a\": 1}\n  ],\n  \"tokens\": 88\n}\n"
	if got != want {
		t.Errorf("layout:\n%s\nwant:\n%s", got, want)
	}
}
