package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/core"
)

// Rendering a tool result for the model.
//
// A result is a list of typed content items. Text goes through as text, after
// sanitising (it is data from a third party: control sequences, invisible
// characters and invalid UTF-8 are removed). Everything else is summarised, not
// forwarded:
//
//   - images and audio are stored in the blob store, and the model sees a short
//     placeholder with the media type and size. A picture from an untrusted
//     server is an injection channel (text inside it, read by a vision model)
//     and costs a lot of tokens; a harness whose provider supports images can
//     opt in with Options.AttachMedia;
//   - resource links become one line (name, URI, type), never fetched;
//   - embedded resources contribute their text, or a placeholder for a blob;
//   - unknown content types are named and skipped;
//   - structuredContent is rendered as compact JSON unless a text item already
//     carries the same JSON, which the spec asks servers to send for clients
//     that do not read the structured form (printing both would double the
//     tokens for nothing).

// maxResultChars bounds the text kept from one result before the standard
// truncation runs. Env.Finish stores the full text in the blob store, so this
// is also the bound on what one call can put there.
const maxResultChars = 4 << 20

// maxMediaBytes bounds one binary payload the harness will store. A server that
// returns a screenshot is normal; one that returns hundreds of megabytes per call
// is filling the blob store.
const maxMediaBytes = 8 << 20

// blobStore is the part of the harness blob store this package uses: it only
// ever writes. Declaring it here (tools.Env.Blobs satisfies it) keeps the
// package's dependencies to core, tools and perm, and lets tests supply a
// two-line fake.
type blobStore interface {
	Put(data []byte) (core.Hash, error)
}

// mediaRef describes a binary payload that was stored.
type mediaRef struct {
	Kind      string `json:"kind"` // image, audio, blob
	MediaType string `json:"media_type"`
	Bytes     int    `json:"bytes"`
	Ref       string `json:"ref,omitempty"` // blob store hash when stored
}

type rendered struct {
	text  string
	media []mediaRef
	// blocks are image blocks for the model, only when the harness opted in.
	blocks []core.Block
}

// renderResult converts a tool result to model-visible text.
func renderResult(res *CallToolResult, blobs blobStore, attach bool, redact *redactor) rendered {
	var out rendered
	var sb strings.Builder
	over := false
	add := func(s string) {
		if over || s == "" {
			return
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		if sb.Len()+len(s) > maxResultChars {
			cut := max(0, maxResultChars-sb.Len())
			// Back up to a rune boundary: the text was valid UTF-8 and the cut must
			// not be what makes it invalid.
			for cut > 0 && cut < len(s) && !utf8.RuneStart(s[cut]) {
				cut--
			}
			s = s[:cut]
			over = true
		}
		sb.WriteString(s)
	}
	store := func(kind, mediaType string, data []byte, bad bool) string {
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}
		if bad {
			return fmt.Sprintf("[%s omitted: %s, payload was not valid base64]", kind, mediaType)
		}
		ref := mediaRef{Kind: kind, MediaType: mediaType, Bytes: len(data)}
		where := "not stored"
		if len(data) > maxMediaBytes {
			out.media = append(out.media, ref)
			return fmt.Sprintf("[%s: %s, %s, not stored: over the %s limit; not shown]", kind, mediaType, humanBytes(len(data)), humanBytes(maxMediaBytes))
		}
		if blobs != nil {
			if h, err := blobs.Put(data); err == nil {
				ref.Ref = string(h)
				where = "stored as blob " + h.Short()
			}
		}
		out.media = append(out.media, ref)
		if attach && kind == "image" && ref.Ref != "" {
			out.blocks = append(out.blocks, core.Block{Kind: core.BlockImage, MediaType: mediaType, MediaRef: ref.Ref})
		}
		return fmt.Sprintf("[%s: %s, %s, %s; not shown]", kind, mediaType, humanBytes(len(data)), where)
	}

	textJSON := false
	for _, c := range res.Content {
		switch c.Type {
		case "text":
			add(c.Text)
			if !textJSON && len(res.StructuredContent) > 0 && sameJSON(c.Text, res.StructuredContent) {
				textJSON = true
			}
		case "image":
			add(store("image", c.MIMEType, c.Data, c.BadData))
		case "audio":
			add(store("audio", c.MIMEType, c.Data, c.BadData))
		case "resource_link":
			add(linkLine(c))
		case "resource":
			rc := c.Resource
			switch {
			case rc == nil:
				add("[resource omitted: no contents]")
			case rc.Text != "" || (len(rc.Blob) == 0 && !rc.BadData):
				add(fmt.Sprintf("[resource %s%s]", orUnknown(rc.URI), mimeSuffix(rc.MIMEType)))
				add(rc.Text)
			default:
				add(fmt.Sprintf("[resource %s%s]", orUnknown(rc.URI), mimeSuffix(rc.MIMEType)))
				add(store("blob", rc.MIMEType, rc.Blob, rc.BadData))
			}
		default:
			add(fmt.Sprintf("[content of type %q omitted: not supported]", clipForError(c.Type)))
		}
	}
	if len(res.StructuredContent) > 0 && !textJSON {
		if b, err := compactJSON(res.StructuredContent); err == nil {
			add("structuredContent: " + cleanText(string(b)))
		}
	}
	text := sb.String()
	if over {
		text += fmt.Sprintf("\n[server output cut at %s]", humanBytes(maxResultChars))
	}
	out.text = redact.apply(text)
	return out
}

func linkLine(c Content) string {
	var b strings.Builder
	b.WriteString("[resource link: ")
	switch {
	case c.Name != "":
		b.WriteString(c.Name + " ")
	case c.Title != "":
		b.WriteString(c.Title + " ")
	}
	b.WriteString(orUnknown(c.URI) + mimeSuffix(c.MIMEType))
	if c.Description != "" {
		d, _ := truncateRunes(strings.Join(strings.Fields(c.Description), " "), 200)
		b.WriteString(": " + d)
	}
	b.WriteString("]")
	return b.String()
}

func orUnknown(s string) string {
	if s == "" {
		return "(no uri)"
	}
	return s
}

func mimeSuffix(m string) string {
	if m == "" {
		return ""
	}
	return " (" + m + ")"
}

// sameJSON reports whether text is JSON equal to want, ignoring formatting.
func sameJSON(text string, want json.RawMessage) bool {
	text = strings.TrimSpace(text)
	if text == "" || text[0] != '{' {
		return false
	}
	a, err1 := core.Canonical(json.RawMessage(text))
	b, err2 := core.Canonical(want)
	return err1 == nil && err2 == nil && bytes.Equal(a, b)
}

func humanBytes(n int) string {
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d bytes", n)
	case n < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
}
