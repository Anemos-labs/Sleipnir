package inspect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/events"
)

func TestEventsPagingAcrossIndexBoundaries(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 3, Steps: 45})
	s := mustLoad(t, dir)
	last := s.Log().LastSeq
	if last < 3*indexEvery {
		t.Fatalf("the synthetic log has %d events; the test needs several index entries", last)
	}
	if len(s.index) < 3 {
		t.Fatalf("index has %d entries", len(s.index))
	}

	// Walk the whole log 100 at a time: every seq exactly once, in order.
	var seqs []uint64
	since := uint64(0)
	for i := 0; i < 1000; i++ {
		page, err := s.Events(EventQuery{Since: since, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Events {
			seqs = append(seqs, e.Seq)
		}
		since = page.Next
		if !page.More {
			break
		}
	}
	if uint64(len(seqs)) != last {
		t.Fatalf("paged %d events, want %d", len(seqs), last)
	}
	for i, q := range seqs {
		if q != uint64(i+1) {
			t.Fatalf("event %d has seq %d", i, q)
		}
	}

	// Seeking near every index boundary starts at exactly the next event.
	for _, since := range []uint64{0, 1, 254, 255, 256, 257, 511, 512, 513, last - 1, last, last + 5} {
		page, err := s.Events(EventQuery{Since: since, Limit: 3})
		if err != nil {
			t.Fatal(err)
		}
		var want []uint64
		for q := since + 1; q <= last && len(want) < 3; q++ {
			want = append(want, q)
		}
		var got []uint64
		for _, e := range page.Events {
			got = append(got, e.Seq)
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("since %d: got %v, want %v", since, got, want)
		}
		if page.LastSeq != last {
			t.Errorf("last seq = %d, want %d", page.LastSeq, last)
		}
	}

	// Tail mode returns the newest events, oldest first.
	page, _ := s.Events(EventQuery{Tail: true, Limit: 25})
	if len(page.Events) != 25 || page.Events[24].Seq != last || page.Events[0].Seq != last-24 || page.More {
		t.Errorf("tail = %d events from %d to %d more=%v", len(page.Events), page.Events[0].Seq, page.Events[len(page.Events)-1].Seq, page.More)
	}
}

func TestEventsFiltersElisionAndValidity(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 2, Steps: 12})
	// A tool call with an enormous input, as an edit of a big file would log.
	log, err := events.Open(dir, "synth-session")
	if err != nil {
		t.Fatal(err)
	}
	log.Emit("be-1", "tool.call", m{"id": "big", "name": "write", "input": m{"content": strings.Repeat("é€😀", 3000)}})
	log.Close()
	s := mustLoad(t, dir)

	page, _ := s.Events(EventQuery{Agent: "be-1", Type: "tool.call", Limit: 2000, Since: 0})
	if len(page.Events) == 0 {
		t.Fatal("no be-1 tool calls")
	}
	var elided int
	for _, e := range page.Events {
		if e.Agent != "be-1" || e.Type != "tool.call" {
			t.Fatalf("filter leaked %s/%s", e.Agent, e.Type)
		}
		if !json.Valid(e.Data) {
			t.Fatalf("event %d has invalid data %q", e.Seq, e.Data)
		}
		var d map[string]any
		_ = json.Unmarshal(e.Data, &d)
		if d["_truncated"] == true {
			elided++
			if d["_bytes"].(float64) < maxEventData || len(d["_preview"].(string)) > 512 {
				t.Errorf("elided payload = %v", d)
			}
		}
	}
	if elided != 1 {
		t.Errorf("elided %d payloads, want exactly the one huge input", elided)
	}
	// Filters advance the cursor over events they skip, so a client never re-reads them.
	page, _ = s.Events(EventQuery{Type: "no.such.type", Limit: 10})
	if len(page.Events) != 0 || page.Next != s.Log().LastSeq {
		t.Errorf("a filter matching nothing: %d events, next %d, want the cursor at the end", len(page.Events), page.Next)
	}
}

