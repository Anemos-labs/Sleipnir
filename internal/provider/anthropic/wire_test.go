package anthropic_test

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/provider/anthropic"
)

func build(t *testing.T, p *core.Prompt, o anthropic.Options, stream bool) *anthropic.Built {
	t.Helper()
	b, err := anthropic.BuildReport(p, o, stream)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !json.Valid(b.Body) {
		t.Fatalf("body is not valid JSON:\n%s", b.Body)
	}
	return b
}

func codes(ws []anthropic.Warning) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Code
	}
	return out
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// goldenCase is one golden request body.
type goldenCase struct {
	name     string
	prompt   func() *core.Prompt
	opts     anthropic.Options
	stream   bool
	warnings []string
	betas    []string
}

func layeredPrompt() *core.Prompt {
	return &core.Prompt{
		Model: "claude-opus-5-5",
		Tools: testTools(),
		System: []core.Block{
			core.Text("You are Sleipnir, a careful coding agent."),
		},
		Messages: []core.Message{
			user(core.Text("<shared-context>the project is a Go module</shared-context>"),
				core.Text("<role-context>backend conventions</role-context>"),
				core.Text("<my-notes>none yet</my-notes>"),
				core.Text("Fix the failing test.")),
		},
		Breakpoints: []core.Breakpoint{
			bp(toolsRef(), time.Hour, "tools"),
			bp(sysRef(0), time.Hour, "const"),
			bp(ref(0, 1), 0, "role"),
			bp(ref(0, 3), 0, "thread"),
		},
		Params: core.Params{MaxTokens: 4096},
	}
}

func toolLoopPrompt(withHot bool) *core.Prompt {
	msgs := []core.Message{
		user(core.Text("Read a.txt and run the tests.")),
		asst(
			thinkingBlock("sig-fixture-1"),
			core.Text("I will read the file and run the tests."),
			toolUseWire("toolu_01", "read", `{ "path" : "a.txt" }`),
			core.ToolUse("toolu_02", "bash", json.RawMessage(`{"command":"go test ./..."}`)),
			core.ToolUse("toolu_03", "bash", nil),
		),
		user(
			core.ToolResult("toolu_01", false,
				core.Text("package a"),
				core.Block{Kind: core.BlockImage, MediaType: "image/png", MediaRef: pixel}),
			core.ToolResult("toolu_02", true, core.Text("FAIL: TestA")),
			core.ToolResult("toolu_03", false),
		),
	}
	last := ref(2, 2)
	if withHot {
		msgs = append(msgs, hot("next_user_message", "board: 3 tasks open"))
		last = ref(3, 0)
	}
	return &core.Prompt{
		Model:    "claude-opus-5-5",
		Tools:    testTools(),
		System:   []core.Block{core.Text("You are Sleipnir.")},
		Messages: msgs,
		Breakpoints: []core.Breakpoint{
			bp(sysRef(0), 0, "const"),
			bp(last, 0, "thread"),
		},
		Params: core.Params{MaxTokens: 4096, Thinking: "adaptive", Effort: "high"},
	}
}

func hotLoopPrompt() *core.Prompt {
	return &core.Prompt{
		Model:  "claude-opus-5-5",
		Tools:  testTools(),
		System: []core.Block{core.Text("You are Sleipnir.")},
		Messages: []core.Message{
			user(core.Text("Run the tests.")),
			asst(thinkingBlock("sig-fixture-1"), toolUseWire("toolu_01", "bash", `{"command":"go test"}`)),
			user(core.ToolResult("toolu_01", false, core.Text("ok"))),
			hot("next_user_message", "board: 3 tasks open"),
			asst(thinkingBlock("sig-fixture-2"), toolUseWire("toolu_02", "bash", `{"command":"go vet"}`)),
			user(core.ToolResult("toolu_02", false, core.Text("clean"))),
			hot("next_user_message", "board: 2 tasks open"),
		},
		Breakpoints: []core.Breakpoint{
			bp(sysRef(0), 0, "const"),
			bp(ref(6, 0), 0, "thread"), // the hot block: cannot carry a marker
		},
		Params: core.Params{MaxTokens: 4096, Thinking: "adaptive"},
	}
}

func TestBuildGolden(t *testing.T) {
	temp := 0.25
	cases := []goldenCase{
		{
			name:   "cache_layers.json",
			prompt: layeredPrompt,
			opts:   anthropic.Options{},
		},
		{
			name:     "tool_loop_thinking_replay.json",
			prompt:   func() *core.Prompt { return toolLoopPrompt(false) },
			stream:   true,
			warnings: []string{"marker_moved"}, // the rolling marker addressed an empty tool_result
		},
		{
			name:     "hot_native_clear_at.json",
			prompt:   hotLoopPrompt,
			warnings: []string{"marker_moved"},
			betas:    []string{anthropic.BetaTurnScopedSystem},
		},
		{
			name:   "hot_folded_no_clear_at.json",
			prompt: hotLoopPrompt,
			opts:   anthropic.Options{NoTurnScopedSystem: true},
		},
		{
			name: "params_thinking_effort.json",
			prompt: func() *core.Prompt {
				p := layeredPrompt()
				p.Breakpoints = nil
				p.Params = core.Params{MaxTokens: 2048, Thinking: "adaptive", Effort: "xhigh", ToolChoice: "none", Stop: []string{"</done>"}, Temperature: &temp}
				return p
			},
			opts:     anthropic.Options{UserID: "agent-7", ThinkingDisplay: "summarized"},
			warnings: []string{"temperature_dropped"},
		},
		{
			name: "params_legacy_model.json",
			prompt: func() *core.Prompt {
				p := layeredPrompt()
				p.Model = "claude-sonnet-4-6"
				p.Breakpoints = nil
				p.Params = core.Params{MaxTokens: 2048, Thinking: "adaptive", Effort: "xhigh", ToolChoice: "auto", Temperature: &temp}
				return p
			},
			warnings: []string{"effort_clamped"},
		},
		{
			name: "warm.json",
			prompt: func() *core.Prompt {
				p := layeredPrompt()
				p.Messages = append(p.Messages[:0:0], user(core.Text("<shared-context>the project is a Go module</shared-context>"), core.Text("warm")))
				p.Breakpoints = []core.Breakpoint{bp(sysRef(0), time.Hour, "const"), bp(ref(0, 0), time.Hour, "shared")}
				p.Params.Thinking = "adaptive"
				return p
			},
			opts:   anthropic.Options{Warm: true},
			stream: true, // a warm-up is never streamed
		},
		{
			name: "binding_drop_block.json",
			prompt: func() *core.Prompt {
				p := hotLoopPrompt()
				p.Params.Thinking = "off" // Opus 5.5 always thinks: the object still carries the binding
				return p
			},
			opts:     anthropic.Options{BindingMode: "drop_block"},
			warnings: []string{"marker_moved"},
			betas:    []string{anthropic.BetaTurnScopedSystem, anthropic.BetaThinkingBinding},
		},
	}
	for _, tc := range cases {
		t.Run(strings.TrimSuffix(tc.name, ".json"), func(t *testing.T) {
			b := build(t, tc.prompt(), tc.opts, tc.stream)
			golden(t, "golden", tc.name, b.Body)
			if !reflect.DeepEqual(codes(b.Warnings), tc.warnings) && !(len(b.Warnings) == 0 && len(tc.warnings) == 0) {
				t.Errorf("warnings = %v, want %v", b.Warnings, tc.warnings)
			}
			if !reflect.DeepEqual(b.Betas, tc.betas) && !(len(b.Betas) == 0 && len(tc.betas) == 0) {
				t.Errorf("betas = %v, want %v", b.Betas, tc.betas)
			}
		})
	}
}

