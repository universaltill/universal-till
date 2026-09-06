package procrestart

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

// These tests mutate the package-level seams (osExecutable, reexecFn,
// reexecDelay) — the same hermetic convention internal/selfupdate's own
// tests use — so a test can observe the scheduled re-exec WITHOUT ever
// letting a real syscall.Exec replace the test binary mid-run.

func stubSeams(t *testing.T) (reexecd chan string) {
	t.Helper()
	reexecd = make(chan string, 4)
	oldExec, oldReexec, oldDelay := osExecutable, reexecFn, reexecDelay
	osExecutable = func() (string, error) { return "/fake/unitill-pos", nil }
	reexecFn = func(path string) error { reexecd <- path; return nil }
	reexecDelay = 5 * time.Millisecond
	t.Cleanup(func() {
		osExecutable, reexecFn, reexecDelay = oldExec, oldReexec, oldDelay
	})
	return reexecd
}

// Supported() is purely the build-tag decision (reexec_unix.go vs
// reexec_windows.go): a test can only assert it portably against the GOOS
// the test binary is actually running on.
func TestSupportedMatchesGOOS(t *testing.T) {
	want := runtime.GOOS != "windows"
	if got := Supported(); got != want {
		t.Fatalf("Supported() on %s = %v, want %v", runtime.GOOS, got, want)
	}
}

// Restart() must return immediately (the caller is an HTTP handler whose
// response has to flush before the process image is replaced) and only
// AFTER reexecDelay hand the running executable's path to reexecFn.
func TestRestartSchedulesDelayedReexecOfOwnExecutable(t *testing.T) {
	reexecd := stubSeams(t)

	start := time.Now()
	Restart()
	if time.Since(start) >= reexecDelay {
		t.Fatalf("Restart() blocked for %v — it must schedule, not wait", time.Since(start))
	}
	select {
	case <-reexecd:
		t.Fatal("re-exec fired synchronously inside Restart(); it must be delayed so the HTTP response can flush")
	default:
	}

	select {
	case exe := <-reexecd:
		if exe != "/fake/unitill-pos" {
			t.Fatalf("re-exec'd %q, want the running executable /fake/unitill-pos", exe)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("re-exec never fired after reexecDelay")
	}
}

// A re-exec failure is logged, never panicked: the goroutine has no caller
// left to return an error to, and a till mid-"restart" must not crash.
func TestRestartSurvivesReexecError(t *testing.T) {
	reexecd := stubSeams(t)
	old := reexecFn
	reexecFn = func(path string) error { reexecd <- path; return errors.New("boom") }
	t.Cleanup(func() { reexecFn = old })

	Restart()
	select {
	case <-reexecd:
	case <-time.After(2 * time.Second):
		t.Fatal("re-exec never attempted")
	}
	// Give the goroutine a moment to run its error path; a panic there would
	// take the whole test binary down, which is the assertion.
	time.Sleep(20 * time.Millisecond)
}

// SetBeforeRestart's hook must run CONCURRENTLY with Restart's flush-delay
// sleep (started immediately, not after the sleep) but must still be fully
// complete strictly BEFORE reexecFn runs — it exists so a caller
// (internal/app) can cleanly stop hardware-plugin child processes that an
// exec of this process alone would never reach (ut-docs#1616) — and
// Restart() must still return immediately regardless, same as every other
// Restart() caller.
func TestRestartRunsBeforeRestartHookBeforeReexec(t *testing.T) {
	reexecd := stubSeams(t)
	t.Cleanup(func() { SetBeforeRestart(nil) })

	var order []string
	hookRan := make(chan struct{})
	SetBeforeRestart(func(ctx context.Context) {
		order = append(order, "hook")
		close(hookRan)
	})
	oldReexec := reexecFn
	reexecFn = func(path string) error {
		order = append(order, "reexec")
		reexecd <- path
		return nil
	}
	t.Cleanup(func() { reexecFn = oldReexec })

	start := time.Now()
	Restart()
	if time.Since(start) >= reexecDelay {
		t.Fatalf("Restart() blocked for %v — it must schedule, not wait", time.Since(start))
	}

	select {
	case <-hookRan:
	case <-time.After(2 * time.Second):
		t.Fatal("beforeRestart hook never ran")
	}
	select {
	case <-reexecd:
	case <-time.After(2 * time.Second):
		t.Fatal("reexecFn never ran")
	}

	if len(order) != 2 || order[0] != "hook" || order[1] != "reexec" {
		t.Fatalf("call order = %v, want [hook reexec]", order)
	}
}

// The hook must START concurrently with the flush-delay sleep, not after it
// — a silent regression back to sequential (hook only starts once the sleep
// ends) would reintroduce the additive-latency bug this design fixed
// (ut-docs#1616 review finding: web/ui/partials/pairing_wait.html times its
// first health probe off reexecDelay alone) without TestRestartRunsBefore
// RestartHookBeforeReexec catching it — that test only proves ordering
// (hook fully done before reexec), not that the hook started early.
func TestRestartHookStartsConcurrentlyNotAfterSleep(t *testing.T) {
	reexecd := stubSeams(t)
	t.Cleanup(func() { SetBeforeRestart(nil) })
	reexecDelay = 200 * time.Millisecond

	hookStarted := make(chan time.Time, 1)
	SetBeforeRestart(func(context.Context) {
		hookStarted <- time.Now()
	})

	start := time.Now()
	Restart()

	select {
	case startedAt := <-hookStarted:
		if elapsed := startedAt.Sub(start); elapsed >= reexecDelay {
			t.Fatalf("beforeRestart hook started %v after Restart() — it must start "+
				"concurrently with the flush delay, not after it", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("beforeRestart hook never started")
	}

	select {
	case <-reexecd:
	case <-time.After(2 * time.Second):
		t.Fatal("reexecFn never ran")
	}
}

// The default hook is a no-op: every other test in this package never calls
// SetBeforeRestart, so Restart() must work exactly as before when nothing
// has registered a hook.
func TestRestartDefaultBeforeRestartHookIsNoop(t *testing.T) {
	reexecd := stubSeams(t)
	Restart()
	select {
	case <-reexecd:
	case <-time.After(2 * time.Second):
		t.Fatal("re-exec never fired with the default (no-op) beforeRestart hook")
	}
}

// If the running executable can't even be located, nothing is scheduled —
// there is no path to exec — rather than a goroutine that fails later.
func TestRestartSkipsWhenExecutableUnknown(t *testing.T) {
	reexecd := stubSeams(t)
	old := osExecutable
	osExecutable = func() (string, error) { return "", errors.New("no exe") }
	t.Cleanup(func() { osExecutable = old })

	Restart()
	select {
	case exe := <-reexecd:
		t.Fatalf("re-exec fired with %q despite os.Executable failing", exe)
	case <-time.After(50 * time.Millisecond):
	}
}
