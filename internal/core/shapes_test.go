package core_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// shapeCase is one Go value whose stable JSON encoding is pinned.
type shapeCase struct {
	name string
	v    any
	// wantErr: MarshalStable must refuse the value.
	wantErr bool
	// lossy: decoding the bytes and encoding the result does not give the same bytes
	// (invalid UTF-8 was replaced on the way in).
	lossy bool
}

// The notice marker kv puts in MediaType (kv.MediaNotice), spelled out because core must
// not import kv: the bytes are what is pinned here.
const mediaNotice = "text/x-sleipnir-notice"

func shapeCases() []shapeCase {
	temp := 0.25
	at := time.Date(2031, time.February, 3, 4, 5, 6, 0, time.UTC)
	sig := "EqMBCkYIBxgCKkCw9vZ3bGx5IGEgZmFrZSBzaWduYXR1cmUgZm9yIHRoZSBnb2xkZW4gdGVzdA=="
	thinkingWire := json.RawMessage(`{ "type" : "thinking", "thinking" : "weigh <a> & <b>", "signature" : "` + sig + `" }`)
	schema := json.RawMessage(`{"properties":{"path":{"type":"string"}},"required":["path"],"type":"object"}`)
	image := core.Block{Kind: core.BlockImage, MediaType: "image/png", MediaRef: string(core.HashString("png bytes"))}
	use := core.ToolUse("toolu_01", "read", json.RawMessage(`{ "path" : "api/list.go", "limit" : 10 }`))
	result := core.ToolResult("toolu_01", false, core.Text("package api"))
	thinking := core.Block{Kind: core.BlockThinking, Text: "weigh <a> & <b>", Wire: thinkingWire, WireFormat: "anthropic"}
	tools := []core.ToolSpec{
		{Name: "bash", Description: "Run a shell command.", InputSchema: json.RawMessage(`{"properties":{"command":{"type":"string"}},"required":["command"],"type":"object"}`)},
		{Name: "read", Description: "Read a file <path> & return it.", InputSchema: schema, Strict: true, ReadOnly: true},
	}
	turnUsage := &core.Usage{InputTokens: 120, CacheReadTokens: 3000, CacheWrite5mTokens: 40, CacheWrite1hTokens: 8, OutputTokens: 77, ReasoningTokens: 30}

	return []shapeCase{
		// Blocks.
		{name: "block/zero", v: core.Block{}},
		{name: "block/text", v: core.Text("hello")},
		{name: "block/text_empty_omits_the_field", v: core.Text("")},
		{name: "block/text_html_and_unicode", v: core.Text("<live>board</live> a & b é😀 x" + lineSep + "y" + paraSep)},
		{name: "block/text_invalid_utf8_replaced", v: core.Text("a\xffb"), lossy: true},
		{name: "block/text_notice_marker", v: core.Block{Kind: core.BlockText, Text: "board v3", MediaType: mediaNotice}},
		{name: "block/text_ephemeral", v: core.Block{Kind: core.BlockText, Text: "<live board=\"v7\">x</live>", Ephemeral: true}},
		{name: "block/tool_use", v: use},
		{name: "block/tool_use_nil_input", v: core.ToolUse("toolu_02", "bash", nil)},
		// How the adapters keep a tool call whose arguments were cut off: the text as a JSON
		// string, plus the reason. Input must be valid JSON; anything else cannot be encoded.
		{name: "block/tool_use_cut_off_input", v: core.Block{Kind: core.BlockToolUse, ToolID: "toolu_03", ToolName: "bash", Input: json.RawMessage(`"{\"command\":\"go te"`), Invalid: "tool input is not valid JSON (cut off by max_tokens?)"}},
		{name: "block/tool_use_input_not_json_is_an_error", v: core.Block{Kind: core.BlockToolUse, ToolID: "toolu_03", ToolName: "bash", Input: json.RawMessage(`{"command":"go te`)}, wantErr: true},
		{name: "block/tool_use_with_wire", v: core.Block{Kind: core.BlockToolUse, ToolID: "toolu_01", ToolName: "read", Input: json.RawMessage(`{"path":"a"}`), Wire: json.RawMessage(`{"type":"tool_use","id":"toolu_01","name":"read","input":{"path":"a"}}`), WireFormat: "anthropic"}},
		{name: "block/tool_result", v: result},
		{name: "block/tool_result_is_error", v: core.ToolResult("toolu_02", true, core.Text("exit status 1"))},
		{name: "block/tool_result_no_content", v: core.ToolResult("toolu_03", true)},
		{name: "block/tool_result_text_and_image", v: core.ToolResult("toolu_04", false, core.Text("see attached"), image)},
		{name: "block/image", v: image},
		{name: "block/thinking_signed", v: thinking},
		{name: "block/thinking_unsigned", v: core.Block{Kind: core.BlockThinking, Text: "hm"}},
		{name: "block/redacted_thinking", v: core.Block{Kind: core.BlockRedactedThinking, Wire: json.RawMessage(`{"type":"redacted_thinking","data":"AAAA"}`), WireFormat: "anthropic"}},
		{name: "block/compaction", v: core.Block{Kind: core.BlockCompaction, Text: "earlier work", Wire: json.RawMessage(`{"type":"compaction","content":"earlier work"}`), WireFormat: "anthropic"}},

		// Messages. A nil block list and an empty one are different bytes.
		{name: "message/user_text", v: core.Message{Role: core.RoleUser, Blocks: []core.Block{core.Text("hello")}}},
		{name: "message/assistant_thinking_and_tool_use", v: core.Message{Role: core.RoleAssistant, Turn: 8, Blocks: []core.Block{thinking, core.Text("reading"), use}}},
		{name: "message/user_tool_results", v: core.Message{Role: core.RoleUser, Turn: 9, Blocks: []core.Block{result, core.ToolResult("toolu_02", true, core.Text("FAIL"))}}},
		{name: "message/turn_scoped_system", v: core.Message{Role: core.RoleSystem, ClearAt: "next_user_message", Turn: 10, Blocks: []core.Block{core.Text("<live board=\"v8\">y</live>")}}},
		{name: "message/nil_blocks", v: core.Message{Role: core.RoleUser}},
		{name: "message/empty_blocks", v: core.Message{Role: core.RoleUser, Blocks: []core.Block{}}},
		{name: "message/zero", v: core.Message{}},

		// Tools, as BuildManifest hashes them.
		{name: "toolspec/full", v: tools[1]},
		{name: "toolspec/nil_schema_is_null", v: core.ToolSpec{Name: "x"}},
		{name: "toolspec/empty_schema_is_an_error", v: core.ToolSpec{Name: "x", InputSchema: json.RawMessage{}}, wantErr: true},
		{name: "toolspec/list", v: tools},
		{name: "toolspec/empty_list", v: []core.ToolSpec{}},
		{name: "toolspec/nil_list", v: []core.ToolSpec(nil)},

		// Turns and usage, as they are logged and archived.
		// At is omitempty, which never omits a struct: a turn with no time still says
		// 0001-01-01. Pinned as it is; omitzero (Go 1.24) would change every such record.
		{name: "turn/zero", v: core.Turn{}},
		{name: "turn/assistant_with_usage", v: core.Turn{ID: 8, Role: core.RoleAssistant, Blocks: []core.Block{thinking, use}, Origin: core.OriginModel, At: at, Model: "golden-model-1", Usage: turnUsage}},
		{name: "turn/user_tool_origin", v: core.Turn{ID: 9, Role: core.RoleUser, Blocks: []core.Block{result}, Origin: core.OriginTool, At: at}},
		{name: "usage/zero", v: core.Usage{}},
		{name: "usage/full", v: *turnUsage},

		// A whole prompt.
		{name: "prompt/zero", v: core.Prompt{}},
		{name: "prompt/full", v: core.Prompt{
			Model:  "golden-model-1",
			Tools:  tools,
			System: []core.Block{core.Text("You are Golden.")},
			Messages: []core.Message{
				{Role: core.RoleUser, Turn: 7, Blocks: []core.Block{core.Text("pins"), core.Text("task")}},
				{Role: core.RoleAssistant, Turn: 8, Blocks: []core.Block{thinking, use}},
				{Role: core.RoleUser, Turn: 9, Blocks: []core.Block{result}},
			},
			Breakpoints: []core.Breakpoint{
				{After: core.BlockRef{Sys: true, Msg: 0, Blk: 0}, TTL: time.Hour, Label: "const"},
				{After: core.BlockRef{Msg: 2, Blk: 0}, Label: "thread"},
			},
			CacheKey: "sl:golden01:0123456789ab:0",
			Params:   core.Params{MaxTokens: 1024, Thinking: "adaptive", Effort: "high", ToolChoice: "auto", Stop: []string{"END"}, Temperature: &temp},
		}},
		{name: "params/zero", v: core.Params{}},
		{name: "breakpoint/tools_ref_and_ttl", v: core.Breakpoint{After: core.BlockRef{Sys: true, Msg: -1, Blk: 0}, TTL: time.Hour, Label: "shared"}},
		{name: "breakpoint/default_ttl", v: core.Breakpoint{After: core.BlockRef{Msg: 3, Blk: 1}}},

		// Records of self-hosted endpoints.
		{name: "trace/full", v: core.TokenTrace{Tokenizer: "tok-1", ModelVersion: "v2", PromptIDs: []int32{1, 2, 3}, CompletionIDs: []int32{4, 5}, Logprobs: []float32{-0.5, -1.25}}},
		{name: "manifest/zero", v: core.Manifest{}},
		{name: "manifest/full", v: core.Manifest{Model: "m", Tools: "T", System: []core.Hash{"s1"}, Base: "a.1", Keep: 3, Add: []core.Hash{"m4", "m5"}, Wire: "W"}},

		// Every constant that is written into a prompt or a log: they are part of the wire
		// format (hashed into every wire hash), not just names.
		{name: "constants/roles_kinds_origins_stops", v: []any{
			core.RoleUser, core.RoleAssistant, core.RoleSystem,
			core.BlockText, core.BlockThinking, core.BlockRedactedThinking, core.BlockToolUse, core.BlockToolResult, core.BlockImage, core.BlockCompaction,
			core.OriginUser, core.OriginModel, core.OriginTool, core.OriginMail, core.OriginSystem, core.OriginDigest, core.OriginTask,
			core.StopEnd, core.StopToolUse, core.StopMaxTokens, core.StopPause, core.StopRefusal, core.StopOther,
		}},

		// Maps: keys come out sorted whatever the insertion order.
		{name: "map/sorted", v: map[string]any{"b": 1, "a": []int{2, 1}, "": nil, "C": map[string]bool{"y": true, "x": false}}},
	}
}

