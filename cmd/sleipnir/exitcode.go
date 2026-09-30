package main

// Exit statuses beyond 0 (success), 1 (an error) and 2 (usage). A script can tell them apart without parsing text.
const (
	// exitUnfinished: the work ended with tasks left undone (a swarm whose manager stopped with unfinished
	// work). Whatever was done is in place; the run did not do what it was asked.
	exitUnfinished = 3
	// exitTempFail: nothing could be done now, and trying again later may work (EX_TEMPFAIL): a run in which no
	// rollout completed because the endpoint was down or the spend cap was reached. A script that resumes a run
	// loops on this status.
	exitTempFail = 75
)

// exitError is an error that ends the process with a status of its own instead of 1.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }
