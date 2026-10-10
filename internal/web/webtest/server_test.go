package webtest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/anemos-labs/sleipnir/internal/web"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// guard is the most a test waits for something that has to happen: a hang guard, not a timing.
const guard = 20 * time.Second

// call sends a request to the fake server as a script: bearer token, and for a request that is not a GET the custom header and, with a
// body, the JSON content type.
func call(t testing.TB, r *Running, method, path, body string, hdr map[string]string) (int, []byte, http.Header) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, r.Base+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if method != "GET" && method != "HEAD" {
		req.Header.Set(web.RequestHeader, web.RequestHeaderValue)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := r.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

// getJSON gets a path and decodes the answer strictly into v.
func getJSON(t testing.TB, r *Running, path string, v any) {
	t.Helper()
	code, b, _ := call(t, r, "GET", path, "", nil)
	if code != 200 {
		t.Fatalf("GET %s = %d %s", path, code, b)
	}
	strictDecode(t, b, v)
}

// errorCode returns the code of an error body.
func errorCode(b []byte) string {
	var e struct{ Code string }
	_ = json.Unmarshal(b, &e)
	return e.Code
}

// sseFrame is one frame of an event stream.
type sseFrame struct{ id, event, data string }

// openStream opens the page's stream with the token and returns its frames and a function that ends it.
func openStream(t testing.TB, r *Running, query string) (<-chan sseFrame, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "GET", r.Base+"/api/stream"+query, nil)
	req.Header.Set("Authorization", "Bearer "+r.Web.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		cancel()
		t.Fatalf("stream = %d", resp.StatusCode)
	}
	ch := make(chan sseFrame, 4096)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(ch)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		var cur sseFrame
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if cur.event != "" {
					ch <- cur
				}
				cur = sseFrame{}
			case strings.HasPrefix(line, "id: "):
				cur.id = line[4:]
			case strings.HasPrefix(line, "event: "):
				cur.event = line[7:]
			case strings.HasPrefix(line, "data: "):
				cur.data = line[6:]
			}
		}
	}()
	stop := func() { cancel(); resp.Body.Close(); wg.Wait() }
	t.Cleanup(stop)
	return ch, stop
}

// next waits for the next frame.
func next(t testing.TB, ch <-chan sseFrame) sseFrame {
	t.Helper()
	select {
	case f, ok := <-ch:
		if !ok {
			t.Fatal("the stream ended")
		}
		return f
	case <-time.After(guard):
		t.Fatal("no frame")
	}
	return sseFrame{}
}

// evSeq decodes an ev frame into its tab and event head.
func evSeq(t testing.TB, f sseFrame) (string, wire.Base) {
	t.Helper()
	if f.event != "ev" {
		t.Fatalf("frame %q, want ev: %s", f.event, f.data)
	}
	var d struct {
		Tab string
		Ev  json.RawMessage
	}
	strictDecode(t, []byte(f.data), &d)
	var h wire.Base
	if err := json.Unmarshal(d.Ev, &h); err != nil {
		t.Fatal(err)
	}
	return d.Tab, h
}

