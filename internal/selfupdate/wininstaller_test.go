package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// ut-docs#160: a Windows till updates itself by running the signed NSIS
// installer from a detached helper. The flow is platform-neutral Go (the
// hostGOOS seam routes it), so it is tested here on every OS; only the two
// Windows system calls — the Authenticode check and the detached helper
// start — are stubbed. Non-parallel: package seams (selfupdate_apply_test.go).

type helperCall struct {
	script, setup, installDir, logPath, marker string
}

// winFixture is a fake per-user installer install
// (%LOCALAPPDATA%\Programs\Universal Till): the exe plus the uninstall.exe
// the NSIS installer writes, with every Windows seam stubbed.
type winFixture struct {
	dir, exe, work string
	helper         chan helperCall
	helperErr      error
	sigErr         error
	sigCalls       atomic.Int32
	sigPublisher   atomic.Value // string
	sigSawBytes    atomic.Value // []byte: the file content the check saw
	hookRan        atomic.Bool
}

func newWinFixture(t *testing.T) *winFixture {
	t.Helper()
	dir := t.TempDir()
	f := &winFixture{dir: dir, exe: filepath.Join(dir, "unitill-pos.exe"), work: filepath.Join(t.TempDir(), "updates"), helper: make(chan helperCall, 1)}
	for _, name := range []string{"unitill-pos.exe", "unitill-desktop.exe", "uninstall.exe"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("OLD"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldGOOS, oldExec, oldVer, oldDelay := hostGOOS, osExecutable, buildinfo.Version, reexecDelay
	oldSig, oldStart, oldDir, oldWatch, oldHook := verifyAuthenticodeFn, startWindowsHelperFn, windowsUpdateDir, windowsWatchdogAfter, beforeRestart
	hostGOOS = "windows"
	osExecutable = func() (string, error) { return f.exe, nil }
	buildinfo.Version = "0.1.0"
	reexecDelay = time.Millisecond
	windowsWatchdogAfter = time.Hour
	windowsUpdateDir = func() string { return f.work }
	verifyAuthenticodeFn = func(_ context.Context, path, publisher string) error {
		f.sigCalls.Add(1)
		f.sigPublisher.Store(publisher)
		b, _ := os.ReadFile(path)
		f.sigSawBytes.Store(b)
		return f.sigErr
	}
	startWindowsHelperFn = func(script, setup, installDir, logPath, marker string) error {
		if f.helperErr != nil {
			return f.helperErr
		}
		f.helper <- helperCall{script, setup, installDir, logPath, marker}
		return nil
	}
	windowsHandover.Store(false)
	beforeRestart = func(context.Context) { f.hookRan.Store(true) }
	t.Cleanup(func() {
		hostGOOS, osExecutable, buildinfo.Version, reexecDelay = oldGOOS, oldExec, oldVer, oldDelay
		verifyAuthenticodeFn, startWindowsHelperFn, windowsUpdateDir, windowsWatchdogAfter, beforeRestart = oldSig, oldStart, oldDir, oldWatch, oldHook
		windowsHandover.Store(false)
	})
	return f
}

func (f *winFixture) waitHelper(t *testing.T) helperCall {
	t.Helper()
	select {
	case c := <-f.helper:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("the installer helper was never started")
	}
	return helperCall{}
}

func (f *winFixture) noHelper(t *testing.T) {
	t.Helper()
	select {
	case c := <-f.helper:
		t.Fatalf("the installer helper must not start, got %+v", c)
	case <-time.After(50 * time.Millisecond):
	}
}

const winSetup = "unitill-pos-setup-0.2.0.exe"

func winAssets(setup []byte) map[string][]byte {
	return map[string][]byte{
		winSetup:        setup,
		"checksums.txt": []byte(sha256hex(setup) + "  " + winSetup + "\n"),
	}
}

func TestSupportedWindowsNeedsAnInstallerInstall(t *testing.T) {
	f := newWinFixture(t)
	if !Supported() {
		t.Fatal("a writable installer install (uninstall.exe present) must be self-updatable")
	}
	// A portable zip has no uninstall.exe: running the installer over it
	// would turn it into an installed copy, so it keeps the download link.
	if err := os.Remove(filepath.Join(f.dir, "uninstall.exe")); err != nil {
		t.Fatal(err)
	}
	if Supported() {
		t.Fatal("a portable zip extraction (no uninstall.exe) must not be self-updatable")
	}
}

func TestApplyWindowsRunsVerifiedInstallerThroughHelper(t *testing.T) {
	f := newWinFixture(t)
	setup := []byte("SIGNED-SETUP-v2")
	newReleaseServer(t, "v0.2.0", winAssets(setup))

	if err := Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	c := f.waitHelper(t)
	if filepath.Base(c.setup) != winSetup || !strings.HasPrefix(filepath.Base(filepath.Dir(c.setup)), "attempt-") || filepath.Dir(filepath.Dir(c.setup)) != f.work {
		t.Errorf("setup = %q, want %s in a fresh attempt-* folder under %q", c.setup, winSetup, f.work)
	}
	if want := filepath.Join(f.dir, pendingFile); c.marker != want {
		t.Errorf("marker = %q, want %q", c.marker, want)
	}
	if c.installDir != f.dir {
		t.Errorf("installDir = %q, want %q", c.installDir, f.dir)
	}
	if want := filepath.Join(f.work, "updater.log"); c.logPath != want {
		t.Errorf("log = %q, want %q", c.logPath, want)
	}
	if b, err := os.ReadFile(c.setup); err != nil || string(b) != string(setup) {
		t.Errorf("setup on disk = %q, %v; want the verified download", b, err)
	}
	if b, err := os.ReadFile(c.script); err != nil || string(b) != windowsUpdaterScript {
		t.Errorf("helper script on disk = %v; want windowsUpdaterScript", err)
	}
	if got, _ := f.sigSawBytes.Load().([]byte); string(got) != string(setup) {
		t.Errorf("Authenticode check saw %q, want the downloaded setup", got)
	}
	if got, _ := f.sigPublisher.Load().(string); got != windowsPublisher {
		t.Errorf("Authenticode publisher = %q, want %q", got, windowsPublisher)
	}
	if !f.hookRan.Load() {
		t.Error("beforeRestart (stop hardware plugins) must run before the helper stops the till")
	}
	if p, ok := readPending(f.dir); !ok || p.Version != "0.2.0" {
		t.Errorf("restart-pending marker = %+v, %v; want v0.2.0", p, ok)
	}
	// The installer replaces the files: Apply itself never touches them.
	if b, _ := os.ReadFile(f.exe); string(b) != "OLD" {
		t.Errorf("Apply must not swap the exe itself, got %q", b)
	}
}

func TestApplyVersionWindowsUsesTheTag(t *testing.T) {
	f := newWinFixture(t)
	rs := newReleaseServer(t, "v0.2.0", winAssets([]byte("SETUP")))
	if err := ApplyVersion(context.Background(), "0.2.0"); err != nil {
		t.Fatalf("ApplyVersion: %v", err)
	}
	f.waitHelper(t)
	if got := rs.requested(); len(got) != 1 || got[0] != "/tags/v0.2.0" {
		t.Errorf("release metadata asked for %v, want [/tags/v0.2.0]", got)
	}
}

func TestApplyWindowsFailsClosed(t *testing.T) {
	cases := []struct {
		name    string
		assets  func(setup []byte) map[string][]byte
		sigErr  error
		wantErr string
		sigRun  bool
	}{
		{"no installer in the release", func([]byte) map[string][]byte {
			return map[string][]byte{"checksums.txt": []byte("x  y\n")}
		}, nil, "no Windows installer", false},
		{"no checksums.txt", func(s []byte) map[string][]byte {
			return map[string][]byte{winSetup: s}
		}, nil, "no checksums.txt", false},
		{"no checksum line for the installer", func(s []byte) map[string][]byte {
			return map[string][]byte{winSetup: s, "checksums.txt": []byte(sha256hex(s) + "  other.exe\n")}
		}, nil, "checksum not found", false},
		{"checksum mismatch", func(s []byte) map[string][]byte {
			return map[string][]byte{winSetup: s, "checksums.txt": []byte(sha256hex([]byte("TAMPERED")) + "  " + winSetup + "\n")}
		}, nil, "checksum mismatch", false},
		{"download fails", func(s []byte) map[string][]byte {
			return map[string][]byte{winSetup: nil, "checksums.txt": []byte(sha256hex(s) + "  " + winSetup + "\n")}
		}, nil, "download", false},
		{"bad Authenticode signature", winAssets, errors.New("signature status NotSigned"), "NotSigned", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newWinFixture(t)
			f.sigErr = c.sigErr
			newReleaseServer(t, "v0.2.0", c.assets([]byte("SETUP")))
			err := Apply(context.Background())
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("Apply err = %v, want it to mention %q", err, c.wantErr)
			}
			f.noHelper(t)
			if ran := f.sigCalls.Load() > 0; ran != c.sigRun {
				t.Errorf("Authenticode check ran = %v, want %v (it must come after the checksum)", ran, c.sigRun)
			}
			if left := attemptDirs(t, f.work); len(left) != 0 {
				t.Errorf("a rejected installer must be deleted, left %v", left)
			}
			if _, ok := readPending(f.dir); ok {
				t.Error("no restart-pending marker for a refused update")
			}
		})
	}
}

