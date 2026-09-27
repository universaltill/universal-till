package selfupdate

// Windows in-app update (ut-docs#160).
//
// A running .exe can't be renamed over the way Apply swaps a unix binary, and
// the release ships an NSIS installer rather than a Windows archive. So on
// Windows, Apply downloads the release's unitill-pos-setup-<v>.exe instead
// of swapping anything itself. It verifies the installer twice before
// running it: the SHA-256 against checksums.txt, then the Authenticode
// signature (status Valid, signer TASK RUNNER TECHNOLOGY LTD). Then a
// detached PowerShell helper stops the shell and the server, runs the
// installer silently into the same per-user directory, and relaunches the
// app. This is the macOS .dmg path's shape (applyMacApp). Every failure
// before the helper starts deletes the download and keeps the running
// version.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paths"
)

// windowsPublisher is the Authenticode signer of every release installer
// (release.yml's Windows signing job; SIGN_PUBLISHER in
// packaging/windows/setup-signing.sh).
const windowsPublisher = "TASK RUNNER TECHNOLOGY LTD"

// windowsUninstaller is what the NSIS installer writes next to the exe
// (installer.nsi WriteUninstaller). It tells an installer install apart
// from a portable zip extraction, which the installer must not take over.
const windowsUninstaller = "uninstall.exe"

var (
	// verifyAuthenticodeFn checks the installer's signature
	// (authenticode_windows.go; an error everywhere else).
	verifyAuthenticodeFn = verifyAuthenticode
	// startWindowsHelperFn starts the detached helper
	// (winhelper_windows.go; an error everywhere else).
	startWindowsHelperFn = startWindowsHelper
	// windowsUpdateDir holds the download, the helper script and its log.
	// It sits in the data dir, which outlives both the process and the
	// install directory the installer rewrites.
	windowsUpdateDir = func() string { return paths.Data("updates") }
	// windowsWatchdogAfter: stopping the till, a silent install and a
	// relaunch take well under a minute. A process still running after
	// this means the helper never got that far.
	windowsWatchdogAfter = 5 * time.Minute
)

// windowsHandover is set once an update has been handed to the installer
// path. applyMu is released when Apply returns, but this process lives on
// through the delay, the plugin stop and the helper's run. A second Apply in
// that window (the nightly tick, the follow tick, a click) is refused
// instead of racing the first one's files.
var windowsHandover atomic.Bool

func windowsSetupName(version string) string {
	return fmt.Sprintf("unitill-pos-setup-%s.exe", version)
}

// windowsInstallerInstall reports whether exe belongs to a per-user
// installer install that the installer can update in place: its directory is
// writable (installer.nsi installs to %LOCALAPPDATA%, RequestExecutionLevel
// user) and holds the installer's uninstall.exe.
func windowsInstallerInstall(exe string) bool {
	dir := filepath.Dir(exe)
	if info, err := os.Stat(filepath.Join(dir, windowsUninstaller)); err != nil || !info.Mode().IsRegular() {
		return false
	}
	return dirWritable(dir)
}

