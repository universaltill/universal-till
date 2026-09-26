//go:build windows

package procjob

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
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
