package agent

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // a GIF the read tool returned is scaled like any other image
	"image/jpeg"
	"image/png"
)

// Limits of an image sent to a model. Endpoints differ (one refuses an image over 5 MB of base64, another over 8,000 pixels
// a side, most bill by the pixel), so an image larger than these is scaled down before it is sent: maxImageSide is the
// long side models are trained to read at, maxImageSend the bytes an image may have after base64 under the strictest
// common limit. maxImageBytes is the most the blob store is asked to read for one image.
const (
	maxImageSide  = 1568
	maxImageSend  = 3_700_000
	maxImageBytes = 32 << 20
)

// fitImage returns an image no larger than the limits above, as PNG (for a PNG or GIF source, where text and edges must
// stay sharp) or JPEG. An image within the limits, and one the standard library cannot decode (WebP), is returned as it
// is.
func fitImage(data []byte, mediaType string) ([]byte, string) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return data, mediaType
	}
	long := max(cfg.Width, cfg.Height)
	if long <= maxImageSide && len(data) <= maxImageSend {
		return data, mediaType
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return data, mediaType
	}
	side := min(long, maxImageSide)
	for {
		w, h := cfg.Width*side/long, cfg.Height*side/long
		dst := scaleDown(src, max(w, 1), max(h, 1))
		var buf bytes.Buffer
		mt := "image/jpeg"
		if format == "png" || format == "gif" {
			mt = "image/png"
			err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, dst)
		} else {
			err = jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85})
		}
		if err != nil {
			return data, mediaType
		}
		if buf.Len() <= maxImageSend || side <= 256 {
			return buf.Bytes(), mt
		}
		side = side * 3 / 4
	}
}

// scaleDown resizes src to w×h by averaging the source pixels each destination pixel covers (a box filter), which keeps
// thin text legible where nearest-neighbour sampling would drop strokes.
func scaleDown(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	rgba, ok := src.(*image.RGBA)
	if !ok {
		rgba = image.NewRGBA(b)
		draw.Draw(rgba, b, src, b.Min, draw.Src)
	}
	sw, sh := b.Dx(), b.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		y0, y1 := y*sh/h, max((y+1)*sh/h, y*sh/h+1)
		for x := 0; x < w; x++ {
			x0, x1 := x*sw/w, max((x+1)*sw/w, x*sw/w+1)
			var r, g, bl, al, n uint64
			for sy := y0; sy < y1; sy++ {
				off := rgba.PixOffset(b.Min.X+x0, b.Min.Y+sy)
				for sx := x0; sx < x1; sx++ {
					p := rgba.Pix[off : off+4 : off+4]
					r, g, bl, al = r+uint64(p[0]), g+uint64(p[1]), bl+uint64(p[2]), al+uint64(p[3])
					n++
					off += 4
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n), uint8(g / n), uint8(bl / n), uint8(al / n)})
		}
	}
	return dst
}