func applyWindowsInstaller(ctx context.Context, exe, version string, idle func() bool) error {
	log := logging.L()
	rel, err := fetchRelease(ctx, version)
	if err != nil {
		return err
	}
	version = strings.TrimPrefix(rel.TagName, "v")
	setupName := windowsSetupName(version)
	setupURL, checksumsURL := "", ""
	for _, a := range rel.Assets {
		switch a.Name {
		case setupName:
			setupURL = a.URL
		case "checksums.txt":
			checksumsURL = a.URL
		}
	}
	if setupURL == "" {
		return fmt.Errorf("no Windows installer in release v%s (%s)", version, setupName)
	}
	// Fail closed, as on every other platform: a release without
	// checksums.txt is broken or tampered with.
	if checksumsURL == "" {
		return fmt.Errorf("release v%s has no checksums.txt — refusing to install an unverified update", version)
	}

	root := windowsUpdateDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create update folder: %w", err)
	}
	// Only this feature writes here. Clear what earlier attempts left, but
	// keep updater.log: the Problem text points the operator at it.
	if old, err := filepath.Glob(filepath.Join(root, "attempt-*")); err == nil {
		for _, d := range old {
			_ = os.RemoveAll(d)
		}
	}
	work, err := os.MkdirTemp(root, "attempt-*")
	if err != nil {
		return fmt.Errorf("create update folder: %w", err)
	}
	setupPath := filepath.Join(work, setupName)
	discard := func(e error) error { _ = os.RemoveAll(work); return e }

	if err := download(ctx, setupURL, setupPath); err != nil {
		return discard(fmt.Errorf("download: %w", err))
	}
	want, err := checksumFor(ctx, checksumsURL, setupName)
	if err != nil {
		return discard(err)
	}
	if err := verifySHA256(setupPath, want); err != nil {
		return discard(err)
	}
	// The checksum proves this is the file the release lists; the signature
	// proves the release itself came from us.
	if err := verifyAuthenticodeFn(ctx, setupPath, windowsPublisher); err != nil {
		return discard(fmt.Errorf("installer signature check failed: %w", err))
	}

	// Verified, nothing changed yet. An unattended caller holds here until
	// no sale is open (ut-docs#2738); a shutdown cancels ctx.
	if err := waitIdle(ctx, idle); err != nil {
		return discard(fmt.Errorf("waiting for no open sale: %w", err))
	}

	script := filepath.Join(work, "ut-updater.ps1")
	if err := os.WriteFile(script, []byte(windowsUpdaterScript), 0o600); err != nil {
		return discard(fmt.Errorf("write update helper: %w", err))
	}
	installDir := filepath.Dir(exe)
	// The new build clears this marker when it starts. The helper deletes
	// it when the installer fails or never runs, so the old build starting
	// again does not claim an update is waiting.
	if err := writePending(installDir, version, time.Now()); err != nil {
		log.Warnf("[selfupdate] could not record the pending restart: %v", err)
	}
	log.Infof("[selfupdate] Windows installer v%s verified — the till restarts to install it", version)

	plan := newRestartPlan(exe, version, "")
	plan.idle = idle
	plan.watchdogAfter = windowsWatchdogAfter
	windowsHandover.Store(true)
	go handOverToInstaller(plan, startWindowsHelperFn, want, script, setupPath, installDir, filepath.Join(root, "updater.log"))
	return nil
}

// handOverToInstaller is restartInto for Windows. After the delay (so the
// HTTP response flushes), it re-checks that no sale is open and stops the
// hardware plugins. Then it starts the helper, which ends this process.
// start is startWindowsHelperFn, read once by the caller like every other
// seam in p. sha256 is the verified checksum, re-checked right before the
// helper runs the file.
func handOverToInstaller(p restartPlan, start func(script, setup, installDir, logPath, marker string) error, sha256, script, setup, installDir, logPath string) {
	time.Sleep(p.delay)
	_ = waitIdle(context.Background(), p.idle)
	done := make(chan struct{})
	go func() {
		p.hook(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(p.hookBound):
		logging.L().Warnf("[selfupdate] stopping plugins before the update took over %v — continuing anyway", p.hookBound)
	}
	// A sale opened while the plugins stopped still must not be cut off.
	_ = waitIdle(context.Background(), p.idle)

	log := logging.L()
	marker := filepath.Join(installDir, pendingFile)
	// Time has passed since the checks (a sale, the plugin stop): the file
	// must still be the one that was verified.
	err := verifySHA256(setup, sha256)
	if err == nil {
		err = start(script, setup, installDir, logPath, marker)
	}
	if err != nil {
		_ = os.Remove(marker)
		_ = os.RemoveAll(filepath.Dir(setup))
		windowsHandover.Store(false)
		log.WarnProblemf(ProblemKeyUpdateFailed,
			"[selfupdate] update to v%s could not start the installer (%v); kept version v%s — hardware plugins stay stopped until the till restarts",
			p.version, err, p.running)
		return
	}
	log.Infof("[selfupdate] installer helper started for v%s (log: %s)", p.version, logPath)
	time.Sleep(p.watchdogAfter)
	log.WarnProblemf(ProblemKeyRestartPending,
		"[selfupdate] the v%s installer was started but v%s is still running — see %s; restart the till or install the update by hand",
		p.version, p.running, logPath)
}

// authenticodeResult is what the PowerShell check prints (see
// authenticode_windows.go).
type authenticodeResult struct {
	Status  string `json:"status"`
	Signer  string `json:"signer"`  // the certificate's simple name (CN)
	Subject string `json:"subject"` // the full distinguished name
}

// checkAuthenticodeResult accepts only a Valid signature whose signer (the
// certificate's CN) is publisher, compared whole and case-insensitively. It is kept apart from the
// PowerShell call so it can be tested on any OS.
func checkAuthenticodeResult(out []byte, publisher string) error {
	// PowerShell may print warnings first; the JSON is the last line.
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var r authenticodeResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(lines[len(lines)-1])), &r); err != nil {
		return fmt.Errorf("unreadable Authenticode result: %w", err)
	}
	if r.Status != "Valid" {
		return fmt.Errorf("signature status %s", r.Status)
	}
	if strings.EqualFold(strings.TrimSpace(r.Signer), publisher) {
		return nil
	}
	return fmt.Errorf("not signed by %s (signer %q, subject %q)", publisher, r.Signer, r.Subject)
}

