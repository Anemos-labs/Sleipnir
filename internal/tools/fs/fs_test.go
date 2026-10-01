package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/perm"
	"github.com/reee344/sleipnir/internal/tools"
)

func TestRegisterRegistersAllToolsWithStableSpecs(t *testing.T) {
	r := tools.NewRegistry()
	Register(r)
	specs, err := r.Specs()
	if err != nil {
		t.Fatalf("Specs: %v", err)
	}
	var names []string
	for _, s := range specs {
		names = append(names, s.Name)
	}
	want := []string{"apply_patch", "edit", "glob", "grep", "ls", "read", "write"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want %v", names, want)
	}

	readOnly := map[string]bool{"read": true, "glob": true, "grep": true, "ls": true}
	for _, s := range specs {
		if s.ReadOnly != readOnly[s.Name] {
			t.Errorf("%s: ReadOnly = %v", s.Name, s.ReadOnly)
		}
		if words := len(strings.Fields(s.Description)); words > 120 || words < 15 {
			t.Errorf("%s: description has %d words (want 15..120)", s.Name, words)
		}
		var schema map[string]any
		if err := json.Unmarshal(s.InputSchema, &schema); err != nil {
			t.Errorf("%s: schema is not JSON: %v", s.Name, err)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("%s: schema type = %v", s.Name, schema["type"])
		}
		props, _ := schema["properties"].(map[string]any)
		if len(props) == 0 {
			t.Errorf("%s: no properties", s.Name)
		}
		for _, req := range toStrings(schema["required"]) {
			if _, ok := props[req]; !ok {
				t.Errorf("%s: required field %q is not a property", s.Name, req)
			}
		}
		// Schemas cost tokens on every request of every agent: keep them small.
		if len(s.InputSchema) > 1500 {
			t.Errorf("%s: schema is %d bytes", s.Name, len(s.InputSchema))
		}
	}

	// The tool list is byte-identical between registries: it is what the
	// provider caches once for the whole swarm.
	r2 := tools.NewRegistry()
	Register(r2)
	specs2, _ := r2.Specs()
	a, _ := json.Marshal(specs)
	b, _ := json.Marshal(specs2)
	if !bytes.Equal(a, b) {
		t.Errorf("specs differ between registries")
	}
	for _, name := range want {
		if tool, ok := r.Get(name); !ok || tool.Spec().Name != name {
			t.Errorf("Get(%q) failed", name)
		}
	}
}

func toStrings(v any) []string {
	var out []string
	if arr, ok := v.([]any); ok {
		for _, x := range arr {
			out = append(out, fmt.Sprint(x))
		}
	}
	return out
}

func TestParameterContract(t *testing.T) {
	// Names and parameters are a contract: models are prompted with them.
	want := map[string][]string{
		"read":        {"path", "offset", "limit"},
		"write":       {"path", "content"},
		"edit":        {"path", "old_string", "new_string", "replace_all", "edits"},
		"apply_patch": {"patch"},
		"glob":        {"pattern", "path"},
		"grep":        {"pattern", "path", "glob", "output_mode", "-i", "-n", "-A", "-B", "-C", "multiline", "head_limit"},
		"ls":          {"path", "depth", "ignore"},
	}
	r := tools.NewRegistry()
	Register(r)
	for name, params := range want {
		tool, _ := r.Get(name)
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tool.Spec().InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		var got []string
		for k := range schema.Properties {
			got = append(got, k)
		}
		sort.Strings(got)
		sort.Strings(params)
		if strings.Join(got, ",") != strings.Join(params, ",") {
			t.Errorf("%s parameters = %v, want %v", name, got, params)
		}
	}
}

