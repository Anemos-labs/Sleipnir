package sched

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Job is one scheduled goal.
type Job struct {
	ID        string    `json:"id"`
	Cron      string    `json:"cron"`
	Goal      string    `json:"goal"`
	Model     string    `json:"model,omitempty"`
	Dir       string    `json:"dir,omitempty"`
	Mode      string    `json:"mode,omitempty"` // the permission mode of the run; empty is "default", which refuses what needs a person
	BudgetUSD float64   `json:"budget_usd,omitempty"`
	Created   time.Time `json:"created"`
	// LastRun is when the daemon last started the job (zero: never); LastExit is how that run ended ("ok", or what went wrong).
	LastRun  time.Time `json:"last_run,omitempty"`
	LastExit string    `json:"last_exit,omitempty"`
	Log      string    `json:"log,omitempty"` // the file the last run wrote
	// Paused keeps the daemon from starting the job until it is resumed; it is listed, and can be run by hand.
	Paused bool `json:"paused,omitempty"`
}

// ErrNoJob is the error of a change to a job that is not in the file.
var ErrNoJob = errors.New("no such job")

// Store is the job file.
type Store struct{ Path string }

// List reads the jobs, in the order they were added. A missing file holds none.
func (s Store) List() ([]Job, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var jobs []Job
	if err := json.Unmarshal(b, &jobs); err != nil {
		return nil, fmt.Errorf("%s: %w (fix or delete the file)", s.Path, err)
	}
	return jobs, nil
}

// Save writes the jobs atomically, readable by the user only.
func (s Store) Save(jobs []Job) error {
	b, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".schedule-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}

// Add validates and stores a job; the ID is chosen here, and never one a job had before (an ID with logs is not given again).
func (s Store) Add(j Job, now time.Time) (Job, error) {
	if _, err := ParseCron(j.Cron); err != nil {
		return Job{}, err
	}
	if strings.TrimSpace(j.Goal) == "" {
		return Job{}, errors.New("a job needs a goal")
	}
	unlock, err := s.writeLock()
	if err != nil {
		return Job{}, err
	}
	defer unlock()
	jobs, err := s.List()
	if err != nil {
		return Job{}, err
	}
	n := s.highestLogged()
	for _, o := range jobs {
		var v int
		if _, err := fmt.Sscanf(o.ID, "j%d", &v); err == nil && v > n {
			n = v
		}
	}
	j.ID, j.Created = fmt.Sprintf("j%d", n+1), now
	return j, s.Save(append(jobs, j))
}

// highestLogged is the highest job number that has a log in LogDir: a removed job keeps its logs, and a new job must not take its
// id (it would show them as its own). The job file stays a plain list, as every version reads it.
func (s Store) highestLogged() int {
	ents, err := os.ReadDir(s.LogDir())
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range ents {
		id, _, ok := strings.Cut(e.Name(), "-")
		var v int
		if _, err := fmt.Sscanf(id, "j%d", &v); ok && err == nil && v > n && id == fmt.Sprintf("j%d", v) {
			n = v
		}
	}
	return n
}

// Remove deletes a job by ID.
func (s Store) Remove(id string) error {
	unlock, err := s.writeLock()
	if err != nil {
		return err
	}
	defer unlock()
	jobs, err := s.List()
	if err != nil {
		return err
	}
	for i, j := range jobs {
		if j.ID == id {
			return s.Save(append(jobs[:i:i], jobs[i+1:]...))
		}
	}
	return fmt.Errorf("no job %q", id)
}

// Update changes one job and saves the file: the jobs are read as they are on disk now (a person or a daemon may have changed others
// since), fn changes the one with the ID, and a changed cron expression or goal is checked as Add checks them. The ID, the creation
// time and the run records stay what they were. The error wraps ErrNoJob when there is no such job.
func (s Store) Update(id string, fn func(*Job)) (Job, error) {
	return s.update(id, func(j *Job) error {
		before := *j
		fn(j)
		j.ID, j.Created = before.ID, before.Created
		if j.Cron != before.Cron {
			if _, err := ParseCron(j.Cron); err != nil {
				return err
			}
		}
		if strings.TrimSpace(j.Goal) == "" {
			return errors.New("a job needs a goal")
		}
		return nil
	})
}

// update changes one job under the write lock: fn may refuse the change with an error, and nothing is written then.
func (s Store) update(id string, fn func(*Job) error) (Job, error) {
	unlock, err := s.writeLock()
	if err != nil {
		return Job{}, err
	}
	defer unlock()
	jobs, err := s.List()
	if err != nil {
		return Job{}, err
	}
	for i := range jobs {
		if jobs[i].ID == id {
			if err := fn(&jobs[i]); err != nil {
				return Job{}, err
			}
			return jobs[i], s.Save(jobs)
		}
	}
	return Job{}, fmt.Errorf("%w %q", ErrNoJob, id)
}

// LockPath is the daemon lock of the store: schedule.lock beside the job file.
func (s Store) LockPath() string { return filepath.Join(filepath.Dir(s.Path), "schedule.lock") }

// Due lists the jobs that should start at now: those whose next minute after their last run (or their creation) has come; a paused
// job is never due. A daemon that was down while several slots passed starts a job once, not once per slot: the caller records
// LastRun = now.
func Due(jobs []Job, now time.Time) []Job {
	var due []Job
	for _, j := range jobs {
		c, err := ParseCron(j.Cron)
		if err != nil || j.Paused {
			continue
		}
		since := j.Created
		if j.LastRun.After(since) {
			since = j.LastRun
		}
		if next, ok := c.Next(since.In(now.Location())); ok && !next.After(now) {
			due = append(due, j)
		}
	}
	sort.SliceStable(due, func(a, b int) bool { return due[a].ID < due[b].ID })
	return due
}

// Next is when the job runs next, for listings; false for a job that will not run (a paused one, or an expression that names no
// time).
func Next(j Job, now time.Time) (time.Time, bool) {
	c, err := ParseCron(j.Cron)
	if err != nil || j.Paused {
		return time.Time{}, false
	}
	since := j.Created
	if j.LastRun.After(since) {
		since = j.LastRun
	}
	n, ok := c.Next(since.In(now.Location()))
	if ok && n.Before(now) {
		return now, true // overdue: it starts at the daemon's next look
	}
	return n, ok
}