func TestApplyWindowsHelperStartFailureRaisesProblemAndDropsMarker(t *testing.T) {
	f := newWinFixture(t)
	f.helperErr = errors.New("CreateProcess: access denied")
	newReleaseServer(t, "v0.2.0", winAssets([]byte("SETUP")))
	if err := Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		p, ok := openProblem(ProblemKeyUpdateFailed)
		if ok && strings.Contains(p.Msg, "access denied") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s Problem naming the helper failure", ProblemKeyUpdateFailed)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok := readPending(f.dir); ok {
		t.Error("the marker must be dropped when the installer never started")
	}
}

func TestApplyWindowsWaitsForNoOpenSale(t *testing.T) {
	f := newWinFixture(t)
	oldPoll := restartIdlePoll
	restartIdlePoll = time.Millisecond
	t.Cleanup(func() { restartIdlePoll = oldPoll })
	newReleaseServer(t, "v0.2.0", winAssets([]byte("SETUP")))

	var open atomic.Bool
	open.Store(true)
	done := make(chan error, 1)
	go func() { done <- ApplyVersionWhenIdle(context.Background(), "", func() bool { return !open.Load() }) }()
	f.noHelper(t)
	if f.hookRan.Load() {
		t.Fatal("plugins must not stop while a sale is open")
	}
	open.Store(false)
	if err := <-done; err != nil {
		t.Fatalf("ApplyVersionWhenIdle: %v", err)
	}
	f.waitHelper(t)
}

