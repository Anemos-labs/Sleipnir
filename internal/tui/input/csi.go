package input

import "unicode"

// A CSI sequence is ESC [ parameter bytes (0x30-0x3f) intermediate bytes (0x20-0x2f) and a final byte (0x40-0x7e). An SS3
// sequence is ESC O and a final byte, with an optional modifier in front of it on some terminals.

// stepCSI decodes the CSI sequence at the start of b (b[0] is ESC, b[1] is '['). alt is Alt when an extra ESC preceded the
// sequence (older xterm style: ESC ESC [ A is Alt+Up). It returns the bytes used; 0 with the mode unchanged means b is
// incomplete.
func (d *Decoder) stepCSI(b []byte, out []Key, final bool, alt Mod) (int, []Key) {
	j := 2
	for j < len(b) {
		c := b[j]
		if c >= 0x40 && c <= 0x7e {
			break
		}
		if c < 0x20 || c > 0x3f { // a control, DEL or non-ASCII byte cannot be part of a CSI sequence
			if j == 2 { // ESC [ and then an ordinary key: Alt+[ followed by that key
				return 2, append(out, RuneKey('[', Alt|alt))
			}
			return j, out // a damaged sequence: drop it, read the byte again as a key
		}
		j++
		if j-2 > maxCSI {
			d.mode = modeSkipCSI
			return j, out
		}
	}
	if j >= len(b) {
		switch {
		case !final:
			return 0, out
		case j == 2:
			return 2, append(out, RuneKey('[', Alt|alt))
		}
		return j, out // cut off in the middle of a sequence: nothing to report
	}
	params, fin, n := b[2:j], b[j], j+1
	switch {
	case fin == 'M' && len(params) == 0: // legacy mouse report: three raw bytes follow
		if len(b) < n+3 {
			if !final {
				return 0, out
			}
			return len(b), out
		}
		return n + 3, out
	case fin == '[' && len(params) == 0: // Linux console: ESC [ [ A is F1
		if len(b) < n+1 {
			if !final {
				return 0, out
			}
			return n, out
		}
		if c := b[n]; c >= 'A' && c <= 'E' {
			return n + 1, append(out, SpecialKey(F1+Special(c-'A'), alt))
		}
		return n, out
	}
	k, act := csiKey(params, fin)
	switch act {
	case actKey:
		k.Mod |= alt
		out = append(out, k)
	case actPasteStart:
		d.mode = modePaste
		d.paste, d.pasteTrunc = d.paste[:0], false
	}
	return n, out
}

// stepSS3 decodes the SS3 sequence at the start of b (b[0] is ESC, b[1] is 'O').
func (d *Decoder) stepSS3(b []byte, out []Key, final bool, alt Mod) (int, []Key) {
	j := 2
	for j < len(b) && (b[j] >= '0' && b[j] <= '9' || b[j] == ';') {
		j++
		if j-2 > maxCSI {
			return j, out
		}
	}
	if j >= len(b) {
		switch {
		case !final:
			return 0, out
		case j == 2:
			return 2, append(out, RuneKey('O', Alt|alt))
		}
		return j, out
	}
	if c := b[j]; c < 0x40 || c > 0x7e {
		if j == 2 { // ESC O and then an ordinary key: Alt+O followed by that key
			return 2, append(out, RuneKey('O', Alt|alt))
		}
		return j, out
	}
	k, ok := ss3Key(b[2:j], b[j])
	if ok {
		k.Mod |= alt
		out = append(out, k)
	}
	return j + 1, out
}

func ss3Key(params []byte, fin byte) (Key, bool) {
	code, ok := finalKey(fin)
	if !ok {
		return Key{}, false
	}
	f := parseParams(params)
	m := Mod(0)
	switch len(f) {
	case 1: // xterm's older ESC O 5 A
		m, ok = modOf(f[0][0])
	case 2: // ESC O 1;5 A
		m, ok = modOf(f[1][0])
	}
	if !ok {
		return Key{}, false
	}
	return SpecialKey(code, m), true
}

// finalKey is the key a cursor-style final byte names, shared by CSI and SS3.
func finalKey(fin byte) (Special, bool) {
	switch fin {
	case 'A':
		return Up, true
	case 'B':
		return Down, true
	case 'C':
		return Right, true
	case 'D':
		return Left, true
	case 'H':
		return Home, true
	case 'F':
		return End, true
	case 'P':
		return F1, true
	case 'Q':
		return F2, true
	case 'R':
		return F3, true
	case 'S':
		return F4, true
	}
	return NoSpecial, false
}

type csiAction uint8

const (
	actIgnore csiAction = iota
	actKey
	actPasteStart
)

