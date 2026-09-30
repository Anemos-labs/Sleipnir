package state

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/cost"
)

// An oracle is what a log says, counted the plain way: one pass over its lines decoded into generic maps, with none of the State's
// own wire types, rules or limits. A figure that the State and the oracle both arrive at, by ways that share nothing but the log,
// is a figure that two readings of it agree on; a disagreement is a bug in one of them, and it is the State's until shown otherwise.
// The price of a model is the one thing they must agree to take from the same place, and the test says where.

type obj map[string]any

func (o obj) str(k string) string { s, _ := o[k].(string); return s }
func (o obj) num(k string) float64 {
	f, _ := o[k].(float64)
	return f
}
func (o obj) flag(k string) bool { b, _ := o[k].(bool); return b }
func (o obj) sub(k string) obj   { m, _ := o[k].(map[string]any); return obj(m) }

// decodeLog reads every line of a log into a map.
func decodeLog(t testing.TB, log []byte) []obj {
	t.Helper()
	var out []obj
	for n, line := range bytes.Split(log, []byte{'\n'}) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var o obj
		if err := json.Unmarshal(line, &o); err != nil {
			t.Fatalf("line %d of the log is not JSON: %v", n+1, err)
		}
		out = append(out, o)
	}
	return out
}

// prices says what a model's tokens cost: its input price and its cache-read price per million tokens, and whether it is known.
type prices func(model string) (in, read float64, ok bool)

// recordedPrices are the prices the session wrote into its own session.start: what it was billed by.
func recordedPrices(evs []obj) prices {
	known := map[string][2]float64{}
	for _, e := range evs {
		if e.str("type") != "session.start" {
			continue
		}
		for id, v := range e.sub("data").sub("models") {
			m, _ := v.(map[string]any)
			if obj(m).str("source") != "fallback" && obj(m).num("input_per_m") > 0 {
				known[id] = [2]float64{obj(m).num("input_per_m"), obj(m).num("cache_read_per_m")}
			}
		}
	}
	return func(model string) (float64, float64, bool) {
		p, ok := known[model]
		return p[0], p[1], ok
	}
}

// tablePrices are the repository's own list prices (internal/cost), by model id.
func tablePrices() prices {
	tab := cost.Defaults()
	return func(model string) (float64, float64, bool) {
		m, ok := tab.Lookup(model)
		return m.Price.InputPerM, m.Price.CacheReadPerM, ok
	}
}

// nobody knows no model's price.
func nobody(string) (float64, float64, bool) { return 0, 0, false }

type tally struct{ in, read, write, out int64 }

func (a tally) prompt() int64 { return a.in + a.read + a.write }

// oracle is what a log says.
type oracle struct {
	events int
	count  map[string]int // by event type
	spawn  []string       // the agents spawned, in order
	role   map[string]string

	mainReq, mainResp, sideResp map[string]int // by agent
	tok                         map[string]*tally
	total                       tally
	costBy                      map[string]float64
	totalCost                   float64
	lastRatio                   map[string]float64 // the hit ratio of each agent's newest main response

	saved               float64
	savedOf             map[string]float64
	priced, unpriced    int64
	calls, callErrors   map[string]int
	commits, anomalies  map[string]int
	nudges, stops       map[string]int
	sent, delivered     int
	direct, parcels     int // messages delivered without a digest; parcels that digests stand for
	retries, rateLimits int
	errors, cancelled   int
	tasks               map[string]string // the status each task ended in
	taskVer             map[string]float64
	merge               map[string]int  // by event type (merge.*)
	actors              map[string]bool // the agents that wrote events, where the log has no agent.spawn (a session of one agent)

	// agent.cancel: the runs cancelled, by agent and by phase; perm.ask and perm.decide: the questions put and how they were settled
	// (by the "by" word), and the questions that no decision answered (a decision that matches no question, a refusal by policy, is
	// not counted against any).
	cancels, cancelPhase             map[string]int
	asked, allowed, denied           int
	permBy                           map[string]int
	unasked, unanswered              int
	lastCancelPhase, lastCancelCause map[string]string
	pend                             map[string]int
}

// permKey is what tells one request of the permission engine from another in either of its events: who, what tool, what command and
// which paths.
func permKey(agent string, d obj) string {
	ps, _ := d["paths"].([]any)
	b, _ := json.Marshal(ps)
	return agent + "\x00" + d.str("tool") + "\x00" + d.str("command") + "\x00" + string(b)
}

