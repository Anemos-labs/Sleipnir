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
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Tool is the recall tool.
type Tool struct {
	Archive *kv.Archive
}

// New returns a recall tool over an archive.
func New(a *kv.Archive) *Tool { return &Tool{Archive: a} }

const schema = `{"type":"object","properties":{
"turns":{"type":"string","description":"turn range from a history line, e.g. t12-t19"},
"handle":{"type":"string","description":"handle of truncated output, e.g. out_ab12cd34"},
"query":{"type":"string","description":"search words in your archived turns"},
"offset":{"type":"integer","description":"character offset when paging a handle"},
"limit":{"type":"integer","description":"max characters to return"}}}`

// Spec implements tools.Tool.
func (t *Tool) Spec() core.ToolSpec {
	return core.ToolSpec{
		Name: "recall",
		Description: "Retrieve context that was folded out of your prompt. Use turns=\"t12-t19\" to reopen a range " +
			"listed in <history>; handle=\"out_…\" to page through truncated tool output; query=\"words\" to search " +
			"your archived turns. Output is capped; narrow the range or page with offset/limit.",
		InputSchema: json.RawMessage(schema),
		ReadOnly:    true,
	}
}

type input struct {
	Turns  string `json:"turns"`
	Handle string `json:"handle"`
	Query  string `json:"query"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
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
	switch {
	case in.Handle != "":
		return t.handle(env, in, limit), nil
	case in.Turns != "":
		return t.turns(env, in, limit), nil
	case in.Query != "":
		return t.search(env, in), nil
	}
	return tools.Errorf("give one of: turns, handle, query"), nil
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

func (t *Tool) turns(env *tools.Env, in input, limit int) *tools.Result {
	spec := strings.TrimSpace(in.Turns)
	if m := resultRef.FindStringSubmatch(spec); m != nil {
		return t.result(env, m, spec, limit)
	}
	from, to, err := parseRange(spec)
	if err != nil {
		return tools.Errorf("bad turn range %q (use e.g. t12-t19, or t13.7 for one tool result)", spec)
	}
	got, more, err := t.Archive.RangeLimit(env.Agent, from, to, maxRangeTurns, 4*limit)
	if err != nil {
		return tools.Errorf("archive read failed: %v", err)
	}
	if len(got) == 0 {
		return tools.Errorf("no archived turns in %s", spec)
	}
	out := kv.FormatTurns(got, limit)
	if more {
		out += fmt.Sprintf("\n[the range holds more turns: call again with turns=\"t%d-t%d\"]", got[len(got)-1].ID+1, to)
	}
	return &tools.Result{Text: out}
}

// result serves "t13.7": one tool result of one archived turn, in full up to the limit.
func (t *Tool) result(env *tools.Env, m []string, spec string, limit int) *tools.Result {
	id, err1 := strconv.ParseInt(m[1], 10, 64)
	idx, err2 := strconv.Atoi(m[2])
	if err1 != nil || err2 != nil {
		return tools.Errorf("bad tool result reference %q (use e.g. t13.7)", spec)
	}
	got, err := t.Archive.Range(env.Agent, core.TurnID(id), core.TurnID(id))
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

func (t *Tool) search(env *tools.Env, in input) *tools.Result {
	hits := t.Archive.Search(env.Agent, in.Query, 8)
	if len(hits) == 0 {
		return &tools.Result{Text: "no matches in your archived turns"}
	}
	var sb strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&sb, "t%d (%d match): …%s…\n", h.Turn, h.Score, h.Snippet)
	}
	sb.WriteString("Use recall(turns=\"tN\") to read a hit in full.")
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
