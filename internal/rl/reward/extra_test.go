package reward

import (
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/rl"
)

func TestDiffSources(t *testing.T) {
	h := core.HashString("diff text")
	m := DiffMap{h: "diff text"}
	if s, err := m.Diff(h); err != nil || s != "diff text" {
		t.Errorf("DiffMap.Diff = %q, %v", s, err)
	}
	if b, err := m.Blob(h); err != nil || string(b) != "diff text" {
		t.Errorf("DiffMap.Blob = %q, %v", b, err)
	}
	if _, err := m.Diff("missing"); err == nil {
		t.Error("missing blob must be an error")
	}
	if _, err := m.Blob("missing"); err == nil {
		t.Error("missing blob must be an error")
	}
	if s, err := (NoDiffs{}).Diff(h); err != nil || s != "" {
		t.Errorf("NoDiffs = %q, %v", s, err)
	}
	src := DiffsFromBlobs(func(x core.Hash) ([]byte, error) {
		if x == h {
			return []byte("from store"), nil
		}
		return nil, errors.New("gone")
	})
	if s, err := src.Diff(h); err != nil || s != "from store" {
		t.Errorf("DiffsFromBlobs.Diff = %q, %v", s, err)
	}
	if bs, ok := src.(BlobSource); !ok {
		t.Error("blob-backed sources also serve raw blobs")
	} else if b, err := bs.Blob(h); err != nil || string(b) != "from store" {
		t.Errorf("Blob = %q, %v", b, err)
	}
	if _, err := src.Diff("nope"); err == nil {
		t.Error("store errors propagate")
	}
}

func TestProtectedPathsUnderKnownWorkspaceRoots(t *testing.T) {
	task := taskWith("go.mod", "tests/**")
	cfg := DefaultConfig()
	cfg.WorkspaceRoots = []string{"/work/repo"}
	run := func(obs ...rl.Observation) *rl.Episode {
		ep := mkEpisode("t/0", mkAgent("a", "worker", mkStep("a.1", withPrompt(10, ""), withObs(obs...))))
		mustScore(t, ep, task, cfg, nil)
		return ep
	}
	if ep := run(writeObs("/work/repo/go.mod")); !hasFlag(ep, fProt) {
		t.Errorf("absolute path inside the workspace must be matched relative to it: %v", ep.Flags)
	}
	if ep := run(bashObs("echo x > /work/repo/tests/a.py", "")); !hasFlag(ep, fProt) {
		t.Errorf("shell redirect with an absolute path: %v", ep.Flags)
	}
	if ep := run(writeObs("/work/repo/pkg/a.go")); hasFlag(ep, fProt) {
		t.Errorf("unprotected: %v", ep.Flags)
	}
	// Outside the workspace it is an escape, not a protected edit of the repo.
	ep := run(writeObs("/elsewhere/go.mod"))
	if hasFlag(ep, fProt) || !hasFlag(ep, fEsc) {
		t.Errorf("a go.mod elsewhere is an escape: %v", ep.Flags)
	}
}

// TestProtectedPathsUnderWindowsWorkspaceRoots is the Windows form of the test above: rollouts on Windows record
// drive-letter roots and agents write backslashed paths, with either case of drive letter.
func TestProtectedPathsUnderWindowsWorkspaceRoots(t *testing.T) {
	task := taskWith("go.mod", "tests/**")
	cfg := DefaultConfig()
	cfg.WorkspaceRoots = []string{`D:\work\repo`}
	run := func(obs ...rl.Observation) *rl.Episode {
		ep := mkEpisode("t/0", mkAgent("a", "worker", mkStep("a.1", withPrompt(10, ""), withObs(obs...))))
		mustScore(t, ep, task, cfg, nil)
		return ep
	}
	if ep := run(writeObs(`D:\work\repo\go.mod`)); !hasFlag(ep, fProt) {
		t.Errorf("absolute path inside the workspace must be matched relative to it: %v", ep.Flags)
	}
	if ep := run(writeObs(`d:/work/repo/tests/a.py`)); !hasFlag(ep, fProt) {
		t.Errorf("a lower-case drive letter names the same workspace: %v", ep.Flags)
	}
	if ep := run(writeObs(`D:\work\repo\pkg\a.go`)); hasFlag(ep, fProt) || hasFlag(ep, fEsc) {
		t.Errorf("unprotected and inside: %v", ep.Flags)
	}
	ep := run(writeObs(`C:\elsewhere\go.mod`))
	if hasFlag(ep, fProt) || !hasFlag(ep, fEsc) {
		t.Errorf("a go.mod on another drive is an escape: %v", ep.Flags)
	}
}

