package repocheck

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The logo is shown through <img>, and an SVG shown that way is drawn with the fonts of whoever looks at it. The wordmark used to be
// live <text> in a viewBox 1500 wide: on a machine whose bold system font is wider than DejaVu's, the last letter of "Sleipnir" was cut
// off on GitHub (and the PNG made here was cut off as well). The words are outlines now (docs/media/outline_text.py), and these checks
// keep them that way: no text in a logo, and the outlines inside the picture with room to spare.
func TestTheLogoDoesNotDependOnTheReadersFonts(t *testing.T) {
	for _, f := range []string{
		"docs/media/logo.svg", "docs/media/logo-dark.svg", "docs/media/logo-wordmark.svg", "docs/media/logo-wordmark-dark.svg", "docs/media/favicon.svg",
	} {
		if strings.Contains(read(t, f), "<text") {
			t.Errorf("%s has a <text> element: drawn with the reader's fonts it is another width than here, and the end of it can fall outside the picture; make it outlines (docs/media/outline_text.py)", f)
		}
	}
}

func TestTheWordmarkIsInsideItsPicture(t *testing.T) {
	for _, f := range []string{"docs/media/logo-wordmark.svg", "docs/media/logo-wordmark-dark.svg"} {
		svg := read(t, f)
		vb := regexp.MustCompile(`viewBox='0 0 (\d+) (\d+)'`).FindStringSubmatch(svg)
		if vb == nil {
			t.Fatalf("%s: no viewBox of the form 0 0 W H", f)
		}
		w, _ := strconv.Atoi(vb[1])
		h, _ := strconv.Atoi(vb[2])
		for _, id := range []string{"name", "tagline"} {
			m := regexp.MustCompile(`<path id='` + id + `' transform='translate\(([\d.]+) ([\d.]+)\)'[^>]* d='([^']+)'`).FindStringSubmatch(svg)
			if m == nil {
				t.Fatalf("%s: no outlined %q (a path with that id)", f, id)
			}
			tx, _ := strconv.ParseFloat(m[1], 64)
			ty, _ := strconv.ParseFloat(m[2], 64)
			x0, x1, y0, y1 := pathBounds(m[3])
			if tx+x0 < 0 || tx+x1 > float64(w)-20 || ty+y0 < 0 || ty+y1 > float64(h) {
				t.Errorf("%s: %q spans x %.0f to %.0f and y %.0f to %.0f in a picture %d by %d, and wants 20 units of room at the right",
					f, id, tx+x0, tx+x1, ty+y0, ty+y1, w, h)
			}
		}
	}
}

// pathBounds is the box of the points of a path made of absolute M, L, H, V, Q, C and Z commands (what outline_text.py writes: the control
// points of a curve bound it, which is more than the curve and so errs on the side of room).
func pathBounds(d string) (x0, x1, y0, y1 float64) {
	first := true
	grow := func(x, y float64) {
		if first {
			x0, x1, y0, y1, first = x, x, y, y, false
			return
		}
		x0, x1, y0, y1 = min(x0, x), max(x1, x), min(y0, y), max(y1, y)
	}
	var cx, cy float64
	for _, c := range regexp.MustCompile(`([MLHVQCZ])([^MLHVQCZ]*)`).FindAllStringSubmatch(d, -1) {
		var n []float64
		for _, s := range regexp.MustCompile(`-?\d+\.?\d*`).FindAllString(c[2], -1) {
			v, _ := strconv.ParseFloat(s, 64)
			n = append(n, v)
		}
		switch c[1] {
		case "H":
			for _, v := range n {
				cx = v
				grow(cx, cy)
			}
		case "V":
			for _, v := range n {
				cy = v
				grow(cx, cy)
			}
		case "M", "L", "Q", "C":
			for i := 0; i+1 < len(n); i += 2 {
				cx, cy = n[i], n[i+1]
				grow(cx, cy)
			}
		}
	}
	return
}
