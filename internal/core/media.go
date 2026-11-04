package core

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// MediaFunc returns the bytes of the image a block names by reference: kv keeps image bytes in a blob store and an image block
// holds only the blob's hash, so a wire format that sends images inline needs the bytes back. mediaType is the block's MIME type.
type MediaFunc func(ref, mediaType string) ([]byte, error)

// MaxInlineImageBytes bounds one image a wire format sends inline. A provider refuses a far larger one anyway, and a request
// that carries it is held in memory whole.
const MaxInlineImageBytes = 20 << 20

// ImageURL is the URL an image block is sent with on a wire format that takes images as URLs (OpenAI's chat completions and
// Responses API): a data URL or an http(s) URL as it stands, and a blob reference as a data URL of the bytes media returns. A
// blob reference with no media to resolve it is an error, never a URL: a hash sent as an image URL is refused by the endpoint,
// or worse, silently ignored, and the model answers about an image it never saw.
func ImageURL(b Block, media MediaFunc) (string, error) {
	ref := b.MediaRef
	switch {
	case ref == "":
		return "", errors.New("image block has no media reference")
	case strings.HasPrefix(ref, "data:"), strings.HasPrefix(ref, "https://"), strings.HasPrefix(ref, "http://"):
		return ref, nil
	case media == nil:
		return "", fmt.Errorf("image %q is a blob reference and nothing resolves blob references", clip(ref, 24))
	}
	mt := b.MediaType
	if mt == "" {
		return "", fmt.Errorf("image %q has no media type", clip(ref, 24))
	}
	if !strings.HasPrefix(mt, "image/") {
		return "", fmt.Errorf("image %q has media type %q, which is not an image", clip(ref, 24), clip(mt, 40))
	}
	data, err := media(ref, mt)
	if err != nil {
		return "", fmt.Errorf("resolving image %q: %w", clip(ref, 24), err)
	}
	if len(data) > MaxInlineImageBytes {
		return "", fmt.Errorf("image %q is %d bytes, more than the %d an image may be", clip(ref, 24), len(data), MaxInlineImageBytes)
	}
	return "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

// ImageNotSent is the text a wire format sends in place of an image it cannot send (ImageURL's error): the model learns that a
// picture was there and why it does not see it. Failing the request instead would fail every later request of the
// conversation too, since the image stays in its history, and a blob pruned from a resumed session would end it.
func ImageNotSent(err error) string {
	return "[an image is not shown: " + err.Error() + "]"
}

// Imaging is how a wire format sends images: Media resolves blob references, and TextOnly (a model that takes text only) sends
// every image as a note instead.
type Imaging struct {
	Media    MediaFunc
	TextOnly bool
}

// URL is ImageURL for this imaging: ErrTextOnly for every image of a model that takes text only.
func (im Imaging) URL(b Block) (string, error) {
	if im.TextOnly {
		return "", ErrTextOnly
	}
	return ImageURL(b, im.Media)
}

// ErrTextOnly is the refusal for a model that takes text only: the image is sent as a note (ImageNotSent) that says so.
var ErrTextOnly = errors.New("this model takes text only; ask a model that sees images to look at it, or read what produced it")

// ToolResultImages are the image blocks a tool result carries, in order.
func ToolResultImages(b Block) []Block {
	var out []Block
	for _, c := range b.Result {
		if c.Kind == BlockImage {
			out = append(out, c)
		}
	}
	return out
}

// clip shortens s to at most n bytes for an error message.
func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
