package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/web/runner"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

const (
	sidOld    = "20260101-000000-aaaaaa"
	sidOlder  = "20251201-000000-bbbbbb"
	sidNew    = "20261009-000000-cccccc"
	sidHosted = "20261009-120000-dddddd"
	sidHeld   = "20251101-000000-eeeeee"
)

// recordedList is the answer of GET /api/recorded.
type recordedList struct {
	Recorded []recordedRow `json:"recorded"`
	MB       float64       `json:"mb"`
}

func TestRecordedListLeavesHostedSessionsOut(t *testing.T) {
	rg := newRig(t, nil)
	day := 24 * time.Hour
	rg.session(sidOld, 90*day, "fix the <b>failing</b> test")
	rg.session(sidNew, time.Hour, "second")
	rg.session(sidHosted, time.Minute, "hosted")
	rg.host.tabs = []wire.TabSummary{{ID: "shop", SID: sidHosted}}
	w := rg.do(req{method: "GET", path: "/api/recorded"})
	l := decode[recordedList](t, w)
	if len(l.Recorded) != 2 || l.Recorded[0].ID != sidNew || l.Recorded[1].ID != sidOld {
		t.Fatalf("list %s", w.Body.String())
	}
	r := l.Recorded[1]
	if r.First != "fix the <b>failing</b> test" || r.Model != "m-1" || r.Cost != 0.25 || !r.Resumable || r.Agents != 1 || r.AgeS < 89*86400 || r.Cwd != rg.project {
		t.Errorf("row %+v", r)
	}
	if strings.Contains(w.Body.String(), "<b>") {
		t.Error("markup in a response is not escaped")
	}
	if w := rg.do(req{method: "GET", path: "/api/recorded", noAuth: true}); w.Code != 401 {
		t.Errorf("no credential: %d", w.Code)
	}
}

func TestPrunePreviewConfirmAndApply(t *testing.T) {
	rg := newRig(t, nil)
	day := 24 * time.Hour
	rg.session(sidOld, 40*day, "old")
	rg.session(sidOlder, 60*day, "older")
	rg.session(sidNew, time.Hour, "new")
	held := rg.session(sidHeld, 100*day, "held elsewhere")
	release := holdSession(t, held)
	defer release()
	when := time.Now().Add(-100 * day)
	_ = os.Chtimes(held, when, when)
	rg.session(sidHosted, 200*day, "hosted here")
	rg.host.tabs = []wire.TabSummary{{ID: "shop", SID: sidHosted}}

	body := map[string]any{"olderThan": "30d", "keep": 0}
	w := rg.do(req{method: "POST", path: "/api/recorded/prune", body: body})
	plan := decode[planReply](t, w)
	if w.Code != 200 || plan.Applied || len(plan.List) != 2 || plan.List[0].ID != sidOlder || plan.List[1].ID != sidOld {
		t.Fatalf("preview %d %s", w.Code, w.Body.String())
	}
	if len(plan.Locked) != 1 || plan.Locked[0] != sidHeld || plan.Newest != 1 {
		t.Errorf("the held session is left alone and the hosted one counts as the newest: %+v", plan)
	}
	body["apply"] = true
	w = rg.do(req{method: "POST", path: "/api/recorded/prune", body: body})
	ids := []string{sidOld, sidOlder}
	sort.Strings(ids)
	scope := "prune:" + runner.D16(ids)
	if w.Code != http.StatusPreconditionRequired || w.Header().Get("X-Confirm-Scope") != scope || !strings.Contains(w.Body.String(), `"plan"`) {
		t.Fatalf("apply without a confirmation: %d %q %s", w.Code, w.Header().Get("X-Confirm-Scope"), w.Body.String())
	}
	stale := rg.confirm("prune:" + runner.D16([]string{sidOld}))
	if w := rg.do(req{method: "POST", path: "/api/recorded/prune", body: body, header: map[string]string{"X-Confirm": stale}}); w.Code != http.StatusConflict || errCode(w) != "changed" {
		t.Fatalf("a confirmation of another plan: %d %s", w.Code, w.Body.String())
	}
	for _, id := range []string{sidOld, sidOlder} {
		if _, err := os.Stat(filepath.Join(rg.state, "sessions", id)); err != nil {
			t.Fatalf("%s was deleted without the right confirmation", id)
		}
	}
	w = rg.do(req{method: "POST", path: "/api/recorded/prune", body: body, header: map[string]string{"X-Confirm": rg.confirm(scope)}})
	done := decode[planReply](t, w)
	if w.Code != 200 || !done.Applied || len(done.List) != 2 || done.Error != "" {
		t.Fatalf("apply: %d %s", w.Code, w.Body.String())
	}
	for id, gone := range map[string]bool{sidOld: true, sidOlder: true, sidNew: false, sidHeld: false, sidHosted: false} {
		_, err := os.Stat(filepath.Join(rg.state, "sessions", id))
		if os.IsNotExist(err) != gone {
			t.Errorf("%s: gone=%v, want %v", id, os.IsNotExist(err), gone)
		}
	}
	if len(rg.host.framesOf("recorded")) != 1 {
		t.Error("the page is told the list changed")
	}
	for name, b := range map[string]map[string]any{"bad age": {"olderThan": "3x"}, "negative keep": {"olderThan": "1d", "keep": -1}} {
		w := rg.do(req{method: "POST", path: "/api/recorded/prune", body: b})
		if w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body.String())
		}
	}
	if w := rg.do(req{method: "POST", path: "/api/recorded/prune", body: map[string]any{"olderThan": "3x"}}); errCode(w) != "bad_age" || !strings.Contains(w.Body.String(), "bad --older-than") {
		t.Errorf("bad age: %s", w.Body.String())
	}
}

