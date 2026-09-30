package gitx

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A hostile repository: everything in .git/config, .git/hooks and .gitattributes
// that can make git run a program is wired to `touch <markers>/<name>`. After any
// gitx operation the marker directory must still be empty. A "canary" run of the
// plain git binary first proves the fixture really is hostile (otherwise the
// test could pass because the fixture is broken).

type hostileRepo struct {
	dir     string // work tree
	markers string
	victim  string // directory core.worktree points at
	extra   string // config file pulled in through include.path
}

func quoteCfg(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// hostileConfig renders the malicious configuration. Each command marks itself.
func hostileConfig(markers, hooksDir, victim string, withWorktreeRedirect bool) string {
	bin := filepath.Join(filepath.Dir(markers), "bin")
	mk := func(name string) string { return "touch " + filepath.Join(markers, name) }
	var b strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }
	w("[core]")
	w("\tfsmonitor = %s", quoteCfg(mk("core.fsmonitor")+"; echo"))
	w("\tpager = %s", quoteCfg(mk("core.pager")+"; cat"))
	w("\teditor = %s", quoteCfg(mk("core.editor")))
	w("\tsshCommand = %s", quoteCfg(mk("core.sshCommand")))
	w("\taskPass = %s", quoteCfg(mk("core.askPass")))
	w("\thooksPath = %s", hooksDir)
	w("\tgitProxy = %s", quoteCfg(mk("core.gitProxy")))
	w("\tattributesFile = %s", filepath.Join(filepath.Dir(hooksDir), "global-attributes"))
	if withWorktreeRedirect {
		w("\tworktree = %s", victim)
	}
	w("[sequence]")
	w("\teditor = %s", quoteCfg(mk("sequence.editor")))
	w("[diff]")
	w("\texternal = %s", quoteCfg(mk("diff.external")))
	w("\ttool = evil")
	w("[difftool \"evil\"]")
	w("\tcmd = %s", quoteCfg(mk("difftool")))
	w("[merge]")
	w("\ttool = evil")
	w("\tverifySignatures = true")
	w("[mergetool \"evil\"]")
	w("\tcmd = %s", quoteCfg(mk("mergetool")))
	w("[filter \"evil\"]")
	w("\tclean = %s", quoteCfg(mk("filter.clean")+"; cat"))
	w("\tsmudge = %s", quoteCfg(mk("filter.smudge")+"; cat"))
	w("\tprocess = %s", quoteCfg(mk("filter.process")))
	w("\trequired = true")
	// clean/smudge without a process filter: a defined process would shadow them
	w("[filter \"plain\"]")
	w("\tclean = %s", quoteCfg(mk("filter.clean")+"; cat"))
	w("\tsmudge = %s", quoteCfg(mk("filter.smudge")+"; cat"))
	// a driver whose *name* is hostile: dots, a space and '=' must not break the override
	w("[filter \"we.ird na=me\"]")
	w("\tclean = %s", quoteCfg(mk("filter.weird")+"; cat"))
	w("\tsmudge = %s", quoteCfg(mk("filter.weird")+"; cat"))
	w("[merge \"evil\"]")
	w("\tdriver = %s", quoteCfg(mk("merge.driver")+"; exit 0"))
	w("[merge \"evil2\"]")
	w("\tdriver = %s", quoteCfg(mk("merge.driver2")))
	w("[diff \"evil\"]")
	w("\tcommand = %s", quoteCfg(mk("diff.command")))
	w("\ttextconv = %s", quoteCfg(mk("diff.textconv")+"; cat"))
	// gpg.program is an executable path, not a shell line: point it at scripts.
	w("[gpg]")
	w("\tprogram = %s", filepath.Join(bin, "gpg.program"))
	w("[gpg \"ssh\"]")
	w("\tprogram = %s", filepath.Join(bin, "gpg.ssh.program"))
	w("[commit]")
	w("\tgpgsign = true")
	w("[tag]")
	w("\tgpgsign = true")
	w("[log]")
	w("\tshowSignature = true")
	w("[credential]")
	w("\thelper = %s", quoteCfg("!"+mk("credential.helper")))
	w("[protocol \"ext\"]")
	w("\tallow = always")
	w("[uploadpack]")
	w("\tpackObjectsHook = %s", quoteCfg(mk("uploadpack.hook")))
	w("[gc]")
	w("\tauto = 1")
	w("\tautoDetach = true")
	w("[receive]")
	w("\tautogc = true")
	w("[alias]")
	w("\tstatus = %s", quoteCfg("!"+mk("alias.status")))
	w("\tmerge = %s", quoteCfg("!"+mk("alias.merge")))
	w("\tdiff = %s", quoteCfg("!"+mk("alias.diff")))
	w("[submodule]")
	w("\trecurse = true")
	w("[maintenance]")
	w("\tauto = true")
	w("[color]")
	w("\tui = always")
	w("[diff]")
	w("\tnoprefix = true")
	w("\tsubmodule = log")
	w("[include]")
	return b.String()
}