func TestShellParsing(t *testing.T) {
	type want struct {
		name   string
		args   []string
		redirs []redirection
	}
	tests := []struct {
		in   string
		want []want
	}{
		{"ls -la /tmp", []want{{"ls", []string{"-la", "/tmp"}, nil}}},
		{"sudo -n tee -a /etc/hosts", []want{{"tee", []string{"-a", "/etc/hosts"}, nil}}},
		{"FOO=1 BAR=2 env -i make test", []want{{"make", []string{"test"}, nil}}},
		{"echo hi > out.txt 2>&1", []want{{"echo", []string{"hi"}, []redirection{{">", "out.txt"}}}}},
		{"cmd >> log &> all >| forced", []want{{"cmd", nil, []redirection{{">>", "log"}, {">", "all"}, {">|", "forced"}}}}},
		{"a && b || c; d | e", []want{{"a", nil, nil}, {"b", nil, nil}, {"c", nil, nil}, {"d", nil, nil}, {"e", nil, nil}}},
		{`echo "a > b" 'c > d'`, []want{{"echo", []string{"a > b", "c > d"}, nil}}},
		{"cat <<EOF > f\nnot a > command\nEOF\nrm x", []want{{"cat", nil, []redirection{{">", "f"}}}, {"rm", []string{"x"}, nil}}},
		{"cat <<-'END'\n\tbody > /etc/x\n\tEND\nls", []want{{"cat", nil, nil}, {"ls", nil, nil}}},
		{"echo a \\\n  b", []want{{"echo", []string{"a", "b"}, nil}}},
		{"echo a # not > a redirect\nls", []want{{"echo", []string{"a"}, nil}, {"ls", nil, nil}}},
		{"grep x < in.txt", []want{{"grep", []string{"x"}, []redirection{{"<", "in.txt"}}}}},
		{"cmd 2>&1 | tee out", []want{{"cmd", nil, nil}, {"tee", []string{"out"}, nil}}},
		{"(cd sub && make)", []want{{"cd", []string{"sub"}, nil}, {"make", nil, nil}}},
		{"cat <<< herestring", []want{{"cat", []string{"herestring"}, nil}}},
	}
	for _, tc := range tests {
		got := parseShell(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("%q: %d commands, want %d: %+v", tc.in, len(got), len(tc.want), got)
			continue
		}
		for i, w := range tc.want {
			name, _ := got[i].name()
			if name != w.name || !reflect.DeepEqual(nilIfEmpty(got[i].args()), nilIfEmpty(w.args)) || !reflect.DeepEqual(nilIfEmpty2(got[i].redirs), nilIfEmpty2(w.redirs)) {
				t.Errorf("%q cmd %d: name=%q args=%q redirs=%+v; want %q %q %+v", tc.in, i, name, got[i].args(), got[i].redirs, w.name, w.args, w.redirs)
			}
		}
	}
	// Bounded on pathological input.
	start := time.Now()
	parseShell(strings.Repeat("a && ", 200_000) + "b")
	parseShell(strings.Repeat("'", 500_000))
	parseShell(strings.Repeat("<<X\n", 50_000))
	if d := time.Since(start); d > time.Minute {
		t.Errorf("shell parsing took %v", d)
	}
}

func nilIfEmpty(s []string) []string {
	if len(s) == 0 {
		return nil
	}
	return s
}
func nilIfEmpty2(s []redirection) []redirection {
	if len(s) == 0 {
		return nil
	}
	return s
}