func TestDescribeArgError(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`{"path": 5}`, `argument "path" must be a string (got number)`},
		{`{"path": ["a"]}`, `argument "path" must be a string (got array)`},
		{`{"path": {"a": 1}}`, `argument "path" must be a string (got object)`},
		{`{"path": true}`, `argument "path" must be a string (got bool)`},
		{`{"path": "x", "offset": "5"}`, `argument "offset" must be an integer (got string)`},
		{`{"path": "x", "offset": 1.5}`, `argument "offset" must be an integer (got number 1.5)`},
		{`{"path": "x", "offset": [1]}`, `argument "offset" must be an integer (got array)`},
		{`{"path": "x", "offset": true}`, `argument "offset" must be an integer (got bool)`},
		{`{"path": "x", "limit": {}}`, `argument "limit" must be an integer (got object)`},
		{`{"path": "x", "offset": 1e30}`, `argument "offset" must be an integer`},
		{`[1]`, `arguments must be a JSON object`},
		{`"text"`, `arguments must be a JSON object`},
		{`12`, `arguments must be a JSON object`},
		{`{"path": `, `not valid JSON`},
		{`{path: "x"}`, `not valid JSON`},
		{`{"path": "x"} trailing`, `not valid JSON`},
	}
	env := testEnv(t)
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			contains(t, mustErr(t, run(t, Read{}, env, tc.input)), tc.want)
		})
	}
}

// The argument that is named is the first wrong one, in the order the model wrote them, and the message is the same whichever Go
// built the binary (from Go 1.27 the decoder names no field for an error that a UnmarshalJSON method returns, and counts the
// elements of an array in the path of one it does name).
func TestTheFirstWrongArgumentIsTheOneNamedWhateverTheGoVersion(t *testing.T) {
	env := testEnv(t)
	for _, tc := range []struct{ input, want string }{
		{`{"path": "x", "offset": "5", "limit": "6"}`, `argument "offset" must be an integer (got string)`},
		{`{"limit": "6", "path": "x", "offset": "5"}`, `argument "limit" must be an integer (got string)`},
		{`{"path": 5, "offset": "5"}`, `argument "path" must be a string (got number)`},
		{`{"offset": 1.5, "path": 5}`, `argument "offset" must be an integer (got number 1.5)`},
	} {
		got := mustErr(t, run(t, Read{}, env, tc.input))
		contains(t, got, tc.want)
		if strings.Contains(got, "arguments must be a JSON object") {
			t.Errorf("%s: the answer does not name the argument: %s", tc.input, got)
		}
	}
	// The path of a wrong element of an array has no position in it.
	contains(t, mustErr(t, run(t, Edit{}, env, `{"path":"a.txt","edits":[{"old_string":"a","new_string":"b"},{"old_string":"c","new_string":5}]}`)),
		`argument "edits.new_string" must be a string (got number)`)
}

func TestIntArgAcceptsIntegralNumbers(t *testing.T) {
	for _, in := range []string{`5`, `5.0`, `5e0`, `0.5e1`, `+5`[1:]} {
		var a struct {
			N intArg `json:"n"`
		}
		if err := json.Unmarshal([]byte(`{"n":`+in+`}`), &a); err != nil || !a.N.Set || a.N.V != 5 {
			t.Errorf("%s -> %+v, %v", in, a.N, err)
		}
	}
	var a struct {
		N intArg `json:"n"`
	}
	if err := json.Unmarshal([]byte(`{"n":null}`), &a); err != nil || a.N.Set {
		t.Errorf("null must leave the argument unset: %+v %v", a.N, err)
	}
}

