package scan

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/gitx"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// The repository the tests scan: a tiny web app with a command injection in its handler.
var appFiles = map[string]string{
	"server/handler.go": `package server

import (
	"net/http"
	"os/exec"
)

// Ping runs ping against the host the request names.
func Ping(w http.ResponseWriter, r *http.Request) {
	host := r.URL.Query().Get("host")
	if host == "" {
		http.Error(w, "host?", 400)
		return
	}
	out, _ := exec.Command("sh", "-c", "ping -c1 "+host).CombinedOutput()
	w.Write(out)
}

func Health(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }
`,
	"server/routes.go": `package server

import "net/http"

func Routes(mux *http.ServeMux) {
	mux.HandleFunc("/ping", Ping)
	mux.HandleFunc("/health", Health)
}
`,
	"web/app.js": `export function render(el, name) {
  if (name && name.length > 0) {
    el.innerHTML = "<b>" + name + "</b>";
  }
}

const greet = async (who) => {
  return "hi " + who;
};
`,
	"tools/util.py": `def parse(s):
    if not s:
        return None
    for part in s.split(","):
        if part and part.strip():
            yield part


def other():
    return 1
`,
	"vendor/lib/x.go":          "package lib\n\nfunc X() {}\n",
	"web/app.min.js":           "function a(){}\n",
	"server/handler_test.go":   "package server\n\nimport \"testing\"\n\nfunc TestPing(t *testing.T) {}\n",
	"config/settings.yaml":     "debug: true\ncors: \"*\"\n",
	"assets/logo.png":          "\x89PNG\x00\x00binary",
	"docs/README.md":           "# app\n",
	"package-lock.json":        "{}\n",
	"server/data/fixture.json": "{\"a\": 1}\n",
}

// gitRepo makes a repository with files committed and returns its root and commit.
func gitRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return dir, strings.TrimSpace(string(out))
}

