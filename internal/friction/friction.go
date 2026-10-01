// Package friction finds what slows a harness down from what it recorded: permission questions and refusals, tool calls
// that failed, runs that got stuck or were cancelled, requests that had to be retried, caches that broke, the same file
// read again and again. It reads event logs (a session, a rollout sample, a whole run directory) and ranks the patterns
// by how often they happened, how much they hurt and how many requests they wasted, so that the next thing to fix is the
// top of a list, with the evidence one seq away.
//
// It exists because the defects that cost real runs the most were not the ones a person would have guessed: in the first
// benchmark on real models, three quarters of the episodes hit a permission refusal, and nearly half of those were a cd to
// a path the model had guessed. Nothing failed loudly; requests just went to waste. The miner is what finds that without
// reading a thousand transcripts.
//
// What it says is data about the logs. The hints are one line each and say where a pattern of this kind usually comes from;
// they are leads, not verdicts.
package friction

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// Categories. Each finding belongs to one.
const (
	PermRefused  = "permission.refused" // a request was refused: nobody to ask, the user said no, or a policy
	PermAsked    = "permission.asked"   // a person was asked (approval fatigue); not a failure, but a cost
	ToolError    = "tool.error"         // a tool call that failed, by tool and kind
	ToolUnknown  = "tool.unknown"       // a tool name that is not one (possibly repaired)
	ToolInvalid  = "tool.invalid"       // arguments that did not parse or did not fit
	Stuck        = "agent.stuck"        // the repetition guard stepped in
	Cancelled    = "agent.cancel"       // a run was cancelled
	Retried      = "request.retry"      // the endpoint did not answer and the request was repeated
	CacheBreak   = "cache.break"        // a cache anomaly
	CompactFail  = "compaction.reject"  // a compaction patch was rejected
	ReRead       = "file.reread"        // the same part of a file (path and range), unchanged in between, read three or more times in one session
	RepeatedCall = "call.repeated"      // one identical call three or more times
	OutputLimit  = "model.cutoff"       // a response that ended at the output limit (max_tokens): a full-length generation that did not finish
)

// Severity, from what the pattern does to the work.
const (
	S1 = 1 // a cost: a question the person had to answer, a cache that broke once
	S2 = 2 // waste: requests spent on something that failed or repeated
	S3 = 3 // a stop: the run ended or had to be cancelled because of it
)

// A Finding is one pattern and the evidence for it.
type Finding struct {
	Category string `json:"category"`
	// Key is what the events of this pattern have in common, with paths and numbers replaced by placeholders.
	Key      string `json:"key"`
	Count    int    `json:"count"`
	Sessions int    `json:"sessions"`
	Severity int    `json:"severity"`
	// Wasted is how many model requests the pattern cost: the distinct requests whose tool calls it was.
	Wasted   int       `json:"wasted_requests"`
	Score    float64   `json:"score"`
	Hint     string    `json:"hint,omitempty"`
	Examples []Example `json:"examples,omitempty"`
}

// An Example points at one event.
type Example struct {
	Session string `json:"session"`
	Seq     uint64 `json:"seq"`
	Agent   string `json:"agent,omitempty"`
	Detail  string `json:"detail"`
}

// A Report is what Mine found.
type Report struct {
	Sessions int `json:"sessions"`
	Events   int `json:"events"`
	// Requests is the number of model responses seen: the denominator of Wasted.
	Requests int       `json:"requests"`
	Findings []Finding `json:"findings"`
}

// Options tunes Mine.
type Options struct {
	// MinCount drops findings seen fewer times (default 1).
	MinCount int
	// Examples is how many examples to keep per finding (default 3).
	Examples int
}

