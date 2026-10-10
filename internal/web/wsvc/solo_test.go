package wsvc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/checkpoint"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// utilText is lib/util.go with lines a and b replaced (1-based, 0: none).
func utilText(a, b int, with string) string {
	var sb strings.Builder
	for i := 1; i <= 30; i++ {
		if i == a || i == b {
			sb.WriteString(with + fmt.Sprint(i) + "\n")
			continue
		}
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	return sb.String()
}

// soloEnv is a single agent that made two turns of edits: turn 1 rewrote lines 3 and 27
// of lib/util.go and created new.txt; turn 2 edited main.go.
func soloEnv(t *testing.T) *env {
	t.Helper()
	root := project(t, map[string]string{
		"main.go":               "package main\n\nfunc main() {}\n",
		"lib/util.go":           utilText(0, 0, ""),
		".env":                  "API_KEY=sk-live-0123456789abcdef0123456789abcdef\n",
		"secrets/token.txt":     "s3cret\n",
		".sleipnir/config.json": `{"permissions":{"deny":["Read(./secrets/**)"]}}`,
		"img.bin":               "\x00\x01\x02binary",
		".gitignore":            "ignored.log\n",
	})
	writeFile(t, filepath.Join(root, "ignored.log"), "noise\n")
	sc := &soloScript{turns: [][][]mock.ToolCall{
		{
			{call("r1", "read", map[string]any{"path": "lib/util.go"})},
			{call("w1", "write", map[string]any{"path": "lib/util.go", "content": utilText(3, 27, "changed ")}),
				call("w2", "write", map[string]any{"path": "new.txt", "content": "fresh\n"})},
		},
		{
			{call("r2", "read", map[string]any{"path": "main.go"})},
			{call("e1", "edit", map[string]any{"path": "main.go", "old_string": "func main() {}", "new_string": "func main() { run() }"})},
		},
	}}
	client, model := startMock(t, sc.respond)
	e := newEnv(t, root, options(t, root, client, model))
	for _, goal := range []string{"edit util", "edit main"} {
		if _, err := e.sess.Run(context.Background(), goal); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(readFile(t, filepath.Join(root, "main.go")), "run()") || readFile(t, filepath.Join(root, "lib/util.go")) != utilText(3, 27, "changed ") {
		t.Fatal("the scripted agent did not make its edits")
	}
	return e
}

func TestIndexListsCheckpointsTheTreeAndItsMarkers(t *testing.T) {
	e := soloEnv(t)
	var idx wire.WsIndex
	w := e.get("/ws/index", &idx)
	expect(t, w, http.StatusOK, "")
	if idx.Isolation != "none" || idx.Root != e.root || len(idx.Cps) < 2 {
		t.Fatalf("index: isolation %q root %q cps %d", idx.Isolation, idx.Root, len(idx.Cps))
	}
	byID := map[string]wire.WsCheckpoint{}
	for _, c := range idx.Cps {
		byID[c.ID] = c
	}
	c1, c2 := byID["c01"], byID["c02"]
	if c1.NFiles != 2 || len(c1.Changes) != 2 || c1.Added != 3 || c1.Removed != 2 || !strings.HasPrefix(c1.Label, "turn 1") {
		t.Fatalf("c01 = %+v", c1)
	}
	if c2.NFiles != 1 || c2.Changes[0].Path != "main.go" || c2.Changes[0].Status != "modified" || c2.Changes[0].Agents[0] != "main" {
		t.Fatalf("c02 = %+v", c2)
	}
	rows := map[string]wire.WsFile{}
	for _, f := range idx.Tree {
		rows[f.Path] = f
	}
	if r := rows["main.go"]; r.Status != "M" || r.Owner != "main" || r.Cp != "c02" || r.Add != 1 || r.Del != 1 {
		t.Fatalf("main.go row = %+v", r)
	}
	if r := rows["new.txt"]; r.Status != "A" || r.Owner != "main" || r.Cp != "c01" {
		t.Fatalf("new.txt row = %+v", r)
	}
	if r := rows[".env"]; r.Protected == nil || r.Protected.Tier != "guarded" {
		t.Fatalf(".env must be protected: %+v", r)
	}
	if r := rows["secrets/token.txt"]; r.Protected == nil || r.Protected.Rule != "Read(./secrets/**)" || r.Protected.Tier != "deny" {
		t.Fatalf("secrets/token.txt must be protected by the deny rule: %+v", r.Protected)
	}
	if r := rows[".sleipnir/config.json"]; r.Ask == nil || r.Ask.Rule != "Edit(./.sleipnir/**)" {
		t.Fatalf(".sleipnir/config.json carries the ask marker: %+v", r)
	}
	if r := rows["img.bin"]; r.Kind != "other" || r.Status != "-" || r.Size == 0 {
		t.Fatalf("img.bin row = %+v", r)
	}
	for p := range rows {
		if strings.HasPrefix(p, ".git/") || p == "ignored.log" {
			t.Fatalf("%s must not be listed", p)
		}
	}
	// The ETag is the version: asking again with it is 304.
	r := e.do(request{method: http.MethodGet, path: e.tabPath("/ws/index")})
	etag := r.Header().Get("ETag")
	if etag == "" || !strings.Contains(etag, idx.Version) {
		t.Fatalf("etag %q version %q", etag, idx.Version)
	}
	rq := e.do(request{method: http.MethodGet, path: e.tabPath("/ws/index"), header: map[string]string{"If-None-Match": etag}})
	expect(t, rq, http.StatusNotModified, "")
}

func TestFileContentAtAPointWithAuthorship(t *testing.T) {
	e := soloEnv(t)
	var live wire.WsContent
	expect(t, e.get("/ws/file?path=lib/util.go", &live), http.StatusOK, "")
	if !live.Exists || live.At != "live" || live.Text != utilText(3, 27, "changed ") || !live.Exact {
		t.Fatalf("live = %+v", live)
	}
	want := []wire.BlameRun{{Line: 1, Count: 2, Ag: "-"}, {Line: 3, Count: 1, Ag: "main", ID: "c01"}, {Line: 4, Count: 23, Ag: "-"},
		{Line: 27, Count: 1, Ag: "main", ID: "c01"}, {Line: 28, Count: 3, Ag: "-"}}
	if fmt.Sprint(live.Blame) != fmt.Sprint(want) {
		t.Fatalf("blame %+v\nwant %+v", live.Blame, want)
	}
	var at1, base wire.WsContent
	expect(t, e.get("/ws/file?path=lib/util.go&at=c01", &at1), http.StatusOK, "")
	expect(t, e.get("/ws/file?path=lib/util.go&at=base", &base), http.StatusOK, "")
	if at1.Text != utilText(0, 0, "") || base.Text != at1.Text || at1.At != "c01" {
		t.Fatalf("at c01 / base: %q / %q", at1.Text, base.Text)
	}
	var at2 wire.WsContent
	expect(t, e.get("/ws/file?path=lib/util.go&at=c02", &at2), http.StatusOK, "")
	if at2.Text != live.Text {
		t.Fatal("at c02 the file was as turn 1 left it")
	}
	// new.txt did not exist when c01 began.
	expect(t, e.get("/ws/file?path=new.txt&at=c01", nil), http.StatusNotFound, "no_file")
	expect(t, e.get("/ws/file?path=lib/util.go&at=c99", nil), http.StatusNotFound, "no_checkpoint")
	expect(t, e.get("/ws/file?path=lib/util.go&at=yesterday", nil), http.StatusBadRequest, "bad_request")
	var bin wire.WsContent
	expect(t, e.get("/ws/file?path=img.bin", &bin), http.StatusOK, "")
	if !bin.Binary || bin.Text != "" {
		t.Fatalf("binary file: %+v", bin)
	}
}

func TestSafePaths(t *testing.T) {
	e := soloEnv(t)
	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "secret.txt"), "outside secret")
	if err := os.Symlink(outside, filepath.Join(e.root, "door")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(e.root, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/zero", filepath.Join(e.root, "zero")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := mkfifo(filepath.Join(e.root, "fifo"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(e.root, "big.txt"), strings.Repeat("0123456789abcde\n", (3<<20)/16))
	cases := []struct {
		path   string
		status int
		code   string
	}{
		{"../" + filepath.Base(outside) + "/secret.txt", 400, "bad_path"},
		{"/etc/passwd", 400, "bad_path"},
		{"door/secret.txt", 400, "bad_path"},
		{"link.txt", 400, "bad_path"},
		{"zero", 400, "bad_path"},
		{".git/config", 403, "denied"},
		{"sub/.git/HEAD", 403, "denied"},
		{".env", 403, "denied"},
		{"secrets/token.txt", 403, "denied"},
		{"a\x00b", 400, "bad_path"},
		{"", 400, "bad_path"},
		{"lib\\util.go", 400, "bad_path"},
		{strings.Repeat("a/", 3000), 400, "bad_path"},
		{"nope.txt", 404, "no_file"},
	}
	for _, c := range cases {
		for _, route := range []string{"/ws/file?path=", "/ws/diff?path="} {
			w := e.get(route+url.QueryEscape(c.path), nil)
			if w.Code != c.status || code(w) != c.code {
				t.Errorf("%s%.40q: %d %s, want %d %s", route, c.path, w.Code, w.Body, c.status, c.code)
			}
			if strings.Contains(w.Body.String(), "outside secret") || strings.Contains(w.Body.String(), "sk-live") || strings.Contains(w.Body.String(), "s3cret") {
				t.Errorf("%s%q leaked content", route, c.path)
			}
		}
	}
	if runtime.GOOS != "windows" {
		var fifo wire.WsContent
		expect(t, e.get("/ws/file?path=fifo", &fifo), http.StatusOK, "") // answered at once, never opened for reading
		if !fifo.Binary || fifo.Text != "" {
			t.Fatalf("fifo: %+v", fifo)
		}
	}
	var big wire.WsContent
	expect(t, e.get("/ws/file?path=big.txt", &big), http.StatusOK, "")
	if !big.Truncated || len(big.Text) > maxText || big.Size != 3<<20 {
		t.Fatalf("big: truncated %v len %d size %d", big.Truncated, len(big.Text), big.Size)
	}
	// The denied path carries why, for the page's card.
	w := e.get("/ws/file?path=.env", nil)
	var body struct {
		Detail wire.WsProtect `json:"detail"`
	}
	if json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Detail.Tier != "guarded" || body.Detail.Why == "" {
		t.Fatalf("denied detail: %s", w.Body)
	}
}

func TestDiffBetweenPoints(t *testing.T) {
	e := soloEnv(t)
	var d wire.WsDiff
	expect(t, e.get("/ws/diff?path=lib/util.go", &d), http.StatusOK, "")
	if d.From != "base" || d.To != "live" || d.Added != 2 || d.Removed != 2 || len(d.Hunks) != 2 {
		t.Fatalf("diff = %+v", d)
	}
	h := d.Hunks[0]
	if h.OldStart != 1 || h.NewStart != 1 || h.Lines[2].T != "-" || h.Lines[2].S != "line 3" || h.Lines[3].T != "+" || h.Lines[3].S != "changed 3" {
		t.Fatalf("first hunk = %+v", h)
	}
	var none wire.WsDiff
	expect(t, e.get("/ws/diff?path=lib/util.go&from=c02&to=live", &none), http.StatusOK, "")
	if len(none.Hunks) != 0 || none.Added != 0 {
		t.Fatalf("nothing changed lib/util.go after c02 began: %+v", none)
	}
	var created wire.WsDiff
	expect(t, e.get("/ws/diff?path=new.txt&from=c01&to=c02", &created), http.StatusOK, "")
	if created.Added != 1 || created.Hunks[0].OldLines != 0 {
		t.Fatalf("new.txt: %+v", created)
	}
	var bin wire.WsDiff
	expect(t, e.get("/ws/diff?path=img.bin", &bin), http.StatusOK, "")
	if !bin.Binary || len(bin.Hunks) != 0 {
		t.Fatalf("binary diff: %+v", bin)
	}
}

func TestRevertHunkAndUndo(t *testing.T) {
	e := soloEnv(t)
	util := filepath.Join(e.root, "lib/util.go")
	body := map[string]string{"path": "lib/util.go", "key": "24:24", "from": "base", "to": "live"}
	scope := "revert:" + e.tab.TabID() + ":" + d16(body)
	// no confirmation: 428; a confirmation for something else: 403
	expect(t, e.post("/ws/revert", body, "", nil), http.StatusPreconditionRequired, "confirm_required")
	expect(t, e.post("/ws/revert", body, "revert:"+e.tab.TabID()+":0000000000000000", nil), http.StatusForbidden, "confirm_invalid")
	// a turn runs: 409 busy, nothing written
	e.tab.HoldTurns(true)
	if _, err := e.tab.Send(context.Background(), wire.MessageRequest{Text: "hold"}); err != nil {
		t.Fatal(err)
	}
	expect(t, e.post("/ws/revert", body, scope, nil), http.StatusConflict, "busy")
	e.tab.ReleaseTurn()
	e.tab.HoldTurns(false)

	var rev wire.WsRevert
	expect(t, e.post("/ws/revert", body, scope, &rev), http.StatusOK, "")
	if readFile(t, util) != utilText(3, 0, "changed ") || !revIDRE.MatchString(rev.ID) || rev.Path != "lib/util.go" {
		t.Fatalf("after revert: %q %+v", readFile(t, util), rev)
	}
	// /rewind sees it: the revert is a recorded write of the person's.
	ws := e.sess.Ckpt.Writes("lib/util.go")
	if ws[len(ws)-1].Agent != checkpoint.PersonAgent {
		t.Fatalf("journal: %+v", ws)
	}
	// the same hunk again: it is not in the diff any more
	expect(t, e.post("/ws/revert", body, scope, nil), http.StatusConflict, "changed")
	var idx wire.WsIndex
	e.get("/ws/index", &idx)
	if len(idx.Reverted) != 1 || idx.Reverted[0].ID != rev.ID {
		t.Fatalf("index reverts: %+v", idx.Reverted)
	}
	// undo puts the line back
	expect(t, e.post("/ws/revert/"+rev.ID+"/undo", nil, "", nil), http.StatusOK, "")
	if readFile(t, util) != utilText(3, 27, "changed ") {
		t.Fatalf("after undo: %q", readFile(t, util))
	}
	expect(t, e.post("/ws/revert/"+rev.ID+"/undo", nil, "", nil), http.StatusNotFound, "not_found")
	expect(t, e.post("/ws/revert/v_notanid/undo", nil, "", nil), http.StatusBadRequest, "bad_request")
	// the page saw the acknowledgement rows, and the agent that wrote the file was told
	if !sawSys(e, "reverted a hunk of lib/util.go") {
		t.Fatal("no acknowledgement row")
	}
	told := false
	for _, c := range e.tab.Calls() {
		if c.Method == "Notify" && strings.Contains(fmt.Sprint(c.Arg), "reverted part of your change to lib/util.go") {
			told = true
		}
	}
	if !told {
		t.Fatalf("the agent was not told: %+v", e.tab.Calls())
	}
	// a revert whose undo would overwrite a later change is refused
	expect(t, e.post("/ws/revert", body, scope, &rev), http.StatusOK, "")
	writeFile(t, util, "edited by the person afterwards\n")
	expect(t, e.post("/ws/revert/"+rev.ID+"/undo", nil, "", nil), http.StatusConflict, "changed")
	if readFile(t, util) != "edited by the person afterwards\n" {
		t.Fatal("a refused undo writes nothing")
	}
	// a hunk that does not apply to the file as it is now
	body2 := map[string]string{"path": "lib/util.go", "key": "1:1", "from": "base", "to": "c02"}
	expect(t, e.post("/ws/revert", body2, "revert:"+e.tab.TabID()+":"+d16(body2), nil), http.StatusUnprocessableEntity, "conflict")
}

// sawSys reports whether the tab's journal received a sys say row containing text.
func sawSys(e *env, text string) bool {
	for _, f := range e.host.FramesOf("ev") {
		ev, ok := f.Data.(wire.EvFrame)
		if ok && strings.Contains(string(ev.Ev), text) {
			return true
		}
		if b, err := json.Marshal(f.Data); err == nil && strings.Contains(string(b), jsonEscape(text)) {
			return true
		}
	}
	return false
}

// jsonEscape is text as it appears inside a JSON string.
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	return strings.Trim(string(b), `"`)
}

func TestRestorePreviewApplyUndo(t *testing.T) {
	e := soloEnv(t)
	main := filepath.Join(e.root, "main.go")
	util := filepath.Join(e.root, "lib/util.go")
	before := map[string]string{"main": readFile(t, main), "util": readFile(t, util)}

	var plan wire.RestorePlan
	expect(t, e.post("/ws/restore", wire.RestoreRequest{ID: "c01", DryRun: true}, "", &plan), http.StatusOK, "")
	if plan.Applied || len(plan.Files) != 3 || plan.ID != "c01" {
		t.Fatalf("preview = %+v", plan)
	}
	if readFile(t, main) != before["main"] {
		t.Fatal("a preview writes nothing")
	}
	got := map[string]wire.RestoreFile{}
	for _, f := range plan.Files {
		got[f.Path] = f
	}
	if f := got["new.txt"]; f.To != "removed" || f.Outcome != "planned" || f.Removed != 1 {
		t.Fatalf("new.txt in the preview: %+v", f)
	}
	if f := got["lib/util.go"]; f.To != "put back" || f.Added != 2 || f.Removed != 2 {
		t.Fatalf("lib/util.go in the preview: %+v", f)
	}
	expect(t, e.post("/ws/restore", wire.RestoreRequest{ID: "c09", DryRun: true}, "", nil), http.StatusNotFound, "no_checkpoint")
	expect(t, e.post("/ws/restore", wire.RestoreRequest{ID: "c01"}, "", nil), http.StatusPreconditionRequired, "confirm_required")

	var applied wire.RestorePlan
	expect(t, e.post("/ws/restore", wire.RestoreRequest{ID: "c01"}, "restore:"+e.tab.TabID()+":c01", &applied), http.StatusOK, "")
	if !applied.Applied || applied.Safety != "c01s" || readFile(t, util) != utilText(0, 0, "") {
		t.Fatalf("restore: %+v", applied)
	}
	if _, err := os.Stat(filepath.Join(e.root, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("new.txt is removed")
	}
	if !sawSys(e, "restored the files of c01 (3 files)") || !sawSys(e, `"safety":true`) {
		t.Fatal("the restore's rows were not emitted")
	}
	notified := false
	for _, c := range e.tab.Calls() {
		if c.Method == "Notify" && strings.Contains(fmt.Sprint(c.Arg), "rewound the project's files to checkpoint cp_0001") {
			notified = true
		}
	}
	if !notified {
		t.Fatalf("the agents were not told: %+v", e.tab.Calls())
	}
	var idx wire.WsIndex
	e.get("/ws/index", &idx)
	if idx.Restore == nil || idx.Restore.To != "c01" || idx.Restore.Safety != "c01s" || idx.Cps[len(idx.Cps)-1].ID != "c01s" || !idx.Cps[len(idx.Cps)-1].Safety {
		t.Fatalf("index restore: %+v", idx.Restore)
	}
	// c01 has nothing left to put back
	expect(t, e.post("/ws/restore", wire.RestoreRequest{ID: "c01", DryRun: true}, "", nil), http.StatusConflict, "nothing")

	expect(t, e.post("/ws/restore/undo", nil, "", nil), http.StatusPreconditionRequired, "confirm_required")
	var undone wire.RestorePlan
	expect(t, e.post("/ws/restore/undo", nil, "restore.undo:"+e.tab.TabID(), &undone), http.StatusOK, "")
	if readFile(t, main) != before["main"] || readFile(t, util) != before["util"] || readFile(t, filepath.Join(e.root, "new.txt")) != "fresh\n" {
		t.Fatal("the undo puts every file back")
	}
	expect(t, e.post("/ws/restore/undo", nil, "restore.undo:"+e.tab.TabID(), nil), http.StatusConflict, "nothing")

	// A file edited after the agents' last write: the restore is refused whole.
	writeFile(t, main, "edited by the person\n")
	w := e.post("/ws/restore", wire.RestoreRequest{ID: "c02"}, "restore:"+e.tab.TabID()+":c02", nil)
	expect(t, w, http.StatusConflict, "conflict")
	if !strings.Contains(w.Body.String(), "main.go") || readFile(t, main) != "edited by the person\n" {
		t.Fatalf("conflict: %s", w.Body)
	}
}

func TestReviewedMarks(t *testing.T) {
	e := soloEnv(t)
	put := func(body any) int {
		w := e.do(request{method: http.MethodPut, path: e.tabPath("/ws/reviewed"), body: body})
		return w.Code
	}
	if c := put(wire.ReviewedRequest{Path: "main.go", Cp: "c02", On: true}); c != http.StatusOK {
		t.Fatalf("mark: %d", c)
	}
	put(wire.ReviewedRequest{Path: "new.txt", Cp: "live", On: true})
	put(wire.ReviewedRequest{Path: "new.txt", On: false})
	var idx wire.WsIndex
	e.get("/ws/index", &idx)
	if len(idx.Reviewed) != 1 || idx.Reviewed["main.go"] != "c02" {
		t.Fatalf("reviewed = %v", idx.Reviewed)
	}
	if c := put(wire.ReviewedRequest{Path: "../x", On: true}); c != http.StatusBadRequest {
		t.Fatalf("bad path: %d", c)
	}
}

func TestComplete(t *testing.T) {
	e := soloEnv(t)
	var out struct{ Paths []string }
	expect(t, e.get("/complete?prefix=util", &out), http.StatusOK, "")
	if len(out.Paths) != 1 || out.Paths[0] != "lib/util.go" {
		t.Fatalf("util: %v", out.Paths)
	}
	expect(t, e.get("/complete?prefix=&limit=50", &out), http.StatusOK, "")
	joined := strings.Join(out.Paths, " ")
	for _, bad := range []string{".env", "secrets/token.txt", ".git/", "ignored.log"} {
		if strings.Contains(" "+joined+" ", " "+bad+" ") {
			t.Fatalf("%s must not be offered: %v", bad, out.Paths)
		}
	}
	if !strings.Contains(joined, "lib/") || !strings.Contains(joined, "main.go") {
		t.Fatalf("all: %v", out.Paths)
	}
	expect(t, e.get("/complete?prefix=&limit=2", &out), http.StatusOK, "")
	if len(out.Paths) != 2 {
		t.Fatalf("limit: %v", out.Paths)
	}
	expect(t, e.get("/complete?prefix=x&limit=zero", nil), http.StatusBadRequest, "bad_request")
}

func TestPermissionsCheck(t *testing.T) {
	e := soloEnv(t)
	cases := []struct {
		tool, arg, d, cls, why string
	}{
		{"Read", ".env", "refused", "err", "built-in protection"},
		{"Read", "secrets/token.txt", "refused", "err", "a deny rule: Read(./secrets/**)"},
		{"Edit", ".sleipnir/config.json", "ask", "warm", "an ask rule: Edit(./.sleipnir/**) (built-in protection)"},
		{"Edit", "main.go", "allowed", "err", "bypass"},
		{"Bash", "sudo ls", "ask", "warm", "high risk"},
		{"Bash", "rm -rf ~/.ssh", "refused", "err", "built-in protection (hard)"},
	}
	for _, c := range cases {
		var v wire.PermVerdict
		expect(t, e.post("/permissions/check", wire.PermCheckRequest{Tool: c.tool, Arg: c.arg}, "", &v), http.StatusOK, "")
		if v.D != c.d || v.Cls != c.cls || !strings.Contains(v.Why, c.why) {
			t.Errorf("%s %s: %+v, want %s %s %q", c.tool, c.arg, v, c.d, c.cls, c.why)
		}
	}
	expect(t, e.post("/permissions/check", wire.PermCheckRequest{Tool: "Fetch", Arg: "x"}, "", nil), http.StatusBadRequest, "bad_tool")
}

func TestTheEnvelopeGuardsEveryRoute(t *testing.T) {
	e := soloEnv(t)
	gets := []string{"/ws/index", "/ws/file?path=main.go", "/ws/diff?path=main.go", "/ws/worktrees", "/ws/queue", "/ws/verify/T1", "/complete?prefix=m"}
	for _, p := range gets {
		if w := e.do(request{method: http.MethodGet, path: e.tabPath(p), noToken: true}); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a token: %d", p, w.Code)
		}
	}
	posts := []struct {
		path string
		body any
	}{
		{"/ws/revert", wire.RevertRequest{Path: "main.go", Key: "1:1"}},
		{"/ws/restore", wire.RestoreRequest{ID: "c01", DryRun: true}},
		{"/ws/accept", wire.AcceptRequest{DryRun: true}},
		{"/permissions/check", wire.PermCheckRequest{Tool: "Read", Arg: "x"}},
	}
	for _, p := range posts {
		path := e.tabPath(p.path)
		expect(t, e.do(request{method: http.MethodPost, path: path, body: p.body, noToken: true}), http.StatusUnauthorized, "")
		expect(t, e.do(request{method: http.MethodPost, path: path, body: p.body, origin: "http://evil.example"}), http.StatusForbidden, "forbidden_origin")
		expect(t, e.do(request{method: http.MethodPost, path: path, body: p.body, noHeader: true}), http.StatusForbidden, "csrf")
		expect(t, e.do(request{method: http.MethodPost, path: path, raw: []byte(`{}`), contentType: "text/plain"}), http.StatusUnsupportedMediaType, "")
		expect(t, e.do(request{method: http.MethodPost, path: path, raw: []byte(`{"bogus":1}`)}), http.StatusBadRequest, "bad_json")
		expect(t, e.do(request{method: http.MethodPost, path: path, raw: []byte(`{"path":"` + strings.Repeat("a", 70<<10) + `"}`)}), http.StatusRequestEntityTooLarge, "body_too_large")
	}
	// a confirmation is spent by its first use
	scope := "restore:" + e.tab.TabID() + ":c02"
	id := e.confirm(scope)
	expect(t, e.do(request{method: http.MethodPost, path: e.tabPath("/ws/restore"), body: wire.RestoreRequest{ID: "c02"}, confirm: id}), http.StatusOK, "")
	expect(t, e.do(request{method: http.MethodPost, path: e.tabPath("/ws/restore"), body: wire.RestoreRequest{ID: "c02"}, confirm: id}), http.StatusForbidden, "confirm_invalid")
	// tab ids are checked before any lookup
	expect(t, e.do(request{method: http.MethodGet, path: "/api/sessions/BAD_ID/ws/index"}), http.StatusBadRequest, "bad_request")
	expect(t, e.do(request{method: http.MethodGet, path: "/api/sessions/nobody/ws/index"}), http.StatusNotFound, "not_found")
	// the isolated team's routes on a shared tree
	expect(t, e.get("/ws/worktrees", nil), http.StatusConflict, "not_isolated")
	expect(t, e.get("/ws/queue", nil), http.StatusConflict, "not_isolated")
	expect(t, e.get("/ws/verify/T1", nil), http.StatusNotFound, "not_found")
	expect(t, e.get("/ws/verify/T%20x", nil), http.StatusBadRequest, "bad_request")
	expect(t, e.post("/ws/accept", wire.AcceptRequest{DryRun: true}, "", nil), http.StatusConflict, "not_isolated")
	expect(t, e.post("/ws/accept", wire.AcceptRequest{Mode: "squash", DryRun: true}, "", nil), http.StatusBadRequest, "bad_request")
}