// TestShapesRoundTrip: what the encoding writes can be read back, and writing it again
// gives the same bytes (so a logged prompt can be replayed and re-hashed to the same
// hash). The shapes that are knowingly lossy are skipped by name in shapeCases.
func TestShapesRoundTrip(t *testing.T) {
	for _, c := range shapeCases() {
		if c.wantErr || c.lossy {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			first, err := core.MarshalStable(c.v)
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(first) {
				t.Fatalf("not valid JSON: %s", first)
			}
			if strings.Contains(string(first), "\n") {
				t.Fatalf("contains a newline: %q", first)
			}
			back := reflect.New(reflect.TypeOf(c.v))
			if err := json.Unmarshal(first, back.Interface()); err != nil {
				t.Fatalf("does not decode: %v\n%s", err, first)
			}
			second, err := core.MarshalStable(back.Elem().Interface())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first, second) {
				t.Fatalf("encode, decode, encode changed the bytes:\n  %s\n  %s", first, second)
			}
		})
	}
}

// ---- BuildManifest ----------------------------------------------------------------------

// recordingBlobs stores blobs in memory and remembers what was put, in order.
type recordingBlobs struct {
	byHash map[core.Hash][]byte
	puts   []core.Hash
}

func newRecordingBlobs() *recordingBlobs { return &recordingBlobs{byHash: map[core.Hash][]byte{}} }