func TestAdversarialInputsNeverPanic(t *testing.T) {
	env := testEnv(t)
	writeFile(t, filepath.Join(env.Cwd, "f.txt"), "hello\nworld\n")
	os.Symlink("loop", filepath.Join(env.Cwd, "loop"))
	deep := strings.Repeat("[", 5000) + strings.Repeat("]", 5000)
	hugePath := strings.Repeat("x", 1<<20)
	inputs := []string{
		``, `null`, `{}`, `[]`, `""`, `0`, `true`, `{`, `}`, `{"a"`, `{"path":null}`, `{"path":[]}`,
		`{"path":"` + hugePath + `"}`,
		`{"path":"\u0000"}`, `{"path":"/"}`, `{"path":"."}`, `{"path":".."}`, `{"path":"~"}`, `{"path":"loop"}`,
		`{"path":"a\nb"}`, `{"path":"\ud800"}`, `{"path":"日本語/🎉"}`,
		`{"path":` + deep + `}`,
		`{"pattern":"` + strings.Repeat("(", 1000) + `"}`,
		`{"pattern":"` + strings.Repeat("a*", 5000) + `"}`,
		`{"pattern":"(?i)` + strings.Repeat("x", 100000) + `"}`,
		`{"pattern":"**/*.go","path":"` + hugePath + `"}`,
		`{"patch":"` + strings.Repeat("*** Begin Patch\\n", 1000) + `"}`,
		`{"patch":"*** Begin Patch\n*** Update File: ` + hugePath + `\n@@\n-a\n+b\n*** End Patch"}`,
		`{"path":"f.txt","old_string":"` + hugePath + `","new_string":"x"}`,
		`{"path":"f.txt","edits":[` + strings.Repeat(`{"old_string":"h","new_string":"h"},`, 300) + `{"old_string":"h","new_string":"H"}]}`,
		`{"path":"f.txt","content":"` + strings.Repeat("é", 100000) + `"}`,
		`{"path":"f.txt","offset":-9223372036854775808}`,
		`{"path":"f.txt","offset":9223372036854775807}`,
		`{"path":"f.txt","limit":1e300}`,
		`{"depth":1000000,"path":"."}`,
		`{"ignore":[null,1]}`,
		`{"ignore":["` + strings.Repeat("{a,b}", 50) + `"]}`,
		`{"pattern":"x","glob":"` + strings.Repeat("{a,b}", 50) + `"}`,
		`{"pattern":"x","head_limit":9223372036854775807,"-C":1e9}`,
	}
	all := []tools.Tool{Read{}, Write{}, Edit{}, ApplyPatch{}, Glob{}, Grep{}, LS{}}
	for _, tool := range all {
		for i, in := range inputs {
			name := fmt.Sprintf("%s/%d", tool.Spec().Name, i)
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("%s panicked on input %.60q: %v", name, in, r)
					}
				}()
				res, err := tool.Run(context.Background(), &tools.Call{Input: json.RawMessage(in), Env: env})
				if err != nil || res == nil {
					t.Errorf("%s: err=%v res=%v", name, err, res)
					return
				}
				if res.IsError && strings.TrimSpace(res.Text) == "" {
					t.Errorf("%s: empty error text", name)
				}
				if res.IsError && len(res.Text) > env.Limits.MaxOutputChars+300 {
					t.Errorf("%s: error text is %d chars", name, len(res.Text))
				}
			}()
		}
	}
	if got := readFileT(t, filepath.Join(env.Cwd, "f.txt")); got != "hello\nworld\n" && !strings.HasPrefix(got, "é") {
		// Only the deliberate write of "é..." content may change the file.
		t.Errorf("file unexpectedly changed: %.40q", got)
	}
}

func TestEnvDefaultsAreAppliedSafelyUnderConcurrency(t *testing.T) {
	// Tools are handed an Env whose optional fields may be nil, and one agent's
	// parallel read-only calls share it: filling the defaults must not race.
	dir := realTemp(t)
	writeFile(t, filepath.Join(dir, "f.txt"), "x\n")
	env := &tools.Env{Cwd: dir, Perm: perm.AllowAll{}}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var tool tools.Tool = Read{}
			in := `{"path":"f.txt"}`
			if i%3 == 1 {
				tool, in = Glob{}, `{"pattern":"*"}`
			} else if i%3 == 2 {
				tool, in = LS{}, `{}`
			}
			res, err := tool.Run(context.Background(), &tools.Call{Input: json.RawMessage(in), Env: env})
			if err != nil || res.IsError {
				t.Errorf("%T: %v %v", tool, err, res)
			}
		}(i)
	}
	wg.Wait()
}

