package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/gitx"
)

// The workspace layer drives git constantly on behalf of agents. These tests run
// complete scenarios against a repository (and agents) that try to make git run
// programs: hooks, fsmonitor, filter/merge/diff drivers, signing, editors,
// credential helpers, includes, a core.worktree redirect. Every hostile command
// touches a marker file; the marker directory must stay empty. internal/gitx has
// the canary proving plain git fires these; here the point is that no path through
// Manager, Tree, Queue, Prune or copy mode reaches git without the hardening.

type hostileProject struct {
	dir, markers, victim, bin string
}

func q(s string) string { return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"` }

func (h *hostileProject) cmd(name string) string { return "touch " + filepath.Join(h.markers, name) }

func (h *hostileProject) config(hooksDir string, redirect bool) string {
	var b strings.Builder
	w := func(f string, a ...any) { fmt.Fprintf(&b, f+"\n", a...) }
	w("[core]")
	w("\tfsmonitor = %s", q(h.cmd("core.fsmonitor")+"; echo"))
	w("\teditor = %s", q(h.cmd("core.editor")))
	w("\tsshCommand = %s", q(h.cmd("core.sshCommand")))
	w("\taskPass = %s", q(h.cmd("core.askPass")))
	w("\thooksPath = %s", hooksDir)
	if redirect {
		w("\tworktree = %s", h.victim)
	}
	w("[sequence]")
	w("\teditor = %s", q(h.cmd("sequence.editor")))
	w("[diff]")
	w("\texternal = %s", q(h.cmd("diff.external")))
	w("[filter \"evil\"]")
	w("\tclean = %s", q(h.cmd("filter.clean")+"; cat"))
	w("\tsmudge = %s", q(h.cmd("filter.smudge")+"; cat"))
	w("\tprocess = %s", q(h.cmd("filter.process")))
	w("\trequired = true")
	w("[filter \"plain\"]")
	w("\tclean = %s", q(h.cmd("filter.plain.clean")+"; cat"))
	w("\tsmudge = %s", q(h.cmd("filter.plain.smudge")+"; cat"))
	w("[merge \"evil\"]")
	w("\tdriver = %s", q(h.cmd("merge.driver")+"; exit 0"))
	w("[diff \"evil\"]")
	w("\tcommand = %s", q(h.cmd("diff.command")))
	w("\ttextconv = %s", q(h.cmd("diff.textconv")+"; cat"))
	w("[gpg]")
	w("\tprogram = %s", filepath.Join(h.bin, "gpg"))
	w("[commit]")
	w("\tgpgsign = true")
	w("[log]")
	w("\tshowSignature = true")
	w("[credential]")
	w("\thelper = %s", q("!"+h.cmd("credential.helper")))
	w("[gc]")
	w("\tauto = 1")
	w("\tautoDetach = true")
	w("[alias]")
	w("\tstatus = %s", q("!"+h.cmd("alias.status")))
	w("[submodule]")
	w("\trecurse = true")
	w("[color]")
	w("\tui = always")
	w("[include]")
	w("\tpath = %s", filepath.Join(h.dir, ".git", "extra-config"))
	return b.String()
}

var hostileHooks = []string{"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit", "pre-merge-commit", "post-merge", "pre-rebase",
	"post-rewrite", "post-checkout", "post-index-change", "reference-transaction", "fsmonitor-watchman", "pre-auto-gc", "applypatch-msg", "update"}

func newHostileProject(t *testing.T, redirectWorktree bool) *hostileProject {
	t.Helper()
	skipWithoutUnix(t)
	dir := newRepo(t)
	base := t.TempDir()
	h := &hostileProject{dir: dir, markers: filepath.Join(base, "markers"), victim: filepath.Join(base, "victim"), bin: filepath.Join(base, "bin")}
	for _, d := range []string{h.markers, h.victim, h.bin} {
		must(t, os.MkdirAll(d, 0o755))
	}
	writeFile(t, filepath.Join(h.victim, "precious.txt"), "do not touch\n")
	writeFile(t, filepath.Join(h.bin, "gpg"), "#!/bin/sh\n"+h.cmd("gpg.program")+"\nexit 1\n")
	must(t, os.Chmod(filepath.Join(h.bin, "gpg"), 0o755))

	// Real history first (nothing hostile is configured yet): attributes select every
	// driver, and a side branch gives merges something to do.
	writeFile(t, filepath.Join(dir, ".gitattributes"), "* filter=evil diff=evil merge=evil\n*.go filter=plain\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "attributes")

	hooks := filepath.Join(dir, ".git", "evilhooks")
	must(t, os.MkdirAll(hooks, 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755))
	for _, name := range hostileHooks {
		script := fmt.Sprintf("#!/bin/sh\ntouch %q\nexit 0\n", filepath.Join(h.markers, "hook."+name))
		for _, p := range []string{filepath.Join(hooks, name), filepath.Join(dir, ".git", "hooks", name)} {
			must(t, os.WriteFile(p, []byte(script), 0o755))
		}
	}
	writeFile(t, filepath.Join(dir, ".git", "info", "attributes"), "*.md filter=plain merge=evil\n")
	writeFile(t, filepath.Join(dir, ".git", "extra-config"), "[core]\n\tfsmonitor = "+q(h.cmd("include.fsmonitor")+"; echo")+"\n")
	f, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	must(t, err)
	_, err = f.WriteString(h.config(hooks, redirectWorktree))
	must(t, err)
	must(t, f.Close())
	return h
}

func (h *hostileProject) fired(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(h.markers)
	must(t, err)
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

// none asserts nothing fired since the last call.
func (h *hostileProject) none(t *testing.T, phase string) {
	t.Helper()
	if fired := h.fired(t); len(fired) > 0 {
		t.Errorf("%s ran repository-controlled code: %v", phase, fired)
		for _, f := range fired {
			os.Remove(filepath.Join(h.markers, f))
		}
	}
}

func (h *hostileProject) victimIntact(t *testing.T) {
	t.Helper()
	if readFile(t, filepath.Join(h.victim, "precious.txt")) != "do not touch\n" {
		t.Fatal("the victim directory was modified")
	}
	if ents, _ := os.ReadDir(h.victim); len(ents) != 1 {
		t.Fatalf("the victim directory gained or lost files: %v", ents)
	}
}

func TestHostileRepositoryThroughTheWholeWorkspaceLayer(t *testing.T) {
	h := newHostileProject(t, true)
	repo := openRepo(t, h.dir)
	m := newManager(t, repo)
	m.Snapshot = true // also exercises the snapshot commit path
	writeFile(t, filepath.Join(h.dir, "notes.md"), "user notes in progress\n")
	q1 := mustQueue(t, m, QueueOptions{VerifyCmd: "true"})
	h.none(t, "NewQueue")

	a := mustCreate(t, m, "a", CreateOptions{Base: q1.Tip()})
	b := mustCreate(t, m, "b", CreateOptions{Base: q1.Tip()})
	sp := mustCreate(t, m, "sp", CreateOptions{Base: q1.Tip(), Sparse: []string{"internal/core"}})
	h.none(t, "Create (checkout runs smudge filters)")

	edit(t, a, "internal/core/core.go", strings.Replace(coreV1, "return 1", "return 100", 1))
	edit(t, a, "docs/guide.md", "# guide\n\nedited by a\nline two\nline three\nline four\nline five\n")
	edit(t, a, "data/notes.cfg", "cfg\n")
	edit(t, b, "internal/core/core.go", strings.Replace(coreV1, "return 1", "return 200", 1))
	edit(t, sp, "internal/core/extra.go", "package core\n")

	for _, tr := range []*Tree{a, b, sp} {
		if _, err := tr.Changed(tctx(t)); err != nil {
			t.Fatal(err)
		}
		if _, err := tr.Diff(tctx(t)); err != nil {
			t.Fatal(err)
		}
		if _, err := tr.Dirty(tctx(t)); err != nil {
			t.Fatal(err)
		}
	}
	h.none(t, "Changed/Diff/Dirty")

	r := mustSubmit(t, q1, Submission{Tree: a, Task: "a"}) // Commit + merge + verify
	if !r.Merged() {
		t.Fatalf("a: %+v", r)
	}
	h.none(t, "Commit and merge")
	r = mustSubmit(t, q1, Submission{Tree: b, Task: "b"}) // both edited core.go: a real conflict; the merge driver must not decide it
	if r.Outcome != OutcomeConflict {
		t.Fatalf("b must conflict (the hostile merge driver must not run, and its would-be verdict must not win): %+v", r)
	}
	h.none(t, "conflict handling")
	if c, err := b.Update(tctx(t), q1.Tip()); err != nil || c == nil {
		t.Fatalf("Update: %+v %v", c, err)
	}
	must(t, b.AbortUpdate(tctx(t)))
	h.none(t, "Update/AbortUpdate")
	if r := mustSubmit(t, q1, Submission{Tree: sp, Task: "sp"}); !r.Merged() {
		t.Fatalf("sp: %+v", r)
	}
	if _, err := q1.Finish(tctx(t)); err != nil {
		t.Fatal(err)
	}
	h.none(t, "Finish")
	if err := b.Reset(tctx(t)); err != nil {
		t.Fatal(err)
	}
	h.none(t, "Reset")
	// removing and pruning
	must(t, a.Remove(tctx(t), false))
	must(t, sp.Remove(tctx(t), false))
	orphan(t, b)
	orphan(t, q1.tree)
	fresh := &Manager{Repo: repo, Dir: m.Dir, Prefix: "sleipnir/s1", Clock: testClock()}
	if _, err := fresh.Prune(tctx(t), PruneOptions{Salvage: true, Force: true}); err != nil {
		t.Fatal(err)
	}
	h.none(t, "Remove/Prune")
	h.victimIntact(t)

	// the user's own checkout was never disturbed by any of it
	if st, _ := repo.Status(tctx(t)); len(st.Staged) != 0 || len(st.Unstaged) != 0 {
		t.Fatalf("the user's checkout changed: %+v", st)
	}
	if !exists(filepath.Join(h.dir, "notes.md")) {
		t.Fatal("the user's uncommitted file vanished")
	}
	// FastForward on a clean checkout runs git merge in the user's repository: also silent
	must(t, os.Remove(filepath.Join(h.dir, "notes.md")))
	m2 := &Manager{Repo: repo, Dir: filepath.Join(t.TempDir(), "t"), Prefix: "sleipnir/s7", Clock: testClock()}
	q2 := mustQueue(t, m2, QueueOptions{})
	x := mustCreate(t, m2, "x", CreateOptions{Base: q2.Tip()})
	edit(t, x, "README.md", "# fast-forwarded\n")
	if r := mustSubmit(t, q2, Submission{Tree: x}); !r.Merged() {
		t.Fatalf("x: %+v", r)
	}
	if _, err := q2.FastForward(tctx(t)); err != nil {
		t.Fatalf("FastForward: %v", err)
	}
	h.none(t, "FastForward")
	h.victimIntact(t)
}

func TestAgentsCannotPlantCodeForTheHarnessToRun(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	base := t.TempDir()
	markers := filepath.Join(base, "markers")
	must(t, os.MkdirAll(markers, 0o755))
	mark := func(name string) string { return "touch " + filepath.Join(markers, name) }
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	q1 := mustQueue(t, m, QueueOptions{VerifyCmd: "true"})
	a := mustCreate(t, m, "a", CreateOptions{Base: q1.Tip()})
	b := mustCreate(t, m, "b", CreateOptions{Base: q1.Tip()})

	// The agent has a shell. Everything below is what a hostile one would leave
	// around for the harness's next git command.
	// 1. into the repository configuration shared by every worktree
	f, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	must(t, err)
	fmt.Fprintf(f, "[filter \"planted\"]\n\tclean = %s\n\tsmudge = %s\n\trequired = true\n", q(mark("planted.clean")+"; cat"), q(mark("planted.smudge")+"; cat"))
	fmt.Fprintf(f, "[merge \"planted\"]\n\tdriver = %s\n", q(mark("planted.merge")+"; exit 0"))
	fmt.Fprintf(f, "[core]\n\tfsmonitor = %s\n\thooksPath = %s\n", q(mark("planted.fsmonitor")+"; echo"), filepath.Join(dir, ".git", "planted-hooks"))
	fmt.Fprintf(f, "[commit]\n\tgpgsign = true\n")
	must(t, f.Close())
	// 2. hooks, in the shared hooks directory and the one core.hooksPath now names
	for _, hd := range []string{filepath.Join(dir, ".git", "hooks"), filepath.Join(dir, ".git", "planted-hooks")} {
		must(t, os.MkdirAll(hd, 0o755))
		for _, hook := range []string{"pre-commit", "post-commit", "post-merge", "post-checkout", "reference-transaction", "commit-msg"} {
			must(t, os.WriteFile(filepath.Join(hd, hook), []byte("#!/bin/sh\n"+mark("planted.hook."+hook)+"\n"), 0o755))
		}
	}
	// 3. attributes in its own tree and in the private admin directory
	edit(t, a, ".gitattributes", "* filter=planted merge=planted\n")
	writeFile(t, filepath.Join(a.repo.GitDir(), "info", "attributes"), "* filter=planted merge=planted\n")
	// 4. work the harness must now diff, commit and merge
	edit(t, a, "internal/util/util.go", "package util\n\nfunc Add(a, b int) int { return a + b + 1 }\n")
	edit(t, a, "planted-file.txt", "x\n")
	edit(t, b, "internal/util/util.go", "package util\n\nfunc Add(a, b int) int { return a + b + 2 }\n")

	if _, err := a.Changed(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Diff(tctx(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Commit(tctx(t), "planted"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	r1 := mustSubmit(t, q1, Submission{Tree: a})
	if !r1.Merged() {
		t.Fatalf("a: %+v", r1)
	}
	r2 := mustSubmit(t, q1, Submission{Tree: b}) // conflicts on util.go: the planted merge driver must not "resolve" it
	if r2.Outcome != OutcomeConflict {
		t.Fatalf("b: %+v", r2)
	}
	if err := a.Reset(tctx(t)); err != nil {
		t.Fatal(err)
	}
	must(t, a.Remove(tctx(t), true))
	must(t, q1.Close(tctx(t)))
	ents, _ := os.ReadDir(markers)
	if len(ents) != 0 {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("code planted by an agent ran in the harness: %v", names)
	}
}

func TestAnAgentRewritingItsDotGitCannotRedirectTheHarness(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	repo := openRepo(t, dir)
	m := newManager(t, repo)
	q1 := mustQueue(t, m, QueueOptions{})
	a := mustCreate(t, m, "a", CreateOptions{Base: q1.Tip()})
	mainHead, _ := repo.Head(tctx(t))
	// the agent points its tree at the user's own repository
	writeFile(t, filepath.Join(a.Path, ".git"), "gitdir: "+filepath.Join(dir, ".git")+"\n")
	edit(t, a, "agent.txt", "agent work\n")
	sha, err := a.Commit(tctx(t), "agent work")
	if err != nil || sha == "" {
		t.Fatalf("Commit: %q, %v", sha, err)
	}
	if head, _ := repo.Head(tctx(t)); head != mainHead {
		t.Fatal("the user's branch moved: the agent's commit was recorded in the wrong repository")
	}
	if br, _ := repo.BranchSHA(tctx(t), a.Branch); br != sha {
		t.Fatal("the commit did not land on the agent's own branch")
	}
	if st, _ := repo.Status(tctx(t)); !st.Clean() {
		t.Fatalf("the user's checkout was touched: %+v", st)
	}
	// and the tree can still be removed, and only the tree
	must(t, a.Remove(tctx(t), true))
	if !exists(dir) || !exists(filepath.Join(dir, "README.md")) {
		t.Fatal("Remove damaged the user's checkout")
	}

	// Removal does not depend on the agent leaving its .git file alone: a deleted
	// file, a replacement directory holding a hostile repository, and a worktree
	// somebody locked all still end with the tree gone and nothing else touched.
	markers := t.TempDir()
	for name, tamper := range map[string]func(tr *Tree){
		"deleted": func(tr *Tree) { must(t, os.Remove(filepath.Join(tr.Path, ".git"))) },
		"replaced by a directory": func(tr *Tree) {
			must(t, os.Remove(filepath.Join(tr.Path, ".git")))
			must(t, os.MkdirAll(filepath.Join(tr.Path, ".git", "hooks"), 0o755))
			writeFile(t, filepath.Join(tr.Path, ".git", "config"), "[core]\n\tfsmonitor = \"touch "+filepath.Join(markers, "fsmonitor")+"; echo\"\n")
		},
		"locked by someone": func(tr *Tree) { rawGit(t, dir, "worktree", "lock", tr.Path) },
	} {
		tr := mustCreate(t, m, "tamper-"+strings.Fields(name)[0], CreateOptions{Base: q1.Tip()})
		tamper(tr)
		if err := tr.Remove(tctx(t), true); err != nil {
			t.Fatalf("%s: Remove(force): %v", name, err)
		}
		if exists(tr.Path) {
			t.Fatalf("%s: directory still there", name)
		}
		if wts, _ := repo.Worktrees(tctx(t)); len(wts) != 2 { // the main tree and the queue's integration tree
			t.Fatalf("%s: registrations left: %+v", name, wts)
		}
	}
	if ents, _ := os.ReadDir(markers); len(ents) != 0 {
		t.Fatalf("a planted repository ran code during removal: %v", ents)
	}
	if !exists(filepath.Join(dir, "README.md")) {
		t.Fatal("the user's checkout was damaged")
	}
}

func TestHostileSourceDirectoryInCopyMode(t *testing.T) {
	// A directory prepared by someone else, snapshotted in copy mode: its own
	// .gitattributes/.gitignore are data; the private repository has no drivers, and
	// a global hostile configuration is neutralized as everywhere else.
	skipWithoutUnix(t)
	home := isolateHome(t)
	base := t.TempDir()
	markers := filepath.Join(base, "markers")
	must(t, os.MkdirAll(markers, 0o755))
	mark := func(n string) string { return "touch " + filepath.Join(markers, n) }
	writeFile(t, filepath.Join(home, ".gitconfig"),
		"[filter \"evil\"]\n\tclean = "+q(mark("global.clean")+"; cat")+"\n\tsmudge = "+q(mark("global.smudge")+"; cat")+"\n[commit]\n\tgpgsign = true\n[core]\n\tfsmonitor = "+q(mark("global.fsmonitor")+"; echo")+"\n")
	src := filepath.Join(base, "src")
	writeFile(t, filepath.Join(src, ".gitattributes"), "* filter=evil\n")
	writeFile(t, filepath.Join(src, "a.txt"), "a\n")
	writeFile(t, filepath.Join(src, ".git", "config"), "[core]\n\tfsmonitor = "+q(mark("src.fsmonitor")+"; echo")+"\n") // a decoy .git directory in the source
	m := &Manager{Source: src, Dir: filepath.Join(base, "trees"), Prefix: "sleipnir/s1", Clock: testClock(), Mode: ModeCopy}
	q1 := mustQueue(t, m, QueueOptions{})
	a := mustCreate(t, m, "a", CreateOptions{Base: q1.Tip()})
	edit(t, a, "a.txt", "a edited\n")
	if r := mustSubmit(t, q1, Submission{Tree: a}); !r.Merged() {
		t.Fatalf("%+v", r)
	}
	if _, err := q1.Finish(tctx(t)); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(markers)
	if len(ents) != 0 {
		t.Fatalf("hostile configuration ran: %v", ents)
	}
	if exists(filepath.Join(a.Path, ".git", "config")) {
		if _, err := os.ReadFile(filepath.Join(a.Path, ".git", "config")); err == nil {
			t.Fatal("the decoy .git directory from the source was copied into a tree")
		}
	}
}

// keep gitx imported for hostile helpers that grow later
var _ = gitx.KindOther
