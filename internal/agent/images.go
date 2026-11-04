package agent

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/cost"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/kv"
	"github.com/anemos-labs/sleipnir/internal/provider"
)

// Images in a conversation.
//
// Many endpoints accept an image or two in a prompt and then fail: too many images, too many image tokens, or a gateway
// that times out re-encoding every picture of a long history on every request. So an image is attached only while it is
// needed: from the moment it enters the thread (a person attached it, a tool returned it) for the rest of the run that
// received it, and only the newest ImagePolicy.Attached of those at once. Every other image is retired: its block is
// replaced, durably and as a declared rebase, by a text block holding a description written for it. The description is
// asked for as soon as the image arrives, in a request of its own that carries that one image and what was asked about it,
// so it is usually ready when the image retires. The event log keeps the image itself, so a person still sees it.
//
// A turn that brings more images than can be attached (five screenshots in one message) is therefore split: each extra
// image is described in its own request, one image per request, and the conversation request carries the descriptions and
// the newest image(s). An endpoint that rejects images altogether is noticed from its error; from then on every image is
// described in text before the conversation request, and the request that failed is sent again.

// DefaultAttachedImages is how many images one request carries when ImagePolicy.Attached is 0.
const DefaultAttachedImages = 1

// ImagePolicy says how images reach the model (images.go).
type ImagePolicy struct {
	// Attached is the most images one conversation request carries: 0 is DefaultAttachedImages, a negative number none (a
	// model that cannot see: every image reaches it as its description).
	Attached int
	// NoDescribe retires images to a short placeholder instead of a description written by a model, saving the request
	// each image costs.
	NoDescribe bool
	// Describer, with DescriberModel, writes the descriptions (a model that sees, for an agent whose model does not, or a
	// cheaper one). Nil describes on the agent's own provider and model.
	Describer      provider.Provider
	DescriberModel cost.Model
}

// KindImage is the request kind of an image description in model.request.
const KindImage = "image"

// describeTimeout bounds one description request, and waitDescription bounds how long a request that must retire an image
// waits for its description before it uses the placeholder.
const (
	describeTimeout = 3 * time.Minute
	waitDescription = 3 * time.Minute
	// maxDescribing is how many description requests one agent has in flight at once.
	maxDescribing = 3
	// describeMaxTokens is the output limit of a description request (reasoning models spend part of it thinking).
	describeMaxTokens = 4096
	// maxQuestion bounds the context a description request is given about its image.
	maxQuestion = 1500
)

// describeSystem is the instruction of a description request.
const describeSystem = `You describe one image for an AI agent that will not be able to see it again. Your description replaces the image in the agent's history, so everything the agent could need from it must be in your words.

Write plain text, no preamble. Start with one sentence saying what the image is (a screenshot of a web page, a terminal, a chart, a photo, a diagram...). Then:
- Transcribe all legible text exactly, keeping its structure: headings, labels, buttons, menu items, table rows, code, error messages, numbers and units.
- Describe the layout and the visual state that matters: what is where, colours, what is selected, highlighted, disabled, overlapping, cut off, misaligned or broken.
- For charts and diagrams, give the axes, series, values and the relations shown.
Be precise and complete rather than short. Do not guess at what you cannot read; say it is illegible. Answer the context below where the image answers it, but describe the whole image.`

// imageDesc is the description of one image (by its media reference): done closes when text or err is set.
type imageDesc struct {
	done chan struct{}
	text string
	err  error
}

// imageState is the agent's bookkeeping of images: the descriptions asked for, the run whose images may stay attached, and
// whether the endpoint has refused images.
type imageState struct {
	mu      sync.Mutex
	descs   map[string]*imageDesc
	sem     chan struct{}
	runFrom core.TurnID // images of turns from this one on belong to the current run
	refused bool        // the endpoint rejected a prompt for its images: none is attached from now on
	media   map[string]string
	order   []string // media cache, oldest first
}

