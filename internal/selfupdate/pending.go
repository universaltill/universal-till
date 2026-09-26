package selfupdate

// Restart verification for an applied update (ut-docs#2759).
//
// Apply swaps the binary and then re-execs it in place (same PID). On a Pi
// the process runs as the pos user under systemd; exec keeps the PID, so
// systemd's NRestarts/ActiveEnterTimestamp never change and cannot tell
// whether the new build is running. Until this file, a failed or stuck exec
// was only logged, and the UI reloaded on the first /healthz 200 — which the
// OLD process answers too — so an operator could sit on the old build with
// the new one on disk and nothing saying so.
//
// What this adds, with no extra privilege (no polkit rule, no sudo):
//   - a marker next to the binary recording which version was swapped in;
//   - a smoke run (`<new> --version`) right after the swap and again before
//     any self-SIGTERM: a binary that cannot start on this machine is rolled
//     back from its .bak instead of being handed to systemd, which would
//     respawn it in a loop and leave the till offline;
//   - restartInto: exec, and if exec fails under systemd, a graceful
//     self-SIGTERM so the unit's Restart=always starts the new binary (the
//     unit already restarts after any exit — pinned by a test); outside
//     systemd nothing would bring the process back, so it stays up and
//     raises a Problem;
//   - a watchdog: any code of this process still running well after the
//     restart was scheduled proves the restart never happened (exec replaces
//     the image) → Problem;
//   - ReconcileAtStartup: the new image clears the marker when it is at
//     least the swapped-in version, or raises the Problem when it is older;
//   - CurrentStatus for GET /api/update/status, which the UI polls instead
//     of /healthz so it only reloads once the new version answers.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/updates"
)

// pendingFile lives in the binary's directory: it describes that binary, and
// Supported() has already proved the directory writable by this process. A
// .deb install that leaves it behind is harmless: the marker is dropped when
// a build at least as new as the recorded one starts, or when the binary
// changed after the marker was written (livePending); postremove.sh deletes
// it on purge.
const pendingFile = ".unitill-update-pending"

// ProblemKeyUpdateFailed tags the Problem for "the downloaded build could not
// start, so the till kept (or went back to) the running version".
const ProblemKeyUpdateFailed = "selfupdate.update_failed"

// maxPendingSize caps the marker read: the real file is ~80 bytes.
const maxPendingSize = 4096

// ProblemKeyRestartPending tags the Problems entry (back-office Problems
// panel + cloud heartbeat, ADR-0018) for "installed but not running".
const ProblemKeyRestartPending = "selfupdate.restart_pending"

type pending struct {
	Version   string    `json:"version"`
	SwappedAt time.Time `json:"swapped_at"`
}

// Status is what GET /api/update/status reports.
type Status struct {
	Running        string `json:"running_version"`
	Installed      string `json:"installed_version"`
	RestartPending bool   `json:"restart_pending"`
}

var (
	// signalSelfFn asks this process to shut down gracefully (reexec_*.go).
	signalSelfFn = signalSelf
	// restartWatchdogAfter is generous: the normal path re-execs within
	// reexecDelay plus a plugin stop bounded at 5s.
	restartWatchdogAfter = 60 * time.Second
	// restartHookBound caps how long a wedged beforeRestart may hold the
	// restart back (the production hook bounds itself at 5s already).
	restartHookBound = 15 * time.Second
	// smokeRunFn proves a swapped-in binary can start at all (see smokeRun).
	smokeRunFn = smokeRun
	// smokeRunTimeout bounds that check; `--version` exits before any
	// config, DB or network work, so even a cold Pi SD card answers in well
	// under a second.
	smokeRunTimeout = 10 * time.Second
)

func writePending(dir, version string, now time.Time) error {
	b, err := json.Marshal(pending{Version: version, SwappedAt: now.UTC()})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, pendingFile), b, 0o600)
}

// readPending trusts only a small regular file: a symlink planted in the
// binary's directory (or anything huge) is ignored rather than followed.
func readPending(dir string) (pending, bool) {
	path := filepath.Join(dir, pendingFile)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxPendingSize {
		return pending{}, false
	}
	f, err := os.Open(path)
	if err != nil {
		return pending{}, false
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxPendingSize+1))
	if err != nil || len(b) > maxPendingSize {
		return pending{}, false
	}
	var p pending
	if json.Unmarshal(b, &p) != nil || strings.TrimSpace(p.Version) == "" {
		return pending{}, false
	}
	return p, true
}

// isDevBuild: an unstamped local build compares older than every release
// (updates.Newer), so a marker would read as "restart pending" forever.
func isDevBuild() bool {
	return buildinfo.Version == "dev" || buildinfo.Version == ""
}

