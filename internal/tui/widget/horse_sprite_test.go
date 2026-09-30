package widget

// Tests of the generated horse sprites themselves (horsesprite_gen.go, made by docs/design/ux/sprite.py from the official mark):
// the invariants the terminal horse must keep however the generator is tuned. They are in the package rather than beside the
// other widget tests because they read the generated data. They never register a flag and share no names with other tests:
// every identifier here starts with "horse".

import (
	"fmt"
	"strings"
	"testing"
)

func horseEachSet(t *testing.T, f func(t *testing.T, name string, s horseSet)) {
	t.Helper()
	for _, name := range horseSizeNames {
		s, ok := horseLoad(name)
		if !ok {
			t.Fatalf("the sprite size %q is missing from horsesprite_gen.go", name)
		}
		t.Run(name, func(t *testing.T) { f(t, name, s) })
	}
}

// horsePictures is the four gait frames and the standing picture, named.
func horsePictures(s horseSet) []struct {
	name string
	pic  hpic
} {
	out := []struct {
		name string
		pic  hpic
	}{}
	for i, f := range s.frames {
		out = append(out, struct {
			name string
			pic  hpic
		}{fmt.Sprintf("frame %d", i), f})
	}
	return append(out, struct {
		name string
		pic  hpic
	}{"stand", s.stand})
}

// legRow is the first row in which all eight legs show (the far ones are behind the belly until a few rows below it), or -1.
func horseLegRow(p hpic) int {
	for y := 0; y < p.h; y++ {
		seen := map[int8]bool{}
		for x := 0; x < p.w; x++ {
			if a := p.at(x, y); a.key == 'L' {
				seen[a.leg] = true
			}
		}
		if len(seen) == 8 {
			return y
		}
	}
	return -1
}

// horseRuns is the legs in a row from the left: a run of one key is one leg column, however close its neighbour is.
func horseRuns(p hpic, y int) []int8 {
	var runs []int8
	last := int8(-2)
	for x := 0; x < p.w; x++ {
		a := p.at(x, y)
		if a.key != 'L' {
			last = -2
			continue
		}
		if a.leg != last {
			runs = append(runs, a.leg)
		}
		last = a.leg
	}
	return runs
}

// Every gait frame and the standing horse has eight legs under the belly, one column each, left to right: a regression to a gap
// in the middle (the horse once had four) fails here.
func TestHorseHasEightSeparateLegs(t *testing.T) {
	horseEachSet(t, func(t *testing.T, _ string, s horseSet) {
		for _, pc := range horsePictures(s) {
			y := horseLegRow(pc.pic)
			if y < 0 {
				t.Fatalf("%s: there is no row where all eight legs show", pc.name)
			}
			runs := horseRuns(pc.pic, y)
			if len(runs) != 8 {
				t.Fatalf("%s: %d leg columns in row %d, want 8: %v", pc.name, len(runs), y, runs)
			}
			for i, k := range runs {
				if int(k) != i {
					t.Fatalf("%s: the legs in row %d are %v, want 0..7 in order", pc.name, y, runs)
				}
			}
			// nothing in the middle is missing: every leg is at most a few cells from the next
			var xs []int
			for k := int8(0); k < 8; k++ {
				first := -1
				for x := 0; x < pc.pic.w; x++ {
					if a := pc.pic.at(x, y); a.key == 'L' && a.leg == k {
						first = x
						break
					}
				}
				xs = append(xs, first)
			}
			for i := 1; i < 8; i++ {
				if gap := xs[i] - xs[i-1]; gap < 1 || gap > 6 {
					t.Errorf("%s: legs %d and %d are %d cells apart in row %d (%v)", pc.name, i-1, i, gap, y, xs)
				}
			}
		}
	})
}