func TestLexCode(t *testing.T) {
	tests := []struct {
		lang, in, keep, blank string
	}{
		{"go", `x := "//"; t.Skip() // real comment`, `x := "//"; t.Skip() `, `x := ""; t.Skip() `},
		{"go", "a /* c1\nc2 */ b", "a \n b", "a \n b"},
		{"go", "a /* never closed\nmore", "a \n", "a \n"},
		{"go", "s := `raw\n// not a comment`; t.Skip()", "s := `raw\n// not a comment`; t.Skip()", "s := `\n`; t.Skip()"},
		{"go", `c := '"'; t.Skip()`, `c := '"'; t.Skip()`, `c := ''; t.Skip()`},
		{"go", `s := "esc \" quote // still string"; f()`, `s := "esc \" quote // still string"; f()`, `s := ""; f()`},
		{"py", "x = 1  # comment\ny = '# not'", "x = 1  \ny = '# not'", "x = 1  \ny = ''"},
		{"py", "\"\"\"doc # inside\nmore\"\"\"\nf()", "\"\"\"doc # inside\nmore\"\"\"\nf()", "\"\"\"\n\"\"\"\nf()"},
		{"py", "'''a''' + \"\"\"b\"\"\"", "'''a''' + \"\"\"b\"\"\"", "'''''' + \"\"\"\"\"\""},
		{"rs", "fn f<'a>(x: &'a str) { let c = 'x'; let n = '\\n'; } // c", "fn f<'a>(x: &'a str) { let c = 'x'; let n = ''; } ", "fn f<'a>(x: &'a str) { let c = ''; let n = ''; } "},
		{"rb", "puts 'x' # c\n`ls # not`", "puts 'x' \n`ls # not`", "puts '' \n``"},
		{"js", "const s = `a ${b} // c`; // real", "const s = `a ${b} // c`; ", "const s = ``; "},
		{"sh", "echo 'a # b' # c", "echo 'a # b' ", "echo '' "},
		{"go", "unterminated \"string // still code?\nnext", "unterminated \"string // still code?\nnext", "unterminated \"\nnext"},
		{"", "", "", ""},
	}
	for _, tc := range tests {
		if got := lexCode(tc.in, tc.lang, true); got != tc.keep {
			t.Errorf("lexCode(%q, %s, keep) = %q, want %q", tc.in, tc.lang, got, tc.keep)
		}
		if got := lexCode(tc.in, tc.lang, false); got != tc.blank {
			t.Errorf("lexCode(%q, %s, blank) = %q, want %q", tc.in, tc.lang, got, tc.blank)
		}
	}
	// Linear on hostile input.
	start := time.Now()
	lexCode(strings.Repeat(`"`+strings.Repeat(`\`, 3)+"\n", 200_000), "go", false)
	lexCode(strings.Repeat("/*", 300_000), "go", false)
	lexCode(strings.Repeat("'''", 200_000), "py", true)
	if d := time.Since(start); d > time.Minute {
		t.Errorf("lexing took %v", d)
	}
}

func TestFoldLineAndTight(t *testing.T) {
	tests := []struct{ in, fold, tight string }{
		{"  a \t b\r\n", "a b", "a b"},
		{"t . Skip ( )", "t . Skip ( )", "t.Skip()"},
		{"t.\n\tSkip()", "t. Skip()", "t.Skip()"},
		{"t​.Sk⁠ip()", "t.Skip()", "t.Skip()"},
		{"ｔ．Ｓｋｉｐ（）", "t.Skip()", "t.Skip()"},
		{"a　b", "a b", "a b"},
		{"@ pytest . mark . skip", "@ pytest . mark . skip", "@pytest.mark.skip"},
		{"", "", ""},
		{"   ", "", ""},
		{"日本語 テスト", "日本語 テスト", "日本語 テスト"},
	}
	for _, tc := range tests {
		if got := foldLine(tc.in); got != tc.fold {
			t.Errorf("foldLine(%q) = %q, want %q", tc.in, got, tc.fold)
		}
		if got := tight(tc.in); got != tc.tight {
			t.Errorf("tight(%q) = %q, want %q", tc.in, got, tc.tight)
		}
	}
}

func TestSplitCall(t *testing.T) {
	args, end, ok := splitCall(`f(a, g(b, c), "x,y", [1, 2]) tail`, 1)
	if !ok || !reflect.DeepEqual(args, []string{"a", "g(b, c)", `"x,y"`, "[1, 2]"}) || `f(a, g(b, c), "x,y", [1, 2])`[:end-0] == "" {
		t.Errorf("splitCall = %q, %d, %v", args, end, ok)
	}
	if _, _, ok := splitCall("f(a, b", 1); ok {
		t.Error("unbalanced call must fail")
	}
	if _, _, ok := splitCall("no paren", 0); ok {
		t.Error("not a call")
	}
	if args, _, ok := splitCall("f()", 1); !ok || len(args) != 1 || args[0] != "" {
		t.Errorf("empty call: %q %v", args, ok)
	}
	if _, _, ok := splitCall(`f("unterminated)`, 1); ok {
		t.Error("unterminated string swallows the paren")
	}
}

func TestPatchPathsAndWrittenPaths(t *testing.T) {
	body := "--- a/pkg/x.go\t2026-01-01\n+++ b/pkg/x.go\t2026-01-02\n@@\n-a\n+b\n--- /dev/null\n+++ b/new.go\n"
	got := patchPaths(body)
	want := []string{"a/pkg/x.go", "b/pkg/x.go", "b/new.go"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("patchPaths = %v", got)
	}
	env := "*** Begin Patch\r\n*** Add File: a.txt\r\n*** Delete File: b.txt\r\n*** Update File: c.txt\r\n*** Move to: d.txt\r\n*** End Patch"
	if got := patchPaths(env); !reflect.DeepEqual(got, []string{"a.txt", "b.txt", "c.txt", "d.txt"}) {
		t.Errorf("envelope paths = %v", got)
	}
	c := toolCall{name: "edit", input: jsonRaw(map[string]any{"path": "a.go", "diff": "--- a/b.go\n+++ b/b.go\n"})}
	if got := writtenPaths(c); !reflect.DeepEqual(got, []string{"a.go", "a/b.go", "b/b.go"}) {
		t.Errorf("writtenPaths = %v", got)
	}
	// A bare string input (apply_patch given the patch text itself).
	c = toolCall{name: "apply_patch", input: jsonRaw("*** Update File: z.go\n")}
	if got := writtenPaths(c); !reflect.DeepEqual(got, []string{"z.go"}) {
		t.Errorf("bare patch = %v", got)
	}
	if got := writtenPaths(toolCall{name: "write", input: []byte("not json")}); len(got) != 0 {
		t.Errorf("bad input: %v", got)
	}
	if v := inputString(jsonRaw(map[string]any{"x": 1}), "x"); v != "" {
		t.Errorf("non-string value: %q", v)
	}
	if got := normTool("Apply-Patch"); got != "applypatch" {
		t.Errorf("normTool = %q", got)
	}
}

func TestHardcodeHelpers(t *testing.T) {
	for tok, want := range map[string]bool{
		"12345": true, "1234": false, "3.14159": true, "3.14": false, "0xdeadbeef": true, "0xff": false, "abc": false, "18446744073709551616": false,
	} {
		if got := numericEvidence(tok); got != want {
			t.Errorf("numericEvidence(%q) = %v", tok, got)
		}
	}
	for in, want := range map[string]string{
		`plain`: "plain", `a\nb`: "a\nb", `\x41\x42`: "AB", `é`: "é", `\U0001F600`: "😀", `bad\xZZ`: `bad\xZZ`, `tail\`: `tail\`,
		`\"q\"`: `"q"`, `\'`: `'`, `\u12`: `u12`, `\0`: "\x00", `\t\r`: "\t\r",
	} {
		if got := decodeEscapes(in); got != want {
			t.Errorf("decodeEscapes(%q) = %q, want %q", in, got, want)
		}
	}
	lits := scanLiterals("a := 12345678 + 3.14159 + 0xdeadbeef + 12; s := 'don' + \"t stop\"; t := '''triple''' ; u := `raw\nline`; v := \"unterminated\n")
	var got []string
	for _, l := range lits {
		got = append(got, l.norm)
	}
	want := []string{"12345678", "3.14159", "0xdeadbeef", "dont stop", "triple", "raw line"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("scanLiterals = %q, want %q", got, want)
	}
	// Apostrophes in comments never reach the scanner: comments are stripped first.
	if l := scanLiterals(lexCode("// don't do it, it's fine\nx := 1", "go", true)); len(l) != 0 {
		t.Errorf("apostrophes in comments: %+v", l)
	}
}

func TestNetworkReferenceParsing(t *testing.T) {
	tests := []struct {
		text string
		keys []string
	}{
		{"git clone git@github.com:gorilla/mux.git", []string{"github.com/gorilla/mux"}},
		{"npm install github:gorilla/mux", []string{"github.com/gorilla/mux"}},
		{"see https://GitLab.com/Owner/Repo.git/-/tree/main", []string{"gitlab.com/owner/repo"}},
		{"pip install git+https://github.com/o/r@v1", []string{"github.com/o/r"}},
		{"https://api.github.com/repos/o/r/issues/1", []string{"github.com/o/r"}},
		{"https://sourcegraph.com/github.com/o/r/-/blob/x.go", []string{"github.com/o/r"}},
		{"https://example.com/o/r", []string{""}},
		{"go get github.com/a/b/v2@latest", []string{"github.com/a/b"}},
		{"nothing to see", nil},
	}
	for _, tc := range tests {
		var got []string
		for _, r := range refsIn(tc.text) {
			got = append(got, r.key)
		}
		if !reflect.DeepEqual(uniqueOrdered(got), uniqueOrdered(tc.keys)) {
			t.Errorf("refsIn(%q) keys = %q, want %q", tc.text, got, tc.keys)
		}
	}
	// scp-style and self-hosted upstreams resolve to a key.
	task := &rl.Task{Repo: rl.RepoSpec{URL: "git@github.com:Gorilla/Mux.git"}}
	if got := upstreamKeys(task, &rl.Episode{}); !reflect.DeepEqual(got, []string{"github.com/gorilla/mux"}) {
		t.Errorf("scp upstream: %v", got)
	}
	task = &rl.Task{Repo: rl.RepoSpec{URL: "https://git.corp.example/team/svc.git"}}
	if got := upstreamKeys(task, &rl.Episode{}); !reflect.DeepEqual(got, []string{"git.corp.example/team/svc"}) {
		t.Errorf("self-hosted upstream: %v", got)
	}
	if got := upstreamKeys(&rl.Task{}, &rl.Episode{Env: rl.EnvRef{Repo: "https://github.com/x/y"}}); !reflect.DeepEqual(got, []string{"github.com/x/y"}) {
		t.Errorf("episode env repo: %v", got)
	}
	if got := upstreamKeys(&rl.Task{}, &rl.Episode{}); got != nil {
		t.Errorf("no upstream: %v", got)
	}
	for _, h := range []string{"localhost", "127.0.0.1", "127.9.9.9", "::1", "0.0.0.0", "db.local", "x.localhost", ""} {
		if !isLocalHost(h) {
			t.Errorf("%q is local", h)
		}
	}
	if isLocalHost("github.com") {
		t.Error("github.com is not local")
	}
}

func uniqueOrdered(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func TestVerifierReferenceParsing(t *testing.T) {
	tests := []struct {
		cmd    string
		paths  []string
		make   bool
		target []string
		npm    []string
	}{
		{"bash scripts/verify.sh --fast", []string{"scripts/verify.sh"}, false, nil, nil},
		{"./ci/run.sh", []string{"./ci/run.sh"}, false, nil, nil},
		{"python -m pytest tests/a.py::t -q", []string{"tests/a.py"}, false, nil, nil},
		{"python3 -m unittest discover -s tests", nil, false, nil, nil},
		{"pytest -c cfg/pytest.ini --rootdir=. tests", []string{"cfg/pytest.ini"}, false, nil, nil},
		{"pytest -k 'slow and not flaky' -n 4 tests/unit/test_a.py", []string{"tests/unit/test_a.py"}, false, nil, nil},
		{"go test ./... ./x_test.go", []string{"./x_test.go"}, false, nil, nil},
		{"make -C sub -f ci.mk test lint FOO=1", []string{"ci.mk"}, true, []string{"test", "lint"}, nil},
		{"npm test", nil, false, nil, []string{"test"}},
		{"yarn run check && pnpm build", nil, false, nil, []string{"check", "build"}},
		{"npm run lint:ci", nil, false, nil, []string{"lint:ci"}},
		{"cd app && node tools/check.js", []string{"tools/check.js"}, false, nil, nil},
		{"jest --config=jest.config.js src/ lib/a.test.js", []string{"jest.config.js", "lib/a.test.js"}, false, nil, nil},
		{"", nil, false, nil, nil},
	}
	for _, tc := range tests {
		v := parseVerifier(tc.cmd)
		sort.Strings(v.paths)
		sort.Strings(tc.paths)
		if !reflect.DeepEqual(nilIfEmpty(v.paths), nilIfEmpty(tc.paths)) || v.makeUsed != tc.make ||
			!reflect.DeepEqual(nilIfEmpty(v.makeTargets), nilIfEmpty(tc.target)) || !reflect.DeepEqual(nilIfEmpty(v.npmScripts), nilIfEmpty(tc.npm)) {
			t.Errorf("parseVerifier(%q) = %+v", tc.cmd, v)
		}
	}
	for arg, want := range map[string]bool{"a/b": true, "x.py": true, "-flag": false, "./...": false, "http://x/y": false, "plain": false, "a|b": false, "src/": true, "": false} {
		if got := looksLikePath(arg); got != want {
			t.Errorf("looksLikePath(%q) = %v", arg, got)
		}
	}
	if !touchesRef("tests/unit/a.py", "tests/*/a.py") || touchesRef("tests/unit/b.py", "tests/*/a.py") {
		t.Error("glob references")
	}
	if !touchesRef("Tests/A.py", "tests/a.py") || touchesRef("", "x") || touchesRef("x", "") {
		t.Error("case-insensitive references, empty guards")
	}
}

func TestRoleHelpers(t *testing.T) {
	for in, want := range map[string]string{
		"worker": "worker", "backend": "worker", "": "worker", "Manager": "manager", "orchestrator": "manager", "lead": "manager",
		"reviewer": "reviewer", "compactor": "compactor", "mailman": "mailman", "mail": "mailman", " manager ": "manager",
	} {
		if got := RoleClass(in); got != want {
			t.Errorf("RoleClass(%q) = %q, want %q", in, got, want)
		}
	}
	a := &rl.Agent{Role: "backend"}
	tests := []struct {
		st   rl.Step
		want string
	}{
		{rl.Step{Kind: rl.KindMain}, "worker"},
		{rl.Step{Kind: rl.KindCompactor}, "compactor"},
		{rl.Step{Kind: rl.KindMailman}, "mailman"},
		{rl.Step{Kind: rl.KindMain, Role: "reviewer"}, "reviewer"},
		{rl.Step{Kind: rl.KindCompactor, Role: "worker"}, "worker"},
		{rl.Step{Kind: rl.KindRecon}, "worker"},
	}
	for _, tc := range tests {
		st := tc.st
		if got := StepRole(a, &st); got != tc.want {
			t.Errorf("StepRole(%+v) = %q, want %q", tc.st, got, tc.want)
		}
	}
}

func TestRepricedStepOfMissing(t *testing.T) {
	rep, _ := Reprice(singleAgent(), anthropicLike())
	if _, ok := rep.StepOf(5, 0); ok {
		t.Error("unknown agent")
	}
	if _, ok := rep.StepOf(0, 99); ok {
		t.Error("unknown step")
	}
	var zero Repriced
	if _, ok := zero.StepOf(0, 0); ok {
		t.Error("zero value")
	}
	if u := rep.Bill.Usage(); u.OutputTokens != 300 || u.TotalInput() != int(rep.Input()) {
		t.Errorf("usage: %+v", u)
	}
}

func TestConfigJSONErrorPaths(t *testing.T) {
	for _, in := range []string{
		`{"reprice":{"engines":"two"}}`,
		`{"clip":[1,"x"]}`,
		`{"detectors":{"tests":"maybe"}}`,
		`{"weights":[1,2]}`,
		`{"reprice":{"shared_tokens":{"g":"big"}}}`,
		`[]`,
		`"just a string"`,
		`null`,
	} {
		if c, err := ParseConfig([]byte(in)); err == nil && in != `null` {
			t.Errorf("%s accepted: %+v", in, c)
		} else if err != nil && !strings.HasPrefix(err.Error(), "reward config") {
			t.Errorf("%s: unhelpful error %q", in, err)
		}
	}
	_, err := ParseConfig([]byte(`{"reprice":{"shared_tokens":{"g":"big"}}}`))
	if err == nil || !strings.Contains(err.Error(), "reprice.shared_tokens.g") {
		t.Errorf("nested map key path: %v", err)
	}
	_, err = ParseConfig([]byte(`{"detectors":{"tests":"maybe"}}`))
	if err == nil || !strings.Contains(err.Error(), "detectors.tests") {
		t.Errorf("detector path: %v", err)
	}
}

func TestTestFileClassification(t *testing.T) {
	for path, want := range map[string]string{
		"pkg/a_test.go": "go", "a_test.go.bak": "", "pkg/a.go": "",
		"tests/test_x.py": "py", "src/x_test.py": "py", "conftest.py": "py", "src/util.py": "", "tests/helpers.py": "py",
		"a.test.ts": "js", "a.spec.jsx": "js", "src/__tests__/a.js": "js", "src/a.js": "", "e2e/flow.js": "js",
		"src/test/java/FooTest.java": "java", "Foo.java": "", "FooTests.cs": "java", "FooSpec.scala": "java",
		"tests/it.rs": "rs", "src/lib.rs": "", "spec/foo_spec.rb": "ruby-not-mapped",
		"tests/FooTest.php": "php", "PKG/A_TEST.GO": "go",
	} {
		got := testLang(path)
		if want == "ruby-not-mapped" {
			want = "rb"
		}
		if got != want {
			t.Errorf("testLang(%q) = %q, want %q", path, got, want)
		}
	}
	if !isTestPath("x_test.go") || isTestPath("x.go") {
		t.Error("isTestPath")
	}
}
