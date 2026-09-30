package swarm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/reee344/sleipnir/internal/workspace"
)

// {dirs} lets a decomposed task be verified on its own work: `go test {dirs}` is `go test ./p01 ./p05`
// for a task that may touch those two directories, and everything only when the task has no scope.
func TestExpandVerifyReplacesDirsWithTheTasksDirectories(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"p01", "p05", "internal/a", "docs"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	long := make([]string, 0, maxVerifyDirs+1)
	for i := 0; i <= maxVerifyDirs; i++ {
		long = append(long, "pkg"+strings.Repeat("x", i)+"/f.go")
	}
	for _, tc := range []struct {
		name  string
		cmd   string
		files []string
		want  string
	}{
		{"no token: untouched", "go test ./...", []string{"p01/**"}, "go test ./..."},
		{"a glob contributes its directory", "go test {dirs}", []string{"p01/**"}, "go test ./p01"},
		{"files contribute their parents, distinct and sorted", "go test {dirs}", []string{"p05/p05.go", "p01/p01.go", "p01/p01_test.go"}, "go test ./p01 ./p05"},
		{"a directory that exists is itself", "go test {dirs}", []string{"internal/a"}, "go test ./internal/a"},
		{"a file that does not exist yet contributes its parent", "go test {dirs}", []string{"internal/a/new.go"}, "go test ./internal/a"},
		{"a wildcard in the last directory's name is the top", "go test {dirs}", []string{"p0*/x.go"}, "go test ./..."},
		{"a wildcard in a file name", "go test {dirs}", []string{"docs/n*.md"}, "go test ./docs"},
		{"the token can appear twice", "vet {dirs} && test {dirs}", []string{"p01/**"}, "vet ./p01 && test ./p01"},
		{"no scope: everything", "go test {dirs}", nil, "go test ./..."},
		{"the top of the repository: everything", "go test {dirs}", []string{"**/*.go"}, "go test ./..."},
		{"a file at the top: everything", "go test {dirs}", []string{"go.mod"}, "go test ./..."},
		{"too many directories: everything", "go test {dirs}", long, "go test ./..."},
		{"a leading ./ is ignored", "go test {dirs}", []string{"./p01/x.go"}, "go test ./p01"},
	} {
		got := ExpandVerify(tc.cmd, root, tc.files)
		if got != tc.want {
			t.Errorf("%s: ExpandVerify(%q, %q) = %q, want %q", tc.name, tc.cmd, tc.files, got, tc.want)
		}
	}
}

// The scope is text a model wrote and it lands in a command line: anything that is not a plain
// relative path makes {dirs} the whole tree, and nothing of it is ever inserted.
func TestExpandVerifyNeverInsertsAnythingButPlainPaths(t *testing.T) {
	root := t.TempDir()
	for _, f := range []string{
		"p01/x.go; rm -rf ~", "$(reboot)/x.go", "`id`/a", "a b/c.go", "p01/'x'", `p01/"x"`, "p01\nrm/x.go", "p01/a|b",
		"../outside/x.go", "p01/../../x.go", "/etc/passwd", "-rf/x.go", "p01/x.go&&id", "p01/<x>", "p01/\x00",
	} {
		got := ExpandVerify("go test {dirs}", root, []string{"p01/ok.go", f})
		if got != "go test ./..." {
			t.Errorf("entry %q: got %q, want the whole tree (nothing of the entry may reach the command)", f, got)
		}
	}
	if got := ExpandVerify("go test {dirs}", root, []string{"p01/ok.go", "p_2/a-b.c+d/x.go"}); got != "go test ./p01 ./p_2/a-b.c+d" {
		t.Errorf("plain paths: %q", got)
	}
}

// The verifier a worker's done, the gate after a stop and the merge all run is the one for the task's own
// scope, and a failure says which command failed.
func TestTheVerifierRunsWithTheTasksScope(t *testing.T) {
	var mu sync.Mutex
	var ran []string
	s := New(Config{VerifyCmd: "go test {dirs}", Verify: func(ctx context.Context, dir, cmd string) (string, int, error) {
		mu.Lock()
		ran = append(ran, cmd)
		mu.Unlock()
		return "", 0, nil
	}}, Deps{}, nil)
	if vr := s.verify(context.Background(), t.TempDir(), []string{"p01/**", "p02/p02.go"}); !vr.ok {
		t.Fatal(vr)
	}
	if vr := s.verify(context.Background(), t.TempDir(), nil); !vr.ok {
		t.Fatal(vr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ran) != 2 || ran[0] != "go test ./p01 ./p02" || ran[1] != "go test ./..." {
		t.Fatalf("commands run: %q", ran)
	}
}

// A worker in an isolated tree whose verifier looks at the whole repository is told why it may fail;
// one whose command names its own directories is not (it has no such excuse).
func TestAnIsolatedWorkersVerifyFailureSaysItsTreeHoldsOnlyItsOwnWork(t *testing.T) {
	m := &member{id: "be-1", tree: nil}
	s := New(Config{VerifyCmd: "go test ./..."}, Deps{}, nil)
	if h := s.isolatedVerifyHint(m); h != "" {
		t.Errorf("a worker in the shared checkout gets no hint: %q", h)
	}
	m.tree = &workspace.Tree{}
	if h := s.isolatedVerifyHint(m); !strings.Contains(h, "only your own changes") || !strings.Contains(h, "block the task") {
		t.Errorf("hint = %q", h)
	}
	scoped := New(Config{VerifyCmd: "go test {dirs}"}, Deps{}, nil)
	if h := scoped.isolatedVerifyHint(m); h != "" {
		t.Errorf("a scoped verifier has no such excuse: %q", h)
	}
	if h := s.isolatedVerifyHint(nil); h != "" {
		t.Errorf("no member, no hint: %q", h)
	}
}
