package openairesp_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider/openairesp"
)

func TestToolResultImagesReachTheModel(t *testing.T) {
	media := func(ref, _ string) ([]byte, error) { return []byte("png:" + ref), nil }
	p := basePrompt()
	p.Messages = append(p.Messages,
		core.Message{Role: core.RoleAssistant, Blocks: []core.Block{{Kind: core.BlockToolUse, ToolID: "c1", ToolName: "read", Input: json.RawMessage(`{"path":"shot.png"}`)}}},
		core.Message{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("c1", false, core.Text("Image shot.png"),
			core.Block{Kind: core.BlockImage, MediaType: "image/png", MediaRef: "h1"})}})
	b, err := openairesp.Build(p, openairesp.Options{Media: media}, false)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	out := strings.Index(s, `"function_call_output"`)
	img := strings.Index(s, `"image_url":"data:image/png;base64,cG5nOmgx"`)
	if out < 0 || img < out || !strings.Contains(s, "Image returned by tool call c1:") {
		t.Fatalf("the image does not follow the call's output: %s", s)
	}
	// An image that cannot be resolved is a note, not a failed request: it stays in the history, and every later request would fail.
	b2, err := openairesp.Build(p, openairesp.Options{}, false)
	if err != nil || !strings.Contains(string(b2), "an image is not shown") || strings.Contains(string(b2), `"h1"`) {
		t.Fatalf("an unresolvable image: %v %s", err, b2)
	}
}
