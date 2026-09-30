package reward

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/rl"
)

const goFailOutput = `--- FAIL: TestParseHeaders (0.00s)
    parse_test.go:42: got "content-length: 12", want "Content-Length: 12"
--- FAIL: TestParseEmpty/nil_input (0.00s)
FAIL
FAIL	example.com/pkg	0.012s
[exit code 1]`

// compactionEpisode builds one agent whose thread was compacted once:
//
//	A.1..A.3   folded main steps (turns 2, 4, 6)
//	A.4        retained main step (turn 8)
//	A.c1       the compactor call, keep_from t7: turns before t7 are folded
//	A.5        the first request after the rebase (segment 1)
func compactionEpisode() (*rl.Episode, map[string]string) {
	patch := `{"keep_from":"t7","spine":[{"turns":"t1-t6","line":"..."}],"notes":[],"promote":[]}`
	a := mkAgent("A", "worker",
		mkStep("A.1", withTurnID(2), withPrompt(3000, ""), withObs(
			writeObs("pkg/parse.go"),
			bashObs("go test ./pkg -run TestParseHeaders", goFailOutput),
		)),
		mkStep("A.2", withTurnID(4), withPrompt(3600, ""), withObs(
			obs("edit", map[string]any{"path": "pkg/lexer.go", "old_string": "a", "new_string": "b"}, "edited pkg/lexer.go"),
			obs("read", map[string]any{"path": "pkg/consts.go"}, `const errTooBig = "request entity exceeds the 8192 byte limit"`+"\n"+`const retries = 5`),
		)),
		mkStep("A.3", withTurnID(6), withPrompt(4200, ""), withObs(
			bashObs("cd pkg && go test ./... 2>&1 | tail -20", "ok  \texample.com/pkg\t0.011s"),
		)),
		mkStep("A.4", withTurnID(8), withPrompt(4800, ""), withObs(bashObs("git diff --stat", " pkg/parse.go | 4 ++--"))),
		mkStep("A.c1", withKind(rl.KindCompactor), withPrompt(6300, ""), withText("```json\n"+patch+"\n```")),
		mkStep("A.5", withTurnID(12), withSeg(1, 1), withPrompt(2600, "")),
	)
	prompts := map[string]string{
		"A.1": "You are a worker. Task: fix header parsing. Use recall to see old turns.",
		"A.c1": "thread: pkg/parse.go written; go test ./pkg -run TestParseHeaders; --- FAIL: TestParseHeaders; " +
			`got "content-length: 12", want "Content-Length: 12"; pkg/lexer.go edited; ` + `const errTooBig = "request entity exceeds the 8192 byte limit"`,
		"A.5": "SPINE t1-t6: wrote pkg/parse.go; ran go test ./pkg -run TestParseHeaders which failed in TestParseHeaders: " +
			`got "content-length: 12"; the limit is "request entity exceeds the 8192 byte limit". Edited pkg/lexer.go.`,
	}
	ep := mkEpisode("task/0", a)
	ep.ID = "task/0"
	return ep, prompts
}

func resolverOf(prompts map[string]string) PromptText {
	return func(_ *rl.Episode, st *rl.Step) (string, error) {
		if p, ok := prompts[st.ID]; ok {
			return p, nil
		}
		return "", fmt.Errorf("no prompt for %s", st.ID)
	}
}

func factTexts(p Probe) map[string]string {
	m := map[string]string{}
	for _, f := range p.Facts {
		m[f.Text] = f.Kind
	}
	return m
}

func TestProbesExtractFactsFromTheFoldedSteps(t *testing.T) {
	ep, prompts := compactionEpisode()
	ps := probes(ep, resolverOf(prompts), 100) // big quota: see every candidate
	if len(ps) != 1 {
		t.Fatalf("probes = %d", len(ps))
	}
	p := ps[0]
	if p.Compactor != "A.c1" || p.Next != "A.5" || p.Agent != "A" {
		t.Errorf("probe identity: %+v", p)
	}
	got := factTexts(p)
	for _, want := range []struct{ text, kind string }{
		{"pkg/parse.go", FactFile},
		{"pkg/lexer.go", FactFile},
		{"TestParseHeaders", FactTest},
		{"go test ./pkg -run TestParseHeaders", FactCommand},
		{`request entity exceeds the 8192 byte limit`, FactLiteral},
	} {
		if got[want.text] != want.kind {
			t.Errorf("fact %q (%s) missing; have %v", want.text, want.kind, got)
		}
	}
	// A.4 was kept verbatim (turn 8 >= keep_from t7): nothing from it is asked.
	for text := range got {
		if strings.Contains(text, "git diff") || text == "pkg/parse.go | 4 ++--" {
			t.Errorf("fact from a retained step: %q", text)
		}
	}
	// The failed test is a failure fact with its message too.
	foundMsg := false
	for _, f := range p.Facts {
		if f.Kind == FactFailure && strings.Contains(f.Text, "content-length") {
			foundMsg = true
		}
	}
	if !foundMsg {
		t.Errorf("failure message fact missing: %v", got)
	}
	for _, f := range p.Facts {
		if f.Question == "" || f.Step == "" {
			t.Errorf("fact lacks a question or step: %+v", f)
		}
	}
}

