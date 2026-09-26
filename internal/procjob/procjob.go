// Package procjob ties a child process's life to the current process's
// (ut-docs#2760).
//
// Why: on Windows, when the desktop shell (unitill-desktop.exe) died without
// running its deferred cleanup — the WebView2 access violation of
// ut-docs#2761, or a taskkill — the unitill-pos.exe it had spawned kept
// running on its own. The orphan held the data directory and
// unitill-pos.exe's image file, so the next launch showed no window and the
// installer could not overwrite the exe. Windows has no parent-death signal;
// the documented mechanism is a Job object with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE: the OS closes this process's handle to
// the job when it exits for any reason, crash included, and then kills every
// process still in the job.
//
// Only the child goes in the job, never the calling process: the shell may
// open the default browser (ut-docs#2761's fallback), and a browser started
// from inside a kill-on-close job would be killed along with the shell.
// Processes the child itself starts (hardware plugins) join the job
// automatically, which is what we want — they die with their server.
//
// Other platforms: a no-op. The Linux till normally runs unitill-pos as a
// systemd service the shell only attaches to, and macOS stops the server
// from showWindow before the process exits.
package procjob

import "os"

// KillWithParent puts p in a new kill-on-close job so the OS kills it when
// the calling process exits, however it exits. The job handle is
// deliberately never closed or returned: closing it would kill p, and only
// the caller's exit should do that.
//
// On error p is left running normally; callers log and carry on, because a
// till that opens without this protection beats one that doesn't open.
func KillWithParent(p *os.Process) error {
	return killWithParent(p)
}
