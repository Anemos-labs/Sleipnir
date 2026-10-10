package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
)

// The listing of recorded sessions: what `sleipnir sessions` prints and what the web interface's Sessions view and Resume dialog
// show. A session is a directory of <state>/sessions that holds an events.jsonl; everything here reads, never writes, and a session
// that is being written by another process is read as it is at that moment.

// StateRoot is the state directory: $SLEIPNIR_HOME, else ~/.sleipnir under home (the current user's home directory when home is
// empty). It is the one definition the commands share.
func StateRoot(home string) string {
	if home == "" && os.Getenv("SLEIPNIR_HOME") == "" {
		home, _ = os.UserHomeDir()
	}
	return stateRoot(home)
}

// SessionsDir is the directory that holds the session directories: <state>/sessions.
func SessionsDir(home string) string { return filepath.Join(StateRoot(home), "sessions") }

// idPattern is the shape of a session id: a UTC timestamp and three random bytes (newID).
var idPattern = regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{6}$`)

// ValidID reports whether id has the shape of a session id (20261009-221530-a91c3e). Routes check it before an id is joined into a
// path, so that no request can name a directory outside the sessions directory.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// LiveWindow is how recently a session's log must have been written for a listing to take it for one that is running even when no
// lock says so (a session of a platform without directory locks, or one between two writes).
const LiveWindow = 10 * time.Second

// Recorded is a session directory as lists show it.
type Recorded struct {
	ID, Dir, First, Model, Cwd, Name string
	CostUSD                          float64
	Bytes                            int64
	// LastWritten is the newest of the directory's and its log's modification times (the age prune judges by).
	LastWritten time.Time
	// Duration spans the first session.start to the last event of the log.
	Duration time.Duration
	Agents   int
	// Resumable: --resume can continue it. Interrupted: its last run ended by Ctrl-C or SIGTERM, or ended without a session.end
	// while nothing holds the session (the process died). Locked: a process (another one, or this one) holds the session's lock.
	Resumable, Interrupted, Locked bool
	// Live is Locked, or a log written within LiveWindow: the session is being written now.
	Live bool
	// Integration is how the end of an isolated team's run went, from the log's last swarm.integration event (nil: none).
	Integration *Integration
}

// Integration is the end of an isolated run as a listing reports it: whether the verified result reached the checkout, and when it
// did not, the message and the one command that gets it.
type Integration struct {
	Applied   bool   `json:"applied"`
	Committed bool   `json:"committed,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Message   string `json:"message"`
	Hint      string `json:"hint,omitempty"`
}

// Summary is what a listing reads from a session's log in one pass.
type Summary struct {
	// Model is the model of the last session.start; CostUSD the cost of the last session.end.
	Model   string
	CostUSD float64
	// First is the first input a person typed (a task the harness handed out is not one), with its whitespace collapsed; empty when
	// nothing was asked.
	First string
	// Cwd and Root are the working directory and the project root of the first session.start.
	Cwd, Root string
	// Started is the time of the first session.start, Last the time of the last event.
	Started, Last time.Time
	// Agents counts the distinct agents that wrote to the log.
	Agents int
	// EndReason is the reason of the last session.end; Open is true when a session.start follows the last session.end (or there is
	// no end at all): the last run did not close the session.
	EndReason string
	Open      bool
	// Integration is the end of an isolated run (nil: none recorded).
	Integration *Integration
}

// maxFirst bounds Summary.First, in runes; listings cut it much shorter.
const maxFirst = 1000

// maxAgents bounds the agents a summary tells apart.
const maxAgents = 4096

