package traj

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/rl"
)

// Run is one recorded run: an event log and the blob store it points into. It is
// an immutable index built once; every method is safe for concurrent use.
//
// A Run never trusts the log. Prompts are rebuilt from content hashes and checked
// against their wire hashes, completions and token traces are parsed, and
// anything that does not check out is reported by [Run.Verify] instead of being
// exported.
type Run struct {
	dir   string
	evs   []events.Event
	blobs events.Blobs
	task  *rl.Task

	torn     bool  // the last line of the log was cut off and dropped
	badLines []int // 1-based numbers of unparseable lines before the tail
	dups     int   // exact duplicate events dropped (a resumed writer re-appending)

	reqs      map[string]*reqInfo
	reqOrder  []*reqInfo
	resps     map[string]*respInfo
	respOrder []*respInfo
	modelErrs map[string][]int // request id -> indexes (into evs) of model.error events
	issues    []Mismatch       // problems found while indexing

	mu       sync.Mutex
	msgMemo  map[string][]core.Hash // request id -> full message hash list
	msgErr   map[string]error
	toolsMem map[core.Hash][]core.ToolSpec
	sysMem   map[core.Hash]core.Block
	msgMem   map[core.Hash]core.Message
	memBytes int

	verifyOnce sync.Once
	verified   []Mismatch
}

// reqInfo is one parsed model.request event.
type reqInfo struct {
	id       string
	seq      uint64
	idx      int // index into Run.evs
	ts       time.Time
	agent    string
	role     string
	kind     string
	model    string
	renderer string
	hot      core.Hash
	params   json.RawMessage
	man      core.Manifest
	wire     core.Hash // the event's own wire_hash field
	// noManifest marks a request whose event carried no manifest (older logs, or a
	// recorder that failed to build one). It is still a call and still a cost, but
	// its prompt cannot be rebuilt.
	noManifest bool
}

// respInfo is one parsed model.response event.
type respInfo struct {
	id         string
	seq        uint64
	idx        int
	ts         time.Time
	model      string
	usage      core.Usage
	costUSD    float64
	hitRatio   float64
	expected   int
	anomaly    bool
	stop       core.StopReason
	totalMs    int64
	side       bool
	completion core.Hash
	tokens     core.Hash
}

// maxMemBytes bounds the decoded-blob caches. Exceeding it drops the caches; they
// are only a speed-up.
const maxMemBytes = 256 << 20

// Open loads a run directory: dir/events.jsonl (a torn last line is tolerated),
// dir/blobs and the optional dir/task.json. A missing blobs directory is not an
// error here: the run opens and [Run.Verify] reports every prompt as unreadable,
// which is more useful than refusing to look at the log.
func Open(dir string) (*Run, error) {
	evs, torn, bad, err := readEvents(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("traj: open %s: %w", dir, err)
	}
	var blobs events.Blobs = events.NewMemBlobs() // empty: every Get fails
	if st, serr := os.Stat(filepath.Join(dir, "blobs")); serr == nil && st.IsDir() {
		db, derr := events.NewDirBlobs(filepath.Join(dir, "blobs"))
		if derr != nil {
			return nil, fmt.Errorf("traj: open %s: blobs: %w", dir, derr)
		}
		blobs = db
	}
	r := newRun(evs, blobs)
	r.dir, r.torn, r.badLines = dir, torn, bad
	for _, n := range bad {
		r.issues = append(r.issues, Mismatch{Kind: KindLog, Detail: fmt.Sprintf("events.jsonl line %d is not a valid event and was skipped", n)})
	}
	if b, terr := os.ReadFile(filepath.Join(dir, "task.json")); terr == nil {
		var t rl.Task
		if uerr := json.Unmarshal(b, &t); uerr != nil {
			return nil, fmt.Errorf("traj: open %s: task.json: %w", dir, uerr)
		}
		r.task = &t
	} else if !errors.Is(terr, os.ErrNotExist) {
		return nil, fmt.Errorf("traj: open %s: task.json: %w", dir, terr)
	}
	return r, nil
}

// OpenWith builds a Run from events and blobs held in memory (tests, or callers
// that already parsed the log). The slice is copied; events are ordered by their
// sequence number when all of them carry one.
func OpenWith(evs []events.Event, blobs events.Blobs) *Run {
	return newRun(append([]events.Event(nil), evs...), blobs)
}

