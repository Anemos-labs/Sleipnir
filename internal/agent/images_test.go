package agent_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anemos-labs/sleipnir/internal/agent"
	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/provider"
	"github.com/anemos-labs/sleipnir/internal/tools"
)

// imgPNG is a small PNG whose colour makes each picture of a test distinct.
func imgPNG(t *testing.T, w, h int, c color.RGBA) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			m.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, m); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// imgReq is what the provider saw of one request: whether it asked for a description, its images and its text.
type imgReq struct {
	describe bool
	images   []string // media references as sent
	text     string
}

// imgProvider answers description requests with a description naming the picture's reference, and conversation requests
// with the reply its script gives; it records every request.
type imgProvider struct {
	mu     sync.Mutex
	reqs   []imgReq
	reply  func(n int, r imgReq) (core.Turn, *provider.Error)
	mainN  int
	refuse bool // refuse a conversation request that carries any image
	hang   bool // never answer a conversation request that carries any image
}

func (p *imgProvider) Profile() provider.Profile {
	return provider.Profile{Name: "img", Dialect: "openai-chat", Cache: cost.OpenAICacheModel()}
}

func (p *imgProvider) Do(ctx context.Context, req *provider.Request, on func(provider.Event)) (*provider.Response, error) {
	if on != nil {
		on(provider.Event{Kind: provider.EvStart})
	}
	var r imgReq
	for _, s := range req.Prompt.System {
		if strings.Contains(s.Text, "You describe one image") {
			r.describe = true
		}
	}
	var sb strings.Builder
	for _, m := range req.Prompt.Messages {
		for _, b := range m.Blocks {
			switch b.Kind {
			case core.BlockImage:
				r.images = append(r.images, b.MediaRef)
			case core.BlockText:
				sb.WriteString(b.Text + "\n")
			case core.BlockToolResult:
				for _, c := range b.Result {
					if c.Kind == core.BlockImage {
						r.images = append(r.images, c.MediaRef)
					} else {
						sb.WriteString(c.Text + "\n")
					}
				}
			}
		}
	}
	r.text = sb.String()
	p.mu.Lock()
	p.reqs = append(p.reqs, r)
	id := len(p.reqs)
	n := p.mainN
	if !r.describe {
		p.mainN++
	}
	refuse, hang := p.refuse, p.hang
	p.mu.Unlock()
	if hang && !r.describe && len(r.images) > 0 {
		<-ctx.Done()
		return nil, &provider.Error{Kind: provider.ErrNetwork, Message: "cancelled", Err: ctx.Err()}
	}
	var turn core.Turn
	switch {
	case r.describe:
		turn = arvText(fmt.Sprintf("DESCRIPTION of picture %d: a test card", len(r.images[0])%1000))
		if len(r.images) == 1 {
			// Name the picture by the end of its data URL, which differs per colour.
			turn = arvText("DESCRIPTION " + r.images[0][len(r.images[0])-24:])
		}
	case refuse && len(r.images) > 0:
		return nil, &provider.Error{Kind: provider.ErrBadRequest, Status: 400, Message: "this model does not support image input"}
	default:
		var pe *provider.Error
		turn, pe = p.reply(n, r)
		if pe != nil {
			return nil, pe
		}
	}
	stop := core.StopEnd
	if len(turn.ToolCalls()) > 0 {
		stop = core.StopToolUse
	}
	turn.Role = core.RoleAssistant
	return &provider.Response{ID: fmt.Sprintf("img-%d", id), Model: "img-1", Turn: turn, Stop: stop}, nil
}

// main returns the conversation requests, describe the description requests.
func (p *imgProvider) split() (main, describe []imgReq) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range p.reqs {
		if r.describe {
			describe = append(describe, r)
		} else {
			main = append(main, r)
		}
	}
	return main, describe
}

