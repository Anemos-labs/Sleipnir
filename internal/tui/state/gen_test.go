package state

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/tui/state/statetest"
)

// genEvents makes a deterministic log of n events of every kind the State knows, over more agents, tasks, prefixes, models, files
// and messages than any cap allows, with valid payloads mostly and a few broken ones. It writes payloads as text, so making a
// few hundred thousand of them costs next to nothing.
func genEvents(n int, seed int64) []events.Event {
	r := rand.New(rand.NewSource(seed))
	t := statetest.Epoch
	evs := make([]events.Event, 0, n)
	agent := func() string {
		if r.Intn(10) == 0 {
			return "a-0" // one agent that does a tenth of everything, so that its rings fill
		}
		return fmt.Sprintf("a-%d", r.Intn(700))
	}
	task := func() string { return fmt.Sprintf("T%d", 1+r.Intn(3000)) }
	model := func() string { return fmt.Sprintf("model-%d", r.Intn(120)) }
	prefix := func() string { return fmt.Sprintf("prefix-%d-0123456789", r.Intn(200)) }
	sections := func() string {
		return fmt.Sprintf(`[{"name":"shared","hash":"h%d","tokens":%d},{"name":"role","hash":"r%d","tokens":%d},{"name":"notes","hash":"n","tokens":%d,"bp":true}]`,
			r.Intn(5), 50+r.Intn(200), r.Intn(9), 20+r.Intn(100), r.Intn(300))
	}
	add := func(ag, typ, data string) {
		var raw json.RawMessage
		if data != "" {
			raw = json.RawMessage(data)
		}
		evs = append(evs, events.Event{Seq: uint64(len(evs) + 1), TS: t, Session: "gen", Agent: ag, Type: typ, Data: raw})
	}
	add("", events.TypeSessionStart, `{"model":"model-1","swarm":true,"root":"/r","models":{"model-1":{"source":"given","input_per_m":3,"cache_read_per_m":0.3,"output_per_m":15,"cache_write_5m_per_m":3.75,"cache_write_1h_per_m":6,"ttl_s":300}}}`)
	for len(evs) < n {
		t = t.Add(time.Duration(r.Intn(400)) * time.Millisecond)
		a := agent()
		switch k := r.Intn(100); {
		case k < 4:
			add("swarm", events.TypeAgentSpawn, fmt.Sprintf(`{"id":%q,"role":"role%d","task":%q,"by":"a-0","parent":"a-0","model":%q}`, a, r.Intn(6), task(), model()))
		case k < 7:
			add(a, events.TypeAgentState, fmt.Sprintf(`{"id":%q,"state":%q,"line":"doing %d","task":%q}`, a, []string{"running", "idle", "failed", "done", "waiting"}[r.Intn(5)], r.Intn(99), task()))
		case k < 8:
			add(a, events.TypeAgentEnd, fmt.Sprintf(`{"id":%q,"state":%q,"evidence":"edited %d files"}`, a, []string{"idle", "failed"}[r.Intn(2)], r.Intn(9)))
		case k < 20:
			req := fmt.Sprintf("%s.%d", a, r.Intn(50))
			kind := []string{"main", "main", "main", "compactor"}[r.Intn(4)]
			add(a, events.TypeModelRequest, fmt.Sprintf(`{"req":%q,"agent":%q,"role":"role1","kind":%q,"model":%q,"provider":"p","dialect":"d","sections":%s,"thread_from":1,"thread_to":%d,"prefix_key":%q,"cache_key":"sl:x:%d","breakpoints":[{"label":"shared","ttl":3600000000000},{"label":"thread"}],"shared_blocks":%d,"shared_tokens":%d,"tools":17}`,
				req, a, kind, model(), sections(), r.Intn(99), prefix(), r.Intn(9), r.Intn(50), r.Intn(9000)))
		case k < 32:
			req := fmt.Sprintf("%s.%d", a, r.Intn(50))
			add(a, events.TypeModelResponse, fmt.Sprintf(`{"req":%q,"model":%q,"usage":{"input_tokens":%d,"cache_read_tokens":%d,"cache_write_5m_tokens":%d,"output_tokens":%d},"cost_usd":%g,"expected_read":%d,"missed":%d,"anomaly":%v,"stop":%q,"total_ms":%d,"side":%v}`,
				req, model(), r.Intn(5000), r.Intn(20000), r.Intn(300), r.Intn(800), r.Float64()/50, r.Intn(9000), r.Intn(2000), r.Intn(10) == 0, []string{"tool_use", "end_turn"}[r.Intn(2)], r.Intn(5000), r.Intn(8) == 0))
		case k < 34:
			add(a, events.TypeModelError, fmt.Sprintf(`{"req":"%s.%d","kind":"rate_limit","status":429,"attempt":%d,"delay_ms":%d,"error":"boom"}`, a, r.Intn(50), r.Intn(3), r.Intn(9000)))
		case k < 52:
			add(a, events.TypeToolCall, fmt.Sprintf(`{"id":"c%d","name":%q,"input":{"command":"go test ./%d","path":"p/%d.go","pattern":"x"}}`, r.Intn(30), []string{"bash", "read", "edit", "wait", "grep", "task", "mail"}[r.Intn(7)], r.Intn(99), r.Intn(99)))
		case k < 68:
			add(a, events.TypeToolResult, fmt.Sprintf(`{"id":"c%d","name":"bash","error":%v,"chars":12,"ms":%d}`, r.Intn(30), r.Intn(5) == 0, r.Intn(3000)))
		case k < 72:
			ops := []string{"create", "claim", "assign", "update", "finish", "block", "resume", "requeue"}
			st := []string{"todo", "doing", "blocked", "review", "done", "failed"}
			add("mgr", events.TypeBoardOp, fmt.Sprintf(`{"op":%q,"version":%d,"task":%q,"status":%q,"owner":%q,"line":"x","result":"r","evidence":"e","attempts":%d,"rev":%d,"files":["a/%d/**","b"],"title":"task title %d","role":"backend","deps":["T1"]}`,
				ops[r.Intn(len(ops))], len(evs), task(), st[r.Intn(len(st))], agent(), r.Intn(3), len(evs), r.Intn(50), r.Intn(99)))
		case k < 73:
			add("mgr", events.TypeBoardOp, fmt.Sprintf(`{"op":"agent","version":%d,"agent":%q,"role":"role2","state":"running","task":%q,"line":"x","ctx_tokens":%d,"cost_usd":0.1}`, len(evs), a, task(), r.Intn(90000)))
		case k < 74:
			add("harness", events.TypeBoardOp, fmt.Sprintf(`{"op":%q,"version":%d,"kind":"lease","text":"warn %d","key":"k%d","note":%d,"evicted":[%d],"notes":[%d],"dropped":1,"n":2}`, []string{"alert", "alert-clear", "alert-expire", "note", "notes-take", "dropped", "agent-remove"}[r.Intn(7)], len(evs), r.Intn(9), r.Intn(30), r.Intn(900), r.Intn(900), r.Intn(900)))
		case k < 80:
			add(a, events.TypeMailSend, fmt.Sprintf(`{"id":"m%d","from":%q,"to":%q,"kind":"info","text":"message number %d with some words"}`, len(evs), a, agent(), r.Intn(999)))
		case k < 84:
			add(a, events.TypeMailDeliver, fmt.Sprintf(`{"id":"m%d","from":%q}`, r.Intn(len(evs)+1), agent()))
		case k < 85:
			add(a, "mail.drop", fmt.Sprintf(`{"id":"m%d","from":%q,"reason":"gone"}`, r.Intn(len(evs)+1), agent()))
		case k < 86:
			add(a, events.TypeMailDigest, fmt.Sprintf(`{"id":"m%d","to":"x","parcels":["m%d","m%d"]}`, len(evs), r.Intn(len(evs)+1), r.Intn(len(evs)+1)))
		case k < 90:
			add(a, events.TypeLease, fmt.Sprintf(`{"action":%q,"agent":%q,"path":"/r/dir%d/file%d.go","holder":%q}`, []string{"acquire", "acquire", "acquire", "release", "conflict", "scope", "overlap"}[r.Intn(7)], a, r.Intn(50), r.Intn(900), agent()))
		case k < 91:
			add(a, events.TypeCompactPlan, fmt.Sprintf(`{"decision":%q,"warm":%v,"yes":true,"mode":"fork"}`, []string{"start", "commit?"}[r.Intn(2)], r.Intn(2) == 0))
		case k < 92:
			add(a, events.TypeCompactCommit, fmt.Sprintf(`{"reason":%q,"snap_tokens":%d,"retained_tokens":%d,"spine_added":%d,"removed_tokens":%d}`, []string{"x", "mask: y", "emergency: z"}[r.Intn(3)], r.Intn(90000), r.Intn(3000), r.Intn(900), r.Intn(80000)))
		case k < 93:
			add(a, events.TypeCacheAnomaly, fmt.Sprintf(`{"kind":%q,"req":"%s.1","expected_read":%d,"actual_read":%d,"missed":%d,"diverged":"notes"}`, []string{"low_hit", "drift", "thinking_binding"}[r.Intn(3)], a, r.Intn(9000), r.Intn(900), r.Intn(9000)))
		case k < 94:
			add(a, events.TypeLayerCommit, fmt.Sprintf(`{"scope":%q,"reason":"r"}`, []string{"shared-sync", "shared-epoch", "agent", "thinking-strip"}[r.Intn(4)]))
		case k < 95:
			add(a, events.TypeAgentStuck, fmt.Sprintf(`{"phase":%q,"note":"n","error":"e"}`, []string{"nudge", "stop"}[r.Intn(2)]))
		case k < 96:
			add(a, []string{events.TypeMergeQueued, events.TypeMergeMerged, events.TypeMergeConflict, events.TypeMergeVerifyFail, events.TypeMergeRolledBack, events.TypeMergeRejected, events.TypeWorkspaceCreate, events.TypeWorkspaceRemove}[r.Intn(8)],
				fmt.Sprintf(`{"task":"%s: title","position":%d,"files":["a","b"],"after":"0123456789abcdef","cmd":"go test","exit_code":1,"reason":"r","empty":%v,"hunks":2}`, task(), r.Intn(5), r.Intn(4) == 0))
		case k < 97:
			add("swarm", events.TypeTaskMerge, fmt.Sprintf(`{"task":%q,"outcome":%q,"commit":"0123456789abcdef","files":["a"]}`, task(), []string{"merged", "empty", "conflict", "error"}[r.Intn(4)]))
		case k < 98:
			add("swarm", events.TypeGovernor, fmt.Sprintf(`{"action":"rate-limited","rate_per_min":%d,"pause_ms":%d,"inflight":3,"queued":4}`, r.Intn(600), r.Intn(9000)))
		case k < 99:
			id := fmt.Sprintf("p%d", r.Intn(40))
			if r.Intn(2) == 0 {
				add(a, events.TypePermAsk, fmt.Sprintf(`{"id":%q,"agent":%q,"tool":"bash","summary":"run something %d"}`, id, a, r.Intn(99)))
			} else {
				add(a, events.TypePermDecide, fmt.Sprintf(`{"id":%q,"allow":%v,"reason":"r"}`, id, r.Intn(2) == 0))
			}
		default:
			switch r.Intn(6) {
			case 0:
				add("", fmt.Sprintf("x.unknown.%d", r.Intn(500)), `{"a":1}`)
			case 1:
				add(a, events.TypeModelResponse, `{"req":5,"usage":[1],"model":{},"cost_usd":"x"}`)
			case 2:
				add(a, events.TypeToolCall, `{"id":7,"name":["x"],"input":"s"}`)
			case 3:
				add(a, events.TypeUserInput, fmt.Sprintf(`{"text":"please do thing %d"}`, r.Intn(99)))
			case 4:
				add(a, events.TypeTurnAppend, `{"id":1,"role":"user","blocks":[]}`)
			default:
				add(a, "notice", `{"level":"warn","msg":"careful"}`)
			}
		}
	}
	return evs
}

// roundTrip writes each event as the log does and reads it back.
func roundTrip(evs []events.Event) []events.Event {
	out := make([]events.Event, len(evs))
	for i, e := range evs {
		b, err := json.Marshal(e)
		if err != nil {
			panic(err)
		}
		if err := json.Unmarshal(b, &out[i]); err != nil {
			panic(err)
		}
	}
	return out
}

// lineOf is how an event sits in events.jsonl.
func lineOf(e events.Event) string {
	b, _ := json.Marshal(e)
	return string(b) + "\n"
}

// logOf writes events the way the log does.
func logOf(evs []events.Event) string {
	var sb strings.Builder
	for _, e := range evs {
		sb.WriteString(lineOf(e))
	}
	return sb.String()
}
