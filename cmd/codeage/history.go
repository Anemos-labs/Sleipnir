package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type lineAge struct {
	Line   int    `json:"line"`
	Commit string `json:"commit"`
	Time   int64  `json:"time"`
}

type fileAge struct {
	Path       string    `json:"path"`
	Category   string    `json:"category"`
	Bytes      int64     `json:"bytes"`
	LastChange int64     `json:"last_change"`
	Oldest     int64     `json:"oldest"`
	Median     int64     `json:"median"`
	Lines      int       `json:"lines"`
	Samples    []lineAge `json:"samples,omitempty"`
	Status     string    `json:"status"`
	Object     string    `json:"-"`
	Mode       string    `json:"-"`
}

type report struct {
	Revision  string    `json:"revision"`
	SourceURL string    `json:"source_url,omitempty"`
	AsOf      time.Time `json:"as_of"`
	Shallow   bool      `json:"shallow"`
	Files     []fileAge `json:"files"`
	Errors    []string  `json:"errors"`
	Warnings  []string  `json:"warnings"`
}

// git reads history without interpreting path arguments in a shell or as pathspecs.
func git(ctx context.Context, repo string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-replace-objects", "--literal-pathspecs", "-C", repo}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return b, nil
}

// collect freezes the revision before reading any files and preserves failed rows.
func collect(ctx context.Context, repo, ref string, jobs int, requireFull bool, now time.Time) (report, error) {
	r := report{AsOf: now, Files: []fileAge{}, Errors: []string{}, Warnings: []string{}}
	sha, err := git(ctx, repo, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return r, err
	}
	r.Revision = strings.TrimSpace(string(sha))
	if remote, e := git(ctx, repo, "remote", "get-url", "origin"); e == nil {
		if m := regexp.MustCompile(`^(?:https://github\.com/|git@github\.com:)([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(?:\.git)?$`).FindStringSubmatch(strings.TrimSpace(string(remote))); m != nil {
			r.SourceURL = "https://github.com/" + m[1]
		}
	}
	shallow, err := git(ctx, repo, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return r, err
	}
	r.Shallow = strings.TrimSpace(string(shallow)) == "true"
	if r.Shallow {
		if requireFull {
			return r, fmt.Errorf("shallow history: fetch full history before collecting an authoritative report")
		}
		r.Warnings = append(r.Warnings, "Shallow history: surviving lines can predate the reported boundary commits. Fetch full history before comparing ages.")
	}
	listing, err := git(ctx, repo, "ls-tree", "-r", "-z", "-l", r.Revision)
	if err != nil {
		return r, err
	}
	for _, record := range bytes.Split(listing, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		meta, name, ok := bytes.Cut(record, []byte{'\t'})
		fields := strings.Fields(string(meta))
		if !ok || len(fields) != 4 {
			return r, fmt.Errorf("invalid ls-tree record %q", record)
		}
		size, _ := strconv.ParseInt(fields[3], 10, 64) // submodule entries have size "-"
		r.Files = append(r.Files, fileAge{Path: string(name), Mode: fields[0], Object: fields[2], Bytes: size, Category: category(string(name))})
	}
	var wg sync.WaitGroup
	queue := make(chan int)
	for i := 0; i < jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := range queue {
				f := &r.Files[n]
				if err := analyze(ctx, repo, r.Revision, f); err != nil {
					f.Status = "error: " + err.Error()
				}
			}
		}()
	}
	for n := range r.Files {
		queue <- n
	}
	close(queue)
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return r, err
	}
	for _, f := range r.Files {
		if strings.HasPrefix(f.Status, "error:") {
			r.Errors = append(r.Errors, f.Path+": "+f.Status)
		}
	}
	return r, nil
}

// category provides filter labels without excluding fixtures, assets, or tooling.
func category(name string) string {
	switch {
	case strings.Contains("/"+name, "/testdata/") || strings.HasPrefix(name, "bench/fixtures/"):
		return "fixtures"
	case strings.HasSuffix(name, "_test.go"):
		return "tests"
	case strings.HasPrefix(name, "docs/media/"):
		return "media"
	case strings.HasSuffix(name, ".md"):
		return "docs"
	case strings.HasSuffix(name, ".go"):
		return "code"
	case strings.HasPrefix(name, "scripts/") || strings.HasPrefix(name, ".github/"):
		return "automation"
	default:
		return "other"
	}
}

// analyze reads committed blobs, follows line provenance across renames, and marks
// binary, large, link, and submodule entries as file-age-only instead of omitting them.
func analyze(ctx context.Context, repo, revision string, f *fileAge) error {
	last, err := git(ctx, repo, "log", "-1", "--format=%ct", revision, "--", f.Path)
	if err != nil {
		return err
	}
	f.LastChange, err = strconv.ParseInt(strings.TrimSpace(string(last)), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid last-change date: %w", err)
	}
	f.Status = "text"
	switch {
	case f.Mode == "160000":
		f.Status = "submodule (file age only)"
	case f.Mode == "120000":
		f.Status = "symlink (file age only)"
	case f.Bytes > 2<<20:
		f.Status = "over 2 MiB (file age only)"
	}
	if f.Status != "text" {
		return nil
	}
	blob, err := git(ctx, repo, "cat-file", "blob", f.Object)
	if err != nil {
		return err
	}
	if bytes.IndexByte(blob, 0) >= 0 || !utf8.Valid(blob) {
		f.Status = "binary or non-UTF-8 (file age only)"
		return nil
	}
	if len(blob) == 0 {
		f.Status = "empty"
		return nil
	}
	b, err := git(ctx, repo, "blame", "--no-textconv", "--ignore-revs-file", "", "-w", "-M", "--line-porcelain", revision, "--", f.Path)
	if err != nil {
		return err
	}
	ages, err := parseBlame(b)
	if err != nil {
		return err
	}
	f.Lines = len(ages)
	if len(ages) > 0 {
		f.Oldest = ages[0].Time
		f.Median = ages[len(ages)/2].Time
		f.Samples = ages[:min(10, len(ages))]
	}
	return nil
}

// parseBlame extracts current line numbers and committer dates, ignoring blank lines.
func parseBlame(b []byte) ([]lineAge, error) {
	var ages []lineAge
	var current lineAge
	s := bufio.NewScanner(bytes.NewReader(b))
	s.Buffer(make([]byte, 4096), 4<<20)
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "\t") {
			if current.Line <= 0 || current.Time == 0 {
				return nil, fmt.Errorf("incomplete blame metadata")
			}
			if strings.TrimSpace(line[1:]) != "" {
				ages = append(ages, current)
			}
			current = lineAge{}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 3 && (len(fields[0]) == 40 || len(fields[0]) == 64) {
			current.Commit = fields[0]
			current.Line, _ = strconv.Atoi(fields[2])
		} else if value, ok := strings.CutPrefix(line, "committer-time "); ok {
			current.Time, _ = strconv.ParseInt(value, 10, 64)
		}
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	sort.Slice(ages, func(i, j int) bool {
		if ages[i].Time == ages[j].Time {
			return ages[i].Line < ages[j].Line
		}
		return ages[i].Time < ages[j].Time
	})
	return ages, nil
}
