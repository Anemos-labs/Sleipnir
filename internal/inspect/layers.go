package inspect

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	layerTextMax = 8 << 10   // bytes of a layer's text shown in the preview
	diffReadMax  = 256 << 10 // bytes of each side read to locate the first difference
	diffWindow   = 120       // bytes of context shown on each side of the first difference
)

// textJob is a blob read deferred until the model lock is released.
type textJob struct {
	idx        int
	label      string
	hash       string
	prevHash   string
	system     []string
	prevSystem []string
	tools      string
	prevTools  string
}

// overlap returns the nonnegative intersection length of the half-open intervals [a,b) and [c,d).
func overlap(a, b, c, d int) int {
	lo, hi := max(a, c), min(b, d)
	if hi > lo {
		return hi - lo
	}
	return 0
}

// Layers explains one request's prompt: the seven layers with their sizes,
// which of them the provider served from cache, wrote or processed fresh, and
// what changed compared with the agent's previous request. With withText it also
// reads layer text from the blob store and locates the first differing byte of
// every layer that was rewritten.
func (s *Session) Layers(id string, withText bool) (LayerReport, bool) {
	s.mu.RLock()
	r := s.byID[id]
	if r == nil {
		s.mu.RUnlock()
		return LayerReport{}, false
	}
	rep := LayerReport{Req: s.reqViewLocked(r), Layers: []LayerInfo{}, Expected: r.expected, Rebase: r.rebase,
		CacheKey: r.cacheKey, PrefixKey: shortHash(r.prefixKey), Ratio: r.ratio, Breakpoints: r.breakpoints}
	prev, next := s.prevMainLocked(r), s.nextMainLocked(r)
	if prev != nil {
		rep.Prev = prev.id
		rep.KeyChanged = prev.cacheKey != r.cacheKey
	}
	if next != nil {
		rep.Next = next.id
	}
	if r.kind != "main" {
		rep.Notes = append(rep.Notes, "This is a side request (a compactor fork). It reuses the agent's prompt with one instruction appended, so it has no layers of its own: look at the agent's main requests around it.")
		s.mu.RUnlock()
		return rep, true
	}
	tok, g0known := s.layerTokensLocked(r)
	rep.Prompt = r.prompt
	if r.done {
		rep.Read, rep.Write = r.usage.CacheReadTokens, r.usage.CacheWriteTokens()
		rep.Fresh = max(r.prompt-rep.Read-rep.Write, 0)
	}
	if r.first >= 0 {
		rep.FirstChange = LayerKeys[r.first]
	}

	var jobs []textJob
	pos, sum := 0, 0
	for i := 0; i < 7; i++ {
		li := LayerInfo{Key: LayerKeys[i], Title: LayerTitles[i], Tokens: tok[i], Start: pos, State: LayerAbsent}
		var prevHash string
		present := tok[i] > 0
		switch i {
		case 0:
			li.Hash = shortHash(r.toolsHash)
			li.Source = "unknown"
			if g0known {
				li.Source, present = "blob", true
			}
			if prev != nil {
				prevHash = shortHash(prev.toolsHash)
			}
			if r.toolsHash != "" || len(r.systemHashes) > 0 {
				present = true
			}
			if withText {
				j := textJob{idx: i, label: "G0", tools: r.toolsHash, system: r.systemHashes}
				if prev != nil {
					j.prevTools, j.prevSystem = prev.toolsHash, prev.systemHashes
				}
				jobs = append(jobs, j)
			}
		case 1, 2, 3, 4:
			name := layerKey(i)
			li.Source = "recorded"
			for _, sec := range r.sections {
				if sec.name == name {
					li.Hash, li.Breakpoint, present = shortHash(sec.hash), sec.bp, true
					if withText {
						j := textJob{idx: i, label: LayerKeys[i], hash: sec.hash}
						if prev != nil {
							j.prevHash = prev.sectionHash(name)
						}
						jobs = append(jobs, j)
					}
				}
			}
			if prev != nil {
				prevHash = shortHash(prev.sectionHash(name))
			}
		case 5:
			li.Source = "derived"
			li.Hash = fmt.Sprintf("t%d–t%d", r.threadFrom, r.threadTo)
			present = r.prompt > 0 || r.threadTo > 0
		case 6:
			li.Source = "unknown"
			switch {
			case r.hotTok >= 0 && r.hotHash == "":
				li.Source = "recorded" // no hot block was sent
			case r.hotTok >= 0:
				li.Source = "blob"
			}
			li.Hash = shortHash(r.hotHash)
			present = r.hotHash != ""
			if withText && r.hotHash != "" {
				jobs = append(jobs, textJob{idx: i, label: "G6", hash: r.hotHash})
			}
		}
		li.PrevHash = prevHash
		li.Changed = r.changed&(1<<i) != 0
		li.State = layerState(i, present, li.Changed, prev != nil, prevHash != "" || (i == 5 && prev != nil), r.first)
		if !present {
			li.State = LayerAbsent
		}
		li.HasText = withText
		// Billing split by position along the prompt: the provider serves a
		// prefix from cache, then writes, then processes the rest.
		if r.done {
			a, b := pos, pos+tok[i]
			li.Read = overlap(a, b, 0, rep.Read)
			li.Write = overlap(a, b, rep.Read, rep.Read+rep.Write)
			li.Fresh = tok[i] - li.Read - li.Write
		}
		pos += tok[i]
		sum += tok[i]
		rep.Layers = append(rep.Layers, li)
	}

	if !g0known {
		rep.Notes = append(rep.Notes, "G0 (constitution + tools) has no size: its blobs are missing, so its tokens are counted inside the thread.")
	}
	if r.done && sum > r.prompt {
		rep.Notes = append(rep.Notes, fmt.Sprintf("The agent's layer estimates exceed the provider-reported prompt by %s tokens, so the thread is shown as 0.", fmtK(sum-r.prompt)))
	}
	if r.done {
		rep.Notes = append(rep.Notes, "G1 to G4 are the agent's token estimates from the request recipe; the thread is the rest of the provider-reported prompt, so its size absorbs their estimation error.")
	}
	if rep.KeyChanged {
		rep.Notes = append(rep.Notes, "The routing cache key differs from the previous request's.")
	}
	if r.done && r.expected > 0 {
		rep.Notes = append(rep.Notes, fmt.Sprintf("The guard expected about %s tokens to be read from cache; the provider reported %s.", fmtK(r.expected), fmtK(rep.Read)))
	}
	blobs := s.blobs
	s.mu.RUnlock()

	if withText && blobs != nil {
		for _, j := range jobs {
			fillText(&rep, blobs, j)
		}
	}
	return rep, true
}

