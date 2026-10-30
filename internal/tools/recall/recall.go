// Package recall implements the tool that pages folded context back in.
//
// Compaction is only acceptable because it is reversible: spine lines point at
// turn ranges, truncated tool output points at blob handles, and this tool
// resolves both. Without it a lossy summary would be a one-way door.
package recall

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Tool is the recall tool.
type Tool struct {
	Archive *kv.Archive
	// Past opens the archive of an agent of an earlier session of this project: session is "last" or a session id, agent an agent
	// id or "" for the agent the person talked to. It returns the archive and the agent key its turns are under. Nil: recall reads
	// this session only.
	Past func(session, agent string) (*kv.Archive, string, error)
	// PastSearch searches the turns of the most recent earlier sessions of this project together (session "all"): agent as in
	// Past. It returns at most limit hits, each scoped by its session's id, and how many sessions it searched. Nil: session
	// "all" is refused.
	PastSearch func(agent, query string, limit int) ([]kv.Hit, int, error)
}

// New returns a recall tool over an archive.
func New(a *kv.Archive) *Tool { return &Tool{Archive: a} }

const schema = `{"type":"object","properties":{
"turns":{"type":"string","description":"turn range from a history line, e.g. t12-t19"},
"handle":{"type":"string","description":"handle of truncated output, e.g. out_ab12cd34"},
"query":{"type":"string","description":"search words in your archived turns"},
"agent":{"type":"string","description":"another agent's id, to read its turns instead of yours (e.g. bk-2)"},
"session":{"type":"string","description":"an earlier session of this project: last, or its id; all (with query) searches every recent one"},
"offset":{"type":"integer","description":"character offset when paging a handle"},
"limit":{"type":"integer","description":"max characters to return"}}}`

// Spec implements tools.Tool.
func (t *Tool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "recall",
		Description: "Retrieve context that was folded out of your prompt. Use turns=\"t12-t19\" to reopen a range " +
			"listed in <history>; handle=\"out_…\" to page through truncated tool output; query=\"words\" to search " +
			"your archived turns. Output is capped; narrow the range or page with offset/limit. " +
			"To ask what another agent found or decided, add agent=\"bk-2\" (a teammate in this session) or session=\"last\" " +
			"(an earlier session of this project; agent defaults to the one the person talked to) to turns or query. " +
			"session=\"all\" with query searches the recent earlier sessions of this project together, for what was " +
			"decided or tried before when you do not know which session it was in.",
		InputSchema: json.RawMessage(schema),
		ReadOnly:    true,
	}
}

type input struct {
	Turns   string `json:"turns"`
	Handle  string `json:"handle"`
	Query   string `json:"query"`
	Offset  int    `json:"offset"`
	Limit   int    `json:"limit"`
	Agent   string `json:"agent"`
	Session string `json:"session"`
}

// source is whose turns a call reads: an archive, the agent key in it, and the arguments that name it again in a hint.
type source struct {
	ar    *kv.Archive
	agent string
	args  string
}

// Run implements tools.Tool.
func (t *Tool) Run(ctx context.Context, c *tools.Call) (*tools.Result, error) {
	env := c.Env.Defaults()
	var in input
	if err := json.Unmarshal(c.Input, &in); err != nil {
		return tools.Errorf("invalid arguments: %v", err), nil
	}
	limit := in.Limit
	if limit <= 0 || limit > env.Limits.MaxOutputChars {
		limit = env.Limits.MaxOutputChars
	}
	// The policy is asked before anything is read: a rule that denies recall, or asks about it, holds for every source.
	if d := env.Perm.Check(ctx, perm.Request{Agent: env.Agent, Role: env.Role, Tool: "recall", Input: c.Input, Summary: recallSummary(in)}); !d.Allow {
		reason := strings.TrimSpace(d.Reason)
		if reason == "" {
			reason = "not allowed by the permission policy"
		}
		return tools.Errorf("permission denied (recall): %s", reason), nil
	}
	if strings.TrimSpace(in.Session) == "all" {
		return t.searchAll(env, in), nil
	}
	src, bad := t.source(env, in)
	if bad != nil {
		return bad, nil
	}
	switch {
	case in.Handle != "":
		if src.args != "" {
			return tools.Errorf("a handle is your own: page it without agent or session"), nil
		}
		return t.handle(env, in, limit), nil
	case in.Turns != "":
		return t.turns(src, in, limit), nil
	case in.Query != "":
		return t.search(src, in), nil
	}
	return tools.Errorf("give one of: turns, handle, query"), nil
}

