package kv_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/kv"
)

// ---- the canonical session ---------------------------------------------------------------
//
// One fixed agent, be-1 (role backend, on task T-1), at the moment it sends its fifth
// request: a constitution, a shared pin, a role pin, notes and a spine, and a thread of
// nine turns in the order a real worker's has them (task kickoff, tool exchanges with
// thinking, an error result, human steering, a thinking-only reply, peer mail). Nothing in
// it comes from the machine: the clock is fixed, turn ids are assigned from 7 (the spine
// says t1-t6 were folded), tool and call ids are constants, and every text is a literal.
//
// It deliberately does not use agent.Constitution or the real tool list, so that changing
// either of those fails their own golden tests and not this one.

const goldenModel = "golden-model-1"

// fixedClock never reads the machine's: 2031-02-03T04:05:06Z plus one second per call.
func fixedClock() func() time.Time {
	n := 0
	base := time.Date(2031, time.February, 3, 4, 5, 6, 0, time.UTC)
	return func() time.Time { n++; return base.Add(time.Duration(n) * time.Second) }
}

func goldenEstimator() core.Estimator { return core.NewBytesEstimator().WithRatio(4) }

// goldenTools are given unsorted and with loose JSON, as tool authors write them;
// kv.SortTools is what makes them the list every agent sends.
func goldenTools(t testing.TB) []core.ToolSpec {
	t.Helper()
	tools, err := kv.SortTools([]core.ToolSpec{
		{Name: "read", Description: "Read a file.", InputSchema: json.RawMessage(`{"type":"object","required":["path"],"properties":{"path":{"type":"string","description":"File path"}}}`), ReadOnly: true},
		{Name: "bash", Description: "Run a shell command.", InputSchema: json.RawMessage(`{ "type": "object", "properties": { "command": { "type": "string" } }, "required": ["command"] }`), Strict: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return tools
}

// boardView is the always-fresh tail as the swarm renders it (a <live> frame).
func boardView(version int, t2 string) string {
	return "<live board=\"v" + strconv.Itoa(version) + "\">\n" +
		"you: be-1 (backend) · T-1 · ctx 2k\n" +
		"T-1 [doing] be-1 · fix the pagination off-by-one\n" +
		"T-2 [" + t2 + "] fe-1 · render the page footer\n" +
		"</live>"
}

// goldenStack builds the session for one hot mode. The conversation is the same in all
// three; what differs is how the board view reaches the model, exactly as the agent does
// it (Agent.pushUser): HotInline keeps it out of the thread (Render gets it as RenderOpts.Hot),
// HotPersist writes it into the user turn as a frozen notice only when its content changed, and
// HotTurnScoped follows every user turn with a system turn.
func goldenStack(t testing.TB, mode kv.HotMode) (*kv.Stack, []core.Block) {
	t.Helper()
	views := []string{
		boardView(3, "todo"), boardView(4, "todo"), boardView(5, "todo"), boardView(6, "doing"), boardView(7, "doing"),
	}
	persistAt := map[int]bool{0: true, 3: true} // the content changed at the first request and at the fourth

	th := kv.NewThread()
	th.SetClock(fixedClock())
	if err := th.Restore(nil, 7, 1); err != nil { // ids continue after the six turns the spine covers
		t.Fatal(err)
	}
	user := func(k int, origin core.Origin, blocks ...core.Block) {
		if mode == kv.HotPersist && persistAt[k] {
			blocks = append(blocks, kv.Notice(views[k]))
		}
		th.Append(core.Turn{Role: core.RoleUser, Origin: origin, Blocks: blocks})
		if mode == kv.HotTurnScoped {
			th.Append(core.Turn{Role: core.RoleSystem, Origin: core.OriginSystem, Blocks: []core.Block{core.Text(views[k])}})
		}
	}
	assistant := func(blocks ...core.Block) {
		th.Append(core.Turn{Role: core.RoleAssistant, Origin: core.OriginModel, Model: goldenModel, Blocks: blocks})
	}
	thinking := func(text, sig string) core.Block {
		return core.Block{Kind: core.BlockThinking, Text: text, WireFormat: "anthropic",
			Wire: json.RawMessage(`{"type":"thinking","thinking":"` + text + `","signature":"` + sig + `"}`)}
	}

	user(0, core.OriginTask, kv.Task("New assignment: T-1. Fix the pagination off-by-one in api/list.go and keep the JSON shape of the response."))
	assistant(
		thinking("the last page is one item short", "c2lnLWdvbGRlbi0x"),
		core.Text("I'll start with the handler."),
		core.ToolUse("toolu_01", "read", json.RawMessage(`{"path":"api/list.go"}`)),
	)
	user(1, core.OriginTool, core.ToolResult("toolu_01", false, core.Text(
		"package api\n\nfunc page(items []Item, n, size int) []Item {\n\tstart := n * size\n\tif start >= len(items) && size > 0 {\n\t\treturn nil\n\t}\n"+
			"\tend := start + size\n\tif end > len(items)-1 { // drops the last item\n\t\tend = len(items) - 1\n\t}\n\treturn items[start:end]\n}\n")))
	assistant(
		core.ToolUse("toolu_02", "bash", json.RawMessage(`{"command":"go test ./api/..."}`)),
		core.ToolUse("toolu_03", "bash", json.RawMessage(`{"command":"git status --short"}`)),
	)
	user(2, core.OriginTool,
		core.ToolResult("toolu_02", true, core.Text("--- FAIL: TestPage (0.00s)\n    list_test.go:41: got 3 items, want 4\nFAIL\nexit status 1")),
		core.ToolResult("toolu_03", false, core.Text(" M api/list.go")),
	)
	assistant(core.Text("The bound is len(items)-1; it should be len(items)."))
	user(3, core.OriginUser, kv.Steer("also add a regression test"))
	assistant(thinking("a test first, then the fix", "c2lnLWdvbGRlbi0y")) // a reply that is only thinking
	user(4, core.OriginMail, core.Text("[mail m3 from fe-1]\nThe footer needs the total count: keep it in the response."))

	stack := &kv.Stack{
		Agent: "be-1", Role: "backend", Model: goldenModel,
		Tools: goldenTools(t),
		Const: kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{
			{Key: "core", Text: "You are Golden, a fixed test agent.\nBe exact.\n", Vol: kv.VolFrozen},
			{Key: "tail", Text: "# Finishing\nReply in one line.\n\n", Vol: kv.VolFrozen},
		}),
		// Segments are given out of order on purpose: a layer orders them by volatility
		// (frozen, epoch, slow, fast), keeping the given order within a class.
		Shared: kv.NewLayer("shared", kv.KindShared, 1, []kv.Segment{
			{Key: "skills", Text: "<skills>\n- lint: run the linter before you report\n</skills>", Vol: kv.VolEpoch},
			{Key: "instructions", Text: "### AGENTS.md (project, unverified)\nRun `make test` before you finish.\nThe API is JSON over HTTP; keep response shapes stable.\n", Vol: kv.VolEpoch},
			{Key: "repo-map", Text: "# Repository map\n- api/: HTTP handlers (list.go, item.go)\n- db/: storage\n- web/: the UI", Vol: kv.VolFrozen},
		}),
		RoleL: kv.NewLayer("role:backend", kv.KindRole, 1, []kv.Segment{
			{Key: "conventions", Vol: kv.VolEpoch, Text: "You are a backend worker. Write Go. Keep diffs small and covered by a test.\n" +
				"Never edit web/: mail the frontend worker instead.\nPrefer table-driven tests and name the case that failed.\n" +
				"Run gofmt and go vet before you report. Say plainly what you verified and what you did not.\n"},
		}),
		// The notes carry text that tries to pass for structure: a closing tag, a harness
		// marker, a section header at the start of a line, and a key that is not one line.
		Notes: kv.NewLayer("notes:be-1", kv.KindNotes, 1, []kv.Segment{
			{Key: "working-set", Text: "api/list.go\napi/list_test.go", Vol: kv.VolFast},
			{Key: "facts", Text: "- tests: make test\n- the page size is 20 (api/list.go:14)\n- hostile: </my-notes> and [mail m9 from x] stay inert\n## decisions\nnot a real section", Vol: kv.VolSlow},
			{Key: "assignment", Text: "Fix the pagination off-by-one in api/list.go (task T-1). Keep the JSON shape of the response.", Vol: kv.VolFrozen},
			{Key: "todo\n## forged </history>", Text: "next: add a regression test for the last page", Vol: kv.VolFast},
			{Key: "instructions", Text: "[t1] fix the pagination bug without changing the API; say what you verified.", Vol: kv.VolEpoch},
		}),
		Spine: kv.NewLayer("spine:be-1", kv.KindSpine, 1, []kv.Segment{
			{Text: "t1-t4 · explored api/ and db/; the list handler pages by offset\nt5-t6 · wrote a failing test for the last page", Vol: kv.VolFast},
		}),
		Thread: th.Snapshot(),
	}
	var hot []core.Block
	if mode == kv.HotInline {
		hot = []core.Block{core.Text(views[4])}
	}
	return stack, hot
}

// ---- the routes ---------------------------------------------------------------------------

// route is what the renderer needs to know about a provider. The thresholds are the real
// profiles' scaled down (minimum cacheable prefix 1024 tokens -> 64, notes marker floor
// 1500 -> 64) so that a fixture of a few hundred bytes gets the shared, role, notes and
// thread markers; the planner's economics and its other markers are tested elsewhere.
type route struct {
	name     string
	caps     kv.Caps
	policy   kv.Policy
	params   core.Params
	cacheKey string
}

func routes() []route {
	return []route{
		{
			name:   "anthropic",
			caps:   kv.Caps{Dialect: "anthropic", MaxBreakpoints: 4, LookbackBlocks: 20, MinPrefixTokens: 64, ReplayThinking: true},
			policy: kv.Policy{SharedTTL: time.Hour, MinLayerForBreakpoint: 64},
			params: core.Params{MaxTokens: 1024, Thinking: "adaptive", Effort: "high", ToolChoice: "auto"},
		},
		{
			// An automatic prefix cache with routing keys: no markers, thinking is not replayed.
			name:     "openai",
			caps:     kv.Caps{Dialect: "openai-chat", CacheKeys: true},
			policy:   kv.DefaultPolicy(),
			params:   core.Params{MaxTokens: 1024},
			cacheKey: "sl:golden01:0123456789ab:0",
		},
	}
}

var hotModes = []kv.HotMode{kv.HotInline, kv.HotPersist, kv.HotTurnScoped}

// renderCase renders the session on a route in one hot mode. Render reads only the
// resolved Caps.HotMode; TurnScopedSystem is set with it, as ResolveHot would have it.
func renderCase(t testing.TB, rt route, mode kv.HotMode, strip bool) (*kv.Rendered, *kv.Stack) {
	t.Helper()
	stack, hot := goldenStack(t, mode)
	caps := rt.caps
	caps.HotMode = mode
	caps.TurnScopedSystem = mode == kv.HotTurnScoped
	r := kv.Render(stack, kv.RenderOpts{
		Hot: hot, Caps: caps, Policy: rt.policy, Params: rt.params, CacheKey: rt.cacheKey,
		StripThinking: strip, Est: goldenEstimator(),
	})
	return r, stack
}

// ---- the transcript -----------------------------------------------------------------------

// writeBlock prints every field of a block that is not its zero value; the text is
// printed as it is between <<< and >>> so that a change in it shows as a change of lines.
func writeBlock(sb *strings.Builder, label string, b core.Block) {
	fmt.Fprintf(sb, "@@ block %s kind=%s", label, b.Kind)
	for _, f := range []struct{ k, v string }{
		{"tool_id", b.ToolID}, {"tool_name", b.ToolName}, {"media_type", b.MediaType}, {"media_ref", b.MediaRef},
		{"wire_format", b.WireFormat}, {"invalid", b.Invalid},
	} {
		if f.v != "" {
			fmt.Fprintf(sb, " %s=%s", f.k, strconv.Quote(f.v))
		}
	}
	if b.IsError {
		sb.WriteString(" is_error=true")
	}
	if b.Ephemeral {
		sb.WriteString(" ephemeral=true")
	}
	if b.Text != "" {
		fmt.Fprintf(sb, " text_bytes=%d", len(b.Text))
	}
	if len(b.Result) > 0 {
		fmt.Fprintf(sb, " result_blocks=%d", len(b.Result))
	}
	sb.WriteByte('\n')
	if len(b.Input) > 0 {
		fmt.Fprintf(sb, "input: %s\n", b.Input)
	}
	if len(b.Wire) > 0 {
		fmt.Fprintf(sb, "wire: %s\n", b.Wire)
	}
	if b.Text != "" {
		fmt.Fprintf(sb, "<<<\n%s\n>>>\n", b.Text)
	}
	for i, c := range b.Result {
		writeBlock(sb, label+"."+strconv.Itoa(i), c)
	}
}

// modelVisible is what the model is sent: the tools, the system blocks and the messages.
// It leaves out what is only about caching (markers, routing key, parameters).
func modelVisible(r *kv.Rendered) string {
	var sb strings.Builder
	p := r.Prompt
	fmt.Fprintf(&sb, "== model: %s\n\n", p.Model)
	fmt.Fprintf(&sb, "== tools (%d)\n", len(p.Tools))
	for i, tl := range p.Tools {
		fmt.Fprintf(&sb, "@@ tool %d name=%s strict=%v\ndescription: %s\ninput_schema: %s\n", i, tl.Name, tl.Strict, tl.Description, tl.InputSchema)
	}
	fmt.Fprintf(&sb, "\n== system blocks (%d)\n", len(p.System))
	for i, b := range p.System {
		writeBlock(&sb, "system."+strconv.Itoa(i), b)
	}
	fmt.Fprintf(&sb, "\n== messages (%d)\n", len(p.Messages))
	for mi, m := range p.Messages {
		clear := "-"
		if m.ClearAt != "" {
			clear = m.ClearAt
		}
		fmt.Fprintf(&sb, "@@ message %d role=%s turn=%d clear_at=%s blocks=%d\n", mi, m.Role, m.Turn, clear, len(m.Blocks))
		for bi, b := range m.Blocks {
			label := fmt.Sprintf("%d.%d", mi, bi)
			if mi == 0 && bi < len(r.Sections) {
				label += " layer=" + r.Sections[bi].Name
			}
			writeBlock(&sb, label, b)
		}
	}
	return sb.String()
}

// transcript is the golden text of one render: the request's mechanics, the planner's
// markers and sections, then modelVisible.
func transcript(title string, r *kv.Rendered) string {
	var sb strings.Builder
	p := r.Prompt
	fmt.Fprintf(&sb, "# kv.Render of the canonical session (internal/kv/golden_render_test.go)\n# %s\n", title)
	sb.WriteString("# regenerate: go test ./internal/kv -run TestGoldenRenderedStack -update   (read docs/BUILDING.md, \"Changing prompt bytes\", first)\n\n")

	sb.WriteString("== request\n")
	key := p.CacheKey
	if key == "" {
		key = "(none)"
	}
	temp := "(unset)"
	if p.Params.Temperature != nil {
		temp = strconv.FormatFloat(*p.Params.Temperature, 'g', -1, 64)
	}
	fmt.Fprintf(&sb, "cache_key: %s\n", key)
	fmt.Fprintf(&sb, "params: max_tokens=%d thinking=%q effort=%q tool_choice=%q stop=%q temperature=%s\n",
		p.Params.MaxTokens, p.Params.Thinking, p.Params.Effort, p.Params.ToolChoice, p.Params.Stop, temp)
	fmt.Fprintf(&sb, "prefix_key: %s\n", r.PrefixKey)
	fmt.Fprintf(&sb, "thread_from: %d\n", r.ThreadFrom)
	if r.Rolling != nil {
		fmt.Fprintf(&sb, "rolling_marker: message %d block %d\n", r.Rolling.Msg, r.Rolling.Blk)
	} else {
		sb.WriteString("rolling_marker: none\n")
	}

	fmt.Fprintf(&sb, "\n== breakpoints (%d)\n", len(p.Breakpoints))
	for _, b := range p.Breakpoints {
		where := fmt.Sprintf("message %d block %d", b.After.Msg, b.After.Blk)
		if b.After.Sys {
			where = fmt.Sprintf("system (msg %d) block %d", b.After.Msg, b.After.Blk)
		}
		fmt.Fprintf(&sb, "%s: after %s ttl=%v\n", b.Label, where, b.TTL)
	}

	fmt.Fprintf(&sb, "\n== sections (%d)\n", len(r.Sections))
	for _, s := range r.Sections {
		fmt.Fprintf(&sb, "%s: sha256=%s tokens=%d breakpoint=%v\n", s.Name, s.Hash, s.Tokens, s.Breakpoint)
	}

	sb.WriteString("\n")
	sb.WriteString(modelVisible(r))

	// The text above is for reading. These two are computed from every non-zero field of the
	// result (see flatten), printed or not, so a field this file does not show still fails it.
	sb.WriteString("\n== digests (of every non-zero field, whether or not it is printed above)\n")
	fmt.Fprintf(&sb, "model_visible_sha256: %s\n", digestOf(visibleLines(r.Prompt)))
	fmt.Fprintf(&sb, "rendered_sha256: %s\n", digestOf(renderedLines(r)))
	return sb.String()
}

// flatten lists every non-zero field of v as path=value, in declaration order, naming fields
// by their JSON names (the wire format's names: renaming a Go field changes nothing, renaming
// what goes on the wire does). It is how the golden files know about a field the transcript
// does not print, and how the layout digest covers a field somebody adds tomorrow. skip names
// struct fields ("Type.Field") that are bookkeeping, not part of what the model is sent.
func flatten(path string, v reflect.Value, skip map[string]bool, out *[]string) {
	add := func(format string, args ...any) { *out = append(*out, path+fmt.Sprintf(format, args...)) }
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			flatten(path, v.Elem(), skip, out)
		}
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() || skip[t.Name()+"."+f.Name] {
				continue
			}
			name := f.Name
			if tag, _, _ := strings.Cut(f.Tag.Get("json"), ","); tag == "-" {
				continue
			} else if tag != "" {
				name = tag
			}
			flatten(path+"."+name, v.Field(i), skip, out)
		}
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 { // []byte, json.RawMessage
			if v.Len() > 0 {
				add("=%q", v.Bytes())
			}
			return
		}
		for i := 0; i < v.Len(); i++ {
			flatten(fmt.Sprintf("%s[%d]", path, i), v.Index(i), skip, out)
		}
	case reflect.Map:
		keys := v.MapKeys()
		sort.Slice(keys, func(i, j int) bool { return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j]) })
		for _, k := range keys {
			flatten(fmt.Sprintf("%s[%v]", path, k), v.MapIndex(k), skip, out)
		}
	case reflect.String:
		if v.Len() > 0 {
			add("=%q", v.String())
		}
	case reflect.Bool:
		if v.Bool() {
			add("=true")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if v.Int() != 0 {
			add("=%d", v.Int())
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if v.Uint() != 0 {
			add("=%d", v.Uint())
		}
	case reflect.Float32, reflect.Float64:
		if v.Float() != 0 {
			add("=%v", v.Float())
		}
	default:
		panic(fmt.Sprintf("flatten: %s at %s is not a kind this golden test knows; teach flatten about it", v.Kind(), path))
	}
}