func TestScoreProbesFractionRecovered(t *testing.T) {
	ep, prompts := compactionEpisode()
	ps := probes(ep, resolverOf(prompts), 100)
	res, err := ScoreProbes(ep, ps, resolverOf(prompts))
	if err != nil {
		t.Fatal(err)
	}
	r := res[0]
	if !r.Scored || len(r.Found) != len(r.Facts) {
		t.Fatalf("result: %+v", r)
	}
	if r.Recall <= 0 || r.Recall >= 1 {
		t.Errorf("a partial spine should have partial recall: %v (missing %v)", r.Recall, r.Missing)
	}
	// The spine names parse.go, lexer.go, the failed test and the limit; it does
	// not mention the retries constant or TestParseEmpty.
	must := map[string]bool{"pkg/parse.go": true, "pkg/lexer.go": true, "TestParseHeaders": true}
	for i, f := range r.Facts {
		if must[f.Text] && !r.Found[i] {
			t.Errorf("fact %q should have been recovered", f.Text)
		}
	}

	// A compaction that keeps nothing scores zero; a verbatim copy scores one.
	empty := map[string]string{"A.1": prompts["A.1"], "A.c1": prompts["A.c1"], "A.5": "nothing kept"}
	res, _ = ScoreProbes(ep, ps, resolverOf(empty))
	if res[0].Recall != 0 || len(res[0].Missing) != len(res[0].Facts) {
		t.Errorf("empty spine recall = %v", res[0].Recall)
	}
	verbatim := map[string]string{"A.1": prompts["A.1"], "A.c1": prompts["A.c1"], "A.5": prompts["A.c1"] + " " + goFailOutput + " pkg/consts.go retries = 5 " + " go test ./... TestParseEmpty/nil_input"}
	res, _ = ScoreProbes(ep, ps, resolverOf(verbatim))
	near(t, "verbatim recall", res[0].Recall, 1)
}

