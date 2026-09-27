package main

import (
	"errors"
	"testing"
	"time"
)

// TestWaitForChildReadyReturnsAsSoonAsTheServerAnswers covers the ordinary
// launch: the spawned server binds quickly and the shell opens the window
// without polling any further.
func TestWaitForChildReadyReturnsAsSoonAsTheServerAnswers(t *testing.T) {
	probes, sleeps := 0, 0
	exited := make(chan error, 1)
	got, err := waitForChild(func() bool { probes++; return probes == 3 }, exited, 100, 100*time.Millisecond, func(time.Duration) { sleeps++ })
	if got != childReady || err != nil {
		t.Fatalf("waitForChild = %d, %v; want childReady, nil", got, err)
	}
	if probes != 3 || sleeps != 2 {
		t.Fatalf("probes=%d sleeps=%d; want 3 probes and 2 sleeps", probes, sleeps)
	}
}

// TestWaitForChildStopsWhenTheServerExitsEarly is ut-docs#2760: a server that
// exits before it binds (e.g. db.ErrDataDirLocked because an orphaned
// unitill-pos still owns the data directory) used to be polled for the full
// ~10s with nothing recorded. The shell must notice the exit on the next
// probe and hand back the child's exit error so it can be logged.
func TestWaitForChildStopsWhenTheServerExitsEarly(t *testing.T) {
	exitErr := errors.New("exit status 1")
	exited := make(chan error, 1)
	probes := 0
	got, err := waitForChild(func() bool {
		probes++
		if probes == 2 {
			exited <- exitErr // the child dies while the shell is waiting
		}
		return false
	}, exited, 100, 100*time.Millisecond, func(time.Duration) {})
	if got != childExited {
		t.Fatalf("waitForChild = %d; want childExited", got)
	}
	if !errors.Is(err, exitErr) {
		t.Fatalf("err = %v; want the child's exit error", err)
	}
	if probes != 2 {
		t.Fatalf("probes = %d; want the wait to stop right after the exit (2)", probes)
	}
}

// TestWaitForChildReportsACleanEarlyExit: a child that exits 0 before binding
// is still an early exit (nil error), not "ready".
func TestWaitForChildReportsACleanEarlyExit(t *testing.T) {
	exited := make(chan error, 1)
	exited <- nil
	got, err := waitForChild(func() bool { return false }, exited, 100, time.Millisecond, func(time.Duration) {})
	if got != childExited || err != nil {
		t.Fatalf("waitForChild = %d, %v; want childExited, nil", got, err)
	}
}

// TestWaitForChildTimesOutAfterTheAttemptBudget keeps today's behaviour for a
// server that is alive but slow: give up after the attempt budget and let the
// window open anyway (it shows the till once the server answers).
func TestWaitForChildTimesOutAfterTheAttemptBudget(t *testing.T) {
	probes, sleeps := 0, 0
	exited := make(chan error, 1)
	got, err := waitForChild(func() bool { probes++; return false }, exited, 5, time.Millisecond, func(time.Duration) { sleeps++ })
	if got != childTimedOut || err != nil {
		t.Fatalf("waitForChild = %d, %v; want childTimedOut, nil", got, err)
	}
	if probes != 5 || sleeps != 5 {
		t.Fatalf("probes=%d sleeps=%d; want 5 and 5", probes, sleeps)
	}
}
