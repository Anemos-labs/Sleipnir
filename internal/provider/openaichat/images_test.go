package openaichat_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/provider/openaichat"
)

// A screenshot a tool returned reaches the model: a role=tool message carries text only, so the image follows in a user message
// that names the call, sent as a data URL of the blob's bytes and never as the blob's hash.
func TestToolResultImagesReachTheModel(t *testing.T) {
	media := func(ref, _ string) ([]byte, error) { return []byte("png:" + ref), nil }
	p := &core.Prompt{Model: "m", Messages: []core.Message{
		{Role: core.RoleUser, Blocks: []core.Block{core.Text("look at the app")}},
		{Role: core.RoleAssistant, Blocks: []core.Block{{Kind: core.BlockToolUse, ToolID: "c1", ToolName: "read", Input: json.RawMessage(`{"path":"shot.png"}`)}}},
		{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("c1", false, core.Text("Image shot.png (image/png, 3 B)"),
			core.Block{Kind: core.BlockImage, MediaType: "image/png", MediaRef: "h1"})}},
	}}
	body, err := openaichat.Build(p, openaichat.Options{Media: media}, false)
	if err != nil {
		t.Fatal(err)
	}
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatal(err)
	}
	if n := len(req.Messages); n != 4 || req.Messages[2].Role != "tool" || req.Messages[3].Role != "user" {
		t.Fatalf("messages: %s", body)
	}
	last := string(req.Messages[3].Content)
	if !strings.Contains(last, "Image returned by tool call c1:") || !strings.Contains(last, `"url":"data:image/png;base64,cG5nOmgx"`) {
		t.Fatalf("the image does not follow the tool message: %s", last)
	}
	if strings.Contains(string(body), `"url":"h1"`) {
		t.Fatalf("a blob hash was sent as an image URL: %s", body)
	}
	// An image that cannot be resolved is a note, not a failed request: it stays in the history, and every later request would fail.
	b2, err := openaichat.Build(p, openaichat.Options{}, false)
	if err != nil || !strings.Contains(string(b2), "an image is not shown") || strings.Contains(string(b2), `"h1"`) {
		t.Fatalf("an unresolvable image: %v %s", err, b2)
	}
}

// A prompt with no image renders as it did: the image path adds nothing to the bytes of a text-only conversation.
func TestToolResultWithoutImagesIsUnchanged(t *testing.T) {
	p := &core.Prompt{Model: "m", Messages: []core.Message{
		{Role: core.RoleAssistant, Blocks: []core.Block{{Kind: core.BlockToolUse, ToolID: "c1", ToolName: "read", Input: json.RawMessage(`{}`)}}},
		{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("c1", false, core.Text("ok"))}},
	}}
	body, err := openaichat.Build(p, openaichat.Options{}, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"role":"user"`) {
		t.Fatalf("a user message was added: %s", body)
	}
}

// A text-only model gets a note in place of each image, decided when the request is rendered: the conversation keeps the
// image for a model that sees it (after a switch, or on a fallback).
func TestTextOnlyModelGetsNotes(t *testing.T) {
	media := func(ref, _ string) ([]byte, error) { return []byte("png"), nil }
	p := &core.Prompt{Model: "m", Messages: []core.Message{
		{Role: core.RoleAssistant, Blocks: []core.Block{{Kind: core.BlockToolUse, ToolID: "c1", ToolName: "read", Input: json.RawMessage(`{}`)}}},
		{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("c1", false, core.Text("Image"), core.Block{Kind: core.BlockImage, MediaType: "image/png", MediaRef: "h1"}),
			{Kind: core.BlockImage, MediaRef: "data:image/png;base64,AAAA"}}},
	}}
	body, err := openaichat.Build(p, openaichat.Options{Media: media, NoImages: true}, false)
	if err != nil || strings.Contains(string(body), "image_url") || strings.Count(string(body), "takes text only") != 2 {
		t.Fatalf("%v %s", err, body)
	}
}

// Switching between a model that sees images and one that takes text only, and back, renders the same stored thread each time:
// the images come back after the text-only model, the thread is never changed, and each request's bytes follow only the model
// it goes to (so a prompt cached for one is never reused for the other, and a switch back renders the first bytes again).
func TestImagesSurviveASwitchToTextOnlyAndBack(t *testing.T) {
	media := func(ref, _ string) ([]byte, error) { return []byte("png"), nil }
	img := core.Block{Kind: core.BlockImage, MediaType: "image/png", MediaRef: "h1"}
	p := &core.Prompt{Model: "m", Messages: []core.Message{
		{Role: core.RoleAssistant, Blocks: []core.Block{{Kind: core.BlockToolUse, ToolID: "c1", ToolName: "read", Input: json.RawMessage(`{}`)}}},
		{Role: core.RoleUser, Blocks: []core.Block{core.ToolResult("c1", false, core.Text("Image"), img), img}},
	}}
	render := func(textOnly bool) string {
		t.Helper()
		body, err := openaichat.Build(p, openaichat.Options{Media: media, NoImages: textOnly}, false)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	seeing, blind, again := render(false), render(true), render(false)
	if strings.Count(seeing, "data:image/png;base64,") != 2 || strings.Contains(seeing, "takes text only") {
		t.Fatalf("a model that sees images: %s", seeing)
	}
	if strings.Contains(blind, "image_url") || strings.Count(blind, "takes text only") != 2 {
		t.Fatalf("a text-only model: %s", blind)
	}
	if again != seeing {
		t.Fatalf("switching back renders other bytes:\n%s\n%s", seeing, again)
	}
	got := p.Messages[1].Blocks
	if !reflect.DeepEqual(got[1], img) || len(got[0].Result) != 2 || !reflect.DeepEqual(got[0].Result[1], img) {
		t.Fatalf("rendering changed the stored thread: %+v", got)
	}
}
