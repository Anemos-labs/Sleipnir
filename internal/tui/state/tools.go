package state

import (
	"strconv"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
)

// toolInput is the part of a tool call's input the state reads: the field each tool names its target by.
type toolInput struct {
	Command  string `json:"command"`
	Path     string `json:"path"`
	FilePath string `json:"file_path"`
	Pattern  string `json:"pattern"`
	URL      string `json:"url"`
	Query    string `json:"query"`
	Action   string `json:"action"`
	ID       string `json:"id"`
	Title    string `json:"title"`
	Text     string `json:"text"`
	Task     string `json:"task"`
	Role     string `json:"role"`
	To       string `json:"to"`
	Agent    string `json:"agent"`
	Patch    string `json:"patch"`
}

// toolSummary says what a call was asked to do, in a short line: the command of bash, the path of the file tools, the pattern
// of grep, the URL of a fetch, what a swarm tool was asked. It is text the model chose, made one line and cut.
func toolSummary(name string, in *toolInput) string {
	path := firstOf(in.Path, in.FilePath)
	var v string
	switch name {
	case "bash":
		v = in.Command
	case "read", "write", "edit", "ls", "glob":
		v = firstOf(path, in.Pattern)
	case "grep":
		v = in.Pattern
		if path != "" {
			v += " " + path
		}
	case "apply_patch":
		v = in.Patch
	case "web_fetch":
		v = in.URL
	case "web_search":
		v = in.Query
	case "task":
		v = in.Action
		if t := firstOf(in.ID, in.Title); t != "" {
			v += " " + t
		}
	case "spawn":
		v = firstOf(in.Role, "worker") + " " + firstOf(in.Task, in.Agent)
	case "mail":
		v = "to " + in.To + ": " + in.Text
	case "note":
		v = in.Text
	case "wait":
		v = "waiting for the team"
	default:
		v = firstOf(in.Command, path, in.Pattern, in.URL, in.Query, in.Action, in.Title, in.Text)
	}
	return clean(v, textShort)
}

// toolStatus maps file mutations to editing and wait to waiting, using generic tool activity
// otherwise.
func toolStatus(name string) Status {
	switch name {
	case "edit", "write", "apply_patch":
		return StatusEditing
	case "wait":
		return StatusWaiting
	}
	return StatusTool
}

// onToolCall is tool.call: id, name (as the model wrote it), input, and as when the harness ran another tool than the name says
// (a model that leaks pieces of its chat format into the name, "read<|channel|>commentary", or qualifies it, "functions.read":
// internal/agent exec.go repairToolName). What the agent is shown doing is the tool that ran.
func (s *State) onToolCall(e events.Event, t time.Time) {
	var p struct {
		ID    string    `json:"id"`
		Name  string    `json:"name"`
		As    string    `json:"as"`
		Input toolInput `json:"input"`
	}
	if !s.decode(e.Data, maxToolPayload, &p) {
		return
	}
	name := clip(firstOf(p.As, p.Name), textID)
	if name == "" {
		s.bad()
		return
	}
	s.totals.ToolCalls++
	a := s.agent(e.Agent, t)
	if a == nil {
		return
	}
	a.ToolCalls++
	id := clip(p.ID, textID)
	if id != "" {
		a.removeTool(id) // the same call again: the later one stands
	}
	if len(a.tools) >= MaxOpenTools {
		a.tools = append(a.tools[:0], a.tools[1:]...)
	}
	a.failed = failure{}
	a.tools = append(a.tools, openTool{id: id, name: name, summary: toolSummary(name, &p.Input), t: t, seq: e.Seq, status: toolStatus(name)})
	if a.run == runIdle || a.run == runDone || a.run == runError {
		a.run = runThinking // it is working: whatever said it had stopped is out of date
	}
	a.active(t)
	a.syncBusy(t)
	a.refresh()
}

func (s *State) onToolResult(e events.Event, t time.Time) {
	var p struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Error bool   `json:"error"`
		Ms    int64  `json:"ms"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	if p.Error {
		s.totals.ToolErrors++
	}
	name, summary := clip(p.Name, textID), ""
	a := s.agent(e.Agent, t)
	if a != nil {
		if c, ok := a.removeTool(clip(p.ID, textID)); ok {
			name, summary = firstOf(c.name, name), c.summary // the tool that ran, not the name as the model wrote it
			if !c.t.IsZero() && p.Ms <= 0 {
				p.Ms = t.Sub(c.t).Milliseconds()
			}
		}
		if p.Error {
			a.ToolErrors++
		} else if a.Stuck.Active {
			a.Stuck.Active = false // a call succeeded: it got out of the loop
		}
		a.active(t)
		a.syncBusy(t)
		a.refresh()
	}
	glyph, kind := GlyphOK, FeedTool
	if p.Error {
		glyph, kind = GlyphFail, FeedToolErr
	}
	text := firstOf(name, "tool")
	if summary != "" {
		text += " " + summary
	}
	s.line(e.Seq, t, e.Agent, kind, glyph, text, fmtDur(p.Ms))
}

func (s *State) onToolJob(e events.Event, t time.Time) {
	var p struct {
		ID         string `json:"id"`
		Agent      string `json:"agent"`
		Command    string `json:"command"`
		Status     string `json:"status"`
		Exit       *int   `json:"exit"`
		DurationMS int64  `json:"duration_ms"`
	}
	if !s.decode(e.Data, maxPayload, &p) {
		return
	}
	if p.Status == "started" {
		return
	}
	glyph, what := GlyphOK, "finished"
	if p.Status == "killed" {
		glyph, what = GlyphFail, "was killed"
	} else if p.Exit != nil && *p.Exit != 0 {
		glyph, what = GlyphFail, "exited "+strconv.Itoa(*p.Exit)
	}
	s.line(e.Seq, t, firstOf(p.Agent, e.Agent), FeedJob, glyph, "background job "+clip(p.ID, textID)+" "+what+": "+clean(p.Command, textShort), fmtDur(p.DurationMS))
}