func TestEventsNeverReturnATornTail(t *testing.T) {
	b := newEvb(t)
	b.emit("a", "user.input", m{"text": "one"})
	b.emit("a", "user.input", m{"text": "two"})
	full := b.line("a", "user.input", m{"text": "three"})
	b.raw(string(full[:len(full)-9]))
	s := mustLoad(t, b.dir)
	page, err := s.Events(EventQuery{Limit: 10})
	if err != nil || len(page.Events) != 2 || page.LastSeq != 2 {
		t.Fatalf("events %d last %d err %v", len(page.Events), page.LastSeq, err)
	}
	b.raw(string(full[len(full)-9:]))
	mustTail(t, s)
	page, _ = s.Events(EventQuery{Since: 2, Limit: 10})
	if len(page.Events) != 1 || page.Events[0].Seq != 3 {
		t.Errorf("after completion: %+v", page.Events)
	}
}

func TestEventsOnAReaderBackedSessionIsAnError(t *testing.T) {
	s, err := LoadReader(strings.NewReader(""), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Events(EventQuery{}); err == nil {
		t.Error("a session with no file has no raw events to serve")
	}
}

// ---- blobs: layer text, diffs, hostile hashes --------------------------------------------------------------------------

func TestBlobStoreReadsWhatDirBlobsWrote(t *testing.T) {
	dir := t.TempDir()
	db, err := events.NewDirBlobs(filepath.Join(dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	text := []byte(strings.Repeat("layer text ", 1000))
	h, err := db.Put(text)
	if err != nil {
		t.Fatal(err)
	}
	bs := openBlobs(filepath.Join(dir, "blobs"))
	if !bs.available() {
		t.Fatal("blobs directory not detected")
	}
	if n, ok := bs.size(string(h)); !ok || n != int64(len(text)) {
		t.Errorf("size = %d, %v", n, ok)
	}
	got, total, ok := bs.read(string(h), 100)
	if !ok || total != int64(len(text)) || string(got) != string(text[:100]) {
		t.Errorf("bounded read = %d bytes of %d, ok=%v", len(got), total, ok)
	}
	if p, _ := bs.path(string(h)); p != filepath.Join(dir, "blobs", string(h)[:2], string(h)[2:4], string(h)) {
		t.Errorf("layout drifted from events.DirBlobs: %s", p)
	}
}

func TestBlobStoreRefusesUnsafeHashes(t *testing.T) {
	root := t.TempDir()
	blobs := filepath.Join(root, "blobs")
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(root, "secret")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	valid := h64("x")
	bad := []string{
		"", "..", "../secret", "../../secret", "../../../../etc/passwd", "ab/cd/" + valid, valid[:63], valid + "0",
		strings.ToUpper(valid), "zz" + valid[2:], valid[:32] + "\x00" + valid[33:], "%2e%2e/%2e%2e/secret", `..\..\secret`, "٠١٢" + valid[3:],
	}
	bs := openBlobs(blobs)
	for _, h := range bad {
		if validHash(h) {
			t.Errorf("validHash(%q) = true", h)
		}
		if _, ok := bs.path(h); ok {
			t.Errorf("path(%q) resolved", h)
		}
		if _, _, ok := bs.read(h, 100); ok {
			t.Errorf("read(%q) succeeded", h)
		}
		if _, ok := bs.size(h); ok {
			t.Errorf("size(%q) succeeded", h)
		}
	}
	if !validHash(valid) {
		t.Errorf("validHash rejected a real hash")
	}
	// A symlink at a valid blob path is not followed.
	p, _ := bs.path(valid)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, p); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, _, ok := bs.read(valid, 100); ok {
		t.Error("a symlinked blob was followed to a file outside the store")
	}
	if _, ok := bs.size(valid); ok {
		t.Error("a symlinked blob has a size")
	}
	// A missing store is simply unavailable, and is never created.
	none := openBlobs(filepath.Join(root, "nothing"))
	if none.available() {
		t.Error("a missing directory reported available")
	}
	if _, err := os.Stat(filepath.Join(root, "nothing")); err == nil {
		t.Error("the read-only inspector created a directory")
	}
}

func TestHostileHashesInALogNeverTouchTheFilesystem(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "sess")
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "secret"), []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	b := &evb{t: t, dir: dir, path: filepath.Join(dir, "events.jsonl"), now: t0}
	evil := "../../secret"
	secs := []m{sec("shared", 5000, evil), sec("notes", 300, "../../secret"), sec("spine", 50, strings.Repeat("a", 64))}
	b.req("a", "a.1", secs, m{"hot": evil, "manifest": m{"tools": evil, "system": []string{evil}}})
	b.resp("a", "a.1", 100, 0, 5000, 10, nil)
	b.at(time.Second).req("a", "a.2", []m{sec("shared", 5000, evil), sec("notes", 310, "../../../secret")}, m{"hot": evil, "manifest": m{"tools": "../secret", "system": []string{"../secret"}}})
	b.resp("a", "a.2", 100, 5000, 0, 10, nil)
	s := mustLoad(t, dir)
	rep, ok := s.Layers("a.2", true)
	if !ok {
		t.Fatal("no request")
	}
	blob, _ := json.Marshal(rep)
	if strings.Contains(string(blob), "TOP SECRET") {
		t.Fatalf("a hash in the log read a file outside the blob store: %s", blob)
	}
	for _, l := range rep.Layers {
		if l.Text != "" || l.Bytes != 0 {
			t.Errorf("%s got text or bytes from a hostile hash: %+v", l.Key, l)
		}
	}
	if len(rep.Diffs) != 0 {
		t.Errorf("diffs from hostile hashes: %+v", rep.Diffs)
	}
}

