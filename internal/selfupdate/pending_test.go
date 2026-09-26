package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#2759: an in-app update on a Pi swaps /opt/unitill/bin/unitill-pos
// and must then actually run it. These tests pin the restart decision, the
// "restart pending" marker and the startup verification. Non-parallel: they
// mutate package seams (see selfupdate_apply_test.go).

func openProblem(key string) (logging.Problem, bool) {
	for _, p := range logging.OpenProblems(time.Now().UTC(), 0) {
		if p.Key == key {
			return p, true
		}
	}
	return logging.Problem{}, false
}

// fakeExe points the osExecutable seam at a binary in a temp dir, so the
// pending marker lands there, never next to the real test binary.
func fakeExe(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "unitill-pos")
	if err := os.WriteFile(exe, []byte("BIN"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := osExecutable
	osExecutable = func() (string, error) { return exe, nil }
	t.Cleanup(func() { osExecutable = old })
	return exe
}

func TestApplyWritesRestartPendingMarker(t *testing.T) {
	fix := newInstallFixture(t)
	name := archiveNameFor("0.2.0")
	archive := makeTarGz(t, []tarEntry{{name: "unitill-pos", body: "NEW-BINARY-v2"}})
	newReleaseServer(t, "v0.2.0", map[string][]byte{
		name:            archive,
		"checksums.txt": []byte(sha256hex(archive) + "  " + name + "\n"),
	})
	if err := Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	<-fix.reexecd
	p, ok := readPending(fix.dir)
	if !ok || p.Version != "0.2.0" {
		t.Fatalf("pending marker = %+v, %v; want version 0.2.0", p, ok)
	}
	if info, err := os.Stat(filepath.Join(fix.dir, pendingFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("marker perms: %v %v, want 0600", info, err)
	}
	st := CurrentStatus()
	if !st.RestartPending || st.Installed != "0.2.0" || st.Running != "0.1.0" {
		t.Fatalf("CurrentStatus in the old process = %+v, want restart pending 0.1.0 → 0.2.0", st)
	}
}

// A failed exec under systemd must hand the restart to systemd
// (Restart=always) via a graceful self-SIGTERM, not leave the old build up.
func TestExecFailureUnderSystemdSignalsSelf(t *testing.T) {
	exe := fakeExe(t)
	t.Setenv("INVOCATION_ID", "abc123")
	signalled := make(chan struct{}, 1)
	oldSig, oldReexec, oldDelay := signalSelfFn, reexecFn, reexecDelay
	signalSelfFn = func() error { signalled <- struct{}{}; return nil }
	reexecFn = func(string) error { return errors.New("exec format error") }
	reexecDelay = time.Millisecond
	t.Cleanup(func() { signalSelfFn, reexecFn, reexecDelay = oldSig, oldReexec, oldDelay })
	stubSmokeRun(t, nil) // the swapped binary itself is fine

	restartInto(newRestartPlan(exe, "0.2.0", ""))
	select {
	case <-signalled:
	default:
		t.Fatal("exec failed under systemd but the process did not ask systemd to restart it")
	}
}

// Outside systemd (desktop-spawned or a hand-run server) nothing would bring
// the process back, so it must stay up and raise a Problem instead.
func TestExecFailureOutsideSystemdStaysUpAndRaisesProblem(t *testing.T) {
	logging.ResetRecent()
	exe := fakeExe(t)
	t.Setenv("INVOCATION_ID", "")
	oldSig, oldReexec, oldDelay := signalSelfFn, reexecFn, reexecDelay
	signalSelfFn = func() error { t.Error("must not signal itself outside systemd"); return nil }
	reexecFn = func(string) error { return errors.New("permission denied") }
	reexecDelay = time.Millisecond
	t.Cleanup(func() { signalSelfFn, reexecFn, reexecDelay = oldSig, oldReexec, oldDelay })

	restartInto(newRestartPlan(exe, "0.2.0", ""))
	if _, ok := openProblem(ProblemKeyRestartPending); !ok {
		t.Fatal("no restart-pending Problem raised after a failed exec outside systemd")
	}
}

// Any code still running in this process long after the restart was
// scheduled proves the restart never happened (exec replaces the image).
func TestWatchdogRaisesProblemWhenStillRunning(t *testing.T) {
	logging.ResetRecent()
	oldAfter := restartWatchdogAfter
	restartWatchdogAfter = time.Millisecond
	t.Cleanup(func() { restartWatchdogAfter = oldAfter })
	oldVer := buildinfo.Version
	buildinfo.Version = "0.1.0"
	t.Cleanup(func() { buildinfo.Version = oldVer })

	restartWatchdog(newRestartPlan("unitill-pos", "0.2.0", ""))
	p, ok := openProblem(ProblemKeyRestartPending)
	if !ok || !strings.Contains(p.Msg, "0.2.0") || !strings.Contains(p.Msg, "0.1.0") {
		t.Fatalf("watchdog Problem = %+v, %v; want one naming both versions", p, ok)
	}
}

func TestReconcileAtStartup(t *testing.T) {
	cases := []struct {
		name, marker, running string
		wantPending, keep     bool
	}{
		{"new build answered", "0.2.0", "0.2.0", false, false},
		{"superseded by a newer .deb", "0.2.0", "0.3.0", false, false},
		{"old build still running", "0.2.0", "0.1.0", true, true},
		{"release candidate of the swapped version still running", "0.2.0", "0.2.0-rc1", true, true},
		{"no marker", "", "0.1.0", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			logging.ResetRecent()
			exe := fakeExe(t)
			dir := filepath.Dir(exe)
			if c.marker != "" {
				if err := writePending(dir, c.marker, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			old := buildinfo.Version
			buildinfo.Version = c.running
			t.Cleanup(func() { buildinfo.Version = old })

			if got := ReconcileAtStartup(); got != c.wantPending {
				t.Fatalf("ReconcileAtStartup = %v, want %v", got, c.wantPending)
			}
			_, kept := readPending(dir)
			if kept != c.keep {
				t.Fatalf("marker kept = %v, want %v", kept, c.keep)
			}
			_, raised := openProblem(ProblemKeyRestartPending)
			if raised != c.wantPending {
				t.Fatalf("Problem raised = %v, want %v", raised, c.wantPending)
			}
		})
	}
}

// The service fallback above depends on the shipped unit restarting the
// process after ANY exit (a graceful SIGTERM exits 0). Pin it.
func TestServiceUnitRestartsOnAnyExit(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "packaging", "systemd", "unitill-pos.service"))
	if err != nil {
		t.Fatal(err)
	}
	unit := string(b)
	if !strings.Contains(unit, "\nRestart=always\n") {
		t.Fatal("unitill-pos.service must keep Restart=always: selfupdate's exec-failure fallback relies on systemd restarting a clean exit (ut-docs#2759)")
	}
	if strings.Contains(unit, "RestartPreventExitStatus") {
		t.Fatal("RestartPreventExitStatus would stop systemd restarting after an update (ut-docs#2759)")
	}
}

// stubSmokeRun replaces the "can the new binary start" check with a fixed
// result for the duration of the test.
func stubSmokeRun(t *testing.T, err error) {
	t.Helper()
	old := smokeRunFn
	smokeRunFn = func(string, string) error { return err }
	t.Cleanup(func() { smokeRunFn = old })
}

// writeScript writes an executable shell script (the smoke run's fake
// "new binary"). Unix only: self-update is unsupported on Windows.
func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSmokeRun(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("self-update is unsupported on Windows")
	}
	oldTimeout := smokeRunTimeout
	smokeRunTimeout = 2 * time.Second
	t.Cleanup(func() { smokeRunTimeout = oldTimeout })
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage")
	if err := os.WriteFile(garbage, []byte("\x7fELF-not-really"), 0o755); err != nil {
		t.Fatal(err)
	}
	noExec := filepath.Join(dir, "noexec")
	if err := os.WriteFile(noExec, []byte("#!/bin/sh\necho 0.2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, exe string
		ok        bool
	}{
		{"prints its version", writeScript(t, dir, "good", `[ "$1" = "--version" ] && echo 0.2.0`), true},
		// Package init logs a line to stdout before main() runs.
		{"logs before the version", writeScript(t, dir, "logs", `echo "[INFO] logging initialised"; echo 0.2.0`), true},
		{"version not on the last line", writeScript(t, dir, "notlast", `echo 0.2.0; echo "[INFO] booting"`), false},
		{"exits non-zero", writeScript(t, dir, "fails", "exit 3"), false},
		{"wrong version", writeScript(t, dir, "wrong", "echo 0.1.0"), false},
		{"hangs past the timeout", writeScript(t, dir, "hangs", "sleep 30"), false},
		{"not an executable format (ENOEXEC)", garbage, false},
		{"not executable (EACCES)", noExec, false},
		{"missing (ENOENT)", filepath.Join(dir, "missing"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := time.Now()
			err := smokeRun(c.exe, "0.2.0")
			if (err == nil) != c.ok {
				t.Fatalf("smokeRun = %v, want ok=%v", err, c.ok)
			}
			if time.Since(start) > 10*time.Second {
				t.Fatalf("smokeRun took %v — the timeout did not bound it", time.Since(start))
			}
		})
	}
}

// A swapped-in binary that cannot start must never reach the restart: Apply
// puts the old binary + web back, writes no marker, schedules nothing and
// says so.
func TestApplyRollsBackWhenNewBinaryCannotStart(t *testing.T) {
	logging.ResetRecent()
	fix := newInstallFixture(t)
	stubSmokeRun(t, errors.New("exec format error"))
	name := archiveNameFor("0.2.0")
	archive := makeTarGz(t, []tarEntry{
		{name: "unitill-pos", body: "NEW-BINARY-v2"},
		{name: "web", dir: true},
		{name: "web/index.html", body: "NEW-WEB"},
	})
	newReleaseServer(t, "v0.2.0", map[string][]byte{
		name:            archive,
		"checksums.txt": []byte(sha256hex(archive) + "  " + name + "\n"),
	})
	err := Apply(context.Background())
	if err == nil || !strings.Contains(err.Error(), "could not start") || !strings.Contains(err.Error(), "0.1.0") {
		t.Fatalf("Apply = %v, want a 'could not start; kept v0.1.0' error", err)
	}
	if got, _ := os.ReadFile(fix.exe); string(got) != fix.oldBin {
		t.Fatalf("binary = %q, want the old one restored", got)
	}
	if got, _ := os.ReadFile(filepath.Join(fix.dir, "web", "index.html")); string(got) != "OLD-WEB" {
		t.Fatalf("web = %q, want the old one restored", got)
	}
	if _, ok := readPending(fix.dir); ok {
		t.Fatal("marker written for an update that was rolled back")
	}
	select {
	case <-fix.reexecd:
		t.Fatal("restart scheduled into a binary that cannot start")
	case <-time.After(50 * time.Millisecond):
	}
	if p, ok := openProblem(ProblemKeyUpdateFailed); !ok || !strings.Contains(p.Msg, "0.1.0") {
		t.Fatalf("Problem = %+v, %v; want one naming the kept version", p, ok)
	}
}

// If exec fails and the swapped binary cannot start, a self-SIGTERM would
// hand systemd (Restart=always) a binary that crash-loops: an offline till.
// Instead: restore the backup, drop the marker, raise a Problem, stay up.
func TestExecFailureWithUnstartableBinaryRestoresBackupAndDoesNotSignal(t *testing.T) {
	logging.ResetRecent()
	exe := fakeExe(t)
	dir := filepath.Dir(exe)
	if err := os.WriteFile(exe+".bak", []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writePending(dir, "0.2.0", time.Now()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INVOCATION_ID", "abc123")
	oldSig, oldReexec, oldDelay := signalSelfFn, reexecFn, reexecDelay
	signalSelfFn = func() error { t.Error("signalled itself into a binary that cannot start"); return nil }
	reexecFn = func(string) error { return errors.New("exec format error") }
	reexecDelay = time.Millisecond
	t.Cleanup(func() { signalSelfFn, reexecFn, reexecDelay = oldSig, oldReexec, oldDelay })
	stubSmokeRun(t, errors.New("exec format error"))

	restartInto(newRestartPlan(exe, "0.2.0", ""))
	if got, _ := os.ReadFile(exe); string(got) != "OLD" {
		t.Fatalf("binary = %q, want the .bak restored", got)
	}
	if _, ok := readPending(dir); ok {
		t.Fatal("marker kept after rolling back")
	}
	if _, ok := openProblem(ProblemKeyUpdateFailed); !ok {
		t.Fatal("no Problem raised for an update that could not start")
	}
}

// readPending only trusts a small regular file: a symlink planted in the
// binary's directory, or a huge file, is ignored.
func TestReadPendingRejectsSymlinkAndOversize(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere.json")
	if err := os.WriteFile(target, []byte(`{"version":"9.9.9"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, pendingFile)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if p, ok := readPending(dir); ok {
		t.Fatalf("followed a symlinked marker: %+v", p)
	}
	_ = os.Remove(filepath.Join(dir, pendingFile))
	big := `{"version":"9.9.9","pad":"` + strings.Repeat("x", 8192) + `"}`
	if err := os.WriteFile(filepath.Join(dir, pendingFile), []byte(big), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, ok := readPending(dir); ok {
		t.Fatalf("accepted an oversize marker: %+v", p)
	}
}

// A dev build compares older than every release (updates.Newer), so any
// leftover marker would read as "restart pending" forever on a developer's
// machine. Dev builds skip the check entirely.
func TestDevBuildIgnoresMarker(t *testing.T) {
	logging.ResetRecent()
	exe := fakeExe(t)
	if err := writePending(filepath.Dir(exe), "0.2.0", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	old := buildinfo.Version
	buildinfo.Version = "dev"
	t.Cleanup(func() { buildinfo.Version = old })
	if ReconcileAtStartup() {
		t.Fatal("dev build reported a pending restart")
	}
	if st := CurrentStatus(); st.RestartPending {
		t.Fatalf("dev build status = %+v, want no restart pending", st)
	}
	if _, raised := openProblem(ProblemKeyRestartPending); raised {
		t.Fatal("dev build raised the restart-pending Problem")
	}
}

// A .deb install (e.g. a downgrade) over a failed in-app update replaces
// the binary after the marker was written: the marker no longer describes
// what is on disk and must be dropped, not reported as "restart pending".
func TestMarkerOlderThanBinaryIsDropped(t *testing.T) {
	logging.ResetRecent()
	exe := fakeExe(t) // binary written now
	dir := filepath.Dir(exe)
	if err := writePending(dir, "0.3.0", time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	old := buildinfo.Version
	buildinfo.Version = "0.2.0"
	t.Cleanup(func() { buildinfo.Version = old })
	if st := CurrentStatus(); st.RestartPending {
		t.Fatalf("status = %+v, want the stale marker ignored", st)
	}
	if ReconcileAtStartup() {
		t.Fatal("stale marker reported as restart pending")
	}
	if _, ok := readPending(dir); ok {
		t.Fatal("stale marker not removed")
	}
	if _, raised := openProblem(ProblemKeyRestartPending); raised {
		t.Fatal("stale marker raised the Problem")
	}
}

// After restartInto rolled back an unstartable update, the watchdog must not
// then claim "restart pending" for a version that is no longer on disk.
func TestWatchdogSilentAfterRollback(t *testing.T) {
	logging.ResetRecent()
	exe := fakeExe(t)
	if err := os.WriteFile(exe+".bak", []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INVOCATION_ID", "abc123")
	oldSig, oldReexec, oldDelay, oldAfter := signalSelfFn, reexecFn, reexecDelay, restartWatchdogAfter
	signalSelfFn = func() error { return nil }
	reexecFn = func(string) error { return errors.New("exec format error") }
	reexecDelay, restartWatchdogAfter = time.Millisecond, time.Millisecond
	t.Cleanup(func() {
		signalSelfFn, reexecFn, reexecDelay, restartWatchdogAfter = oldSig, oldReexec, oldDelay, oldAfter
	})
	stubSmokeRun(t, errors.New("exec format error"))

	plan := newRestartPlan(exe, "0.2.0", "")
	restartInto(plan)
	restartWatchdog(plan)
	if p, raised := openProblem(ProblemKeyRestartPending); raised {
		t.Fatalf("watchdog raised %+v after the update was rolled back", p)
	}
}