// layerState decides what happened to one layer relative to the agent's previous
// request. Because a prefix cache breaks at the first changed byte, a layer whose
// own bytes are unchanged is still invalidated when an earlier one changed.
func layerState(i int, present, changed, hasPrev, hadBefore bool, first int) string {
	switch {
	case !present:
		return LayerAbsent
	case i == 6:
		return LayerFresh
	case !hasPrev:
		return LayerNew // first request of the agent: everything is written for the first time
	case i == 5:
		switch {
		case changed:
			return LayerRewritten
		case first >= 0 && first < 5:
			return LayerInvalidated
		}
		return LayerAppended
	case changed && !hadBefore:
		return LayerNew
	case changed:
		return LayerRewritten
	case first >= 0 && i > first:
		return LayerInvalidated
	}
	return LayerCached
}

// fillText reads a layer's blob(s) for the preview and, if it changed since the
// previous request, finds the first differing byte.
func fillText(rep *LayerReport, b *blobStore, j textJob) {
	li := &rep.Layers[j.idx]
	if j.idx == 0 {
		fillG0(rep, b, j)
		return
	}
	data, total, ok := b.read(j.hash, layerTextMax)
	if !ok {
		return
	}
	li.Text, li.TextCut, li.Bytes = strings.ToValidUTF8(string(data), "�"), total > int64(len(data)), int(total)
	if j.prevHash != "" && j.prevHash != j.hash {
		if d, ok := diffBlobs(b, j.label, j.prevHash, j.hash); ok {
			rep.Diffs = append(rep.Diffs, d)
		}
	} else if j.prevHash == "" && j.hash != "" && li.State != LayerAbsent && rep.Prev != "" {
		rep.Notes = append(rep.Notes, li.Key+" did not exist in the previous request.")
	}
}