func (r *recordingBlobs) Put(b []byte) (core.Hash, error) {
	h := core.HashBytes(b)
	r.byHash[h] = append([]byte(nil), b...)
	r.puts = append(r.puts, h)
	return h, nil
}

func (r *recordingBlobs) Get(h core.Hash) ([]byte, error) {
	b, ok := r.byHash[h]
	if !ok {
		return nil, fmt.Errorf("blob %s missing", h.Short())
	}
	return b, nil
}

// manifestPrompt is the prompt of the n-th request of a fixed conversation: a constant
// prefix (tools, system, the pinned preamble) and n tool exchanges.
func manifestPrompt(exchanges int) *core.Prompt {
	schema := json.RawMessage(`{"properties":{"path":{"type":"string"}},"required":["path"],"type":"object"}`)
	p := &core.Prompt{
		Model: "golden-model-1",
		Tools: []core.ToolSpec{
			{Name: "bash", Description: "Run a shell command.", InputSchema: json.RawMessage(`{"properties":{"command":{"type":"string"}},"required":["command"],"type":"object"}`)},
			{Name: "read", Description: "Read a file <path>.", InputSchema: schema, Strict: true},
		},
		System:   []core.Block{core.Text("You are Golden.")},
		Messages: []core.Message{{Role: core.RoleUser, Turn: 1, Blocks: []core.Block{core.Text("<shared-context>\npins & notes\n</shared-context>")}}},
		// Request mechanics: none of these are part of what the model sees, or of its hash.
		CacheKey:    "sl:golden01:0123456789ab:0",
		Params:      core.Params{MaxTokens: 1024},
		Breakpoints: []core.Breakpoint{{After: core.BlockRef{Msg: 0, Blk: 0}, Label: "shared"}},
	}
	for i := 0; i < exchanges; i++ {
		id := fmt.Sprintf("toolu_%02d", i+1)
		p.Messages = append(p.Messages,
			core.Message{Role: core.RoleAssistant, Turn: core.TurnID(2*i + 2), Blocks: []core.Block{
				{Kind: core.BlockThinking, Text: "hm", Wire: json.RawMessage(`{ "type":"thinking", "signature":"sig-` + id + `" }`), WireFormat: "anthropic"},
				core.ToolUse(id, "read", json.RawMessage(`{ "path" : "a<b>.go" }`)),
			}},
			core.Message{Role: core.RoleUser, Turn: core.TurnID(2*i + 3), Blocks: []core.Block{core.ToolResult(id, false, core.Text("é😀 ok & done"))}},
		)
	}
	return p
}

