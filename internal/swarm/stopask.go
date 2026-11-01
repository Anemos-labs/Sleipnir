package swarm

import (
	"strings"
	"unicode"
)

// Stopping the team on the person's word.
//
// A person who tells a team's manager to stop ("stop all your agents", "STOP ALL TASKS", "stop") means it now, for every agent,
// and the manager is the last one to decide that: in a long run it read the request as one more task, spawned, nudged idle
// workers back to work, and the guards that keep a manager from abandoning work sent it back each time it tried to answer. A
// message the harness recognizes as a stop request is therefore not given to the manager to act on: the host stops the whole
// team (StopAll, through session.StopTeam), the manager is told what happened, and so is the person.
//
// StopRequest is deliberately narrow. It takes a message that is only a stop ("stop", "halt", "stop now"), or one with a
// sentence whose verb is stop, halt, kill, cancel, abort or terminate applied to the team as a whole (all, every, the agents,
// workers, tasks, team, everything, everyone, spawning). A sentence that negates the verb ("don't stop the agents"), names what
// to stop doing ("stop the agents from writing tests", "stop using mocks"), or stops one agent by name is not one: those are
// steering, for the manager.

// stopVerbs are the verbs of a stop request.
var stopVerbs = map[string]bool{"stop": true, "halt": true, "kill": true, "cancel": true, "abort": true, "terminate": true}

// stopAlone are whole messages that are a stop request by themselves.
var stopAlone = map[string]bool{
	"stop": true, "halt": true, "abort": true, "stop now": true, "stop it": true, "stop it now": true, "stop stop": true,
	"stop please": true, "please stop": true, "stop all": true, "stop all now": true, "halt all": true, "cancel all": true,
	"kill all": true, "abort all": true, "stop everything": true, "cancel everything": true, "kill everything": true,
	"stop right now": true, "stop immediately": true,
}

// stopFiller are the words between the verb and what it stops: "stop all of your agents".
var stopFiller = map[string]bool{
	"all": true, "every": true, "each": true, "the": true, "your": true, "my": true, "our": true, "of": true, "those": true,
	"these": true, "remaining": true, "running": true, "other": true, "active": true, "current": true, "ongoing": true, "this": true,
	"that": true, "whole": true, "entire": true, "now": true, "please": true, "them": true,
}

// stopObjects are what a stop request stops: the team as a whole.
var stopObjects = map[string]bool{
	"agents": true, "workers": true, "tasks": true, "jobs": true, "runs": true, "subagents": true, "team": true, "swarm": true,
	"everything": true, "everyone": true, "everybody": true, "work": true, "spawning": true, "spawns": true, "teammates": true,
	"members": true, "all": true,
}

// stopNegations before the verb turn a stop request into its opposite.
var stopNegations = map[string]bool{
	"dont": true, "not": true, "never": true, "didnt": true, "doesnt": true, "wont": true, "cant": true, "cannot": true,
	"shouldnt": true, "without": true, "no": true,
}

// stopModals before the verb make it a description, not a request: "the server can stop all tasks".
var stopModals = map[string]bool{"can": true, "could": true, "should": true, "would": true, "will": true, "must": true, "might": true, "may": true}

// stopQualifiers after what is stopped narrow it to one part of the work: "stop the agents from writing tests".
var stopQualifiers = map[string]bool{
	"from": true, "when": true, "if": true, "after": true, "before": true, "once": true, "until": true, "unless": true, "that": true,
	"which": true, "who": true, "on": true, "in": true, "for": true, "with": true, "using": true, "doing": true, "writing": true,
	"working": true, "touching": true, "editing": true, "changing": true, "except": true, "but": true, "to": true,
}

// StopRequest reports whether text asks to stop the whole team now (see the head of this file).
func StopRequest(text string) bool {
	if len(text) > 600 {
		return false // a long message is instructions, for the manager
	}
	sentences := stopSentences(text)
	var all []string
	for _, w := range sentences {
		all = append(all, w...)
	}
	if stopAlone[strings.Join(all, " ")] {
		return true
	}
	for _, words := range sentences {
		if stopAlone[strings.Join(words, " ")] {
			return true
		}
		for i, w := range words {
			if !stopVerbs[w] {
				continue
			}
			if negated(words[:i]) || i > 0 && stopModals[words[i-1]] {
				continue // "don't stop the agents"; "the server can stop all tasks" describes, it does not ask
			}
			if i+1 == len(words)-1 && words[i+1] == "working" {
				return true // "stop working"
			}
			j := i + 1
			for j < len(words) && stopFiller[words[j]] {
				j++
			}
			switch {
			case j < len(words) && stopObjects[words[j]]:
			case j == len(words) && j > i+1 && words[j-1] == "all": // "stop them all", "stop all"
			default:
				continue
			}
			if j+1 < len(words) && stopQualifiers[words[j+1]] {
				continue
			}
			return true
		}
	}
	return false
}

// negated reports whether the words before a verb negate it: one of the two last is a negation ("don't stop", "do not stop").
func negated(before []string) bool {
	for k := max(len(before)-2, 0); k < len(before); k++ {
		if stopNegations[before[k]] {
			return true
		}
	}
	return false
}

// stopSentences splits text into sentences of lowercase words: letters and digits only, apostrophes dropped ("don't" is "dont").
func stopSentences(text string) [][]string {
	var out [][]string
	var words []string
	var w strings.Builder
	flushWord := func() {
		if w.Len() > 0 {
			words = append(words, w.String())
			w.Reset()
		}
	}
	flushSentence := func() {
		flushWord()
		if len(words) > 0 {
			out = append(out, words)
			words = nil
		}
	}
	for _, r := range text {
		switch {
		case r == '\'' || r == '’':
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			w.WriteRune(unicode.ToLower(r))
		case r == '.' || r == '!' || r == '?' || r == ';' || r == '\n' || r == ',' || r == ':':
			flushSentence()
		default:
			flushWord()
		}
	}
	flushSentence()
	return out
}
