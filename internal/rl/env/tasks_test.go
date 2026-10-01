package env

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// goodTask is a minimal valid task; tests mutate copies of it.
func goodTask(id string) rl.Task {
	return rl.Task{
		ID:       id,
		Kind:     rl.TaskFix,
		Repo:     rl.RepoSpec{Path: "/data/repos/" + id, Commit: "a1b2c3d4"},
		Prompt:   "fix it",
		Team:     rl.Team{Mode: "single"},
		Verifier: rl.Verifier{Cmd: "go test ./..."},
	}
}

func TestValidateTask(t *testing.T) {
	hex64 := strings.Repeat("ab", 32)
	tests := []struct {
		name    string
		mutate  func(*rl.Task)
		wantErr string // substring naming the field; "" means valid
	}{
		{"valid", func(*rl.Task) {}, ""},
		{"empty id", func(t *rl.Task) { t.ID = "" }, "id: is required"},
		{"id with slash", func(t *rl.Task) { t.ID = "a/b" }, "id: must match"},
		{"id dot dot", func(t *rl.Task) { t.ID = ".." }, "id: must match"},
		{"id leading dot", func(t *rl.Task) { t.ID = ".hidden" }, "id: must match"},
		{"unknown kind", func(t *rl.Task) { t.Kind = "sing" }, "kind: unknown kind"},
		{"empty prompt", func(t *rl.Task) { t.Prompt = " \n" }, "prompt: is required"},
		{"no repo location", func(t *rl.Task) { t.Repo.Path = "" }, "repo: needs a path or a url"},
		{"no commit", func(t *rl.Task) { t.Repo.Commit = "" }, "repo.commit: is required"},
		{"option-like commit", func(t *rl.Task) { t.Repo.Commit = "--upload-pack=touch /tmp/x" }, "repo.commit"},
		{"commit with space", func(t *rl.Task) { t.Repo.Commit = "a b" }, "repo.commit"},
		{"ref commit ok", func(t *rl.Task) { t.Repo.Commit = "refs/tags/v1.2.3" }, ""},
		{"url ext transport", func(t *rl.Task) { t.Repo.Path = ""; t.Repo.URL = "ext::sh -c touch% /tmp/pwned" }, "repo.url"},
		{"url dash", func(t *rl.Task) { t.Repo.Path = ""; t.Repo.URL = "--upload-pack=x" }, "repo.url"},
		{"url https ok", func(t *rl.Task) { t.Repo.Path = ""; t.Repo.URL = "https://github.com/a/b.git" }, ""},
		{"url scp ok", func(t *rl.Task) { t.Repo.Path = ""; t.Repo.URL = "git@github.com:a/b.git" }, ""},
		{"subdir traversal", func(t *rl.Task) { t.Repo.Subdir = "../x" }, "repo.subdir"},
		{"subdir absolute", func(t *rl.Task) { t.Repo.Subdir = "/etc" }, "repo.subdir"},
		{"subdir ok", func(t *rl.Task) { t.Repo.Subdir = "svc/api" }, ""},
		{"empty setup cmd", func(t *rl.Task) { t.Setup = []string{"go mod download", "  "} }, "setup[1]: is empty"},
		{"team mode", func(t *rl.Task) { t.Team.Mode = "herd" }, "team.mode"},
		{"single with agents", func(t *rl.Task) { t.Team.Agents = 3 }, "team.agents"},
		{"swarm ok", func(t *rl.Task) { t.Team = rl.Team{Mode: "swarm", Agents: 4, Roles: []string{"a", "b"}} }, ""},
		{"dup roles", func(t *rl.Task) { t.Team = rl.Team{Mode: "swarm", Roles: []string{"a", "a"}} }, "team.roles[1]"},
		{"negative steps", func(t *rl.Task) { t.Budget.Steps = -1 }, "budget.steps"},
		{"negative requests", func(t *rl.Task) { t.Budget.Requests = -1 }, "budget.requests"},
		{"negative wall", func(t *rl.Task) { t.Budget.WallS = -5 }, "budget.wall_s"},
		{"negative ctx", func(t *rl.Task) { t.Budget.ContextWindow = -5 }, "budget.context_window"},
		{"negative ite", func(t *rl.Task) { t.Budget.ITE = -0.5 }, "budget.ite"},
		{"tag with space", func(t *rl.Task) { t.Tags = []string{"a b"} }, "tags[0]"},
		{"tag with comma", func(t *rl.Task) { t.Tags = []string{"a,b"} }, "tags[0]"},
		{"empty cmd", func(t *rl.Task) { t.Verifier.Cmd = "" }, "verifier.cmd"},
		{"negative timeout", func(t *rl.Task) { t.Verifier.TimeoutS = -1 }, "verifier.timeout_s"},
		{"bad pass", func(t *rl.Task) { t.Verifier.Pass = "vibes" }, "verifier.pass"},
		{"bad regex", func(t *rl.Task) { t.Verifier.Pass = "regex:(" }, "verifier.pass"},
		{"empty regex", func(t *rl.Task) { t.Verifier.Pass = "regex:" }, "verifier.pass"},
		{"regex ok", func(t *rl.Task) { t.Verifier.Pass = "regex:^ok\\s" }, ""},
		{"json-score ok", func(t *rl.Task) { t.Verifier.Pass = "json-score" }, ""},
		{"hidden traversal", func(t *rl.Task) { t.Verifier.Hidden = map[string]string{"../x_test.go": "text:x"} }, "verifier.hidden"},
		{"hidden absolute", func(t *rl.Task) { t.Verifier.Hidden = map[string]string{"/etc/passwd": "text:x"} }, "verifier.hidden"},
		{"hidden .git", func(t *rl.Task) { t.Verifier.Hidden = map[string]string{".git/hooks/pre-commit": "text:x"} }, "verifier.hidden"},
		{"hidden .GIT", func(t *rl.Task) { t.Verifier.Hidden = map[string]string{"a/.GIT/config": "text:x"} }, "verifier.hidden"},
		{"hidden ntfs git", func(t *rl.Task) { t.Verifier.Hidden = map[string]string{"git~1/config": "text:x"} }, "verifier.hidden"},
		{"hidden bad ref", func(t *rl.Task) { t.Verifier.Hidden = map[string]string{"a_test.go": "file:/etc/passwd"} }, "verifier.hidden"},
		{"hidden short blob", func(t *rl.Task) { t.Verifier.Hidden = map[string]string{"a_test.go": "blob:9f2c"} }, "verifier.hidden"},
		{"hidden ok", func(t *rl.Task) {
			t.Verifier.Hidden = map[string]string{"a_test.go": "blob:" + hex64, "b/c_test.go": "text:package b"}
		}, ""},
		{"protected negation", func(t *rl.Task) { t.Verifier.Protected = []string{"!a"} }, "verifier.protected[0]"},
		{"protected unterminated", func(t *rl.Task) { t.Verifier.Protected = []string{"a[b"} }, "verifier.protected[0]"},
		{"protected traversal", func(t *rl.Task) { t.Verifier.Protected = []string{"../x"} }, "verifier.protected[0]"},
		{"protected ok", func(t *rl.Task) { t.Verifier.Protected = []string{"*_test.go", "go.mod", ".github/**"} }, ""},
		{"recall without expect", func(t *rl.Task) { t.Kind = rl.TaskRecall }, "verifier.expect: is required"},
		{"recall with expect no cmd", func(t *rl.Task) {
			t.Kind = rl.TaskRecall
			t.Verifier = rl.Verifier{Expect: json.RawMessage(`{"contains":["x"]}`)}
		}, ""},
		{"expect empty list", func(t *rl.Task) { t.Verifier.Expect = json.RawMessage(`{"contains":[]}`) }, "verifier.expect"},
		{"expect empty string", func(t *rl.Task) { t.Verifier.Expect = json.RawMessage(`{"contains":[""]}`) }, "verifier.expect"},
		{"expect typo", func(t *rl.Task) { t.Verifier.Expect = json.RawMessage(`{"contain":["x"]}`) }, "verifier.expect"},
		{"expect null is absent", func(t *rl.Task) { t.Verifier.Expect = json.RawMessage(`null`) }, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := goodTask("t1")
			tc.mutate(&task)
			err := ValidateTask(task)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.wantErr)
			}
			if task.ID != "" && !strings.Contains(err.Error(), fmt.Sprintf("task %q", task.ID)) {
				t.Errorf("error does not name the task id: %v", err)
			}
		})
	}
}