// csiKey interprets a complete CSI sequence.
func csiKey(params []byte, fin byte) (Key, csiAction) {
	if len(params) > 0 && params[0] >= 0x3c { // private parameters (< = > ?): mouse, device and mode reports
		return Key{}, actIgnore
	}
	for _, c := range params {
		if c < 0x30 { // an intermediate byte (CSI Ps SP q and the like): not a key
			return Key{}, actIgnore
		}
	}
	f := parseParams(params)
	arg := func(i, sub, def int) int {
		if i < len(f) && sub < len(f[i]) && f[i][sub] >= 0 {
			return f[i][sub]
		}
		return def
	}
	switch fin {
	case 'A', 'B', 'C', 'D', 'H', 'F', 'P', 'Q', 'S':
		return cursorKey(fin, arg(1, 0, 1))
	case 'R':
		// CSI 1;5 R is Ctrl+F3; CSI row;col R is a cursor position report, and is not a key. xterm sends a modifier of 2 or
		// more, so "1;1R" is the report for the top left corner.
		if len(f) > 2 || arg(0, 0, 1) != 1 || (len(f) == 2 && arg(1, 0, 1) < 2) {
			return Key{}, actIgnore
		}
		return cursorKey(fin, arg(1, 0, 1))
	case 'Z':
		m, ok := modOf(arg(1, 0, 1))
		if !ok {
			return Key{}, actIgnore
		}
		return SpecialKey(Tab, m|Shift), actKey
	case '~':
		return tildeKey(arg(0, 0, 0), arg(1, 0, 1), arg(2, 0, 0), len(f))
	case 'u': // CSI code [: shifted : base] ; mods [: event] u (fixterms, kitty)
		if arg(1, 1, 1) == 3 {
			return Key{}, actIgnore // a key release
		}
		return codepointKey(arg(0, 0, 0), arg(1, 0, 1))
	}
	return Key{}, actIgnore // focus in and out (CSI I, CSI O) and everything else we do not know
}

func cursorKey(fin byte, mod int) (Key, csiAction) {
	code, _ := finalKey(fin)
	m, ok := modOf(mod)
	if !ok {
		return Key{}, actIgnore
	}
	return SpecialKey(code, m), actKey
}

// tildeKey is CSI n ~ (and CSI 27;mod;code ~ for modifyOtherKeys).
func tildeKey(n, mod, code, nfields int) (Key, csiAction) {
	var c Special
	switch n {
	case 1, 7:
		c = Home
	case 2:
		c = Insert
	case 3:
		c = Delete
	case 4, 8:
		c = End
	case 5:
		c = PgUp
	case 6:
		c = PgDn
	case 11, 12, 13, 14, 15:
		c = F1 + Special(n-11)
	case 17, 18, 19, 20, 21:
		c = F6 + Special(n-17)
	case 23, 24:
		c = F11 + Special(n-23)
	case 200:
		return Key{}, actPasteStart
	case 27: // xterm modifyOtherKeys
		if nfields < 3 {
			return Key{}, actIgnore
		}
		return codepointKey(code, mod)
	default:
		return Key{}, actIgnore // 201 outside a paste, F13 and up, and the unknown
	}
	m, ok := modOf(mod)
	if !ok {
		return Key{}, actIgnore
	}
	return SpecialKey(c, m), actKey
}

// codepointKey is the key for a Unicode code point and an xterm modifier parameter (CSI u and modifyOtherKeys).
func codepointKey(cp, mod int) (Key, csiAction) {
	m, ok := modOf(mod)
	if !ok {
		return Key{}, actIgnore
	}
	switch {
	case cp == 13:
		return SpecialKey(Enter, m), actKey
	case cp == 9:
		return SpecialKey(Tab, m), actKey
	case cp == 27:
		return SpecialKey(Esc, m), actKey
	case cp == 127 || cp == 8:
		return SpecialKey(Backspace, m), actKey
	case cp == ' ':
		return RuneKey(' ', m), actKey
	case cp < 0x20 || cp > unicode.MaxRune || (cp >= 0xd800 && cp < 0xe000) || (cp >= 0xe000 && cp <= 0xf8ff) || (cp >= 0x7f && cp <= 0x9f):
		return Key{}, actIgnore // a control, a surrogate, or kitty's private-use code points for keys we do not name
	}
	r := rune(cp)
	if m&Shift != 0 && m&Ctrl == 0 && unicode.IsLetter(r) {
		r, m = unicode.ToUpper(r), m&^Shift // the same key legacy input reports as ESC A: the rune carries the shift
	}
	return RuneKey(r, m), actKey
}

// modOf turns an xterm modifier parameter (1 plus a bit set: 1 Shift, 2 Alt, 4 Ctrl) into a Mod. Super, Hyper and Meta (8,
// 16, 32) are not ours: the key is dropped, as it would be if the terminal had not reported it at all. Caps and Num lock
// bits (64, 128) are ignored.
func modOf(p int) (Mod, bool) {
	if p < 1 {
		p = 1
	}
	bits := p - 1
	if bits&(8|16|32) != 0 {
		return 0, false
	}
	return Mod(bits & 7), true
}

// parseParams splits CSI parameter bytes into fields at ';' and subfields at ':'. An empty number is -1, so the caller can
// tell "absent" from zero. There are at most 16 fields of 8 subfields, and numbers are clamped: a hostile sequence cannot
// make this allocate much or overflow.
func parseParams(p []byte) [][]int {
	if len(p) == 0 {
		return nil
	}
	var fields [][]int
	cur := []int{-1}
	flush := func() {
		if len(fields) < 16 {
			fields = append(fields, cur)
		}
	}
	for _, c := range p {
		switch {
		case c >= '0' && c <= '9':
			i := len(cur) - 1
			if cur[i] < 0 {
				cur[i] = 0
			}
			if cur[i] < 1<<24 {
				cur[i] = cur[i]*10 + int(c-'0')
			}
		case c == ';':
			flush()
			cur = []int{-1}
		case c == ':':
			if len(cur) < 8 {
				cur = append(cur, -1)
			}
		}
	}
	flush()
	return fields
}
