//go:build windows

package procjob

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// TestChildDiesWhenItsParentIsKilled models the field crash (ut-docs#2760):
// a "shell" process spawns a "server", ties it with KillWithParent, and is
// then killed outright — no deferred cleanup runs. The server must die too.
// Runs only on Windows (no Windows CI runner today; `GOOS=windows go vet` in
// ci.yml keeps it compiling).
func TestChildDiesWhenItsParentIsKilled(t *testing.T) {
	switch os.Getenv("PROCJOB_ROLE") {
	case "server":
		time.Sleep(time.Minute)
		os.Exit(0)
	case "shell":
		srv := exec.Command(os.Args[0], "-test.run=^TestChildDiesWhenItsParentIsKilled$")
		srv.Env = append(os.Environ(), "PROCJOB_ROLE=server")
		if err := srv.Start(); err != nil {
			fmt.Println("error", err)
			os.Exit(1)
		}
		if err := KillWithParent(srv.Process); err != nil {
			fmt.Println("error", err)
			os.Exit(1)
		}
		fmt.Println("pid", srv.Process.Pid)
		time.Sleep(time.Minute)
		os.Exit(0)
	}

	shell := exec.Command(os.Args[0], "-test.run=^TestChildDiesWhenItsParentIsKilled$")
	shell.Env = append(os.Environ(), "PROCJOB_ROLE=shell")
	out, err := shell.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := shell.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || !strings.HasPrefix(line, "pid ") {
		_ = shell.Process.Kill()
		t.Fatalf("shell said %q, %v; want \"pid N\"", line, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "pid ")))
	if err != nil {
		_ = shell.Process.Kill()
		t.Fatal(err)
	}
	srv, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		_ = shell.Process.Kill()
		t.Fatalf("open server %d: %v", pid, err)
	}
	defer windows.CloseHandle(srv)
	if ev, _ := windows.WaitForSingleObject(srv, 0); ev == windows.WAIT_OBJECT_0 {
		_ = shell.Process.Kill()
		t.Fatal("server exited before its shell was killed")
	}

	_ = shell.Process.Kill() // TerminateProcess: no cleanup runs, like a crash
	_ = shell.Wait()

	if ev, _ := windows.WaitForSingleObject(srv, 10_000); ev != windows.WAIT_OBJECT_0 {
		_ = windows.TerminateProcess(srv, 1)
		t.Fatal("server still running 10s after its shell was killed")
	}
}

// TestJobLetsAChildBreakAway: the in-app updater (internal/selfupdate,
// ut-docs#160) starts its helper with CREATE_BREAKAWAY_FROM_JOB so the
// helper outlives the shell and server it stops; it refuses to start at all
// when the server sits in a kill-on-close job that forbids breakaway. So the
// job must carry JOB_OBJECT_LIMIT_BREAKAWAY_OK. The bound child does what the
// updater does — start a process with that flag — and reports the result;
// without BREAKAWAY_OK, CreateProcess fails with ERROR_ACCESS_DENIED.
func TestJobLetsAChildBreakAway(t *testing.T) {
	switch os.Getenv("PROCJOB_ROLE") {
	case "leaf":
		os.Exit(0)
	case "probe":
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n') // wait until bound
		leaf := exec.Command(os.Args[0], "-test.run=^TestJobLetsAChildBreakAway$")
		leaf.Env = append(os.Environ(), "PROCJOB_ROLE=leaf")
		leaf.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x01000000} // CREATE_BREAKAWAY_FROM_JOB
		if err := leaf.Run(); err != nil {
			fmt.Println("error", err)
			os.Exit(1)
		}
		fmt.Println("ok")
		os.Exit(0)
	}
	probe := exec.Command(os.Args[0], "-test.run=^TestJobLetsAChildBreakAway$")
	probe.Env = append(os.Environ(), "PROCJOB_ROLE=probe")
	in, err := probe.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := probe.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Process.Kill(); _ = probe.Wait() }()
	if err := KillWithParent(probe.Process); err != nil {
		t.Fatalf("KillWithParent: %v", err)
	}
	_, _ = in.Write([]byte("go\n"))
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "ok" {
		t.Fatalf("breakaway start from inside the job: %q, %v; want \"ok\"", strings.TrimSpace(line), err)
	}
}
