package swarm

import "testing"

// StopRequest takes what a person types to stop a team, including the messages of a real run whose manager went on working after
// each of them, and leaves steering, questions and the stop of one agent to the manager.
func TestStopRequest(t *testing.T) {
	stops := []string{
		"stop all your agents",
		"What do you not understand. Im telling you the task is complete STOP your agents",
		"STOP ALL TASKS",
		"STOP",
		"stop!",
		"Stop.",
		"halt",
		"stop everything",
		"please stop all the workers now",
		"cancel all tasks",
		"kill all agents",
		"stop the team",
		"stop all of your agents and tell me where things stand",
		"ok, stop spawning",
		"Abort all",
		"stop them all",
		"The scan is done. Stop the agents.",
		"stop working",
		"stop all work",
		"don't do anything else, stop all agents",
		"can you stop all agents",
		"could you please stop all the agents?",
	}
	for _, s := range stops {
		if !StopRequest(s) {
			t.Errorf("StopRequest(%q) = false, want true", s)
		}
	}
	steering := []string{
		"I literally see agents running",
		"THERES STILL 29 AGENTS RUNNING",
		"don't stop the agents",
		"do not stop the workers, just fix the tests",
		"stop the agents from writing more tests",
		"stop using mocks in the tests",
		"stop the agent be-1",
		"stop be-1",
		"stop all agents except the reviewer",
		"stop working on the frontend",
		"why did the agents stop?",
		"the stop button does nothing",
		"add a stop button to the toolbar",
		"never stop the workers before the review",
		"make sure the server can stop all tasks cleanly",
		"pause all agents",
		"pause everything",
		"",
	}
	for _, s := range steering {
		if StopRequest(s) {
			t.Errorf("StopRequest(%q) = true, want false", s)
		}
	}
}
