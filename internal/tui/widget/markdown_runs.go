package widget

import (
	"strings"

	"github.com/anemos-labs/sleipnir/internal/tui/cell"
)

// Flattening: the tokens of the inline parser become runs of text with flags. (The parser's overview is in markdown_inline.go.)

type mdLinkFrame struct {
	url  string
	from int // index in the runs where the link text starts
	img  bool
}

// mdRunBuf collects runs; text of the same flags is gathered in a builder, so a paragraph of many small tokens is linear.
type mdRunBuf struct {
	out   []mdRun
	cur   strings.Builder
	flags uint8
	open  bool
}

func (b *mdRunBuf) add(text string, f uint8) {
	if text == "" {
		return
	}
	if !b.open || b.flags != f {
		b.close()
		b.flags, b.open = f, true
	}
	b.cur.WriteString(text)
}

// close ends the run in progress, so that b.out holds every run so far.
func (b *mdRunBuf) close() {
	if b.open {
		b.out = append(b.out, mdRun{text: b.cur.String(), flags: b.flags})
		b.cur = strings.Builder{}
		b.open = false
	}
}

// runs turns the tokens into runs: emphasis as flags, links as styled text followed by a dim " (url)" when the address is not
// the text, images as "[alt]".
func (p *inlineParser) runs() []mdRun {
	var b mdRunBuf
	var italic, bold, strike, link int
	var frames []mdLinkFrame
	emit := func(text string, extra uint8) {
		f := extra
		if bold > 0 {
			f |= mdBold
		}
		if italic > 0 {
			f |= mdItalic
		}
		if strike > 0 {
			f |= mdStrike
		}
		if link > 0 {
			f |= mdLinkText
		}
		b.add(text, f)
	}
	apply := func(ch byte, use uint8, d int) {
		ptr := &italic
		switch {
		case ch == '~':
			ptr = &strike
		case use >= 2:
			ptr = &bold
		}
		*ptr = max(0, *ptr+d)
	}
	for k := range p.toks {
		t := &p.toks[k]
		switch t.kind {
		case tkText, tkOpen:
			emit(t.text, 0)
		case tkSoft:
			emit(" ", 0)
		case tkHard:
			b.close()
			b.out = append(b.out, mdRun{br: true})
		case tkCode:
			emit(t.text, mdCodeSpan)
		case tkAuto:
			if link > 0 {
				emit(t.text, 0)
			} else {
				link++
				emit(t.text, 0)
				link--
			}
		case tkDelim:
			for _, u := range t.closeUses {
				apply(t.ch, u, -1)
			}
			if t.n > 0 {
				emit(strings.Repeat(string(t.ch), t.n), 0)
			}
			for j := len(t.openUses) - 1; j >= 0; j-- {
				apply(t.ch, t.openUses[j], +1)
			}
		case tkLinkStart:
			b.close()
			frames = append(frames, mdLinkFrame{url: t.url, from: len(b.out), img: t.img})
			if !t.img {
				link++
			}
		case tkLinkEnd:
			if len(frames) == 0 {
				continue
			}
			b.close()
			fr := frames[len(frames)-1]
			frames = frames[:len(frames)-1]
			var plain strings.Builder
			for _, r := range b.out[fr.from:] {
				plain.WriteString(r.text)
			}
			text := plain.String()
			if fr.img {
				b.out = b.out[:fr.from]
				alt := strings.TrimSpace(text)
				if alt == "" {
					emit("[image]", mdImage)
					if fr.url != "" {
						emit(" ("+p.showURL(fr.url)+")", mdURLNote)
					}
				} else {
					emit("["+alt+"]", mdImage)
				}
				continue
			}
			link--
			switch {
			case strings.TrimSpace(text) == "" && fr.url != "":
				b.out = b.out[:fr.from]
				link++
				emit(p.showURL(fr.url), 0)
				link--
			case fr.url != "" && fr.url != text && fr.url != "mailto:"+text:
				emit(" ("+p.showURL(fr.url)+")", mdURLNote)
			}
		}
	}
	b.close()
	out := b.out
	for n := len(out); n > 0 && out[n-1].br; n = len(out) { // a hard break at the very end breaks nothing
		out = out[:n-1]
	}
	return out
}

// showURL shortens an address that is too long to be read to mdMaxURLShow cells.
func (p *inlineParser) showURL(u string) string {
	if cell.StringWidth(u) <= mdMaxURLShow {
		return u
	}
	return cell.Text(u).Truncate(mdMaxURLShow, p.ellipsis).Plain()
}