// fillG0 shows the system prompt and the tool names, and diffs either if it changed.
func fillG0(rep *LayerReport, b *blobStore, j textJob) {
	li := &rep.Layers[0]
	var sb strings.Builder
	var bytes int64
	for _, h := range j.system {
		data, total, ok := b.read(h, layerTextMax)
		if !ok {
			continue
		}
		bytes += total
		var blk struct{ Text string }
		if json.Unmarshal(data, &blk) == nil && blk.Text != "" {
			sb.WriteString(blk.Text)
		} else {
			sb.Write(data)
		}
		sb.WriteString("\n")
	}
	if j.tools != "" {
		data, total, ok := b.read(j.tools, diffReadMax)
		if ok {
			bytes += total
			var specs []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			}
			if json.Unmarshal(data, &specs) == nil {
				fmt.Fprintf(&sb, "\n-- tools (%d) --\n", len(specs))
				for i, t := range specs {
					if i >= 200 {
						break
					}
					fmt.Fprintf(&sb, "%s: %s\n", oneLine(t.Name, 60), oneLine(t.Description, 100))
				}
			}
		}
	}
	text := sb.String()
	if text == "" {
		return
	}
	if len(text) > layerTextMax {
		text, li.TextCut = text[:layerTextMax], true
	}
	li.Text, li.Bytes = strings.ToValidUTF8(text, "�"), int(bytes)
	if j.prevTools != "" && j.prevTools != j.tools {
		if d, ok := diffBlobs(b, "G0 tools", j.prevTools, j.tools); ok {
			rep.Diffs = append(rep.Diffs, d)
		}
	}
	if len(j.prevSystem) == 1 && len(j.system) == 1 && j.prevSystem[0] != j.system[0] {
		if d, ok := diffBlobs(b, "G0 system", j.prevSystem[0], j.system[0]); ok {
			rep.Diffs = append(rep.Diffs, d)
		}
	}
}

// diffBlobs reports where two blobs first differ, with a little context.
func diffBlobs(b *blobStore, label, prevHash, hash string) (LayerDiff, bool) {
	pa, ta, ok1 := b.read(prevHash, diffReadMax)
	pb, tb, ok2 := b.read(hash, diffReadMax)
	if !ok1 || !ok2 {
		return LayerDiff{}, false
	}
	at := commonPrefix(pa, pb)
	before, split := window(pa, at)
	after, _ := window(pb, at)
	return LayerDiff{Layer: label, At: at, PrevBytes: int(ta), Bytes: int(tb), Before: before, After: after, Split: split}, true
}

// commonPrefix is the length of the longest common prefix of a and b.
func commonPrefix(a, b []byte) int {
	n := min(len(a), len(b))
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return i
}

// window returns the text around byte offset at, cut on character boundaries,
// and how many of its characters come before at.
func window(data []byte, at int) (string, int) {
	lo, hi := max(at-diffWindow, 0), min(at+diffWindow, len(data))
	at = min(at, len(data))
	head := strings.ToValidUTF8(string(data[lo:at]), "\uFFFD")
	return strings.ToValidUTF8(string(data[lo:hi]), "\uFFFD"), utf8.RuneCountInString(head)
}