var hookNames = []string{
	"pre-commit", "prepare-commit-msg", "commit-msg", "post-commit", "pre-merge-commit", "post-merge", "pre-rebase",
	"post-rewrite", "post-checkout", "post-index-change", "reference-transaction", "fsmonitor-watchman", "pre-auto-gc",
	"applypatch-msg", "pre-applypatch", "post-applypatch", "pre-push", "push-to-checkout", "update", "pre-receive", "post-update",
}

// newHostileRepo builds a repository with real history, then arms it. The history
// is created before arming so that building the fixture itself runs nothing.
func newHostileRepo(t *testing.T, redirectWorktree bool) *hostileRepo {
	t.Helper()
	dir := newRepo(t)
	base := t.TempDir()
	h := &hostileRepo{dir: dir, markers: filepath.Join(base, "markers"), victim: filepath.Join(base, "victim")}
	for _, d := range []string{h.markers, h.victim} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(base, "bin")
	for _, name := range []string{"gpg.program", "gpg.ssh.program"} {
		writeFile(t, filepath.Join(bin, name), fmt.Sprintf("#!/bin/sh\ntouch %q\nexit 1\n", filepath.Join(h.markers, name)))
		if err := os.Chmod(filepath.Join(bin, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(h.victim, "precious.txt"), "do not touch\n")
	writeFile(t, filepath.Join(h.victim, "untracked-precious.txt"), "do not delete\n")

	// Attributes that select every hostile driver for every file, committed for real.
	writeFile(t, filepath.Join(dir, ".gitattributes"), "* filter=evil diff=evil merge=evil\n*.md filter=we.ird na=me merge=evil2\n")
	writeFile(t, filepath.Join(dir, "notes.md"), "notes\n")
	// data.cfg matches neither *.txt nor *.md, so it uses the "evil" filter driver,
	// which has a long-running process filter (the others only have clean/smudge)
	writeFile(t, filepath.Join(dir, "data.cfg"), "cfg\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "attributes")
	// A second branch that changes a.txt one way, so a merge has something to merge.
	rawGit(t, dir, "checkout", "-q", "-b", "side")
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha from side\n")
	writeFile(t, filepath.Join(dir, "side-only.txt"), "side\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "side change")
	rawGit(t, dir, "checkout", "-q", "main")

	hooks := filepath.Join(dir, ".git", "evilhooks")
	if err := os.MkdirAll(hooks, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range hookNames {
		script := fmt.Sprintf("#!/bin/sh\ntouch %q\nexit 0\n", filepath.Join(h.markers, "hook."+name))
		for _, p := range []string{filepath.Join(hooks, name), filepath.Join(dir, ".git", "hooks", name)} {
			if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	// attributes through the places only the repository owner controls
	writeFile(t, filepath.Join(dir, ".git", "info", "attributes"), "*.txt filter=plain merge=evil\n")
	writeFile(t, filepath.Join(filepath.Dir(hooks), "global-attributes"), "*.txt diff=evil\n")

	h.extra = filepath.Join(dir, ".git", "extra-config")
	writeFile(t, h.extra, "[core]\n\tfsmonitor = "+quoteCfg("touch "+filepath.Join(h.markers, "include.fsmonitor")+"; echo")+"\n")
	cfg := hostileConfig(h.markers, hooks, h.victim, redirectWorktree) + "\tpath = " + h.extra + "\n"
	f, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(cfg); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return h
}

func (h *hostileRepo) fired(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(h.markers)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out
}

func (h *hostileRepo) reset(t *testing.T) {
	t.Helper()
	for _, m := range h.fired(t) {
		os.Remove(filepath.Join(h.markers, m))
	}
}

// TestHostileFixtureIsReallyHostile is the canary: the same fixture, driven by
// plain git, fires a marker from every category that can be exercised without a
// terminal or a network (fsmonitor, filter drivers, hooks, merge driver, external
// diff and textconv, signing). If this ever stops holding, the "nothing fires"
// assertions below prove nothing. Each plain-git step disarms the blockers that
// earlier steps hit (a required filter that is not a real filter process aborts
// `add`), so later categories get to run.
func TestHostileFixtureIsReallyHostile(t *testing.T) {
	skipWithoutUnix(t)
	h := newHostileRepo(t, false)
	relief := []string{"-c", "core.fsmonitor=false", "-c", "filter.evil.process=", "-c", "filter.evil.required=false", "-c", "merge.verifySignatures=false", "-c", "log.showSignature=false"}
	git := func(args ...string) { rawGitMayFail(h.dir, args...) }
	writeFile(t, filepath.Join(h.dir, "a.txt"), "edited\n")
	writeFile(t, filepath.Join(h.dir, "data.cfg"), "edited cfg\n")
	git("status")                                                              // fsmonitor (via include), filter process
	git(append(relief, "add", "-A")...)                                        // clean filter
	git(append(relief, "commit", "-qm", "signed attempt")...)                  // hooks, gpg.program
	git(append(relief, "-c", "commit.gpgsign=false", "commit", "-qm", "x")...) // post-commit
	writeFile(t, filepath.Join(h.dir, "a.txt"), "edited again\n")
	git(append(relief, "-c", "commit.gpgsign=false", "commit", "-aqm", "y")...)
	git(append(relief, "-c", "commit.gpgsign=false", "merge", "--edit", "side")...) // merge driver, editor, merge hooks
	git(append(relief, "diff", "HEAD~2", "HEAD~1")...)                              // external diff
	git(append(relief, "show", "HEAD^1")...)                                        // textconv (HEAD is the merge)
	git(append(relief, "worktree", "add", filepath.Join(t.TempDir(), "wt"), "HEAD")...)
	fired := h.fired(t)
	t.Logf("plain git fired: %v", fired)
	have := map[string]bool{}
	for _, f := range fired {
		have[f] = true
	}
	for _, want := range []string{"include.fsmonitor", "filter.process", "filter.clean", "hook.pre-commit", "hook.post-commit",
		"merge.driver", "diff.command", "diff.textconv", "gpg.program"} {
		if !have[want] {
			t.Errorf("canary: plain git did not fire %s; the fixture no longer exercises that category", want)
		}
	}
}

// battery runs every gitx operation that touches a work tree, in a hostile
// repository, and reports which marker (if any) each one fired.
func battery(t *testing.T, h *hostileRepo, r *Repo) {
	t.Helper()
	ctx := ctxT(t)
	step := func(name string, fn func() error) {
		t.Helper()
		err := fn()
		if fired := h.fired(t); len(fired) > 0 {
			t.Errorf("%s ran repository-controlled code: %v (err=%v)", name, fired, err)
			h.reset(t)
		}
	}
	tolerate := func(err error) error { return nil } // operations may legitimately fail (a disabled driver conflicts)
	_ = tolerate

	writeFile(t, filepath.Join(h.dir, "a.txt"), "edited on main\n")
	writeFile(t, filepath.Join(h.dir, "data.cfg"), "cfg edited on main\n")
	writeFile(t, filepath.Join(h.dir, "new.txt"), "untracked\n")
	writeFile(t, filepath.Join(h.dir, "notes.md"), "notes edited\n")

	step("status", func() error { _, err := r.Status(ctx); return err })
	step("is-clean", func() error { _, err := r.IsClean(ctx); return err })
	step("diff", func() error { _, err := r.Diff(ctx, "HEAD", DiffOptions{}); return err })
	step("diff renames", func() error { _, err := r.Diff(ctx, "HEAD", DiffOptions{Renames: true}); return err })
	step("changed", func() error { _, err := r.ChangedPaths(ctx, "HEAD", ""); return err })
	step("snapshot", func() error { _, err := r.SnapshotTree(ctx); return err })
	step("log", func() error { _, err := r.Log(ctx, LogOptions{}); return err })
	step("show commit", func() error { _, err := r.Show(ctx, "HEAD", ""); return err })
	step("show file", func() error { _, err := r.Show(ctx, "HEAD", "a.txt"); return err })
	step("resolve", func() error { _, err := r.ResolveRef(ctx, "side"); return err })
	step("diff commits", func() error { _, err := r.Diff(ctx, "main", DiffOptions{To: "side"}); return err })
	step("commit", func() error { _, err := r.CommitAll(ctx, "hostile commit", Author{Name: "agent"}); return err })

	// worktrees: checkout runs smudge, and the tree is opened and committed in.
	wt := filepath.Join(t.TempDir(), "wt")
	step("worktree add", func() error {
		return r.WorktreeAdd(ctx, WorktreeAddOptions{Path: wt, Branch: "hostile/wt", Commit: "main"})
	})
	step("worktrees", func() error { _, err := r.Worktrees(ctx); return err })
	var wr *Repo
	step("open worktree", func() error {
		var err error
		wr, err = r.Reopen(ctx, wt)
		return err
	})
	if wr != nil {
		writeFile(t, filepath.Join(wt, "wt-file.txt"), "from worktree\n")
		writeFile(t, filepath.Join(wt, "a.txt"), "worktree edit\n")
		step("worktree status", func() error { _, err := wr.Status(ctx); return err })
		step("worktree diff", func() error { _, err := wr.Diff(ctx, "main", DiffOptions{}); return err })
		step("worktree commit", func() error { _, err := wr.CommitAll(ctx, "wt commit", Author{}); return err })
		step("worktree reset", func() error { return wr.ResetHard(ctx, "main") })
		step("worktree clean", func() error { return wr.CleanUntracked(ctx) })
	}

	// merges: a.txt changed on both sides (main edited, side edited): text merge conflict or driver conflict
	step("merge (conflicting)", func() error {
		res, err := r.Merge(ctx, MergeOptions{Ref: "side", NoFF: true, Author: Author{Name: "queue"}})
		if err == nil && res.Conflicted {
			return r.Abort(ctx)
		}
		return err
	})
	step("merge (clean)", func() error {
		wt2 := filepath.Join(t.TempDir(), "wt2")
		if err := r.WorktreeAdd(ctx, WorktreeAddOptions{Path: wt2, Detach: true, Commit: "main"}); err != nil {
			return err
		}
		w2, err := r.Reopen(ctx, wt2)
		if err != nil {
			return err
		}
		_, err = w2.Merge(ctx, MergeOptions{Ref: "side", NoFF: true})
		return err
	})
	step("rebase", func() error {
		wt3 := filepath.Join(t.TempDir(), "wt3")
		if err := r.WorktreeAdd(ctx, WorktreeAddOptions{Path: wt3, Detach: true, Commit: "main"}); err != nil {
			return err
		}
		w3, err := r.Reopen(ctx, wt3)
		if err != nil {
			return err
		}
		side, _ := r.ResolveRef(ctx, "side")
		mb, _ := r.MergeBase(ctx, "main", "side")
		res, err := w3.Rebase(ctx, RebaseOptions{Onto: "main", Upstream: mb, Branch: side})
		if err == nil && res.Conflicted {
			return w3.Abort(ctx)
		}
		return err
	})

	// patches
	step("apply check", func() error {
		d, err := r.Diff(ctx, "main~1", DiffOptions{To: "main"})
		if err != nil {
			return err
		}
		fresh := filepath.Join(t.TempDir(), "fresh")
		if err := r.WorktreeAdd(ctx, WorktreeAddOptions{Path: fresh, Detach: true, Commit: "main~1"}); err != nil {
			return err
		}
		fr, err := r.Reopen(ctx, fresh)
		if err != nil {
			return err
		}
		if err := fr.Apply(ctx, d.Patch, true); err != nil {
			return err
		}
		return fr.ApplyWith(ctx, d.Patch, ApplyOptions{ThreeWay: true, Index: true})
	})
	step("reset hard", func() error { return r.ResetHard(ctx, "main") })
	step("clean", func() error { return r.CleanUntracked(ctx) })

	// refs and branches
	step("branches", func() error { _, err := r.Branches(ctx, "hostile/"); return err })
	step("create/update/delete branch", func() error {
		head, _ := r.Head(ctx)
		if err := r.CreateBranch(ctx, "hostile/tmp", head); err != nil {
			return err
		}
		if err := r.UpdateBranch(ctx, "hostile/tmp", head, head, "test"); err != nil {
			return err
		}
		return r.DeleteBranch(ctx, "hostile/tmp")
	})
	step("worktree remove", func() error { return r.WorktreeRemove(ctx, wt, true) })
	step("generic Git", func() error { _, err := r.Git(ctx, "status", "--short"); return err })
	step("init", func() error {
		_, err := Init(ctx, filepath.Join(t.TempDir(), "shadow"), false, WithHermeticConfig())
		return err
	})
	step("merge-file", func() error {
		_, _, err := MergeFile(ctx, []byte("a\nb\n"), []byte("a\n"), []byte("a\nc\n"), "o", "b", "t", WithHermeticConfig())
		return err
	})
}

func TestHostileRepositoryRunsNothing(t *testing.T) {
	skipWithoutUnix(t)
	h := newHostileRepo(t, true)
	r := openRepo(t, h.dir)
	if r.Root() != h.dir {
		t.Fatalf("Root = %q, want %q (core.worktree must not redirect the work tree)", r.Root(), h.dir)
	}
	battery(t, h, r)
	if fired := h.fired(t); len(fired) > 0 {
		t.Fatalf("markers left: %v", fired)
	}
	// core.worktree pointed at the victim: nothing there may have been touched.
	if got := readFile(t, filepath.Join(h.victim, "precious.txt")); got != "do not touch\n" {
		t.Fatalf("victim file modified: %q", got)
	}
	if _, err := os.Stat(filepath.Join(h.victim, "untracked-precious.txt")); err != nil {
		t.Fatalf("victim file deleted through core.worktree: %v", err)
	}
	ents, _ := os.ReadDir(h.victim)
	if len(ents) != 2 {
		t.Fatalf("victim directory gained or lost files: %v", ents)
	}
}

func TestHostileGlobalConfigIsNeutralizedToo(t *testing.T) {
	skipWithoutUnix(t)
	// The same hostility, but arriving through ~/.gitconfig and a non-hermetic
	// handle: the user's own configuration is not trusted to run programs for
	// harness commits either (a global core.hooksPath would fire on every agent
	// commit; commit.gpgsign is the default in many setups and would run a
	// signing program - for a container with no signing key, that just fails).
	h := newHostileRepo(t, false)
	home := t.TempDir()
	global := hostileConfig(h.markers, filepath.Join(home, "hooks"), h.victim, false) // scripts already exist beside h.markers
	writeFile(t, filepath.Join(home, ".gitconfig"), global)
	if err := os.MkdirAll(filepath.Join(home, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range hookNames {
		script := fmt.Sprintf("#!/bin/sh\ntouch %q\n", filepath.Join(h.markers, "global-hook."+name))
		if err := os.WriteFile(filepath.Join(home, "hooks", name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	// strip the repository-level hostility so only the global one is in play
	cfgPath := filepath.Join(h.dir, ".git", "config")
	writeFile(t, cfgPath, "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = true\n")
	writeFile(t, filepath.Join(h.dir, ".git", "info", "attributes"), "")
	r, err := Open(h.dir) // deliberately not hermetic
	if err != nil {
		t.Fatal(err)
	}
	battery(t, h, r)
}

func TestTrustedFilterMayRunButOthersStillMayNot(t *testing.T) {
	skipWithoutUnix(t)
	dir := newRepo(t)
	markers := t.TempDir()
	writeFile(t, filepath.Join(dir, ".gitattributes"), "*.lfs filter=mine\n*.evil filter=evil\n")
	rawGit(t, dir, "add", "-A")
	rawGit(t, dir, "commit", "-qm", "attrs")
	cfg := fmt.Sprintf("[filter \"mine\"]\n\tclean = %s\n[filter \"evil\"]\n\tclean = %s\n",
		quoteCfg("touch "+filepath.Join(markers, "mine")+"; cat"), quoteCfg("touch "+filepath.Join(markers, "evil")+"; cat"))
	f, _ := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(cfg)
	f.Close()
	writeFile(t, filepath.Join(dir, "x.lfs"), "pointer\n")
	writeFile(t, filepath.Join(dir, "y.evil"), "payload\n")

	r := openRepo(t, dir, WithTrustedFilters("mine"))
	if _, err := r.SnapshotTree(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(markers)
	var got []string
	for _, e := range ents {
		got = append(got, e.Name())
	}
	if len(got) != 1 || got[0] != "mine" {
		t.Fatalf("fired %v, want exactly [mine]", got)
	}
}

func TestConfigScanLimits(t *testing.T) {
	dir := newRepo(t)
	var b strings.Builder
	for i := 0; i < maxDriverNames+10; i++ {
		fmt.Fprintf(&b, "[filter \"f%d\"]\n\tclean = cat\n", i)
	}
	f, _ := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString(b.String())
	f.Close()
	r := openRepo(t, dir)
	if _, err := r.Status(ctxT(t)); !isKind(err, KindUnsafe) {
		t.Fatalf("absurd driver count: want KindUnsafe, got %v", err)
	}
}

func isKind(err error, k Kind) bool { return KindOf(err) == k }
