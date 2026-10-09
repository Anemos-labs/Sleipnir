package taskgen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// FixtureSpec is the task.json of a benchmark fixture, a directory
//
//	<id>/task.json   what the agent is asked and how the result is judged
//	<id>/start/      the repository the agent starts in
//	<id>/hidden/     files written over it only when the result is verified (the hidden tests)
//	<id>/solution/   a reference solution: the whole files that differ from start/
//
// A fixture is authored, not mined: it exists to measure something a mined commit cannot (a language the project's own history
// is not written in, a task built from a specification, a bug planted where models tend to miss it).
type FixtureSpec struct {
	ID         string    `json:"id"`
	Kind       string    `json:"kind"` // fix | feature | refactor | greenfield
	Lang       string    `json:"lang"`
	Difficulty string    `json:"difficulty"` // easy | medium | hard
	Prompt     string    `json:"prompt"`
	Verify     string    `json:"verify"` // a shell command run in the repository root once the hidden files are in place
	TimeoutS   int       `json:"timeout_s,omitempty"`
	Setup      []string  `json:"setup,omitempty"`
	Protected  []string  `json:"protected,omitempty"`
	Team       rl.Team   `json:"team"`
	Tags       []string  `json:"tags,omitempty"`
	Budget     rl.Budget `json:"budget,omitempty"`
}

// FixtureOptions configures FromFixture.
type FixtureOptions struct {
	// RepoRoot is the directory the fixtures' repositories are written to: <RepoRoot>/<id>, a git repository with one
	// commit that holds start/. The commit's identity and date are fixed, so its hash depends on the fixture alone.
	RepoRoot string
	// RepoPathPrefix is how a task names its repository: <RepoPathPrefix>/<id>, resolved by a rollout against its
	// repository base (the directory it runs in). Relative, so that a task file is not tied to the machine that made it.
	// Empty means RepoRoot/<id> as it is.
	RepoPathPrefix string
	// HiddenBlobs stores the hidden files (tasks then carry "blob:<hash>"); nil inlines them as text.
	HiddenBlobs events.Blobs
	// GoldBlobs receives the reference solution as a patch; its hash is recorded in the task's meta.
	GoldBlobs events.Blobs
	// Git runs git; nil makes one of its own.
	Git *env.Git
}

// Limits on what a fixture may hold: a fixture is small by design, and a runaway one (a stray build directory) is an error.
const (
	maxFixtureFiles = 400
	maxFixtureFile  = 1 << 20
	maxFixtureTotal = 8 << 20
)

