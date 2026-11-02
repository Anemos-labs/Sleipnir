package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/anemos-labs/sleipnir/internal/session"
	"github.com/anemos-labs/sleipnir/internal/tools"
	"github.com/anemos-labs/sleipnir/internal/tools/shell"
	"github.com/anemos-labs/sleipnir/internal/web/wire"
)

// jobsSlash is /jobs: the background jobs the agents started, each with the local addresses its output named, and the strays
// their finished commands left running; with `stop ID`, ends one of them and what it started; with `log ID`, prints a job's whole
// command and the end of its output.
func jobsSlash(w io.Writer, s *session.Session, args []string) {
	switch {
	case len(args) == 0:
		printJobs(w, s.Jobs(), s.Strays())
	case len(args) == 2 && strings.EqualFold(args[0], "stop"):
		if err := s.StopJob(args[1]); err != nil {
			fmt.Fprintln(w, "jobs:", tools.SanitizeForTerminal(err.Error()))
			return
		}
		fmt.Fprintln(w, "stopped", firstText(args[1], 40))
	case len(args) == 2 && strings.EqualFold(args[0], "log"):
		printJobLog(w, s, args[1])
	default:
		fmt.Fprintln(w, "usage: /jobs lists the background jobs and stray processes; /jobs stop ID ends one; /jobs log ID prints a job's output")
	}
}

// jobLogMax is the most of a job's output /jobs log prints: its end.
const jobLogMax = 64 << 10

// printJobLog writes a job's whole command and the end of its output (at most jobLogMax bytes), cleaned for a terminal.
func printJobLog(w io.Writer, s *session.Session, id string) {
	cmd, err := s.JobCommand(id)
	if err != nil {
		fmt.Fprintln(w, "jobs:", tools.SanitizeForTerminal(err.Error()))
		return
	}
	out, omitted, err := s.JobOutput(id, jobLogMax)
	if err != nil {
		fmt.Fprintln(w, "jobs:", tools.SanitizeForTerminal(err.Error()))
		return
	}
	fmt.Fprintf(w, "$ %s\n", tools.SanitizeForTerminal(cmd))
	if omitted > 0 {
		fmt.Fprintf(w, "[%d earlier bytes not shown]\n", omitted)
	}
	if out = strings.TrimRight(out, "\n"); out == "" {
		fmt.Fprintln(w, "(no output yet)")
		return
	}
	fmt.Fprintln(w, tools.SanitizeForTerminal(out))
}

// printJobs writes the job list: one line per job (id, state, agent, command), its addresses below it, running jobs first; then
// the strays, each with its live processes.
func printJobs(w io.Writer, js []shell.JobInfo, strays []shell.StrayInfo) {
	if len(js) == 0 && len(strays) == 0 {
		fmt.Fprintln(w, "no background jobs: an agent starts one with bash run_in_background (a dev server, a watcher, a long test run)")
		return
	}
	running := 0
	for _, j := range js {
		if j.Status == "running" {
			running++
		}
	}
	if len(js) > 0 {
		fmt.Fprintf(w, "background jobs: %d running, %d ended\n", running, len(js)-running)
	}
	for _, pass := range []bool{true, false} {
		for _, j := range js {
			if (j.Status == "running") != pass {
				continue
			}
			fmt.Fprintf(w, "  %-7s %-10s %-8s %s\n", firstText(j.ID, 12), jobState(j), firstText(j.Agent, 12), firstText(j.Command, 80))
			for _, u := range j.URLs {
				fmt.Fprintf(w, "          %s\n", firstText(u, 200))
			}
		}
	}
	if len(strays) > 0 {
		fmt.Fprintf(w, "left running by finished commands: %d\n", len(strays))
		for _, st := range strays {
			fmt.Fprintf(w, "  %-7s %-10s %-8s %s\n", firstText(st.ID, 12), "stray", firstText(st.Agent, 12), firstText(st.Command, 80))
			for _, p := range st.Procs {
				fmt.Fprintf(w, "          pid %d  %s\n", p.PID, firstText(p.Cmd, 120))
			}
		}
	}
	if len(js) > 0 {
		fmt.Fprintln(w, "read one's output: /jobs log ID")
	}
	if running > 0 || len(strays) > 0 {
		fmt.Fprintln(w, "stop one: /jobs stop ID")
	}
}

// jobState is a job's state in a word or two: running, exited 0, killed.
func jobState(j shell.JobInfo) string {
	if j.Status == "exited" && j.Exit != nil {
		return fmt.Sprintf("exited %d", *j.Exit)
	}
	return j.Status
}

// webJobs is the session's background jobs, then its strays, as the page's meta carries them (wire.Job); never nil, so that the
// last job forgotten is sent as an empty list.
func webJobs(s *session.Session) []wire.Job {
	out := []wire.Job{}
	for _, j := range s.Jobs() {
		out = append(out, wire.Job{ID: j.ID, Agent: j.Agent, Command: j.Command, Status: j.Status, Exit: j.Exit, Started: j.Started.UnixMilli(), URLs: j.URLs})
	}
	for _, st := range s.Strays() {
		wj := wire.Job{ID: st.ID, Agent: st.Agent, Command: st.Command, Status: "running", Started: st.Started.UnixMilli(), Kind: "stray"}
		for _, p := range st.Procs {
			wj.Procs = append(wj.Procs, wire.JobProc{PID: p.PID, Cmd: p.Cmd})
		}
		out = append(out, wj)
	}
	return out
}