// imageKeep is how many images the next conversation request may carry.
func (a *Agent) imageKeep() int {
	a.imgs.mu.Lock()
	refused := a.imgs.refused
	a.imgs.mu.Unlock()
	if refused {
		return 0
	}
	n := a.cfg.Images.Attached
	switch {
	case n == 0:
		return DefaultAttachedImages
	case n < 0:
		return 0
	}
	return n
}

// beginImageRun marks the start of a run: images of turns appended from now on may stay attached for the run.
func (a *Agent) beginImageRun() {
	next := a.thread.Snapshot().NextID
	a.imgs.mu.Lock()
	a.imgs.runFrom = next
	a.imgs.mu.Unlock()
}

// imageAt is one image of the thread: the turn it is in, the index of its block (and of the block inside a tool result,
// or -1), and the block itself.
type imageAt struct {
	turn     core.TurnID
	idx      int // index of the turn in the snapshot
	blk, sub int
	b        core.Block
}

// threadImages lists the images of turns in thread order.
func threadImages(turns []core.Turn) []imageAt {
	var out []imageAt
	for ti, tr := range turns {
		for bi, b := range tr.Blocks {
			switch b.Kind {
			case core.BlockImage:
				out = append(out, imageAt{turn: tr.ID, idx: ti, blk: bi, sub: -1, b: b})
			case core.BlockToolResult:
				for si, c := range b.Result {
					if c.Kind == core.BlockImage {
						out = append(out, imageAt{turn: tr.ID, idx: ti, blk: bi, sub: si, b: c})
					}
				}
			}
		}
	}
	return out
}

// imageQuestion is the context a description request is given about an image: what the person wrote with it, or the call
// of the tool that returned it.
func imageQuestion(turns []core.Turn, at imageAt) string {
	tr := turns[at.idx]
	var q string
	if at.sub >= 0 {
		res := tr.Blocks[at.blk]
		name, input := "a tool", ""
		for i := at.idx - 1; i >= 0 && q == ""; i-- {
			for _, c := range turns[i].ToolCalls() {
				if c.ToolID == res.ToolID {
					name, input = c.ToolName, string(c.Input)
				}
			}
			if turns[i].Role == core.RoleAssistant {
				break
			}
		}
		q = "The image was returned by the tool " + name
		if input != "" {
			q += " called with " + input
		}
		var said []string
		for _, c := range res.Result {
			if c.Kind == core.BlockText && strings.TrimSpace(c.Text) != "" {
				said = append(said, strings.TrimSpace(c.Text))
			}
		}
		if len(said) > 0 {
			q += "; the tool said: " + strings.Join(said, " ")
		}
		q += "."
		// What the agent meant to learn from it: the text of the turn that made the call.
		for i := at.idx - 1; i >= 0; i-- {
			if turns[i].Role == core.RoleAssistant {
				if t := strings.TrimSpace(kv.AnswerText(turns[i])); t != "" {
					q += " The agent wrote before the call: " + t
				}
				break
			}
		}
	} else {
		var said []string
		for _, b := range tr.Blocks {
			if b.Kind == core.BlockText && !kv.IsNotice(b) && strings.TrimSpace(b.Text) != "" {
				said = append(said, strings.TrimSpace(b.Text))
			}
		}
		q = "The image was attached to this message"
		if len(said) > 0 {
			q += ": " + strings.Join(said, "\n")
		} else {
			q += ", which has no text."
		}
	}
	return clipRunes(q, maxQuestion)
}

// clipRunes cuts s to at most n runes, marking a cut.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// noteImages asks for the description of every new image of a turn just appended, so it is ready when the image retires.
func (a *Agent) noteImages(t core.Turn) {
	if a.cfg.Images.NoDescribe {
		return
	}
	snap := a.thread.Snapshot()
	idx := -1
	for i := len(snap.Turns) - 1; i >= 0; i-- {
		if snap.Turns[i].ID == t.ID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return
	}
	for _, at := range threadImages(snap.Turns[idx : idx+1]) {
		at.idx = idx
		a.describe(at.b, imageQuestion(snap.Turns, at))
	}
}

