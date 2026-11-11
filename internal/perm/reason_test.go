package perm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A model that invented a path ("/root/repo", "/workspace") is refused; what the refusal says is the only thing that tells
// it where it actually is, so it names the workspace. Without this the real-model pilots kept guessing: one model asked
// for nine invented paths in a row.
func TestRefusalOutsideTheWorkspaceNamesTheWorkspace(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		name string
		mode Mode
		req  rq
		ask  string // the start of the sentence that must precede the workspace
	}{
		{"read", ModeDefault, read("{out}/secret.txt"), "reads "},
		{"read in accept-edits", ModeAcceptEdits, read("{out}/secret.txt"), "reads "},
		{"write in accept-edits", ModeAcceptEdits, write("{out}/new.txt"), "writes "},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			e := f.engine(t, Config{Mode: c.mode})
			d := e.Check(context.Background(), f.request(c.req))
			if d.Allow {
				t.Fatalf("allowed: %s", d.Reason)
			}
			want := "outside the workspace (" + f.root + ")"
			if !strings.Contains(d.Reason, want) || !strings.Contains(d.Reason, c.ask) {
				t.Errorf("reason %q does not say %q", d.Reason, c.ask+"… "+want)
			}
		})
	}
}

// Without a configured root there is nothing to name, and the sentence stays as it was.
func TestRefusalWithoutAWorkspaceDoesNotInventOne(t *testing.T) {
	f := newFixture(t)
	e, err := NewEngine(Config{Home: f.home})
	if err != nil {
		t.Fatal(err)
	}
	d := e.Check(context.Background(), f.request(read("{out}/secret.txt")))
	if d.Allow || strings.Contains(d.Reason, "()") {
		t.Errorf("allow=%v reason=%q", d.Allow, d.Reason)
	}
}

// With a private scratch directory, $TMPDIR is known to the engine, is inside the
// workspace, and the refusal of any other place says where scratch files go.
func TestPrivateTmpdirIsKnownAndNamedInTheRefusal(t *testing.T) {
	f := newFixture(t)
	tmp := filepath.Join(f.root, "..", "scratch-"+filepath.Base(f.root))
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	tmp, _ = filepath.EvalSymlinks(tmp)
	e := f.engine(t, Config{Mode: ModeAcceptEdits, Tmp: tmp})
	for _, cmd := range []string{`echo x > "$TMPDIR/a"`, `echo x > ${TMPDIR}/a`, "echo x > " + tmp + "/a"} {
		if d := e.Check(context.Background(), f.request(bash(cmd))); !d.Allow {
			t.Errorf("%s: %s", cmd, d.Reason)
		}
	}
	d := e.Check(context.Background(), f.request(write("{out}/new.txt")))
	if d.Allow || !strings.Contains(d.Reason, "scratch files go in $TMPDIR ("+tmp+")") {
		t.Errorf("reason %q", d.Reason)
	}
	// A read has no use for a scratch directory: the hint is for writes.
	if d := e.Check(context.Background(), f.request(read("{out}/secret.txt"))); d.Allow || strings.Contains(d.Reason, "$TMPDIR") {
		t.Errorf("a read's reason %q", d.Reason)
	}
	// Without one, $TMPDIR is a variable the engine cannot know.
	e = f.engine(t, Config{Mode: ModeAcceptEdits})
	if d := e.Check(context.Background(), f.request(bash(`echo x > "$TMPDIR/a"`))); d.Allow {
		t.Errorf("allowed without a scratch directory: %s", d.Reason)
	}
}

// A read-only agent (plan mode) may write a scratch file in $TMPDIR, which is outside the project; a write in the workspace is
// still refused.
func TestPlanModeWritesOnlyToTheScratchDirectory(t *testing.T) {
	f := newFixture(t)
	tmp := filepath.Join(f.root, "..", "scratch-plan-"+filepath.Base(f.root))
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	tmp, _ = filepath.EvalSymlinks(tmp)
	e := f.engine(t, Config{Mode: ModePlan, Tmp: tmp})
	for _, cmd := range []string{`cat > "$TMPDIR/ci.yaml" <<'X'` + "\na: 1\nX", "echo x > " + tmp + "/a", `mkdir -p "$TMPDIR/out"`} {
		if d := e.Check(context.Background(), f.request(bash(cmd))); !d.Allow {
			t.Errorf("%s: %s", cmd, d.Reason)
		}
	}
	if d := e.Check(context.Background(), f.request(bash("echo x > notes.txt"))); d.Allow {
		t.Error("plan mode let a command write in the workspace")
	}
	// A link made in the scratch directory would let a write after it, judged before it exists, reach the project.
	for _, cmd := range []string{"mkdir build", `cp go.mod "$TMPDIR/x" && mkdir build`,
		`ln -s "$PWD" "$TMPDIR/link" && printf x > "$TMPDIR/link/main.go"`,
		`mv "$TMPDIR/a" "$TMPDIR/link" && printf x > "$TMPDIR/link/main.go"`,
		`cp -s "$TMPDIR/a" "$TMPDIR/l"`, `cp -l "$TMPDIR/a" "$TMPDIR/l"`, `cp -R "$TMPDIR/a" "$TMPDIR/c"`, `cp -a "$TMPDIR/a" "$TMPDIR/c"`,
		// A copy into a directory writes a child the command does not name, which may be a link into the project; touch
		// follows a link too. Only mkdir is let through.
		`cp "$TMPDIR/a" "$TMPDIR/b"`, `touch "$TMPDIR/a"`} {
		if d := e.Check(context.Background(), f.request(bash(cmd))); d.Allow {
			t.Errorf("plan mode allowed %s", cmd)
		}
	}
}