var hints = map[string]string{
	PermRefused:  "a tool call was refused: look at the commands (a cd to a path the model guessed? a scratch file in /tmp? a read-only command that is not on the allowlist?)",
	PermAsked:    "a person was asked: if the same question comes back, it is a candidate for an allow rule or a safer default",
	ToolError:    "the tool said no: is the error message enough for the model to do something different?",
	ToolUnknown:  "a tool name that does not exist: a garbled name (chat-template tokens), or a tool the model remembers from another harness",
	ToolInvalid:  "arguments the tool could not use: is the schema or its description unclear?",
	Stuck:        "the same failing call again and again: what does the model see that does not tell it what to do next?",
	Cancelled:    "a run was cancelled: by a person (what were they waiting for?) or by a deadline (is the budget right?)",
	Retried:      "the endpoint did not answer: rate limit, overload or a dropped connection",
	CacheBreak:   "the prompt cache broke: a stable layer changed, or the endpoint's cache is erratic",
	CompactFail:  "a compaction patch did not validate: the compactor model, or a rule that is too strict",
	ReRead:       "the same part of a file read again and again: did the answer get lost (truncation, compaction) or never used?",
	RepeatedCall: "the same call over and over: polling, or a loop",
	OutputLimit:  "a response ended at the output limit: a model that rambles (look at the sampling temperature and at the text: a degenerate generation is mostly words that are not there) or a task that asks for one huge write; the agent asks the model to carry on twice and then stops with an error",
}

var (
	pathRe   = regexp.MustCompile(`(?:~|\.{0,2})/[^\s"'();|&<>]*`)
	numberRe = regexp.MustCompile(`\b\d+\b`)
	spaceRe  = regexp.MustCompile(`\s+`)
)

// normalise turns the parts of a message that vary from one occurrence to the next into placeholders, so that "reads
// /workspace outside the workspace (/root/w/tree)" and "reads /repo outside the workspace (/root/w2/tree)" are one pattern.
func normalise(s string) string {
	s = pathRe.ReplaceAllString(s, "<path>")
	s = numberRe.ReplaceAllString(s, "<n>")
	return strings.TrimSpace(spaceRe.ReplaceAllString(s, " "))
}

// commandKey is what a shell command is, for grouping: its first words, with the paths left out. "cd /workspace && go test
// ./..." and "cd /repo && ls" are both "cd <path>".
func commandKey(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	for {
		// leading VAR=value assignments do not say what runs
		i := strings.IndexAny(cmd, " \t")
		if i > 0 && strings.Contains(cmd[:i], "=") && !strings.ContainsAny(cmd[:i], "\"'$(") {
			cmd = strings.TrimSpace(cmd[i:])
			continue
		}
		break
	}
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return "(empty)"
	}
	if fields[0] == "cd" && len(fields) > 1 {
		return "cd <path>"
	}
	n := 1
	if len(fields) > 1 && wordRe.MatchString(fields[1]) {
		n = 2 // a subcommand or a flag: "go test", "git diff", "sed -n", "grep -rn"
	}
	return normalise(strings.Join(fields[:n], " "))
}

// wordRe is what the second word of a command must look like to be part of its key: a subcommand, a flag, a redirect.
var wordRe = regexp.MustCompile(`^[-A-Za-z0-9_>]{1,19}$`)

type acc struct {
	f        Finding
	sessions map[string]bool
	reqs     map[string]bool // "session\x00agent\x00request"
}