func TestValidateTasksDuplicateID(t *testing.T) {
	err := ValidateTasks([]rl.Task{goodTask("a"), goodTask("b"), goodTask("a")})
	if err == nil || !strings.Contains(err.Error(), `task "a": id: duplicate id`) {
		t.Fatalf("got %v", err)
	}
}

func TestLoadWriteTasksRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "tasks.jsonl")
	a := goodTask("a")
	a.Tags = []string{"go"}
	a.Verifier.Hidden = map[string]string{"x_test.go": "text:package x <&>"}
	a.Verifier.Protected = []string{"*_test.go"}
	a.Meta = json.RawMessage(`{"commit":"abc"}`)
	b := goodTask("b")
	b.Kind = rl.TaskSwarm
	b.Team = rl.Team{Mode: "swarm", Agents: 2}
	want := []rl.Task{a, b}
	if err := WriteTasks(p, want); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(p)
	if strings.Contains(string(raw), "\\"+"u003c") {
		t.Errorf("HTML characters were escaped in the file: %s", raw)
	}
	got, err := LoadTasks(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed tasks:\n got %+v\nwant %+v", got, want)
	}
	// A task that does not validate is never written.
	bad := goodTask("bad")
	bad.Verifier.Cmd = ""
	if err := WriteTasks(filepath.Join(dir, "bad.jsonl"), []rl.Task{bad}); err == nil {
		t.Fatal("WriteTasks accepted an invalid task")
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.jsonl")); err == nil {
		t.Fatal("invalid task file was created")
	}
}