// describe starts the description of an image unless one was asked for already, and returns its record.
func (a *Agent) describe(b core.Block, question string) *imageDesc {
	a.imgs.mu.Lock()
	if a.imgs.descs == nil {
		a.imgs.descs = map[string]*imageDesc{}
		a.imgs.sem = make(chan struct{}, maxDescribing)
	}
	if d, ok := a.imgs.descs[b.MediaRef]; ok {
		a.imgs.mu.Unlock()
		return d
	}
	d := &imageDesc{done: make(chan struct{})}
	a.imgs.descs[b.MediaRef] = d
	sem := a.imgs.sem
	a.imgs.mu.Unlock()
	a.mu.Lock()
	closed := a.closed
	if !closed {
		a.jobs.Add(1)
	}
	a.mu.Unlock()
	if closed {
		d.err = ErrClosed
		close(d.done)
		return d
	}
	go func() {
		defer a.jobs.Done()
		defer close(d.done)
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-a.life.Done():
			d.err = ErrClosed
			return
		}
		ctx, cancel := context.WithTimeout(a.life, describeTimeout)
		defer cancel()
		d.text, d.err = a.describeOnce(ctx, b, question)
		if d.err != nil {
			a.emit(events.TypeModelError, map[string]any{"req": "image", "kind": "image_description", "error": clipError(d.err.Error())})
		}
	}()
	return d
}

// describeOnce asks the describer for the description of one image.
func (a *Agent) describeOnce(ctx context.Context, b core.Block, question string) (string, error) {
	prov, model := a.cfg.Provider, a.cfg.Model
	if a.cfg.Images.Describer != nil {
		prov, model = a.cfg.Images.Describer, a.cfg.Images.DescriberModel
	}
	params := a.requestParams(prov, model.ID)
	params.MaxTokens = describeMaxTokens
	if model.MaxOutput > 0 && params.MaxTokens > model.MaxOutput {
		params.MaxTokens = model.MaxOutput
	}
	p := &core.Prompt{
		Model:  model.ID,
		System: []core.Block{core.Text(describeSystem)},
		Messages: []core.Message{{Role: core.RoleUser, Blocks: []core.Block{
			{Kind: core.BlockImage, MediaType: b.MediaType, MediaRef: b.MediaRef},
			core.Text("Context: " + question + "\n\nDescribe the image."),
		}}},
		Params: params,
	}
	a.mu.Lock()
	a.forkN++
	label := fmt.Sprintf("%s.i%d", a.cfg.ID, a.forkN)
	a.markPendingLocked(label)
	a.mu.Unlock()
	defer a.clearPending(label)
	a.recordRequest(label, &kv.Rendered{Prompt: p}, nil, kv.Check{}, prov.Profile(), KindImage, false)
	resp, err := a.callOn(ctx, prov, &provider.Request{Prompt: p, Label: label, Capture: a.cfg.CaptureTokens}, PrioBackground, nil, nil)
	if err != nil {
		return "", err
	}
	a.account(resp, label, model)
	text := strings.TrimSpace(kv.AnswerText(resp.Turn))
	if text == "" {
		return "", errors.New("the describer answered with no text")
	}
	return text, nil
}

// retiredText is the text an image is replaced with in the model's history.
func retiredText(b core.Block, desc string) string {
	kind := strings.TrimPrefix(b.MediaType, "image/")
	if kind == "" {
		kind = "image"
	}
	if desc == "" {
		return fmt.Sprintf("[%s image, no longer attached: it was shown earlier in this conversation and no description of it is available; what was said about it then still stands]", kind)
	}
	return fmt.Sprintf("[%s image, no longer attached; a description written when it was shown follows]\n%s\n[end of image description]", kind, desc)
}