func TestTheFakeServerServesTheRealUIBehindTheRealEnvelope(t *testing.T) {
	r := StartT(t, Options{})
	code, body, hdr := call(t, r, "GET", "/", "", nil)
	if code != 200 || !strings.Contains(string(body), "<html") || !strings.HasPrefix(hdr.Get("Content-Type"), "text/html") {
		t.Fatalf("GET / = %d %s", code, hdr.Get("Content-Type"))
	}
	if !strings.Contains(hdr.Get("Content-Security-Policy"), "script-src 'self'") || hdr.Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("headers: %v", hdr)
	}
	// The token URL signs a browser in; without a credential nothing is served; the fake routes do not weaken the envelope.
	resp, err := http.Get(r.Base + "/api/hello")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("hello without a token = %d", resp.StatusCode)
	}
	noClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if resp, err := noClient.Get(r.URL); err != nil || resp.StatusCode != http.StatusSeeOther || len(resp.Cookies()) != 1 {
		t.Errorf("the printed URL: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}
	if code, b, _ := call(t, r, "POST", "/api/_fake/step", "", map[string]string{web.RequestHeader: ""}); code != 403 || errorCode(b) != "csrf" {
		t.Errorf("a control route without the custom header = %d %s", code, b)
	}
	if code, b, _ := call(t, r, "POST", "/api/_fake/step", "", map[string]string{"Origin": "http://evil.example"}); code != 403 {
		t.Errorf("a control route from another origin = %d %s", code, b)
	}
	// A directory of files can stand in for the embedded UI.
	d := StartT(t, Options{UI: fstest.MapFS{"index.html": {Data: []byte("<!doctype html><title>mine</title>")}}})
	if _, b, _ := call(t, d, "GET", "/", "", nil); !strings.Contains(string(b), "<title>mine</title>") {
		t.Errorf("custom UI: %s", b)
	}
}

func TestHelloAndSnapshotOverHTTP(t *testing.T) {
	r := StartT(t, Options{})
	var hello wire.Hello
	getJSON(t, r, "/api/hello", &hello)
	if hello.Boot != Boot || hello.Active != TabID || len(hello.Tabs) != 1 || hello.Server.Addr != r.Addr || !hello.Server.Loopback || hello.StreamAfter != 0 ||
		hello.Limits.MaxMessage != 256<<10 || hello.Limits.MaxQuestions != 64 || hello.Version == "" {
		t.Errorf("hello = %+v", hello)
	}
	var snap wire.TabSnapshot
	getJSON(t, r, "/api/sessions/shop/snapshot", &snap)
	if snap.Tab.ID != TabID || snap.Seq != uint64(len(Shop().History)) || len(snap.Questions) != 1 || len(snap.Keyframe) == 0 {
		t.Errorf("snapshot: seq %d questions %d keyframe %d", snap.Seq, len(snap.Questions), len(snap.Keyframe))
	}
	var list struct{ Tabs []wire.TabSummary }
	getJSON(t, r, "/api/sessions", &list)
	var projects struct{ Projects []wire.Project }
	getJSON(t, r, "/api/projects", &projects)
	if len(list.Tabs) != 1 || len(projects.Projects) != 3 {
		t.Errorf("lists: %d tabs %d projects", len(list.Tabs), len(projects.Projects))
	}
	for path, want := range map[string]int{"/api/sessions/nope/snapshot": 404, "/api/sessions/A_B/snapshot": 400, "/api/sessions/shop/nothing": 404} {
		if code, b, _ := call(t, r, "GET", path, "", nil); code != want {
			t.Errorf("GET %s = %d %s, want %d", path, code, b, want)
		}
	}
}

func TestTheContinuationArrivesOnTheStreamAndEndsInTheSameStateAsTheJournal(t *testing.T) {
	r := StartT(t, Options{})
	var first wire.TabSnapshot
	_, firstBody, _ := call(t, r, "GET", "/api/sessions/shop/snapshot", "", nil)
	strictDecode(t, firstBody, &first)
	ch, _ := openStream(t, r, "?after=0")
	live := Shop().Live
	for i := range live {
		code, b, _ := call(t, r, "POST", "/api/_fake/step", "", nil)
		if code != 200 {
			t.Fatalf("step %d = %d %s", i, code, b)
		}
		f := next(t, ch)
		tab, h := evSeq(t, f)
		if tab != TabID || h.Seq != first.Seq+uint64(i)+1 || h.K != wire.KindOf(live[i]) {
			t.Fatalf("frame %d: tab %s head %+v, want %s seq %d", i, tab, h, wire.KindOf(live[i]), first.Seq+uint64(i)+1)
		}
		if f.id == "" {
			t.Errorf("frame %d has no hub id", i)
		}
	}
	// Nothing is left; another step says so.
	var rest struct {
		Stepped   bool
		Remaining int
	}
	_, b, _ := call(t, r, "POST", "/api/_fake/step", "", nil)
	strictDecode(t, b, &rest)
	if rest.Stepped || rest.Remaining != 0 {
		t.Errorf("past the end: %+v", rest)
	}
	// The journal now holds history and continuation: a late joiner sees the same.
	var end wire.TabSnapshot
	getJSON(t, r, "/api/sessions/shop/snapshot", &end)
	if end.Seq != first.Seq+uint64(len(live)) || len(end.Events) != len(first.Events)+len(live) || end.Now <= first.Now {
		t.Errorf("end: seq %d events %d now %v (start %d, %d, %v)", end.Seq, len(end.Events), end.Now, first.Seq, len(first.Events), first.Now)
	}
	if end.Meta.Running == nil || *end.Meta.Running {
		t.Errorf("the turn is still running at the end of the script")
	}
	// Reset: the page is told, and the snapshot is byte for byte the first one again.
	_, _, _ = call(t, r, "POST", "/api/_fake/reset", "", nil)
	var sawReset bool
	for !sawReset {
		f := next(t, ch)
		sawReset = f.event == "reset"
	}
	_, again, _ := call(t, r, "GET", "/api/sessions/shop/snapshot", "", nil)
	if !bytes.Equal(again, firstBody) {
		t.Error("after reset the snapshot differs from the first")
	}
	// play publishes the rest at once.
	_, b, _ = call(t, r, "POST", "/api/_fake/play", "", nil)
	var played struct{ Played, Remaining int }
	strictDecode(t, b, &played)
	if played.Played != len(live) {
		t.Errorf("played %d, want %d", played.Played, len(live))
	}
}

func TestAMessageAndAnAnswerComeBackAsEvents(t *testing.T) {
	r := StartT(t, Options{})
	ch, _ := openStream(t, r, "")
	// The canned manager is in the middle of a turn: the line waits its turn.
	code, b, _ := call(t, r, "POST", "/api/sessions/shop/messages", `{"text":"keep going"}`, nil)
	var res wire.SendResult
	strictDecode(t, b, &res)
	if code != 200 || !res.Queued || res.Position != 1 {
		t.Fatalf("message during the turn = %d %+v", code, res)
	}
	if f := next(t, ch); f.event != "meta" || !strings.Contains(f.data, `"queued":[{"id":"`) {
		t.Errorf("frame = %+v", f)
	}
	// The question of the snapshot is answered once.
	qid := ShopQuestion().ID
	code, b, _ = call(t, r, "POST", "/api/questions/"+qid+"/answer", `{"choice":2}`, nil)
	var ans wire.AnswerResult
	strictDecode(t, b, &ans)
	if code != 200 || !ans.OK || ans.Rule != ShopQuestion().Rule {
		t.Fatalf("answer = %d %s", code, b)
	}
	var kinds []string
	for len(kinds) < 2 || kinds[len(kinds)-1] != "state" {
		f := next(t, ch)
		if f.event == "ev" {
			_, h := evSeq(t, f)
			kinds = append(kinds, h.K)
		}
	}
	if strings.Join(kinds, " ") != "answer state" {
		t.Errorf("an answer produced %v", kinds)
	}
	for body, want := range map[string]struct {
		qid  string
		code int
		err  string
	}{
		`{"choice":1}`:       {qid, 409, "answered"},
		`{"choice":9}`:       {qid, 409, "answered"},
		`{"choice":1} `:      {"q_zzzzzzzzzzzzzzzzzzzzzzzzzz", 404, "no_question"},
		`{"choice":1,"x":1}`: {qid, 400, "bad_json"},
	} {
		code, b, _ := call(t, r, "POST", "/api/questions/"+want.qid+"/answer", body, nil)
		if code != want.code || errorCode(b) != want.err {
			t.Errorf("answer %s to %s = %d %s, want %d %s", body, want.qid, code, b, want.code, want.err)
		}
	}
	_, _, _ = call(t, r, "POST", "/api/_fake/ask", "", nil)
	if code, b, _ := call(t, r, "POST", "/api/questions/"+LaterQuestion().ID+"/answer", `{"choice":9}`, nil); code != 400 || errorCode(b) != "bad_choice" {
		t.Errorf("a choice that is not 1, 2 or 3 = %d %s", code, b)
	}
	if code, b, _ := call(t, r, "POST", "/api/questions/not-a-question/answer", `{"choice":1}`, nil); code != 400 || errorCode(b) != "bad_request" {
		t.Errorf("bad question id = %d %s", code, b)
	}
	// Once the script has run to its end the manager is idle, and a message starts a turn that ends.
	_, _, _ = call(t, r, "POST", "/api/_fake/play", "", nil)
	code, b, _ = call(t, r, "POST", "/api/sessions/shop/messages", `{"text":"thanks"}`, nil)
	strictDecode(t, b, &res)
	if code != 200 || res.Queued {
		t.Errorf("message after the script = %d %+v", code, res)
	}
	if code, b, _ := call(t, r, "POST", "/api/sessions/shop/steer", `{"text":"x"}`, nil); code != 409 || errorCode(b) != "idle" {
		t.Errorf("steer when idle = %d %s", code, b)
	}
}

func TestPrivilegedRoutesAskForAConfirmation(t *testing.T) {
	r := StartT(t, Options{})
	confirm := func(scope string) string {
		code, b, _ := call(t, r, "POST", "/api/confirm", `{"scope":"`+scope+`"}`, nil)
		var id wire.ConfirmID
		strictDecode(t, b, &id)
		if code != 200 || id.ID == "" || id.Scope != scope {
			t.Fatalf("confirm %q = %d %s", scope, code, b)
		}
		return id.ID
	}
	if code, b, h := call(t, r, "POST", "/api/sessions/shop/mode", `{"mode":"bypass"}`, nil); code != 428 || errorCode(b) != "confirm_required" || h.Get("X-Confirm-Scope") != "mode:bypass:shop" {
		t.Errorf("bypass without a confirmation = %d %s scope %q", code, b, h.Get("X-Confirm-Scope"))
	}
	if code, b, _ := call(t, r, "POST", "/api/sessions/shop/mode", `{"mode":"bypass"}`, map[string]string{web.ConfirmHeader: confirm("mode:yolo:shop")}); code != 403 || errorCode(b) != "confirm_invalid" {
		t.Errorf("bypass with the confirmation of another action = %d %s", code, b)
	}
	if code, b, _ := call(t, r, "POST", "/api/sessions/shop/mode", `{"mode":"bypass"}`, map[string]string{web.ConfirmHeader: confirm("mode:bypass:shop")}); code != 200 {
		t.Errorf("bypass with its confirmation = %d %s", code, b)
	}
	if code, _, _ := call(t, r, "POST", "/api/sessions/shop/mode", `{"mode":"plan"}`, nil); code != 200 {
		t.Errorf("plan needs none: %d", code)
	}
	if code, b, _ := call(t, r, "POST", "/api/sessions/shop/mode", `{"mode":"chaos"}`, nil); code != 400 || errorCode(b) != "bad_mode" {
		t.Errorf("unknown mode = %d %s", code, b)
	}
	flags := []string{"--mode", "yolo"}
	if code, _, _ := call(t, r, "POST", "/api/sessions/shop/restart", `{"kind":"restart","fresh":false,"flags":["--mode","yolo"]}`, nil); code != 428 {
		t.Errorf("a restart into yolo without a confirmation = %d", code)
	}
	if code, b, _ := call(t, r, "POST", "/api/sessions/shop/restart", `{"kind":"restart","fresh":false,"flags":["--mode","yolo"]}`, map[string]string{web.ConfirmHeader: confirm("restart:shop:" + D16(flags))}); code != 202 {
		t.Errorf("a restart into yolo with its confirmation = %d %s", code, b)
	}

	// New session: an untrusted project asks for the trust step first, with the challenge as the detail.
	body := `{"cwd":"/home/me/projects/orders","trustProject":true,"name":"orders"}`
	code, b, _ := call(t, r, "POST", "/api/sessions", body, nil)
	var e struct {
		Error  string
		Code   string
		Detail wire.TrustChallenge
	}
	strictDecode(t, b, &e)
	if code != 409 || e.Code != "trust_required" || e.Detail.Confirm == "" || !strings.HasPrefix(e.Detail.Scope, "trust:") || e.Detail.Dir != "/home/me/projects/orders" || len(e.Detail.Files) == 0 {
		t.Fatalf("trust step = %d %s", code, b)
	}
	if code, b, _ := call(t, r, "POST", "/api/sessions", body, map[string]string{web.ConfirmHeader: e.Detail.Confirm}); code != 201 {
		t.Fatalf("new session after the trust step = %d %s", code, b)
	} else {
		var created struct{ Tab wire.TabSummary }
		strictDecode(t, b, &created)
		if created.Tab.Name != "orders" || created.Tab.Cwd != "/home/me/projects/orders" || len(r.Host.Tabs()) != 2 {
			t.Errorf("created %+v, %d tabs", created.Tab, len(r.Host.Tabs()))
		}
	}
	if code, b, _ := call(t, r, "POST", "/api/sessions", e.Error, nil); code == 201 {
		t.Errorf("a request that is not JSON created a tab: %s", b)
	}
	if code, b, _ := call(t, r, "POST", "/api/sessions", `{"cwd":"/etc","trustProject":false}`, nil); code != 403 || errorCode(b) != "not_a_project" {
		t.Errorf("a directory that is not a project = %d %s", code, b)
	}
	if code, _, _ := call(t, r, "POST", "/api/sessions", `{"cwd":"`+Cwd+`","name":"fresh","goalText":"do it"}`, nil); code != 201 {
		t.Errorf("a trusted project needs no step: %d", code)
	}
}

func TestSessionsCanBeRenamedStoppedResumedAndClosed(t *testing.T) {
	r := StartT(t, Options{})
	if code, b, _ := call(t, r, "PATCH", "/api/sessions/shop", `{"name":"shop two"}`, nil); code != 200 {
		t.Errorf("rename = %d %s", code, b)
	}
	if code, b, _ := call(t, r, "PATCH", "/api/sessions/shop", `{"name":""}`, nil); code != 400 || errorCode(b) != "bad_name" {
		t.Errorf("empty name = %d %s", code, b)
	}
	if code, b, _ := call(t, r, "POST", "/api/sessions/shop/stop", "", nil); code != 200 {
		t.Errorf("stop = %d %s", code, b)
	}
	if code, b, _ := call(t, r, "POST", "/api/sessions/shop/stop", "", nil); code != 409 || errorCode(b) != "idle" {
		t.Errorf("stop twice = %d %s", code, b)
	}
	for body, want := range map[string]struct {
		code int
		err  string
	}{
		`{"from":"latest"}`:                 {201, ""},
		`{"from":"20261007-153000-91ac03"}`: {201, ""},
		`{"from":"20261001-090000-0a7d55"}`: {409, "not_resumable"},
		`{"from":"20200101-000000-000000"}`: {404, "no_session"},
	} {
		code, b, _ := call(t, r, "POST", "/api/sessions/resume", body, nil)
		if code != want.code || (want.err != "" && errorCode(b) != want.err) {
			t.Errorf("resume %s = %d %s", body, code, b)
		}
	}
	var list struct{ Tabs []wire.TabSummary }
	getJSON(t, r, "/api/sessions", &list)
	if len(list.Tabs) != 3 {
		t.Fatalf("%d tabs", len(list.Tabs))
	}
	if code, _, _ := call(t, r, "DELETE", "/api/sessions/"+list.Tabs[1].ID, "", nil); code != 200 {
		t.Errorf("close = %d", code)
	}
	if code, b, _ := call(t, r, "DELETE", "/api/sessions/nope", "", nil); code != 404 {
		t.Errorf("close unknown = %d %s", code, b)
	}
	if code, b, _ := call(t, r, "DELETE", "/api/sessions/shop", `{}`, nil); code != 400 {
		t.Errorf("close with a body = %d %s", code, b)
	}
}

func TestEveryCannedPageAnswerIsInTheContractsShape(t *testing.T) {
	r := StartT(t, Options{})
	for _, c := range []struct {
		path string
		into any
	}{
		{"/api/cli", &wire.CLISpec{}},
		{"/api/recorded", &struct {
			Recorded []wire.RecordedSession
			MB       float64
		}{}},
		{"/api/recorded/20261008-101500-4be2f1/events", &struct {
			Events []json.RawMessage
			Next   string
		}{}},
		{"/api/models", &wire.ModelsView{}},
		{"/api/providers", &wire.ProvidersView{}},
		{"/api/sessions/shop/permissions", &wire.PermissionsView{}},
		{"/api/sessions/shop/trust", &wire.TrustView{}},
		{"/api/sessions/shop/mcp", &wire.MCPView{}},
		{"/api/sessions/shop/skills", &wire.SkillsView{}},
		{"/api/sessions/shop/config", &wire.ConfigView{}},
		{"/api/sessions/shop/rules", &struct{ Rules []wire.Rule }{}},
		{"/api/sessions/shop/slash", &struct{ Slash []wire.SlashEntry }{}},
		{"/api/sessions/shop/ws/index", &wire.WsIndex{}},
		{"/api/sessions/shop/ws/file?path=api/catalog/items.go&at=c01", &wire.WsContent{}},
		{"/api/sessions/shop/ws/diff?path=api/catalog/items.go&from=c01&to=live", &wire.WsDiff{}},
		{"/api/sessions/shop/complete?prefix=cart", &struct{ Paths []string }{}},
		{"/api/questions", &struct{ Questions []wire.OpenQuestion }{}},
		{"/api/trust/challenge?dir=/home/me/projects/docs", &wire.TrustChallenge{}},
		{"/api/schedule", &wire.ScheduleView{}},
		{"/api/schedule/next?cron=0+7+*+*+1-5", &wire.CronCheck{}},
		{"/api/schedule/jobs/j1/log", &wire.JobLog{}},
		{"/api/doctor/endpoints", &struct{ Endpoints []wire.DoctorEndpoint }{}},
		{"/api/update", &wire.UpdateStatus{}},
		{"/api/runs", &struct{ Runs []wire.RunInfo }{}},
	} {
		getJSON(t, r, c.path, c.into)
	}
	var idx wire.WsIndex
	getJSON(t, r, "/api/sessions/shop/ws/index", &idx)
	if len(idx.Cps) != 3 || len(idx.Tree) == 0 || idx.Version == "" {
		t.Errorf("index: %+v", idx)
	}
	var spec wire.CLISpec
	getJSON(t, r, "/api/cli", &spec)
	if spec.Commands[0].Mode == "" || len(spec.ChatSlash) == 0 {
		t.Errorf("spec: %+v", spec.Commands[0])
	}
	var cron wire.CronCheck
	getJSON(t, r, "/api/schedule/next?cron=nonsense", &cron)
	if cron.OK || cron.Err == "" {
		t.Errorf("a bad cron: %+v", cron)
	}
	// Files and diffs refuse what the Workspace refuses.
	for path, want := range map[string]struct {
		code int
		err  string
	}{
		"/api/sessions/shop/ws/file?path=../secret":          {400, "bad_path"},
		"/api/sessions/shop/ws/file?path=/etc/passwd":        {400, "bad_path"},
		"/api/sessions/shop/ws/file?path=a%5Cb":              {400, "bad_path"},
		"/api/sessions/shop/ws/file?path=a%00b":              {400, "bad_path"},
		"/api/sessions/shop/ws/file?path=":                   {400, "bad_path"},
		"/api/sessions/shop/ws/file?path=.env":               {403, "denied"},
		"/api/sessions/shop/ws/diff?path=a//b":               {400, "bad_path"},
		"/api/sessions/shop/ws/worktrees":                    {409, "not_isolated"},
		"/api/sessions/shop/ws/queue":                        {409, "not_isolated"},
		"/api/trust/challenge?dir=/etc":                      {403, "not_a_project"},
		"/api/recorded/20200101-000000-000000/events":        {404, "not_found"},
		"/api/schedule/jobs/j9/log":                          {404, "not_found"},
		"/api/sessions/nope/permissions":                     {404, "no_session"},
		"/api/sessions/shop/ws/file?path=api/../../etc/x":    {400, "bad_path"},
		"/api/sessions/shop/ws/file?path=%2e%2e/%2e%2e/x":    {400, "bad_path"},
		"/api/sessions/shop/ws/diff?path=.&from=c01&to=live": {400, "bad_path"},
	} {
		if code, b, _ := call(t, r, "GET", path, "", nil); code != want.code || errorCode(b) != want.err {
			t.Errorf("GET %s = %d %s, want %d %s", path, code, b, want.code, want.err)
		}
	}
}

func TestRoutesTheFakeDoesNotImplementSay501(t *testing.T) {
	r := StartT(t, Options{})
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/schedule/jobs", `{"cron":"* * * * *","goal":"x","dir":"` + Cwd + `","budgetUsd":1}`},
		{"PUT", "/api/schedule/jobs/j1", `{}`},
		{"DELETE", "/api/schedule/jobs/j1", ""},
		{"POST", "/api/sessions/shop/ws/revert", `{}`},
		{"POST", "/api/sessions/shop/ws/restore", `{}`},
		{"POST", "/api/providers/anthropic/key", `{}`},
		{"POST", "/api/update/install", ""},
	} {
		code, b, _ := call(t, r, c.method, c.path, c.body, nil)
		if code != 501 || errorCode(b) != "not_implemented" || !strings.Contains(string(b), c.path) {
			t.Errorf("%s %s = %d %s", c.method, c.path, code, b)
		}
	}
	// Unknown GETs stay 404, and the 501 does not hide the envelope's other refusals.
	if code, b, _ := call(t, r, "GET", "/api/nothing", "", nil); code != 404 || errorCode(b) != "not_found" {
		t.Errorf("unknown GET = %d %s", code, b)
	}
	if code, _, _ := call(t, r, "POST", "/api/update/install", "", map[string]string{web.RequestHeader: ""}); code != 403 {
		t.Errorf("a 501 route without the custom header = %d", code)
	}
}

