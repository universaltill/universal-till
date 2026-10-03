package logging

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// firstUseOK is printed by the child only after its checks pass, so a child
// that exits 0 without running them (a -test.run filter that stopped
// matching after a rename) cannot pass vacuously.
const firstUseOK = "ut-first-use-ok"

// firstUseRuns is how many fresh child processes repeat the check. One
// concurrent first use per process is all there is, and a non-singleton
// Init only shows up when the goroutines actually interleave, so a single
// child misses a broken once.Do often enough (~15% measured) to matter.
const firstUseRuns = 5

// L() must be safe to call from many goroutines before the logger is
// initialised: the recover path reaches the global logger from every
// background goroutine (ut-docs#3410). A bare `defaultLogger == nil` check
// outside once.Do races with the write inside it.
//
// Run in a subprocess (ut-docs#3525): resetting the package-level `once` in
// this process to retest first use is itself the same unsynchronised-write
// hazard #3513 fixed for defaultLogger. A fresh child process starts with
// real, never-touched `once`/`defaultLogger` zero state, so there is
// nothing to reset and nothing to race — and it is a truer test of "first
// use" than resetting shared state ever was. The child is the same
// (race-instrumented, under -race) test binary; a data race makes it exit
// 66 even after os.Exit(0), so the parent's exit check covers it.
func TestLConcurrentFirstUseIsRaceFree(t *testing.T) {
	if os.Getenv("UT_TEST_CONCURRENT_FIRST_USE") != "" {
		const n = 16
		start := make(chan struct{})
		got := make([]*Logger, n)
		var wg sync.WaitGroup
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				got[i] = L()
			}(i)
		}
		close(start)
		wg.Wait()

		for i, l := range got {
			if l == nil {
				fmt.Fprintf(os.Stderr, "goroutine %d got a nil logger\n", i)
				os.Exit(1)
			}
			if l != got[0] {
				fmt.Fprintf(os.Stderr, "goroutine %d got a different logger instance\n", i)
				os.Exit(1)
			}
		}
		fmt.Println(firstUseOK)
		os.Exit(0)
	}

	for run := 0; run < firstUseRuns; run++ {
		cmd := exec.Command(os.Args[0], "-test.run=^TestLConcurrentFirstUseIsRaceFree$")
		cmd.Env = append(os.Environ(),
			"UT_TEST_CONCURRENT_FIRST_USE=1",
			// Keep the caller's race options; only skip the race
			// runtime's default 1s sleep at exit, which would otherwise
			// cost a second per child.
			"GORACE="+os.Getenv("GORACE")+" atexit_sleep_ms=0")
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("child run %d: %v\nstdout: %s\nstderr: %s", run, err, stdout.String(), stderr.String())
		}
		if !strings.Contains(stdout.String(), firstUseOK) {
			t.Fatalf("child run %d exited 0 without running the check\nstdout: %s\nstderr: %s", run, stdout.String(), stderr.String())
		}
	}
}
