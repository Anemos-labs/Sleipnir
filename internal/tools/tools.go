// Package tools defines the tool contract shared by all built-in and external
// (MCP) tools, plus the pieces every tool leans on: output truncation with
// recall handles, and cross-agent file state for staleness checks.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/reee344/sleipnir/internal/core"
	"github.com/reee344/sleipnir/internal/events"
	"github.com/reee344/sleipnir/internal/perm"
)

// Tool is one capability offered to models.
//
// Every agent in a session is offered the same tool list (identical bytes are
// what lets the provider cache the tool schemas once for the whole swarm), so
// role restrictions are enforced at run time through permissions and Guard,
// never by hiding tools.
type Tool interface {
	Spec() core.ToolSpec
	// Run executes the call. Errors that the model can act on (bad arguments,
	// file not found, command failed) are returned as Result{IsError: true};
	// a non-nil error means the harness itself failed.
	Run(ctx context.Context, c *Call) (*Result, error)
}

// Call is one tool invocation.
type Call struct {
	ID    string
	Name  string
	Input json.RawMessage
	Env   *Env
}

// Result is what a tool returns to the model (and the UI).
type Result struct {
	// Text is the model-visible output, already truncated to Env.Limits.
	Text    string
	IsError bool
	// Blocks are extra model-visible blocks (images).
	Blocks []core.Block
	// FullRef is the blob holding the complete output when Text was truncated;
	// Handle is the short id the model can pass to the recall tool.
	FullRef core.Hash
	Handle  string
	// FullChars is the length of the complete output FullRef holds.
	FullChars int
	Truncated bool
	// Meta carries structured details for logs and UIs (exit code, diff, ...).
	Meta map[string]any
}

// Errorf builds a model-visible error result.
func Errorf(format string, args ...any) *Result {
	return &Result{Text: fmt.Sprintf(format, args...), IsError: true}
}

// Guard lets the swarm layer veto or observe writes (file leases, ownership).
type Guard interface {
	// BeforeWrite is called before an agent modifies path. A non-nil error is
	// shown to the model verbatim and the write does not happen.
	BeforeWrite(agent, path string) error
	AfterWrite(agent, path string)
}

// NoGuard permits everything.
type NoGuard struct{}

func (NoGuard) BeforeWrite(string, string) error { return nil }
func (NoGuard) AfterWrite(string, string)        {}

// Snapshotter records a file's prior state before it is modified so edits can
// be rewound (checkpoints).
type Snapshotter interface {
	Before(agent, path string) error
}

// Limits bound tool output and work.
type Limits struct {
	// MaxOutputChars truncates model-visible output (head + tail kept).
	MaxOutputChars int
	// MaxReadBytes refuses to read files larger than this without a range.
	MaxReadBytes int64
	// DefaultTimeout / MaxTimeout apply to shell commands.
	DefaultTimeout time.Duration
	MaxTimeout     time.Duration
}

// DefaultLimits are conservative defaults: 24k chars is about 6k tokens.
func DefaultLimits() Limits {
	return Limits{
		MaxOutputChars: 24_000,
		MaxReadBytes:   4 << 20,
		DefaultTimeout: 2 * time.Minute,
		MaxTimeout:     10 * time.Minute,
	}
}

// Env is the execution context handed to tools. It is per agent.
type Env struct {
	Agent string
	Role  string
	// Cwd is the agent's working directory (a worktree in isolated mode);
	// Root is the project root permissions are scoped to.
	Cwd  string
	Root string

	Files   *FileState
	Guard   Guard
	Perm    perm.Requester
	Snap    Snapshotter
	Blobs   events.Blobs
	Emit    events.Emitter
	Handles *Handles

	// Out streams live output to the UI as a tool runs (stream is "stdout",
	// "stderr" or "info").
	Out func(stream, text string)

	Limits Limits
	Now    func() time.Time
}

// Defaults fills unset fields with safe no-op values.
func (e *Env) Defaults() *Env {
	if e.Files == nil {
		e.Files = NewFileState()
	}
	if e.Guard == nil {
		e.Guard = NoGuard{}
	}
	if e.Perm == nil {
		e.Perm = perm.DenyAll{} // fail closed: see perm.DenyAll
	}
	if e.Emit == nil {
		e.Emit = events.Discard{}
	}
	if e.Handles == nil {
		e.Handles = NewHandles()
	}
	if e.Out == nil {
		e.Out = func(string, string) {}
	}
	if e.Limits == (Limits{}) {
		e.Limits = DefaultLimits()
	}
	if e.Now == nil {
		e.Now = time.Now
	}
	return e
}

// Registry holds tools by name.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry { return &Registry{tools: map[string]Tool{}} }

// Register adds a tool, replacing any tool with the same name.
func (r *Registry) Register(t Tool) {
	r.mu.Lock()
	r.tools[t.Spec().Name] = t
	r.mu.Unlock()
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// Names returns the registered tool names, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	out := make([]string, 0, len(r.tools))
	for n := range r.tools {
		out = append(out, n)
	}
	r.mu.RUnlock()
	sort.Strings(out)
	return out
}

// Specs returns tool specs sorted by name with canonical schemas: the exact
// list every agent sends.
func (r *Registry) Specs() ([]core.ToolSpec, error) {
	r.mu.RLock()
	out := make([]core.ToolSpec, 0, len(r.tools))
	for _, t := range r.tools {
		s := t.Spec()
		cs, err := core.Canonical(s.InputSchema)
		if err != nil {
			r.mu.RUnlock()
			return nil, fmt.Errorf("tool %s: %w", s.Name, err)
		}
		s.InputSchema = cs
		out = append(out, s)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