func TestRunsStreamTheirOutput(t *testing.T) {
	r := StartT(t, Options{})
	ch, _ := openStream(t, r, "")
	code, b, _ := call(t, r, "POST", "/api/runs", `{"path":["config"],"flags":{"json":true}}`, nil)
	var started wire.RunStarted
	strictDecode(t, b, &started)
	if code != 202 || !strings.HasPrefix(started.ID, "r_") || started.Cmdline != "sleipnir config" {
		t.Fatalf("run = %d %s", code, b)
	}
	var lines, results int
	for results == 0 {
		f := next(t, ch)
		if f.event != "run" {
			continue
		}
		var rf wire.RunFrame
		strictDecode(t, []byte(f.data), &rf)
		if rf.ID != started.ID {
			t.Fatalf("frame for run %s", rf.ID)
		}
		lines += len(rf.Lines)
		if rf.Result != nil {
			results++
			if rf.Result.Exit != 0 || rf.Result.Card == nil {
				t.Errorf("result = %+v", rf.Result)
			}
		}
	}
	if lines == 0 {
		t.Error("no output lines")
	}
	if code, b, _ := call(t, r, "POST", "/api/runs", `{"path":["chat"]}`, nil); code != 403 || errorCode(b) != "tty_only" || !strings.Contains(string(b), "New session") {
		t.Errorf("chat = %d %s", code, b)
	}
	if code, b, h := call(t, r, "POST", "/api/runs", `{"path":["trust","add"]}`, nil); code != 428 || !strings.HasPrefix(h.Get("X-Confirm-Scope"), "run:") {
		t.Errorf("a privileged command without a confirmation = %d %s", code, b)
	}
	if code, b, _ := call(t, r, "POST", "/api/runs", `{"path":["nonesuch"]}`, nil); code != 400 || errorCode(b) != "bad_flags" {
		t.Errorf("unknown command = %d %s", code, b)
	}
	if code, b, _ := call(t, r, "POST", "/api/runs", `{"path":[]}`, nil); code != 400 {
		t.Errorf("no command = %d %s", code, b)
	}
}

