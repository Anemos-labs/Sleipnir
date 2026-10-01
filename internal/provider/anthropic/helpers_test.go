package anthropic_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
)

var update = flag.Bool("update", false, "rewrite golden files under testdata/")

// golden compares got (indented for review) with testdata/<dir>/<name>. Run
// `go test ./internal/provider/anthropic -update` to rewrite after an
// intentional change, then read the diff: a golden body is the contract with the
// endpoint.
func golden(t *testing.T, dir, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", dir, name)
	var pretty []byte
	if strings.HasSuffix(name, ".json") {
		var buf bytes.Buffer
		if err := json.Indent(&buf, got, "", "  "); err != nil {
			t.Fatalf("%s is not valid JSON: %v\n%s", name, err, got)
		}
		buf.WriteByte('\n')
		pretty = buf.Bytes()
	} else {
		pretty = got
	}
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, pretty, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden %s (run with -update): %v", path, err)
	}
	if !bytes.Equal(want, pretty) {
		t.Fatalf("%s differs from golden %s\n--- got ---\n%s\n--- want ---\n%s", name, path, pretty, want)
	}
}

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Schemas are canonical (sorted keys, no spaces) like the ones kv.SortTools
// produces, so Build's verbatim pass-through and a canonical re-encode agree.
const (
	bashSchema = `{"properties":{"command":{"type":"string"}},"required":["command"],"type":"object"}`
	readSchema = `{"properties":{"path":{"type":"string"}},"required":["path"],"type":"object"}`
)

func testTools() []core.ToolSpec {
	return []core.ToolSpec{
		{Name: "bash", Description: "Run a shell command.", InputSchema: json.RawMessage(bashSchema)},
		{Name: "read", Description: "Read a file.", InputSchema: json.RawMessage(readSchema), Strict: true},
	}
}

func user(blocks ...core.Block) core.Message {
	return core.Message{Role: core.RoleUser, Blocks: blocks}
}

func asst(blocks ...core.Block) core.Message {
	return core.Message{Role: core.RoleAssistant, Blocks: blocks}
}

func hot(clearAt string, text string) core.Message {
	return core.Message{Role: core.RoleSystem, ClearAt: clearAt, Blocks: []core.Block{core.Text(text)}}
}

func ref(msg, blk int) core.BlockRef { return core.BlockRef{Msg: msg, Blk: blk} }

func sysRef(blk int) core.BlockRef { return core.BlockRef{Sys: true, Msg: 0, Blk: blk} }

// toolsRef addresses the tools segment the way Prompt.WalkBlocks numbers it.
func toolsRef() core.BlockRef { return core.BlockRef{Sys: true, Msg: -1, Blk: 0} }

func bp(after core.BlockRef, ttl time.Duration, label string) core.Breakpoint {
	return core.Breakpoint{After: after, TTL: ttl, Label: label}
}

// wireThinking is a thinking block exactly as the API sends it, with the
// whitespace and escapes a re-marshal would change.
func wireThinking(sig string) json.RawMessage {
	return json.RawMessage(`{"type":"thinking", "thinking":"weigh <a> & <b>\u2028 then decide", "signature":"` + sig + `"}`)
}

func thinkingBlock(sig string) core.Block {
	return core.Block{Kind: core.BlockThinking, Text: "weigh <a> & <b>" + string(rune(0x2028)) + " then decide", Wire: wireThinking(sig), WireFormat: "anthropic"}
}

func toolUseWire(id, name, input string) core.Block {
	w := `{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}`
	return core.Block{Kind: core.BlockToolUse, ToolID: id, ToolName: name, Input: json.RawMessage(input), Wire: json.RawMessage(w), WireFormat: "anthropic"}
}

// pixel is a 1x1 PNG as a data URL.
const pixel = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/q842iQAAAABJRU5ErkJggg=="