// FromFixture turns a fixture directory into a task: it writes the start repository, stores the hidden files and the
// reference solution, and describes the verifier. It does not run anything: `rl tasks check` proves the task sound.
func FromFixture(ctx context.Context, dir string, opts FixtureOptions) (*rl.Task, error) {
	spec, err := LoadFixtureSpec(dir)
	if err != nil {
		return nil, err
	}
	if opts.RepoRoot == "" {
		return nil, errors.New("fixture: RepoRoot is required")
	}
	start, err := readTree(filepath.Join(dir, "start"))
	if err != nil {
		return nil, fmt.Errorf("fixture %s: start/: %w", spec.ID, err)
	}
	hidden, err := readTree(filepath.Join(dir, "hidden"))
	if err != nil {
		return nil, fmt.Errorf("fixture %s: hidden/: %w", spec.ID, err)
	}
	solution, err := readTree(filepath.Join(dir, "solution"))
	if err != nil {
		return nil, fmt.Errorf("fixture %s: solution/: %w", spec.ID, err)
	}
	if len(start) == 0 || len(hidden) == 0 || len(solution) == 0 {
		return nil, fmt.Errorf("fixture %s: start/, hidden/ and solution/ must each hold files", spec.ID)
	}

	g := opts.Git
	if g == nil {
		if g, err = env.NewGit(env.GitOptions{}); err != nil {
			return nil, err
		}
		defer g.Close()
	}
	repoDir := filepath.Join(opts.RepoRoot, spec.ID)
	commit, err := writeFixtureRepo(ctx, g, repoDir, start)
	if err != nil {
		return nil, fmt.Errorf("fixture %s: %w", spec.ID, err)
	}
	gold, err := fixturePatch(ctx, g, start, solution)
	if err != nil {
		return nil, fmt.Errorf("fixture %s: %w", spec.ID, err)
	}
	if len(gold) == 0 {
		return nil, fmt.Errorf("fixture %s: the solution is the start", spec.ID)
	}

	hiddenRefs := map[string]string{}
	hh := sha256.New()
	for _, p := range sortedPaths(hidden) {
		data := hidden[p]
		switch {
		case opts.HiddenBlobs != nil:
			h, err := opts.HiddenBlobs.Put(data)
			if err != nil {
				return nil, err
			}
			hiddenRefs[p] = "blob:" + string(h)
		case utf8.Valid(data) && bytes.IndexByte(data, 0) < 0:
			hiddenRefs[p] = "text:" + string(data)
		default:
			return nil, fmt.Errorf("fixture %s: hidden file %s is binary and there is no blob store to hold it", spec.ID, p)
		}
		hh.Write([]byte(p))
		hh.Write([]byte{0})
		hh.Write(data)
		hh.Write([]byte{0})
	}
	goldHash := sha256.Sum256(gold)
	meta := Meta{
		Commit: commit, Generator: "taskgen/fixture", Lang: spec.Lang, Files: sortedPaths(solution),
		GoldHash: hex.EncodeToString(goldHash[:]), HiddenHash: hex.EncodeToString(hh.Sum(nil)),
	}
	if opts.GoldBlobs != nil {
		h, err := opts.GoldBlobs.Put(gold)
		if err != nil {
			return nil, err
		}
		meta.GoldBlob = string(h)
	}
	metaRaw, _ := json.Marshal(meta)

	repoPath := repoDir
	if opts.RepoPathPrefix != "" {
		repoPath = path.Join(filepath.ToSlash(opts.RepoPathPrefix), spec.ID)
	}
	timeout := spec.TimeoutS
	if timeout == 0 {
		timeout = 300
	}
	protected := append([]string(nil), spec.Protected...)
	for _, p := range sortedPaths(hidden) {
		protected = append(protected, escapeGlob(p))
	}
	kind := rl.TaskFeature
	switch spec.Kind {
	case "fix":
		kind = rl.TaskFix
	case "refactor":
		kind = rl.TaskRefactor
	}
	tags := dedupe(append([]string{"fixture", spec.Lang, spec.Kind, spec.Difficulty}, spec.Tags...))
	return &rl.Task{
		ID: spec.ID, Kind: kind,
		Repo:   rl.RepoSpec{Path: repoPath, Commit: commit},
		Setup:  spec.Setup,
		Prompt: spec.Prompt,
		Team:   spec.Team,
		Verifier: rl.Verifier{
			Cmd: spec.Verify, TimeoutS: timeout, Pass: "exit0", Hidden: hiddenRefs, Protected: dedupe(protected),
		},
		Budget: spec.Budget,
		Tags:   tags,
		Meta:   metaRaw,
	}, nil
}

// LoadFixtureSpec reads and checks <dir>/task.json. Unknown fields are errors: a misspelt key is a setting that silently does nothing.
func LoadFixtureSpec(dir string) (FixtureSpec, error) {
	var spec FixtureSpec
	b, err := os.ReadFile(filepath.Join(dir, "task.json"))
	if err != nil {
		return spec, fmt.Errorf("fixture %s: %w", filepath.Base(dir), err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&spec); err != nil {
		return spec, fmt.Errorf("fixture %s: task.json: %w", filepath.Base(dir), err)
	}
	if err := spec.validate(filepath.Base(dir)); err != nil {
		return spec, err
	}
	return spec, nil
}

var fixtureKinds = map[string]bool{"fix": true, "feature": true, "refactor": true, "greenfield": true}

func (s FixtureSpec) validate(dirName string) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("fixture %s: task.json: %s", dirName, fmt.Sprintf(format, a...))
	}
	switch {
	case s.ID == "" || !safeID(s.ID):
		return bad("id %q must be letters, digits, '.', '_' and '-'", s.ID)
	case s.ID != dirName:
		return bad("id %q differs from the directory name %q", s.ID, dirName)
	case !fixtureKinds[s.Kind]:
		return bad("kind %q: want fix, feature, refactor or greenfield", s.Kind)
	case s.Lang == "":
		return bad("lang is required")
	case s.Difficulty != "easy" && s.Difficulty != "medium" && s.Difficulty != "hard":
		return bad("difficulty %q: want easy, medium or hard", s.Difficulty)
	case strings.TrimSpace(s.Prompt) == "":
		return bad("prompt is required")
	case strings.TrimSpace(s.Verify) == "":
		return bad("verify is required")
	case s.Team.Mode != "" && s.Team.Mode != "single" && s.Team.Mode != "swarm":
		return bad("team.mode %q: want single or swarm", s.Team.Mode)
	}
	return nil
}

