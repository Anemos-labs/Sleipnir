package translate

import (
	"sort"
	"time"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// deriveState is everything the translator remembers between events to derive the next ones. It is guarded by the translator's
// lock. Every map is bounded: by the agents and tasks of the session (themselves bounded by internal/tui/state), by the open tool
// calls and questions, or by an explicit cap.
type deriveState struct {
	ags      map[string]*agentOut // by UI id
	ros      rosterOut
	hid      map[string]string   // UI id -> the harness's id (mgr -> main for a single agent)
	msgs     map[string]*message // the open prose message of an agent (UI id)
	codes    map[string]*message // the open code stream of a write (tool call id)
	runs     map[string]*toolRun // open tool calls seen through the sink (tool call id)
	qs       map[string]*openQ   // open questions (question id)
	qorder   []string
	reqSide  map[string]bool // agent|req -> a side request (compactor)
	tasks    map[string]*taskOut
	queue    queueOut
	goalKey  string
	verdKey  string
	warm     time.Time
	gov      govOut
	g0       int
	gate     noticeGate
	dedupe   noticeDedupe
	lastErr  map[string]string
	hold     map[string]holdInfo  // UI id -> a sink tool start the log has not caught up with
	pend     map[string]*pendTool // log-only tool rows waiting for their output (tool call id)
	hist     []string             // the person's lines
	ckpts    map[string]*ckptOut  // cid
	ckptSeen map[string]bool      // cid -> a ckpt event was journalled (the first is critical)
	mstat    MailStat
	mstatOK  bool
	digests  map[string]digestInfo // a mailman digest's mail.send, until its mail.digest
	full     float64               // the session time of the last full pass over the agents

	logOnly    bool // everything comes from the log (a followed log, a resumed session's history)
	history    bool // translating the history of a resumed log: t 0, at the event's time
	histBuf    []histEntry
	histDrop   int
	histN      int    // history entries ever put: the absolute index of the next one
	histSlot   int    // when not negative, the absolute index the next history event fills (a tool row's place, kept at its call)
	logSeq     uint64 // the last log seq applied
	logPath    string
	logFlush   func() error
	noGapCheck bool // the next log event sets logSeq without a gap check (history was skipped)
	detach     func()
	stopFollow func()
	lastTS     time.Time // the newest log event's time
	turnOpen   bool      // a turn of the main agent is running
	root       string    // the project root a log-only translation learned from session.start (Config.Root unset)
	ended      bool      // the log's last run ended (session.end, with no session.start after it)
	lastWall   time.Time // when a followed log last gave an event (the server's clock)
	permMode   string    // the last perm.state of the log: the permission mode
	permRules  int       // and how many allow rules
}

// histEntry is a history event waiting to be journalled at the end of the history (its seq is not known before).
type histEntry struct {
	e   wire.Event
	at  int64
	pre func(seq uint64)
}

// maxDigests bounds the mailman digests waiting for their mail.digest; maxPend the log-only tool rows waiting for their output.
const (
	maxDigests = 256
	maxPend    = 1024
	maxHist    = 200
)

// init makes the maps.
func (d *deriveState) init() {
	d.ags = map[string]*agentOut{}
	d.ros.ents = map[string]*wire.RosterEntry{}
	d.hid = map[string]string{}
	d.msgs = map[string]*message{}
	d.codes = map[string]*message{}
	d.runs = map[string]*toolRun{}
	d.qs = map[string]*openQ{}
	d.reqSide = map[string]bool{}
	d.tasks = map[string]*taskOut{}
	d.queue.at = map[string]time.Time{}
	d.queue.ms = map[string]int64{}
	d.queue.vfails = map[string]int{}
	d.lastErr = map[string]string{}
	d.hold = map[string]holdInfo{}
	d.pend = map[string]*pendTool{}
	d.ckpts = map[string]*ckptOut{}
	d.ckptSeen = map[string]bool{}
	d.digests = map[string]digestInfo{}
	d.dedupe.n = map[string]*dedupeEntry{}
	d.histSlot = -1
}

// histPut keeps a history event, the newest max of them; it returns the event's absolute index. An event put while histSlot is set
// fills that place instead (when it is still kept).
func (d *deriveState) histPut(h histEntry, max int) int {
	if d.histSlot >= 0 {
		at := d.histSlot - (d.histN - len(d.histBuf))
		d.histSlot = -1
		if at >= 0 && at < len(d.histBuf) {
			d.histBuf[at] = h
		}
		return -1
	}
	d.histBuf = append(d.histBuf, h)
	d.histN++
	if len(d.histBuf) >= 2*max {
		d.histDrop += len(d.histBuf) - max
		d.histBuf = append(d.histBuf[:0], d.histBuf[len(d.histBuf)-max:]...)
	}
	return d.histN - 1
}

// addHist remembers a line the person sent, the newest maxHist.
func (d *deriveState) addHist(s string) {
	if s == "" {
		return
	}
	d.hist = append(d.hist, s)
	if len(d.hist) > maxHist {
		d.hist = append(d.hist[:0], d.hist[len(d.hist)-maxHist:]...)
	}
}

// sortedKeys lists a map's keys in order, so that whatever the translator does for each entry of a map it does in the same order on
// every run.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// root is the project root paths are shown relative to: Config.Root, else the root the log's session.start named.
func (t *Translator) root() string { return firstNonEmpty(t.cfg.Root, t.d.root) }