// A copy into a scratch directory whose child is a link to a project file would overwrite that file: it is refused.
func TestPlanModeCopyIntoALinkToTheProjectIsRefused(t *testing.T) {
	f := newFixture(t)
	tmp := filepath.Join(f.root, "..", "scratch-cp-"+filepath.Base(f.root))
	if err := os.MkdirAll(filepath.Join(tmp, "out"), 0o700); err != nil {
		t.Fatal(err)
	}
	tmp, _ = filepath.EvalSymlinks(tmp)
	if err := os.WriteFile(filepath.Join(f.root, "main.go"), []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.root, "main.go"), filepath.Join(tmp, "out", "main.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmp, "input"), 0o700); err != nil {
		t.Fatal(err)
	}
	e := f.engine(t, Config{Mode: ModePlan, Tmp: tmp})
	for _, cmd := range []string{`cp "$TMPDIR/input/main.go" "$TMPDIR/out"`, `echo x > "$TMPDIR/out/main.go"`} {
		if d := e.Check(context.Background(), f.request(bash(cmd))); d.Allow {
			t.Errorf("plan mode allowed %s, which writes the project's main.go through a link", cmd)
		}
	}
}

// A path in $TMPDIR that holds the project is not scratch either: writing there could reach the project's files.
func TestPlanModeScratchWriteToAPathThatHoldsTheProjectIsRefused(t *testing.T) {
	f := newFixture(t)
	root, _ := filepath.EvalSymlinks(f.root)
	parent := filepath.Dir(filepath.Dir(root))
	e := f.engine(t, Config{Mode: ModePlan, Tmp: parent})
	if d := e.Check(context.Background(), f.request(bash("touch "+filepath.Dir(root)))); d.Allow {
		t.Error("plan mode allowed a write to the directory that holds the project")
	}
}

// A scratch directory that holds the project is no scratch directory: plan mode does not let a command write in the
// project because its path is under the session's $TMPDIR.
func TestPlanModeScratchThatHoldsTheProjectIsNotScratch(t *testing.T) {
	f := newFixture(t)
	parent, _ := filepath.EvalSymlinks(filepath.Dir(f.root))
	e := f.engine(t, Config{Mode: ModePlan, Tmp: parent})
	for _, cmd := range []string{"echo x > notes.txt", "mkdir build", "echo x > " + filepath.Join(f.root, "notes.txt")} {
		if d := e.Check(context.Background(), f.request(bash(cmd))); d.Allow {
			t.Errorf("plan mode allowed %s with the project inside $TMPDIR", cmd)
		}
	}
	if d := e.Check(context.Background(), f.request(bash("echo x > "+filepath.Join(parent, "elsewhere.txt")))); !d.Allow {
		t.Errorf("a write in $TMPDIR outside the project: %s", d.Reason)
	}
}

// A project at $TMPDIR itself has no scratch directory apart from it.
func TestPlanModeScratchThatIsTheProjectIsNotScratch(t *testing.T) {
	f := newFixture(t)
	root, _ := filepath.EvalSymlinks(f.root)
	e := f.engine(t, Config{Mode: ModePlan, Tmp: root})
	for _, cmd := range []string{"echo x > notes.txt", "mkdir build", `echo x > "$TMPDIR/notes.txt"`, `mkdir -p "$TMPDIR/out"`} {
		if d := e.Check(context.Background(), f.request(bash(cmd))); d.Allow {
			t.Errorf("plan mode allowed %s with the project at $TMPDIR", cmd)
		}
	}
}

// A read-only role's checks include a build and a type check: a pool member asked to check the combined result was refused
// `go build -o "$TMPDIR/app" ./cmd/app`.
func TestReadOnlyChecksIncludeABuildToTheScratchDirectory(t *testing.T) {
	f := newFixture(t)
	tmp := filepath.Join(f.root, "..", "scratch-build-"+filepath.Base(f.root))
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	tmp, _ = filepath.EvalSymlinks(tmp)
	po := f.engine(t, Config{Mode: ModeBypass, Tmp: tmp}).PlanOnly(ReadOnlyAllow...)
	for _, cmd := range []string{`go build -o "$TMPDIR/app" ./cmd/app`, "pnpm exec tsc --noEmit", "pnpm exec eslint src"} {
		if d := po.Check(context.Background(), f.request(bash(cmd))); !d.Allow {
			t.Errorf("%s: %s", cmd, d.Reason)
		}
	}
	if d := po.Check(context.Background(), f.request(bash("go build -o notes.bin ./cmd/app"))); d.Allow {
		t.Error("a build that writes its named output into the workspace was allowed")
	}
	// A checker's writing options take it off its rule; a project's lint script may take --fix.
	for _, cmd := range []string{"pnpm exec tsc --noEmit --generateTrace trace", "pnpm exec eslint --fix src",
		"npm run lint -- --fix", "pnpm lint --fix", "pnpm exec eslint --cache-location package.json src",
		"pnpm exec eslint --cache-file=package.json src"} {
		if d := po.Check(context.Background(), f.request(bash(cmd))); d.Allow {
			t.Errorf("a read-only check allowed %s: %s", cmd, d.Reason)
		}
	}
}
