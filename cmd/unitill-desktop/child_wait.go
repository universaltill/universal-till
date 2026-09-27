package main

import "time"

// childStartOutcome is how waiting for the spawned unitill-pos ended.
type childStartOutcome int

const (
	childReady    childStartOutcome = iota // it accepted a connection
	childExited                            // it exited before it ever did
	childTimedOut                          // still alive, still not listening
)

// waitForChild polls ready() up to attempts times, interval apart, and stops
// early when the child exits (ut-docs#2760). Before this, a server that died
// on startup — typically db.ErrDataDirLocked because an orphaned unitill-pos
// still owned the data directory — was polled for the whole ~10s budget and
// the shell recorded nothing, so desktop.log showed no reason for the empty
// window. exited carries cmd.Wait()'s result; the returned error is the
// child's exit error for childExited and nil otherwise.
//
// Pure logic — the probe, the exit channel and sleep are injected — so it is
// tested in the untagged build (the desktop-tagged main is never run by
// `go test` in CI).
func waitForChild(ready func() bool, exited <-chan error, attempts int, interval time.Duration, sleep func(time.Duration)) (childStartOutcome, error) {
	for range attempts {
		select {
		case err := <-exited:
			return childExited, err
		default:
		}
		if ready() {
			return childReady, nil
		}
		sleep(interval)
	}
	select {
	case err := <-exited:
		return childExited, err
	default:
		return childTimedOut, nil
	}
}