func TestLoadTasksStrict(t *testing.T) {
	good, _ := json.Marshal(goodTask("ok"))
	tests := []struct {
		name    string
		content string
		wantErr string
	}{
		{"unknown field", string(good[:len(good)-1]) + `,"extra":1}` + "\n", "unknown field"},
		{"two values", string(good) + string(good) + "\n", "more than one value"},
		{"garbage", "not json\n", "invalid JSON"},
		{"truncated", string(good[:20]) + "\n", "invalid JSON"},
		{"bom and blanks ok", "\xef\xbb\xbf\n" + string(good) + "\n\n\n", ""},
		{"crlf ok", string(good) + "\r\n", ""},
		{"no trailing newline ok", string(good), ""},
		{"empty file ok", "", ""},
		{"wrong type", `{"id":5,"kind":"fix"}` + "\n", "invalid JSON"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "t.jsonl")
			if err := os.WriteFile(p, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadTasks(p)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), "line 1") {
				t.Errorf("error should carry the line number: %v", err)
			}
		})
	}
}

func TestLoadTasksReportsAllProblemsWithLines(t *testing.T) {
	a := goodTask("a")
	b := goodTask("b")
	b.Verifier.Cmd = ""
	c := goodTask("a") // duplicate of line 1
	c.Kind = "nope"
	var sb strings.Builder
	for _, tk := range []rl.Task{a, b, c} {
		raw, _ := json.Marshal(tk)
		sb.Write(raw)
		sb.WriteByte('\n')
	}
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(sb.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadTasks(p)
	if err == nil {
		t.Fatal("expected errors")
	}
	msg := err.Error()
	for _, want := range []string{"line 2", `task "b"`, "verifier.cmd", "line 3", "kind: unknown kind", "duplicate id"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in:\n%s", want, msg)
		}
	}
	var te *TaskError
	if !errors.As(err, &te) {
		t.Errorf("errors.As(*TaskError) failed for %T", err)
	}
}

func TestLoadTasksHugeLine(t *testing.T) {
	// A line just under the cap loads; the memory guard itself is exercised in
	// readLine's own test to keep this one fast.
	task := goodTask("big")
	task.Verifier.Hidden = map[string]string{"big_test.go": "text:" + strings.Repeat("x", 3<<20)}
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := WriteTasks(p, []rl.Task{task}); err != nil {
		t.Fatal(err)
	}
	got, err := LoadTasks(p)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %d tasks, err %v", len(got), err)
	}
}

func manyTasks(n int, tagFn func(i int) []string) []rl.Task {
	var out []rl.Task
	for i := 0; i < n; i++ {
		tk := goodTask(fmt.Sprintf("t%03d", i))
		tk.Repo.Path = fmt.Sprintf("/repos/r%d", i/3) // three tasks per repo
		tk.Tags = tagFn(i)
		out = append(out, tk)
	}
	return out
}

func ids(ts []rl.Task) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