func TestProbeMatchingIsNormalised(t *testing.T) {
	f := func(kind, text string, alts ...string) Fact { return Fact{Kind: kind, Text: text, Alts: alts} }
	tests := []struct {
		name   string
		fact   Fact
		prompt string
		want   bool
	}{
		{"exact", f(FactFile, "pkg/parse.go"), "edited pkg/parse.go today", true},
		{"case", f(FactFile, "pkg/Parse.go"), "edited PKG/PARSE.GO", true},
		{"windows separators", f(FactFile, "pkg/parse.go"), `edited pkg\parse.go`, true},
		{"quotes and backticks are dropped", f(FactLiteral, `"max retries exceeded"`), "error `max retries exceeded` seen", true},
		{"whitespace collapses", f(FactLiteral, "max   retries\texceeded"), "max retries\nexceeded", true},
		{"zero width characters", f(FactFile, "pkg/parse.go"), "pkg/pa​rse.go", true},
		{"fullwidth forms", f(FactCommand, "go test ./..."), "ｇｏ　ｔｅｓｔ　./...", true},
		{"alternative rendering", f(FactFile, "internal/deep/dir/parse.go", "parse.go"), "changed parse.go", true},
		{"absent", f(FactFile, "pkg/parse.go"), "changed other.go", false},
		{"partial word does not count", f(FactLiteral, "exceeds the limit"), "exceeds the lim", false},
		{"empty fact never matches", f(FactFile, ""), "anything", false},
		{"unicode", f(FactLiteral, "größe überschritten"), "Fehler: GRÖSSE ÜBERSCHRITTEN", true},
	}
	for _, tc := range tests {
		if got := factFound(normalizeText(tc.prompt), tc.fact); got != tc.want {
			t.Errorf("%s: factFound = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestProbeSamplingIsDeterministicButUnpredictable(t *testing.T) {
	var cands []Fact
	for i := 0; i < 40; i++ {
		cands = append(cands, Fact{Kind: FactFile, Text: fmt.Sprintf("pkg/file%d.go", i)})
		cands = append(cands, Fact{Kind: FactLiteral, Text: fmt.Sprintf("some distinctive message number %d", i)})
	}
	a := sampleFacts(cands, "task/0", "A.c1", 8)
	b := sampleFacts(cands, "task/0", "A.c1", 8)
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatal("same ids must give the same sample")
	}
	if len(a) != 8 {
		t.Fatalf("sample size = %d", len(a))
	}
	// Different episodes or steps sample different facts.
	differs := 0
	for i := 0; i < 20; i++ {
		c := sampleFacts(cands, fmt.Sprintf("task/%d", i), "A.c1", 8)
		if fmt.Sprint(c) != fmt.Sprint(a) {
			differs++
		}
	}
	if differs < 15 {
		t.Errorf("samples barely depend on the ids: %d/20 differ", differs)
	}
	if fmt.Sprint(sampleFacts(cands, "task/0", "A.c2", 8)) == fmt.Sprint(a) {
		t.Error("a different compactor step must sample differently")
	}
	// Spread over kinds.
	kinds := map[string]int{}
	for _, f := range a {
		kinds[f.Kind]++
	}
	if len(kinds) < 2 {
		t.Errorf("sample should mix kinds: %v", kinds)
	}
	// Order of candidates does not matter.
	rev := make([]Fact, len(cands))
	for i, f := range cands {
		rev[len(cands)-1-i] = f
	}
	if fmt.Sprint(sampleFacts(rev, "task/0", "A.c1", 8)) != fmt.Sprint(a) {
		t.Error("sampling must not depend on candidate order")
	}
	// Small pools and degenerate quotas.
	if len(sampleFacts(cands[:3], "e", "s", 8)) != 3 {
		t.Error("a small pool is returned whole")
	}
	if sampleFacts(nil, "e", "s", 8) != nil || sampleFacts(cands, "e", "s", 0) != nil {
		t.Error("nothing to sample")
	}
}

func TestProbesDropFactsAlreadyInTheInitialPromptAndInvisibleOnes(t *testing.T) {
	ep, prompts := compactionEpisode()
	// pkg/parse.go is named in the task statement: finding it later proves nothing.
	prompts["A.1"] += " The bug is in pkg/parse.go."
	ps := probes(ep, resolverOf(prompts), 100)
	if _, ok := factTexts(ps[0])["pkg/parse.go"]; ok {
		t.Error("a fact already in the initial prompt must not be asked")
	}
	if _, ok := factTexts(ps[0])["pkg/lexer.go"]; !ok {
		t.Error("other facts stay")
	}
	// Facts the compactor could not see in its own prompt are not asked.
	prompts["A.c1"] = "the compactor prompt mentions only pkg/lexer.go"
	ps = probes(ep, resolverOf(prompts), 100)
	got := factTexts(ps[0])
	if len(got) != 1 || got["pkg/lexer.go"] != FactFile {
		t.Errorf("only visible facts remain: %v", got)
	}
	// Without a resolver every extracted fact is kept.
	ps = Probes(ep, nil)
	if len(ps[0].Facts) < 4 {
		t.Errorf("no resolver: %v", factTexts(ps[0]))
	}
}

func TestProbeWindowHeuristicsWithoutTurnIDs(t *testing.T) {
	// Without turn ids or a parseable patch the window is all but the newest
	// quarter, which compactors keep verbatim.
	var steps []rl.Step
	for i := 0; i < 8; i++ {
		steps = append(steps, mkStep(fmt.Sprintf("A.%d", i), withPrompt(1000+i*100, ""), withObs(writeObs(fmt.Sprintf("pkg/file%d.go", i)))))
	}
	steps = append(steps, mkStep("A.c", withKind(rl.KindCompactor), withText("no json here"), withPrompt(3000, "")))
	steps = append(steps, mkStep("A.next", withSeg(1, 1), withPrompt(1500, "")))
	ep := mkEpisode("t/0", mkAgent("A", "worker", steps...))
	ps := Probes(ep, nil)
	got := factTexts(ps[0])
	if len(got) != 6 || got["pkg/file0.go"] == "" || got["pkg/file5.go"] == "" || got["pkg/file6.go"] != "" || got["pkg/file7.go"] != "" {
		t.Errorf("window: %v", got)
	}
	// Small windows keep every step.
	ep = mkEpisode("t/0", mkAgent("A", "worker",
		mkStep("A.0", withObs(writeObs("pkg/a.go"))), mkStep("A.1", withObs(writeObs("pkg/b.go"))),
		mkStep("A.c", withKind(rl.KindCompactor), withText("x")), mkStep("A.n", withSeg(1, 1))))
	if got := factTexts(Probes(ep, nil)[0]); len(got) != 2 {
		t.Errorf("small window: %v", got)
	}
}

func TestProbesWithoutANextStepAreUnscored(t *testing.T) {
	ep, prompts := compactionEpisode()
	ep.Agents[0].Steps = ep.Agents[0].Steps[:5] // the run ended before the commit took effect
	ps := Probes(ep, resolverOf(prompts))
	if len(ps) != 1 || ps[0].Next != "" {
		t.Fatalf("probe: %+v", ps)
	}
	res, err := ScoreProbes(ep, ps, resolverOf(prompts))
	if err != nil || res[0].Scored || res[0].Recall != 1 {
		t.Errorf("unscored probe: %+v %v", res, err)
	}
}

func TestNextRebaseUsesEdgesThenSegmentsThenShrinkage(t *testing.T) {
	// 1. An explicit compact edge wins.
	ep, _ := compactionEpisode()
	ep.Edges = []rl.Edge{{Kind: rl.EdgeCompact, From: "A.c1", To: "A.5"}}
	if got := nextRebase(ep, 0, 4); got != 5 {
		t.Errorf("edge: %d", got)
	}
	// 2. Segment numbers.
	ep, _ = compactionEpisode()
	if got := nextRebase(ep, 0, 4); got != 5 {
		t.Errorf("segment: %d", got)
	}
	// 2b. A compactor step tagged with the *new* segment still finds the right step.
	ep.Agents[0].Steps[4].Segment = 1
	if got := nextRebase(ep, 0, 4); got != 5 {
		t.Errorf("compactor tagged with the new segment: %d", got)
	}
	// 3. No segment info: the first main step whose prompt shrank.
	ep, _ = compactionEpisode()
	ep.Agents[0].Steps[5].Segment, ep.Agents[0].Steps[5].Epoch = 0, 0
	if got := nextRebase(ep, 0, 4); got != 5 {
		t.Errorf("shrink: %d", got)
	}
	// 4. Nothing.
	ep, _ = compactionEpisode()
	ep.Agents[0].Steps = ep.Agents[0].Steps[:5]
	if got := nextRebase(ep, 0, 4); got != -1 {
		t.Errorf("none: %d", got)
	}
	// A commit held across several main steps skips the steps of the old segment.
	ep, _ = compactionEpisode()
	held := mkStep("A.4b", withTurnID(10), withPrompt(5200, ""))
	steps := ep.Agents[0].Steps
	ep.Agents[0].Steps = append(append(append([]rl.Step{}, steps[:5]...), held), steps[5:]...)
	if got := nextRebase(ep, 0, 4); got != 6 {
		t.Errorf("held commit: %d", got)
	}
}

func TestParsePatch(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		want   int64
		reason string
	}{
		{"plain", `{"keep_from":"t30","spine":[]}`, 30, ""},
		{"fenced with prose", "Here you go:\n```json\n{\"keep_from\":\"t6\"}\n```\nThanks", 6, ""},
		{"bare number string", `{"keep_from":"41"}`, 41, ""},
		{"numeric", `{"keep_from":12}`, 12, ""},
		{"upper case", `{"keep_from":"T9"}`, 9, ""},
		{"braces inside strings", `{"spine":[{"line":"a } b { c"}],"keep_from":"t3"}`, 3, ""},
		{"no object", `I could not do it`, 0, "no JSON object"},
		{"missing keep_from", `{"spine":[]}`, 0, "no keep_from"},
		{"null keep_from", `{"keep_from":null}`, 0, "no keep_from"},
		{"bad keep_from", `{"keep_from":"soon"}`, 0, "not a turn reference"},
		{"wrong type", `{"keep_from":[1]}`, 0, "neither"},
		{"unterminated", `{"keep_from":"t3"`, 0, "no JSON object"},
		{"invalid json", `{keep_from: t3}`, 0, "not valid JSON"},
		{"empty", ``, 0, "no JSON object"},
	}
	for _, tc := range tests {
		got, reason := parsePatch(tc.in)
		if got != tc.want || (tc.reason == "") != (reason == "") || !strings.Contains(reason, tc.reason) {
			t.Errorf("%s: parsePatch = %d,%q want %d,%q", tc.name, got, reason, tc.want, tc.reason)
		}
	}
}

func TestScoreProbesErrors(t *testing.T) {
	ep, prompts := compactionEpisode()
	ps := Probes(ep, nil)
	if _, err := ScoreProbes(ep, ps, nil); err == nil {
		t.Error("nil resolver must be an error")
	}
	if _, err := ScoreProbes(nil, ps, resolverOf(prompts)); err == nil {
		t.Error("nil episode must be an error")
	}
	boom := func(*rl.Episode, *rl.Step) (string, error) { return "", errors.New("blob missing") }
	if _, err := ScoreProbes(ep, ps, boom); err == nil || !strings.Contains(err.Error(), "A.5") || !strings.Contains(err.Error(), "blob missing") {
		t.Errorf("resolver failure must be reported with the step: %v", err)
	}
	// Probes for a step index that no longer exists are tolerated.
	bad := []Probe{{AgentIndex: 9, Next: "x", NextIndex: 3}}
	if res, err := ScoreProbes(ep, bad, resolverOf(prompts)); err != nil || res[0].Scored {
		t.Errorf("stale probe: %+v %v", res, err)
	}
	if got := Probes(nil, nil); got != nil {
		t.Errorf("nil episode: %v", got)
	}
	if got := Probes(&rl.Episode{}, nil); len(got) != 0 {
		t.Errorf("empty episode: %v", got)
	}
}

func TestInlinePromptText(t *testing.T) {
	st := mkStep("A.1", withInline("hello inline world"))
	text, err := InlinePromptText(nil, &st)
	if err != nil || !strings.Contains(text, "hello inline world") {
		t.Errorf("inline: %q %v", text, err)
	}
	blank := mkStep("A.2")
	if _, err := InlinePromptText(nil, &blank); !errors.Is(err, ErrNoPromptText) {
		t.Errorf("no inline prompt: %v", err)
	}
	// Tool calls and tool results in the prompt are searchable.
	p := &core.Prompt{
		System: []core.Block{core.Text("system pins")},
		Messages: []core.Message{
			{Role: core.RoleAssistant, Blocks: []core.Block{core.ToolUse("c1", "edit", jsonRaw(map[string]any{"path": "pkg/x.go"}))}},
			{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("c1", false, core.Text("edited pkg/x.go OK"))}},
		},
	}
	st2 := rl.Step{Inline: p}
	text, _ = InlinePromptText(nil, &st2)
	for _, want := range []string{"system pins", "pkg/x.go", "edited pkg/x.go OK", "edit"} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt text lacks %q: %q", want, text)
		}
	}
}