// readEvents parses events.jsonl. It returns the events, whether the final line
// was torn (cut off by a crash, and so dropped), and the numbers of any earlier
// lines that were skipped because they did not parse.
func readEvents(path string) (evs []events.Event, torn bool, bad []int, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, nil, err
	}
	defer f.Close()
	rd := bufio.NewReaderSize(f, 1<<20)
	lineNo, lastBad := 0, 0
	for {
		line, rerr := rd.ReadBytes('\n')
		if len(line) > 0 {
			lineNo++
			if t := bytes.TrimSpace(line); len(t) > 0 {
				var e events.Event
				if json.Unmarshal(t, &e) != nil || e.Type == "" {
					bad = append(bad, lineNo)
					lastBad = lineNo
				} else {
					evs = append(evs, e)
					lastBad = 0
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, false, nil, rerr
		}
	}
	// An unparseable final line is a torn write, not corruption.
	if lastBad != 0 && lastBad == bad[len(bad)-1] && lastBad == lineNo {
		bad = bad[:len(bad)-1]
		torn = true
	}
	return evs, torn, bad, nil
}

func newRun(evs []events.Event, blobs events.Blobs) *Run {
	r := &Run{
		blobs:     blobs,
		reqs:      map[string]*reqInfo{},
		resps:     map[string]*respInfo{},
		modelErrs: map[string][]int{},
		msgMemo:   map[string][]core.Hash{},
		msgErr:    map[string]error{},
		toolsMem:  map[core.Hash][]core.ToolSpec{},
		sysMem:    map[core.Hash]core.Block{},
		msgMem:    map[core.Hash]core.Message{},
	}
	r.evs = r.normalize(evs)
	r.index()
	return r
}

// normalize orders events by sequence number and drops exact duplicates (the
// same record appended twice by a resumed writer). A log where any event lacks a
// sequence number is numbered in the order given: the caller's order is all the
// information there is.
func (r *Run) normalize(evs []events.Event) []events.Event {
	seqd := len(evs) > 0
	for _, e := range evs {
		if e.Seq == 0 {
			seqd = false
			break
		}
	}
	if seqd {
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].Seq < evs[j].Seq })
	} else {
		// Every derivation below compares positions in the log, so events get
		// numbers in the order they were handed over.
		for i := range evs {
			evs[i].Seq = uint64(i + 1)
		}
	}
	out := evs[:0:0]
	for i, e := range evs {
		if i > 0 && e.Seq != 0 && e.Seq == evs[i-1].Seq && e.Type == evs[i-1].Type && e.Agent == evs[i-1].Agent && bytes.Equal(e.Data, evs[i-1].Data) {
			r.dups++ // an identical copy carries no new information and is harmless
			continue
		}
		out = append(out, e)
	}
	return out
}

// index parses the model traffic events once.
func (r *Run) index() {
	for i, e := range r.evs {
		switch e.Type {
		case events.TypeModelRequest:
			r.indexRequest(i, e)
		case events.TypeModelResponse:
			r.indexResponse(i, e)
		case events.TypeModelError:
			var p struct {
				Req string `json:"req"`
			}
			if json.Unmarshal(e.Data, &p) == nil && p.Req != "" {
				r.modelErrs[p.Req] = append(r.modelErrs[p.Req], i)
			}
		}
	}
	sort.SliceStable(r.reqOrder, func(i, j int) bool { return r.reqOrder[i].idx < r.reqOrder[j].idx })
}

func (r *Run) indexRequest(i int, e events.Event) {
	var p struct {
		Req      string          `json:"req"`
		Agent    string          `json:"agent"`
		Role     string          `json:"role"`
		Kind     string          `json:"kind"`
		Model    string          `json:"model"`
		Renderer string          `json:"renderer"`
		Hot      core.Hash       `json:"hot"`
		Params   json.RawMessage `json:"params"`
		Manifest *core.Manifest  `json:"manifest"`
		Wire     core.Hash       `json:"wire_hash"`
	}
	if err := json.Unmarshal(e.Data, &p); err != nil {
		r.issues = append(r.issues, Mismatch{Kind: KindLog, Detail: fmt.Sprintf("seq %d: model.request payload: %v", e.Seq, err)})
		return
	}
	if p.Req == "" {
		r.issues = append(r.issues, Mismatch{Kind: KindLog, Detail: fmt.Sprintf("seq %d: model.request without a req id", e.Seq)})
		return
	}
	if _, dup := r.reqs[p.Req]; dup {
		r.issues = append(r.issues, Mismatch{Req: p.Req, Kind: KindLog, Detail: fmt.Sprintf("seq %d: request id already used; the first one is kept", e.Seq)})
		return
	}
	noManifest := p.Manifest == nil
	if noManifest {
		p.Manifest = &core.Manifest{}
	}
	if p.Agent == "" {
		p.Agent = e.Agent
	}
	if p.Kind == "" {
		p.Kind = rl.KindMain
	}
	if p.Model == "" {
		p.Model = p.Manifest.Model
	}
	q := &reqInfo{
		id: p.Req, seq: e.Seq, idx: i, ts: e.TS, agent: p.Agent, role: p.Role, kind: p.Kind, model: p.Model,
		renderer: p.Renderer, hot: p.Hot, params: p.Params, man: *p.Manifest, wire: p.Wire, noManifest: noManifest,
	}
	r.reqs[q.id] = q
	r.reqOrder = append(r.reqOrder, q)
}