// livePending returns the marker only when it still describes the binary on
// disk. A marker older than the binary's last change means something else —
// a .deb install or downgrade over a failed in-app update — replaced it
// since; that stale marker is removed.
func livePending(dir, exe string) (pending, bool) {
	p, ok := readPending(dir)
	if !ok {
		return pending{}, false
	}
	if info, err := os.Stat(exe); err == nil && fileChangedAt(info).After(p.SwappedAt) {
		_ = os.Remove(filepath.Join(dir, pendingFile))
		logging.L().Infof("[selfupdate] dropped the v%s update marker: the binary was replaced after it was written", p.Version)
		return pending{}, false
	}
	return p, true
}

func binPath() (string, bool) {
	exe, err := osExecutable()
	if err != nil {
		return "", false
	}
	return exe, true
}

// CurrentStatus reports the running version and, when an applied update has
// not taken over yet, the version waiting on disk.
func CurrentStatus() Status {
	st := Status{Running: buildinfo.Version, Installed: buildinfo.Version}
	exe, ok := binPath()
	if !ok || isDevBuild() {
		return st
	}
	if p, ok := livePending(filepath.Dir(exe), exe); ok && updates.Newer(p.Version, buildinfo.Version) {
		st.Installed = p.Version
		st.RestartPending = true
	}
	return st
}

// ReconcileAtStartup runs once at server start. It reports whether an
// applied update is still waiting (this build is older than the one swapped
// in), raising the Problem in that case; otherwise it clears the marker.
func ReconcileAtStartup() bool {
	exe, ok := binPath()
	if !ok || isDevBuild() {
		return false
	}
	dir := filepath.Dir(exe)
	p, ok := livePending(dir, exe)
	if !ok {
		return false
	}
	log := logging.L()
	if updates.Newer(p.Version, buildinfo.Version) {
		log.WarnProblemf(ProblemKeyRestartPending,
			"[selfupdate] v%s was installed at %s but v%s started — restart pending; restart the till to finish the update",
			p.Version, p.SwappedAt.Format(time.RFC3339), buildinfo.Version)
		return true
	}
	_ = os.Remove(filepath.Join(dir, pendingFile))
	log.Infof("[selfupdate] update to v%s is running (installed %s)", buildinfo.Version, p.SwappedAt.Format(time.RFC3339))
	return false
}

// smokeRun proves the swapped-in binary can start on this machine before
// anything restarts into it: `<exe> --version` must exit 0 within
// smokeRunTimeout and print exactly the expected version as its last stdout line. It catches a wrong
// architecture or corrupt file (ENOEXEC), a lost exec bit or noexec mount
// (EACCES), a missing file (ENOENT) and a build that crashes on start. The
// --version path in main.go returns before config, DB, plugins or network.
func smokeRun(exe, version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), smokeRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "--version")
	cmd.WaitDelay = time.Second // don't wait on pipes a killed child's children hold
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return fmt.Errorf("%s --version did not finish within %v", filepath.Base(exe), smokeRunTimeout)
	}
	if err != nil {
		return fmt.Errorf("%s --version: %w", filepath.Base(exe), err)
	}
	// The version is the last line: package init may log before main().
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if got := strings.TrimSpace(lines[len(lines)-1]); got != version {
		if len(got) > 64 {
			got = got[:64] + "…"
		}
		return fmt.Errorf("%s --version reported %q, want %q", filepath.Base(exe), got, version)
	}
	return nil
}

// rollbackSwap restores the pre-update binary (and web/, when Apply swapped
// it) from the .bak copies Apply kept, and drops the marker. Each rename is
// atomic on the same filesystem, so the binary path always holds a complete
// file.
func rollbackSwap(exe, webDir string) error {
	var errs []error
	if err := os.Rename(exe+".bak", exe); err != nil {
		errs = append(errs, fmt.Errorf("restore binary: %w", err))
	}
	if webDir != "" {
		if _, err := os.Stat(webDir + ".bak"); err == nil {
			_ = os.RemoveAll(webDir)
			if err := os.Rename(webDir+".bak", webDir); err != nil {
				errs = append(errs, fmt.Errorf("restore web: %w", err))
			}
		}
	}
	if err := os.Remove(filepath.Join(filepath.Dir(exe), pendingFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("remove marker: %w", err))
	}
	return errors.Join(errs...)
}

// cannotStartMsg is the Problem text for an update whose binary could not
// start (the till stays on, or goes back to, the running version).
func cannotStartMsg(version, running string, cause error) string {
	return fmt.Sprintf("[selfupdate] update to v%s could not start (%v); kept version v%s", version, cause, running)
}

