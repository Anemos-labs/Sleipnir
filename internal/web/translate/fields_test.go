package translate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// required lists, per kind, the fields every event of the kind carries: what the page's reducer (30-model.js) and the views that
// read the log (Replay) read. A dotted name is a field of an object field.
var required = map[string][]string{
	"say":       {"who", "text"},
	"sys":       {"ch", "glyph", "text"},
	"tool":      {"id", "name", "arg", "out", "ok"},
	"note":      {"id", "g", "text"},
	"state":     {"id", "s", "doing", "task"},
	"task":      {"id"},
	"plan":      {"steps", "st"},
	"verdict":   {"text"},
	"req":       {"id", "ratio"},
	"use":       {"id", "rd", "un", "out"},
	"warm":      {},
	"gov":       {"rpm", "r429", "retries"},
	"mail":      {"from", "to", "text"},
	"ckpt":      {"cid", "ts", "files", "note"},
	"ask":       {"q.id", "q.agent", "q.cmd", "q.cwd", "q.why", "q.what", "q.kind"},
	"answer":    {"qid", "choice", "by"},
	"queue":     {"head", "conflicts", "bounced"},
	"merge":     {"id", "cmd", "ms"},
	"break":     {"id", "kind", "read", "expected", "why"},
	"compact":   {"id", "from", "to", "pct"},
	"stream":    {"id", "text", "rate", "mid"},
	"diff":      {"file", "done"},
	"goal":      {"s"},
	"final":     {},
	"steer":     {"to", "text"},
	"interrupt": {"id"},
	"refuse":    {"id", "name", "arg", "reason"},
	"more":      {"mid", "text"},
	"turn":      {"s"},
	"stall":     {"kind", "s", "text"},
	"handover":  {"task", "from", "s"},
	"layers":    {"id", "toks"},
	"alert":     {"s", "kind"},
	"mailstat":  {"sent", "delivered", "dropped"},
	"svc":       {"rd", "un", "out", "wr", "cost", "saved"},
}

// notProduced are the kinds of the reference page that the server never sends: the page makes them itself, or they belong to a
// scripted reply.
var notProduced = map[string]bool{"local": true, "digest": true, "reply": true}

// has reports whether the event has the dotted field.
func has(e map[string]any, field string) bool {
	head, rest, nested := strings.Cut(field, ".")
	v, ok := e[head]
	if !ok {
		return false
	}
	if !nested {
		return true
	}
	m, ok := v.(map[string]any)
	return ok && has(m, rest)
}

// checkStream checks what every journal must keep: consecutive seqs from the first, t never decreasing, a known kind on every
// event, the fields the page reads, and a say of the manager or a stream that names its message.
func checkStream(tb testing.TB, raws []wire.Raw) {
	tb.Helper()
	evs := decodeAll(tb, raws)
	var lastSeq, lastT float64
	for i, e := range evs {
		k, _ := e["k"].(string)
		fields, known := required[k]
		if !known || notProduced[k] {
			tb.Fatalf("event %d has the kind %q, which the server does not send: %s", i, k, raws[i])
		}
		for _, f := range fields {
			if !has(e, f) {
				tb.Fatalf("event %d (%s) lacks %q: %s", i, k, f, raws[i])
			}
		}
		seq, _ := e["seq"].(float64)
		t, _ := e["t"].(float64)
		if i > 0 && seq != lastSeq+1 {
			tb.Fatalf("event %d: seq %v follows %v", i, seq, lastSeq)
		}
		if t < lastT || t < 0 {
			tb.Fatalf("event %d: t %v after %v", i, t, lastT)
		}
		lastSeq, lastT = seq, t
	}
}

// caseRE finds the cases of the reducer's switch; evRE the fields of the event a case reads.
var (
	caseRE = regexp.MustCompile(`case '([a-z]+)':`)
	evRE   = regexp.MustCompile(`\bev\.([a-zA-Z0-9_]+)(?:\.([a-zA-Z0-9_]+))?`)
)

