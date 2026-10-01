package env

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// LedgerFile is the append-only record of what every attempt of a run cost, in the run directory.
//
// An attempt starts from an empty sample directory (a half-written log of a crashed attempt must not be
// resumed), so what a failed attempt spent used to vanish with it, and a run that retried reported only its
// last attempt. The ledger is written as each attempt ends, before its directory can be wiped, and it is what
// the spend cap counts: the cap is the run's, across invocations, and the endpoint charged for all of it.
const LedgerFile = "ledger.jsonl"

// LedgerEntry is one attempt of one rollout.
type LedgerEntry struct {
	Time    time.Time `json:"time"`
	Run     string    `json:"run"`
	Task    string    `json:"task"`
	Sample  int       `json:"sample"`
	Attempt int       `json:"attempt"`
	// Status is how the attempt ended: ok, infra or cancelled.
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	// CostUSD and Requests are what the attempt's own log recorded: model responses and what they cost.
	CostUSD  float64 `json:"cost_usd"`
	Requests int     `json:"requests"`
	// Retries are the requests the endpoint did not answer (429, 5xx, dropped connections): repeated, never charged
	// to the request budget.
	Retries int `json:"retries,omitempty"`
}

// ledger appends entries to LedgerFile and keeps the running total.
type ledger struct {
	mu    sync.Mutex
	path  string
	spent float64
}

// openLedger opens the ledger of a run directory. What an earlier invocation wrote counts: a run's cap is
// the run's, not the invocation's.
func openLedger(path string) *ledger {
	l := &ledger{path: path}
	f, err := os.Open(path)
	if err != nil {
		return l
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var e LedgerEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			l.spent += cleanCost(e.CostUSD)
		}
	}
	return l
}

// add appends an entry. A failure to write is returned; the total counts the entry regardless, so a full disk
// cannot hide spending from the cap of the running process.
func (l *ledger) add(e LedgerEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.spent += cleanCost(e.CostUSD)
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(b, '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// total is what the ledger says was spent.
func (l *ledger) total() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.spent
}

// cleanCost keeps a hostile or broken number out of the total: a cost that is not a number, or negative, is nothing.
func cleanCost(c float64) float64 {
	if math.IsNaN(c) || math.IsInf(c, 0) || c < 0 {
		return 0
	}
	return c
}

// spendOf reads what a run directory's log recorded: the cost and count of model responses, and the requests
// the endpoint did not answer. A missing or damaged log reads as what it holds.
func spendOf(dir string) (usd float64, requests, retries int) {
	_ = events.Scan(filepath.Join(dir, "events.jsonl"), func(e events.Event) error {
		switch e.Type {
		case events.TypeModelResponse:
			requests++
			var d struct {
				CostUSD float64 `json:"cost_usd"`
			}
			if json.Unmarshal(e.Data, &d) == nil {
				usd += cleanCost(d.CostUSD)
			}
		case events.TypeModelError:
			retries++
		}
		return nil
	})
	return usd, requests, retries
}

// recordAttempt writes the ledger entry of an attempt that is ending. An attempt that spent nothing and ran
// nothing leaves no entry.
func (rn *run) recordAttempt(j job, dir string, attempt int, out attemptOutcome) {
	if rn.ledger == nil {
		return
	}
	usd, requests, retries := spendOf(dir)
	if usd == 0 && requests == 0 && retries == 0 {
		return
	}
	status := out.result.Status
	switch {
	case out.cancelled:
		status = StatusCancelled
	case status == "":
		status = StatusOK
	}
	e := LedgerEntry{
		Time: rn.r.now().UTC(), Run: rn.id, Task: j.task.ID, Sample: j.sample, Attempt: attempt, Status: status,
		Error: out.result.Error, CostUSD: usd, Requests: requests, Retries: retries,
	}
	if err := rn.ledger.add(e); err != nil {
		rn.r.logf("env: %s/%d: writing the ledger: %v", j.task.ID, j.sample, err)
	}
}

// capReached reports whether the run's spend cap is used up.
func (rn *run) capReached() bool {
	return rn.r.MaxSpendUSD > 0 && rn.ledger != nil && rn.ledger.total() >= rn.r.MaxSpendUSD
}