func TestBuildSystemIsAlwaysAnArray(t *testing.T) {
	p := &core.Prompt{Model: "m", System: []core.Block{core.Text("a")}, Messages: []core.Message{user(core.Text("hi"))}}
	m := decode(t, build(t, p, anthropic.Options{}, false).Body)
	sys, ok := m["system"].([]any)
	if !ok || len(sys) != 1 {
		t.Fatalf("system must be an array of blocks, got %T %v", m["system"], m["system"])
	}
	if sys[0].(map[string]any)["type"] != "text" {
		t.Fatalf("system[0] = %v", sys[0])
	}
	// No system at all: the member is omitted rather than sent empty.
	p.System = nil
	if _, ok := decode(t, build(t, p, anthropic.Options{}, false).Body)["system"]; ok {
		t.Fatal("empty system must be omitted")
	}
}

func TestBuildIsVerbatimAndPure(t *testing.T) {
	// Raw values the caller owns must reach the wire byte for byte: odd
	// whitespace and key order are exactly what a map re-marshal would change.
	weirdSchema := `{ "type" : "object",  "properties":{"z":{"type":"string"},"a":{"type":"number"}} }`
	weirdInput := `{"path" :  "a.txt",  "lines":[1, 2],"z":1,"a":2}`
	wire := `{"type": "thinking",  "thinking": "hm \u003c  x", "signature": "sig-fixture-9"}`
	p := &core.Prompt{
		Model: "claude-opus-5-5",
		Tools: []core.ToolSpec{{Name: "t", Description: "d", InputSchema: json.RawMessage(weirdSchema)}},
		Messages: []core.Message{
			user(core.Text("go")),
			asst(
				core.Block{Kind: core.BlockThinking, Wire: json.RawMessage(wire), WireFormat: "anthropic"},
				core.ToolUse("toolu_x", "t", json.RawMessage(weirdInput)),
			),
			user(core.ToolResult("toolu_x", false, core.Text("<b>&</b>"))),
		},
	}
	before, _ := json.Marshal(p)
	body := build(t, p, anthropic.Options{}, false).Body
	for _, want := range []string{weirdSchema, weirdInput, wire, `"text":"<b>&</b>"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("body lost verbatim bytes %q:\n%s", want, body)
		}
	}
	after, _ := json.Marshal(p)
	if string(before) != string(after) {
		t.Fatal("Build mutated the prompt")
	}
	again := build(t, p, anthropic.Options{}, false).Body
	if string(again) != string(body) {
		t.Fatal("Build is not deterministic")
	}
}

func TestBuildTextEncoding(t *testing.T) {
	// Control characters, invalid UTF-8, line separators, astral runes and NULs
	// must produce valid JSON that decodes to the (sanitised) original.
	texts := []string{
		"plain",
		"tab\tnewline\ncr\rbell\a\x00nul\x1f",
		"quote \" backslash \\ slash / <html> & 'x'",
		"\u2028line\u2029sep",
		"emoji \U0001F40E and \u00e9 and \u4e2d\u6587",
		"bad utf8 \xff\xfe end",
		strings.Repeat("x", 1<<20),
	}
	for i, s := range texts {
		p := &core.Prompt{Model: "m", Messages: []core.Message{user(core.Text(s))}}
		b := build(t, p, anthropic.Options{}, false)
		m := decode(t, b.Body)
		got := m["messages"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
		enc, _ := json.Marshal(s) // encoding/json replaces every invalid byte with U+FFFD
		var want string
		_ = json.Unmarshal(enc, &want)
		if got != want {
			t.Errorf("case %d: text did not round-trip (len got %d want %d)", i, len(got), len(want))
		}
	}
	// HTML characters are not escaped.
	p := &core.Prompt{Model: "m", Messages: []core.Message{user(core.Text("a<b>&c"))}}
	if !strings.Contains(string(build(t, p, anthropic.Options{}, false).Body), `"a<b>&c"`) {
		t.Fatal("HTML escaping must stay off")
	}
}

func TestBuildBreakpointPlacement(t *testing.T) {
	type tc struct {
		name     string
		prompt   func() *core.Prompt
		opts     anthropic.Options
		markers  int
		warnings []string
		check    func(t *testing.T, body string)
	}
	// hotTail: [user, assistant(thinking, text), user(tool_result...)].
	tail := func(bps ...core.Breakpoint) func() *core.Prompt {
		return func() *core.Prompt {
			p := toolLoopPrompt(false)
			p.Breakpoints = bps
			return p
		}
	}
	cases := []tc{
		{
			name:    "on a text block",
			prompt:  tail(bp(ref(0, 0), 0, "a")),
			markers: 1,
			check: func(t *testing.T, body string) {
				if !strings.Contains(body, `{"type":"text","text":"Read a.txt and run the tests.","cache_control":{"type":"ephemeral"}}`) {
					t.Errorf("marker not on the user text block:\n%s", body)
				}
			},
		},
		{
			name:     "on a thinking block moves to the previous eligible block",
			prompt:   tail(bp(ref(1, 0), 0, "thinking")),
			markers:  1,
			warnings: []string{"marker_moved"},
			check: func(t *testing.T, body string) {
				if !strings.Contains(body, `"text":"Read a.txt and run the tests.","cache_control"`) {
					t.Errorf("marker did not move to the earlier user text:\n%s", body)
				}
			},
		},
		{
			name:     "on an empty tool_result moves back within the same message",
			prompt:   tail(bp(ref(2, 2), 0, "rolling")),
			markers:  1,
			warnings: []string{"marker_moved"},
			check: func(t *testing.T, body string) {
				if !strings.Contains(body, `"tool_use_id":"toolu_02","content":[{"type":"text","text":"FAIL: TestA"}],"is_error":true,"cache_control"`) {
					t.Errorf("marker did not land on the previous tool_result:\n%s", body)
				}
			},
		},
		{
			name:    "on a tool_result carries the marker on the block itself",
			prompt:  tail(bp(ref(2, 1), 0, "rolling")),
			markers: 1,
			check: func(t *testing.T, body string) {
				if !strings.Contains(body, `"is_error":true,"cache_control":{"type":"ephemeral"}}`) {
					t.Errorf("tool_result marker missing:\n%s", body)
				}
			},
		},
		{
			name:    "on a tool_use replayed from wire splices without touching the raw bytes",
			prompt:  tail(bp(ref(1, 2), 0, "tu")),
			markers: 1,
			check: func(t *testing.T, body string) {
				want := `{"type":"tool_use","id":"toolu_01","name":"read","input":{ "path" : "a.txt" },"cache_control":{"type":"ephemeral"}}`
				if !strings.Contains(body, want) {
					t.Errorf("want %s in\n%s", want, body)
				}
			},
		},
		{
			name:    "on the tools segment lands on the last tool whatever Blk says",
			prompt:  tail(core.Breakpoint{After: core.BlockRef{Sys: true, Msg: -1, Blk: 0}, TTL: time.Hour}),
			markers: 1,
			check: func(t *testing.T, body string) {
				if !strings.Contains(body, `"strict":true,"cache_control":{"type":"ephemeral","ttl":"1h"}}]`) {
					t.Errorf("marker not on the last tool:\n%s", body)
				}
			},
		},
		{
			name:     "tools segment without tools",
			prompt:   func() *core.Prompt { p := tail(bp(toolsRef(), 0, "tools"))(); p.Tools = nil; return p },
			markers:  0,
			warnings: []string{"marker_unresolved"},
		},
		{
			name:     "block that does not exist",
			prompt:   tail(bp(ref(9, 0), 0, "x"), bp(ref(0, 7), 0, "y")),
			markers:  0,
			warnings: []string{"marker_unresolved", "marker_unresolved"},
		},
		{
			name:    "duplicate breakpoints on one block make one marker, the longer TTL wins",
			prompt:  tail(bp(ref(0, 0), 0, "a"), bp(ref(0, 0), time.Hour, "b")),
			markers: 1,
			check: func(t *testing.T, body string) {
				if !strings.Contains(body, `"ttl":"1h"`) {
					t.Errorf("1h lost:\n%s", body)
				}
			},
		},
		{
			name: "more than four keep the latest four",
			prompt: tail(bp(sysRef(0), 0, "e1"), bp(ref(0, 0), 0, "e2"), bp(ref(1, 1), 0, "e3"),
				bp(ref(1, 2), 0, "e4"), bp(ref(2, 0), 0, "e5"), bp(ref(2, 1), 0, "e6")),
			markers:  4,
			warnings: []string{"marker_capped", "marker_capped"},
			check: func(t *testing.T, body string) {
				if strings.Contains(body, `"text":"You are Sleipnir.","cache_control"`) {
					t.Errorf("the earliest marker should have been dropped:\n%s", body)
				}
			},
		},
		{
			name:     "a 5m marker before a 1h marker is raised to 1h",
			prompt:   tail(bp(sysRef(0), 0, "sys"), bp(ref(0, 0), time.Hour, "user")),
			markers:  2,
			warnings: []string{"ttl_promoted"},
			check: func(t *testing.T, body string) {
				if strings.Count(body, `"ttl":"1h"`) != 2 {
					t.Errorf("want both markers at 1h:\n%s", body)
				}
			},
		},
		{
			name:    "1h before 5m is fine as given",
			prompt:  tail(bp(sysRef(0), time.Hour, "sys"), bp(ref(0, 0), 0, "user")),
			markers: 2,
			check: func(t *testing.T, body string) {
				if strings.Count(body, `"ttl":"1h"`) != 1 {
					t.Errorf("exactly one 1h marker expected:\n%s", body)
				}
			},
		},
		{
			name:    "TTL under an hour is the default lifetime",
			prompt:  tail(bp(ref(0, 0), 59*time.Minute, "x")),
			markers: 1,
			check: func(t *testing.T, body string) {
				if strings.Contains(body, `"ttl"`) {
					t.Errorf("ttl must only be sent for >= 1h:\n%s", body)
				}
			},
		},
		{
			name: "marker on an empty text block that was not sent moves back",
			prompt: func() *core.Prompt {
				p := toolLoopPrompt(false)
				p.Messages[0].Blocks = append(p.Messages[0].Blocks, core.Text("   "))
				p.Breakpoints = []core.Breakpoint{bp(ref(0, 1), 0, "x")}
				return p
			},
			markers:  1,
			warnings: []string{"empty_block_dropped", "marker_moved"},
		},
		{
			name: "no earlier block can carry it",
			prompt: func() *core.Prompt {
				return &core.Prompt{Model: "m", Messages: []core.Message{
					{Role: core.RoleUser, Blocks: []core.Block{core.Text(" "), core.Text("real")}},
				}, Breakpoints: []core.Breakpoint{bp(ref(0, 0), 0, "x")}}
			},
			markers:  0,
			warnings: []string{"empty_block_dropped", "marker_dropped"},
		},
		{
			name: "system block marker",
			prompt: func() *core.Prompt {
				p := toolLoopPrompt(false)
				p.Breakpoints = []core.Breakpoint{bp(sysRef(0), 0, "const")}
				return p
			},
			markers: 1,
			check: func(t *testing.T, body string) {
				if !strings.Contains(body, `"system":[{"type":"text","text":"You are Sleipnir.","cache_control":{"type":"ephemeral"}}]`) {
					t.Errorf("system marker missing:\n%s", body)
				}
			},
		},
		{
			name:    "custom cap",
			prompt:  tail(bp(ref(0, 0), 0, "a"), bp(ref(1, 1), 0, "b"), bp(ref(1, 2), 0, "c")),
			opts:    anthropic.Options{MaxBreakpoints: 2},
			markers: 2, warnings: []string{"marker_capped"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := build(t, c.prompt(), c.opts, false)
			body := string(b.Body)
			if n := strings.Count(body, `"cache_control"`); n != c.markers {
				t.Errorf("markers = %d, want %d\n%s", n, c.markers, body)
			}
			if got := codes(b.Warnings); !(len(got) == 0 && len(c.warnings) == 0) && !reflect.DeepEqual(got, c.warnings) {
				t.Errorf("warnings = %v, want %v", b.Warnings, c.warnings)
			}
			if c.check != nil {
				c.check(t, body)
			}
		})
	}
}

func TestBuildTurnScopedSystemMessages(t *testing.T) {
	// Native: role system, text blocks, clear_at, never a cache_control marker.
	b := build(t, hotLoopPrompt(), anthropic.Options{}, false)
	m := decode(t, b.Body)
	msgs := m["messages"].([]any)
	if len(msgs) != 7 {
		t.Fatalf("messages = %d, want 7 (nothing merged or dropped)", len(msgs))
	}
	for _, i := range []int{3, 6} {
		sm := msgs[i].(map[string]any)
		if sm["role"] != "system" || sm["clear_at"] != "next_user_message" {
			t.Errorf("messages[%d] = %v", i, sm)
		}
		if strings.Contains(fmt.Sprint(sm["content"]), "cache_control") {
			t.Errorf("a turn-scoped message must never carry cache_control: %v", sm)
		}
	}
	if !reflect.DeepEqual(b.Betas, []string{anthropic.BetaTurnScopedSystem}) {
		t.Errorf("betas = %v", b.Betas)
	}

	// Folded: the same text sits at the end of the preceding user message, in the
	// same place on every later request (append-only), and no beta is needed.
	f := build(t, hotLoopPrompt(), anthropic.Options{NoTurnScopedSystem: true}, false)
	fm := decode(t, f.Body)["messages"].([]any)
	if len(fm) != 5 {
		t.Fatalf("folded messages = %d, want 5", len(fm))
	}
	last := fm[2].(map[string]any)["content"].([]any)
	if len(last) != 2 || last[1].(map[string]any)["text"] != "board: 3 tasks open" {
		t.Errorf("hot text not folded after the tool_result: %v", last)
	}
	if len(f.Betas) != 0 {
		t.Errorf("no beta expected when folding: %v", f.Betas)
	}
	// The rolling marker asked for the (now folded) hot block, which is an
	// ordinary text block here, so it stays put.
	if !strings.Contains(string(f.Body), `"text":"board: 2 tasks open","cache_control"`) {
		t.Errorf("marker should sit on the folded text:\n%s", f.Body)
	}

	// Growth must be append-only on the wire: the body of request N, minus its
	// tail, is a prefix of request N+1 (this is what keeps the cache and thinking
	// signatures valid). Compare message by message.
	short := hotLoopPrompt()
	short.Messages = short.Messages[:4]
	short.Breakpoints = nil
	long := hotLoopPrompt()
	long.Breakpoints = nil
	sm := decode(t, build(t, short, anthropic.Options{}, false).Body)["messages"].([]any)
	lm := decode(t, build(t, long, anthropic.Options{}, false).Body)["messages"].([]any)
	for i := range sm {
		if !reflect.DeepEqual(sm[i], lm[i]) {
			t.Errorf("messages[%d] changed when the conversation grew:\n%v\n%v", i, sm[i], lm[i])
		}
	}
}

func TestBuildSystemMessageEdgeCases(t *testing.T) {
	base := func(msgs ...core.Message) *core.Prompt {
		return &core.Prompt{Model: "claude-opus-5-5", Messages: msgs}
	}
	t.Run("first message becomes user text", func(t *testing.T) {
		b := build(t, base(hot("next_user_message", "reminder"), user(core.Text("go"))), anthropic.Options{}, false)
		msgs := decode(t, b.Body)["messages"].([]any)
		if msgs[0].(map[string]any)["role"] != "user" {
			t.Fatalf("a system message cannot be first: %v", msgs[0])
		}
		if !reflect.DeepEqual(codes(b.Warnings), []string{"system_folded"}) {
			t.Errorf("warnings = %v", b.Warnings)
		}
	})
	t.Run("after an assistant message folds into a user message", func(t *testing.T) {
		b := build(t, base(user(core.Text("q")), asst(core.Text("a")), hot("", "note")), anthropic.Options{}, false)
		msgs := decode(t, b.Body)["messages"].([]any)
		if len(msgs) != 3 || msgs[2].(map[string]any)["role"] != "user" {
			t.Fatalf("messages = %v", msgs)
		}
	})
	t.Run("permanent system message has no clear_at and needs no beta", func(t *testing.T) {
		b := build(t, base(user(core.Text("q")), core.Message{Role: core.RoleSystem, Blocks: []core.Block{core.Text("promote: use mutex")}}), anthropic.Options{}, false)
		if strings.Contains(string(b.Body), "clear_at") || len(b.Betas) != 0 {
			t.Errorf("body %s betas %v", b.Body, b.Betas)
		}
		if !strings.Contains(string(b.Body), `{"role":"system","content":[{"type":"text","text":"promote: use mutex"}]}`) {
			t.Errorf("native system message missing:\n%s", b.Body)
		}
	})
	t.Run("clear_at never is the default", func(t *testing.T) {
		b := build(t, base(user(core.Text("q")), hot("never", "x")), anthropic.Options{}, false)
		if strings.Contains(string(b.Body), "clear_at") || len(b.Betas) != 0 {
			t.Errorf("never must render as the default: %s %v", b.Body, b.Betas)
		}
	})
	t.Run("non-text blocks are dropped and empty messages vanish", func(t *testing.T) {
		m := core.Message{Role: core.RoleSystem, ClearAt: "next_user_message", Blocks: []core.Block{core.ToolUse("a", "b", nil)}}
		b := build(t, base(user(core.Text("q")), m), anthropic.Options{}, false)
		if len(decode(t, b.Body)["messages"].([]any)) != 1 {
			t.Errorf("empty system message must not be sent: %s", b.Body)
		}
		if !reflect.DeepEqual(codes(b.Warnings), []string{"block_dropped", "empty_message_dropped"}) {
			t.Errorf("warnings = %v", b.Warnings)
		}
	})
	t.Run("unknown clear_at is an error", func(t *testing.T) {
		if _, err := anthropic.Build(base(user(core.Text("q")), hot("tomorrow", "x")), anthropic.Options{}, false); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("marker on the folded message stays on an ordinary block", func(t *testing.T) {
		p := base(user(core.Text("q")), asst(core.Text("a")), hot("", "note"))
		p.Breakpoints = []core.Breakpoint{bp(ref(2, 0), 0, "thread")}
		b := build(t, p, anthropic.Options{}, false)
		if !strings.Contains(string(b.Body), `"text":"note","cache_control"`) {
			t.Errorf("%s", b.Body)
		}
	})
}

func TestBuildParams(t *testing.T) {
	tmp := 0.7
	nan := math.NaN()
	mk := func(model string, params core.Params) *core.Prompt {
		return &core.Prompt{Model: model, Tools: testTools(), Params: params, Messages: []core.Message{user(core.Text("hi"))}}
	}
	type tc struct {
		name     string
		prompt   *core.Prompt
		opts     anthropic.Options
		want     []string // substrings that must appear
		not      []string // substrings that must not
		warnings []string
	}
	cases := []tc{
		{name: "default max tokens", prompt: mk("m", core.Params{}), want: []string{`"max_tokens":8192`}},
		{name: "custom default max tokens", prompt: mk("m", core.Params{}), opts: anthropic.Options{DefaultMaxTokens: 100}, want: []string{`"max_tokens":100`}},
		{name: "thinking adaptive", prompt: mk("claude-opus-5-5", core.Params{MaxTokens: 10, Thinking: "adaptive"}), want: []string{`"thinking":{"type":"adaptive"}`}},
		{name: "thinking off is omitted", prompt: mk("claude-opus-5-5", core.Params{MaxTokens: 10, Thinking: "off"}), not: []string{`"thinking"`}},
		{name: "thinking unset is omitted", prompt: mk("claude-opus-5-5", core.Params{MaxTokens: 10}), not: []string{`"thinking"`}},
		{
			name:   "budget model gets a budget under max_tokens",
			prompt: mk("claude-haiku-4-5", core.Params{MaxTokens: 20000, Thinking: "adaptive"}),
			want:   []string{`"thinking":{"type":"enabled","budget_tokens":8192}`},
		},
		{
			name:   "budget is clamped below max_tokens",
			prompt: mk("claude-haiku-4-5", core.Params{MaxTokens: 3000, Thinking: "adaptive"}),
			opts:   anthropic.Options{ThinkingBudget: 5000},
			want:   []string{`"budget_tokens":2999`},
		},
		{
			name:     "no room for a budget",
			prompt:   mk("claude-haiku-4-5", core.Params{MaxTokens: 1024, Thinking: "adaptive"}),
			not:      []string{`"thinking"`},
			warnings: []string{"thinking_unsupported"},
		},
		{
			name:     "model without thinking",
			prompt:   mk("claude-3-5-haiku-20241022", core.Params{MaxTokens: 10, Thinking: "adaptive"}),
			not:      []string{`"thinking"`},
			warnings: []string{"thinking_unsupported"},
		},
		{name: "unknown model passes adaptive through", prompt: mk("gateway/some-model", core.Params{MaxTokens: 10, Thinking: "adaptive", Effort: "max"}),
			want: []string{`"thinking":{"type":"adaptive"}`, `"output_config":{"effort":"max"}`}},
		{name: "prefixed and dated model ids resolve", prompt: mk("anthropic/claude-opus-5-5-20260401", core.Params{MaxTokens: 10, Temperature: &tmp}),
			not: []string{`"temperature"`}, warnings: []string{"temperature_dropped"}},
		{name: "temperature kept where accepted", prompt: mk("claude-sonnet-4-6", core.Params{MaxTokens: 10, Temperature: &tmp}), want: []string{`"temperature":0.7`}},
		{name: "temperature kept on unknown models", prompt: mk("mock-1", core.Params{MaxTokens: 10, Temperature: &tmp}), want: []string{`"temperature":0.7`}},
		{name: "effort dropped where unsupported", prompt: mk("claude-haiku-4-5", core.Params{MaxTokens: 10, Effort: "high"}), not: []string{`output_config`}, warnings: []string{"effort_dropped"}},
		{name: "effort clamped", prompt: mk("claude-opus-4-5", core.Params{MaxTokens: 10, Effort: "max"}), want: []string{`"effort":"high"`}, warnings: []string{"effort_clamped"}},
		{name: "future effort level passes through", prompt: mk("claude-opus-5-5", core.Params{MaxTokens: 10, Effort: "ultra"}), want: []string{`"effort":"ultra"`}},
		{name: "tool_choice none is passed", prompt: mk("m", core.Params{MaxTokens: 10, ToolChoice: "none"}), want: []string{`"tool_choice":{"type":"none"}`}},
		{name: "tool_choice auto is passed", prompt: mk("m", core.Params{MaxTokens: 10, ToolChoice: "auto"}), want: []string{`"tool_choice":{"type":"auto"}`}},
		{name: "tool_choice is never invented", prompt: mk("m", core.Params{MaxTokens: 10}), not: []string{`tool_choice`}},
		{name: "tool_choice without tools is dropped", prompt: func() *core.Prompt {
			p := mk("m", core.Params{MaxTokens: 10, ToolChoice: "none"})
			p.Tools = nil
			return p
		}(),
			not: []string{`tool_choice`}, warnings: []string{"tool_choice_dropped"}},
		{name: "stop sequences", prompt: mk("m", core.Params{MaxTokens: 10, Stop: []string{"A", "", "B"}}), want: []string{`"stop_sequences":["A","B"]`}, warnings: []string{"stop_sequence_dropped"}},
		{name: "user id", prompt: mk("m", core.Params{MaxTokens: 10}), opts: anthropic.Options{UserID: "u1"}, want: []string{`"metadata":{"user_id":"u1"}`}},
		{name: "warm uses zero tokens", prompt: mk("m", core.Params{MaxTokens: 999}), opts: anthropic.Options{Warm: true}, want: []string{`"max_tokens":0`}, not: []string{`"stream"`}},
		{name: "warm on an endpoint without zero tokens", prompt: mk("m", core.Params{MaxTokens: 999}), opts: anthropic.Options{Warm: true, NoZeroMaxTokens: true}, want: []string{`"max_tokens":1`}},
		{name: "warm drops a thinking budget", prompt: mk("claude-haiku-4-5", core.Params{MaxTokens: 999, Thinking: "adaptive"}), opts: anthropic.Options{Warm: true},
			not: []string{`"thinking"`}, warnings: []string{"prewarm_thinking_dropped"}},
		{name: "binding with adaptive", prompt: mk("claude-opus-5-5", core.Params{MaxTokens: 10, Thinking: "adaptive"}), opts: anthropic.Options{BindingMode: "error"},
			want: []string{`"thinking":{"type":"adaptive","block_binding":{"prefix_mismatch_behavior":"error"}}`}},
		{name: "binding needs thinking", prompt: mk("claude-sonnet-5", core.Params{MaxTokens: 10, Thinking: "off"}), opts: anthropic.Options{BindingMode: "drop_block"},
			not: []string{`block_binding`}, warnings: []string{"binding_ignored"}},
		{name: "binding with a budget", prompt: mk("claude-haiku-4-5", core.Params{MaxTokens: 20000, Thinking: "adaptive"}), opts: anthropic.Options{BindingMode: "drop_block"},
			want: []string{`"type":"enabled","budget_tokens":8192,"block_binding"`}},
		{name: "display", prompt: mk("claude-opus-5-5", core.Params{MaxTokens: 10, Thinking: "adaptive"}), opts: anthropic.Options{ThinkingDisplay: "updates"},
			want: []string{`"thinking":{"type":"adaptive","display":"updates"}`}},
		{name: "extra body", prompt: mk("m", core.Params{MaxTokens: 10}), opts: anthropic.Options{ExtraBody: map[string]any{"zeta": 1, "alpha": map[string]any{"b": 2, "a": 1}}},
			want: []string{`],"alpha":{"a":1,"b":2},"zeta":1}`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := build(t, c.prompt, c.opts, false)
			body := string(b.Body)
			for _, w := range c.want {
				if !strings.Contains(body, w) {
					t.Errorf("missing %q in\n%s", w, body)
				}
			}
			for _, n := range c.not {
				if strings.Contains(body, n) {
					t.Errorf("unexpected %q in\n%s", n, body)
				}
			}
			if got := codes(b.Warnings); !(len(got) == 0 && len(c.warnings) == 0) && !reflect.DeepEqual(got, c.warnings) {
				t.Errorf("warnings = %v, want %v", b.Warnings, c.warnings)
			}
		})
	}

	errs := []struct {
		name   string
		mutate func(p *core.Prompt, o *anthropic.Options)
	}{
		{"forced tool_choice", func(p *core.Prompt, o *anthropic.Options) { p.Params.ToolChoice = "any" }},
		{"unknown thinking mode", func(p *core.Prompt, o *anthropic.Options) { p.Params.Thinking = "enabled" }},
		{"unknown binding mode", func(p *core.Prompt, o *anthropic.Options) { o.BindingMode = "drop" }},
		{"NaN temperature", func(p *core.Prompt, o *anthropic.Options) { p.Params.Temperature = &nan }},
		{"extra body collides", func(p *core.Prompt, o *anthropic.Options) { o.ExtraBody = map[string]any{"messages": 1} }},
		{"extra body not encodable", func(p *core.Prompt, o *anthropic.Options) { o.ExtraBody = map[string]any{"x": make(chan int)} }},
	}
	for _, e := range errs {
		t.Run("error/"+e.name, func(t *testing.T) {
			p := mk("m", core.Params{MaxTokens: 10})
			var o anthropic.Options
			e.mutate(p, &o)
			if _, err := anthropic.Build(p, o, false); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestBuildStreamFlag(t *testing.T) {
	p := &core.Prompt{Model: "m", Messages: []core.Message{user(core.Text("hi"))}, Params: core.Params{MaxTokens: 5}}
	if b := build(t, p, anthropic.Options{}, true); !strings.Contains(string(b.Body), `"stream":true`) {
		t.Errorf("stream flag missing: %s", b.Body)
	}
	if b := build(t, p, anthropic.Options{}, false); strings.Contains(string(b.Body), `"stream"`) {
		t.Errorf("non-streaming bodies carry no stream member: %s", b.Body)
	}
}

func TestBuildValidation(t *testing.T) {
	msg := []core.Message{user(core.Text("hi"))}
	cases := []struct {
		name string
		p    *core.Prompt
	}{
		{"nil prompt", nil},
		{"no model", &core.Prompt{Messages: msg}},
		{"blank model", &core.Prompt{Model: "  ", Messages: msg}},
		{"no messages", &core.Prompt{Model: "m"}},
		{"only blank messages", &core.Prompt{Model: "m", Messages: []core.Message{user(core.Text(" "))}}},
		{"tool without name", &core.Prompt{Model: "m", Messages: msg, Tools: []core.ToolSpec{{InputSchema: json.RawMessage(`{}`)}}}},
		{"tool schema not json", &core.Prompt{Model: "m", Messages: msg, Tools: []core.ToolSpec{{Name: "t", InputSchema: json.RawMessage(`{"a":`)}}}},
		{"tool schema not an object", &core.Prompt{Model: "m", Messages: msg, Tools: []core.ToolSpec{{Name: "t", InputSchema: json.RawMessage(`[1]`)}}}},
		{"system block not text", &core.Prompt{Model: "m", Messages: msg, System: []core.Block{{Kind: core.BlockImage}}}},
		{"unsupported role", &core.Prompt{Model: "m", Messages: []core.Message{{Role: "tool", Blocks: []core.Block{core.Text("x")}}}}},
		{"tool_result without id", &core.Prompt{Model: "m", Messages: []core.Message{user(core.Block{Kind: core.BlockToolResult})}}},
		{"thinking in a user message", &core.Prompt{Model: "m", Messages: []core.Message{user(core.Block{Kind: core.BlockThinking})}}},
		{"tool_result in an assistant message", &core.Prompt{Model: "m", Messages: []core.Message{asst(core.ToolResult("a", false))}}},
		{"tool_use without id", &core.Prompt{Model: "m", Messages: []core.Message{user(core.Text("x")), asst(core.ToolUse("", "n", nil))}}},
		{"image without ref", &core.Prompt{Model: "m", Messages: []core.Message{user(core.Block{Kind: core.BlockImage})}}},
		{"image blob without resolver", &core.Prompt{Model: "m", Messages: []core.Message{user(core.Block{Kind: core.BlockImage, MediaType: "image/png", MediaRef: "0123abcd"})}}},
		{"data url that is not base64", &core.Prompt{Model: "m", Messages: []core.Message{user(core.Block{Kind: core.BlockImage, MediaRef: "data:image/png,rawbytes"})}}},
		{"data url without media type", &core.Prompt{Model: "m", Messages: []core.Message{user(core.Block{Kind: core.BlockImage, MediaRef: "data:;base64,AAAA"})}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if b, err := anthropic.Build(c.p, anthropic.Options{}, false); err == nil {
				t.Fatalf("want an error, got body %s", b)
			}
		})
	}
}

func TestBuildImages(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("\x00", 32))
	resolver := func(ref, mt string) ([]byte, error) {
		switch ref {
		case "hash-png":
			return png, nil
		case "hash-text":
			return []byte("just text, not an image"), nil
		}
		return nil, fmt.Errorf("no such blob %s", ref)
	}
	img := func(ref, mt string) *core.Prompt {
		return &core.Prompt{Model: "m", Messages: []core.Message{user(core.Block{Kind: core.BlockImage, MediaType: mt, MediaRef: ref})}}
	}
	ok := []struct {
		name, ref, mt, want string
	}{
		{"data url", pixel, "", `"source":{"type":"base64","media_type":"image/png","data":"iVBOR`},
		{"https url", "https://example.com/a.png", "image/png", `"source":{"type":"url","url":"https://example.com/a.png"}`},
		{"file id", "file_011CNha8", "image/png", `"source":{"type":"file","file_id":"file_011CNha8"}`},
		{"blob via resolver", "hash-png", "image/png", `"media_type":"image/png","data":"iVBORw0KGgo`},
		{"blob type sniffed", "hash-png", "", `"media_type":"image/png"`},
	}
	for _, c := range ok {
		t.Run(c.name, func(t *testing.T) {
			b := build(t, img(c.ref, c.mt), anthropic.Options{Media: resolver}, false)
			if !strings.Contains(string(b.Body), c.want) {
				t.Errorf("want %s in\n%s", c.want, b.Body)
			}
		})
	}
	for _, ref := range []string{"hash-text", "hash-missing"} {
		if _, err := anthropic.Build(img(ref, ""), anthropic.Options{Media: resolver}, false); err == nil {
			t.Errorf("%s: want an error", ref)
		}
	}
}

