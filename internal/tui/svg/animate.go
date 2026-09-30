package svg

import (
	"fmt"
	"strings"
	"time"
)

// Options tune Animated.
type Options struct {
	// Hold is how long the last frame stays before the animation starts again (default 3s).
	Hold time.Duration
	// Once plays the animation one time and stops on the last frame.
	Once bool
}

type visit struct{ start, end time.Duration }

// A version is one content a row (or the cursor) had, and when it is visible.
type version struct {
	row    []Cell // a row's cells
	cx, cy int    // the cursor's place
	off    bool   // the cursor was not shown
	visits []visit
}

// A track is the history of one row, or of the cursor: its versions in the order they first appeared.
type track struct {
	byKey    map[string]*version
	versions []*version
}

func newTrack() *track { return &track{byKey: map[string]*version{}} }

// see records that the version with this key is visible from start to end; mk makes it when it is new.
func (t *track) see(key string, mk func() *version, start, end time.Duration) {
	v, ok := t.byKey[key]
	if !ok {
		v = mk()
		t.byKey[key] = v
		t.versions = append(t.versions, v)
	}
	if n := len(v.visits); n > 0 && v.visits[n-1].end == start {
		v.visits[n-1].end = end
		return
	}
	v.visits = append(v.visits, visit{start, end})
}

// Animated draws the frames as one SVG that plays them. The frames must be the same size (the first one decides); their At
// times must not go backwards (a frame earlier than the one before it is placed at the same moment).
func Animated(frames []Frame, th Theme, o Options) string {
	th = th.fill()
	if len(frames) == 0 || len(frames[0].Rows) == 0 {
		return Static(Frame{}, th)
	}
	if o.Hold <= 0 {
		o.Hold = 3 * time.Second
	}
	rows, cols := len(frames[0].Rows), len(frames[0].Rows[0])
	at := make([]time.Duration, len(frames))
	for i, f := range frames {
		at[i] = f.At
		if i > 0 && at[i] < at[i-1] {
			at[i] = at[i-1]
		}
	}
	total := at[len(at)-1] + o.Hold

	rowTracks := make([]*track, rows)
	for y := range rowTracks {
		rowTracks[y] = newTrack()
	}
	cursor := newTrack()
	for i, f := range frames {
		end := total
		if i+1 < len(frames) {
			end = at[i+1]
		}
		if end <= at[i] {
			continue // a frame that is replaced at the same instant is never seen
		}
		for y := 0; y < rows; y++ {
			var row []Cell
			if y < len(f.Rows) {
				row = f.Rows[y]
			}
			rowTracks[y].see(rowSig(row), func() *version { return &version{row: row} }, at[i], end)
		}
		key, x, y := "off", 0, 0
		if f.CursorOn {
			x, y = f.CursorX, f.CursorY
			key = fmt.Sprintf("%d,%d", x, y)
		}
		cursor.see(key, func() *version { return &version{cx: x, cy: y, off: key == "off"} }, at[i], end)
	}

	// keyframes are shared by every version that is visible at the same moments
	var css strings.Builder
	names := map[string]string{}
	classOf := func(vs []visit) string {
		if len(vs) == 1 && vs[0].start == 0 && vs[0].end == total {
			return "" // always visible: no animation at all
		}
		var key strings.Builder
		var kf strings.Builder
		if vs[0].start == 0 {
			kf.WriteString("0%{opacity:1}")
		} else {
			kf.WriteString("0%{opacity:0}")
		}
		for _, v := range vs {
			s, e := pct(v.start, total), pct(v.end, total)
			if v.start > 0 {
				fmt.Fprintf(&kf, "%s%%{opacity:1}", s)
			}
			if v.end < total {
				fmt.Fprintf(&kf, "%s%%{opacity:0}", e)
			}
			key.WriteString(s + "-" + e + ",")
		}
		name, ok := names[key.String()]
		if !ok {
			name = fmt.Sprintf("k%d", len(names))
			names[key.String()] = name
			fmt.Fprintf(&css, "@keyframes %s{%s}.%s{animation-name:%s}", name, kf.String(), name, name)
		}
		return "a " + name
	}

	var body strings.Builder
	for y := 0; y < rows; y++ {
		for _, v := range rowTracks[y].versions {
			if !rowVisible(v.row) {
				continue
			}
			drawRow(&body, th, v.row, y, classOf(v.visits))
		}
	}
	for _, v := range cursor.versions {
		if v.off {
			continue
		}
		var cb strings.Builder
		drawCursor(&cb, th, Frame{Rows: make([][]Cell, rows), CursorX: v.cx, CursorY: v.cy, CursorOn: true})
		s := strings.TrimRight(cb.String(), "\n")
		if class := classOf(v.visits); class != "" {
			s = `<g class="` + class + `">` + s + `</g>`
		}
		body.WriteString(s + "\n")
	}

	w, h, top := th.size(cols, rows)
	var b strings.Builder
	header(&b, th, w, h, top)
	iter, fill := "infinite", ""
	if o.Once {
		iter, fill = "1", "animation-fill-mode:forwards;"
	}
	fmt.Fprintf(&b, `<style>%s.a{opacity:0;animation-duration:%ss;animation-iteration-count:%s;animation-timing-function:step-end;%s}%s</style>`+"\n",
		commonCSS(th), num(total.Seconds()), iter, fill, css.String())
	chrome(&b, th, w, h, top)
	fmt.Fprintf(&b, `<g transform="translate(%s %s)">`+"\n", num(th.Padding), num(th.Padding+top))
	b.WriteString(body.String())
	b.WriteString("</g>\n</svg>\n")
	return b.String()
}

// pct is t as a percentage of total, with enough digits to place a frame to within a few milliseconds of a minute.
func pct(t, total time.Duration) string {
	if total <= 0 {
		return "0"
	}
	s := fmt.Sprintf("%.4f", float64(t)/float64(total)*100)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" {
		return "0"
	}
	return s
}

// rowSig is a row's content as a string, so equal rows are found cheaply.
func rowSig(row []Cell) string {
	var b strings.Builder
	for _, c := range row {
		b.WriteString(c.Text)
		s := c.Style
		fmt.Fprintf(&b, "\x1f%d.%d.%d.%d.%d:%d.%d.%d.%d.%d:%d\x1e", s.FG.Kind, s.FG.N, s.FG.R, s.FG.G, s.FG.B,
			s.BG.Kind, s.BG.N, s.BG.R, s.BG.G, s.BG.B, s.Attr)
	}
	return b.String()
}
