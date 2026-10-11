package main

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/rl"
	"github.com/anemos-labs/sleipnir/internal/rl/env"
)

// The filters of rl export that need the files on disk: exclusion lists, role
// teachers and the task tags of older runs.

// exclusions is the set of rollout names read from --exclude files, with the
// entries no rollout matched.
type exclusions struct {
	entries map[string]bool // normalised entry -> matched
	order   []string        // entries in the order read, for reporting
}

// readExclusions reads --exclude files: one rollout per line, blank lines and
// lines starting with # ignored. An entry names a sample directory: absolute, or
// relative to the current directory, to the parent of the path given to rl export
// (deepseek_deepseek-v4.1-flash/job/task/3 for runs/deepseek_deepseek-v4.1-flash),
// or to that path itself (job/task/3); or the run directory's name and the episode
// id (job/task/3, the run directory being the one that holds task/3). A trailing
// /episode.json is ignored.
func readExclusions(files []string) (*exclusions, error) {
	ex := &exclusions{entries: map[string]bool{}}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return nil, fmt.Errorf("--exclude: %w", err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\xef\xbb\xbf")) // a UTF-8 byte order mark
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			e := normRolloutPath(line)
			if e == "" || e == "." {
				continue
			}
			if _, dup := ex.entries[e]; !dup {
				ex.entries[e] = false
				ex.order = append(ex.order, e)
			}
		}
		err = sc.Err()
		fh.Close()
		if err != nil {
			return nil, fmt.Errorf("--exclude %s: %w", f, err)
		}
	}
	return ex, nil
}

// normRolloutPath cleans a rollout path for comparison: forward slashes, no "./",
// no trailing slash or episode.json.
func normRolloutPath(p string) string {
	p = path.Clean(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/"))
	if path.Base(p) == "episode.json" {
		p = path.Dir(p)
	}
	return p
}

// rolloutNames are the names an exclusion entry may use for one sample (see
// readExclusions).
func rolloutNames(s sample) []string {
	var out []string
	add := func(p string) {
		if p = normRolloutPath(p); p != "" && p != "." && !strings.HasPrefix(p, "../") && p != ".." {
			out = append(out, p)
		}
	}
	add(filepath.ToSlash(s.Dir))
	abs, err := filepath.Abs(s.Dir)
	if err != nil {
		return out
	}
	add(filepath.ToSlash(abs))
	if root, err := filepath.Abs(s.Root); err == nil {
		for _, base := range []string{filepath.Dir(root), root} {
			if rel, err := filepath.Rel(base, abs); err == nil {
				add(filepath.ToSlash(rel))
			}
		}
	}
	// <run dir name>/<episode id>: the run directory holds <task>/<sample>.
	if id := s.Episode.ID; id != "" {
		slashed := filepath.ToSlash(abs)
		if strings.HasSuffix(slashed, "/"+id) {
			run := strings.TrimSuffix(slashed, "/"+id)
			add(path.Base(run) + "/" + id)
		}
	}
	return out
}

// match marks the sample's names as used and reports whether any is listed.
func (ex *exclusions) match(s sample) bool {
	hit := false
	for _, n := range rolloutNames(s) {
		if _, ok := ex.entries[n]; ok {
			ex.entries[n] = true
			hit = true
		}
	}
	return hit
}

// unmatched lists, in file order, the entries no rollout matched.
func (ex *exclusions) unmatched() []string {
	var out []string
	for _, e := range ex.order {
		if !ex.entries[e] {
			out = append(out, e)
		}
	}
	return out
}

// parseRoleTeachers reads --role-teacher values, ROLE=MODEL[,MODEL...]; a role
// named twice allows the models of both.
func parseRoleTeachers(vals []string) (map[string][]string, error) {
	if len(vals) == 0 {
		return nil, nil
	}
	out := map[string][]string{}
	for _, v := range vals {
		role, models, ok := strings.Cut(v, "=")
		role = strings.TrimSpace(role)
		ms := splitList(models)
		if !ok || role == "" || len(ms) == 0 {
			return nil, fmt.Errorf("--role-teacher %q: want ROLE=MODEL[,MODEL...]", v)
		}
		out[role] = append(out[role], ms...)
	}
	return out, nil
}

// joinTaskTags gives every episode that recorded no task tags (runs made before
// episodes carried them) the tags of its task: from the tasks file when it lists
// the task, else from the task.json stored with the rollout. It returns how many
// episodes received tags.
func joinTaskTags(samples []sample, tasksFile string) (int, error) {
	byID := map[string][]string{}
	if tasksFile != "" {
		ts, err := env.LoadTasks(tasksFile)
		if err != nil {
			return 0, err
		}
		for _, t := range ts {
			byID[t.ID] = t.Tags
		}
	}
	n := 0
	for _, s := range samples {
		ep := s.Episode
		if len(ep.Env.Tags) > 0 {
			continue
		}
		tags, ok := byID[ep.TaskID]
		if !ok {
			var t *rl.Task
			if t, _ = readTask(s.Dir); t != nil && (t.ID == "" || t.ID == ep.TaskID) {
				tags = t.Tags
			}
		}
		if len(tags) > 0 {
			ep.Env.Tags = append([]string(nil), tags...)
			n++
		}
	}
	return n, nil
}