func TestBuildReplayRules(t *testing.T) {
	redacted := core.Block{Kind: core.BlockRedactedThinking, Wire: json.RawMessage(`{"type":"redacted_thinking","data":"opaque-fixture"}`), WireFormat: "anthropic"}
	compaction := core.Block{Kind: core.BlockCompaction, Wire: json.RawMessage(`{"type":"compaction","content":"summary"}`), WireFormat: "anthropic"}
	unknown := core.Block{Kind: "server_tool_use", Wire: json.RawMessage(`{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{}}`), WireFormat: "anthropic"}
	mk := func(blocks ...core.Block) *core.Prompt {
		return &core.Prompt{Model: "m", Messages: []core.Message{user(core.Text("q")), asst(blocks...)}}
	}
	t.Run("thinking and redacted thinking replay verbatim", func(t *testing.T) {
		b := build(t, mk(thinkingBlock("s1"), redacted, core.Text("a")), anthropic.Options{}, false)
		for _, want := range []string{string(wireThinking("s1")), `{"type":"redacted_thinking","data":"opaque-fixture"}`} {
			if !strings.Contains(string(b.Body), want) {
				t.Errorf("missing %s in\n%s", want, b.Body)
			}
		}
		if len(b.Warnings) != 0 {
			t.Errorf("warnings = %v", b.Warnings)
		}
	})
	t.Run("no wire means dropped", func(t *testing.T) {
		b := build(t, mk(core.Block{Kind: core.BlockThinking, Text: "hidden"}, core.Text("a")), anthropic.Options{}, false)
		if strings.Contains(string(b.Body), "hidden") || strings.Contains(string(b.Body), `"thinking"`) {
			t.Errorf("a thinking block without wire must never be rebuilt:\n%s", b.Body)
		}
		if !reflect.DeepEqual(codes(b.Warnings), []string{"thinking_dropped"}) {
			t.Errorf("warnings = %v", b.Warnings)
		}
	})
	t.Run("wire of another dialect is dropped", func(t *testing.T) {
		blk := thinkingBlock("s1")
		blk.WireFormat = "openai-chat"
		b := build(t, mk(blk, core.Text("a")), anthropic.Options{}, false)
		if strings.Contains(string(b.Body), "signature") {
			t.Errorf("foreign wire leaked:\n%s", b.Body)
		}
	})
	t.Run("corrupt wire is dropped, not sent", func(t *testing.T) {
		blk := thinkingBlock("s1")
		blk.Wire = json.RawMessage(`{"type":"thinking","thinking":`)
		b := build(t, mk(blk, core.Text("a")), anthropic.Options{}, false)
		if strings.Contains(string(b.Body), "signature") {
			t.Errorf("corrupt wire leaked:\n%s", b.Body)
		}
	})
	t.Run("NoThinkingReplay drops everything", func(t *testing.T) {
		b := build(t, mk(thinkingBlock("s1"), redacted, core.Text("a")), anthropic.Options{NoThinkingReplay: true}, false)
		if strings.Contains(string(b.Body), "thinking") {
			t.Errorf("%s", b.Body)
		}
	})
	t.Run("unknown block types replay from wire and drop without it", func(t *testing.T) {
		b := build(t, mk(compaction, unknown, core.Text("a")), anthropic.Options{}, false)
		for _, want := range []string{`{"type":"compaction","content":"summary"}`, `{"type":"server_tool_use","id":"srv_1","name":"web_search","input":{}}`} {
			if !strings.Contains(string(b.Body), want) {
				t.Errorf("missing %s in\n%s", want, b.Body)
			}
		}
		bare := unknown
		bare.Wire = nil
		b = build(t, mk(bare, core.Text("a")), anthropic.Options{}, false)
		if strings.Contains(string(b.Body), "server_tool_use") || !reflect.DeepEqual(codes(b.Warnings), []string{"block_dropped"}) {
			t.Errorf("%s %v", b.Body, b.Warnings)
		}
	})
	t.Run("a marker may sit on a compaction block", func(t *testing.T) {
		p := mk(compaction, core.Text("a"))
		p.Breakpoints = []core.Breakpoint{bp(ref(1, 0), 0, "c")}
		b := build(t, p, anthropic.Options{}, false)
		if !strings.Contains(string(b.Body), `{"type":"compaction","content":"summary","cache_control":{"type":"ephemeral"}}`) {
			t.Errorf("%s", b.Body)
		}
	})
	t.Run("tool_use without wire is rebuilt from its fields", func(t *testing.T) {
		b := build(t, mk(core.ToolUse("toolu_1", "bash", nil)), anthropic.Options{}, false)
		if !strings.Contains(string(b.Body), `{"type":"tool_use","id":"toolu_1","name":"bash","input":{}}`) {
			t.Errorf("%s", b.Body)
		}
	})
	t.Run("a non-object tool input is replaced by an empty object", func(t *testing.T) {
		blk := core.Block{Kind: core.BlockToolUse, ToolID: "toolu_1", ToolName: "bash", Input: json.RawMessage(`"cut off {"`), Invalid: "cut"}
		b := build(t, mk(blk), anthropic.Options{}, false)
		if !strings.Contains(string(b.Body), `"input":{}`) || !reflect.DeepEqual(codes(b.Warnings), []string{"tool_input_not_object"}) {
			t.Errorf("%s %v", b.Body, b.Warnings)
		}
	})
}