// visibleLines is what the model is sent, field by field: the model, tools, system blocks and
// messages, without the thread turn a message renders (bookkeeping) and without anything
// about caching (markers, routing key, parameters). RendererVersion names this layout.
func visibleLines(p *core.Prompt) []string {
	skip := map[string]bool{"Message.Turn": true}
	var out []string
	flatten("model", reflect.ValueOf(p.Model), skip, &out)
	flatten("tools", reflect.ValueOf(p.Tools), skip, &out)
	flatten("system", reflect.ValueOf(p.System), skip, &out)
	flatten("messages", reflect.ValueOf(p.Messages), skip, &out)
	return out
}

// renderedLines is everything Render returns: the prompt with its markers and parameters,
// the sections, the keys and the thread boundary.
func renderedLines(r *kv.Rendered) []string {
	var out []string
	flatten("rendered", reflect.ValueOf(r), nil, &out)
	return out
}

func digestOf(lines []string) string {
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// layerReport is the per-layer byte hashes of the session and its routing keys.
func layerReport(s *kv.Stack) string {
	var sb strings.Builder
	sb.WriteString("# The layers of the canonical session as rendered: bytes and sha256 of each layer's text, and the keys derived from them\n")
	sb.WriteString("# columns: layer, byte count, sha256\n")
	sb.WriteString("# regenerate: go test ./internal/kv -run TestGoldenRenderedStack -update   (read docs/BUILDING.md, \"Changing prompt bytes\", first)\n")
	for _, l := range []struct {
		name  string
		layer *kv.Layer
	}{{"const", s.Const}, {"shared", s.Shared}, {"role", s.RoleL}, {"notes", s.Notes}, {"spine", s.Spine}} {
		fmt.Fprintf(&sb, "%s\t%d\t%s\n", l.name, len(l.layer.Text()), l.layer.Hash())
	}
	fmt.Fprintf(&sb, "prefix_key\t-\t%s\n", s.PrefixKey())
	fmt.Fprintf(&sb, "global_key\t-\t%s\n", s.GlobalKey())
	return sb.String()
}

// ---- the tests ----------------------------------------------------------------------------

// renderHistory lists the layouts the renderer has had, oldest first: the RendererVersion
// and the digest of the model-visible output of the canonical session for it (see
// renderDigest). TestGoldenRenderedStack/renderer_version fails when the output no longer
// has the newest row's digest, and TestRendererVersion fails when the newest row is not
// kv.RendererVersion: so changing what the model is sent takes a new row here and a new
// version in render.go, in the same commit, where a reviewer sees both.
var renderHistory = []struct{ version, digest string }{
	{"sleipnir-kv/2", "27c98b11c17d077bc552aa0dfe3e8eedc33e2e1ace77eb636407a0bc68077f1e"},
}

// renderDigest is the digest over what the model is sent (visibleLines) for every route and
// hot mode. It does not depend on how the transcript is laid out.
func renderDigest(t testing.TB) string {
	t.Helper()
	vis := map[string]string{}
	for _, rt := range routes() {
		for _, m := range hotModes {
			r, _ := renderCase(t, rt, m, false)
			vis[rt.name+"_"+m.String()] = strings.Join(visibleLines(r.Prompt), "\n")
		}
	}
	r, _ := renderCase(t, routes()[0], kv.HotInline, true)
	vis["anthropic_inline_strip-thinking"] = strings.Join(visibleLines(r.Prompt), "\n")

	names := make([]string, 0, len(vis))
	for n := range vis {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s\n%d\n%s\n", n, len(vis[n]), vis[n])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// TestGoldenRenderedStack pins what kv.Render sends for one fixed session: the hash of
// every layer's bytes, the routing keys, and the whole rendered prompt (tools, system
// block, every message block, markers, sections) for each hot mode on an Anthropic-style
// route (explicit markers, thinking replayed) and an OpenAI-style route (automatic
// caching, routing key, thinking dropped). The expected text is in testdata/golden/render.
func TestGoldenRenderedStack(t *testing.T) {
	t.Run("layers", func(t *testing.T) {
		s, _ := goldenStack(t, kv.HotInline)
		golden(t, "render/layers.txt", []byte(layerReport(s)))
	})

	for _, rt := range routes() {
		for _, mode := range hotModes {
			name := rt.name + "_" + mode.String()
			t.Run(name, func(t *testing.T) {
				r, stack := renderCase(t, rt, mode, false)
				// Each section's hash is the hash of the layer's text, which is the block the
				// model is sent: the accounting cannot drift from the bytes.
				layers := map[string]*kv.Layer{"shared": stack.Shared, "role": stack.RoleL, "notes": stack.Notes, "spine": stack.Spine}
				for i, s := range r.Sections {
					l := layers[s.Name]
					if s.Hash != core.HashString(l.Text()) || r.Prompt.Messages[0].Blocks[i].Text != l.Text() {
						t.Errorf("section %s: hash %s does not describe the bytes sent for that layer", s.Name, s.Hash.Short())
					}
				}
				title := fmt.Sprintf("route: %s   hot mode: %s   strip thinking: false", rt.name, mode)
				golden(t, "render/"+name+".txt", []byte(transcript(title, r)))
			})
		}
	}

	t.Run("anthropic_inline_strip-thinking", func(t *testing.T) {
		rt := routes()[0]
		r, _ := renderCase(t, rt, kv.HotInline, true)
		golden(t, "render/anthropic_inline_strip-thinking.txt", []byte(transcript("route: anthropic   hot mode: inline   strip thinking: true (the first request after a declared rebase)", r)))
	})

	t.Run("deterministic", func(t *testing.T) {
		// The same session, rebuilt from nothing, renders to the same bytes every time (map
		// iteration order, clocks, ids and addresses play no part), and nothing the clock
		// produced is in what the model is sent.
		for _, rt := range routes() {
			for _, mode := range hotModes {
				r0, _ := renderCase(t, rt, mode, false)
				want := transcript("x", r0)
				for i := 0; i < 15; i++ {
					r, _ := renderCase(t, rt, mode, false)
					if got := transcript("x", r); got != want {
						t.Fatalf("%s/%s: render %d differs from the first:\n%s", rt.name, mode, i, lineDiff([]byte(want), []byte(got)))
					}
				}
				visible := modelVisible(r0)
				for _, leak := range []string{"2031-02-03", "04:05:0"} { // the fixed clock's date and time
					if strings.Contains(visible, leak) {
						t.Errorf("%s/%s: %q, a timestamp, is in the bytes the model is sent", rt.name, mode, leak)
					}
				}
				if strings.Contains(want, "0xc0") { // hex hashes have no x: this is a pointer
					t.Errorf("%s/%s: an address is in the transcript", rt.name, mode)
				}
			}
		}
	})

	t.Run("renderer_version", func(t *testing.T) {
		got := renderDigest(t)
		last := renderHistory[len(renderHistory)-1]
		if got != last.digest {
			t.Fatalf("the bytes kv.Render sends the model changed (digest %s; the newest row of renderHistory, for %s, says %s).\n"+
				"Changing them is a declared event: bump RendererVersion in internal/kv/render.go and append\n\t{%q, %q},\nto renderHistory in internal/kv/golden_render_test.go "+
				"(docs/BUILDING.md, \"Changing prompt bytes\")",
				got, last.version, last.digest, "sleipnir-kv/<next>", got)
		}
	})
}

// TestRendererVersion: kv.RendererVersion names the prompt layout, so that logged prompts
// and training data can be tied to the layout the model was served with. It is bumped
// whenever Render's model-visible bytes change, never otherwise, and always together with a
// new row in renderHistory (TestGoldenRenderedStack/renderer_version checks that the bytes
// match the row).
func TestRendererVersion(t *testing.T) {
	last := renderHistory[len(renderHistory)-1]
	if kv.RendererVersion != last.version {
		t.Fatalf("kv.RendererVersion is %q but the newest layout in renderHistory (internal/kv/golden_render_test.go) is %q.\n"+
			"A bump is deliberate: append {%q, <digest>} to renderHistory, where the digest is the one TestGoldenRenderedStack/renderer_version prints (docs/BUILDING.md, \"Changing prompt bytes\")",
			kv.RendererVersion, last.version, kv.RendererVersion)
	}
	seenVersion, seenDigest := map[string]bool{}, map[string]bool{}
	prev := 0
	for i, row := range renderHistory {
		n, err := strconv.Atoi(strings.TrimPrefix(row.version, "sleipnir-kv/"))
		if err != nil || !strings.HasPrefix(row.version, "sleipnir-kv/") || n <= prev {
			t.Errorf("row %d: version %q must be sleipnir-kv/<n> with n above the previous row's (%d)", i, row.version, prev)
		}
		prev = n
		if seenVersion[row.version] || seenDigest[row.digest] {
			t.Errorf("row %d (%s): a version or a digest is listed twice; a version bump without a byte change is not a layout change", i, row.version)
		}
		seenVersion[row.version], seenDigest[row.digest] = true, true
	}
}
