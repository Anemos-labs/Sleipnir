package core_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/anemos-labs/sleipnir/internal/core"
)

func TestImageURLResolvesBlobReferences(t *testing.T) {
	media := func(ref, mt string) ([]byte, error) {
		if ref != "abc123" || mt != "image/png" {
			t.Fatalf("asked for %q %q", ref, mt)
		}
		return []byte("png"), nil
	}
	img := core.Block{Kind: core.BlockImage, MediaType: "image/png", MediaRef: "abc123"}
	u, err := core.ImageURL(img, media)
	if err != nil || u != "data:image/png;base64,cG5n" {
		t.Fatalf("got %q %v", u, err)
	}
	for _, ref := range []string{"data:image/png;base64,AAAA", "https://example.com/a.png"} {
		if u, err := core.ImageURL(core.Block{Kind: core.BlockImage, MediaRef: ref}, nil); err != nil || u != ref {
			t.Fatalf("%s: got %q %v", ref, u, err)
		}
	}
}

func TestImageURLRefusesWhatCannotBeSent(t *testing.T) {
	cases := map[string]struct {
		b     core.Block
		media core.MediaFunc
		want  string
	}{
		"no reference":  {core.Block{Kind: core.BlockImage}, nil, "no media reference"},
		"no resolver":   {core.Block{Kind: core.BlockImage, MediaType: "image/png", MediaRef: "abc"}, nil, "nothing resolves"},
		"no media type": {core.Block{Kind: core.BlockImage, MediaRef: "abc"}, func(string, string) ([]byte, error) { return []byte("x"), nil }, "no media type"},
		"missing blob":  {core.Block{Kind: core.BlockImage, MediaType: "image/png", MediaRef: "abc"}, func(string, string) ([]byte, error) { return nil, errors.New("gone") }, "gone"},
		"not an image":  {core.Block{Kind: core.BlockImage, MediaType: "text/plain", MediaRef: "abc"}, func(string, string) ([]byte, error) { return []byte("x"), nil }, "not an image"},
		"too large": {core.Block{Kind: core.BlockImage, MediaType: "image/png", MediaRef: "abc"}, func(string, string) ([]byte, error) {
			return make([]byte, core.MaxInlineImageBytes+1), nil
		}, "more than"},
	}
	for name, c := range cases {
		if _, err := core.ImageURL(c.b, c.media); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err %v, want %q", name, err, c.want)
		}
	}
}

func TestImagingTextOnly(t *testing.T) {
	img := core.Block{Kind: core.BlockImage, MediaRef: "data:image/png;base64,AAAA"}
	if u, err := (core.Imaging{}).URL(img); err != nil || u != img.MediaRef {
		t.Fatalf("%q %v", u, err)
	}
	if _, err := (core.Imaging{TextOnly: true}).URL(img); err != core.ErrTextOnly {
		t.Fatalf("a text-only model was sent an image: %v", err)
	}
}
