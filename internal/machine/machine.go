// Package machine shares the computer the harness runs on between its agents and the person using it.
//
// A team of agents runs many commands at once, and the build and test tools they call size themselves for a machine of their
// own: each compiler, test runner or bundler starts one worker per core and takes the memory it finds. Eight agents building at
// once on one machine then ask for eight times its cores and more memory than it has, and the machine stops answering. A Budget
// keeps the commands of every agent, of every Sleipnir process of the same user, inside one share of the machine:
//
//   - Heavy commands (builds, test runs, installs: Heavy) take a slot before they start. The slots are files locked by the
//     operating system, so every Sleipnir process that uses the same state directory shares them, and a slot whose holder died is
//     free again at once.
//   - Every command is told how many workers it may start (Env: CARGO_BUILD_JOBS, GOMAXPROCS, OMP_NUM_THREADS and the like, and
//     SLEIPNIR_JOBS for the tools that only take a flag), the CPU budget divided between the slots.
//   - Every command runs at a lower scheduling and I/O priority than the person's own programs (Start), so a busy machine still
//     answers its keyboard.
//   - A heavy command waits while free memory is below the reserve kept for the rest of the machine (Admit), and a command that
//     keeps eating memory past half of that reserve is stopped by its owner (MemoryLow, GroupRSS) before the machine starts
//     swapping itself to a halt.
//
// A nil *Budget is valid and limits nothing.
package machine

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Settings are the person's choices (the "resources" section of the configuration). The zero value is the default: a share
// of the machine computed from its size.
type Settings struct {
	// CPUs is how many cores the agents' commands may use together. 0: all of them but a quarter (at least two) left for
	// the rest of the machine.
	CPUs int
	// HeavySlots is how many heavy commands (builds, test runs, installs) may run at once on the machine, across every agent
	// and every Sleipnir process. 0: one for every four cores of CPUs, at least one.
	HeavySlots int
	// MemoryReserveMB is the memory, in MiB, kept free for the rest of the machine. 0: 15% of the machine's memory, between
	// 1 GiB and 8 GiB.
	MemoryReserveMB int
	// Priority is the scheduling priority of the agents' commands: "low" (the default) or "normal".
	Priority string
	// Unlimited turns every limit off: no slots, no worker counts, no memory guard, normal priority.
	Unlimited bool
}

// Budget is the share of the machine the agents' commands live in. It is safe for concurrent use.
type Budget struct {
	cpus    int   // cores the commands may use together
	total   int   // cores of the machine (or of the cgroup the harness runs in)
	slots   int   // heavy commands at once
	jobs    int   // workers one heavy command may start
	reserve int64 // bytes kept free for the rest of the machine; 0: memory is not watched
	memory  int64 // bytes of memory of the machine; 0: unknown
	low     bool  // lower the priority of commands
	dir     string

	// avail reads the memory available now (MemAvailable); ok is false where it cannot be read. A field so that a test can
	// say how much there is.
	avail func() (int64, bool)
	// poll is how often a waiting command looks again (a test makes it short).
	poll time.Duration
	// memWait bounds a heavy command's wait for memory: memory another program holds must not stop the agents for good.
	memWait time.Duration

	mu   sync.Mutex
	held map[int]bool // slots this process holds (the file lock alone does not exclude a second open where locks are not used)
}

// New returns the budget for settings on this machine. dir is where the slot files live: every process that passes the same
// directory shares the slots ("" keeps them within this process). It returns nil when settings say Unlimited.
func New(s Settings, dir string) *Budget {
	if s.Unlimited {
		return nil
	}
	total := cpuCount()
	memory, _ := totalMemory()
	return newBudget(s, dir, total, memory, availableMemory)
}

// Fixed returns the budget for a machine the caller describes instead of the one detected: cores, memory bytes (0: unknown)
// and how much of it is available now (nil: not known). It is for tests and simulations of other machines; Unlimited
// settings still give nil.
func Fixed(s Settings, dir string, cores int, memory int64, avail func() (int64, bool)) *Budget {
	if s.Unlimited {
		return nil
	}
	if avail == nil {
		avail = func() (int64, bool) { return 0, false }
	}
	return newBudget(s, dir, cores, memory, avail)
}

