package main

// The crew fixture of `sleipnir web --fixture crew`: a manager and a team of 32 workers, four of each built-in role, two of each role on
// the model mock-1 and two on mock-2 (one --role-model per slot, as the New session dialog sends a team whose slots run different
// models). The manager hands each worker a task; a worker waits a few seconds, writes its notes when its role edits files, and hands
// the task in. It shows a large team on the page: the shared legs of the horse, the stalls, the gantt and the board at that size.

import (
	"fmt"
	"path/filepath"
	"slices"

	"github.com/anemos-labs/sleipnir/internal/config"
	"github.com/anemos-labs/sleipnir/internal/provider/mock"
	"github.com/anemos-labs/sleipnir/internal/swarm"
)

// crewPerRole is the number of workers of each role in the crew fixture.
const crewPerRole = 4

// crewEditors are the roles of the crew that write a file of notes; the others only report.
var crewEditors = []string{"backend", "frontend", "devops", "tester", "docs"}

// crew makes the scenario of the team of 32.
func (fx *webFixture) crew() (fixtureScenario, error) {
	project := filepath.Join(fx.dir, "crew")
	if err := writeProject(project, map[string]string{"AGENTS.md": "# Crew\n\nEach worker keeps its notes in notes/<its id>.md.\n", "notes/README.md": "# Notes\n"}, true); err != nil {
		return fixtureScenario{}, err
	}
	type job struct{ role, id, task string }
	var jobs []job
	roles := swarm.BuiltinRoles()
	for _, r := range swarm.WorkerRoleOrder {
		for i := 1; i <= crewPerRole; i++ {
			jobs = append(jobs, job{role: r, id: fmt.Sprintf("%s-%d", roles[r].Short, i), task: fmt.Sprintf("T%d", len(jobs)+1)})
		}
	}
	taskOf := map[string]job{}
	for _, j := range jobs {
		taskOf[j.id] = j
	}
	var st steps
	base := fx.serve(project, func(c *mock.Call) mock.Reply {
		if isJudge(c) {
			return judgeDone
		}
		if isCompactor(c) {
			return mock.Reply{Text: `{"keep_from":"t1","spine":[],"mask":[],"notes":[],"promote":[]}`}
		}
		id, role := whoIs(c)
		n := st.next(id)
		if role == "manager" {
			switch n {
			case 0:
				var calls []mock.ToolCall
				var all []string
				for k, j := range jobs {
					args := map[string]any{"action": "create", "title": fmt.Sprintf("%s notes, part %d", j.role, k%crewPerRole+1), "role": j.role}
					if slices.Contains(crewEditors, j.role) {
						args["files"] = []string{"notes/" + j.id + ".md"}
					}
					calls = append(calls, fixtureCall(fmt.Sprintf("t%d", k+1), "task", args))
					all = append(all, j.task)
				}
				for k, j := range jobs {
					calls = append(calls, fixtureCall(fmt.Sprintf("s%d", k+1), "spawn", map[string]any{"role": j.role, "task": j.task}))
				}
				calls = append(calls, fixtureCall("w1", "wait", map[string]any{"until": all, "timeout_sec": 600}))
				return mock.Reply{Text: "Thirty-two workers, four of each role, each with a part of the notes; two of each role plan on one model and two on the other.", ToolCalls: calls}
			case 1:
				var calls []mock.ToolCall
				for k, j := range jobs {
					calls = append(calls, fixtureCall(fmt.Sprintf("a%d", k+1), "task", map[string]any{"action": "accept", "id": j.task}))
				}
				return mock.Reply{Text: "Every part is in.", ToolCalls: calls}
			}
			return mock.Reply{Text: "The crew wrote its notes."}
		}
		j, ok := taskOf[id]
		if !ok {
			return mock.Reply{Text: "nothing to do"}
		}
		k := slices.IndexFunc(jobs, func(x job) bool { return x.id == id })
		switch n {
		case 0:
			return mock.Reply{Text: "Thinking about my part.", ToolCalls: []mock.ToolCall{fixtureCall("z"+id, "bash", map[string]any{"command": fmt.Sprintf("sleep %d", 8+k%9)})}}
		case 1:
			if slices.Contains(crewEditors, role) {
				return mock.Reply{Text: "Writing my notes.", ToolCalls: []mock.ToolCall{fixtureCall("w"+id, "write", map[string]any{"path": "notes/" + id + ".md", "content": "# " + id + "\n\nMy part is done.\n"})}}
			}
			return mock.Reply{Text: "Checking once more.", ToolCalls: []mock.ToolCall{fixtureCall("y"+id, "bash", map[string]any{"command": fmt.Sprintf("sleep %d", 3+k%4)})}}
		case 2:
			return mock.Reply{Text: "Handing it in.", ToolCalls: []mock.ToolCall{fixtureCall("d"+id, "task", map[string]any{"action": "done", "id": j.task, "text": "my part of the notes"})}}
		}
		return mock.Reply{Text: "Done."}
	}, nil)
	url := fx.servers[len(fx.servers)-1].URL
	base.Config.Providers = map[string]config.Provider{"mock": {BaseURL: url + "/v1"}} // the slots' models resolve to this server
	args := []string{"--cwd", project, "--mode", "accept-edits", "--isolation", "none", "--auto-docs=false", "--allow", "Bash(sleep:*)"}
	for k, j := range jobs {
		m := "mock/mock-1"
		if k%crewPerRole >= crewPerRole/2 {
			m = "mock/mock-2"
		}
		args = append(args, "--role-model", j.role+"="+m)
	}
	return fixtureScenario{name: "crew", project: project, base: base, args: args, goal: "Write the crew's notes: one part for each worker"}, nil
}
