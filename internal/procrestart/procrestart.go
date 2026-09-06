// Package procrestart restarts the running till process in place — the same
// PID, the same argv and environment — after a short delay that lets the
// HTTP response which asked for it flush first.
//
// Why this exists (ut-docs#1550): joining a primary (internal/pages'
// completeJoin) stages a downloaded database snapshot via
// db.StageRestoreFromReader, and db.ApplyPendingRestore only ever runs once,
// before db.Open() at process startup (internal/app/app.go). The process
// genuinely has to restart for the join to take effect — that is structural,
// not cosmetic — and a Pi kiosk operator has no shell to do it from, which
// left the "joined — restart this till to finish" screen a real dead end.
//
// The mechanism is a deliberate, independent copy of the one
// internal/selfupdate has used in production since its first release:
// syscall.Exec of the current executable (reexec_unix.go). Because exec
// replaces the image in the SAME process, the data-directory lock
// (internal/db/lock.go) and the listening socket are both released through
// their CLOEXEC flags at the instant of exec and re-acquired cleanly by the
// new image's Run() — verified directly (an F_GETFD probe on both the lock
// fd and the listener), not just inferred from selfupdate's own track
// record. selfupdate is not reused directly, on purpose: its
// reexecFn/reexecDelay are unexported, and it is a battle-tested
// production-critical path with no reason to be touched for this feature.
//
// A hardware-plugin child process (internal/plugins, exec.CommandContext
// with no SysProcAttr/process-group/Pdeathsig) is NOT reparented, cancelled
// or signalled by an exec of ITS parent — the PID is unchanged and the
// plugin's own context is never cancelled on its own, so left alone it would
// survive across the restart and the new image could spawn a second one
// (review finding, ut-docs#1550, tracked and fixed as ut-docs#1616).
// SetBeforeRestart below is the fix: internal/app.Run wires it to
// internal/plugins.Supervisor.Shutdown, run (bounded) before the re-exec
// below, so every hardware-plugin process is stopped first. Pre-existing gap
// in selfupdate too, fixed there the same way.
//
// Windows is unsupported (reexec_windows.go): there is no in-place exec
// there, so callers fall back to telling the operator to close and reopen
// the app, which re-runs ApplyPendingRestore at the next start through the
// exact same startup path. A native Windows restart is tracked separately
// (ut-docs#1614).
package procrestart

import (
	"context"
	"os"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// Test seams. Production code never changes these; tests do, so Restart()
// can be observed without a real syscall.Exec replacing the test binary
// mid-run — the same convention as internal/selfupdate's own seams.
var (
	osExecutable = os.Executable
	reexecFn     = reexec
	// reexecDelay gives the in-flight HTTP response that requested the
	// restart time to reach the browser before the process image is
	// replaced — same value selfupdate.Apply uses for the same reason.
	reexecDelay = 1500 * time.Millisecond
)

// beforeRestart runs synchronously, immediately before Restart's delayed
// goroutine re-execs, giving the caller a chance to cleanly stop anything
// an exec of THIS process alone won't reach — specifically hardware-plugin
// child processes (internal/plugins.Supervisor), which are not reparented,
// cancelled or signalled by an exec of their parent (ut-docs#1616). No-op
// by default: a caller with nothing to clean up (e.g. every test in this
// package) never needs to set it.
var beforeRestart = func(context.Context) {}

// SetBeforeRestart registers the hook above. Call once at startup — from
// internal/app.Run, the only place that holds the plugin Supervisor —
// before any HTTP handler can reach Restart(). A nil fn resets to the
// no-op default.
func SetBeforeRestart(fn func(context.Context)) {
	if fn == nil {
		fn = func(context.Context) {}
	}
	beforeRestart = fn
}

// Supported reports whether Restart can replace the process image on this
// platform. It is a pure build-tag decision (see reexec_unix.go /
// reexec_windows.go), so a template can branch on it to show either the
// auto-restart flow or the honest "close and reopen" instruction.
func Supported() bool {
	return supported
}

// Restart schedules an in-place re-exec of the running executable after
// reexecDelay and returns immediately. It never blocks the caller; a
// re-exec failure is logged (there is no caller left to return it to) and
// the operator can retry or restart by hand. If the running executable
// cannot be located — practically impossible, but not worth a goroutine
// that only fails later — nothing is scheduled and the error is logged.
func Restart() {
	exe, err := osExecutable()
	if err != nil {
		logging.L().Errorf("[procrestart] locate executable: %v (restart manually)", err)
		return
	}
	logging.L().Infof("[procrestart] restarting %s in %v", exe, reexecDelay)
	go func() {
		// Run beforeRestart CONCURRENTLY with the flush-delay sleep below,
		// not sequentially after it (ut-docs#1616 review finding): stopping
		// hardware plugins can itself take real time (bounded, but not
		// free), and running it after reexecDelay would push the actual
		// re-exec later than callers assume —
		// web/ui/partials/pairing_wait.html times its first health probe
		// off reexecDelay alone. Overlapping the two means the common case
		// (no plugin running, or a fast stop) adds no extra delay at all;
		// only a genuinely slow/stuck plugin pushes the re-exec past
		// reexecDelay, and only by the time it actually needed.
		//
		// Skipped entirely when Supported() is false (Windows): Restart is
		// documented (internal/pages/pairing_join.go) as a safe, logged
		// no-op on a platform that can't in-place exec — nothing ever
		// actually restarts there, so stopping hardware plugins here would
		// just kill them permanently with no compensating restart.
		var hookDone <-chan struct{}
		if Supported() {
			done := make(chan struct{})
			hookDone = done
			go func() {
				beforeRestart(context.Background())
				close(done)
			}()
		}
		time.Sleep(reexecDelay)
		if hookDone != nil {
			<-hookDone
		}
		if err := reexecFn(exe); err != nil {
			logging.L().Errorf("[procrestart] re-exec failed (restart manually): %v", err)
		}
	}()
}