// retireImages replaces, before a conversation request, every image that may not be attached to it by its description:
// images of earlier runs, and all but the newest imageKeep of the current run's. It waits for the descriptions it needs
// (bounded) and rewrites the thread as a declared rebase. It reports how many images it retired.
func (a *Agent) retireImages(ctx context.Context) int {
	snap := a.thread.Snapshot()
	all := threadImages(snap.Turns)
	if len(all) == 0 {
		return 0
	}
	keep := a.imageKeep()
	a.imgs.mu.Lock()
	runFrom := a.imgs.runFrom
	a.imgs.mu.Unlock()
	retire := map[[3]int64]core.Block{}
	var order []imageAt
	kept := 0
	for i := len(all) - 1; i >= 0; i-- {
		at := all[i]
		if at.turn >= runFrom && kept < keep {
			kept++
			continue
		}
		order = append(order, at)
	}
	if len(order) == 0 {
		return 0
	}
	descs := map[string]string{}
	deadline := time.NewTimer(waitDescription)
	defer deadline.Stop()
	if !a.cfg.Images.NoDescribe {
		waiting := map[string]*imageDesc{}
		for _, at := range order {
			if _, ok := waiting[at.b.MediaRef]; !ok {
				waiting[at.b.MediaRef] = a.describe(at.b, imageQuestion(snap.Turns, at))
			}
		}
		if len(waiting) > 0 {
			a.cfg.Sink.Notice(a.cfg.ID, "info", fmt.Sprintf("%d %s replaced by %s in the model's history (still shown here)",
				len(waiting), plural(len(waiting), "image", "images"), plural(len(waiting), "its description", "their descriptions")))
		}
	wait:
		for ref, d := range waiting {
			select {
			case <-d.done:
				if d.err == nil {
					descs[ref] = d.text
				}
			case <-ctx.Done():
				break wait
			case <-deadline.C:
				break wait
			}
		}
	}
	for _, at := range order {
		retire[[3]int64{int64(at.turn), int64(at.blk), int64(at.sub)}] = core.Text(retiredText(at.b, descs[at.b.MediaRef]))
	}
	strip := a.cfg.Provider.Profile().ReplayThinking && a.cfg.ApplyPolicy.StripThinking
	a.mu.Lock()
	changed := a.thread.Rewrite(func(t core.Turn) (core.Turn, bool) {
		var blocks []core.Block
		for bi, b := range t.Blocks {
			if b.Kind == core.BlockImage {
				if r, ok := retire[[3]int64{int64(t.ID), int64(bi), -1}]; ok {
					blocks = cloneOnce(blocks, t.Blocks)
					blocks[bi] = r
				}
			}
			if b.Kind == core.BlockToolResult {
				var res []core.Block
				for si, c := range b.Result {
					if r, ok := retire[[3]int64{int64(t.ID), int64(bi), int64(si)}]; ok && c.Kind == core.BlockImage {
						res = cloneOnce(res, b.Result)
						res[si] = r
					}
				}
				if res != nil {
					blocks = cloneOnce(blocks, t.Blocks)
					blocks[bi].Result = res
				}
			}
		}
		changed := blocks != nil
		if changed {
			t.Blocks = blocks
		}
		if strip {
			// Thinking that follows a rewritten turn is bound to the bytes the rewrite changed (see SyncShared).
			if s, ok := kv.StripThinkingTurn(t); ok {
				t, changed = s, true
			}
		}
		return t, changed
	})
	if changed {
		a.epoch++
	}
	a.mu.Unlock()
	if !changed {
		return 0
	}
	described := 0
	for _, at := range order {
		if descs[at.b.MediaRef] != "" {
			described++
		}
	}
	a.emit(events.TypeLayerCommit, map[string]any{"scope": "images", "reason": "images retired from the model's history", "retired": len(order),
		"described": described, "attached": kept})
	return len(order)
}

// cloneOnce returns dst, or a copy of src when dst is still nil: the copy-on-write of a rewrite.
func cloneOnce(dst, src []core.Block) []core.Block {
	if dst != nil {
		return dst
	}
	return append([]core.Block(nil), src...)
}