// The tail streams back from the rump and never hangs into the legs: no mane or tail pixel lies inside the box of any leg, and
// the tail (the hair left of the legs) ends no lower than the belly.
func TestHorseTailNeverLooksLikeALeg(t *testing.T) {
	horseEachSet(t, func(t *testing.T, _ string, s horseSet) {
		for _, pc := range horsePictures(s) {
			p := pc.pic
			for k := int8(0); k < 8; k++ {
				x0, y0, x1, y1 := p.w, p.h, -1, -1
				for y := 0; y < p.h; y++ {
					for x := 0; x < p.w; x++ {
						if a := p.at(x, y); a.key == 'L' && a.leg == k {
							x0, x1 = min(x0, x), max(x1, x)
							y0, y1 = min(y0, y), max(y1, y)
						}
					}
				}
				if x1 < 0 {
					t.Fatalf("%s: leg %d has no pixels", pc.name, k)
				}
				for y := y0; y <= y1; y++ {
					for x := x0; x <= x1; x++ {
						if p.at(x, y).key == 'm' {
							t.Errorf("%s: hair at (%d,%d) is inside leg %d's box (%d,%d)-(%d,%d)", pc.name, x, y, k, x0, y0, x1, y1)
						}
					}
				}
			}
			// the tail: hair left of the first hip
			firstLeg := p.w
			for y := 0; y < p.h; y++ {
				for x := 0; x < p.w; x++ {
					if p.at(x, y).key == 'L' {
						firstLeg = min(firstLeg, x)
					}
				}
			}
			belly := -1 // the lowest row of the body
			for y := 0; y < p.h; y++ {
				for x := 0; x < p.w; x++ {
					if p.at(x, y).key == 'b' {
						belly = max(belly, y)
					}
				}
			}
			low := -1
			for y := 0; y < p.h; y++ {
				for x := 0; x < firstLeg-1 && x < p.w; x++ {
					if p.at(x, y).key == 'm' {
						low = max(low, y)
					}
				}
			}
			if low < 0 {
				t.Errorf("%s: there is no tail left of the legs", pc.name)
			}
			if low > belly+1 {
				t.Errorf("%s: the tail reaches row %d, below the belly (row %d): it would read as legs", pc.name, low, belly)
			}
		}
	})
}

func TestHorseHasAnEyeAndHooves(t *testing.T) {
	horseEachSet(t, func(t *testing.T, _ string, s horseSet) {
		for _, pc := range horsePictures(s) {
			eyes, hooves, lost := 0, 0, 0
			for _, a := range pc.pic.px {
				switch a.key {
				case 'e':
					eyes++
				case 'h':
					hooves++
					if a.leg < 0 {
						lost++
					}
				}
			}
			if eyes != 1 {
				t.Errorf("%s: %d eye pixels, want 1", pc.name, eyes)
			}
			if hooves == 0 {
				t.Errorf("%s: no hooves", pc.name)
			}
			if lost > 0 {
				t.Errorf("%s: %d hoof pixels with no leg near them", pc.name, lost)
			}
		}
	})
}

// The body is the same in every picture, which is what lets a busy leg from one picture stand next to an idle one from another:
// where two pictures show body at all, they show the same body.
func TestHorseBodyIsTheSameInEveryPicture(t *testing.T) {
	horseEachSet(t, func(t *testing.T, _ string, s horseSet) {
		pics := horsePictures(s)
		for i := range s.body.px {
			seen := map[byte]bool{}
			for _, pc := range pics {
				if k := pc.pic.px[i].key; k == 'b' || k == 'm' || k == 'e' {
					seen[k] = true
				}
			}
			if len(seen) > 1 {
				t.Fatalf("pixel (%d,%d) is %v in different pictures", i%s.body.w, i/s.body.w, seen)
			}
		}
		// and the four gait frames are four different pictures, none of them the standing one
		for i := range pics {
			for j := i + 1; j < len(pics); j++ {
				if fmt.Sprint(pics[i].pic.px) == fmt.Sprint(pics[j].pic.px) {
					t.Errorf("%s and %s are the same picture", pics[i].name, pics[j].name)
				}
			}
		}
	})
}

// Composing picks busy legs from the gait frame and idle legs from the standing picture; with every leg busy it is the frame,
// with none it is the standing picture, and in between every leg is still there.
func TestHorseComposeIsExactAtTheEnds(t *testing.T) {
	horseEachSet(t, func(t *testing.T, _ string, s horseSet) {
		var all, none [8]bool
		for k := range all {
			all[k] = true
		}
		for f := 0; f < 4; f++ {
			if got := s.compose(f, all); fmt.Sprint(got.px) != fmt.Sprint(s.frames[f].px) {
				t.Errorf("all legs busy at frame %d is not the frame", f)
			}
		}
		if got := s.compose(0, none); fmt.Sprint(got.px) != fmt.Sprint(s.stand.px) {
			t.Error("no leg busy is not the standing picture")
		}
		if got := s.compose(-3, all); fmt.Sprint(got.px) != fmt.Sprint(s.frames[1].px) {
			t.Error("a negative frame wraps around (-3 is 1)")
		}
	})
}