// recallSummary says what a recall call reads, for a question about it.
func recallSummary(in input) string {
	what := "recall"
	switch {
	case in.Handle != "":
		what += " handle " + in.Handle
	case in.Turns != "":
		what += " turns " + in.Turns
	case in.Query != "":
		what += " " + strconv.Quote(in.Query)
	}
	if a := strings.TrimSpace(in.Agent); a != "" {
		what += " of agent " + a
	}
	if s := strings.TrimSpace(in.Session); s != "" {
		what += " in session " + s
	}
	return what
}

// source resolves agent and session to the turns a call reads: the caller's own by default.
func (t *Tool) source(env *tools.Env, in input) (source, *tools.Result) {
	agent, session := strings.TrimSpace(in.Agent), strings.TrimSpace(in.Session)
	if session != "" {
		if t.Past == nil {
			return source{}, tools.Errorf("earlier sessions cannot be read here")
		}
		ar, key, err := t.Past(session, agent)
		if err != nil {
			return source{}, tools.Errorf("%v", err)
		}
		args := fmt.Sprintf(", session=%q", session)
		if agent != "" {
			args += fmt.Sprintf(", agent=%q", agent)
		}
		return source{ar: ar, agent: key, args: args}, nil
	}
	if agent == "" || agent == env.Agent {
		return source{ar: t.Archive, agent: env.Agent}, nil
	}
	if t.Archive.Len(agent) == 0 {
		return source{}, tools.Errorf("no archived turns of an agent %q in this session: use a teammate's id as the board or live view shows it", cleanID(agent))
	}
	return source{ar: t.Archive, agent: agent, args: fmt.Sprintf(", agent=%q", agent)}, nil
}

// cleanID is an agent id as given, cut short, for a refusal.
func cleanID(s string) string {
	if r := []rune(s); len(r) > 40 {
		return string(r[:40]) + "…"
	}
	return s
}

func (t *Tool) handle(env *tools.Env, in input, limit int) *tools.Result {
	ref, total, ok := env.Handles.Resolve(in.Handle)
	if !ok || env.Blobs == nil {
		return tools.Errorf("unknown handle %q", in.Handle)
	}
	raw, err := env.Blobs.Get(ref)
	if err != nil {
		return tools.Errorf("handle %s is no longer available", in.Handle)
	}
	s := string(raw)
	off := in.Offset
	if off < 0 || off > len(s) {
		off = 0
	}
	end := off + limit
	if end > len(s) {
		end = len(s)
	}
	out := s[off:end]
	if end < len(s) {
		out += fmt.Sprintf("\n[chars %d-%d of %d; call again with offset=%d]", off, end, total, end)
	}
	// A page is returned as-is: it is already the size the model asked for.
	return &tools.Result{Text: out}
}

// resultRef matches "t13.7": the 7th tool result of turn 13, the form the mask and
// excerpt placeholders in the thread name.
var resultRef = regexp.MustCompile(`(?i)^\s*t?(\d+)\s*[.#]\s*(\d+)\s*$`)

// maxRangeTurns bounds how many archived turns one call reads. The output is capped
// at the limit anyway; reading a whole archive to show its first page would only cost
// memory (S47). The budget in bytes follows the output limit.
const maxRangeTurns = 64