func TestInventoryCountsWhatThePersonWrote(t *testing.T) {
	root, commit := gitRepo(t, appFiles)
	repo, err := gitx.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := TakeInventory(context.Background(), repo, commit, "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]File{}
	for _, f := range inv.Files {
		got[f.Path] = f
	}
	for _, p := range []string{"server/handler.go", "server/routes.go", "web/app.js", "tools/util.py", "server/handler_test.go", "config/settings.yaml"} {
		if _, ok := got[p]; !ok {
			t.Errorf("%s is not in the inventory: %v", p, inv.Files)
		}
	}
	// nothing is left out: what no person wrote is scanned too, marked by kind
	for p, kind := range map[string]string{"vendor/lib/x.go": KindVendored, "web/app.min.js": KindGenerated, "assets/logo.png": KindBinary,
		"docs/README.md": KindOther, "package-lock.json": KindGenerated, "server/data/fixture.json": KindData, "server/handler.go": ""} {
		if f, ok := got[p]; !ok || f.Kind != kind {
			t.Errorf("%s: kind %q (listed %v), want %q", p, f.Kind, ok, kind)
		}
	}
	if got["vendor/lib/x.go"].Funcs != 0 || got["assets/logo.png"].Lines != 0 || got["assets/logo.png"].Bytes == 0 {
		t.Errorf("vendored functions or binary lines counted: %+v %+v", got["vendor/lib/x.go"], got["assets/logo.png"])
	}
	if !got["server/handler_test.go"].Test || got["server/handler.go"].Test {
		t.Error("test files are not told apart")
	}
	fn := map[string]Func{}
	for _, f := range inv.Functions {
		fn[f.File+":"+f.Name] = f
	}
	ping := fn["server/handler.go:Ping"]
	if ping.Line != 9 || ping.Complexity != 2 {
		t.Errorf("Ping = %+v, want line 9 and complexity 2 (one if)", ping)
	}
	if f := fn["web/app.js:render"]; f.Line != 1 || f.End != 5 || f.Complexity != 3 {
		t.Errorf("render = %+v, want lines 1-5, complexity 3 (if, &&)", f)
	}
	if _, ok := fn["web/app.js:greet"]; !ok {
		t.Error("the arrow function greet is not found")
	}
	if f := fn["tools/util.py:parse"]; f.Line != 1 || f.End != 6 || f.Complexity != 5 {
		t.Errorf("parse = %+v, want lines 1-6, complexity 5 (if, for, if, and)", f)
	}
	if len(inv.Skipped) != 0 {
		t.Errorf("skipped = %v", inv.Skipped)
	}
	focused, err := TakeInventory(context.Background(), repo, commit, "web")
	if err != nil {
		t.Fatal(err)
	}
	if len(focused.Files) != 2 || focused.Files[0].Path != "web/app.js" || focused.Files[1].Path != "web/app.min.js" {
		t.Errorf("a focus on web/ gave %v", focused.Files)
	}
}

func TestShardsKeepDirectoriesTogetherAndRespectTheSize(t *testing.T) {
	var inv Inventory
	for _, d := range []string{"a", "b", "c"} {
		for i := 0; i < 5; i++ {
			inv.Files = append(inv.Files, File{Path: d + "/f" + string(rune('0'+i)) + ".go", Lines: 100, Funcs: 10})
		}
	}
	shards := MakeShards(inv, Depth{ShardFuncs: 30, ShardLines: 10000})
	total := 0
	for _, s := range shards {
		if s.Funcs > 30 {
			t.Errorf("%s has %d functions", s.ID, s.Funcs)
		}
		dirs := map[string]bool{}
		for _, f := range s.Files {
			dirs[filepath.Dir(f)] = true
		}
		if len(dirs) > 1 {
			t.Errorf("%s mixes directories once a third full: %v", s.ID, s.Files)
		}
		total += len(s.Files)
	}
	if total != 15 {
		t.Errorf("shards hold %d files, want 15", total)
	}
}

func TestCiteIsCheckedAgainstTheCommit(t *testing.T) {
	root, commit := gitRepo(t, appFiles)
	repo, _ := gitx.Open(root)
	code := CodeAt(repo, commit)
	ctx := context.Background()
	c, note, err := CheckCite(ctx, code, Cite{File: "server/handler.go", Line: 15, Quote: `out, _ := exec.Command("sh", "-c", "ping -c1 "+host).CombinedOutput()`})
	if err != nil || c.Line != 15 || note != "" {
		t.Fatalf("an exact quote: %+v %q %v", c, note, err)
	}
	// white space aside, and a few lines off: kept at the right line, with a note
	c, note, err = CheckCite(ctx, code, Cite{File: "./server/handler.go", Line: 12, Quote: "host :=   r.URL.Query().Get(\"host\")\n  if host == \"\" {"})
	if err != nil || c.Line != 10 || c.File != "server/handler.go" || !strings.Contains(note, "line 10") {
		t.Fatalf("a near quote: %+v %q %v", c, note, err)
	}
	// the work tree changing does not change what is checked
	if err := os.WriteFile(filepath.Join(root, "server/handler.go"), []byte("package server\n// rewritten\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CheckCite(ctx, CodeAt(repo, commit), Cite{File: "server/handler.go", Line: 15, Quote: `"ping -c1 "+host`}); err != nil {
		t.Fatalf("the commit is not what is read: %v", err)
	}
	_, _, err = CheckCite(ctx, code, Cite{File: "server/handler.go", Line: 15, Quote: `exec.Command("bash", "-c", host)`})
	if err == nil || !strings.Contains(err.Error(), "not in the file") || !strings.Contains(err.Error(), "ping -c1") {
		t.Fatalf("an invented quote: %v", err)
	}
	for _, bad := range []string{"../etc/passwd", "/etc/passwd", "server/../../x"} {
		if _, _, err := CheckCite(ctx, code, Cite{File: bad, Line: 1, Quote: "root:x:0:0"}); err == nil {
			t.Errorf("%s was read", bad)
		}
	}
}

func TestSeverityIsComputed(t *testing.T) {
	for _, c := range []struct {
		impact, reach, exploit, want string
	}{
		{SevCritical, ReachExternal, ExploitTrivial, SevCritical},
		{SevCritical, ReachInternal, ExploitTrivial, SevHigh},
		{SevCritical, ReachInternal, ExploitDifficult, SevMedium},
		{SevHigh, ReachExternal, ExploitDifficult, SevMedium},
		{SevCritical, ReachUnreachable, ExploitTrivial, SevLow},
		{SevHigh, ReachExternal, ExploitTheoretical, SevLow},
		{SevLow, ReachInternal, ExploitDifficult, SevLow},
	} {
		l := Lead{Impact: c.impact, Reach: c.reach, Exploit: c.exploit}
		if got := l.Severity(); got != c.want {
			t.Errorf("%s/%s/%s = %s, want %s", c.impact, c.reach, c.exploit, got, c.want)
		}
	}
	// a confirming verifier's more cautious reachability wins; a less cautious one does not
	l := Lead{Impact: SevCritical, Reach: ReachExternal, Exploit: ExploitTrivial, Verdicts: []Verdict{{By: "a", Confirm: true, Reach: ReachInternal}}}
	if l.Severity() != SevHigh {
		t.Errorf("the correction is ignored: %s", l.Severity())
	}
	l = Lead{Impact: SevCritical, Reach: ReachInternal, Exploit: ExploitTrivial, Verdicts: []Verdict{{By: "a", Confirm: true, Reach: ReachExternal}}}
	if l.Severity() != SevHigh {
		t.Errorf("a less cautious correction raised the severity: %s", l.Severity())
	}
}

func TestLeadStatusFollowsTheDepth(t *testing.T) {
	std, _ := DepthByID("standard")
	yes := func(by, role string) Verdict { return Verdict{By: by, Role: role, Confirm: true} }
	no := func(by string) Verdict { return Verdict{By: by, Confirm: false, Reason: "mitigated"} }
	high := Lead{By: "f", Impact: SevHigh, Reach: ReachExternal, Exploit: ExploitTrivial}
	med := Lead{By: "f", Impact: SevMedium, Reach: ReachExternal, Exploit: ExploitTrivial}
	for _, c := range []struct {
		name string
		l    Lead
		v    []Verdict
		p    []Proof
		want string
	}{
		{"no verdicts", med, nil, nil, LeadOpen},
		{"one confirm", med, []Verdict{yes("a", RoleVerifier)}, nil, LeadOpen},
		{"two confirms, no defender", med, []Verdict{yes("a", RoleVerifier), yes("b", RoleVerifier)}, nil, LeadOpen},
		{"two confirms with a defender", med, []Verdict{yes("a", RoleVerifier), yes("b", RoleDefender)}, nil, LeadConfirmed},
		{"the same agent twice counts once", med, []Verdict{yes("a", RoleVerifier), yes("a", RoleDefender)}, nil, LeadOpen},
		{"two rejects", med, []Verdict{no("a"), no("b")}, nil, LeadRejected},
		{"one each", med, []Verdict{yes("a", RoleDefender), no("b")}, nil, LeadOpen},
		{"changed mind", med, []Verdict{no("a"), yes("b", RoleDefender), yes("a", RoleVerifier)}, nil, LeadConfirmed},
		{"high, not reproduced", high, []Verdict{yes("a", RoleVerifier), yes("b", RoleDefender)}, nil, LeadUnproven},
		{"high, reproduced by the filer", high, []Verdict{yes("a", RoleVerifier), yes("b", RoleDefender)}, []Proof{{By: "f", Outcome: ProofReproduced}}, LeadUnproven},
		{"high, reproduced", high, []Verdict{yes("a", RoleVerifier), yes("b", RoleDefender)}, []Proof{{By: "c", Outcome: ProofReproduced}}, LeadConfirmed},
	} {
		l := c.l
		l.Verdicts, l.Proofs = c.v, c.p
		if got := l.Status(std); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// fakeCode serves files from a map.
type fakeCode map[string]string

func (f fakeCode) Read(_ context.Context, p string) ([]byte, error) {
	if s, ok := f[p]; ok {
		return []byte(s), nil
	}
	return nil, os.ErrNotExist
}

// harness is a store with one scan of appFiles and the tool over it.
func harness(t *testing.T, depth string) (*Tool, *Store) {
	t.Helper()
	root := t.TempDir()
	st := NewStore(root)
	st.OnChange = func(r *Run) {
		if err := WriteReport(root, r); err != nil {
			t.Errorf("report: %v", err)
		}
	}
	code := fakeCode(appFiles)
	var inv Inventory
	for _, p := range []string{"server/handler.go", "server/routes.go", "web/app.js"} {
		fs := Functions(langs[filepath.Ext(p)], p, []byte(appFiles[p]))
		inv.Files = append(inv.Files, File{Path: p, Lang: langs[filepath.Ext(p)], Lines: strings.Count(appFiles[p], "\n"), Funcs: len(fs)})
		inv.Functions = append(inv.Functions, fs...)
	}
	if _, err := st.Create(strings.Repeat("ab", 20), depth, "", inv); err != nil {
		t.Fatal(err)
	}
	return NewTool(st, func(string) Code { return code }), st
}

// call runs the tool as agent and returns its text and whether it failed.
func call(t *testing.T, tl *Tool, agent string, in map[string]any) (string, bool) {
	t.Helper()
	b, _ := json.Marshal(in)
	res, err := tl.Run(context.Background(), &tools.Call{Input: b, Env: &tools.Env{Agent: agent}})
	if err != nil {
		t.Fatal(err)
	}
	return res.Text, res.IsError
}

func mustOK(t *testing.T, tl *Tool, agent string, in map[string]any) string {
	t.Helper()
	out, bad := call(t, tl, agent, in)
	if bad {
		t.Fatalf("%v by %s: %s", in["action"], agent, out)
	}
	return out
}

func mustFail(t *testing.T, tl *Tool, agent string, in map[string]any, want string) {
	t.Helper()
	out, bad := call(t, tl, agent, in)
	if !bad || !strings.Contains(out, want) {
		t.Fatalf("%v by %s: want a refusal with %q, got %v %s", in["action"], agent, want, bad, out)
	}
}

var pingTrace = []map[string]any{
	{"file": "server/routes.go", "line": 6, "quote": `mux.HandleFunc("/ping", Ping)`, "note": "public route, no auth middleware"},
	{"file": "server/handler.go", "line": 10, "quote": `host := r.URL.Query().Get("host")`, "note": "host comes from the query string unchecked"},
	{"file": "server/handler.go", "line": 15, "quote": `exec.Command("sh", "-c", "ping -c1 "+host)`, "note": "concatenated into a shell command"},
}

func TestAScanFromMapToReport(t *testing.T) {
	tl, st := harness(t, "standard")
	out := mustOK(t, tl, "lead", map[string]any{"action": "status"})
	if !strings.Contains(out, "Phase: map") || !strings.Contains(out, "S1 is not mapped") {
		t.Fatalf("status: %s", out)
	}
	mustOK(t, tl, "mapper", map[string]any{"action": "shards", "claim": "S1"})
	mustFail(t, tl, "other", map[string]any{"action": "shards", "claim": "S1"}, "claimed by mapper")
	mustFail(t, tl, "mapper", map[string]any{"action": "point", "lane": "entry", "title": "GET /ping", "file": "server/routes.go", "line": 6, "quote": `mux.HandleFunc("/pong", Pong)`}, "not in the file")
	p := mustOK(t, tl, "mapper", map[string]any{"action": "point", "lane": "entry", "title": "GET /ping", "file": "server/routes.go", "line": 6, "quote": `mux.HandleFunc("/ping", Ping)`})
	if !strings.HasPrefix(p, "P1 recorded") {
		t.Fatalf("point: %s", p)
	}
	mustOK(t, tl, "mapper", map[string]any{"action": "point", "lane": "sink", "title": "shell command", "file": "server/handler.go", "line": 15, "quote": `exec.Command("sh", "-c", "ping -c1 "+host)`})
	mustFail(t, tl, "mapper", map[string]any{"action": "point", "lane": "sink", "title": "again", "file": "server/handler.go", "line": 15, "quote": `exec.Command("sh"`}, "P2 already")
	e := mustOK(t, tl, "mapper", map[string]any{"action": "edge", "from": "P1", "to": "server/handler.go:Ping", "relation": "routes", "file": "server/routes.go", "line": 6, "quote": `HandleFunc("/ping", Ping)`})
	if !strings.Contains(e, "extracted") {
		t.Fatalf("edge: %s", e)
	}
	if e := mustOK(t, tl, "mapper", map[string]any{"action": "edge", "from": "server/handler.go:Ping", "to": "P2", "relation": "calls"}); !strings.Contains(e, "inferred") {
		t.Fatalf("an edge without a quote: %s", e)
	}
	mustFail(t, tl, "mapper", map[string]any{"action": "map", "shard": "S1", "summary": "a server"}, "characters at least")
	r, _ := st.Snapshot("")
	for _, s := range r.Shards {
		mustOK(t, tl, "mapper", map[string]any{"action": "map", "shard": s.ID, "summary": "HTTP server with two routes; /ping shells out to ping with the host from the query string, /health answers ok. Nothing is authenticated."})
	}
	// a shard is not closed past a sink that no lead goes through and the note does not name
	r, _ = st.Snapshot("")
	sinkShard := ""
	for _, pt := range r.Points {
		if pt.ID == "P2" {
			sinkShard = pt.Shard
		}
	}
	mustFail(t, tl, "hunter", map[string]any{"action": "hunt", "shard": sinkShard, "note": "followed /ping and /health to their handlers, nothing to file here"}, "P2")
	// the filer files with a trace; the harness computes the severity
	lead := mustOK(t, tl, "hunter", map[string]any{"action": "lead", "title": "Command injection in /ping", "category": "command-injection",
		"claim": "Anyone can run shell commands on the server by sending host=x;id to GET /ping.", "trace": pingTrace,
		"impact": "critical", "reach": "external", "exploit": "trivial", "points": []string{"P1", "P2"}, "fix": "exec ping with host as its own argument, after validating it"})
	if !strings.HasPrefix(lead, "L1 filed") || !strings.Contains(lead, "severity critical") {
		t.Fatalf("lead: %s", lead)
	}
	mustFail(t, tl, "hunter2", map[string]any{"action": "lead", "title": "Same thing", "category": "command-injection",
		"claim": "Anyone can run shell commands on the server through the ping handler.", "trace": pingTrace[2:],
		"impact": "critical", "reach": "external", "exploit": "trivial"}, "same category and sink")
	mustFail(t, tl, "hunter", map[string]any{"action": "verdict", "id": "L1", "confirm": true, "note": "I checked my own trace and it holds up fine"}, "is yours")
	mustFail(t, tl, "v1", map[string]any{"action": "verdict", "id": "L1", "confirm": false, "reason": "mitigated", "note": "there is a check somewhere upstream of this"}, "cites the guard")
	out = mustOK(t, tl, "v1", map[string]any{"action": "verdict", "id": "L1", "confirm": true, "note": "routes.go registers /ping with no middleware; host flows into sh -c unescaped"})
	if !strings.Contains(out, "now open") {
		t.Fatalf("after one verdict: %s", out)
	}
	out = mustOK(t, tl, "v2", map[string]any{"action": "verdict", "id": "L1", "confirm": true, "role": "defender", "note": "looked for a validator or a mux wrapper: none; the empty check is the only one",
		"cites": []map[string]any{{"file": "server/handler.go", "line": 11, "quote": `if host == "" {`}}})
	if !strings.Contains(out, "now unproven") || !strings.Contains(out, "scan prove") {
		t.Fatalf("a confirmed critical lead must be reproduced at standard depth: %s", out)
	}
	// no sandbox: prove is refused, and the lead stays unproven
	mustFail(t, tl, "prover", map[string]any{"action": "prove", "id": "L1", "script": "echo"}, "sandbox")
	r, _ = st.Snapshot("")
	if rep := Check(r); rep.Phase != PhaseHunt || rep.Counts.Unproven != 1 {
		t.Fatalf("phase %s, counts %+v", rep.Phase, rep.Counts)
	}
	for _, s := range r.Shards {
		mustOK(t, tl, "hunter", map[string]any{"action": "hunt", "shard": s.ID, "note": "followed /ping and /health to their handlers; filed L1 for the shell command"})
	}
	// the reproduction, recorded as a sandbox run would record it
	if _, err := st.Update("", func(r *Run) error {
		r.Lead("L1").Proofs = append(r.Lead("L1").Proofs, Proof{By: "prover", Outcome: ProofReproduced, Exercised: true, Ready: true})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	out = mustOK(t, tl, "lead", map[string]any{"action": "status"})
	if !strings.Contains(out, "Phase: report") || !strings.Contains(out, "1 critical") {
		t.Fatalf("status before the report: %s", out)
	}
	out = mustOK(t, tl, "lead", map[string]any{"action": "report"})
	if !strings.Contains(out, "the scan is done") {
		t.Fatalf("report: %s", out)
	}
	r, _ = st.Snapshot("")
	dir := RunDir(st.Root(), r.Slug)
	md, _ := os.ReadFile(filepath.Join(dir, ReportFile))
	for _, want := range []string{"## Findings", "### L1 · Command injection in /ping", "| **Critical** | Confirmed, reproduced |", "`server/handler.go:15`", "| Entry points | 1 |"} {
		if !strings.Contains(string(md), want) {
			t.Errorf("REPORT.md lacks %q:\n%s", want, md)
		}
	}
	var sarif struct {
		Runs []struct {
			Results []struct {
				RuleID    string `json:"ruleId"`
				Level     string `json:"level"`
				Locations []struct {
					PhysicalLocation struct {
						ArtifactLocation struct{ URI string }    `json:"artifactLocation"`
						Region           struct{ StartLine int } `json:"region"`
					} `json:"physicalLocation"`
				} `json:"locations"`
				CodeFlows []any `json:"codeFlows"`
			} `json:"results"`
		} `json:"runs"`
	}
	sb, _ := os.ReadFile(filepath.Join(dir, SARIFFile))
	if err := json.Unmarshal(sb, &sarif); err != nil {
		t.Fatal(err)
	}
	res := sarif.Runs[0].Results
	if len(res) != 1 || res[0].RuleID != "sleipnir/command-injection" || res[0].Level != "error" ||
		res[0].Locations[0].PhysicalLocation.ArtifactLocation.URI != "server/handler.go" || res[0].Locations[0].PhysicalLocation.Region.StartLine != 15 {
		t.Errorf("SARIF results: %s", sb)
	}
	var m struct {
		Edges []struct{ Confidence string } `json:"edges"`
	}
	mb, _ := os.ReadFile(filepath.Join(dir, MapFile))
	_ = json.Unmarshal(mb, &m)
	if len(m.Edges) != 2 || m.Edges[0].Confidence != "EXTRACTED" || m.Edges[1].Confidence != "INFERRED" {
		t.Errorf("map edges: %s", mb)
	} // a confirmed finding is written up for the tracker and handed to a fix goal, both with its trace
	title, body, err := Issue(r, r.Lead("L1"))
	if err != nil || title != "CRITICAL: Command injection in /ping" {
		t.Fatalf("issue: %q %v", title, err)
	}
	for _, want := range []string{"`server/handler.go:15`", "Confirmed by 2 reviewers", "guard that would make it false", "## Suggested fix", r.Slug} {
		if !strings.Contains(body, want) {
			t.Errorf("the issue lacks %q:\n%s", want, body)
		}
	}
	goal, err := FixObjective(r, r.Lead("L1"))
	if err != nil || !strings.Contains(goal, "Fix finding L1") || !strings.Contains(goal, "regression test") || !strings.Contains(goal, "server/handler.go:15") {
		t.Fatalf("fix goal: %v\n%s", err, goal)
	}
	// the exports: one CSV row per lead, a page that escapes what the agents wrote
	rows, err := csv.NewReader(strings.NewReader(string(CSV(r)))).ReadAll()
	if err != nil || len(rows) != 2 || rows[0][0] != "id" || rows[1][0] != "L1" || rows[1][2] != "critical" || rows[1][8] != "server/handler.go" {
		t.Fatalf("findings.csv: %v %q", err, rows)
	}
	page := HTML(r)
	if !strings.Contains(page, "Command injection in /ping") || !strings.Contains(page, "&#34;ping -c1 &#34;+host") || strings.Contains(page, "<script") {
		t.Errorf("report.html:\n%s", page)
	}
	for _, name := range []string{CSVFile, HTMLFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Errorf("the report did not write %s: %v", name, err)
		}
	}
	r.Leads = append(r.Leads, Lead{ID: "L2", Title: "maybe"})
	if _, _, err := Issue(r, r.Lead("L2")); err == nil || !strings.Contains(err.Error(), "still being verified") {
		t.Fatalf("an open lead was written up: %v", err)
	}
}

func TestOnlyTheFilerAmendsALeadAndOnlyBeforeAVerdict(t *testing.T) {
	tl, _ := harness(t, "survey")
	mustOK(t, tl, "hunter", map[string]any{"action": "lead", "title": "XSS in render", "category": "xss",
		"claim":  "A name with markup is written into innerHTML, so a crafted name runs script in the page.",
		"trace":  []map[string]any{{"file": "web/app.js", "line": 3, "quote": `el.innerHTML = "<b>" + name + "</b>";`, "note": "name goes into innerHTML"}},
		"impact": "medium", "reach": "external", "exploit": "moderate"})
	mustFail(t, tl, "other", map[string]any{"action": "lead", "id": "L1", "impact": "high"}, "only the filer")
	out := mustOK(t, tl, "hunter", map[string]any{"action": "lead", "id": "L1", "reach": "internal"})
	if !strings.Contains(out, "L1 amended") || !strings.Contains(out, "severity low") {
		t.Fatalf("amend: %s", out)
	}
	out = mustOK(t, tl, "v", map[string]any{"action": "verdict", "id": "L1", "confirm": true, "note": "render is called with the user's display name, unescaped"})
	if !strings.Contains(out, "now confirmed") {
		t.Fatalf("survey depth needs one verifier and no proof: %s", out)
	}
	mustFail(t, tl, "hunter", map[string]any{"action": "lead", "id": "L1", "impact": "high"}, "has verdicts")
}

func TestProveRunsInACopyAndJudgesByTheMarkers(t *testing.T) {
	root, commit := gitRepo(t, appFiles)
	repo, err := gitx.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	st := NewStore(root)
	inv, err := TakeInventory(context.Background(), repo, commit, "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := st.Create(commit, "standard", "", inv)
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	// the runner a session gives runs in its sandbox; this one runs on the host, which is enough to test the copy and the markers
	run := func(ctx context.Context, dir string, argv []string, timeout time.Duration) ([]byte, int, error) {
		dirs = append(dirs, dir)
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
		cmd.Dir = dir
		cmd.WaitDelay = time.Second
		out, err := cmd.CombinedOutput()
		if ctx.Err() != nil {
			return out, -1, nil
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return out, ee.ExitCode(), nil
		}
		return out, 0, err
	}
	p := &Prover{Repo: repo, Run: run}
	ctx := context.Background()
	good := `set -e
grep -q 'ping -c1 "+host' server/handler.go && echo ` + MarkerExercised + `
echo pwned > marker.txt
if [ -f marker.txt ]; then echo ` + MarkerReproduced + `; fi
`
	pr, err := p.Prove(ctx, root, r, "L1", "prover", good, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Outcome != ProofReproduced || !pr.Exercised {
		t.Fatalf("proof: %+v", pr)
	}
	if _, err := os.Stat(dirs[0]); !os.IsNotExist(err) {
		t.Errorf("the copy %s was not removed", dirs[0])
	}
	if _, err := os.Stat(filepath.Join(root, "marker.txt")); !os.IsNotExist(err) {
		t.Error("the reproducer wrote into the project")
	}
	// the copy is gone; the report writes the reproduction into the scan's proofs directory, self-contained and without
	// agent names, so it can be shared
	r.Leads = append(r.Leads, Lead{ID: "L1", Title: "Command injection | in ping", Category: "injection", Claim: "host reaches the shell",
		Trace: []Step{{Cite: Cite{File: "server/handler.go", Line: 3}, Note: "host goes to sh -c"}}, By: "hunter-7"})
	pr.By = "prover-9"
	// a run that did not reproduce is not a proof: it stays in the ledger, not in proofs/
	r.Leads[0].Proofs = append(r.Leads[0].Proofs, Proof{By: "prover-9", Script: "true", Outcome: ProofInconclusive, Exit: 1}, pr)
	for i := range r.DepthInfo().Verifiers {
		r.Leads[0].Verdicts = append(r.Leads[0].Verdicts, Verdict{By: "verifier-" + strconv.Itoa(i), Role: RoleVerifier, Confirm: true, Note: "holds"})
	}
	r.Leads[0].Verdicts = append(r.Leads[0].Verdicts, Verdict{By: "defender-1", Role: RoleDefender, Confirm: true, Note: "no guard"})
	if err := WriteReport(root, r); err != nil {
		t.Fatal(err)
	}
	ev := filepath.Join(RunDir(root, r.Slug), filepath.FromSlash(ProofDir("L1", 1)))
	if b, err := os.ReadFile(filepath.Join(ev, ProofScriptOut)); err != nil || string(b) != good {
		t.Errorf("script not kept: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(ev, ProofOutput)); err != nil || !strings.Contains(string(b), MarkerReproduced) {
		t.Errorf("output not kept: %q %v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(ev, ProofReadme)); err != nil || !strings.Contains(string(b), r.Commit) ||
		!strings.Contains(string(b), "server/handler.go:3") || !strings.Contains(string(b), "independent") {
		t.Errorf("readme: %s %v", b, err)
	}
	var rec struct{ Lead, Commit, Outcome string }
	b, err := os.ReadFile(filepath.Join(ev, ProofRecord))
	if err != nil || json.Unmarshal(b, &rec) != nil || rec.Lead != "L1" || rec.Commit != r.Commit || rec.Outcome != ProofReproduced {
		t.Errorf("record: %s %v", b, err)
	}
	if strings.Contains(string(b), "prover-9") || strings.Contains(string(b), "hunter-7") {
		t.Errorf("the record names an agent: %s", b)
	}
	if idx, err := os.ReadFile(filepath.Join(RunDir(root, r.Slug), ProofsDir, ProofReadme)); err != nil ||
		!strings.Contains(string(idx), "(l1-1/README.md)") || !strings.Contains(string(idx), `Command injection \| in ping`) {
		t.Errorf("index: %s %v", idx, err)
	}
	if md, _ := os.ReadFile(filepath.Join(RunDir(root, r.Slug), ReportFile)); !strings.Contains(string(md), "(proofs/l1-1/README.md)") {
		t.Error("the report does not link the proof")
	}
	if es, err := os.ReadDir(filepath.Join(RunDir(root, r.Slug), ProofsDir)); err != nil || len(es) != 2 {
		t.Errorf("proofs dir holds %d entries (%v), want the index and the proof", len(es), err)
	}
	notRepro := "echo " + MarkerExercised + " >/dev/null; grep -q host server/handler.go && echo " + MarkerExercised + "\nif false; then echo " + MarkerReproduced + "; fi\n"
	if pr, err := p.Prove(ctx, root, r, "L1", "prover", notRepro, time.Minute); err != nil || pr.Outcome != ProofNotReproduce {
		t.Fatalf("exercised, not reproduced: %+v %v", pr, err)
	}
	if _, err := p.Prove(ctx, root, r, "L1", "prover", "echo "+MarkerExercised+"\necho "+MarkerReproduced+"\n", time.Minute); err == nil ||
		!strings.Contains(err.Error(), "unconditionally") {
		t.Fatalf("a script that only prints the markers: %v", err)
	}
	slow := "sleep 30\nif true; then echo " + MarkerExercised + "; echo " + MarkerReproduced + "; fi\n"
	if _, err := p.Prove(ctx, root, r, "../../x", "prover", good, time.Minute); err == nil {
		t.Fatal("a lead id that leaves the scan directory was used as a path")
	}
	if pr, err := p.Prove(ctx, root, r, "L1", "prover", slow, time.Second); err != nil || pr.Outcome != ProofInconclusive {
		t.Fatalf("a run that timed out: %+v %v", pr, err)
	}
	var none *Prover
	if _, err := none.Prove(ctx, root, r, "L1", "x", good, 0); err == nil || !strings.Contains(err.Error(), "--sandbox") {
		t.Fatalf("no sandbox: %v", err)
	}
}

func TestBriefNamesTheTeamAndTheMilestones(t *testing.T) {
	_, st := harness(t, "deep")
	r, _ := st.Snapshot("")
	b := Brief(r)
	for _, want := range []string{"Do not change the project", "team of about 8 agents", "defender", MarkerReproduced, "## Milestones", "from medium up"} {
		if !strings.Contains(b, want) {
			t.Errorf("the brief lacks %q", want)
		}
	}
	if o := Objective(r, "x/BRIEF.md"); !strings.Contains(o, "Spec: x/BRIEF.md") || !strings.Contains(o, "do not change the project") {
		t.Errorf("objective: %s", o)
	}
}

// A marker counts at the start of a line, with or without a note after it, and not inside another word or line.
func TestHasMarker(t *testing.T) {
	for out, want := range map[string]bool{
		MarkerReproduced + "\n":               true,
		"  " + MarkerReproduced + " L1\n":     true,
		"x\n" + MarkerReproduced + ": rows\n": true,
		"saw " + MarkerReproduced + "\n":      false,
		MarkerReproduced + "X\n":              false,
	} {
		if got := hasMarker(out, MarkerReproduced); got != want {
			t.Errorf("hasMarker(%q) = %v, want %v", out, got, want)
		}
	}
}

// A spreadsheet never runs a cell an agent wrote as a formula.
func TestCSVCellsAreNotFormulas(t *testing.T) {
	for in, want := range map[string]string{"=HYPERLINK(1)": "'=HYPERLINK(1)", "+1": "'+1", "-x": "'-x", "@SUM": "'@SUM", "plain": "plain", "": ""} {
		if got := csvCell(in); got != want {
			t.Errorf("csvCell(%q) = %q, want %q", in, got, want)
		}
	}
}

// Only https and ssh repositories are cloned: no local path, file://, transport helper or option.
func TestCloneTakesOnlyRemoteRepositories(t *testing.T) {
	for _, ok := range []string{"https://github.com/owner/repo", "https://gitlab.com/a/b/c.git", "ssh://git@host/owner/repo.git", "git@github.com:owner/repo.git"} {
		if err := CheckURL(ok); err != nil {
			t.Errorf("CheckURL(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "/home/me/repo", "file:///etc", "ext::sh -c id", "fd::3", "--upload-pack=x", "http://host/owner/repo", "https://host", "https://host/ a", "git@host:repo;id"} {
		if err := CheckURL(bad); err == nil {
			t.Errorf("CheckURL(%q) accepted it", bad)
		}
	}
	for in, want := range map[string]string{"https://github.com/Owner/Repo.git": "Owner-Repo", "git@github.com:a/b": "a-b", "https://host/x": "x"} {
		if got := CloneName(in); got != want {
			t.Errorf("CloneName(%q) = %q, want %q", in, got, want)
		}
	}
}

// git's progress on standard error reaches the caller one update at a time: a carriage return ends an update, a newline a step,
// and an update split across writes is joined.
func TestCloneProgressSplitsGitsUpdates(t *testing.T) {
	var got []string
	p := &progressLines{fn: func(l string) { got = append(got, l) }}
	for _, w := range []string{"Cloning into 'x'...\n", "Receiving objects:  10% (1/10)\r", "Receiving objects:  5", "0% (5/10)\r\r", "Resolving deltas: 100% (3/3), done.\n", "tail"} {
		if n, err := p.Write([]byte(w)); err != nil || n != len(w) {
			t.Fatalf("Write(%q) = %d, %v", w, n, err)
		}
	}
	want := []string{"Cloning into 'x'...", "Receiving objects:  10% (1/10)", "Receiving objects:  50% (5/10)", "Resolving deltas: 100% (3/3), done."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("lines = %q, want %q", got, want)
	}
}

// A session works on the scan its goal names: an older unfinished scan of the same project numbers its leads differently, and
// a call that names it goes to the session's scan instead, with a note.
func TestASessionStaysOnItsScan(t *testing.T) {
	root := t.TempDir()
	inv := Inventory{Files: []File{{Path: "a.go", Lang: "go", Lines: 1}}}
	old, err := NewStore(root).Create(strings.Repeat("ab", 20), "survey", "", inv)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := NewStore(root).Create(strings.Repeat("cd", 20), "survey", "", inv)
	if err != nil {
		t.Fatal(err)
	}
	st := NewStore(root)
	if _, _, err := st.Resolve(""); err == nil || !strings.Contains(err.Error(), "2 are unfinished") {
		t.Fatalf("two unfinished scans and none named: %v", err)
	}
	goal := Objective(cur, filepath.Join(root, filepath.FromSlash(BriefPath(cur.Slug))))
	slug := ObjectiveSlug(goal)
	if slug != cur.Slug {
		t.Fatalf("ObjectiveSlug(%q) = %q, want %q", goal, slug, cur.Slug)
	}
	// a scan opened without a goal (the newest one created, a person's /scan status) still lets a call name another
	if _, err := st.Open(slug); err != nil {
		t.Fatal(err)
	}
	if used, note, err := st.Resolve(old.Slug); err != nil || used != old.Slug || note != "" {
		t.Fatalf("an unbound session naming the older scan: %q %q %v", used, note, err)
	}
	if _, err := st.Bind(slug); err != nil {
		t.Fatal(err)
	}
	used, note, err := st.Resolve(old.Slug)
	if err != nil || used != cur.Slug || !strings.Contains(note, "left as it is") {
		t.Fatalf("a call naming the older scan: %q %q %v", used, note, err)
	}
	for objective, want := range map[string]string{
		"Deep scan.\n\nSpec: .sleipnir/scan/" + cur.Slug + "/BRIEF.md":           cur.Slug,
		"Deep scan.\n\nSpec: C:\\p\\.sleipnir\\scan\\" + cur.Slug + "\\BRIEF.md": cur.Slug,
		"Research.\n\nSpec: .sleipnir/research/x/BRIEF.md":                       "",
		"see .sleipnir/scan/" + cur.Slug + "/BRIEF.md":                           "",
	} {
		if got := ObjectiveSlug(objective); got != want {
			t.Errorf("ObjectiveSlug(%q) = %q, want %q", objective, got, want)
		}
	}
}

// A review covers the files a change wrote: the inventory holds only them, a lead needs a step on a line the change made, and
// a finding is anchored on the last such step.
func TestAReviewCoversWhatTheChangeWrote(t *testing.T) {
	root, base := gitRepo(t, map[string]string{
		"server/handler.go": "package server\n\nimport \"net/http\"\n\nfunc Ping(w http.ResponseWriter, r *http.Request) {\n\tw.Write([]byte(\"pong\"))\n}\n",
		"server/other.go":   "package server\n\nfunc Other() int { return 1 }\n",
	})
	changed := "package server\n\nimport (\n\t\"net/http\"\n\t\"os/exec\"\n)\n\nfunc Ping(w http.ResponseWriter, r *http.Request) {\n\thost := r.URL.Query().Get(\"host\")\n\tout, _ := exec.Command(\"sh\", \"-c\", \"ping -c1 \"+host).Output()\n\tw.Write(out)\n}\n"
	if err := os.WriteFile(filepath.Join(root, "server", "handler.go"), []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-qm", "ping host"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	repo, err := gitx.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	st := NewStore(root)
	if _, err := StartReview(context.Background(), st, repo, "standard", "HEAD"); err == nil || !strings.Contains(err.Error(), "no change") {
		t.Fatalf("a review of nothing: %v", err)
	}
	r, err := StartReview(context.Background(), st, repo, "standard", base)
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsReview() || r.Base != base || !strings.HasPrefix(r.Slug, "review-") || len(r.Inventory.Files) != 1 || r.Inventory.Files[0].Path != "server/handler.go" {
		t.Fatalf("review: %+v", r)
	}
	if len(r.Changes) != 1 || !r.Changed("server/handler.go", 10, 10) || r.Changed("server/handler.go", 1, 2) {
		t.Fatalf("changes: %+v", r.Changes)
	}
	if b := Brief(r); !strings.Contains(b, "review of a change") || !strings.Contains(b, "`server/handler.go`: ") {
		t.Errorf("brief: %s", b)
	}
	if g := Objective(r, ""); !strings.HasPrefix(g, "Review (standard) of the change") || ObjectiveSlug(g) != r.Slug {
		t.Errorf("objective: %s", g)
	}
	tl := NewTool(st, func(c string) Code { return CodeAt(repo, c) })
	lead := map[string]any{"action": "lead", "title": "Command injection in Ping", "category": "command-injection",
		"claim":  "Anyone can run shell commands on the server by sending host=x;id to the ping handler.",
		"impact": "critical", "reach": "external", "exploit": "trivial"}
	lead["trace"] = []map[string]any{{"file": "server/handler.go", "line": 1, "quote": "package server", "note": "the package, untouched by the change"}}
	mustFail(t, tl, "hunter", lead, "a line the change added or modified")
	lead["trace"] = []map[string]any{
		{"file": "server/handler.go", "line": 9, "quote": `host := r.URL.Query().Get("host")`, "note": "host from the query string"},
		{"file": "server/handler.go", "line": 10, "quote": `exec.Command("sh", "-c", "ping -c1 "+host)`, "note": "into a shell"},
	}
	mustOK(t, tl, "hunter", lead)
	r, _ = st.Snapshot("")
	f := FindingOf(r, r.Lead("L1"))
	if f.Anchor == nil || f.Anchor.Line != 10 {
		t.Fatalf("anchor: %+v", f.Anchor)
	}
	if doc := FindingsDoc(r); doc["base"] != base {
		t.Errorf("findings.json without the base: %v", doc["base"])
	}
	if !strings.Contains(StatusText(r), "Lines the change wrote") {
		t.Errorf("status: %s", StatusText(r))
	}
}

func TestParseDiff(t *testing.T) {
	out := "diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -3,0 +4,2 @@\n+x\n+y\n@@ -9 +11 @@\n-a\n+b\n@@ -20,3 +22,0 @@\n-c\n" +
		"diff --git a/gone.go b/gone.go\n--- a/gone.go\n+++ /dev/null\n@@ -1 +0,0 @@\n-x\n" +
		"diff --git \"a/sp ace.go\" \"b/sp ace.go\"\n--- \"a/sp ace.go\"\n+++ \"b/sp ace.go\"\n@@ -1 +1 @@\n-a\n+b\n" +
		// an added line "++ b/other.go" is rendered as "+++ b/other.go": it is content, and the next hunk is still b.go's
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1,0 +2 @@\n+++ b/other.go\n@@ -7 +8 @@\n-a\n+b\n" +
		"diff --git a/t ab.go b/t ab.go\n--- a/t ab.go\t\n+++ b/t ab.go\t\n@@ -1 +1 @@\n-a\n+b\n"
	got := parseDiff(out)
	if len(got) != 4 || got[0].File != "a.go" || len(got[0].Lines) != 2 || got[0].Lines[0] != [2]int{4, 5} || got[0].Lines[1] != [2]int{11, 11} ||
		got[1].File != "sp ace.go" || got[2].File != "b.go" || len(got[2].Lines) != 2 || got[2].Lines[1] != [2]int{8, 8} ||
		got[3].File != "t ab.go" {
		t.Fatalf("parseDiff: %+v", got)
	}
}

// Declarations whose closing line never comes cost a bounded amount of reading, not a read to the end of the file for each.
func TestHeuristicFunctionsAreBounded(t *testing.T) {
	src := []byte(strings.Repeat("def f\n  x\n", 100000))
	start := time.Now()
	heuristicFunctions("ruby", "a.rb", src)
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %s", d)
	}
	if got := heuristicFunctions("ruby", "b.rb", []byte("def a\n  if x\n  end\nend\ndef b\nend\n")); len(got) != 2 {
		t.Fatalf("functions: %+v", got)
	}
}

func TestNothingTrackedIsLeftOutOfTheShards(t *testing.T) {
	inv := Inventory{Files: []File{
		{Path: "app/main.go", Lang: "go", Lines: 400, Funcs: 20},
		{Path: "bin/tool", Lang: KindBinary, Kind: KindBinary, Bytes: 5 << 20},
		{Path: "bin/tool2", Lang: KindBinary, Kind: KindBinary, Bytes: 5 << 20},
		{Path: "data/huge.sql", Lang: "sql", Kind: KindLarge, Bytes: 100 << 20},
		{Path: "lib", Lang: KindSubmodule, Kind: KindSubmodule},
		{Path: "vendor/x/x.go", Lang: "go", Kind: KindVendored, Lines: 4000},
	}}
	shards := MakeShards(inv, Depth{ShardFuncs: 1000, ShardLines: 2000})
	n := 0
	for _, s := range shards {
		n += len(s.Files)
		if s.Lines > 2000 {
			t.Errorf("%s weighs %d lines", s.ID, s.Lines)
		}
		for _, f := range s.Files {
			if f == "bin/tool" && s.Lines == 0 {
				t.Errorf("a binary weighs nothing in %s", s.ID)
			}
		}
	}
	if n != len(inv.Files) {
		t.Errorf("shards hold %d of %d files", n, len(inv.Files))
	}
	for p, want := range map[string]string{"site/index.html": KindOther, "web/big.js": KindLarge, "Dockerfile": "", ".env": "", "dist/a.js": KindVendored} {
		size := 10
		if p == "web/big.js" {
			size = 2 << 20
		}
		if _, kind := classify(p, size); kind != want {
			t.Errorf("classify(%s) = %q, want %q", p, kind, want)
		}
	}
}

func TestABinaryIsQuotedByAStringItHolds(t *testing.T) {
	code := fakeCode{"bin/agent": "\x7fELF\x00\x01\x02garbage\x00build-id=opaque-test-value-42\x00\x03more\n\x00tail"}
	if _, _, err := CheckCite(context.Background(), code, Cite{File: "bin/agent", Line: 1, Quote: "build-id=opaque-test-value-42"}); err != nil {
		t.Fatalf("a string the binary holds: %v", err)
	}
	if _, _, err := CheckCite(context.Background(), code, Cite{File: "bin/agent", Line: 1, Quote: "not in the binary at all"}); err == nil ||
		!strings.Contains(err.Error(), "as strings prints it") {
		t.Fatalf("a quote that is not there: %v", err)
	}
}