func TestFilter(t *testing.T) {
	tasks := manyTasks(30, func(i int) []string {
		tags := []string{"go"}
		if i%2 == 0 {
			tags = append(tags, "small")
		}
		if i%5 == 0 {
			tags = append(tags, "flaky")
		}
		return tags
	})
	t.Run("all tags required", func(t *testing.T) {
		got := Filter(tasks, []string{"go", "small"}, nil, 0, 1)
		if len(got) != 15 {
			t.Fatalf("got %d", len(got))
		}
	})
	t.Run("negated tag", func(t *testing.T) {
		got := Filter(tasks, []string{"small", "!flaky"}, nil, 0, 1)
		for _, tk := range got {
			for _, tag := range tk.Tags {
				if tag == "flaky" {
					t.Fatalf("%s is flaky", tk.ID)
				}
			}
		}
		if len(got) != 12 {
			t.Fatalf("got %d", len(got))
		}
	})
	t.Run("ids and globs", func(t *testing.T) {
		got := Filter(tasks, nil, []string{"t001", "t02*"}, 0, 1)
		if !reflect.DeepEqual(ids(got), []string{"t001", "t020", "t021", "t022", "t023", "t024", "t025", "t026", "t027", "t028", "t029"}) {
			t.Fatalf("got %v", ids(got))
		}
	})
	t.Run("n is deterministic and order preserving", func(t *testing.T) {
		a := Filter(tasks, nil, nil, 7, 42)
		b := Filter(tasks, nil, nil, 7, 42)
		c := Filter(tasks, nil, nil, 7, 43)
		if len(a) != 7 || !reflect.DeepEqual(ids(a), ids(b)) {
			t.Fatalf("not deterministic: %v vs %v", ids(a), ids(b))
		}
		if reflect.DeepEqual(ids(a), ids(c)) {
			t.Fatalf("different seeds gave the same subset")
		}
		if !sort.StringsAreSorted(ids(a)) {
			t.Fatalf("original order lost: %v", ids(a))
		}
	})
	t.Run("selection ignores input order and new tasks", func(t *testing.T) {
		rev := append([]rl.Task(nil), tasks...)
		for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
			rev[i], rev[j] = rev[j], rev[i]
		}
		a := ids(Filter(tasks, nil, nil, 5, 9))
		b := ids(Filter(rev, nil, nil, 5, 9))
		sort.Strings(b)
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("input order changed the subset: %v vs %v", a, b)
		}
	})
	t.Run("n larger than input", func(t *testing.T) {
		if got := Filter(tasks, nil, nil, 100, 1); len(got) != 30 {
			t.Fatalf("got %d", len(got))
		}
	})
	t.Run("empty", func(t *testing.T) {
		if got := Filter(nil, []string{"x"}, nil, 3, 1); len(got) != 0 {
			t.Fatalf("got %d", len(got))
		}
	})
}

func TestSplitByRepoDeterministic(t *testing.T) {
	tasks := manyTasks(60, func(int) []string { return nil }) // 20 repos x 3 tasks
	parts, err := Split(tasks, "train:0.9,val:0.1", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(parts["train"])+len(parts["val"]) != 60 {
		t.Fatalf("tasks lost: %d + %d", len(parts["train"]), len(parts["val"]))
	}
	// Repos never straddle parts.
	seen := map[string]string{}
	for name, ts := range parts {
		for _, tk := range ts {
			if prev, ok := seen[RepoKey(tk)]; ok && prev != name {
				t.Fatalf("repo %s is in both %s and %s", RepoKey(tk), prev, name)
			}
			seen[RepoKey(tk)] = name
		}
	}
	if n := len(parts["val"]); n < 3 || n > 9 {
		t.Errorf("val part has %d tasks, want roughly 6", n)
	}
	// Deterministic, and independent of input order.
	again, _ := Split(tasks, "train:0.9,val:0.1", 7)
	if !reflect.DeepEqual(ids(parts["val"]), ids(again["val"])) {
		t.Fatal("split is not deterministic")
	}
	rev := append([]rl.Task(nil), tasks...)
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	shuffled, _ := Split(rev, "train:0.9,val:0.1", 7)
	a, b := ids(parts["val"]), ids(shuffled["val"])
	sort.Strings(b)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("input order changed the split: %v vs %v", a, b)
	}
	other, _ := Split(tasks, "train:0.9,val:0.1", 8)
	if reflect.DeepEqual(ids(parts["val"]), ids(other["val"])) {
		t.Error("seed has no effect")
	}
}