// Mine reads every event log under the given paths (each a log file, a session or run directory, or a directory of them) and
// returns the ranked findings. Unreadable logs are skipped and counted in the error only if none could be read.
func Mine(paths []string, o Options) (Report, error) {
	if o.MinCount <= 0 {
		o.MinCount = 1
	}
	if o.Examples <= 0 {
		o.Examples = 3
	}
	var logs []string
	for _, p := range paths {
		found, err := findLogs(p)
		if err != nil {
			return Report{}, err
		}
		logs = append(logs, found...)
	}
	if len(logs) == 0 {
		return Report{}, fmt.Errorf("no events.jsonl found under %s", strings.Join(paths, ", "))
	}
	sort.Strings(logs)

	byKey := map[string]*acc{}
	var rep Report
	add := func(session, category, key string, sev int, ex Example, req string) {
		id := category + "\x00" + key
		a := byKey[id]
		if a == nil {
			a = &acc{f: Finding{Category: category, Key: key, Severity: sev, Hint: hints[category]}, sessions: map[string]bool{}, reqs: map[string]bool{}}
			byKey[id] = a
		}
		a.f.Count++
		a.f.Severity = max(a.f.Severity, sev)
		a.sessions[session] = true
		if req != "" {
			a.reqs[session+"\x00"+req] = true
		}
		if ex.Seq != 0 && len(a.f.Examples) < o.Examples {
			a.f.Examples = append(a.f.Examples, ex)
		}
	}
	for _, path := range logs {
		session := sessionName(path)
		if err := mineLog(path, session, &rep, add); err != nil {
			continue // a torn or unreadable log is still evidence in the others
		}
		rep.Sessions++
	}
	for _, a := range byKey {
		a.f.Sessions = len(a.sessions)
		a.f.Wasted = len(a.reqs)
		// Frequency, weighted by how much it hurts (severity squared: a run that ended outweighs a hundred cache misses) and by
		// the requests it wasted per occurrence.
		a.f.Score = float64(a.f.Count) * float64(a.f.Severity*a.f.Severity) * (1 + float64(a.f.Wasted)/float64(max(a.f.Count, 1)))
		if a.f.Count >= o.MinCount {
			rep.Findings = append(rep.Findings, a.f)
		}
	}
	sort.Slice(rep.Findings, func(i, j int) bool {
		a, b := rep.Findings[i], rep.Findings[j]
		switch {
		case a.Score != b.Score:
			return a.Score > b.Score
		case a.Count != b.Count:
			return a.Count > b.Count
		case a.Category != b.Category:
			return a.Category < b.Category
		}
		return a.Key < b.Key
	})
	return rep, nil
}

