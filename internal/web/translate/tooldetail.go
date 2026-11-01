package translate

import (
	"bytes"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/core"
	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// Limits of a tool call's detail (ToolDetail): what a person reads of one call when the row of the feed, which shows a line of
// each, is not enough.
const (
	// DetailMaxInput bounds the arguments shown, as indented JSON.
	DetailMaxInput = 256 << 10
	// DetailMaxOutput bounds the output shown: a longer one keeps its head and its end (where a command's errors are), with a
	// line in between that says how much is left out.
	DetailMaxOutput = 1 << 20
	// detailMaxBlob bounds the complete output read from the blob store when the agent was shown a truncated result.
	detailMaxBlob = 16 << 20
)

// ErrNoToolCall is ToolDetail's error when the log holds no call of that agent with that id.
var ErrNoToolCall = errors.New("no such tool call in the session's log")

// ToolDetail reads one tool call from a session's log (path, an events.jsonl) and its blob store (nil: none): its whole
// arguments, the whole command or path it names, and its whole output, the complete one from the blob store when the agent was
// shown a shortened result. agent is the call's agent as the page names it (mgr for the main agent) and tid the call's id, as a
// tool row (wire.Tool) carries them. When an id was used more than once by the agent, the last call wins. root is the project
// directory, against which the paths the call names are shown. The text is bounded by DetailMaxInput and DetailMaxOutput.
func ToolDetail(path string, blobs events.Blobs, root, agent, tid string) (wire.ToolDetail, error) {
	var (
		d     wire.ToolDetail
		found bool
		raw   json.RawMessage
		ref   core.Hash
		out   string
		hasR  bool
	)
	needle := []byte(tid)
	err := events.Scan(path, func(e events.Event) error {
		if uiID(e.Agent) != agent || !bytes.Contains(e.Data, needle) {
			return nil
		}
		switch e.Type {
		case events.TypeToolCall:
			var p struct {
				ID    string          `json:"id"`
				Name  string          `json:"name"`
				As    string          `json:"as"`
				Input json.RawMessage `json:"input"`
			}
			if json.Unmarshal(e.Data, &p) != nil || p.ID != tid {
				return nil
			}
			found, hasR, ref, out = true, false, "", ""
			raw = p.Input
			d = wire.ToolDetail{Agent: agent, TID: tid, Tool: firstNonEmpty(p.As, p.Name), At: e.TS.UnixMilli(), Running: true}
		case events.TypeToolResult:
			var p struct {
				ID        string    `json:"id"`
				Error     bool      `json:"error"`
				Refused   bool      `json:"refused"`
				Chars     int       `json:"chars"`
				Ms        int64     `json:"ms"`
				Ref       core.Hash `json:"ref"`
				FullChars int       `json:"full_chars"`
			}
			if !found || json.Unmarshal(e.Data, &p) != nil || p.ID != tid {
				return nil
			}
			d.Running, d.Error, d.Refused, d.Ms, d.Chars = false, p.Error, p.Refused, p.Ms, max(p.Chars, p.FullChars)
			ref = p.Ref
		case events.TypeTurnAppend:
			if !found {
				return nil
			}
			var turn core.Turn
			if json.Unmarshal(e.Data, &turn) != nil || turn.Role != core.RoleUser {
				return nil
			}
			for _, blk := range turn.Blocks {
				if blk.Kind != core.BlockToolResult || blk.ToolID != tid {
					continue
				}
				var b strings.Builder
				for _, r := range blk.Result {
					if r.Kind == core.BlockText {
						b.WriteString(r.Text)
					}
					if b.Len() > detailMaxBlob {
						break
					}
				}
				out, hasR = b.String(), true
				if blk.IsError {
					d.Error = true
				}
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, events.ErrCorruptLog) {
		return wire.ToolDetail{}, err
	}
	if !found {
		return wire.ToolDetail{}, ErrNoToolCall
	}
	in := decodeInput(raw)
	d.Name = displayName(d.Tool)
	d.Arg = argOf(root, d.Tool, &in, "")
	d.Input = indentInput(raw)
	if ref != "" && blobs != nil && events.ValidHash(ref) {
		if b, err := blobs.GetMax(ref, detailMaxBlob); err == nil {
			out, hasR, d.Full = string(b), true, true
		}
	}
	d.HasOutput = hasR
	d.Output, d.Cut = headTail(strings.ToValidUTF8(out, "�"), DetailMaxOutput)
	return d, nil
}

// indentInput is a call's arguments as indented JSON, at most DetailMaxInput bytes; arguments that are not JSON are shown as they are.
func indentInput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var b bytes.Buffer
	if len(raw) > DetailMaxInput || json.Indent(&b, raw, "", "  ") != nil {
		b.Reset()
		b.Write(raw)
	}
	s, _ := headTail(strings.ToValidUTF8(b.String(), "�"), DetailMaxInput)
	return s
}

// headTail keeps s whole when it fits in max bytes, else its first quarter and its last three quarters with a line between
// them that counts what is left out; cut reports the shortening.
func headTail(s string, max int) (out string, cut int) {
	if len(s) <= max {
		return s, 0
	}
	head, tail := max/4, max-max/4
	h, t := strings.ToValidUTF8(s[:head], ""), strings.ToValidUTF8(s[len(s)-tail:], "")
	cut = len(s) - len(h) - len(t)
	return h + "\n… [" + strconv.Itoa(cut) + " bytes not shown] …\n" + t, cut
}