// newBudget computes the budget for a machine of total cores and memory bytes (0: unknown).
func newBudget(s Settings, dir string, total int, memory int64, avail func() (int64, bool)) *Budget {
	if total < 1 {
		total = 1
	}
	cpus := s.CPUs
	if cpus <= 0 {
		cpus = total - max(2, total/4)
	}
	cpus = max(1, min(cpus, total))
	slots := s.HeavySlots
	if slots <= 0 {
		slots = cpus / 4
	}
	slots = max(1, min(slots, cpus))
	var reserve int64
	if memory > 0 {
		if s.MemoryReserveMB > 0 {
			reserve = int64(s.MemoryReserveMB) << 20
		} else {
			reserve = min(max(memory*15/100, 1<<30), 8<<30)
		}
		reserve = min(reserve, memory/2) // a reserve larger than half the machine would stop every heavy command
	}
	return &Budget{
		cpus: cpus, total: total, slots: slots, jobs: max(1, cpus/slots),
		reserve: reserve, memory: memory,
		low: s.Priority != "normal", dir: dir,
		avail: avail, poll: 500 * time.Millisecond, memWait: 2 * time.Minute,
		held: map[int]bool{},
	}
}

// cpuCount is the number of cores the harness may use: the CPUs it is allowed to run on, within the CPU limit of its cgroup
// (which the Go runtime reads into GOMAXPROCS's default).
func cpuCount() int {
	n := runtime.NumCPU()
	if os.Getenv("GOMAXPROCS") == "" {
		n = min(n, runtime.GOMAXPROCS(0))
	}
	return n
}

// CPUs is how many cores the commands may use together.
func (b *Budget) CPUs() int {
	if b == nil {
		return 0
	}
	return b.cpus
}

// Slots is how many heavy commands may run at once.
func (b *Budget) Slots() int {
	if b == nil {
		return 0
	}
	return b.slots
}

// Jobs is how many workers one command may start: CPUs divided between the slots.
func (b *Budget) Jobs() int {
	if b == nil {
		return 0
	}
	return b.jobs
}

// Reserve is the memory, in bytes, kept free for the rest of the machine (0: memory is not watched).
func (b *Budget) Reserve() int64 {
	if b == nil {
		return 0
	}
	return b.reserve
}

// Summary describes the budget in one line, for a status display.
func (b *Budget) Summary() string {
	if b == nil {
		return "agent commands are not limited (resources.unlimited)"
	}
	s := fmt.Sprintf("agent commands use %d of %d CPUs: %d heavy at once, %d workers each", b.cpus, b.total, b.slots, b.jobs)
	if b.reserve > 0 {
		s += fmt.Sprintf("; %s of %s memory kept free", Bytes(b.reserve), Bytes(b.memory))
	}
	if b.low {
		s += "; low priority"
	}
	return s
}

// workerVars are the variables that tell a build or test tool how many workers to start, for the tools that otherwise start
// one per core. Tools that read only a flag (ninja -j, playwright --workers, jest --maxWorkers) can be given $SLEIPNIR_JOBS.
// MAKEFLAGS is deliberately absent: make is serial unless told otherwise, and a -j there would run a Makefile that is not
// written for it in parallel.
var workerVars = []string{
	"SLEIPNIR_JOBS",
	"CARGO_BUILD_JOBS",
	"CMAKE_BUILD_PARALLEL_LEVEL",
	"GOMAXPROCS",
	"PYTEST_XDIST_AUTO_NUM_WORKERS",
	"OMP_NUM_THREADS",
	"OPENBLAS_NUM_THREADS",
	"MKL_NUM_THREADS",
	"RAYON_NUM_THREADS",
	"VITEST_MAX_THREADS",
	"VITEST_MAX_FORKS",
	"UV_CONCURRENT_BUILDS",
	"BUNDLE_JOBS",
}

// Env adds the worker counts to env ("K=V" entries): each variable of workerVars that env does not set already, and -p to
// GOFLAGS unless it names one. A value the person or the project set is theirs and stays.
func (b *Budget) Env(env []string) []string {
	if b == nil {
		return env
	}
	set := map[string]int{}
	for i, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		set[envKey(name)] = i
	}
	out := append([]string(nil), env...)
	n := strconv.Itoa(b.jobs)
	for _, name := range workerVars {
		if _, ok := set[envKey(name)]; !ok {
			out = append(out, name+"="+n)
		}
	}
	if i, ok := set[envKey("GOFLAGS")]; ok {
		_, v, _ := strings.Cut(env[i], "=")
		if !hasGoParallel(v) {
			out[i] = "GOFLAGS=" + strings.TrimSpace(v+" -p="+n)
		}
	} else {
		out = append(out, "GOFLAGS=-p="+n)
	}
	return out
}

// envKey is how the platform compares variable names: by case on Windows, exactly elsewhere.
func envKey(name string) string {
	if runtime.GOOS == "windows" {
		return strings.ToUpper(name)
	}
	return name
}

// hasGoParallel reports whether GOFLAGS already sets the go command's -p.
func hasGoParallel(flags string) bool {
	for _, f := range strings.Fields(flags) {
		if f == "-p" || strings.HasPrefix(f, "-p=") || f == "--p" || strings.HasPrefix(f, "--p=") {
			return true
		}
	}
	return false
}