// safeID accepts only ASCII letters, digits, dots, underscores, and hyphens; empty input also
// passes.
func safeID(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// readTree reads a directory's regular files into memory, keyed by slash-separated relative path. Symlinks, special files,
// anything oversized and a runaway file count are errors.
func readTree(root string) (map[string][]byte, error) {
	out := map[string][]byte{}
	total := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", rel)
		}
		if len(out) >= maxFixtureFiles {
			return fmt.Errorf("more than %d files", maxFixtureFiles)
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxFixtureFile+1))
		if err != nil {
			return err
		}
		if len(data) > maxFixtureFile {
			return fmt.Errorf("%s is larger than %d bytes", rel, maxFixtureFile)
		}
		if total += len(data); total > maxFixtureTotal {
			return fmt.Errorf("more than %d bytes in all", maxFixtureTotal)
		}
		out[rel] = data
		return nil
	})
	return out, err
}

// sortedPaths returns file-map keys in lexical order.
func sortedPaths(m map[string][]byte) []string {
	ps := make([]string, 0, len(m))
	for p := range m {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	return ps
}

// fixtureEnv fixes what makes a commit's hash: who, when.
var fixtureEnv = []string{
	"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
	"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
}

// writeFixtureRepo makes (or remakes) the repository of a fixture and returns its one commit.
func writeFixtureRepo(ctx context.Context, g *env.Git, dir string, files map[string][]byte) (string, error) {
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := writeFiles(dir, files); err != nil {
		return "", err
	}
	run := func(args ...string) ([]byte, error) {
		return g.RunEnv(ctx, dir, fixtureEnv, nil, 1<<24, args...)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A", "--force"},
		{"-c", "commit.gpgsign=false", "commit", "-q", "--no-verify", "-m", "fixture start"},
	} {
		if _, err := run(args...); err != nil {
			return "", fmt.Errorf("git %s: %w", args[0], err)
		}
	}
	out, err := run("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// writeFiles writes fixture files in sorted order, rejecting empty, absolute (rooted or with a volume name), or double-dot
// paths and making shebang files executable.
func writeFiles(dir string, files map[string][]byte) error {
	for _, p := range sortedPaths(files) {
		if p == "" || strings.HasPrefix(p, "/") || filepath.IsAbs(p) || filepath.VolumeName(p) != "" || strings.Contains(p, "..") {
			return fmt.Errorf("unsafe path %q", p)
		}
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if bytes.HasPrefix(files[p], []byte("#!")) {
			mode = 0o755
		}
		if err := os.WriteFile(full, files[p], mode); err != nil {
			return err
		}
	}
	return nil
}

// fixturePatch is the reference solution as a patch: what turns start into start with the solution's files over it.
func fixturePatch(ctx context.Context, g *env.Git, start, solution map[string][]byte) ([]byte, error) {
	dir, err := os.MkdirTemp("", "sleipnir-fixture-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if _, err := writeFixtureRepo(ctx, g, dir, start); err != nil {
		return nil, err
	}
	if err := writeFiles(dir, solution); err != nil {
		return nil, err
	}
	if _, err := g.RunEnv(ctx, dir, fixtureEnv, nil, 1<<24, "add", "-A", "--force"); err != nil {
		return nil, err
	}
	return g.RunEnv(ctx, dir, append([]string{"GIT_LITERAL_PATHSPECS=1"}, fixtureEnv...), nil, 1<<26,
		"diff", "--cached", "--binary", "--full-index", "--no-renames", "--no-ext-diff", "--no-textconv")
}