// readFields parses the page's reducer (the reduce function of 30-model.js) into the event fields each kind's case reads.
func readFields(t *testing.T) map[string][]string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "ui", "js", "30-model.js"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "function reduce(")
	end := strings.Index(body[start:], "\n  function reduceRange")
	if start < 0 || end < 0 {
		t.Fatal("30-model.js: the reduce function was not found")
	}
	body = body[start : start+end]
	idx := caseRE.FindAllStringSubmatchIndex(body, -1)
	out := map[string][]string{}
	for i, m := range idx {
		kind := body[m[2]:m[3]]
		stop := len(body)
		if i+1 < len(idx) {
			stop = idx[i+1][0]
		}
		seen := map[string]bool{}
		for _, f := range evRE.FindAllStringSubmatch(body[m[1]:stop], -1) {
			name := f[1]
			if name == "q" && f[2] != "" {
				name = "q." + f[2]
			}
			if !seen[name] {
				seen[name] = true
				out[kind] = append(out[kind], name)
			}
		}
		sort.Strings(out[kind])
	}
	return out
}

// absent are the fields the reducer reads that the server leaves out on purpose, with the reason: a field that is
// optional (read with a default), or one of a form the server does not produce.
var absent = map[string]string{
	"say.lines": "scouts rows are not produced", "say.title": "local rows are page-made", "say.html": "local rows are page-made",
	"say.glyph": "optional (◇ by default)", "say.plan": "optional", "say.task": "optional", "say.stream": "the manager's live say only", "say.rate": "streamed says only",
	"sys.ag": "optional", "sys.task": "optional",
	"tool.refused": "refusals only", "tool.reason": "refusals only", "tool.file": "file tools only", "tool.add": "file tools only",
	"tool.del": "file tools only", "tool.task": "an agent with a task only", "tool.ok": "",
	"note.task": "optional", "task.title": "the creating event", "task.owner": "an owned task", "task.deps": "a task with deps",
	"task.scope": "", "task.s": "", "plan.n": "the single-index form is not produced", "plan.s": "the single-index form is not produced",
	"req.p": "live requests only", "req.o": "live requests only", "req.hist": "keyframes and history only",
	"ckpt.step": "pack change sets are not produced", "ckpt.id": "the cid form is sent", "ckpt.skipped": "optional (false)", "ckpt.safety": "optional (false)",
	"queue.cmd": "a queued submission only", "queue.step": "a queued submission only", "queue.ms": "a verified submission only",
	"stream.code": "code streams only", "goal.paused": "a paused goal only", "goal.turns": "a goal that has run continuation turns (omitempty; TestAGoalCarriesItsTurnsAndReason)", "goal.reason": "a goal the judge has given a reason for (omitempty; TestAGoalCarriesItsTurnsAndReason)", "say.open": "host rows only", "use.savedPartial": "reads at unknown prices only (TestHonestPrices)", "use.unpriced": "reads at unknown prices only (TestHonestPrices)", "sys.open": "host rows only", "alert.text": "raises only", "alert.key": "optional", "steer.quiet": "optional", "answer.note": "optional", "mail.id": "",
}

// Every field the page's reducer reads of an event is in what the server sends, or is listed with the reason it may be absent: the
// reducer of the shipped page is parsed, so a field a patch of it starts to read fails this test until the server sends it.
func TestEveryFieldTheReducerReadsIsProduced(t *testing.T) {
	reads := readFields(t)
	if len(reads) < 25 {
		t.Fatalf("parsed only %d cases of the reducer: %v", len(reads), reads)
	}
	corpus := everyKindCorpus(t)
	byKind := map[string][]map[string]any{}
	for _, e := range corpus {
		k, _ := e["k"].(string)
		byKind[k] = append(byKind[k], e)
	}
	for kind, fields := range reads {
		if notProduced[kind] {
			continue
		}
		evs := byKind[kind]
		if len(evs) == 0 {
			t.Errorf("the reducer handles %q and no test produced one", kind)
			continue
		}
		for _, f := range fields {
			if _, ok := absent[kind+"."+f]; ok || f == "at" {
				continue // at is on history events only
			}
			found := false
			for _, e := range evs {
				if has(e, f) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("the reducer reads %s.%s and no %s event carries it", kind, f, kind)
			}
		}
	}
}

// Every kind of the vocabulary is produced by some test (the 30 kinds of the reference page but local, digest and reply, and the
// additive ones).
func TestEveryKindIsCovered(t *testing.T) {
	corpus := everyKindCorpus(t)
	seen := map[string]int{}
	for _, e := range corpus {
		k, _ := e["k"].(string)
		seen[k]++
	}
	for kind := range required {
		if seen[kind] == 0 {
			t.Errorf("no test produced a %q event", kind)
		}
	}
	for kind := range notProduced {
		if seen[kind] > 0 {
			t.Errorf("a %q event was produced", kind)
		}
	}
	b, _ := json.Marshal(seen)
	t.Logf("kinds produced: %s", b)
}