func TestBuildBlankContent(t *testing.T) {
	p := &core.Prompt{
		Model:  "m",
		System: []core.Block{core.Text(" \n"), core.Text("kept")},
		Messages: []core.Message{
			user(core.Text("q"), core.Text("")),
			asst(core.Text("  ")), // becomes an empty message: not sent
			user(core.ToolResult("toolu_1", false, core.Text(" "), core.Block{Kind: core.BlockThinking})),
		},
	}
	b := build(t, p, anthropic.Options{}, false)
	m := decode(t, b.Body)
	if n := len(m["system"].([]any)); n != 1 {
		t.Errorf("system blocks = %d", n)
	}
	msgs := m["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	tr := msgs[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if _, has := tr["content"]; has {
		t.Errorf("an empty tool_result carries no content member: %v", tr)
	}
	want := []string{"empty_block_dropped", "empty_block_dropped", "empty_block_dropped", "empty_message_dropped", "empty_block_dropped", "block_dropped"}
	if !reflect.DeepEqual(codes(b.Warnings), want) {
		t.Errorf("warnings = %v, want %v", codes(b.Warnings), want)
	}
}

func TestBuildBetas(t *testing.T) {
	p := func(mut func(*core.Prompt)) *core.Prompt {
		x := hotLoopPrompt()
		mut(x)
		return x
	}
	cases := []struct {
		name string
		p    *core.Prompt
		o    anthropic.Options
		want []string
	}{
		{"nothing", p(func(x *core.Prompt) { x.Messages = x.Messages[:3] }), anthropic.Options{}, nil},
		{"clear_at", hotLoopPrompt(), anthropic.Options{}, []string{anthropic.BetaTurnScopedSystem}},
		{"folded clear_at needs no beta", hotLoopPrompt(), anthropic.Options{NoTurnScopedSystem: true}, nil},
		{"binding", p(func(x *core.Prompt) { x.Messages = x.Messages[:3] }), anthropic.Options{BindingMode: "error"}, []string{anthropic.BetaThinkingBinding}},
		{"all three", hotLoopPrompt(), anthropic.Options{BindingMode: "error", ThinkingDisplay: "updates"},
			[]string{anthropic.BetaTurnScopedSystem, anthropic.BetaThinkingBinding, anthropic.BetaThinkingDisplayUpd}},
		{"summarized display needs none", p(func(x *core.Prompt) { x.Messages = x.Messages[:3] }), anthropic.Options{ThinkingDisplay: "summarized"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := build(t, c.p, c.o, false).Betas
			if !(len(got) == 0 && len(c.want) == 0) && !reflect.DeepEqual(got, c.want) {
				t.Errorf("betas = %v, want %v", got, c.want)
			}
		})
	}
}