func (t *Tool) turns(src source, in input, limit int) *tools.Result {
	spec := strings.TrimSpace(in.Turns)
	if m := resultRef.FindStringSubmatch(spec); m != nil {
		return t.result(src, m, spec, limit)
	}
	from, to, err := parseRange(spec)
	if err != nil {
		return tools.Errorf("bad turn range %q (use e.g. t12-t19, or t13.7 for one tool result)", spec)
	}
	got, more, err := src.ar.RangeLimit(src.agent, from, to, maxRangeTurns, 4*limit)
	if err != nil {
		return tools.Errorf("archive read failed: %v", err)
	}
	if len(got) == 0 {
		return tools.Errorf("no archived turns in %s", spec)
	}
	out := kv.FormatTurns(got, limit)
	if more {
		out += fmt.Sprintf("\n[the range holds more turns: call again with turns=\"t%d-t%d\"%s]", got[len(got)-1].ID+1, to, src.args)
	}
	return &tools.Result{Text: out}
}

// result serves "t13.7": one tool result of one archived turn, in full up to the limit.
func (t *Tool) result(src source, m []string, spec string, limit int) *tools.Result {
	id, err1 := strconv.ParseInt(m[1], 10, 64)
	idx, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return tools.Errorf("bad tool result reference %q (use e.g. t13.7)", spec)
	}
	got, err := src.ar.Range(src.agent, core.TurnID(id), core.TurnID(id))
	if err != nil {
		return tools.Errorf("archive read failed: %v", err)
	}
	if len(got) == 0 {
		return tools.Errorf("no archived turn t%d", id)
	}
	text, err := kv.FormatResult(got[0], idx, limit)
	if err != nil {
		return tools.Errorf("%v", err)
	}
	return &tools.Result{Text: text}
}

func (t *Tool) search(src source, in input) *tools.Result {
	hits := src.ar.Search(src.agent, in.Query, 8)
	if len(hits) == 0 {
		if src.args != "" {
			return &tools.Result{Text: "no matches in " + src.agent + "'s archived turns"}
		}
		return &tools.Result{Text: "no matches in your archived turns"}
	}
	var sb strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&sb, "t%d (%d match): …%s…\n", h.Turn, h.Score, h.Snippet)
	}
	fmt.Fprintf(&sb, "Use recall(turns=\"tN\"%s) to read a hit in full.", src.args)
	return &tools.Result{Text: sb.String()}
}

// searchAll serves session="all": a query over the recent earlier sessions of this project, ranked together.
func (t *Tool) searchAll(env *tools.Env, in input) *tools.Result {
	if t.PastSearch == nil {
		return tools.Errorf("earlier sessions cannot be read here")
	}
	if in.Handle != "" || in.Turns != "" || strings.TrimSpace(in.Query) == "" {
		return tools.Errorf("session=\"all\" takes a query; read a hit with turns and the session id it names")
	}
	agent := strings.TrimSpace(in.Agent)
	hits, n, err := t.PastSearch(agent, in.Query, 8)
	if err != nil {
		return tools.Errorf("%v", err)
	}
	if len(hits) == 0 {
		return &tools.Result{Text: fmt.Sprintf("no matches in the %d most recent earlier sessions of this project", n)}
	}
	var sb strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&sb, "session %s t%d (%d match): …%s…\n", h.Scope, h.Turn, h.Score, h.Snippet)
	}
	args := ""
	if agent != "" {
		args = fmt.Sprintf(", agent=%q", agent)
	}
	fmt.Fprintf(&sb, "Searched the %d most recent earlier sessions. Use recall(turns=\"tN\", session=\"ID\"%s) to read a hit in full.", n, args)
	return &tools.Result{Text: sb.String()}
}

func parseRange(s string) (core.TurnID, core.TurnID, error) {
	for _, sep := range []string{"-", "–", "—", ":"} {
		if a, b, ok := strings.Cut(s, sep); ok {
			from, err1 := kv.ParseTurnID(a)
			to, err2 := kv.ParseTurnID(b)
			if err1 != nil || err2 != nil || to < from {
				return 0, 0, fmt.Errorf("bad range")
			}
			return from, to, nil
		}
	}
	id, err := kv.ParseTurnID(s)
	return id, id, err
}