func TestExtractFactsAdversarialOutputs(t *testing.T) {
	// Huge, repetitive, binary-ish and unicode outputs must neither panic nor
	// explode the fact list.
	big := strings.Repeat("line of noise that repeats\n", 20000)
	weird := "\x00\x01\x02 \xff\xfe bad utf8 ‮ reversed ​ zero width \"a quoted value with spaces\""
	unicode := "错误: 无法解析 \"配置文件 config.yaml 不存在\"\nFAILED tests/test_ü.py::test_ünï - AssertionError: nöpe"
	a := mkAgent("A", "worker",
		mkStep("A.1", withObs(bashObs("make all", big), bashObs("cat blob", weird), bashObs("pytest", unicode), writeObs("dir/with space/ü.go"))),
		mkStep("A.2", withObs(bashObs("ls", "a b c"))),
		mkStep("A.3"), mkStep("A.4"),
		mkStep("A.c", withKind(rl.KindCompactor), withText("{}")), mkStep("A.n", withSeg(1, 1)))
	ep := mkEpisode("t/0", a)
	start := time.Now()
	ps := probes(ep, nil, 100)
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("extraction took %v", d)
	}
	facts := factTexts(ps[0])
	if _, ok := facts["dir/with space/ü.go"]; !ok {
		t.Errorf("unicode path fact missing: %v", facts)
	}
	if len(facts) > 60 {
		t.Errorf("fact explosion: %d", len(facts))
	}
	for text := range facts {
		if strings.Contains(text, "line of noise that repeats") {
			t.Errorf("boilerplate repeated in many lines is not a distinctive fact: %q", text)
		}
	}
	if facts["test_ünï"] != FactTest {
		t.Errorf("python failing test missing: %v", facts)
	}
}

func TestCommandCore(t *testing.T) {
	tests := []struct{ in, want string }{
		{"go test ./pkg -run TestX", "go test ./pkg -run TestX"},
		{"cd pkg && go test ./... 2>&1 | tail -20", "go test ./..."},
		{"echo start; pytest tests/ -x", "pytest tests/ -x"},
		{"  ls   -la  ", "ls -la"},
		{"a && b", "a"},
		{`echo "a && b" && make test`, "make test"},
		{strings.Repeat("x", 500), strings.Repeat("x", 120)},
	}
	for _, tc := range tests {
		if got := commandCore(tc.in); got != tc.want {
			t.Errorf("commandCore(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