func findLogs(root string) ([]string, error) {
	st, err := os.Stat(root)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		return []string{root}, nil
	}
	var out []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == "blobs" || d.Name() == "checkpoints" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "events.jsonl" {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

// sessionName names a log by its directory and its parent: "<run>/<task>/<sample>" or "<session id>".
func sessionName(path string) string {
	dir := filepath.Dir(path)
	parts := strings.Split(filepath.ToSlash(dir), "/")
	if len(parts) >= 3 {
		return strings.Join(parts[len(parts)-3:], "/")
	}
	return dir
}

// call is a tool call waiting for its result.
type call struct {
	name, command, path string
	input               json.RawMessage
	req                 string // the request whose response asked for it
}

// readPart names the part of a file a read call asked for: a key (the path, then the other arguments of the call, which are the
// range) and the words for an example ("read /w/main.go (offset 81, limit 80)"). Two reads of one file with different ranges have
// different keys.
func readPart(path string, input json.RawMessage) (key, what string) {
	var args map[string]json.RawMessage
	_ = json.Unmarshal(input, &args)
	for _, k := range []string{"path", "file_path"} {
		delete(args, k)
	}
	names := make([]string, 0, len(args))
	for k := range args {
		names = append(names, k)
	}
	sort.Strings(names)
	var rng []string
	for _, k := range names {
		rng = append(rng, k+" "+strings.Trim(string(args[k]), `"`))
	}
	if len(rng) == 0 {
		return path + "\x00", path
	}
	return path + "\x00" + strings.Join(rng, ", "), path + " (" + strings.Join(rng, ", ") + ")"
}

func mineLog(path, session string, rep *Report, add func(session, category, key string, sev int, ex Example, req string)) error {
	calls := map[string]call{}     // by tool call id
	lastReq := map[string]string{} // the request each agent's last response answered, by agent
	reads := map[string]int{}      // "read" calls per part of a file (path and range), since the file was last changed
	same := map[string]int{}       // identical calls that did not fail (a failing call that is repeated is that failure's)
	callKey := map[string]string{} // the key of each call, by id
	readEx := map[string]Example{}
	sameEx := map[string]Example{}
	// A refusal is in the log twice since perm.decide exists: as that event, with its reason and who decided, and as the
	// failed tool result. Logs from before have only the second. Whichever the log has is counted, never both.
	type legacy struct {
		key string
		ex  Example
		req string
	}
	var legacyRefusals []legacy
	sawPerm := false
	err := events.Scan(path, func(e events.Event) error {
		rep.Events++
		ex := func(detail string) Example {
			return Example{Session: session, Seq: e.Seq, Agent: e.Agent, Detail: clip(detail, 200)}
		}
		req := lastReq[e.Agent]
		switch e.Type {
		case events.TypeModelResponse:
			rep.Requests++
			var d struct {
				Req     string
				Stop    string
				TotalMS int64 `json:"total_ms"`
				Usage   struct {
					OutputTokens int `json:"output_tokens"`
				}
			}
			if json.Unmarshal(e.Data, &d) == nil {
				lastReq[e.Agent] = d.Req
				if d.Stop == "max_tokens" {
					add(session, OutputLimit, "max_tokens", S2, ex(fmt.Sprintf("%d output tokens in %.0f s", d.Usage.OutputTokens, float64(d.TotalMS)/1000)), d.Req)
				}
			}
		case events.TypeToolCall:
			var d struct {
				ID, Name string
				As       string
				Input    json.RawMessage
			}
			if json.Unmarshal(e.Data, &d) != nil {
				return nil
			}
			c := call{name: d.Name, input: d.Input, req: req}
			var in struct {
				Command string `json:"command"`
				Path    string `json:"path"`
			}
			_ = json.Unmarshal(d.Input, &in)
			c.command, c.path = in.Command, in.Path
			calls[d.ID] = c
			if d.As != "" {
				add(session, ToolUnknown, normalise(d.Name)+" repaired to "+d.As, S1, ex(fmt.Sprintf("%q was used as %q", d.Name, d.As)), req)
			}
			switch {
			case d.Name == "read" && in.Path != "":
				// a big file is read a window at a time: only the same part of it is read again
				part, what := readPart(in.Path, d.Input)
				reads[part]++
				readEx[part] = ex("read " + what)
			case (d.Name == "edit" || d.Name == "write") && in.Path != "":
				// a file that was just changed is worth reading again: the count starts over (the same file is spelled
				// relatively and absolutely by the same model)
				for part := range reads {
					p, _, _ := strings.Cut(part, "\x00")
					if p == in.Path || strings.HasSuffix(p, "/"+strings.TrimPrefix(in.Path, "./")) || strings.HasSuffix(in.Path, "/"+strings.TrimPrefix(p, "./")) {
						reads[part] = 0
					}
				}
			}
			if k := d.Name + " " + string(d.Input); len(k) < 4096 {
				same[k]++
				callKey[d.ID] = k
				if _, seen := sameEx[k]; !seen {
					sameEx[k] = ex(clip(k, 120))
				}
			}
		case events.TypeToolResult:
			var d struct {
				ID, Name string
				Error    bool
				Ms       int64
				Meta     map[string]any
			}
			if json.Unmarshal(e.Data, &d) != nil || !d.Error {
				return nil
			}
			if k, ok := callKey[d.ID]; ok {
				same[k]--
			}
			c := calls[d.ID]
			kind, _ := d.Meta["error_kind"].(string)
			switch kind {
			case "permission":
				// refusals are counted from perm.decide when the log has them; this covers logs written before those existed
				// (the key then comes from the command, which is what they do have)
				key := d.Name
				if c.command != "" {
					key += ": " + commandKey(c.command)
				}
				legacyRefusals = append(legacyRefusals, legacy{key, ex(clip(firstNonEmpty(c.command, c.path), 160)), c.req})
			case "unknown_tool":
				add(session, ToolUnknown, normalise(d.Name), S2, ex(d.Name), c.req)
			case "invalid_input", "invalid_arguments", "invalid":
				add(session, ToolInvalid, d.Name, S2, ex(d.Name), c.req)
			default:
				key := d.Name
				if kind != "" {
					key += " (" + kind + ")"
				}
				add(session, ToolError, key, S2, ex(clip(firstNonEmpty(c.command, c.path), 160)), c.req)
			}
		case events.TypePermAsk:
			sawPerm = true // the question is counted by its answer (perm.decide)
		case events.TypePermDecide:
			sawPerm = true
			var d permPayload
			if json.Unmarshal(e.Data, &d) != nil {
				return nil
			}
			switch {
			case d.By == "canceled":
			case !d.Allow:
				add(session, PermRefused, d.key()+" ["+firstNonEmpty(d.By, "?")+"]", S2, ex(d.detail()), req)
			case d.By == "user":
				add(session, PermAsked, d.key(), S1, ex(d.detail()), "")
			}
		case events.TypeAgentStuck:
			var d struct{ Phase, Note, Error string }
			if json.Unmarshal(e.Data, &d) == nil {
				sev := S2
				if d.Phase == "stop" {
					sev = S3
				}
				add(session, Stuck, d.Phase, sev, ex(firstNonEmpty(d.Error, d.Note)), req)
			}
		case events.TypeAgentCancel:
			var d struct{ Phase, Cause string }
			if json.Unmarshal(e.Data, &d) == nil {
				add(session, Cancelled, d.Cause+" during "+d.Phase, S3, ex(d.Cause+" during "+d.Phase), "")
			}
		case events.TypeModelError:
			var d struct {
				Kind   string
				Status int
				Error  string
			}
			if json.Unmarshal(e.Data, &d) == nil && d.Kind != "" { // a retry; a final failure has no kind
				add(session, Retried, fmt.Sprintf("%s (%d)", d.Kind, d.Status), S1, ex(d.Kind), "")
			}
		case events.TypeCacheAnomaly:
			var d struct{ Kind string }
			if json.Unmarshal(e.Data, &d) == nil {
				add(session, CacheBreak, d.Kind, S1, ex(d.Kind), "")
			}
		case events.TypeCompactReject:
			var d struct{ Stage, Reason string }
			if json.Unmarshal(e.Data, &d) == nil {
				add(session, CompactFail, firstNonEmpty(d.Stage, "?")+": "+normalise(clip(d.Reason, 80)), S1, ex(d.Reason), "")
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if !sawPerm {
		for _, l := range legacyRefusals {
			add(session, PermRefused, l.key, S2, l.ex, l.req)
		}
	}
	// Every repetition after the first was avoidable; the example is attached once.
	parts := make([]string, 0, len(reads))
	for p := range reads {
		parts = append(parts, p)
	}
	sort.Strings(parts)
	for _, p := range parts {
		if n := reads[p]; n >= 3 {
			for i := 1; i < n; i++ {
				ex := Example{}
				if i == 1 {
					ex = readEx[p]
				}
				add(session, ReRead, "the same part of a file read three or more times", S2, ex, "")
			}
		}
	}
	keys := make([]string, 0, len(same))
	for k := range same {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if n := same[k]; n >= 3 && !strings.HasPrefix(k, "read ") {
			for i := 1; i < n; i++ {
				ex := Example{}
				if i == 1 {
					ex = sameEx[k]
				}
				add(session, RepeatedCall, normalise(clip(k, 60)), S2, ex, "")
			}
		}
	}
	return nil
}

// permPayload is the body of perm.ask and perm.decide.
type permPayload struct {
	Tool    string
	Command string
	Paths   []string
	Reason  string
	Allow   bool
	By      string
}

func (p permPayload) key() string {
	switch {
	case p.Command != "":
		return p.Tool + ": " + commandKey(p.Command) + " — " + normalise(clip(p.Reason, 70))
	case len(p.Paths) > 0:
		return p.Tool + " — " + normalise(clip(p.Reason, 90))
	}
	return p.Tool + " — " + normalise(clip(p.Reason, 90))
}

func (p permPayload) detail() string {
	return clip(firstNonEmpty(p.Command, strings.Join(p.Paths, " "), p.Reason), 160)
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}
