package widget_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/reee344/sleipnir/internal/tui/cell"
	"github.com/reee344/sleipnir/internal/tui/widget"
	"github.com/reee344/sleipnir/internal/tui/widget/showtest"
)

func TestMailFlowGolden(t *testing.T) {
	p := widget.DefaultPalette()
	var d showtest.Doc
	d.Note("MailFlow: ✉ from ➜ to “subject” and the cost; the newest message arrives over four frames after its Born")
	mails := showSketchMails() // the last one was born at frame 10
	for _, f := range []int{9, 10, 11, 12, 13, 14, 40} {
		d.Add(fmt.Sprintf("frame %d", f), widget.MailFlow(mails, f, 46, p), 46)
	}
	d.Note("other widths: the tokens go first, then the subject is cut")
	for _, w := range []int{36, 30, 24, 16, 10} {
		d.Add(fmt.Sprintf("width %d, settled", w), widget.MailFlow(mails, 40, w, p), w)
	}
	d.Note("the arrival at width 30: the subject is typed a third at a time")
	for _, f := range []int{10, 11, 12, 13} {
		d.Add(fmt.Sprintf("width 30, frame %d", f), widget.MailFlow(mails[3:], f, 30, p), 30)
	}
	showGolden(t, "mail", &d)
}

func TestMailFlowStyledGolden(t *testing.T) {
	p := showPal()
	var d showtest.Doc
	d.Note("the envelope is warn and Bold, names Bold, the arrow and the subject dim; while a message arrives its subject is Bold")
	mails := showSketchMails()
	d.AddText("settled", showtest.FlattenStyled(widget.MailFlow(mails[:2], 40, 40, p), showNames(p)))
	d.AddText("arriving (frame 12)", showtest.FlattenStyled(widget.MailFlow(mails[3:], 12, 40, p), showNames(p)))
	showGolden(t, "mail_styled", &d)
}

// Before the animation has run and after it, the same message: the arrival is only ever a function of the frame.
func TestMailFlowArrivalIsAFunctionOfTheFrame(t *testing.T) {
	p := widget.MonoPalette()
	mails := showSketchMails()
	prev := ""
	distinct := map[string]bool{}
	for f := 10; f <= 13; f++ {
		got := showtest.Flatten(widget.MailFlow(mails, f, 46, p))
		if got == prev {
			t.Errorf("frame %d looks like frame %d", f, f-1)
		}
		prev = got
		distinct[got] = true
		if again := showtest.Flatten(widget.MailFlow(mails, f, 46, p)); again != got {
			t.Fatalf("frame %d is not deterministic", f)
		}
	}
	if len(distinct) != 4 {
		t.Errorf("%d different pictures over 4 frames", len(distinct))
	}
	settled := showtest.Flatten(widget.MailFlow(mails, 13, 46, p))
	for _, f := range []int{14, 50, 1 << 20} {
		if showtest.Flatten(widget.MailFlow(mails, f, 46, p)) != settled {
			t.Errorf("frame %d: a settled message changed", f)
		}
	}
	// only the newest message is ever animated
	first := widget.MailFlow(mails, 10, 46, p)[0].Plain()
	if first != widget.MailFlow(mails, 13, 46, p)[0].Plain() {
		t.Error("an old message was animated")
	}
	// a message that has not arrived yet is at its first frame
	if got := widget.MailFlow(mails, 3, 46, p)[3].Plain(); !strings.HasPrefix(got, "✉ w2 ➜ w1") || strings.Contains(got, "“") {
		t.Errorf("before its frame: %q", got)
	}
	// a message born at a negative frame is not animated at all
	old := []widget.Mail{{From: "a", To: "b", Subject: "hello", Tokens: 5, Born: -1}}
	if got := widget.MailFlow(old, 0, 40, p)[0].Plain(); !strings.Contains(got, "“hello”") || !strings.Contains(got, "5 tok") {
		t.Errorf("an old message: %q", got)
	}
}

func TestMailFlowLayout(t *testing.T) {
	p := widget.MonoPalette()
	lines := widget.MailFlow(showSketchMails()[:3], 99, 46, p)
	if len(lines) != 3 {
		t.Fatalf("%d lines", len(lines))
	}
	for i, l := range lines {
		if l.Width() != 46 {
			t.Errorf("line %d is %d wide, the tokens are flush right at 46: %q", i, l.Width(), l.Plain())
		}
	}
	if !strings.HasPrefix(lines[0].Plain(), "✉ w1 ➜ w3 “schema: GET /orders”") || !strings.HasSuffix(lines[0].Plain(), "312 tok") {
		t.Errorf("%q", lines[0].Plain())
	}
	// the envelope is Bold even without colour
	if !lines[0][0].Style.Has(cell.Bold) {
		t.Errorf("the envelope is Bold: %+v", lines[0][0].Style)
	}
}

func TestMailFlowEdgeCases(t *testing.T) {
	p := widget.DefaultPalette()
	if widget.MailFlow(nil, 0, 40, p) != nil || widget.MailFlow(showSketchMails(), 0, 0, p) != nil || widget.MailFlow(showSketchMails(), 0, -3, p) != nil {
		t.Error("no mail or no width draws nothing")
	}
	evil := []widget.Mail{{From: "a\x1b]0;x\x07", To: "b\nc", Subject: "\x1b[2Jrm -rf\r\n/", Tokens: -5, Born: 0}}
	for f := 0; f < 6; f++ {
		got := widget.MailFlow(evil, f, 40, p)
		showNoControl(t, "mail", got)
		showNoWider(t, "mail", got, 40)
	}
	if got := showtest.Flatten(widget.MailFlow(evil, 9, 60, p)); !strings.Contains(got, "0 tok") {
		t.Errorf("negative tokens are 0: %q", got)
	}
	if got := widget.MailFlow([]widget.Mail{{}}, 0, 12, p); len(got) != 1 {
		t.Error("an empty message")
	}
}