// Summarize reads the session log at path (an events.jsonl) for a listing line. A log that cannot be read gives the zero Summary;
// a line longer than 16 MiB ends the reading where it is, as it does for every reader of logs that bounds its lines.
func Summarize(path string) Summary {
	var s Summary
	f, err := os.Open(path)
	if err != nil {
		return s
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	agents := map[string]bool{}
	var (
		started bool
		iso     struct {
			Base   string `json:"base"`
			Commit bool   `json:"commit"`
		}
		applied string
	)
	for sc.Scan() {
		var e events.Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if !e.TS.IsZero() {
			s.Last = e.TS
		}
		if e.Agent != "" && len(agents) < maxAgents {
			agents[e.Agent] = true
		}
		switch e.Type {
		case events.TypeSessionStart:
			var d struct{ Model, Cwd, Root string }
			_ = json.Unmarshal(e.Data, &d)
			s.Model = d.Model
			if !started {
				started, s.Started, s.Cwd, s.Root = true, e.TS, d.Cwd, d.Root
			}
			s.Open = true
		case events.TypeUserInput:
			if s.First == "" {
				var d struct{ Text, Origin string }
				_ = json.Unmarshal(e.Data, &d)
				if d.Origin == "" || d.Origin == string(core.OriginUser) { // a task the harness handed out is not the prompt
					s.First = collapse(d.Text, maxFirst)
				}
			}
		case events.TypeSessionEnd:
			var d struct {
				Cost   float64 `json:"cost_usd"`
				Reason string  `json:"reason"`
			}
			_ = json.Unmarshal(e.Data, &d)
			s.CostUSD, s.EndReason, s.Open = d.Cost, d.Reason, false
		case isolationEvent:
			_ = json.Unmarshal(e.Data, &iso)
		case integrationStateEvent:
			var d struct {
				Applied string `json:"applied"`
			}
			_ = json.Unmarshal(e.Data, &d)
			applied = d.Applied
		case events.TypeSwarmIntegration:
			var d struct {
				Branch    string `json:"branch"`
				Applied   bool   `json:"applied"`
				Committed bool   `json:"committed"`
				Reason    string `json:"reason"`
				Files     []string
			}
			_ = json.Unmarshal(e.Data, &d)
			s.Integration = integrationOf(d.Branch, d.Reason, d.Applied, d.Committed, len(d.Files), iso.Commit, iso.Base, applied)
		}
	}
	s.Agents = len(agents)
	return s
}

// integrationOf words the end of an isolated run as the swarm reports it (swarm.IntegrationReport): the message, and for a result
// that stayed on its branch the one command that gets it.
func integrationOf(branch, reason string, ok, committed bool, files int, commitMode bool, base, applied string) *Integration {
	in := &Integration{Applied: ok, Committed: committed, Branch: branch}
	switch {
	case ok && committed:
		in.Message = "The verified result was committed onto your branch (branch " + branch + ")."
	case ok:
		in.Message = "The verified result was applied to your working tree as uncommitted changes (branch " + branch + ")."
	default:
		from := applied
		if from == "" {
			from = base
		}
		if commitMode || from == "" {
			in.Hint = "git merge " + branch
		} else {
			in.Hint = "git diff --binary " + from + " " + branch + " | git apply --3way"
		}
		why := reason
		if why == "" {
			why = "it could not be applied safely"
		}
		in.Message = "The result was NOT applied to your checkout (" + why + "). It is safe on branch " + branch + ". To get it: " + in.Hint
	}
	return in
}

// collapse joins the words of s with single spaces and cuts the result at n runes.
func collapse(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > n {
		s = string([]rune(s)[:n])
	}
	return s
}

// Locked reports whether another process (or this one) holds a session directory's lock. It never creates the lock file (a
// directory without one is held by nobody: every holder creates it) and so never changes the directory's modification time. On Linux
// it reads the kernel's table of locks (/proc/locks) and takes no lock itself, so that a `--resume` that starts at that moment never
// finds the session "in use" because of the probe. Elsewhere, or when that table cannot be read, it probes by taking the lock and
// giving it back at once. A lock that cannot be probed (permissions) counts as held: a caller that would delete must not. On a
// platform without directory locks nothing is ever held.
func Locked(dir string) bool {
	return lockedIn(dir, nil)
}

// lockedIn is Locked with the kernel's lock table already read (nil: read it, or probe).
func lockedIn(dir string, table *flockTable) bool {
	fi, err := os.Lstat(filepath.Join(dir, ".lock"))
	if err != nil {
		return false
	}
	if table == nil {
		table = readFlocks()
	}
	if table != nil {
		if key, ok := fileKey(fi); ok {
			return table.held[key]
		}
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return true
	}
	unlock()
	return false
}

// flockKey names a file as /proc/locks does: the device's major and minor numbers and the inode.
type flockKey struct{ major, minor, ino uint64 }

// flockTable is the set of files that hold a flock now.
type flockTable struct{ held map[flockKey]bool }

// readFlocks reads the flock entries of /proc/locks (Linux); nil where there is no such table.
func readFlocks() *flockTable {
	if runtime.GOOS != "linux" {
		return nil
	}
	b, err := os.ReadFile("/proc/locks")
	if err != nil {
		return nil
	}
	return parseFlocks(b)
}

// parseFlocks reads lines such as "1: FLOCK  ADVISORY  WRITE 4321 fd:01:131073 0 EOF" (a waiter's line, "1: -> FLOCK ...", holds
// nothing). The device is MAJOR:MINOR in hex, the inode in decimal.
func parseFlocks(b []byte) *flockTable {
	t := &flockTable{held: map[flockKey]bool{}}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) < 6 || f[1] != "FLOCK" {
			continue
		}
		parts := strings.Split(f[5], ":")
		if len(parts) != 3 {
			continue
		}
		major, err1 := strconv.ParseUint(parts[0], 16, 64)
		minor, err2 := strconv.ParseUint(parts[1], 16, 64)
		ino, err3 := strconv.ParseUint(parts[2], 10, 64)
		if err1 == nil && err2 == nil && err3 == nil {
			t.held[flockKey{major, minor, ino}] = true
		}
	}
	return t
}

