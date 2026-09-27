//go:build windows

package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/universaltill/universal-till/internal/logging"
)

const (
	createNewProcessGroup  = 0x00000200
	createBreakawayFromJob = 0x01000000
	createNoWindow         = 0x08000000
)

// powershellExe is the system's Windows PowerShell by absolute path, so a
// powershell.exe earlier on PATH (or in the working directory) is never
// the one that runs.
func powershellExe() string {
	root := os.Getenv("SystemRoot")
	if root == "" {
		root = `C:\Windows`
	}
	return filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
}

// configureHidden keeps a console child from flashing a window over the till.
func configureHidden(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// startWindowsHelper starts the update helper detached from this process.
// The helper has to outlive both this server and the desktop shell it stops.
// So it asks to leave any job this process is in: a kill-on-close job
// (#2760) would otherwise kill the helper the moment it stops the shell,
// before the installer runs. Such a job must allow breakaway
// (JOB_OBJECT_LIMIT_BREAKAWAY_OK). If it does not, the helper is not
// started at all and the till keeps running its current version. A job
// without kill-on-close can't take the helper down, so there the helper
// starts inside it.
func startWindowsHelper(script, setup, installDir, logPath, marker string) error {
	start := func(flags uint32) error {
		cmd := exec.Command(powershellExe(), "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
			"-WindowStyle", "Hidden", "-File", script, "-Setup", setup, "-InstallDir", installDir, "-Log", logPath, "-Marker", marker)
		cmd.Dir = filepath.Dir(script)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	}
	base := uint32(createNewProcessGroup | createNoWindow)
	err := start(base | createBreakawayFromJob)
	if err == nil {
		return nil
	}
	if inKillOnCloseJob() {
		return fmt.Errorf("this process's job does not allow the update helper to leave it (%v); the helper would be killed with the desktop shell", err)
	}
	logging.L().Warnf("[selfupdate] starting the update helper outside this process's job failed (%v); starting it inside", err)
	return start(base)
}

// inKillOnCloseJob reports whether this process is in a job that kills its
// members when the job closes. A nil job handle queries the caller's own job.
// The query fails when the process is in no job.
func inKillOnCloseJob() bool {
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	err := windows.QueryInformationJobObject(0, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil)
	if err != nil {
		return false // in no job
	}
	return info.BasicLimitInformation.LimitFlags&windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE != 0
}
