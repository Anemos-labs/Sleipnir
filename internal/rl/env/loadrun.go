package env

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/rl"
)

// AttemptStats is what a run's ledger says about attempts. A rollout that failed for infrastructure reasons is tried again
// from scratch; its spend is in the ledger even though its directory is gone, so a report that counts only the answered
// attempts would understate what the run cost and hide how much of it was wasted.
type AttemptStats struct {
	Attempts       int     `json:"attempts"`
	Wasted         int     `json:"wasted"` // attempts that ended without an answer: infrastructure, cancelled
	Requests       int     `json:"requests"`
	WastedRequests int     `json:"wasted_requests"`
	Retries        int     `json:"retries"` // requests the endpoint did not answer and the harness repeated
	SpentUSD       float64 `json:"spent_usd"`
	WastedUSD      float64 `json:"wasted_usd"`
}

// Identity says what was measured, so two reports can be told apart and refused when they measure different things.
type Identity struct {
	Run      string `json:"run"`
	Model    string `json:"model"`
	Endpoint string `json:"endpoint,omitempty"`
	Mode     string `json:"mode"` // single, swarm:N, or mixed when the tasks differ
	Group    int    `json:"group"`
	Seed     int64  `json:"seed"`
	Tasks    int    `json:"tasks"`
	// TasksDigest is a hash of the task ids and their verifier versions: equal digests mean the same suite.
	TasksDigest string `json:"tasks_digest"`
	// Version, Commit and Renderer name the harness build and its prompt engine, as the episodes recorded them.
	Version  string `json:"version,omitempty"`
	Commit   string `json:"commit,omitempty"`
	Renderer string `json:"renderer,omitempty"`
}

// LoadRun reads a run directory (what `rl rollout --out` wrote, finished or still running) back into a Report. The
// numbers are recomputed from the episodes on disk, not copied from summary.json, so a run that was rescored
// (`rl reward`) reports what it is now.
func LoadRun(dir string) (Report, error) {
	b, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return Report{}, fmt.Errorf("%s is not a run directory: %w", dir, err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return Report{}, fmt.Errorf("%s: manifest.json: %w", dir, err)
	}
	group := max(m.GroupSize, 1)
	tasks := make([]rl.Task, len(m.Tasks))
	for i, t := range m.Tasks {
		tasks[i] = rl.Task{ID: t.ID, Kind: t.Kind, Repo: rl.RepoSpec{Path: t.Repo, Commit: t.Commit}, Tags: t.Tags, Team: t.Team, Budget: t.Budget}
	}

	var results []RolloutResult
	var id Identity
	pending, unreadable, skipped := 0, 0, 0
	skips := skippedRollouts(dir)
	for _, t := range tasks {
		for s := 0; s < group; s++ {
			raw, err := os.ReadFile(filepath.Join(dir, t.ID, strconv.Itoa(s), "episode.json"))
			if err != nil {
				// A skipped rollout leaves no episode (its task requires a tool this machine lacks): the run's
				// summary says so, and it is not one that has yet to run.
				if skips[t.ID] > 0 {
					skips[t.ID]--
					skipped++
					continue
				}
				pending++
				continue
			}
			var ep rl.Episode
			if json.Unmarshal(raw, &ep) != nil {
				unreadable++
				continue
			}
			if ep.Has(rl.FlagInfraError) {
				results = append(results, RolloutResult{Task: t.ID, Sample: s, Status: StatusInfra, Tags: t.Tags})
				continue
			}
			if id.Version == "" && id.Renderer == "" {
				id.Version, id.Commit, id.Renderer = ep.Harness.Version, ep.Harness.Commit, ep.Harness.Renderer
			}
			r := ResultFromEpisode(&ep, t.Tags)
			r.Task, r.Sample = t.ID, s // the directory names are the truth even if the episode was copied
			results = append(results, r)
		}
	}

	rep := BuildReport(tasks, results, group)
	rep.RunID, rep.Model, rep.Created = m.RunID, m.Policy.Model, m.Updated
	rep.Pending, rep.Skipped = pending, skipped
	if unreadable > 0 {
		pending += unreadable
		rep.Pending = pending
	}
	id.Run, id.Model, id.Endpoint = m.RunID, m.Policy.Model, m.Policy.Endpoint
	id.Group, id.Seed, id.Tasks = group, m.Config.Seed, len(tasks)
	id.Mode = modeOf(m)
	id.TasksDigest = tasksDigest(m.Tasks)
	rep.Identity = &id
	if a, ok := readAttempts(filepath.Join(dir, LedgerFile)); ok {
		rep.Attempts = &a
	}
	return rep, nil
}

// skippedRollouts reads how many rollouts of each task the run's summary records as skipped for a missing tool, or nil
// when there is no readable summary. Skipped rollouts write no episode, so the summary is the only record of them.
func skippedRollouts(dir string) map[string]int {
	b, err := os.ReadFile(filepath.Join(dir, "summary.json"))
	if err != nil {
		return nil
	}
	var s struct {
		Skips []SkipRecord `json:"skips"`
	}
	if json.Unmarshal(b, &s) != nil {
		return nil
	}
	out := map[string]int{}
	for _, k := range s.Skips {
		out[k.Task] += k.Rollouts
	}
	return out
}

// modeOf says how the run's teams were made up.
func modeOf(m Manifest) string {
	modes := map[string]bool{}
	agents := 0
	for _, t := range m.Tasks {
		modes[t.Team.Mode] = true
		agents = max(agents, t.Team.Agents)
	}
	agents = max(agents, m.Config.Agents)
	switch {
	case len(modes) == 1 && modes["single"], len(modes) == 0:
		return "single"
	case len(modes) == 1 && modes["swarm"]:
		return "swarm:" + strconv.Itoa(agents)
	}
	return "mixed"
}

// tasksDigest identifies a suite by its task ids and verifier versions, independent of order.
func tasksDigest(ts []ManifestTask) string {
	lines := make([]string, len(ts))
	for i, t := range ts {
		lines[i] = t.ID + " " + t.VerifierVersion
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:8])
}

// readAttempts totals a run's ledger; ok is false when there is none.
func readAttempts(path string) (a AttemptStats, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return a, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var e LedgerEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		ok = true
		cost := cleanCost(e.CostUSD)
		a.Attempts++
		a.Requests += e.Requests
		a.Retries += e.Retries
		a.SpentUSD += cost
		if e.Status != StatusOK {
			a.Wasted++
			a.WastedRequests += e.Requests
			a.WastedUSD += cost
		}
	}
	return a, ok
}
