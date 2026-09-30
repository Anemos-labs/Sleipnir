package vt

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// The parser is the DEC ANSI state machine (vt100.net/emu/dec_ansi_parser) reduced to what is acted on: a byte is either
// text (ground state), part of an escape or CSI sequence, or part of a string (OSC, DCS, SOS, PM, APC) that is consumed.
// Every state can be left by CAN, SUB or ESC, so no input can trap it.

type pstate uint8

const (
	sGround    pstate = iota
	sEsc              // after ESC
	sEscInter         // ESC and an intermediate byte, waiting for the final byte (ESC ( B)
	sCSI              // after ESC [ : parameters, intermediates, then a final byte
	sCSIIgnore        // a malformed CSI: consumed up to its final byte
	sOSC              // an operating system command, ended by BEL or ST
	sStr              // DCS, SOS, PM or APC: consumed up to ST
	sStrEsc           // ESC inside a string: a backslash makes it ST, anything else ends the string and starts a sequence
)

const (
	maxParam  = 65535 // a larger number is clamped, so a count can never overflow
	maxParams = 32
	maxString = 4096 // bytes of an OSC kept for the record; the rest of a longer string is consumed and dropped
	maxInter  = 4
)

// param is one CSI parameter. v is -1 when it was left out (ESC [ ; 5 H), sub says ':' introduced it instead of ';'.
type param struct {
	v   int
	sub bool
}

type parser struct {
	state   pstate
	params  []param
	cur     int
	have    bool // a digit of the current parameter has been seen
	curSub  bool // the current parameter follows a ':'
	private byte // '<', '=', '>' or '?' leading the parameters
	inter   []byte
	str     []byte
	osc     bool // the string being read is an OSC (BEL ends it)
	utf     []byte
	c2      bool // the last string byte was 0xC2, the first half of a UTF-8 encoded C1 ST
}

func (t *Term) feed(b byte) {
	p := &t.p
	switch p.state {
	case sGround:
		t.ground(b)
	case sEsc:
		t.escape(b)
	case sEscInter:
		t.escInter(b)
	case sCSI:
		t.csiByte(b)
	case sCSIIgnore:
		t.csiIgnore(b)
	case sOSC, sStr:
		t.strByte(b)
	case sStrEsc:
		if b == '\\' {
			t.endString()
			return
		}
		t.endString() // the ESC ends the string, as in xterm, and is the start of the next sequence
		p.state = sEsc
		t.escape(b)
	}
}

func (t *Term) ground(b byte) {
	switch {
	case len(t.p.utf) > 0 || b >= 0x80:
		t.utf8Byte(b)
	case b < 0x20:
		t.control(b)
	case b == 0x7f:
		// DEL is ignored
	default:
		t.print(rune(b))
	}
}

// utf8Byte accumulates a multi-byte rune. A byte that cannot continue the rune ends it as U+FFFD, and is then read again as
// the start of whatever it is.
func (t *Term) utf8Byte(b byte) {
	p := &t.p
	p.utf = append(p.utf, b)
	if !utf8.FullRune(p.utf) {
		return
	}
	r, n := utf8.DecodeRune(p.utf)
	var rest []byte
	if n < len(p.utf) {
		rest = append(rest, p.utf[n:]...)
	}
	p.utf = p.utf[:0]
	switch {
	case r == utf8.RuneError && n == 1:
		t.print(utf8.RuneError)
	case r >= 0x80 && r <= 0x9f:
		t.c1(r)
	default:
		t.print(r)
	}
	for _, rb := range rest {
		t.feed(rb)
	}
}

// c1 acts on a C1 control that arrived as a rune (xterm in UTF-8 mode honours these, so a renderer that let one through would
// be caught here).
func (t *Term) c1(r rune) {
	switch r {
	case 0x84: // IND
		t.lineFeed()
	case 0x85: // NEL
		t.x, t.pending = 0, false
		t.lineFeed()
	case 0x8d: // RI
		t.reverseIndex()
	case 0x90, 0x98, 0x9e, 0x9f: // DCS, SOS, PM, APC
		t.startString(false)
	case 0x9b: // CSI
		t.startCSI()
	case 0x9d: // OSC
		t.startString(true)
	}
}

// control executes a C0 control. It is used by the ground state and, as the DEC parser does, inside escape and CSI sequences,
// where it does not end the sequence.
func (t *Term) control(b byte) {
	switch b {
	case 0x1b:
		t.p.state = sEsc
	case 0x08: // BS
		t.x = max(t.x-1, 0)
		t.pending = false
	case 0x09: // HT
		t.x = min((t.x/8+1)*8, t.cols-1)
		t.pending = false
	case 0x0a, 0x0b, 0x0c: // LF, VT, FF: a line feed does not return the carriage (no LNM, no ONLCR)
		t.lineFeed()
	case 0x0d: // CR
		t.x, t.pending = 0, false
	}
}