// plural picks the singular or plural form for n.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// imageRefusal reports whether a provider error says the prompt's images were the problem: no image input on this model or
// route, too many images, an image too large or in a format it does not take.
func imageRefusal(pe *provider.Error) bool {
	if pe == nil || pe.Kind == provider.ErrContextLength {
		return false
	}
	switch pe.Kind {
	case provider.ErrBadRequest, provider.ErrServer, provider.ErrUnknown:
	default:
		return false
	}
	return imageError.MatchString(strings.ToLower(pe.Message + " " + string(pe.Raw)))
}

// imageError matches what endpoints say when they refuse a prompt's images.
var imageError = regexp.MustCompile(`image|vision|multimodal|multi-modal|modalit|image_url|input_image|media type|mime`)

// refuseImages records that the endpoint rejected images, so every image is described from now on, and retires them all.
// It reports whether anything was retired, that is whether sending the request again can succeed.
func (a *Agent) refuseImages(ctx context.Context, pe *provider.Error) bool {
	if len(threadImages(a.thread.Snapshot().Turns)) == 0 {
		return false
	}
	a.imgs.mu.Lock()
	a.imgs.refused = true
	a.imgs.mu.Unlock()
	a.cfg.Sink.Notice(a.cfg.ID, "warn", "the model's endpoint refused the images of the prompt ("+clipError(pe.Message)+"): images reach this model as descriptions from now on")
	return a.retireImages(ctx) > 0
}

// ---- media ------------------------------------------------------------------------------------------------------------

// maxMediaCache is how many prepared images (data URLs) an agent keeps between requests.
const maxMediaCache = 8

// withMedia returns the prompt with every image that refers to the blob store made a data URL the endpoints take,
// downscaled when it is larger than they accept. An image whose bytes cannot be read becomes a text block that says so, so a
// missing blob costs the picture, not the request. The prompt passed in is not changed.
func (a *Agent) withMedia(p *core.Prompt) *core.Prompt {
	if p == nil || !needsMedia(p) {
		return p
	}
	q := *p
	q.Messages = make([]core.Message, len(p.Messages))
	for i, m := range p.Messages {
		m.Blocks = a.mediaBlocks(m.Blocks)
		q.Messages[i] = m
	}
	return &q
}

// needsMedia reports whether any image of p refers to the blob store.
func needsMedia(p *core.Prompt) bool {
	for _, m := range p.Messages {
		for _, b := range m.Blocks {
			if b.Kind == core.BlockImage && blobRef(b.MediaRef) {
				return true
			}
			if b.Kind == core.BlockToolResult {
				for _, c := range b.Result {
					if c.Kind == core.BlockImage && blobRef(c.MediaRef) {
						return true
					}
				}
			}
		}
	}
	return false
}

// blobRef reports whether ref names a blob (a hash) rather than a data URL, a URL or a provider's file id.
func blobRef(ref string) bool { return events.ValidHash(core.Hash(ref)) }

// mediaBlocks is withMedia for one block list (copied when anything in it changes).
func (a *Agent) mediaBlocks(in []core.Block) []core.Block {
	var out []core.Block
	for i, b := range in {
		switch {
		case b.Kind == core.BlockImage && blobRef(b.MediaRef):
			out = cloneOnce(out, in)
			out[i] = a.mediaBlock(b)
		case b.Kind == core.BlockToolResult:
			if res := a.mediaBlocks(b.Result); len(res) > 0 && &res[0] != &b.Result[0] {
				out = cloneOnce(out, in)
				out[i].Result = res
			}
		}
	}
	if out == nil {
		return in
	}
	return out
}

