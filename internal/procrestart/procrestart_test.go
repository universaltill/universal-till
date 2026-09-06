package procrestart

import (
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