func (t *Term) escape(b byte) {
	p := &t.p
	switch {
	case b == 0x1b:
	case b == 0x18 || b == 0x1a:
		p.state = sGround
	case b < 0x20:
		t.control(b)
	case b >= 0x80:
		p.state = sGround
		t.ground(b)
	case b >= 0x20 && b <= 0x2f:
		p.inter = append(p.inter[:0], b)
		p.state = sEscInter
	case b == '[':
		t.startCSI()
	case b == ']':
		t.startString(true)
	case b == 'P' || b == 'X' || b == '^' || b == '_':
		t.startString(false)
	default:
		p.state = sGround
		switch b {
		case '7':
			t.saveCursor()
		case '8':
			t.restoreCursor()
		case 'c':
			t.Reset()
		case 'D':
			t.lineFeed()
		case 'E':
			t.x, t.pending = 0, false
			t.lineFeed()
		case 'M':
			t.reverseIndex()
		}
	}
}

func (t *Term) escInter(b byte) {
	p := &t.p
	switch {
	case b >= 0x20 && b <= 0x2f:
	case b == 0x1b:
		p.state = sEsc
	case b == 0x18 || b == 0x1a:
		p.state = sGround
	case b < 0x20:
		t.control(b)
	case b >= 0x80:
		p.state = sGround
		t.ground(b)
	default: // the final byte: a character set designation, DECALN and the like are not modelled
		p.state = sGround
	}
}

func (t *Term) startCSI() {
	p := &t.p
	p.state = sCSI
	p.params = p.params[:0]
	p.cur, p.have, p.curSub = 0, false, false
	p.private = 0
	p.inter = p.inter[:0]
}

func (p *parser) pushParam() {
	if len(p.params) < maxParams {
		v := -1
		if p.have {
			v = p.cur
		}
		p.params = append(p.params, param{v: v, sub: p.curSub})
	}
	p.cur, p.have, p.curSub = 0, false, false
}

func (t *Term) csiByte(b byte) {
	p := &t.p
	switch {
	case b >= '0' && b <= '9':
		if len(p.inter) > 0 {
			p.state = sCSIIgnore
			return
		}
		p.have = true
		p.cur = min(p.cur*10+int(b-'0'), maxParam)
	case b == ';' || b == ':':
		if len(p.inter) > 0 {
			p.state = sCSIIgnore
			return
		}
		p.pushParam()
		p.curSub = b == ':'
	case b >= 0x3c && b <= 0x3f: // < = > ?
		if len(p.params) == 0 && !p.have && p.private == 0 && len(p.inter) == 0 {
			p.private = b
		} else {
			p.state = sCSIIgnore
		}
	case b >= 0x20 && b <= 0x2f:
		if len(p.inter) < maxInter {
			p.inter = append(p.inter, b)
		}
	case b >= 0x40 && b <= 0x7e:
		if p.have || len(p.params) > 0 {
			p.pushParam()
		}
		p.state = sGround
		t.csi(b)
	case b == 0x1b:
		p.state = sEsc
	case b == 0x18 || b == 0x1a:
		p.state = sGround
	case b < 0x20:
		t.control(b)
	case b == 0x7f:
	default:
		p.state = sGround
		t.ground(b)
	}
}

func (t *Term) csiIgnore(b byte) {
	p := &t.p
	switch {
	case b >= 0x40 && b <= 0x7e:
		p.state = sGround
	case b == 0x1b:
		p.state = sEsc
	case b == 0x18 || b == 0x1a:
		p.state = sGround
	case b < 0x20:
		t.control(b)
	case b >= 0x80:
		p.state = sGround
		t.ground(b)
	}
}

func (t *Term) startString(osc bool) {
	p := &t.p
	p.state = sStr
	if osc {
		p.state = sOSC
	}
	p.osc = osc
	p.str = p.str[:0]
	p.c2 = false
}

func (t *Term) strByte(b byte) {
	p := &t.p
	switch {
	case b == 0x1b:
		p.state = sStrEsc
	case b == 0x18 || b == 0x1a: // CAN, SUB
		t.endString()
	case b == 0x07 && p.osc:
		t.endString()
	case b == 0x9c && p.c2: // the second half of U+009C, the C1 form of ST
		if n := len(p.str); n > 0 && p.str[n-1] == 0xc2 {
			p.str = p.str[:n-1]
		}
		t.endString()
	case b < 0x20:
		p.c2 = false
	default:
		if len(p.str) < maxString {
			p.str = append(p.str, b)
		}
		p.c2 = b == 0xc2
	}
}

// endString leaves a string state. An OSC is acted on when it ends, however it ends (xterm does the same for ESC and CAN),
// so that a test sees every command a terminal could have obeyed.
func (t *Term) endString() {
	p := &t.p
	if p.osc {
		t.osc(string(p.str))
	}
	p.state = sGround
	p.str = p.str[:0]
	p.c2 = false
}

// osc handles an operating system command. Only the two a renderer must never emit are noted; titles, colours, directories
// and the rest are consumed without effect.
func (t *Term) osc(s string) {
	num, _, _ := strings.Cut(s, ";")
	n, err := strconv.Atoi(num)
	if err != nil {
		return
	}
	if n == 52 || n == 8 {
		t.Rejected = append(t.Rejected, s)
	}
}