// newImageAgent builds an agent on p with a tool "shot" that returns a new picture each call.
func newImageAgent(t *testing.T, p provider.Provider, pol agent.ImagePolicy) (*agent.Agent, events.Blobs) {
	t.Helper()
	agent.RetryBase = 0
	blobs := events.NewMemBlobs()
	reg := tools.NewRegistry()
	shots := 0
	reg.Register(arvFake{name: "shot", run: func() *tools.Result {
		shots++
		h, err := blobs.Put(imgPNG(t, 8, 8, color.RGBA{uint8(40 * shots), 10, 200, 255}))
		if err != nil {
			t.Fatal(err)
		}
		return &tools.Result{Text: "screenshot taken", Blocks: []core.Block{{Kind: core.BlockImage, MediaType: "image/png", MediaRef: string(h)}}}
	}})
	specs, err := reg.Specs()
	if err != nil {
		t.Fatal(err)
	}
	a, err := agent.New(agent.Config{
		ID: "main", Role: "solo", Model: cost.Model{ID: "img-1", ContextTokens: 1_000_000}, Provider: p, Tools: reg, ToolSpecs: specs,
		Const:  kv.NewLayer("const", kv.KindConst, 1, []kv.Segment{{Text: "You are a careful coding agent."}}),
		Params: core.Params{MaxTokens: 512}, Events: events.NewMemLog(), Blobs: blobs, Images: pol, MaxSteps: 20, NoCompaction: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a, blobs
}

func dataURLs(refs []string) bool {
	for _, r := range refs {
		if !strings.HasPrefix(r, "data:image/png;base64,") {
			return false
		}
	}
	return true
}

// A message with three pictures, on a route that carries one, reaches the model as one request with the last picture and
// the descriptions of the other two, each written in a request of its own; the next message carries none, only the three
// descriptions.
func TestImagesSplitAcrossDescriptions(t *testing.T) {
	p := &imgProvider{reply: func(int, imgReq) (core.Turn, *provider.Error) { return arvText("seen"), nil }}
	a, blobs := newImageAgent(t, p, agent.ImagePolicy{})
	var toks []string
	for i := 0; i < 3; i++ {
		h, err := blobs.Put(imgPNG(t, 6, 6, color.RGBA{uint8(60 * (i + 1)), 0, 0, 255}))
		if err != nil {
			t.Fatal(err)
		}
		toks = append(toks, agent.ImageToken(h, fmt.Sprintf("shot%d.png", i+1)))
	}
	if _, err := a.Run(context.Background(), "compare these "+strings.Join(toks, " ")); err != nil {
		t.Fatal(err)
	}
	// The description of the picture the request carried is written in the background, for the messages after it: wait for it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := agent.AwaitCompactionForTest(ctx, a); err != nil {
		t.Fatal(err)
	}
	main, desc := p.split()
	if len(main) != 1 {
		t.Fatalf("conversation requests = %d, want 1", len(main))
	}
	if n := len(main[0].images); n != 1 || !dataURLs(main[0].images) {
		t.Fatalf("first request carried %d images (%v), want 1 data URL", n, main[0].images)
	}
	if got := strings.Count(main[0].text, "DESCRIPTION "); got != 2 {
		t.Errorf("first request has %d descriptions, want 2:\n%s", got, main[0].text)
	}
	if !strings.Contains(main[0].text, "[image 1: shot1.png]") || !strings.Contains(main[0].text, "[image 3: shot3.png]") {
		t.Errorf("the person's text does not name the pictures:\n%s", main[0].text)
	}
	if len(desc) != 3 {
		t.Errorf("description requests = %d, want one per picture (3)", len(desc))
	}
	for _, d := range desc {
		if len(d.images) != 1 || !dataURLs(d.images) || !strings.Contains(d.text, "compare these") {
			t.Errorf("a description request carried %d images, or not the person's question: %+v", len(d.images), d)
		}
	}
	if _, err := a.Run(context.Background(), "thanks"); err != nil {
		t.Fatal(err)
	}
	main, _ = p.split()
	if n := len(main[1].images); n != 0 {
		t.Errorf("the next message's request carried %d images, want 0", n)
	}
	if got := strings.Count(main[1].text, "DESCRIPTION "); got != 3 {
		t.Errorf("the next request has %d descriptions, want 3:\n%s", got, main[1].text)
	}
}

// Screenshots a tool takes in a loop never pile up: every request carries the newest one only.
func TestToolImagesRetireWithinRun(t *testing.T) {
	p := &imgProvider{reply: func(n int, _ imgReq) (core.Turn, *provider.Error) {
		if n < 4 {
			return arvTool(fmt.Sprintf("c%d", n), "shot", map[string]any{}), nil
		}
		return arvText("done"), nil
	}}
	a, _ := newImageAgent(t, p, agent.ImagePolicy{Attached: 1})
	if _, err := a.Run(context.Background(), "take screenshots"); err != nil {
		t.Fatal(err)
	}
	main, _ := p.split()
	if len(main) != 5 {
		t.Fatalf("conversation requests = %d, want 5", len(main))
	}
	for i, r := range main {
		want := 1
		if i == 0 {
			want = 0
		}
		if len(r.images) != want {
			t.Errorf("request %d carried %d images, want %d", i, len(r.images), want)
		}
	}
	if got := strings.Count(main[4].text, "DESCRIPTION "); got != 3 {
		t.Errorf("the last request has %d descriptions, want 3 (the screenshots before the newest)", got)
	}
}

// An endpoint that refuses images gets the same request again with every image described, and no image after that.
func TestImageRefusalFallsBackToDescriptions(t *testing.T) {
	p := &imgProvider{refuse: true, reply: func(int, imgReq) (core.Turn, *provider.Error) { return arvText("ok"), nil }}
	a, blobs := newImageAgent(t, p, agent.ImagePolicy{})
	h, err := blobs.Put(imgPNG(t, 5, 5, color.RGBA{1, 2, 3, 255}))
	if err != nil {
		t.Fatal(err)
	}
	res, err := a.Run(context.Background(), "what is this "+agent.ImageToken(h, "x.png"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "ok" {
		t.Errorf("answer = %q", res.Text)
	}
	main, _ := p.split()
	if len(main) != 2 || len(main[0].images) != 1 || len(main[1].images) != 0 || !strings.Contains(main[1].text, "DESCRIPTION ") {
		t.Errorf("requests = %+v, want a refused one with the image and a second with its description", main)
	}
}

// A token whose blob does not exist, or is not a picture, stays text; NoDescribe retires to a placeholder without a request.
func TestImageTokensAndPlaceholders(t *testing.T) {
	p := &imgProvider{reply: func(int, imgReq) (core.Turn, *provider.Error) { return arvText("ok"), nil }}
	a, blobs := newImageAgent(t, p, agent.ImagePolicy{Attached: -1, NoDescribe: true})
	h, _ := blobs.Put(imgPNG(t, 5, 5, color.RGBA{9, 9, 9, 255}))
	txt, _ := blobs.Put([]byte("not a picture"))
	missing := core.HashString("nothing here")
	in := "look " + agent.ImageToken(h, "a.png") + " " + agent.ImageToken(txt, "") + " " + agent.ImageToken(missing, "m")
	if _, err := a.Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	main, desc := p.split()
	if len(desc) != 0 {
		t.Errorf("NoDescribe sent %d description requests", len(desc))
	}
	if len(main[0].images) != 0 {
		t.Errorf("Attached -1 sent %d images", len(main[0].images))
	}
	for _, want := range []string{"[image 1: a.png]", "no description of it is available", "[[image:" + string(txt) + "]]", "[[image:" + string(missing) + "|m]]"} {
		if !strings.Contains(main[0].text, want) {
			t.Errorf("request lacks %q:\n%s", want, main[0].text)
		}
	}
	if got := agent.StripImageTokens("see " + agent.ImageToken(h, "b|]x.png")); got != "see [image: bx.png]" {
		t.Errorf("StripImageTokens = %q", got)
	}
}

// The event log keeps the person's picture: the turn that carried it is recorded with the image block, whatever the
// thread holds later.
func TestImagesStayInTheLog(t *testing.T) {
	p := &imgProvider{reply: func(int, imgReq) (core.Turn, *provider.Error) { return arvText("ok"), nil }}
	blobs := events.NewMemBlobs()
	log := events.NewMemLog()
	a, err := agent.New(agent.Config{ID: "main", Model: cost.Model{ID: "img-1", ContextTokens: 1_000_000}, Provider: p, Tools: tools.NewRegistry(),
		Params: core.Params{MaxTokens: 512}, Events: log, Blobs: blobs, NoCompaction: true})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	h, _ := blobs.Put(imgPNG(t, 5, 5, color.RGBA{4, 4, 4, 255}))
	for _, in := range []string{"one " + agent.ImageToken(h, ""), "two"} {
		if _, err := a.Run(context.Background(), in); err != nil {
			t.Fatal(err)
		}
	}
	found := false
	for _, ev := range log.OfType(events.TypeTurnAppend) {
		if ev.Type != events.TypeTurnAppend {
			continue
		}
		var tr core.Turn
		if json.Unmarshal(ev.Data, &tr) == nil {
			for _, b := range tr.Blocks {
				if b.Kind == core.BlockImage && b.MediaRef == string(h) {
					found = true
				}
			}
		}
	}
	if !found {
		t.Error("the log has no turn with the picture")
	}
	for _, tr := range a.Thread().Snapshot().Turns {
		for _, b := range tr.Blocks {
			if b.Kind == core.BlockImage {
				t.Errorf("the thread still holds the picture after the next message: %+v", tr)
			}
		}
	}
}

// An endpoint that takes a request with images and never starts its answer is not waited on for its whole timeout: past
// ImageStall the request is sent again with every image described.
func TestImageStallFallsBackToDescriptions(t *testing.T) {
	old := agent.ImageStall
	agent.ImageStall = 50 * time.Millisecond
	defer func() { agent.ImageStall = old }()
	p := &imgProvider{hang: true, reply: func(int, imgReq) (core.Turn, *provider.Error) { return arvText("ok"), nil }}
	a, blobs := newImageAgent(t, p, agent.ImagePolicy{})
	h, _ := blobs.Put(imgPNG(t, 5, 5, color.RGBA{7, 7, 7, 255}))
	start := time.Now()
	res, err := a.Run(context.Background(), "what is this "+agent.ImageToken(h, "x.png"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Text != "ok" || time.Since(start) > 10*time.Second {
		t.Errorf("answer %q after %s", res.Text, time.Since(start))
	}
	main, _ := p.split()
	if len(main) != 2 || len(main[1].images) != 0 || !strings.Contains(main[1].text, "DESCRIPTION ") {
		t.Errorf("requests = %+v, want a stalled one with the image and a second with its description", main)
	}
}
