package session

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/anemos-labs/sleipnir/internal/tools"
)

// Peers is what a host that runs several sessions side by side (the web interface's tabs) offers the agent the person talks to in
// one of them: the other sessions, what they said, a message to one, and a new one. The sleipnir tool's sessions action reaches it;
// a host that runs a single session (the terminal chat) does not implement it. Ids are the host's session ids (a tab's id); a name
// the person gave is accepted where an id is.
type Peers interface {
	// PeerList lists the sessions the host runs, this one included (Self).
	PeerList() []PeerInfo
	// PeerSend delivers text to the session id as a message from this one and says how it was delivered. The receiver's answer
	// comes back as mail when it reads the message in a turn of its own.
	PeerSend(id, text string) (string, error)
	// PeerRead returns the newest n rows of what the session id's transcript shows (the person's messages, its agent's answers, the
	// messages of other sessions and the host's status lines), with a line on its state first.
	PeerRead(id string, n int) (string, error)
	// PeerSpawn starts a new session beside this one and says which; its first message, when there is one, is delivered as
	// PeerSend's.
	PeerSpawn(req PeerSpawn) (string, error)
	// PeerWait returns the next message of the session id to this one (the answer to this one's message), waiting for it at most
	// timeout or until ctx ends; one that reached the agent already since it last wrote to id is returned at once. It is an error
	// when this session has written to id and had its answer already, or never wrote to it.
	PeerWait(ctx context.Context, id string, timeout time.Duration) (string, error)
}

// PeerInfo is one session as Peers lists it. State is starting, working (a turn runs), waiting (a question waits for the person),
// idle or failed (it could not start). Workers is a team's worker slots (0: a single agent). Parent is the session that started it
// through PeerSpawn ("" when the person did). Last is the start of the newest thing its agent said.
type PeerInfo struct {
	ID, Name, Dir, Model, Mode, State string
	Workers                           int
	Self                              bool
	Parent                            string
	Queued                            int
	Goal                              string
	Last                              string
}

// PeerSpawn is a new session asked for through Peers: Dir is the folder it works in ("": the asking session's), Model its model
// ("": the asking session's), Workers its team's worker slots (nil: as many as the asking session has, 0: a single agent), Goal a
// standing goal it works on until a judge finds it met, and Text a first message.
type PeerSpawn struct {
	Name, Dir, Model, Goal, Text string
	Workers                      *int
}

// peerMaxText bounds a message to another session, as a steer is bounded.
const peerMaxText = 4000

// peerWaitDefault is how long sessions op wait waits unless asked for another time, and peerWaitMax the longest it waits (the
// shell tool's own longest call).
const (
	peerWaitDefault = 5 * time.Minute
	peerWaitMax     = 10 * time.Minute
)

// peerReadRows is how many transcript rows sessions op read returns unless asked for another number, and peerReadMax the most.
const (
	peerReadRows = 20
	peerReadMax  = 200
)