// errNotWindows is what the Windows-only system calls return elsewhere.
var errNotWindows = errors.New("only available on Windows")

// windowsUpdaterScript is the detached helper. It gets its values only as
// parameters, never spliced into the text. It stops the shell and then the
// server (only copies whose image is under -InstallDir), and runs the
// installer silently into that directory only once it has confirmed both are
// gone. Then it relaunches whatever was running, with the install dir as the
// working directory: the shell if it was running, else the headless server.
// If a process can't be listed or won't stop, it installs nothing and
// relaunches nothing (the till is still running). When the installer fails
// or never runs, it deletes -Marker, so the old build starting again doesn't
// report a pending update. Processes are found through WMI's ExecutablePath
// (#2760). The prefix test uses only string methods and operators, which
// Constrained Language Mode allows. Everything is logged to -Log.
const windowsUpdaterScript = `param(
  [Parameter(Mandatory = $true)][string]$Setup,
  [Parameter(Mandatory = $true)][string]$InstallDir,
  [Parameter(Mandatory = $true)][string]$Log,
  [Parameter(Mandatory = $true)][string]$Marker
)
function Write-Log([string]$m) {
  try { Add-Content -LiteralPath $Log -Value ((Get-Date).ToString('o') + ' ' + $m) } catch {}
}
$dir = $InstallDir.TrimEnd('\')
$prefix = $dir + '\'
function Get-Till {
  foreach ($n in @('unitill-desktop.exe', 'unitill-pos.exe')) {
    Get-CimInstance Win32_Process -Filter ("Name='" + $n + "'") -ErrorAction Stop | Where-Object {
      $ep = [string]$_.ExecutablePath
      $ep.Length -gt $prefix.Length -and $ep.Substring(0, $prefix.Length) -ieq $prefix
    }
  }
}
function Stop-Update([string]$why) {
  Write-Log ($why + ' - nothing installed')
  Remove-Item -LiteralPath $Marker -Force -ErrorAction SilentlyContinue
  Remove-Item -LiteralPath $Setup -Force -ErrorAction SilentlyContinue
  exit 1
}
Write-Log ('updater start: setup=' + $Setup + ' dir=' + $dir)
$hadShell = $false
try {
  foreach ($p in @(Get-Till)) {
    if ($p.Name -ieq 'unitill-desktop.exe') { $hadShell = $true }
    Write-Log ('stopping ' + $p.Name + ' (pid ' + $p.ProcessId + ')')
    Stop-Process -Id $p.ProcessId -Force -ErrorAction SilentlyContinue
    Wait-Process -Id $p.ProcessId -Timeout 15 -ErrorAction SilentlyContinue
  }
  $left = @(Get-Till)
} catch { Stop-Update ('process lookup failed: ' + $_) }
if ($left.Count -gt 0) { Stop-Update ('still running: ' + (($left | ForEach-Object { $_.Name + ' ' + $_.ProcessId }) -join ', ')) }
Start-Sleep -Seconds 1
$code = -1
try {
  $proc = Start-Process -FilePath $Setup -ArgumentList ('/S /D=' + $dir) -Wait -PassThru -ErrorAction Stop
  $code = $proc.ExitCode
} catch { Write-Log ('installer did not start: ' + $_) }
Write-Log ('installer ExitCode ' + $code)
if ($code -ne 0) { Remove-Item -LiteralPath $Marker -Force -ErrorAction SilentlyContinue }
$shell = Join-Path $dir 'unitill-desktop.exe'
$server = Join-Path $dir 'unitill-pos.exe'
try {
  if ($hadShell -and (Test-Path -LiteralPath $shell)) {
    Start-Process -FilePath $shell -WorkingDirectory $dir -ErrorAction Stop
    Write-Log 'relaunched the desktop shell'
  } else {
    Start-Process -FilePath $server -WorkingDirectory $dir -WindowStyle Hidden -ErrorAction Stop
    Write-Log 'relaunched the till server'
  }
} catch { Write-Log ('relaunch failed: ' + $_) }
Remove-Item -LiteralPath $Setup -Force -ErrorAction SilentlyContinue
`
