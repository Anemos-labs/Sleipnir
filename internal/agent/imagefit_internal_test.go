package agent

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// A screenshot wider than models read is scaled down, keeping its aspect and format; one within the limits is sent as is.
func TestFitImage(t *testing.T) {
	enc := func(w, h int) []byte {
		m := image.NewRGBA(image.Rect(0, 0, w, h))
		for x := 0; x < w; x++ {
			m.SetRGBA(x, h/2, color.RGBA{uint8(x), 0, 0, 255})
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, m); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	small := enc(100, 50)
	if got, mt := fitImage(small, "image/png"); !bytes.Equal(got, small) || mt != "image/png" {
		t.Error("a small image was changed")
	}
	got, mt := fitImage(enc(3136, 1000), "image/png")
	cfg, format, err := image.DecodeConfig(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	if mt != "image/png" || format != "png" || cfg.Width != maxImageSide || cfg.Height != 500 {
		t.Errorf("scaled to %dx%d %s (%s), want %dx500 png", cfg.Width, cfg.Height, format, mt, maxImageSide)
	}
	if got, _ := fitImage([]byte("RIFF....WEBPVP8 "), "image/webp"); string(got) != "RIFF....WEBPVP8 " {
		t.Error("an image the standard library cannot decode was changed")
	}
}