func TestDeleteSelectedSessions(t *testing.T) {
	rg := newRig(t, nil)
	rg.session(sidOld, time.Hour, "a")
	rg.session(sidNew, time.Hour, "b")
	held := rg.session(sidHeld, time.Hour, "c")
	release := holdSession(t, held)
	defer release()
	rg.session(sidHosted, time.Hour, "d")
	rg.host.tabs = []wire.TabSummary{{ID: "shop", SID: sidHosted}}
	for name, tc := range map[string]struct {
		ids  []string
		st   int
		code string
	}{
		"none":       {[]string{}, 400, "bad_request"},
		"traversal":  {[]string{"../../etc"}, 400, "bad_request"},
		"not an id":  {[]string{"20261009-000000-CCCCCC"}, 400, "bad_request"},
		"missing":    {[]string{"20200101-000000-ffffff"}, 404, "not_found"},
		"hosted":     {[]string{sidOld, sidHosted}, 409, "hosted"},
		"held":       {[]string{sidHeld}, 409, "locked"},
		"no confirm": {[]string{sidOld, sidNew}, 428, "confirm_required"},
	} {
		w := rg.do(req{method: "POST", path: "/api/recorded/delete", body: map[string]any{"ids": tc.ids}})
		if w.Code != tc.st || errCode(w) != tc.code {
			t.Errorf("%s: %d %s, want %d %s", name, w.Code, w.Body.String(), tc.st, tc.code)
		}
	}
	ids := []string{sidNew, sidOld, sidOld}
	scope := "delete:" + runner.D16([]string{sidOld, sidNew})
	id := rg.confirm(scope)
	w := rg.do(req{method: "POST", path: "/api/recorded/delete", body: map[string]any{"ids": ids}, header: map[string]string{"X-Confirm": id}})
	done := decode[planReply](t, w)
	if w.Code != 200 || !done.Applied || len(done.List) != 2 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := rg.do(req{method: "POST", path: "/api/recorded/delete", body: map[string]any{"ids": []string{sidOld, sidNew}}, header: map[string]string{"X-Confirm": id}}); w.Code == 200 {
		t.Error("a confirmation was used twice")
	}
	for id, gone := range map[string]bool{sidOld: true, sidNew: true, sidHeld: false, sidHosted: false} {
		if _, err := os.Stat(filepath.Join(rg.state, "sessions", id)); os.IsNotExist(err) != gone {
			t.Errorf("%s: gone=%v", id, os.IsNotExist(err))
		}
	}
}

