// Mail between agents as a feed: who wrote to whom, what about, and how many tokens it cost (docs/UX.md, "Mail"; swarm.png).

package widget

import (
	"github.com/reee344/sleipnir/internal/tui/cell"
)

// Mail is one message between two agents.
type Mail struct {
	From, To string
	// Subject is what it says, in a few words.
	Subject string
	// Tokens is what the message costs the reader.
	Tokens int
	// Born is the frame at which the message arrived, for the animation of its arrival; a negative Born is an old message that is
	// not animated (the zero value is the frame 0).
	Born int
}

// mailFrames is how many frames a message takes to arrive.
const mailFrames = 4

// MailFlow draws the messages, one line each, in the order given:
//
//	✉ w1 ➜ w3 “schema: GET /orders”   312 tok
//	✉ m0 ➜ w5 “start e2e after t3”     96 tok
//
// The envelope is amber and Bold, the names bold, the arrow and the subject dim, and the tokens are at the right edge. The newest
// message (the last one) arrives over mailFrames frames after its Born: first only who writes to whom, then the subject appears
// a third at a time, then the cost, and meanwhile the envelope is Bold and the subject too; older messages are plain. So the
// animation is a function of frame: from Born+3 on the text is complete and from Born+4 on the message is settled, in the style of
// the others. A frame before Born shows only who writes to whom. Any integer is a frame.
//
// When the width is short the tokens go first, then the subject is cut with an ellipsis. No messages, or a width <= 0, draws
// nothing. Names and subjects that came from outside are cleaned of control characters.
func MailFlow(mails []Mail, frame int, width int, p Palette) []cell.Line {
	if width <= 0 || len(mails) == 0 {
		return nil
	}
	out := make([]cell.Line, 0, len(mails))
	for i, m := range mails {
		age := mailFrames // settled
		if i == len(mails)-1 && m.Born >= 0 {
			age = showClamp(frame-m.Born, 0, mailFrames)
		}
		out = append(out, mailLine(m, age, width, p))
	}
	return out
}

func mailLine(m Mail, age, width int, p Palette) cell.Line {
	arriving := age < mailFrames
	env := p.warnSt().With(cell.Bold)
	name := cell.Style{Attr: cell.Bold}
	var b showRowBuf
	b.add(env, "✉ ").add(name, showClean(m.From)).add(p.dimSt(), " ➜ ").add(name, showClean(m.To))

	subject := showClean(m.Subject)
	tokens := ""
	if age >= mailFrames-1 {
		tokens = showTokens(m.Tokens) + " tok"
	}
	if arriving { // the subject is typed out a third at a time
		runes := []rune(subject)
		subject = string(runes[:len(runes)*age/(mailFrames-1)])
		if age >= mailFrames-1 {
			subject = showClean(m.Subject)
		}
	}
	subSt := p.dimSt()
	if arriving {
		subSt = cell.Style{Attr: cell.Bold}
	}
	room := width - b.w
	if subject != "" {
		q := "“" + subject + "”"
		if tokens != "" && cell.StringWidth(q)+2+cell.StringWidth(tokens) > room-1 {
			tokens = "" // the tokens give way first
		}
		b.add(cell.Style{}, " ").add(subSt, showTrunc(q, room-1))
	}
	if tokens != "" {
		if pad := width - b.w - cell.StringWidth(tokens); pad >= 1 {
			b.space(pad).add(p.dimSt(), tokens)
		}
	}
	return showFit(b.line(), width)
}