func TestResultsGoThroughFinish(t *testing.T) {
	// Any oversized result is truncated and stored behind a recall handle.
	env := testEnv(t)
	env.Limits.MaxOutputChars = 200
	writeFile(t, filepath.Join(env.Cwd, "a.txt"), strings.Repeat("needle line\n", 100))
	res := run(t, &Grep{DisableRipgrep: true}, env, map[string]any{"pattern": "needle"})
	if !res.Truncated || res.Handle == "" || res.FullRef == "" {
		t.Fatalf("grep result not truncated through Finish: %+v", res)
	}
	full, err := env.Blobs.Get(res.FullRef)
	if err != nil || strings.Count(string(full), "\n") < 99 {
		t.Errorf("full output not stored: %v", err)
	}
	// Error results too.
	long := strings.Repeat("d/", 400) + "file"
	res = run(t, Read{}, env, map[string]any{"path": long})
	if !res.IsError {
		t.Fatalf("expected an error")
	}
	if len(res.Text) > 400 {
		t.Errorf("error text not bounded: %d chars", len(res.Text))
	}
}

// foldCRLF converts CRLF to LF.
func foldCRLF(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func TestEditLineEndingProperties(t *testing.T) {
	// For random mixed-ending text and random LF edits:
	//  - the result, ignoring line-ending differences, equals the edit applied to
	//    the folded text (the edit means the same thing whatever the endings);
	//  - a pure-CRLF file stays pure CRLF, a pure-LF file stays pure LF;
	//  - text outside the edited span keeps its exact bytes.
	rng := rand.New(rand.NewSource(99))
	alphabet := []string{"a", "b", "c", "ab", "x y", " ", "\n", "\n", "\n"}
	gen := func(n int) string {
		var sb strings.Builder
		for i := 0; i < n; i++ {
			sb.WriteString(alphabet[rng.Intn(len(alphabet))])
		}
		return sb.String()
	}
	for iter := 0; iter < 4000; iter++ {
		lf := gen(rng.Intn(30))
		var cur string
		switch iter % 3 {
		case 0:
			cur = lf
		case 1:
			cur = strings.ReplaceAll(lf, "\n", "\r\n")
		default: // mixed
			var sb strings.Builder
			for _, r := range lf {
				if r == '\n' && rng.Intn(2) == 0 {
					sb.WriteString("\r\n")
				} else {
					sb.WriteRune(r)
				}
			}
			cur = sb.String()
		}
		// Pick old from the text (so it usually matches) or random.
		var old string
		if lf != "" && rng.Intn(4) != 0 {
			i := rng.Intn(len(lf))
			j := i + 1 + rng.Intn(min(len(lf)-i, 6))
			old = lf[i:j]
		} else {
			old = gen(1 + rng.Intn(3))
		}
		repl := gen(rng.Intn(4))
		all := rng.Intn(2) == 0
		sp := editSpec{old: old, new: repl, all: all}

		got, n, msg := applyOne(cur, sp, "f")
		wantLF, wn, wmsg := applyOne(foldCRLF(cur), sp, "f")
		if (msg == "") != (wmsg == "") {
			t.Fatalf("iteration %d: success differs between %q and its folded form\nold=%q repl=%q all=%v\nmsg=%q wmsg=%q", iter, cur, old, repl, all, msg, wmsg)
		}
		if msg != "" {
			continue
		}
		if n != wn {
			t.Fatalf("iteration %d: %d replacements vs %d folded (cur=%q old=%q)", iter, n, wn, cur, old)
		}
		if foldCRLF(got) != wantLF {
			t.Fatalf("iteration %d: fold(result) != result of folded edit\ncur=%q old=%q repl=%q all=%v\ngot=%q\nwant=%q", iter, cur, old, repl, all, foldCRLF(got), wantLF)
		}
		switch iter % 3 {
		case 0:
			if strings.Contains(got, "\r") {
				t.Fatalf("iteration %d: LF file gained a CR: %q", iter, got)
			}
		case 1:
			if strings.Contains(cur, "\r\n") && strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
				t.Fatalf("iteration %d: CRLF file gained a bare LF: cur=%q old=%q repl=%q got=%q", iter, cur, old, repl, got)
			}
		}
	}
}