func TestRecordedEventsPagesTheTranslation(t *testing.T) {
	var calls atomic.Int32
	rg := newRig(t, func(o *Options) {
		o.Replay = func(_ context.Context, dir string) ([]json.RawMessage, error) {
			calls.Add(1)
			var out []json.RawMessage
			for i := range 5 {
				out = append(out, json.RawMessage(`{"k":"say","i":`+string(rune('0'+i))+`}`))
			}
			return out, nil
		}
	})
	rg.session(sidOld, time.Hour, "a")
	w := rg.do(req{method: "GET", path: "/api/recorded/" + sidOld + "/events?from=1&limit=3"})
	page := decode[struct {
		Events []json.RawMessage `json:"events"`
		Next   string            `json:"next"`
	}](t, w)
	if len(page.Events) != 3 || page.Next != "4" || string(page.Events[0]) != `{"k":"say","i":1}` {
		t.Fatalf("page %s", w.Body.String())
	}
	w = rg.do(req{method: "GET", path: "/api/recorded/" + sidOld + "/events?from=4"})
	if !strings.Contains(w.Body.String(), `"next":""`) {
		t.Errorf("last page %s", w.Body.String())
	}
	if calls.Load() != 1 {
		t.Errorf("a log that did not change is translated once: %d", calls.Load())
	}
	for path, st := range map[string]int{"/api/recorded/nope/events": 400, "/api/recorded/20200101-000000-ffffff/events": 404, "/api/recorded/" + sidOld + "/events?from=-1": 400} {
		if w := rg.do(req{method: "GET", path: path}); w.Code != st {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
	plain := newRig(t, nil)
	plain.session(sidOld, time.Hour, "a")
	if w := plain.do(req{method: "GET", path: "/api/recorded/" + sidOld + "/events"}); w.Code != http.StatusNotImplemented {
		t.Errorf("no translator: %d", w.Code)
	}
}

func TestWatchASessionAnotherProcessWrites(t *testing.T) {
	followed := make(chan string, 1)
	stop := make(chan struct{})
	rg := newRig(t, func(o *Options) {
		o.Follow = func(ctx context.Context, tab, dir string, publish func(wire.Frame)) error {
			followed <- tab
			publish(wire.Frame{Type: "ev", Tab: tab, Data: wire.EvFrame{Tab: tab, Ev: json.RawMessage(`{"k":"say"}`)}})
			select {
			case <-ctx.Done():
			case <-stop:
			}
			return nil
		}
	})
	rg.session(sidOld, time.Second, "live")
	w := rg.do(req{method: "POST", path: "/api/recorded/" + sidOld + "/watch"})
	tab := decode[struct {
		Tab wire.TabSummary `json:"tab"`
	}](t, w).Tab
	if w.Code != 201 || tab.ID != "w-"+sidOld || !tab.Headless || tab.SID != sidOld {
		t.Fatalf("watch: %d %s", w.Code, w.Body.String())
	}
	if got := <-followed; got != tab.ID {
		t.Errorf("followed as %q", got)
	}
	if w := rg.do(req{method: "POST", path: "/api/recorded/" + sidOld + "/watch"}); w.Code != 200 {
		t.Errorf("watched already: %d", w.Code)
	}
	if l := decode[struct {
		Tabs []wire.TabSummary `json:"tabs"`
	}](t, rg.do(req{method: "GET", path: "/api/recorded/watching"})); len(l.Tabs) != 1 {
		t.Errorf("watching %+v", l)
	}
	close(stop) // the session ends
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fs := rg.host.framesOf("tab"); len(fs) == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	fs := rg.host.framesOf("tab")
	if len(fs) != 2 || fs[0].Data.(wire.TabFrame).Op != "add" || fs[1].Data.(wire.TabFrame).Op != "update" || fs[1].Data.(wire.TabFrame).Tab.Headless {
		t.Fatalf("tab frames %+v", fs)
	}
	if w := rg.do(req{method: "DELETE", path: "/api/recorded/" + sidOld + "/watch"}); w.Code != 200 {
		t.Errorf("unwatch: %d", w.Code)
	}
	if fs := rg.host.framesOf("tab"); fs[len(fs)-1].Data.(wire.TabFrame).Op != "remove" {
		t.Errorf("the tab goes: %+v", fs[len(fs)-1])
	}
	rg.host.tabs = []wire.TabSummary{{ID: "shop", SID: sidNew}}
	rg.session(sidNew, time.Second, "hosted")
	if w := rg.do(req{method: "POST", path: "/api/recorded/" + sidNew + "/watch"}); w.Code != 409 || errCode(w) != "hosted" {
		t.Errorf("a hosted session: %d %s", w.Code, w.Body.String())
	}
	plain := newRig(t, nil)
	plain.session(sidOld, time.Second, "x")
	if w := plain.do(req{method: "POST", path: "/api/recorded/" + sidOld + "/watch"}); w.Code != http.StatusNotImplemented {
		t.Errorf("no follower: %d", w.Code)
	}
}

// An age that overflows a duration is refused, not read as a negative age every session is older than.
func TestPruneRefusesAnAgeThatOverflows(t *testing.T) {
	rg := newRig(t, nil)
	rg.session(sidOld, 20*time.Minute, "recent")
	for _, age := range []string{"NaNd", "Infd", "1e300d", "9999999999d"} {
		w := rg.do(req{method: "POST", path: "/api/recorded/prune", body: map[string]any{"olderThan": age, "keep": 0}})
		if w.Code != 400 || errCode(w) != "bad_age" {
			t.Errorf("%s: %d %s", age, w.Code, w.Body.String())
		}
	}
}

// keep left out is the CLI's --keep 20; an explicit 0 keeps nothing back.
func TestPruneKeepDefaultsLikeTheCLI(t *testing.T) {
	rg := newRig(t, nil)
	for i := range 3 {
		rg.session(fmt.Sprintf("2025010%d-000000-aaaaaa", i+1), time.Duration(40+i)*24*time.Hour, "old")
	}
	plan := decode[planReply](t, rg.do(req{method: "POST", path: "/api/recorded/prune", body: map[string]any{"olderThan": "30d"}}))
	if len(plan.List) != 0 || plan.Newest != 3 {
		t.Errorf("keep left out: %+v", plan)
	}
	plan = decode[planReply](t, rg.do(req{method: "POST", path: "/api/recorded/prune", body: map[string]any{"olderThan": "30d", "keep": 0}}))
	if len(plan.List) != 3 {
		t.Errorf("keep 0: %+v", plan)
	}
}

// A session whose tab restarts is still hosted: no lock holds it in the gap, and a delete or a prune must leave it for the start.
func TestARestartingTabsSessionIsHosted(t *testing.T) {
	rg := newRig(t, nil)
	rg.session(sidOld, 90*24*time.Hour, "restarting")
	rg.host.tabs = []wire.TabSummary{{ID: "shop"}} // its summary names no session while it restarts
	rg.host.hosted = map[string]string{sidOld: "shop"}
	w := rg.do(req{method: "POST", path: "/api/recorded/delete", body: map[string]any{"ids": []string{sidOld}}})
	if w.Code != 409 || errCode(w) != "hosted" {
		t.Errorf("delete: %d %s", w.Code, w.Body.String())
	}
	plan := decode[planReply](t, rg.do(req{method: "POST", path: "/api/recorded/prune", body: map[string]any{"olderThan": "1d", "keep": 0}}))
	if len(plan.List) != 0 {
		t.Errorf("prune: %+v", plan)
	}
	if l := decode[recordedList](t, rg.do(req{method: "GET", path: "/api/recorded"})); len(l.Recorded) != 0 {
		t.Errorf("listed as recorded: %+v", l)
	}
}

// Watching and unwatching a session whose follower ends at once, many times over, races on nothing (run with -race).
func TestWatchStress(t *testing.T) {
	rg := newRig(t, func(o *Options) {
		o.Follow = func(context.Context, string, string, func(wire.Frame)) error { return nil }
	})
	rg.session(sidOld, time.Second, "live")
	for range 300 {
		for range 2 {
			if w := rg.do(req{method: "POST", path: "/api/recorded/" + sidOld + "/watch"}); w.Code != 201 && w.Code != 200 {
				t.Fatalf("watch: %d %s", w.Code, w.Body.String())
			}
		}
		if w := rg.do(req{method: "DELETE", path: "/api/recorded/" + sidOld + "/watch"}); w.Code != 200 {
			t.Fatalf("unwatch: %d %s", w.Code, w.Body.String())
		}
	}
}