// Admission is what Admit did for one command.
type Admission struct {
	// Waited is how long the command waited for memory and a slot.
	Waited time.Duration
	// Note says why it waited, for the command's result ("" when it did not).
	Note    string
	release func()
}

// Release gives the slot back. It is safe to call more than once and on the zero Admission.
func (a *Admission) Release() {
	if a != nil && a.release != nil {
		a.release()
		a.release = nil
	}
}

// Admit lets one command start. A light command starts at once. A heavy one first waits while free memory is below the
// reserve (at most a couple of minutes: memory another program holds is not for the agents to wait out), then for a free
// slot, as long as ctx allows. waiting, when not nil, is told once for each reason the command waits, with a line for the
// person watching. The error is ctx's when it ends the wait.
func (b *Budget) Admit(ctx context.Context, heavy bool, waiting func(string)) (Admission, error) {
	if b == nil || !heavy {
		return Admission{}, nil
	}
	start := time.Now()
	var notes []string
	say := func(s string) {
		notes = append(notes, s)
		if waiting != nil {
			waiting(s)
		}
	}
	if b.reserve > 0 {
		if free, ok := b.avail(); ok && free < b.reserve {
			say(fmt.Sprintf("waiting for memory: %s free, %s is kept for the rest of the machine", Bytes(free), Bytes(b.reserve)))
			deadline := time.Now().Add(b.memWait)
			for time.Now().Before(deadline) {
				if err := b.sleep(ctx); err != nil {
					return Admission{}, err
				}
				if free, ok := b.avail(); !ok || free >= b.reserve {
					break
				}
			}
		}
	}
	release, err := b.acquire(ctx, func() {
		say(fmt.Sprintf("waiting for a free slot: %d heavy commands (builds, tests, installs) already run on this machine, the most it runs at once", b.slots))
	})
	if err != nil {
		return Admission{}, err
	}
	a := Admission{release: release}
	if len(notes) > 0 {
		a.Waited = time.Since(start)
		a.Note = fmt.Sprintf("[waited %s before starting: %s]", a.Waited.Round(time.Second), strings.Join(notes, "; "))
	}
	return a, nil
}

// sleep waits one poll interval, or until ctx ends.
func (b *Budget) sleep(ctx context.Context) error {
	t := time.NewTimer(b.poll)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// acquire takes a free slot, waiting until there is one or ctx ends. busy is called once, the first time every slot is taken.
func (b *Budget) acquire(ctx context.Context, busy func()) (func(), error) {
	told := false
	for {
		if release := b.tryAcquire(); release != nil {
			return release, nil
		}
		if !told {
			told = true
			busy()
		}
		if err := b.sleep(ctx); err != nil {
			return nil, err
		}
	}
}

// tryAcquire takes the first free slot, or returns nil when there is none. A slot file that cannot be opened or locked (a
// read-only state directory) is counted as free, held within this process alone: a broken lock must not stop the agents.
func (b *Budget) tryAcquire() func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dir != "" {
		_ = os.MkdirAll(b.dir, 0o700)
	}
	for i := 0; i < b.slots; i++ {
		if b.held[i] {
			continue
		}
		var f *os.File
		if b.dir != "" {
			var err error
			f, err = os.OpenFile(filepath.Join(b.dir, "slot-"+strconv.Itoa(i)+".lock"), os.O_RDWR|os.O_CREATE, 0o600)
			if err == nil {
				ok, lerr := tryLock(f)
				if lerr == nil && !ok {
					f.Close()
					continue // another process holds it
				}
				if lerr != nil {
					f.Close()
					f = nil
				}
			}
		}
		b.held[i] = true
		return func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			if f != nil {
				unlockFile(f)
				f.Close()
			}
			delete(b.held, i)
		}
	}
	return nil
}

// MemoryLow reports whether free memory has fallen below half the reserve, the point where a command that keeps taking memory
// is stopped, and how much is free. It is false where memory is not watched.
func (b *Budget) MemoryLow() (free int64, low bool) {
	if b == nil || b.reserve <= 0 {
		return 0, false
	}
	free, ok := b.avail()
	if !ok {
		return 0, false
	}
	return free, free < b.reserve/2
}

// Watches reports whether the budget watches memory (MemoryLow can be true).
func (b *Budget) Watches() bool {
	if b == nil || b.reserve <= 0 {
		return false
	}
	_, ok := b.avail()
	return ok
}

// Bytes formats n bytes for a person: "512 MiB", "3.2 GiB".
func Bytes(n int64) string {
	switch {
	case n >= 1<<30:
		return strconv.FormatFloat(math.Round(float64(n)/(1<<30)*10)/10, 'f', -1, 64) + " GiB"
	case n >= 1<<20:
		return strconv.FormatInt(n>>20, 10) + " MiB"
	}
	return strconv.FormatInt(n>>10, 10) + " KiB"
}