func TestTwoFakeServersServeTheSameBytes(t *testing.T) {
	a, b := StartT(t, Options{}), StartT(t, Options{})
	for _, path := range []string{"/api/sessions/shop/snapshot", "/api/cli", "/api/models", "/api/sessions/shop/ws/index", "/api/sessions/shop/slash"} {
		_, x, _ := call(t, a, "GET", path, "", nil)
		_, y, _ := call(t, b, "GET", path, "", nil)
		if !bytes.Equal(x, y) || len(x) < 20 {
			t.Errorf("%s differs between two servers (%d and %d bytes)", path, len(x), len(y))
		}
	}
	if a.Web.Token() == b.Web.Token() {
		t.Error("two servers share a token")
	}
}

func TestTheContinuationPlaysByItselfAtSpeed(t *testing.T) {
	r := StartT(t, Options{Speed: 1000})
	waitFor(t, "the continuation to play", func() bool { return r.Host.FakeTab(TabID).Remaining() == 0 })
	var snap wire.TabSnapshot
	getJSON(t, r, "/api/sessions/shop/snapshot", &snap)
	if snap.Seq != uint64(len(Shop().History)+len(Shop().Live)) {
		t.Errorf("seq %d after the script played", snap.Seq)
	}
	// A server that is asked to stop while the script is still playing stops.
	slow, err := Start(Options{Speed: 0.001})
	if err != nil {
		t.Fatal(err)
	}
	if err := slow.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// waitFor polls a condition until it holds.
func waitFor(t testing.TB, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(guard)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPublishFrameHandsFramesToTheHubWithTheirClass(t *testing.T) {
	hub := web.NewHub(web.HubConfig{})
	defer hub.Close()
	st, err := hub.Subscribe("ui", web.SubscribeOpts{})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var c Classifier
	e := &wire.Use{ID: "be-1", Rd: 1}
	wire.Stamp(e, 2, 3)
	if _, err := PublishFrame(hub, c.EvFrame("shop", e)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), guard)
	defer cancel()
	got, err := st.Next(ctx)
	if err != nil || got.Type != "ev" || !got.Coalescable || got.Key != "use/shop/be-1" || got.Critical || !bytes.Contains(got.Data, []byte(`"tab":"shop"`)) {
		t.Errorf("hub event = %+v (%v)", got, err)
	}
	if _, err := PublishFrame(hub, wire.Frame{Type: "ev", Data: make(chan int)}); err == nil {
		t.Error("an unencodable frame was published")
	}
}
