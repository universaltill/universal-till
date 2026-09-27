//go:build desktop && linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// Self re-exec on Linux when this binary is replaced on disk (ut-docs#2991,
// AC1) — the apt/.deb upgrade case; see watchOwnBinary (exe_watch.go) for
// why that is the only thing that ever replaces unitill-desktop. The shell
// otherwise keeps running the deleted old inode with the old WebKitGTK
// mapped in, until its web process dies on screen.

func init() { startSelfReexecWatch = linuxStartSelfReexecWatch }

// selfReexecCheckInterval: an upgraded shell restarts within ~30–90s of
// dpkg finishing (a tick, then two identical sightings), cheap enough to never matter.
const selfReexecCheckInterval = 30 * time.Second

// selfExeProcPath names the running image's own inode even after it was
// deleted/replaced, so the baseline is the file we are actually running —
// not whatever sits at the path by the time showWindow gets here (the
// startup gate can hold the window for up to a minute on a cold boot).
const selfExeProcPath = "/proc/self/exe"

// linuxStartSelfReexecWatch starts the watcher goroutine; it stops when
// ctx is cancelled (showWindow returning). childPid > 0 is spawn mode: the
// till server this shell started itself, which must be stopped before the
// exec — the fresh shell spawns (or attaches to) its own, and exec skips
// main's deferred Kill, so it would otherwise be orphaned.
func linuxStartSelfReexecWatch(ctx context.Context, childPid int) {
	exe, err := os.Executable()
	if err != nil {
		logging.L().Warnf("self re-exec watch disabled: resolve own executable: %v", err)
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	first := true
	stat := func(p string) (os.FileInfo, error) {
		if first {
			first = false
			if fi, err := os.Stat(selfExeProcPath); err == nil {
				return fi, nil
			}
		}
		return os.Stat(p)
	}
	go func() {
		t := time.NewTicker(selfReexecCheckInterval)
		defer t.Stop()
		if err := watchOwnBinary(ctx, exe, t.C, stat, func() { reexecSelf(exe, childPid) }, nil); err != nil {
			logging.L().Warnf("self re-exec watch disabled: %v", err)
		}
	}()
}

// reexecSelf replaces this process with the new binary at exe. The new
// file must be executable before anything is torn down (review of
// ut-docs#2991, MAJOR 2): otherwise the old shell just keeps running (the
// pre-#2991 behaviour). If the exec itself still fails after the spawned
// till server was stopped, the shell re-execs its own running image via
// /proc/self/exe (valid even though the file was replaced) so that a
// fresh shell spawns a fresh server — never a window left with no till.
// Known, accepted leftover: the old image's WebKitNetworkProcess/
// WebKitWebProcess exit once their IPC sockets close with the exec, but
// stay as two zombie entries (no memory, just PIDs) until the shell exits
// — reaping arbitrary children in the new image could steal WebKit's or
// the till server's own exit statuses.
func reexecSelf(exe string, childPid int) {
	if err := syscall.Access(exe, unixXOK); err != nil {
		logging.L().Errorf("unitill-desktop: %s was replaced on disk but is not executable (%v) — keeping the running shell", exe, err)
		return
	}
	logging.L().Infof("unitill-desktop: %s was replaced on disk (package upgrade) — restarting the shell on the new version (ut-docs#2991)", exe)
	if childPid > 0 {
		stopChildServer(childPid)
	}
	err := syscall.Exec(exe, os.Args, os.Environ())
	if childPid <= 0 {
		logging.L().Errorf("unitill-desktop: re-exec %s failed, keeping the running shell: %v", exe, err)
		return
	}
	logging.L().Errorf("unitill-desktop: re-exec %s failed (%v) after the till server was stopped — restarting the current shell image instead", exe, err)
	err = syscall.Exec(selfExeProcPath, os.Args, os.Environ())
	logging.L().Errorf("unitill-desktop: restarting the current shell image failed too (%v) — the till server is stopped; restart the app", err)
}

// unixXOK is access(2)'s X_OK.
const unixXOK = 0x1

// stopChildServer asks the spawned till server to shut down cleanly
// (SIGTERM — it closes its SQLite database), escalating to SIGKILL if it
// is still running after a grace period, and reaps it either way.
func stopChildServer(pid int) {
	p, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	exited := make(chan struct{})
	go func() {
		_, _ = p.Wait()
		close(exited)
	}()
	_ = p.Signal(syscall.SIGTERM)
	select {
	case <-exited:
		return
	case <-time.After(10 * time.Second):
	}
	logging.L().Warnf("unitill-desktop: till server pid %d ignored SIGTERM for 10s, killing it", pid)
	_ = p.Kill()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		// Review of ut-docs#2991, MINOR 3: rare (D-state, stuck I/O), but
		// the fresh shell may then find :8080 still held and start a
		// second server on another port — say so rather than hide it.
		logging.L().Errorf("unitill-desktop: till server pid %d did not exit after SIGKILL; restarting the shell anyway", pid)
	}
}
