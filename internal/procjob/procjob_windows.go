//go:build windows

package procjob

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

func killWithParent(p *os.Process) error {
	if p == nil {
		return fmt.Errorf("procjob: nil process")
	}
	h, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("procjob: create job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	// BREAKAWAY_OK: a member may start a process outside the job, but only
	// by asking (CREATE_BREAKAWAY_FROM_JOB); anything else it starts still
	// joins. The in-app updater (internal/selfupdate, ut-docs#160) asks, so
	// its helper survives stopping the shell and server; it refuses to run
	// at all inside a kill-on-close job without this.
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(h)
		return fmt.Errorf("procjob: set kill-on-close: %w", err)
	}
	// os.Process doesn't expose its handle; open our own with just the
	// rights AssignProcessToJobObject needs.
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(p.Pid))
	if err != nil {
		_ = windows.CloseHandle(h)
		return fmt.Errorf("procjob: open process %d: %w", p.Pid, err)
	}
	defer windows.CloseHandle(ph)
	if err := windows.AssignProcessToJobObject(h, ph); err != nil {
		_ = windows.CloseHandle(h)
		return fmt.Errorf("procjob: assign process %d: %w", p.Pid, err)
	}
	// h is intentionally leaked: the OS closes it when this process exits,
	// which is what kills p.
	return nil
}