// sessions is the sleipnir tool's sessions action: list, read, send and spawn through the host's Peers. Looking needs nothing;
// a message or a new session is a change of state and goes through the permission engine.
func (t harnessTool) sessions(ctx context.Context, in harnessInput, ask func(string) *tools.Result) *tools.Result {
	h := t.s.currentHost()
	p, ok := h.(Peers)
	if h == nil || !ok {
		return tools.Errorf("sessions: this program runs this session alone; sessions that see and message each other run side by side in the web interface (sleipnir web)")
	}
	id := strings.TrimSpace(in.ID)
	text := strings.TrimSpace(in.Text)
	switch op := strings.ToLower(strings.TrimSpace(in.Op)); op {
	case "", "list":
		return &tools.Result{Text: formatPeers(p.PeerList())}
	case "read":
		if id == "" {
			return tools.Errorf("sessions read: id is the session to read (sessions op list names them)")
		}
		n := peerReadRows
		if in.N != nil {
			n = min(max(*in.N, 1), peerReadMax)
		}
		out, err := p.PeerRead(id, n)
		if err != nil {
			return tools.Errorf("sessions read: %v", err)
		}
		return &tools.Result{Text: tools.SanitizeForTerminal(out)}
	case "wait":
		if id == "" {
			return tools.Errorf("sessions wait: id is the session whose answer you wait for")
		}
		d := peerWaitDefault
		if in.TimeoutSec != nil {
			d = min(max(time.Duration(*in.TimeoutSec)*time.Second, 10*time.Second), peerWaitMax)
		}
		out, err := p.PeerWait(ctx, id, d)
		if err != nil {
			return tools.Errorf("sessions wait: %v", err)
		}
		return &tools.Result{Text: out}
	case "send":
		switch {
		case id == "":
			return tools.Errorf("sessions send: id is the session to write to (sessions op list names them)")
		case text == "":
			return tools.Errorf("sessions send: text is the message")
		case utf8.RuneCountInString(text) > peerMaxText:
			return tools.Errorf("sessions send: a message is at most %d characters; put long material in a file of the project and name it", peerMaxText)
		}
		if r := ask("send a message to session " + clipLine(id, 60) + ": " + clipLine(text, 160)); r != nil {
			return r
		}
		out, err := p.PeerSend(id, text)
		if err != nil {
			return tools.Errorf("sessions send: %v", err)
		}
		return &tools.Result{Text: out}
	case "spawn":
		if utf8.RuneCountInString(text) > peerMaxText {
			return tools.Errorf("sessions spawn: the first message is at most %d characters; a longer brief goes in goal or in a file", peerMaxText)
		}
		if text == "" && strings.TrimSpace(in.Goal) == "" {
			return tools.Errorf("sessions spawn: give the new session something to do: text (a first message) or goal (a standing goal)")
		}
		if in.Workers != nil && (*in.Workers < 0 || *in.Workers > 64) {
			return tools.Errorf("sessions spawn: workers is 0 (a single agent) to 64")
		}
		req := PeerSpawn{Name: strings.TrimSpace(in.Name), Dir: strings.TrimSpace(in.Dir), Model: strings.TrimSpace(in.Model),
			Goal: strings.TrimSpace(in.Goal), Text: text, Workers: in.Workers}
		where := firstNonEmpty(req.Dir, "this project")
		if r := ask("start a new session in " + clipLine(where, 120) + ": " + clipLine(firstNonEmpty(req.Goal, text), 160)); r != nil {
			return r
		}
		out, err := p.PeerSpawn(req)
		if err != nil {
			return tools.Errorf("sessions spawn: %v", err)
		}
		return &tools.Result{Text: out}
	default:
		return tools.Errorf("sessions: op is list, read, send, wait or spawn")
	}
}

// formatPeers is sessions op list: one block per session, this one marked.
func formatPeers(list []PeerInfo) string {
	if len(list) <= 1 {
		return "no other session runs beside this one. sessions op spawn starts one."
	}
	var b strings.Builder
	for _, p := range list {
		kind := "single agent"
		if p.Workers > 0 {
			kind = fmt.Sprintf("team of %d workers (you write to its manager)", p.Workers)
		}
		self := ""
		if p.Self {
			self = " (this session: you)"
		}
		fmt.Fprintf(&b, "%s%s  %q  %s; %s; %s", p.ID, self, p.Name, p.State, kind, firstNonEmpty(p.Model, "model unknown"))
		if p.Mode != "" {
			b.WriteString("; mode " + p.Mode)
		}
		b.WriteString("\n  dir " + p.Dir)
		if p.Parent != "" {
			b.WriteString("; started by session " + p.Parent)
		}
		if p.Queued > 0 {
			fmt.Fprintf(&b, "; %d waiting in its queue", p.Queued)
		}
		b.WriteString("\n")
		if p.Goal != "" {
			b.WriteString("  goal: " + clipLine(p.Goal, 200) + "\n")
		}
		if p.Last != "" && !p.Self {
			b.WriteString("  last said: " + clipLine(p.Last, 200) + "\n")
		}
	}
	b.WriteString("sessions op read id ID shows what one said; op send id ID text ... writes to it.")
	return strings.TrimSpace(tools.SanitizeForTerminal(b.String()))
}
