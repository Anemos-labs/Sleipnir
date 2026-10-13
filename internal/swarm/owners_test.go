package swarm

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// prefixOwners owns a path by its leading directory: "web/" -> roles.
type prefixOwners map[string][]string

func (p prefixOwners) OwnersOf(rel string) []string {
	for prefix, roles := range p {
		if strings.HasPrefix(rel, prefix) {
			return roles
		}
	}
	return nil
}

// Only an agent of the owning role writes an owned path. A backend worker that tries is
// refused, with a tool error that names the owner and says how to hand the work over
// (classified as a scope refusal, logged as a lease event of action "owner", and raised
// as an alert); the manager is refused too; a frontend worker writes it; paths nobody
// owns are unaffected.
func TestOnlyTheOwningRoleWritesAnOwnedPath(t *testing.T) {
	sc := script{
		"be-1": {act(rvToolCall{"edit", map[string]any{"path": "web/app.css"}}), act(rvToolCall{"edit", map[string]any{"path": "api/server.go"}})},
		"fe-1": {act(rvToolCall{"edit", map[string]any{"path": "web/app.css"}})},
	}
	r := newRVRigWith(t, Config{}, sc.fn(), func(d *Deps) { d.Owners = prefixOwners{"web/": {"frontend"}} })
	r.sw.StartManager()
	be, err := r.sw.Spawn(SpawnReq{Role: "backend", Title: "api", Files: []string{"api"}, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	fe, err := r.sw.Spawn(SpawnReq{Role: "frontend", Title: "page", Files: []string{"web"}, By: "mgr"})
	if err != nil {
		t.Fatal(err)
	}
	if be != "be-1" || fe != "fe-1" {
		t.Fatalf("spawned %s and %s", be, fe)
	}
	rvWait(t, "both workers to finish their runs", func() bool {
		return r.prov.callsFor(be) >= 3 && r.prov.callsFor(fe) >= 2 && r.idle(be) && r.idle(fe)
	})

	const refusal = "web/app.css belongs to the frontend role: only a frontend worker may change it. Leave it alone; " +
		"if your task needs the change, ask the manager (mail) to create a task for a frontend worker"
	if !r.prov.sawEver(refusal) {
		t.Fatal("the backend worker was not told who owns web/app.css")
	}
	if !r.prov.sawEver("edited web/app.css") || !r.prov.sawEver("edited api/server.go") {
		t.Fatal("the frontend worker's write to its own path, or the backend's to a path nobody owns, was refused")
	}

	// The refusal in the log: no lease for the refused write, a scope error kind on the
	// tool result, a lease event of its own, an alert (gone with the worker's run).
	acquired := map[string][]string{}
	var owned []map[string]any
	for _, e := range r.log.OfType(events.TypeLease) {
		var d map[string]any
		_ = json.Unmarshal(e.Data, &d)
		switch d["action"] {
		case "acquire":
			acquired[d["agent"].(string)] = append(acquired[d["agent"].(string)], d["path"].(string))
		case "owner":
			owned = append(owned, d)
		}
	}
	if want := map[string][]string{fe: {"web/app.css"}, be: {"api/server.go"}}; !reflect.DeepEqual(acquired, want) {
		t.Errorf("leases acquired: %v, want %v", acquired, want)
	}
	if len(owned) != 1 || owned[0]["agent"] != be || owned[0]["path"] != "web/app.css" || !reflect.DeepEqual(owned[0]["owners"], []any{"frontend"}) {
		t.Errorf("owner lease events: %v", owned)
	}
	kinds := map[string][]string{}
	for _, e := range r.log.OfType(events.TypeToolResult) {
		var d struct {
			Error bool           `json:"error"`
			Meta  map[string]any `json:"meta"`
		}
		_ = json.Unmarshal(e.Data, &d)
		if d.Error {
			k, _ := d.Meta["error_kind"].(string)
			kinds[e.Agent] = append(kinds[e.Agent], k)
		}
	}
	if want := []string{agent.ErrKindScope}; !reflect.DeepEqual(kinds[be], want) || len(kinds[fe]) != 0 {
		t.Errorf("failed tool results by agent and kind: %v", kinds)
	}
	var alerts []string
	for _, e := range r.log.OfType(events.TypeBoardOp) {
		var d struct{ Op, Kind, Key, Text string }
		_ = json.Unmarshal(e.Data, &d)
		if d.Op == "alert" && d.Kind == "scope" && d.Key == "owner:"+be {
			alerts = append(alerts, d.Text)
		}
	}
	if want := []string{"be-1 tried to change a file of the frontend role"}; !reflect.DeepEqual(alerts, want) {
		t.Errorf("owner alerts: %q, want %q", alerts, want)
	}
	for _, a := range r.sw.Board.Snapshot().Alerts {
		if a.Key == "owner:"+be {
			t.Errorf("the alert outlived the worker's run: %+v", a)
		}
	}

	// The manager is refused by the guard too, with what it can do instead; so is an id
	// that is no member (no role owns anything for it).
	err = r.sw.Leases.BeforeWrite(r.sw.ManagerID(), "web/app.css")
	if err != nil {
		t.Errorf("manager should honor active leases without role-only rejection: %v", err)
	}
	if err := r.sw.Leases.BeforeWrite("ghost", "web/x.html"); err == nil {
		t.Error("an agent of no role wrote an owned path")
	}
	if err := r.sw.Leases.BeforeWrite(r.sw.ManagerID(), "README.md"); err != nil {
		t.Errorf("a path nobody owns: %v", err)
	}
}

// In an isolated run the guard refuses the write too, and what gets around it (a shell
// command) is caught by the merge queue: a submission that changed a path another role
// owns goes back to the worker, which reverts it, and only its own change lands.
func TestTheMergeQueueRefusesAnOwnedPathAShellCommandChanged(t *testing.T) {
	sc := script{
		"be-1": {
			act(writeCall("web/app.css", "body { color: red }\n")),
			act(writeCall("a.txt", "alpha edited\n"), rvToolCall{"sneak", map[string]any{"path": "web/app.css", "content": "body { color: red }\n"}}),
			act(doneCall("T1", "edited a")),
			act(rvToolCall{"sneak", map[string]any{"path": "web/app.css", "content": "body {}\n"}}),
			act(doneCall("T1", "edited a, put the style sheet back")),
		},
	}
	r := newIsoRig(t, isoOpts{
		cfg: Config{MaxWriters: 4}, verify: true, files: map[string]string{"web/app.css": "body {}\n"},
		tweak: func(d *Deps) { d.Owners = prefixOwners{"web/": {"frontend"}} },
	}, sc.fn())
	r.sw.StartManager()
	r.mustSpawn("backend", "edit a", "a.txt")
	r.waitStatus("T1", StatusReview)
	if !r.prov.sawEver("web/app.css belongs to the frontend role: only a frontend worker may change it. Leave it alone") {
		t.Fatal("the guard of an isolated run let the backend worker write web/app.css")
	}
	const bounce = "Not done: the merge queue refused your work: changed 1 file(s) it may not change: web/app.css belongs to the frontend role. " +
		"Revert those files in your tree: only a frontend worker may change them"
	if !r.prov.sawEver(bounce) {
		t.Fatal("the worker was not told that its submission changed a file the frontend role owns")
	}
	if st := r.q.Status(); st.Rejected != 1 || st.Merged != 1 {
		t.Fatalf("queue: %+v", st)
	}
	if res := r.accept("T1"); res.IsError {
		t.Fatal(res.Text)
	}
	r.finish()
	if got := readText(t, r.repo+"/web/app.css"); got != "body {}\n" {
		t.Fatalf("web/app.css = %q: the backend worker's change reached the checkout", got)
	}
	if got := readText(t, r.repo+"/a.txt"); got != "alpha edited\n" {
		t.Fatalf("a.txt = %q", got)
	}
}

// Several owners are named together, and either may write.
func TestAPathSeveralRolesOwn(t *testing.T) {
	roles := map[string]string{"fe-1": "frontend", "dc-1": "docs", "be-1": "backend"}
	l := NewLeases(0, nil)
	l.SetOwners(prefixOwners{"site/": {"docs", "frontend"}}, func(a string) string { return roles[a] })
	for _, a := range []string{"fe-1", "dc-1"} {
		if err := l.BeforeWrite(a, "site/index.md"); err != nil {
			t.Errorf("%s: %v", a, err)
		}
		l.ReleaseAll(a)
	}
	err := l.BeforeWrite("be-1", "site/index.md")
	if err == nil || !strings.HasPrefix(err.Error(), "site/index.md belongs to the docs and frontend roles: only a docs or frontend worker may change it.") {
		t.Errorf("backend: %v", err)
	}
}