func (r *Run) indexResponse(i int, e events.Event) {
	var p struct {
		Req          string          `json:"req"`
		Model        string          `json:"model"`
		Usage        core.Usage      `json:"usage"`
		CostUSD      float64         `json:"cost_usd"`
		HitRatio     float64         `json:"hit_ratio"`
		ExpectedRead int             `json:"expected_read"`
		Anomaly      bool            `json:"anomaly"`
		Stop         core.StopReason `json:"stop"`
		TotalMs      int64           `json:"total_ms"`
		Side         bool            `json:"side"`
		Completion   core.Hash       `json:"completion"`
		Tokens       core.Hash       `json:"tokens"`
	}
	if err := json.Unmarshal(e.Data, &p); err != nil {
		r.issues = append(r.issues, Mismatch{Kind: KindLog, Detail: fmt.Sprintf("seq %d: model.response payload: %v", e.Seq, err)})
		return
	}
	if p.Req == "" {
		r.issues = append(r.issues, Mismatch{Kind: KindLog, Detail: fmt.Sprintf("seq %d: model.response without a req id", e.Seq)})
		return
	}
	if _, dup := r.resps[p.Req]; dup {
		r.issues = append(r.issues, Mismatch{Req: p.Req, Kind: KindLog, Detail: fmt.Sprintf("seq %d: second response for one request; the first one is kept", e.Seq)})
		return
	}
	s := &respInfo{
		id: p.Req, seq: e.Seq, idx: i, ts: e.TS, model: p.Model, usage: p.Usage, costUSD: p.CostUSD, hitRatio: p.HitRatio,
		expected: p.ExpectedRead, anomaly: p.Anomaly, stop: p.Stop, totalMs: p.TotalMs, side: p.Side,
		completion: p.Completion, tokens: p.Tokens,
	}
	r.resps[s.id] = s
	r.respOrder = append(r.respOrder, s)
}

// Task returns the task recorded next to the run (task.json), if any.
func (r *Run) Task() *rl.Task { return r.task }

// Torn reports whether the last line of the log was cut off and ignored.
func (r *Run) Torn() bool { return r.torn }

// Events returns the ordered events of the run. The slice is shared: callers must
// not modify it.
func (r *Run) Events() []events.Event { return r.evs }

// WorkspaceRoots lists the directories the run worked in, as its log recorded them: where each session ran (session.start:
// cwd and root) and every tree a worker was given (workspace.create). Only absolute paths count, each once, in log order.
// Scoring hands them to the hack detector, which cannot otherwise tell a workspace below a hidden directory of the home
// (~/.sleipnir-bench/work/ws/...) from the agent writing into the home's dotfiles.
func (r *Run) WorkspaceRoots() []string {
	var roots []string
	add := func(p string) {
		if p == "" {
			return
		}
		if p = filepath.Clean(p); filepath.IsAbs(p) && p != "/" && !slices.Contains(roots, p) {
			roots = append(roots, p)
		}
	}
	for _, e := range r.evs {
		switch e.Type {
		case events.TypeSessionStart:
			var d struct{ Cwd, Root string }
			if json.Unmarshal(e.Data, &d) == nil {
				add(d.Cwd)
				add(d.Root)
			}
		case events.TypeWorkspaceCreate:
			var d struct{ Path string }
			if json.Unmarshal(e.Data, &d) == nil {
				add(d.Path)
			}
		}
	}
	return roots
}

// blob returns the bytes stored under h after checking they hash to h.
func (r *Run) blob(h core.Hash) ([]byte, error) {
	if h == "" {
		return nil, errors.New("empty blob hash")
	}
	b, err := r.blobs.Get(h)
	if err != nil {
		if errors.Is(err, events.ErrBlobNotFound) {
			return nil, fmt.Errorf("blob %s is missing", h.Short())
		}
		return nil, fmt.Errorf("blob %s: %w", h.Short(), err)
	}
	if core.HashBytes(b) != h {
		return nil, fmt.Errorf("blob %s is corrupt (content hashes to %s)", h.Short(), core.HashBytes(b).Short())
	}
	return b, nil
}

// account adds n bytes to the decoded-cache budget; callers hold r.mu.
func (r *Run) account(n int) {
	r.memBytes += n
	if r.memBytes > maxMemBytes {
		r.toolsMem = map[core.Hash][]core.ToolSpec{}
		r.sysMem = map[core.Hash]core.Block{}
		r.msgMem = map[core.Hash]core.Message{}
		r.memBytes = n
	}
}

// Resolver adapts a Run to the prompt-resolver interface of package export
// (Prompt(ep, st)), without either package importing the other.
type Resolver struct{ Run *Run }

// Prompt returns the exact prompt of the step's request.
func (x Resolver) Prompt(_ *rl.Episode, st *rl.Step) (*core.Prompt, error) {
	return x.Run.Prompt(st.Prompt.Req)
}