func TestApplyWindowsCancelledWhileASaleIsOpenLeavesNothingBehind(t *testing.T) {
	f := newWinFixture(t)
	oldPoll := restartIdlePoll
	restartIdlePoll = time.Millisecond
	t.Cleanup(func() { restartIdlePoll = oldPoll })
	newReleaseServer(t, "v0.2.0", winAssets([]byte("SETUP")))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := ApplyVersionWhenIdle(ctx, "", func() bool { return false }); err == nil {
		t.Fatal("want an error when the wait for no open sale is cancelled")
	}
	f.noHelper(t)
	if left := attemptDirs(t, f.work); len(left) != 0 {
		t.Errorf("the download must be deleted, left %v", left)
	}
	if _, ok := readPending(f.dir); ok {
		t.Error("no marker when nothing was started")
	}
}

func TestCheckAuthenticodeResult(t *testing.T) {
	cases := []struct {
		name, out, wantErr string
	}{
		{"valid, our CN", `{"status":"Valid","signer":"TASK RUNNER TECHNOLOGY LTD","subject":"CN=TASK RUNNER TECHNOLOGY LTD, O=TASK RUNNER TECHNOLOGY LTD, L=London, C=GB"}`, ""},
		{"valid, CN case differs", `{"status":"Valid","signer":"Task Runner Technology Ltd","subject":"CN=Task Runner Technology Ltd"}`, ""},
		{"valid, our O but another CN", `{"status":"Valid","signer":"Build Signer","subject":"CN=Build Signer, O=TASK RUNNER TECHNOLOGY LTD, C=GB"}`, "not signed by"},
		{"not signed", `{"status":"NotSigned","signer":"","subject":""}`, "NotSigned"},
		{"hash mismatch", `{"status":"HashMismatch","signer":"TASK RUNNER TECHNOLOGY LTD","subject":"CN=TASK RUNNER TECHNOLOGY LTD"}`, "HashMismatch"},
		{"valid, someone else", `{"status":"Valid","signer":"Evil Corp","subject":"CN=Evil Corp, O=Evil Corp"}`, "Evil Corp"},
		{"valid, our name only as a prefix", `{"status":"Valid","signer":"TASK RUNNER TECHNOLOGY LTD EVIL","subject":"CN=TASK RUNNER TECHNOLOGY LTD EVIL, O=X"}`, "not signed by"},
		{"our name in another attribute", `{"status":"Valid","signer":"x","subject":"CN=x, OU=TASK RUNNER TECHNOLOGY LTD"}`, "not signed by"},
		{"garbage", `not json`, "Authenticode"},
		{"log lines before the JSON", "WARNING: something\n" + `{"status":"Valid","signer":"TASK RUNNER TECHNOLOGY LTD","subject":""}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkAuthenticodeResult([]byte(c.out), windowsPublisher)
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("want accepted, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
			}
		})
	}
}

// The helper runs after this process is gone, so nothing can test it end to
// end off Windows; pin the properties the design depends on.
func TestWindowsUpdaterScriptShape(t *testing.T) {
	for _, want := range []string{
		"param(",
		"[string]$Setup",
		"[string]$InstallDir",
		"[string]$Log",
		"Win32_Process", // ExecutablePath, not Get-Process (#2760)
		"-ieq $prefix",  // only processes under the install dir, CLM-safe
		"[string]$Marker",
		"$left.Count -gt 0", // never install over a till that is still running
		"if ($code -ne 0) { Remove-Item -LiteralPath $Marker", // a failed install drops the marker
		"'unitill-desktop.exe', 'unitill-pos.exe'",            // the shell first
		"'/S /D=' + $dir",        // silent, into the same dir; /D= last and unquoted (NSIS)
		"-WorkingDirectory $dir", // web/ resolves cwd-relative
		"ExitCode",
	} {
		if !strings.Contains(windowsUpdaterScript, want) {
			t.Errorf("windowsUpdaterScript lacks %q", want)
		}
	}
	// Nothing is ever spliced into the script text: every value arrives as a
	// parameter.
	if strings.Contains(windowsUpdaterScript, "%s") || strings.Contains(windowsUpdaterScript, "Invoke-Expression") {
		t.Error("windowsUpdaterScript must take its values as parameters, never as spliced text")
	}
}

func attemptDirs(t *testing.T, root string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(root, "attempt-*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A second Apply while the first is handed over to the installer (applyMu is
// already released) must not race its files.
func TestApplyWindowsRefusesASecondUpdateDuringHandover(t *testing.T) {
	f := newWinFixture(t)
	newReleaseServer(t, "v0.2.0", winAssets([]byte("SETUP")))
	if err := Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	f.waitHelper(t)
	if err := Apply(context.Background()); err == nil || !strings.Contains(err.Error(), "already being applied") {
		t.Fatalf("second Apply err = %v, want already being applied", err)
	}
}

// Earlier attempts are cleared, but updater.log stays: the Problem text sends
// the operator there.
func TestApplyWindowsKeepsTheUpdaterLog(t *testing.T) {
	f := newWinFixture(t)
	if err := os.MkdirAll(filepath.Join(f.work, "attempt-old"), 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(f.work, "updater.log")
	if err := os.WriteFile(logPath, []byte("previous run"), 0o600); err != nil {
		t.Fatal(err)
	}
	newReleaseServer(t, "v0.2.0", winAssets([]byte("SETUP")))
	if err := Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	c := f.waitHelper(t)
	if c.logPath != logPath {
		t.Errorf("log = %q, want %q", c.logPath, logPath)
	}
	if b, _ := os.ReadFile(logPath); string(b) != "previous run" {
		t.Errorf("updater.log = %q, want it kept", b)
	}
	if _, err := os.Stat(filepath.Join(f.work, "attempt-old")); !os.IsNotExist(err) {
		t.Errorf("an earlier attempt folder must be cleared, stat err = %v", err)
	}
}

// The file is checked again right before the helper runs it: time passes
// between the checks and the handover (an open sale, the plugin stop).
func TestApplyWindowsRechecksTheInstallerBeforeRunningIt(t *testing.T) {
	f := newWinFixture(t)
	reexecDelay = 300 * time.Millisecond
	newReleaseServer(t, "v0.2.0", winAssets([]byte("SETUP")))
	if err := Apply(context.Background()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	dirs := attemptDirs(t, f.work)
	if len(dirs) != 1 {
		t.Fatalf("attempt folders = %v", dirs)
	}
	if err := os.WriteFile(filepath.Join(dirs[0], winSetup), []byte("SWAPPED"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case c := <-f.helper:
		t.Fatalf("a swapped installer must not be run, got %+v", c)
	case <-time.After(time.Second):
	}
	if p, ok := openProblem(ProblemKeyUpdateFailed); !ok || !strings.Contains(p.Msg, "checksum mismatch") {
		t.Errorf("want a %s Problem naming the checksum, got %+v %v", ProblemKeyUpdateFailed, p, ok)
	}
	if _, ok := readPending(f.dir); ok {
		t.Error("the marker must be dropped")
	}
	if windowsHandover.Load() {
		t.Error("a failed handover must allow the next attempt")
	}
}