// manifestGolden builds three successive requests of one agent (the third after an
// early message was rewritten, a declared rebase) and lists, for each, every blob
// BuildManifest stored and the manifest it returned.
func manifestGolden(t *testing.T) string {
	t.Helper()
	var sb strings.Builder
	sb.WriteString(goldenHeader("core.BuildManifest: the blobs it stores and the manifest it returns, for three requests of one agent"))

	rebased := manifestPrompt(2)
	rebased.Messages[1].Blocks[1] = core.ToolUse("toolu_01", "read", json.RawMessage(`{"path":"masked"}`))

	blobs := newRecordingBlobs()
	var prev core.ManifestState
	bases := map[string][]core.Hash{}
	for _, step := range []struct {
		req    string
		prompt *core.Prompt
	}{{"a.1", manifestPrompt(1)}, {"a.2", manifestPrompt(2)}, {"a.3", rebased}} {
		before := len(blobs.puts)
		m, next, err := core.BuildManifest(step.prompt, prev, step.req, blobs.Put)
		if err != nil {
			t.Fatalf("%s: %v", step.req, err)
		}
		label := map[core.Hash]string{m.Tools: "tools"}
		for i, h := range m.System {
			label[h] = fmt.Sprintf("system[%d]", i)
		}
		for i, h := range m.Add {
			label[h] = fmt.Sprintf("message[%d]", m.Keep+i)
		}
		for _, h := range blobs.puts[before:] {
			sb.WriteString(goldenLine(step.req+"/blob/"+label[h], blobs.byHash[h], nil))
		}
		mb, err := core.MarshalStable(m)
		sb.WriteString(goldenLine(step.req+"/manifest", mb, err))

		// Every manifest must expand back to the model-visible prompt it came from.
		got, _, err := m.Expand(bases[m.Base], blobs.Get)
		if err != nil {
			t.Fatalf("%s: Expand: %v", step.req, err)
		}
		want := *step.prompt
		want.Breakpoints, want.CacheKey, want.Params = nil, "", core.Params{}
		want.Messages = append([]core.Message(nil), step.prompt.Messages...)
		for i := range want.Messages {
			want.Messages[i].Turn = 0
		}
		gb, _ := core.MarshalStable(got)
		wb, _ := core.MarshalStable(&want)
		if !bytes.Equal(gb, wb) {
			t.Fatalf("%s: the manifest does not expand to its prompt:\n  %s\n  %s", step.req, gb, wb)
		}
		bases[step.req] = next.Msgs
		prev = next
	}
	return sb.String()
}