func TestEditIsByteExactOutsideTheMatch(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	for iter := 0; iter < 2000; iter++ {
		var sb strings.Builder
		for i := 0; i < 5+rng.Intn(40); i++ {
			sb.WriteByte(byte(rng.Intn(256)))
			if rng.Intn(4) == 0 {
				sb.WriteString([]string{"\n", "\r\n", "MARK"}[rng.Intn(3)])
			}
		}
		cur := sb.String()
		got, n, msg := applyOne(cur, editSpec{old: "MARK", new: "M"}, "f")
		count := strings.Count(cur, "MARK")
		switch {
		case count == 0:
			if msg == "" {
				t.Fatalf("no match should fail")
			}
		case count == 1:
			if msg != "" || n != 1 || got != strings.Replace(cur, "MARK", "M", 1) {
				t.Fatalf("iteration %d: %q -> %q (%d, %q)", iter, cur, got, n, msg)
			}
		default:
			if msg == "" {
				t.Fatalf("ambiguous match must fail")
			}
		}
	}
}

func TestBlobStoreInteraction(t *testing.T) {
	// Sanity: images are stored once per distinct content.
	env := testEnv(t)
	png := "\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 40)
	writeFile(t, filepath.Join(env.Cwd, "a.png"), png)
	writeFile(t, filepath.Join(env.Cwd, "b.png"), png)
	r1 := run(t, Read{}, env, map[string]any{"path": "a.png"})
	r2 := run(t, Read{}, env, map[string]any{"path": "b.png"})
	if r1.Blocks[0].MediaRef != r2.Blocks[0].MediaRef || r1.Blocks[0].MediaRef != string(core.HashBytes([]byte(png))) {
		t.Errorf("image blobs are content addressed: %q vs %q", r1.Blocks[0].MediaRef, r2.Blocks[0].MediaRef)
	}
}

func TestAllToolsWorkWithABareEnv(t *testing.T) {
	// The minimal Env a caller can build: only Cwd. Defaults() supplies the rest
	// on the first call and later calls (same *Env) share that state, so the
	// read-before-edit rule still works across calls.
	dir := realTemp(t)
	env := &tools.Env{Cwd: dir, Perm: perm.AllowAll{}}
	mustOK(t, run(t, Write{}, env, map[string]any{"path": "a/b.txt", "content": "one\ntwo\n"}))
	mustOK(t, run(t, Read{}, env, map[string]any{"path": "a/b.txt"}))
	mustOK(t, run(t, Edit{}, env, map[string]any{"path": "a/b.txt", "old_string": "one", "new_string": "1"}))
	mustOK(t, run(t, ApplyPatch{}, env, map[string]any{"patch": patchText("*** Update File: a/b.txt", "@@", "-two", "+2", "*** Add File: c.txt", "+c")}))
	contains(t, mustOK(t, run(t, Glob{}, env, map[string]any{"pattern": "**/*.txt"})), "a/b.txt", "c.txt")
	contains(t, mustOK(t, run(t, Grep{}, env, map[string]any{"pattern": "^2$"})), "a/b.txt:2:2")
	contains(t, mustOK(t, run(t, LS{}, env, map[string]any{})), "a/", "c.txt")
	if got := readFileT(t, filepath.Join(dir, "a/b.txt")); got != "1\n2\n" {
		t.Errorf("content = %q", got)
	}
	// Edit without a prior read is still refused: state was kept between calls.
	writeFile(t, filepath.Join(dir, "unread.txt"), "x\n")
	contains(t, mustErr(t, run(t, Edit{}, env, map[string]any{"path": "unread.txt", "old_string": "x", "new_string": "y"})), "not been read")
}