// fileKey is the device and inode of a file as /proc/locks names them (Linux's encoding of a device number), read from the stat
// structure by name so that the code builds on every platform.
func fileKey(fi fs.FileInfo) (flockKey, bool) {
	v := reflect.ValueOf(fi.Sys())
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return flockKey{}, false
	}
	dev, ino := v.FieldByName("Dev"), v.FieldByName("Ino")
	if !dev.IsValid() || !ino.IsValid() || !dev.CanUint() || !ino.CanUint() {
		return flockKey{}, false
	}
	d := dev.Uint()
	major := (d>>8)&0xfff | (d>>32)&^uint64(0xfff)
	minor := d&0xff | (d>>12)&^uint64(0xff)
	return flockKey{major, minor, ino.Uint()}, true
}

// listCache keeps what was read of each session directory, keyed by the directory, valid while its log and the directory itself
// are unchanged: a listing of hundreds of sessions reads only the ones written since the last. A listing evicts the entries of its
// sessions directory that it no longer finds, so the cache holds what exists and no more.
var listCache = struct {
	sync.Mutex
	m map[string]cachedListing
}{m: map[string]cachedListing{}}

// cachedListing is the cached part of one directory's listing and the stamps it is valid for.
type cachedListing struct {
	logSize        int64
	logMod, dirMod time.Time
	sum            Summary
	bytes          int64
	resumable      bool
}

// readListing returns the summary, size and resumability of a session directory whose log is log and directory info is fi.
func readListing(dir string, fi, log fs.FileInfo) cachedListing {
	listCache.Lock()
	c, ok := listCache.m[dir]
	listCache.Unlock()
	if ok && c.logSize == log.Size() && c.logMod.Equal(log.ModTime()) && c.dirMod.Equal(fi.ModTime()) {
		return c
	}
	c = cachedListing{logSize: log.Size(), logMod: log.ModTime(), dirMod: fi.ModTime()}
	c.sum = Summarize(filepath.Join(dir, "events.jsonl"))
	c.bytes = DirSize(dir)
	c.resumable = Resumable(dir)
	listCache.Lock()
	listCache.m[dir] = c
	listCache.Unlock()
	return c
}

// ListRecorded lists the session directories of the state directory (StateRoot(home)/sessions), newest first. A missing sessions
// directory lists none. Directories without an event log, files and symbolic links are not sessions and are not listed.
func ListRecorded(home string, now time.Time) ([]Recorded, error) {
	return ListRecordedDir(SessionsDir(home), now)
}

// ListRecordedDir is ListRecorded for a sessions directory given by its path.
func ListRecordedDir(root string, now time.Time) ([]Recorded, error) {
	ents, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Recorded
	locks := readFlocks() // once for the whole listing
	seen := map[string]bool{}
	for _, e := range ents {
		dir := filepath.Join(root, e.Name())
		fi, err := os.Lstat(dir)
		if err != nil || !fi.IsDir() {
			continue
		}
		log, err := os.Stat(filepath.Join(dir, "events.jsonl"))
		if err != nil || log.IsDir() {
			continue
		}
		seen[dir] = true
		c := readListing(dir, fi, log)
		r := recordedOf(e.Name(), dir, fi, log, c)
		r.Locked = lockedIn(dir, locks)
		r.Live = r.Locked || now.Sub(log.ModTime()) < LiveWindow
		r.Interrupted = c.sum.EndReason == EndInterrupted || (c.sum.Open && !r.Live)
		if m, err := LoadMeta(dir); err == nil {
			r.Name = m.Name
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].LastWritten.Equal(out[j].LastWritten) {
			return out[i].LastWritten.After(out[j].LastWritten)
		}
		return out[i].ID > out[j].ID
	})
	evictGone(root, seen)
	return out, nil
}

// evictGone drops the cached entries of the sessions directory root whose session the listing did not find (deleted, or no longer a
// session).
func evictGone(root string, seen map[string]bool) {
	listCache.Lock()
	defer listCache.Unlock()
	for dir := range listCache.m {
		if filepath.Dir(dir) == root && !seen[dir] {
			delete(listCache.m, dir)
		}
	}
}

// recordedOf fills a listing row from what was read of the directory.
func recordedOf(id, dir string, fi, log fs.FileInfo, c cachedListing) Recorded {
	r := Recorded{
		ID: id, Dir: dir, First: c.sum.First, Model: c.sum.Model, Cwd: c.sum.Cwd, CostUSD: c.sum.CostUSD, Bytes: c.bytes,
		Agents: c.sum.Agents, Resumable: c.resumable, Integration: c.sum.Integration,
	}
	r.LastWritten = fi.ModTime()
	if log.ModTime().After(r.LastWritten) {
		r.LastWritten = log.ModTime()
	}
	if !c.sum.Started.IsZero() && c.sum.Last.After(c.sum.Started) {
		r.Duration = c.sum.Last.Sub(c.sum.Started)
	}
	return r
}

// DirSize is the bytes of the regular files under dir; links are counted as themselves, never followed.
func DirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}
