//go:build linux && arm

// Command guestinit is PID 1 of the ut-docs#6300 seccomp simulator VM, and
// (as `init apply BIN`) the launcher that installs the Android 9 app seccomp
// filter and execs BIN (the zygote installs it in the forked app process
// instead; the syscall filtering an app thread sees is the same). Test
// tooling only; see scripts/armv7sim and scripts/ci/sim-armv7-seccomp.sh.
package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"github.com/universaltill/universal-till/scripts/armv7sim/policy"
)

const (
	scenarioTimeout = 300 * time.Second
	appUID          = 10000 // an app-like non-root uid, as on a device
)

var childEnv = []string{"GOTRACEBACK=crash", "TMPDIR=/tmp", "HOME=/tmp"}

func fatal(what string, err error) {
	fmt.Printf("UTSIM FATAL %s: %v\n", what, err)
	syscall.Sync()
	syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
	select {} // PID 1 never returns
}

func ioctl(fd int, req uintptr, arg unsafe.Pointer) error {
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, uintptr(arg)); e != 0 {
		return e
	}
	return nil
}

// loUp brings the loopback interface up (SIOCGIFFLAGS/SIOCSIFFLAGS).
func loUp() error {
	fd, err := syscall.Socket(syscall.AF_INET, syscall.SOCK_DGRAM, 0)
	if err != nil {
		return err
	}
	defer syscall.Close(fd)
	var ifr [32]byte
	copy(ifr[:], "lo")
	if err := ioctl(fd, syscall.SIOCGIFFLAGS, unsafe.Pointer(&ifr[0])); err != nil {
		return err
	}
	*(*uint16)(unsafe.Pointer(&ifr[16])) |= syscall.IFF_UP
	return ioctl(fd, syscall.SIOCSIFFLAGS, unsafe.Pointer(&ifr[0]))
}

// seed credits the host-provided random bytes to the kernel pool
// (RNDADDENTROPY): a 4.9 getrandom blocks until the pool is initialised and
// TCG has almost no entropy source. Test VM only, the seed is not a secret.
func seed() error {
	b, err := os.ReadFile("/seed")
	if err != nil {
		return err
	}
	f, err := os.OpenFile("/dev/urandom", os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	buf := make([]byte, 8+len(b)) // struct rand_pool_info {int entropy_count; int buf_size; __u32 buf[]}
	*(*int32)(unsafe.Pointer(&buf[0])) = int32(len(b) * 8)
	*(*int32)(unsafe.Pointer(&buf[4])) = int32(len(b))
	copy(buf[8:], b)
	return ioctl(int(f.Fd()), 0x40085203, unsafe.Pointer(&buf[0]))
}

type fprog struct {
	n uint16
	_ uint16
	p *policy.Ins
}

// apply installs the AOSP filter on this thread and execs bin directly
// (RawSyscall, no Go runtime hooks between install and execve).
func apply(bin string) {
	prog, err := policy.Assemble()
	if err != nil {
		fatal("assemble", err)
	}
	argv0, _ := syscall.BytePtrFromString(bin)
	argv, _ := syscall.SlicePtrFromStrings([]string{bin})
	envv, _ := syscall.SlicePtrFromStrings(childEnv)
	fp := fprog{n: uint16(len(prog)), p: &prog[0]}

	// Drop to an app-like uid before the filter (setuid et al. are not
	// allowed afterwards, and the app must not take root-only paths).
	if err := syscall.Setgroups(nil); err != nil {
		fatal("setgroups", err)
	}
	if err := syscall.Setresgid(appUID, appUID, appUID); err != nil {
		fatal("setresgid", err)
	}
	if err := syscall.Setresuid(appUID, appUID, appUID); err != nil {
		fatal("setresuid", err)
	}

	runtime.LockOSThread()
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, 38 /*PR_SET_NO_NEW_PRIVS*/, 1, 0); e != 0 {
		fatal("PR_SET_NO_NEW_PRIVS", e)
	}
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, 22 /*PR_SET_SECCOMP*/, 2 /*SECCOMP_MODE_FILTER*/, uintptr(unsafe.Pointer(&fp))); e != 0 {
		fatal("PR_SET_SECCOMP", e)
	}
	_, _, e := syscall.RawSyscall(syscall.SYS_EXECVE, uintptr(unsafe.Pointer(argv0)), uintptr(unsafe.Pointer(&argv[0])), uintptr(unsafe.Pointer(&envv[0])))
	fmt.Printf("UTSIM FATAL execve returned %v\n", e)
	os.Exit(99)
}

func scenario(name, bin string, filter bool) {
	fmt.Printf("UTSIM BEGIN %s\n", name)
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()
	var cmd *exec.Cmd
	if filter {
		cmd = exec.CommandContext(ctx, "/init", "apply", bin)
	} else {
		cmd = exec.CommandContext(ctx, bin)
		cmd.Env = childEnv
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stdout
	if err := cmd.Start(); err != nil {
		fatal("start "+name, err)
	}
	_ = cmd.Wait()
	ws := cmd.ProcessState.Sys().(syscall.WaitStatus)
	timeout := ctx.Err() != nil
	fmt.Printf("\nUTSIM END %s exited=%v code=%d signaled=%v signal=%d timeout=%v\n",
		name, ws.Exited(), ws.ExitStatus(), ws.Signaled(), int(ws.Signal()), timeout)
}

func main() {
	if len(os.Args) == 3 && os.Args[1] == "apply" {
		apply(os.Args[2])
		return
	}
	for _, d := range []struct {
		path string
		mode uint32
	}{{"/proc", 0o755}, {"/dev", 0o755}, {"/tmp", 0o1777}} {
		os.MkdirAll(d.path, os.FileMode(d.mode))
	}
	if err := syscall.Mount("proc", "/proc", "proc", 0, ""); err != nil {
		fatal("mount proc", err)
	}
	if err := syscall.Mount("devtmpfs", "/dev", "devtmpfs", 0, ""); err != nil {
		fatal("mount devtmpfs", err)
	}
	if err := syscall.Mount("tmpfs", "/tmp", "tmpfs", 0, ""); err != nil {
		fatal("mount tmpfs", err)
	}
	os.Chmod("/tmp", 0o1777)
	if err := seed(); err != nil {
		fatal("seed entropy", err)
	}
	if err := loUp(); err != nil {
		fatal("bring lo up", err)
	}
	var probe [8]byte
	if _, err := rand.Read(probe[:]); err != nil { // crypto/rand blocks forever if the pool is not ready
		fatal("crypto/rand", err)
	}
	prog, err := policy.Assemble()
	if err != nil {
		fatal("assemble", err)
	}
	var u syscall.Utsname
	syscall.Uname(&u)
	rel := make([]byte, 0, 65)
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		rel = append(rel, byte(c))
	}
	fmt.Printf("UTSIM READY kernel=%s filter_len=%d filter_sha256=%s\n", rel, len(prog), policy.SHA256(prog))

	scenario("control-pristine-nofilter", "/smoke-pristine", false)
	scenario("pristine-filtered", "/smoke-pristine", true)
	scenario("fork-filtered", "/smoke-fork", true)

	fmt.Println("UTSIM DONE")
	syscall.Sync()
	syscall.Reboot(syscall.LINUX_REBOOT_CMD_POWER_OFF)
	select {}
}
