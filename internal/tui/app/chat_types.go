package app

import (
	"context"
	"io"
	"time"

	"github.com/anemos-labs/sleipnir/internal/events"
	"github.com/anemos-labs/sleipnir/internal/tui/cell"
	"github.com/anemos-labs/sleipnir/internal/tui/input"
	"github.com/anemos-labs/sleipnir/internal/tui/term"
)

// ChatScreen is the inline renderer the chat draws on: lines go to the scrollback (Print) or to the live region under it
// (SetLive, SetCursor), and nothing is written until Flush. render.Inline is one; a test may bring another.
type ChatScreen interface {
	Print(lines ...cell.Line)
	SetLive(lines []cell.Line)
	SetCursor(row, col int)
	Resize(cols, rows int)
	Size() (cols, rows int)
	Flush() error
	Close() error
}

// ChatHost is what the program asks of the session it is in front of. The session is made while the program already runs (it can
// take a while, and what a person types meanwhile is kept), so the host arrives in a ChatAttach.
type ChatHost interface {
	// Turn runs one goal to its end. The program calls it on a goroutine of its own making and cancels ctx to cancel the turn (Esc,
	// Ctrl-C, the end of the session); a turn that ends that way returns an error that is context.Canceled.
	Turn(ctx context.Context, goal string) TurnResult
	// Command runs a slash command (the line starts with "/"), writing what it has to say to out. It runs on a goroutine of the
	// program's too, under a ctx that Ctrl-C cancels. Nothing it does may write to the terminal itself.
	Command(ctx context.Context, line string, out io.Writer) CommandResult
	// Mode is the permission mode, for the footer, and SetMode changes it (shift+tab). SetMode returns the mode that is in force
	// afterwards, which is the old one when the mode was refused.
	Mode() string
	SetMode(mode string) string
}

// TurnResult is how a turn ended.
type TurnResult struct {
	Steps    int
	CostUSD  float64
	HitRatio float64
	// Err is nil for a turn that finished, an error that is context.Canceled for one that was cancelled, and what the harness
	// says for one that failed. Message, when set, is what the person is told instead of "error: " and Err.
	Err     error
	Message string
}

// CommandResult is what a slash command asked for: to end the chat, or to send a prompt (a custom command or a skill expanded into
// one).
type CommandResult struct {
	Quit bool
	Send string
	// Model, when set, is the model the session runs on now (after /model), for the footer.
	Model string
}

// ChatInfo is what the banner and the footer say about the session.
type ChatInfo struct {
	Version   string
	Model     string
	Cwd       string
	SessionID string
	// Budget is "budget $5.00" (or ""), Resumed the sentence about a session that was continued (or "").
	Budget  string
	Resumed string
	Swarm   bool
}

// ChatAttach is the session, once it is made: the host, what to say about it, the log to follow and the commands to complete. A
// session that could not be made is Err, and ends the program with it.
type ChatAttach struct {
	Host ChatHost
	Info ChatInfo
	// Events is the session's log, live (events.Log.Subscribe): what the status line, the stack bar and the sparkline are drawn
	// from, through state.State. Nil shows none of them.
	Events <-chan events.Event
	// Commands are the slash commands the prompt completes; Root is where @paths are completed from.
	Commands []input.Command
	Root     string
	// Models is what `/model ` completes to (the catalogues of the providers, favorites first); it answers from memory and may be empty
	// while the catalogues are still being fetched. Nil offers nothing.
	Models func() []input.Choice
	Err    error
}

// ChatConfig is what the chat program is made of. Everything that touches the world is given: the screen, the keys, the size
// of the window, the tick of the clock, the session (Attach) and what the session says while it works (Link). The program reads
// no clock of its own, opens no file and starts no goroutine but the ones a turn and a slash command run on, so a test drives
// it with the channels it owns and looks at the screen it drew.
type ChatConfig struct {
	Screen ChatScreen
	// Keys, Sizes, Tick and Attach are the events the program reacts to. A closed Keys ends the program (the terminal is gone),
	// a closed or nil Sizes, Tick or Attach is never heard from again. A nil Attach means no session ever comes.
	Keys   <-chan input.Key
	Sizes  <-chan term.Size
	Tick   <-chan time.Time
	Attach <-chan ChatAttach
	// Bell, when set, is called as a question appears: a person who is in another window hears or sees it in the terminal's tab.
	Bell func()
	// Link carries the session's sink and prompter to the program. Required.
	Link *ChatLink
	// Now is the clock (nil: the time of the latest tick). The elapsed time of a turn, the two seconds in which a second Ctrl-C
	// quits and the pause before a question takes an answer are measured on it.
	Now func() time.Time
	// Look is how to draw; MainAgent is the agent whose words are the answer ("" is every agent), Verbose shows the notices of
	// level info.
	Look      Look
	MainAgent string
	Verbose   bool
	// History is where the prompts are remembered (nil: in memory only).
	History *input.History
	// CancelStart cancels the making of the session, for Ctrl-C and Esc while it is being made.
	CancelStart func()
	// QuitWindow is how soon after a first Ctrl-C at an empty prompt a second one quits (default two seconds). AnswerAfter is how
	// long the keyboard must have been quiet before a permission question takes an answer from it (default 350 ms; negative:
	// no pause): keys typed before that belong to the prompt, so that a line typed ahead can never answer a question.
	QuitWindow  time.Duration
	AnswerAfter time.Duration
}

// ChatEnd is how the chat ended, in the words the session's end reasons use.
type ChatEnd string

// The ways a chat ends.
const (
	// ChatQuit: the person left (/exit, Ctrl-D, the keys ended).
	ChatQuit ChatEnd = "exit"
	// ChatInterrupted: a second Ctrl-C at the prompt, or the process was told to stop.
	ChatInterrupted ChatEnd = "interrupted"
	// ChatFailed: the session could not be made, or the screen failed.
	ChatFailed ChatEnd = "error"
)

// QuitHint is what a first Ctrl-C at the prompt says, in the chat on a terminal and in the plain one (cmd/sleipnir/chat.go): the
// same line, word for word, since it has always said it. A second press within the window quits (ChatConfig.QuitWindow, which is
// two seconds unless a test says otherwise).
const QuitHint = "(press Ctrl-C again within 2 seconds to quit, or type /exit)"