func TestBuildWarnsAboutInlineHotAndResultOrder(t *testing.T) {
	inline := core.Text("board view")
	inline.Ephemeral = true
	p := &core.Prompt{Model: "m", Messages: []core.Message{
		user(core.Text("q")),
		asst(core.ToolUse("toolu_1", "bash", nil)),
		user(core.ToolResult("toolu_1", false, core.Text("ok")), inline),
		asst(core.ToolUse("toolu_2", "bash", nil)),
		user(core.Text("text first"), core.ToolResult("toolu_2", false, core.Text("ok"))),
	}}
	b := build(t, p, anthropic.Options{}, false)
	got := codes(b.Warnings)
	if !reflect.DeepEqual(got, []string{"ephemeral_inline", "tool_result_order"}) {
		t.Errorf("warnings = %v", b.Warnings)
	}
	// Blocks are rendered as given: the adapter never reorders.
	if !strings.Contains(string(b.Body), `[{"type":"text","text":"text first"},{"type":"tool_result"`) {
		t.Errorf("content must not be reordered:\n%s", b.Body)
	}
}

func TestBuildLargePrompt(t *testing.T) {
	// 40 turns of 100 KB tool results: must stay linear and valid.
	p := &core.Prompt{Model: "m", Tools: testTools(), Messages: []core.Message{user(core.Text("start"))}}
	big := strings.Repeat("line of output\n", 7000)
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("toolu_%d", i)
		p.Messages = append(p.Messages,
			asst(core.ToolUse(id, "bash", json.RawMessage(`{"command":"ls"}`))),
			user(core.ToolResult(id, false, core.Text(big))))
	}
	p.Breakpoints = []core.Breakpoint{bp(ref(80, 0), 0, "thread")}
	start := time.Now()
	b := build(t, p, anthropic.Options{}, true)
	if len(b.Body) < 4<<20 {
		t.Fatalf("body only %d bytes", len(b.Body))
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("build took %v", d)
	}
}
