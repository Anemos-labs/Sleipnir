package session

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/anemos-labs/sleipnir/internal/delivery"
	"github.com/anemos-labs/sleipnir/internal/perm"
	"github.com/anemos-labs/sleipnir/internal/skills/mdfile"
)

// SetDelivery selects automation for the next objective. Hosts serialize this
// with turns so queued messages cannot change the current objective's authority.
func (s *Session) SetDelivery(fix, merge bool) {
	s.mu.Lock()
	s.deliveryOptions = delivery.Options{AutoFix: fix, AutoMerge: merge}
	s.mu.Unlock()
}

// DeliveryCommand configures the next objective or explicitly resumes a paused
// workflow. The host executes commands between turns.
func (s *Session) DeliveryCommand(arg string) string {
	f := strings.Fields(arg)
	if len(f) == 0 {
		return "usage: /delivery fix|merge|both|off|resume (applies to one objective)"
	}
	switch f[0] {
	case "fix":
		s.SetDelivery(true, false)
	case "merge":
		s.SetDelivery(false, true)
	case "both":
		s.SetDelivery(true, true)
	case "off":
		s.SetDelivery(false, false)
		c, err := delivery.Load(filepath.Join(s.Dir, "delivery.json"), s.deliveryExec(s.opts.Cwd))
		if err != nil {
			return err.Error()
		}
		if c.State.Objective != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := c.Pause(ctx); err != nil {
				return err.Error()
			}
		}
		s.delivery = nil
	case "resume":
		c, err := delivery.Load(filepath.Join(s.Dir, "delivery.json"), s.deliveryExec(s.opts.Cwd))
		if err != nil {
			return err.Error()
		}
		if c.State.Objective == "" || c.State.Phase == "merged" || c.State.Phase == "ready" {
			return "no delivery workflow to resume"
		}
		s.delivery = c
		s.SetDelivery(c.State.AutoFix, c.State.AutoMerge)
		return "delivery restored; send a message to continue the saved objective"
	default:
		return "usage: /delivery fix|merge|both|off|resume"
	}
	return "GitHub automation set for the next objective: " + f[0]
}

// Run executes a turn and, when explicitly enabled, the durable GitHub lifecycle.
// Waiting on remote checks consumes no model requests. The process owns the loop,
// so disconnecting a browser does not cancel it; cancellation pauses it.
func (s *Session) Run(ctx context.Context, objective string) (*Result, error) {
	s.mu.Lock()
	o := s.deliveryOptions
	s.deliveryOptions = delivery.Options{}
	s.mu.Unlock()
	enabled := o.AutoFix || o.AutoMerge
	if !enabled {
		return s.runTurn(ctx, objective)
	}
	dir := s.opts.Cwd
	if s.Swarm != nil {
		dir = s.Swarm.ManagerWorkdir()
	}
	c := s.delivery
	if c == nil {
		var err error
		c, err = delivery.Load(filepath.Join(s.Dir, "delivery.json"), s.deliveryExec(dir))
		if err != nil {
			return nil, err
		}
		if c.State.MergeEnabled {
			if err := c.Pause(ctx); err != nil {
				return nil, err
			}
		}
		if err := c.Start(objective, o); err != nil {
			return nil, err
		}
	} else {
		c.Exec = s.deliveryExec(dir)
		c.State.Options = o
		objective = c.State.Objective
	}
	s.delivery = c
	completed := false
	defer func() {
		if !completed {
			if s.opts.Sink != nil {
				s.opts.Sink.Notice("", "delivery", "GitHub delivery paused; use /delivery resume, then send a message to continue the saved objective.")
			}
			pause, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := c.Pause(pause); err != nil && s.opts.Sink != nil {
				s.opts.Sink.Notice("", "warn", "Could not pause GitHub automation: "+err.Error())
			}
		}
		s.delivery = nil // subsequent objectives require fresh authorization or explicit resume
	}()
	prompt := objective + fmt.Sprintf("\n[GitHub delivery for this objective: auto-fix=%t, auto-merge=%t. Implement and verify the work, then directly commit, push and create or reuse its pull request. Inspect CI and reviews including CodeRabbit. Preserve the objective and repair existing tasks. The harness handles waiting and merging; do not enable auto-merge or merge through tools yourself.]", o.AutoFix, o.AutoMerge)
	result, err := s.runTurn(ctx, prompt)
	if err != nil {
		return result, err
	}
	if s.Swarm != nil {
		c.Exec = s.deliveryExec(s.Swarm.ManagerWorkdir())
	}
	for {
		if ctx.Err() != nil {
			return result, ctx.Err()
		}
		if s.Swarm != nil {
			if err := s.Swarm.BudgetError(); err != nil {
				return result, err
			}
		}
		if budget := s.Budget(); budget > 0 && s.Cost() >= budget {
			return result, fmt.Errorf("delivery paused: budget reached")
		}
		d, err := c.Step(ctx)
		if err != nil {
			c.State.Phase = "paused"
			_ = c.Save()
			return result, fmt.Errorf("delivery paused: %w", err)
		}
		s.Log.Emit("", "delivery.state", map[string]any{"phase": d.Phase, "pr": c.State.PR, "head": c.State.Head, "auto_fix": o.AutoFix, "auto_merge": o.AutoMerge})
		if s.opts.Sink != nil {
			s.opts.Sink.Notice("", "delivery", d.Phase+": "+c.State.PR)
		}
		if d.Done {
			completed = true
			return result, nil
		}
		if d.Prompt != "" {
			result, err = s.runTurn(ctx, d.Prompt)
			if err != nil {
				return result, err
			}
			if d.AssessRelease && result != nil {
				if err := c.RecordRelease(result.Text); err != nil {
					return result, err
				}
			}
			if d.AssessReview && result != nil {
				if err := c.RecordReview(result.Text); err != nil {
					return result, err
				}
			}
			continue
		}
		timer := time.NewTimer(d.Wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return result, ctx.Err()
		case <-timer.C:
		}
	}
}

// deliveryExec applies explicit session restrictions to objective-authorized GitHub
// commands. Checkbox authority covers the default ask policy, never a deny rule.
func (s *Session) deliveryExec(dir string) delivery.Exec {
	run := delivery.Command(dir)
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		words := []string{name}
		for _, arg := range args {
			words = append(words, mdfile.ShellQuote(arg))
		}
		command := strings.Join(words, " ")
		if s.Perm != nil {
			req := perm.Request{Agent: s.mainAgent(), Role: "manager", Tool: "bash", Command: command, Cwd: dir, Writes: len(args) > 1 && args[0] == "pr" && args[1] == "merge"}
			c := s.Perm.Classify(req)
			if c.Verdict == perm.Deny && !c.NoOneToAsk {
				return nil, fmt.Errorf("delivery permission denied: %s", c.Why)
			}
			if c.Verdict != perm.Allow && c.Rule != "" {
				if d := s.Perm.Check(ctx, req); !d.Allow {
					return nil, fmt.Errorf("delivery permission denied: %s", d.Reason)
				}
			}
		}
		return run(ctx, name, args...)
	}
}