func readOracle(t testing.TB, log []byte, price prices) *oracle {
	t.Helper()
	evs := decodeLog(t, log)
	o := &oracle{
		count: map[string]int{}, role: map[string]string{},
		mainReq: map[string]int{}, mainResp: map[string]int{}, sideResp: map[string]int{}, tok: map[string]*tally{},
		costBy: map[string]float64{}, lastRatio: map[string]float64{}, savedOf: map[string]float64{},
		calls: map[string]int{}, callErrors: map[string]int{}, commits: map[string]int{}, anomalies: map[string]int{},
		nudges: map[string]int{}, stops: map[string]int{}, tasks: map[string]string{}, taskVer: map[string]float64{}, merge: map[string]int{},
		cancels: map[string]int{}, cancelPhase: map[string]int{}, permBy: map[string]int{}, pend: map[string]int{}, actors: map[string]bool{},
		lastCancelPhase: map[string]string{}, lastCancelCause: map[string]string{},
	}
	kind := map[string]string{} // agent + "/" + req -> the kind of the request
	for _, e := range evs {
		typ, agent, d := e.str("type"), e.str("agent"), e.sub("data")
		o.events++
		o.count[typ]++
		if agent != "" && agent != "swarm" && agent != "harness" && agent != "curator" {
			o.actors[agent] = true
		}
		switch typ {
		case "agent.spawn":
			id := d.str("id")
			o.spawn = append(o.spawn, id)
			o.role[id] = d.str("role")
		case "model.request":
			kind[agent+"/"+d.str("req")] = d.str("kind")
			if d.str("kind") == "main" {
				o.mainReq[agent]++
			}
		case "model.response":
			u := d.sub("usage")
			tl := tally{int64(u.num("input_tokens")), int64(u.num("cache_read_tokens")),
				int64(u.num("cache_write_5m_tokens") + u.num("cache_write_1h_tokens")), int64(u.num("output_tokens"))}
			if o.tok[agent] == nil {
				o.tok[agent] = &tally{}
			}
			o.tok[agent].in += tl.in
			o.tok[agent].read += tl.read
			o.tok[agent].write += tl.write
			o.tok[agent].out += tl.out
			o.total.in += tl.in
			o.total.read += tl.read
			o.total.write += tl.write
			o.total.out += tl.out
			o.costBy[agent] += d.num("cost_usd")
			o.totalCost += d.num("cost_usd")
			if in, rd, ok := price(d.str("model")); ok {
				v := float64(tl.read) * (in - rd) / 1e6
				o.saved += v
				o.savedOf[agent] += v
				o.priced += tl.read
			} else {
				o.unpriced += tl.read
			}
			if kind[agent+"/"+d.str("req")] == "main" {
				o.mainResp[agent]++
				if p := tl.prompt(); p > 0 {
					o.lastRatio[agent] = float64(tl.read) / float64(p)
				} else {
					o.lastRatio[agent] = 0
				}
			} else {
				o.sideResp[agent]++
			}
		case "model.error":
			if d.num("attempt") > 0 || d.num("delay_ms") > 0 {
				o.retries++
				if d.str("kind") == "rate_limit" || d.num("status") == 429 {
					o.rateLimits++
				}
			} else if msg := d.str("error"); strings.Contains(msg, "context canceled") || strings.HasSuffix(msg, "request cancelled") {
				o.cancelled++ // a request cut off because its run was stopped did not fail
			} else {
				o.errors++
			}
		case "tool.call":
			o.calls[agent]++
		case "tool.result":
			if d.flag("error") {
				o.callErrors[agent]++
			}
		case "compact.commit":
			o.commits[agent]++
		case "cache.anomaly":
			o.anomalies[agent]++
		case "agent.stuck":
			if d.str("phase") == "stop" {
				o.stops[agent]++
			} else {
				o.nudges[agent]++
			}
		case "agent.cancel":
			o.cancels[agent]++
			o.cancelPhase[d.str("phase")]++
			o.lastCancelPhase[agent], o.lastCancelCause[agent] = d.str("phase"), d.str("cause")
		case "perm.ask":
			o.asked++
			o.pend[permKey(agent, d)]++
		case "perm.decide":
			if d.flag("allow") {
				o.allowed++
			} else {
				o.denied++
			}
			o.permBy[d.str("by")]++
			if k := permKey(agent, d); o.pend[k] > 0 {
				o.pend[k]--
			} else {
				o.unasked++
			}
		case "mail.send":
			o.sent++
		case "mail.deliver":
			o.delivered++
		case "mail.direct":
			n := int(d.num("n"))
			if ids, _ := d["ids"].([]any); n <= 0 {
				n = max(len(ids), 1)
			}
			o.direct += n
		case "mail.digest":
			if ps, _ := d["parcels"].([]any); len(ps) > 0 {
				o.parcels += len(ps)
			}
		case "board.op":
			// A task's last word is the op with the highest board version that names it and carries a status.
			if id := d.str("task"); id != "" && d.str("op") != "agent" && d.str("status") != "" {
				if v := d.num("version"); v >= o.taskVer[id] {
					o.taskVer[id], o.tasks[id] = v, d.str("status")
				}
			}
		}
		if strings.HasPrefix(typ, "merge.") {
			o.merge[typ]++
		}
	}
	for _, n := range o.pend {
		o.unanswered += n
	}
	return o
}

// sum adds up a per-agent count.
func sum(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// rewriteLog applies edit to every event of a log and writes the log again: the way a test makes a variant of a real recording
// (another model name, prices left out) without touching the producers.
func rewriteLog(t testing.TB, log []byte, edit func(e obj)) []byte {
	t.Helper()
	var out bytes.Buffer
	for _, e := range decodeLog(t, log) {
		edit(e)
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		out.Write(b)
		out.WriteByte('\n')
	}
	return out.Bytes()
}