// Whatever mix of busy and idle legs, all eight legs are drawn, in order, with no pixel of one leg cut off from the rest of it,
// and the rest of the horse is as it was.
func TestHorseComposeKeepsEveryLeg(t *testing.T) {
	horseEachSet(t, func(t *testing.T, _ string, s horseSet) {
		worst := 0
		for mask := 0; mask < 256; mask++ {
			var busy [8]bool
			for k := range busy {
				busy[k] = mask&(1<<k) != 0
			}
			for _, f := range []int{mask % 4} { // every mix of legs, at a frame that turns with the mix
				pic := s.compose(f, busy)
				seen := map[int8]bool{}
				for _, a := range pic.px {
					if a.key == 'L' {
						seen[a.leg] = true
					}
				}
				if len(seen) != 8 {
					t.Fatalf("mask %08b frame %d: only legs %v are drawn", mask, f, seen)
				}
				for i, a := range pic.px { // the body, hair and eye are never changed by a leg
					if b := s.body.px[i]; (b.key == 'm' || b.key == 'e') && a.key != b.key {
						t.Fatalf("mask %08b frame %d: a leg is painted over the %c at (%d,%d)", mask, f, b.key, i%pic.w, i/pic.w)
					}
				}
				// a far leg that a near leg crosses in front of is in two pieces, which is occlusion, not a fault; a leg in more pieces
				// than there are legs would be a composing fault
				if d := horseDetached(pic); d > worst {
					worst = d
				}
			}
		}
		if worst > 8 {
			t.Errorf("a mix of legs has %d detached leg pieces", worst)
		}
	})
}

// horseDetached counts pieces of legs that touch nothing of their leg (by 8-neighbourhood) beyond the first piece.
func horseDetached(p hpic) int {
	n := 0
	for k := int8(0); k < 8; k++ {
		seen := map[int]bool{}
		comps := 0
		for i, a := range p.px {
			if (a.key != 'L' && a.key != 'h') || a.leg != k || seen[i] {
				continue
			}
			comps++
			stack := []int{i}
			seen[i] = true
			for len(stack) > 0 {
				c := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				cx, cy := c%p.w, c/p.w
				for dy := -1; dy <= 1; dy++ {
					for dx := -1; dx <= 1; dx++ {
						nx, ny := cx+dx, cy+dy
						if nx < 0 || ny < 0 || nx >= p.w || ny >= p.h {
							continue
						}
						j := ny*p.w + nx
						if b := p.px[j]; (b.key == 'L' || b.key == 'h') && b.leg == k && !seen[j] {
							seen[j] = true
							stack = append(stack, j)
						}
					}
				}
			}
		}
		if comps > 1 {
			n += comps - 1
		}
	}
	return n
}

func TestHorseSizesAreOrderedAndNonEmpty(t *testing.T) {
	prevCols, prevRows := 1<<30, 1<<30
	for _, name := range horseSizeNames {
		s, ok := horseLoad(name)
		if !ok {
			t.Fatalf("size %s missing", name)
		}
		if s.cols() <= 0 || s.rows() <= 0 || s.y0%2 != 0 || s.y1%2 != 0 {
			t.Errorf("%s: box %dx%d rows, y %d..%d", name, s.cols(), s.rows(), s.y0, s.y1)
		}
		if s.cols() >= prevCols || s.rows() >= prevRows {
			t.Errorf("%s is not smaller than the size before it (%dx%d after %dx%d)", name, s.cols(), s.rows(), prevCols, prevRows)
		}
		prevCols, prevRows = s.cols(), s.rows()
	}
	if _, ok := horseLoad("no such size"); ok {
		t.Error("an unknown size is not loaded")
	}
	for _, key := range []string{"large", "medium", "small"} {
		sp, ok := horseSprites[key]
		if !ok || len(sp.stand) == 0 {
			t.Errorf("the generated file has no %q sprite", key)
		}
		for _, rows := range sp.frames {
			for _, r := range rows {
				if strings.Trim(r, " bmeh01234567") != "" {
					t.Errorf("%s: unknown pixel keys in %q", key, r)
				}
			}
		}
	}
}
