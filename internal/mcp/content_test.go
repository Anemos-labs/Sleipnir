package mcp

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/reee344/sleipnir/internal/core"
)

// failingBlobs is a store that refuses every write.
type failingBlobs struct{}

func (failingBlobs) Put([]byte) (core.Hash, error) { return "", errors.New("disk full") }

func TestRenderResultCutsOnARuneBoundary(t *testing.T) {
	// Every rune is three bytes and the limit is not a multiple of three, so a
	// byte-exact cut lands inside a rune. The cut must not be what makes the text
	// invalid: it reaches the model and the blob store as UTF-8.
	text := strings.Repeat("€", maxResultChars/3+10)
	if len(text) <= maxResultChars || maxResultChars%3 == 0 {
		t.Fatal("test text does not straddle the limit")
	}
	for _, name := range []string{"one item", "after another item"} {
		t.Run(name, func(t *testing.T) {
			content := []Content{{Type: "text", Text: text}}
			if name != "one item" {
				content = []Content{{Type: "text", Text: "x"}, {Type: "text", Text: text}}
			}
			got := renderResult(&CallToolResult{Content: content}, nil, false, nil).text
			if !utf8.ValidString(got) {
				t.Error("the cut produced invalid UTF-8")
			}
			if !strings.HasSuffix(got, "\n[server output cut at 4.0 MiB]") {
				t.Errorf("no cut marker: ...%q", got[len(got)-60:])
			}
			if len(got) > maxResultChars+100 {
				t.Errorf("kept %d bytes", len(got))
			}
		})
	}
}

func TestRenderResultMediaStorage(t *testing.T) {
	image := func() []Content {
		return []Content{{Type: "image", MIMEType: "image/png", Data: []byte("\x89PNG\r\n\x1a\n")}}
	}
	tests := []struct {
		name    string
		blobs   blobStore
		content []Content
		attach  bool
		want    string
		stored  bool
		blocks  int
	}{
		{"no store", nil, image(), false, "[image: image/png, 8 bytes, not stored; not shown]", false, 0},
		{"store that fails", failingBlobs{}, image(), true, "[image: image/png, 8 bytes, not stored; not shown]", false, 0},
		{"stored", &memBlobs{}, image(), false, "stored as blob ", true, 0},
		{"stored and attached", &memBlobs{}, image(), true, "stored as blob ", true, 1},
		{"audio is never attached", &memBlobs{}, []Content{{Type: "audio", MIMEType: "audio/wav", Data: []byte("RIFF")}}, true, "[audio: audio/wav, 4 bytes, stored as blob ", true, 0},
		{"bad base64", &memBlobs{}, []Content{{Type: "image", MIMEType: "image/png", BadData: true}}, true, "[image omitted: image/png, payload was not valid base64]", false, 0},
		{"no media type", &memBlobs{}, []Content{{Type: "image", Data: []byte("x")}}, false, "application/octet-stream", true, 0},
		{"over the size limit", &memBlobs{}, []Content{{Type: "image", MIMEType: "image/png", Data: make([]byte, maxMediaBytes+1)}}, true, "not stored: over the 8.0 MiB limit", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := renderResult(&CallToolResult{Content: tt.content}, tt.blobs, tt.attach, nil)
			if !strings.Contains(got.text, tt.want) {
				t.Errorf("text = %q, want it to contain %q", got.text, tt.want)
			}
			if len(got.blocks) != tt.blocks {
				t.Errorf("%d image blocks, want %d", len(got.blocks), tt.blocks)
			}
			stored := len(got.media) > 0 && got.media[0].Ref != ""
			if stored != tt.stored {
				t.Errorf("stored = %v, want %v (%+v)", stored, tt.stored, got.media)
			}
			if m, ok := tt.blobs.(*memBlobs); ok && stored != (m.Len() == 1) {
				t.Errorf("store holds %d blobs", m.Len())
			}
		})
	}
}

func TestRenderResultRedactsAcrossItems(t *testing.T) {
	// A credential echoed by a server is removed wherever it appears in the
	// result, including in text the renderer builds itself.
	r := newRedactor([]string{placeholder})
	res := &CallToolResult{
		Content: []Content{
			{Type: "text", Text: "auth: " + placeholder},
			{Type: "resource_link", URI: "https://example.com/?k=" + placeholder, Name: "link"},
		},
		StructuredContent: []byte(`{"k":"` + placeholder + `"}`),
	}
	got := renderResult(res, nil, false, r).text
	if strings.Contains(got, placeholder) {
		t.Errorf("secret survived: %q", got)
	}
}