func TestSplitSpecs(t *testing.T) {
	tasks := manyTasks(30, func(int) []string { return nil })
	parts, err := Split(tasks, "a:1,b:1,c:1", 1)
	if err != nil {
		t.Fatal(err)
	}
	for name, ts := range parts {
		if len(ts) != 9 && len(ts) != 12 && len(ts) != 6 {
			t.Errorf("part %s has %d tasks", name, len(ts))
		}
	}
	for _, bad := range []string{"", "train", "train:x", "train:-1", "train:0", "a:1,a:2", ":1", "a:NaN"} {
		if _, err := Split(tasks, bad, 1); err == nil {
			t.Errorf("spec %q accepted", bad)
		}
	}
	// Tasks without repo identity are their own group instead of colliding.
	tk := goodTask("x")
	tk.Repo = rl.RepoSpec{Commit: "abc"}
	got, err := Split([]rl.Task{tk, tk}, "a:1,b:1", 1)
	if err != nil || len(got["a"])+len(got["b"]) != 2 {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestRepoKeyNormalisation(t *testing.T) {
	tests := []struct{ a, b rl.RepoSpec }{
		{rl.RepoSpec{URL: "https://GitHub.com/org/repo.git"}, rl.RepoSpec{URL: "git@github.com:org/repo"}},
		{rl.RepoSpec{URL: "https://github.com/org/repo/"}, rl.RepoSpec{URL: "ssh://git@github.com/org/repo.git"}},
		{rl.RepoSpec{Path: "/data/repos/mux/"}, rl.RepoSpec{Path: "/data/repos/./mux"}},
	}
	for _, tc := range tests {
		ka, kb := RepoKey(rl.Task{Repo: tc.a}), RepoKey(rl.Task{Repo: tc.b})
		if ka != kb || ka == "" {
			t.Errorf("%+v -> %q, %+v -> %q", tc.a, ka, tc.b, kb)
		}
	}
	if RepoKey(rl.Task{Repo: rl.RepoSpec{URL: "https://github.com/a/b"}}) == RepoKey(rl.Task{Repo: rl.RepoSpec{URL: "https://github.com/a/c"}}) {
		t.Error("different repos share a key")
	}
}

func TestHoldout(t *testing.T) {
	mk := func(id, repo, date string) rl.Task {
		tk := goodTask(id)
		tk.Repo.Path = repo
		if date != "" {
			tk.Meta = json.RawMessage(fmt.Sprintf(`{"commit":"c","author_date":%q}`, date))
		}
		return tk
	}
	tasks := []rl.Task{
		mk("old", "/r/a", "2023-01-01T00:00:00Z"),
		mk("new", "/r/a", "2025-06-01T00:00:00Z"),
		mk("other", "/r/b", "2020-01-01T00:00:00Z"),
		mk("undated", "/r/c", ""),
	}
	train, held, undated := Holdout(tasks, []string{"/r/b"}, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	if !reflect.DeepEqual(ids(train), []string{"old", "undated"}) || !reflect.DeepEqual(ids(held), []string{"new", "other"}) || undated != 1 {
		t.Fatalf("train=%v held=%v undated=%d", ids(train), ids(held), undated)
	}
	train, held, undated = Holdout(tasks, nil, time.Time{})
	if len(train) != 4 || len(held) != 0 || undated != 0 {
		t.Fatalf("no criteria: %d %d %d", len(train), len(held), undated)
	}
}

func TestExcludeSet(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "train.jsonl")
	train := goodTask("train-1")
	train.Repo = rl.RepoSpec{Path: "/repos/mux", Commit: "deadbeef"}
	raw, _ := json.Marshal(train)
	content := "# comment\nplain-id\n\"quoted-id\"\n" + string(raw) + "\n\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	ex, err := LoadExcludeList(p)
	if err != nil {
		t.Fatal(err)
	}
	evalTasks := []rl.Task{goodTask("plain-id"), goodTask("quoted-id"), goodTask("train-1"), goodTask("clean")}
	renamed := goodTask("renamed")
	renamed.Repo = rl.RepoSpec{Path: "/repos/mux/", Commit: "deadbeef"}
	evalTasks = append(evalTasks, renamed)
	got := ex.Overlap(evalTasks)
	want := []string{"plain-id", "quoted-id", "renamed", "train-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("overlap = %v, want %v", got, want)
	}
	if len(ExcludeFromTasks([]rl.Task{train}).Overlap([]rl.Task{renamed})) != 1 {
		t.Error("ExcludeFromTasks missed a renamed copy")
	}
	if (*ExcludeSet)(nil).Overlap(evalTasks) != nil {
		t.Error("nil set should report no overlap")
	}
	if err := os.WriteFile(p, []byte("{bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadExcludeList(p); err == nil {
		t.Error("malformed exclude list accepted")
	}
}

func TestParsePassMode(t *testing.T) {
	m, err := parsePassMode("regex:^ok\\s")
	if err != nil || m.Kind != PassRegex {
		t.Fatal(m, err)
	}
	if !m.Regex.MatchString("noise\nok  pkg 0.1s\n") {
		t.Error("(?m) anchoring is not applied")
	}
	// RE2 has no catastrophic backtracking: a classic evil pattern is fine.
	evil, err := parsePassMode("regex:(a+)+$")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	evil.Regex.MatchString(strings.Repeat("a", 50000) + "b")
	if time.Since(start) > 5*time.Second {
		t.Error("regex matching is not linear time")
	}
}