func TestLayerTextAndFirstDifferingByteFromBlobs(t *testing.T) {
	dir := t.TempDir()
	db, _ := events.NewDirBlobs(filepath.Join(dir, "blobs"))
	put := func(s string) string {
		h, err := db.Put([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return string(h)
	}
	common := "<my-notes>\n## facts\n- the build is green\n- tests: make test-unit\n"
	notes1 := put(common + "- decision: mutex, not channel\n</my-notes>")
	notes2 := put(common + "- decision: channel, not mutex\n</my-notes>")
	tools1 := put(`[{"name":"read","description":"reads"},{"name":"grep","description":"searches"}]`)
	tools2 := put(`[{"name":"read","description":"reads"},{"name":"grep","description":"searches!"}]`)
	sys := put(`{"kind":"text","text":"You are Sleipnir."}`)
	shared := put("<shared-context>\nproject map\n</shared-context>")
	b := &evb{t: t, dir: dir, path: filepath.Join(dir, "events.jsonl"), now: t0}
	man := func(tools string) m { return m{"tools": tools, "system": []string{sys}} }
	b.req("a", "a.1", []m{sec("shared", 300, shared), sec("notes", 200, notes1)}, m{"manifest": man(tools1)})
	b.resp("a", "a.1", 100, 0, 800, 10, nil)
	b.at(time.Second).emit("a", "compact.commit", m{"reason": "ok", "removed_tokens": 100, "retained_tokens": 100, "spine_added": 5})
	b.req("a", "a.2", []m{sec("shared", 300, shared), sec("notes", 200, notes2)}, m{"manifest": man(tools2), "thread_from": 3, "thread_to": 5})
	b.resp("a", "a.2", 100, 400, 400, 10, nil)
	s := mustLoad(t, dir)

	rep, ok := s.Layers("a.2", true)
	if !ok {
		t.Fatal("no a.2")
	}
	diffs := map[string]LayerDiff{}
	for _, d := range rep.Diffs {
		diffs[d.Layer] = d
	}
	nd, ok := diffs["G3"]
	if !ok {
		t.Fatalf("no G3 diff in %+v", rep.Diffs)
	}
	wantAt := len(common) + len("- decision: ")
	if nd.At != wantAt {
		t.Errorf("notes differ at byte %d, want %d", nd.At, wantAt)
	}
	before, after := []rune(nd.Before), []rune(nd.After)
	if string(before[nd.Split:nd.Split+5]) != "mutex" || string(after[nd.Split:nd.Split+7]) != "channel" || !strings.HasSuffix(string(before[:nd.Split]), "- decision: ") {
		t.Errorf("windows do not line up on the first difference: before %q after %q split %d", nd.Before, nd.After, nd.Split)
	}
	if td, ok := diffs["G0 tools"]; !ok || td.At <= 0 || !strings.Contains(td.After, "searches!") {
		t.Errorf("tools diff = %+v", td)
	}
	if _, ok := diffs["G0 system"]; ok {
		t.Error("the system prompt did not change")
	}
	byKey := map[string]LayerInfo{}
	for _, l := range rep.Layers {
		byKey[l.Key] = l
	}
	if !strings.Contains(byKey["G3"].Text, "channel, not mutex") || byKey["G3"].Bytes != len(common)+len("- decision: channel, not mutex\n</my-notes>") {
		t.Errorf("G3 = %+v", byKey["G3"])
	}
	if g := byKey["G0"]; !strings.Contains(g.Text, "You are Sleipnir.") || !strings.Contains(g.Text, "tools (2)") || !strings.Contains(g.Text, "grep: searches!") {
		t.Errorf("G0 text = %q", g.Text)
	}
	if byKey["G1"].State != LayerInvalidated && byKey["G1"].State != LayerCached {
		t.Errorf("G1 = %s", byKey["G1"].State)
	}
	// G0 changed (tools), so everything after it is invalid though its bytes are the same.
	if byKey["G0"].State != LayerRewritten || byKey["G1"].State != LayerInvalidated || byKey["G3"].State != LayerRewritten {
		t.Errorf("states G0/G1/G3 = %s/%s/%s", byKey["G0"].State, byKey["G1"].State, byKey["G3"].State)
	}
	// Without text=1 nothing is read.
	plain, _ := s.Layers("a.2", false)
	if len(plain.Diffs) != 0 || plain.Layers[3].Text != "" {
		t.Errorf("text requested off but returned: %+v", plain.Diffs)
	}
}

func TestLayerTextIsCut(t *testing.T) {
	dir := t.TempDir()
	db, _ := events.NewDirBlobs(filepath.Join(dir, "blobs"))
	h, _ := db.Put([]byte(strings.Repeat("0123456789", 5000)))
	b := &evb{t: t, dir: dir, path: filepath.Join(dir, "events.jsonl"), now: t0}
	b.req("a", "a.1", []m{sec("shared", 12000, string(h))}, nil)
	b.resp("a", "a.1", 10, 0, 12000, 1, nil)
	rep, _ := mustLoad(t, dir).Layers("a.1", true)
	l := rep.Layers[1]
	if len(l.Text) != layerTextMax || !l.TextCut || l.Bytes != 50000 {
		t.Errorf("G1 text %d bytes cut=%v total=%d", len(l.Text), l.TextCut, l.Bytes)
	}
}

func TestMissingBlobsAreTolerated(t *testing.T) {
	dir, _ := synthDir(t, synthCfg{Workers: 1, Steps: 12, NoBlobs: true})
	s := mustLoad(t, dir)
	sum := s.Summary()
	if sum.Log.Blobs {
		t.Error("blobs reported present")
	}
	found := false
	for _, w := range sum.Warnings {
		found = found || strings.Contains(w, "No blobs/ directory")
	}
	if !found {
		t.Errorf("warnings = %v", sum.Warnings)
	}
	page := s.Requests(RequestQuery{Tail: true, Limit: 100, Kind: "main"})
	r := page.Requests[len(page.Requests)-1]
	if r.G0Known || r.Layers[0] != 0 || r.Layers[5] == 0 {
		t.Errorf("without blobs G0 is unknown and folded into the thread: %+v", r.Layers)
	}
	rep, ok := s.Layers(r.ID, true)
	if !ok || rep.Layers[0].Source != "unknown" || rep.Layers[1].Text != "" {
		t.Errorf("layers = %+v ok=%v", rep.Layers[:2], ok)
	}
	found = false
	for _, n := range rep.Notes {
		found = found || strings.Contains(n, "G0")
	}
	if !found {
		t.Errorf("notes = %v, want G0's missing size explained", rep.Notes)
	}
	// Partial loss: one layer blob deleted.
	dir2, _ := synthDir(t, synthCfg{Workers: 1, Steps: 12})
	s2 := mustLoad(t, dir2)
	r2 := s2.Requests(RequestQuery{Tail: true, Limit: 100, Kind: "main"}).Requests
	rep2, _ := s2.Layers(r2[len(r2)-1].ID, false)
	var victim string
	s2.mu.RLock()
	for _, sec := range s2.byID[r2[len(r2)-1].ID].sections {
		if sec.name == "shared" {
			victim = sec.hash
		}
	}
	s2.mu.RUnlock()
	p, _ := s2.blobs.path(victim)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	rep2, _ = s2.Layers(r2[len(r2)-1].ID, true)
	if rep2.Layers[1].Text != "" || rep2.Layers[0].Text == "" {
		t.Errorf("one missing blob must only cost that layer its text: G0 %d bytes, G1 %d bytes", len(rep2.Layers[0].Text), len(rep2.Layers[1].Text))
	}
}

// ---- RL fields -----------------------------------------------------------------------------------------------------------

func TestRLFieldsWhenPresent(t *testing.T) {
	b := newEvb(t)
	secs := []m{sec("shared", 1000, h64("s"))}
	b.req("a", "a.1", secs, m{"wire_hash": h64("w1"), "renderer": "sleipnir-kv/1"})
	b.resp("a", "a.1", 10, 990, 0, 5, m{"tokens": h64("trace"), "completion": h64("c")})
	b.req("a", "a.c1", nil, m{"kind": "compactor", "wire_hash": h64("w2")})
	b.resp("a", "a.c1", 10, 990, 0, 5, m{"side": true})
	b.req("a", "a.m1", nil, m{"kind": "mailman"})
	b.resp("a", "a.m1", 10, 100, 0, 5, m{"side": true})
	b.emit("", "outcome", m{"kind": "verifier", "score": 0.75, "pass": true, "verifier_version": "v3", "detail": h64("log")})
	b.emit("", "outcome", m{"kind": "review", "score": 0, "pass": false})
	ep := `{"schema":"sleipnir.rl/1","id":"task-9/2","task_id":"task-9","group":"g1","sample":2,"policy":{"model":"my-policy"},
		"agents":[{"id":"a"},{"id":"b"}],"outcome":{"verifier":{"kind":"verifier","pass":true,"score":0.75},"claimed":"done"},
		"reward":{"total":0.83,"components":{"outcome":0.75,"cost":-0.02}},"flags":["weak_label","hack:network"],"cost":{"usd":1.25,"ite":90000,"requests":3}}`
	if err := os.WriteFile(filepath.Join(b.dir, "episode.json"), []byte(ep), 0o600); err != nil {
		t.Fatal(err)
	}
	s := mustLoad(t, b.dir)
	rl := s.Summary().RL
	if rl == nil {
		t.Fatal("no RL section")
	}
	if rl.WireHashes != 2 || rl.TokenTraces != 1 || rl.Kinds["main"] != 1 || rl.Kinds["compactor"] != 1 || rl.Kinds["mailman"] != 1 || len(rl.Renderers) != 1 {
		t.Errorf("rl = %+v", rl)
	}
	if len(rl.Outcomes) != 2 || rl.Outcomes[0].Kind != "verifier" || rl.Outcomes[0].Score != 0.75 || !rl.Outcomes[0].Pass || rl.Outcomes[0].Version != "v3" || rl.Outcomes[1].Pass {
		t.Errorf("outcomes = %+v", rl.Outcomes)
	}
	e := rl.Episode
	if e == nil || e.TaskID != "task-9" || e.Sample != 2 || e.Policy != "my-policy" || e.Pass == nil || !*e.Pass || *e.Score != 0.75 || e.Reward != 0.83 ||
		e.Components["cost"] != -0.02 || len(e.Flags) != 2 || e.CostUSD != 1.25 || e.Requests != 3 || e.Agents != 2 || e.Claimed != "done" {
		t.Errorf("episode = %+v", e)
	}
	if s.Episode() == nil {
		t.Error("Episode() empty")
	}
	// A log with none of it has no RL section at all.
	plain := newEvb(t)
	plain.req("a", "a.1", secs, nil)
	plain.resp("a", "a.1", 10, 0, 0, 1, nil)
	if rl := mustLoad(t, plain.dir).Summary().RL; rl != nil {
		t.Errorf("rl = %+v, want none for an ordinary session", rl)
	}
	// Bad episode.json is ignored, and the model keeps working.
	if err := os.WriteFile(filepath.Join(plain.dir, "episode.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := mustLoad(t, plain.dir); s.Episode() != nil || s.Summary().Totals.Requests != 1 {
		t.Error("a corrupt episode.json must not break the session")
	}
}
