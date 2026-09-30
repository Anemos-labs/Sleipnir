package shellparse

import (
	"strconv"
	"strings"
)

// hasBraceCandidate reports whether w holds an unquoted '{' at all, the cheap
// test before attempting a real brace expansion.
func hasBraceCandidate(w *wbuf) bool {
	for i, c := range w.v {
		if c == '{' && w.m[i] == 0 {
			return true
		}
	}
	return false
}

// Bounds on brace expansion: long words and deeply nested expressions are not
// expanded (the analysis becomes unparsed instead).
const (
	maxBraceWordLen = 4096
	maxBraceDepth   = 32
)

// braceExpand performs bash-style brace expansion on the word (v, m) where m
// marks quoted bytes. Brace expansion is purely lexical, so doing it here lets
// callers see every word the shell would run: ~/.{ssh,aws}/x is two paths.
// over reports that the expansion exceeded a bound (maxBraceWords outputs,
// nesting maxBraceDepth, a word longer than maxBraceWordLen); the unexpanded
// word is returned in that case.
func braceExpand(v, m []byte) (words []string, over bool) {
	if len(v) > maxBraceWordLen {
		return []string{string(v)}, true
	}
	budget := maxBraceWords
	blown := false
	var rec func(v, m []byte, depth int) []string
	rec = func(v, m []byte, depth int) []string {
		if blown {
			return nil
		}
		if depth > maxBraceDepth {
			blown = true
			return nil
		}
		match, commas := braceStructure(v, m)
		for i := 0; i < len(v); i++ {
			closeIdx := match[i]
			if v[i] != '{' || m[i] != 0 || closeIdx < 0 {
				continue
			}
			preV, preM := v[:i], m[:i]
			postV, postM := v[closeIdx+1:], m[closeIdx+1:]
			if cs := commas[i]; len(cs) > 0 {
				var out []string
				prev := i + 1
				for _, ci := range append(append([]int(nil), cs...), closeIdx) {
					av, am := cat3(preV, preM, v[prev:ci], m[prev:ci], postV, postM)
					out = append(out, rec(av, am, depth+1)...)
					prev = ci + 1
				}
				return out
			}
			if body := v[i+1 : closeIdx]; allUnquoted(m[i+1 : closeIdx]) {
				if elems, ok, tooBig := braceSeq(string(body)); tooBig {
					blown = true
					return nil
				} else if ok {
					var out []string
					for _, e := range elems {
						ev := []byte(e)
						av, am := cat3(preV, preM, ev, make([]byte, len(ev)), postV, postM)
						out = append(out, rec(av, am, depth+1)...)
					}
					return out
				}
			}
		}
		if budget--; budget < 0 {
			blown = true
			return nil
		}
		return []string{string(v)}
	}
	out := rec(v, m, 0)
	if blown || len(out) == 0 {
		return []string{string(v)}, blown
	}
	return out, false
}

// braceStructure finds, in one pass, the matching close for every unquoted '{'
// (-1 when it has none) and the top-level commas between them. Doing it once
// per expansion step keeps unbalanced input such as 100000 open braces linear.
func braceStructure(v, m []byte) (match []int, commas map[int][]int) {
	match = make([]int, len(v))
	for i := range match {
		match[i] = -1
	}
	commas = map[int][]int{}
	var stack []int
	for k := 0; k < len(v); k++ {
		if m[k] != 0 {
			continue
		}
		switch v[k] {
		case '{':
			stack = append(stack, k)
		case '}':
			if n := len(stack); n > 0 {
				match[stack[n-1]] = k
				stack = stack[:n-1]
			}
		case ',':
			if n := len(stack); n > 0 {
				commas[stack[n-1]] = append(commas[stack[n-1]], k)
			}
		}
	}
	return match, commas
}

func allUnquoted(m []byte) bool {
	for _, c := range m {
		if c != 0 {
			return false
		}
	}
	return true
}

func cat3(av, am, bv, bm, cv, cm []byte) (v, m []byte) {
	v = make([]byte, 0, len(av)+len(bv)+len(cv))
	m = make([]byte, 0, len(v))
	v = append(append(append(v, av...), bv...), cv...)
	m = append(append(append(m, am...), bm...), cm...)
	return v, m
}

// braceSeq expands the {x..y} and {x..y..step} forms for integers and single
// ASCII letters. ok is false when body is not a sequence (it is then literal).
func braceSeq(body string) (elems []string, ok bool, tooBig bool) {
	parts := strings.Split(body, "..")
	if len(parts) != 2 && len(parts) != 3 {
		return nil, false, false
	}
	step := 1
	if len(parts) == 3 {
		s, err := strconv.Atoi(parts[2])
		if err != nil || s == 0 {
			return nil, false, false
		}
		if s < 0 {
			s = -s
		}
		step = s
	}
	if xi, err1 := strconv.Atoi(parts[0]); err1 == nil {
		yi, err2 := strconv.Atoi(parts[1])
		if err2 != nil {
			return nil, false, false
		}
		const lim = 1 << 40 // keeps yi-xi from overflowing
		if xi > lim || xi < -lim || yi > lim || yi < -lim {
			return nil, false, true
		}
		width := 0
		if padded(parts[0]) || padded(parts[1]) {
			width = max(len(parts[0]), len(parts[1]))
		}
		n := abs(yi-xi)/step + 1
		if n > maxBraceWords {
			return nil, false, true
		}
		dir := 1
		if yi < xi {
			dir = -1
		}
		for i, v := 0, xi; i < n; i, v = i+1, v+dir*step {
			s := strconv.Itoa(v)
			if width > len(s) {
				neg := strings.HasPrefix(s, "-")
				digits := strings.TrimPrefix(s, "-")
				digits = strings.Repeat("0", width-len(s)) + digits
				if neg {
					digits = "-" + digits
				}
				s = digits
			}
			elems = append(elems, s)
		}
		return elems, true, false
	}
	if len(parts[0]) == 1 && len(parts[1]) == 1 && isAlpha(parts[0][0]) && isAlpha(parts[1][0]) {
		x, y := int(parts[0][0]), int(parts[1][0])
		n := abs(y-x)/step + 1
		if n > maxBraceWords {
			return nil, false, true
		}
		dir := 1
		if y < x {
			dir = -1
		}
		for i, c := 0, x; i < n; i, c = i+1, c+dir*step {
			elems = append(elems, string(rune(c)))
		}
		return elems, true, false
	}
	return nil, false, false
}

func padded(s string) bool {
	s = strings.TrimPrefix(s, "-")
	return len(s) > 1 && s[0] == '0'
}

func isAlpha(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