// mediaBlock resolves one blob image to a data URL (cached), or to a text block when it cannot be read.
func (a *Agent) mediaBlock(b core.Block) core.Block {
	a.imgs.mu.Lock()
	url, ok := a.imgs.media[b.MediaRef]
	a.imgs.mu.Unlock()
	if !ok {
		data, err := a.cfg.Blobs.GetMax(core.Hash(b.MediaRef), maxImageBytes)
		if err != nil {
			return core.Text(fmt.Sprintf("[image unavailable: %s]", err))
		}
		mt := b.MediaType
		data, mt = fitImage(data, mt)
		url = "data:" + mt + ";base64," + base64.StdEncoding.EncodeToString(data)
		a.imgs.mu.Lock()
		if a.imgs.media == nil {
			a.imgs.media = map[string]string{}
		}
		if _, dup := a.imgs.media[b.MediaRef]; !dup {
			a.imgs.media[b.MediaRef] = url
			a.imgs.order = append(a.imgs.order, b.MediaRef)
			for len(a.imgs.order) > maxMediaCache {
				delete(a.imgs.media, a.imgs.order[0])
				a.imgs.order = a.imgs.order[1:]
			}
		}
		a.imgs.mu.Unlock()
	}
	b.MediaRef = url
	if mt, _, ok := strings.Cut(strings.TrimPrefix(url, "data:"), ";"); ok {
		b.MediaType = mt
	}
	return b
}

// ---- images a person attaches ------------------------------------------------------------------------------------------

// imageToken matches an image a person attached to a message: [[image:HASH]] or [[image:HASH|name]], HASH the blob the
// interface stored the picture under. Only text a person wrote is read for it (Run, Steer); in mail, tool output or a
// task it is plain text.
var imageToken = regexp.MustCompile(`\[\[image:([0-9a-f]{64})(?:\|([^\]\n]{0,200}))?\]\]`)

// ImageToken is the text that attaches the image stored under ref, named name, to a message a person sends.
func ImageToken(ref core.Hash, name string) string {
	name = strings.NewReplacer("]", "", "|", "", "\n", " ").Replace(strings.TrimSpace(name))
	if name == "" {
		return "[[image:" + string(ref) + "]]"
	}
	return "[[image:" + string(ref) + "|" + name + "]]"
}

// StripImageTokens replaces the image tokens of a person's text by a short mention ("[image: name]"), for a place that
// shows the text and not the picture.
func StripImageTokens(text string) string {
	return imageToken.ReplaceAllStringFunc(text, func(m string) string {
		sub := imageToken.FindStringSubmatch(m)
		if sub[2] != "" {
			return "[image: " + sub[2] + "]"
		}
		return "[image]"
	})
}

// attachImages turns the image tokens of a person's text into image blocks that follow it, the text keeping a numbered
// mention of each ("[image 1: shot.png]") so that what the person wrote about "the second screenshot" still reads. A token
// whose blob is missing or is not an image stays as it was written.
func (a *Agent) attachImages(text string) (string, []core.Block) {
	if !strings.Contains(text, "[[image:") {
		return text, nil
	}
	var imgs []core.Block
	out := imageToken.ReplaceAllStringFunc(text, func(m string) string {
		sub := imageToken.FindStringSubmatch(m)
		data, err := a.cfg.Blobs.GetMax(core.Hash(sub[1]), maxImageBytes)
		if err != nil {
			return m
		}
		mt := sniffImage(data)
		if mt == "" {
			return m
		}
		imgs = append(imgs, core.Block{Kind: core.BlockImage, MediaType: mt, MediaRef: sub[1]})
		if sub[2] != "" {
			return fmt.Sprintf("[image %d: %s]", len(imgs), sub[2])
		}
		return fmt.Sprintf("[image %d]", len(imgs))
	})
	return out, imgs
}

// sniffImage names the image format of data among those endpoints take (PNG, JPEG, GIF, WebP), or "".
func sniffImage(b []byte) string {
	switch {
	case len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n":
		return "image/png"
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg"
	case len(b) >= 6 && (string(b[:6]) == "GIF87a" || string(b[:6]) == "GIF89a"):
		return "image/gif"
	case len(b) >= 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}