// underSystemd reports whether a service manager started this process and
// will restart it after it exits. INVOCATION_ID is set by systemd (v232+)
// for every service it starts; whoever could fake it already controls how
// the process is launched, so trusting it grants nothing.
func underSystemd() bool {
	return os.Getenv("INVOCATION_ID") != ""
}

// restartPlan snapshots every seam and value the restart goroutines need at
// the moment Apply schedules them, so nothing reads package state later
// (a real process never changes it; tests do, between runs).
type restartPlan struct {
	exe, version, running string
	webDir                string // "" when Apply did not swap web/
	delay, hookBound      time.Duration
	watchdogAfter         time.Duration
	hook                  func(context.Context)
	exec                  func(string) error
	signalSelf            func() error
	smoke                 func(exe, version string) error
	systemd               bool
	// idle, when set (ApplyVersionWhenIdle, ut-docs#2738), is re-checked
	// after the delay: a sale started meanwhile holds the restart, and the
	// plugins it needs are only stopped once it is done.
	idle func() bool
	// idleSeen is closed by restartInto once its own idle wait passed; the
	// watchdog counts from then, never from a poll of its own (which could
	// see idle just before a sale opens and report a held restart).
	idleSeen chan struct{}
	// rolledBack is shared by restartInto and restartWatchdog: once the
	// swap was undone there is no pending restart left to report.
	rolledBack *atomic.Bool
}

func newRestartPlan(exe, version, webDir string) restartPlan {
	return restartPlan{
		exe: exe, version: version, running: buildinfo.Version, webDir: webDir,
		delay: reexecDelay, hookBound: restartHookBound, watchdogAfter: restartWatchdogAfter,
		hook: beforeRestart, exec: reexecFn, signalSelf: signalSelfFn, smoke: smokeRunFn,
		systemd: underSystemd(), rolledBack: new(atomic.Bool), idleSeen: make(chan struct{}),
	}
}

// restartInto replaces this process with the freshly swapped binary. It
// only returns if that failed.
func restartInto(p restartPlan) {
	if p.idle != nil {
		time.Sleep(p.delay)
		_ = waitIdle(context.Background(), p.idle)
		p.delay = 0 // already waited; stop the plugins now
	}
	close(p.idleSeen)
	done := make(chan struct{})
	go func() {
		p.hook(context.Background())
		close(done)
	}()
	time.Sleep(p.delay)
	select {
	case <-done:
	case <-time.After(p.hookBound):
		logging.L().Warnf("[selfupdate] stopping plugins before restart took over %v — restarting anyway", p.hookBound)
	}
	// A sale opened while the plugins stopped still must not be cut off.
	_ = waitIdle(context.Background(), p.idle)
	err := p.exec(p.exe)
	if err == nil {
		return // only a test seam returns nil; a real exec never returns on success
	}
	log := logging.L()
	log.Errorf("[selfupdate] re-exec into v%s failed: %v", p.version, err)
	if p.systemd {
		// Never hand systemd (Restart=always) a binary that cannot start:
		// it would respawn it in a loop and the till would be offline.
		// Apply already checked it right after the swap; check again here,
		// since exec itself just failed.
		if serr := p.smoke(p.exe, p.version); serr != nil {
			if rerr := rollbackSwap(p.exe, p.webDir); rerr != nil {
				log.Errorf("[selfupdate] restoring the previous version failed: %v", rerr)
			} else {
				p.rolledBack.Store(true)
			}
			log.WarnProblemf(ProblemKeyUpdateFailed, "%s", cannotStartMsg(p.version, p.running, serr))
			return
		}
		log.Warnf("[selfupdate] asking the service manager to restart the till on v%s", p.version)
		serr := p.signalSelf()
		if serr == nil {
			return
		}
		log.Errorf("[selfupdate] self-restart signal failed: %v", serr)
	}
	log.WarnProblemf(ProblemKeyRestartPending, "%s", p.pendingMsg())
}

// restartWatchdog raises the Problem if this process is still running
// watchdogAfter after the restart was scheduled.
func restartWatchdog(p restartPlan) {
	// A restart held for an open sale is not "pending" yet: count from the
	// moment restartInto's own idle wait passed.
	<-p.idleSeen
	time.Sleep(p.watchdogAfter)
	if p.rolledBack.Load() {
		return
	}
	logging.L().WarnProblemf(ProblemKeyRestartPending, "%s", p.pendingMsg())
}

func (p restartPlan) pendingMsg() string {
	return fmt.Sprintf("[selfupdate] v%s is installed but v%s is still running — restart pending; restart the till to finish the update",
		p.version, p.running)
}
