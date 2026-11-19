// Command armv7sim is the host side of the ut-docs#6300 Android 9 seccomp
// simulator. It packs a tiny initramfs (guest PID 1 + the pristine-libc and
// fork smoke binaries), boots a pinned Linux 4.9 armhf kernel under
// qemu-system-arm, installs the AOSP android-9.0.0_r61 app seccomp filter in
// the guest, and decides the verdict from the console (verdict.go). Test
// tooling only; run it through scripts/ci/sim-armv7-seccomp.sh.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/universaltill/universal-till/scripts/armv7sim/policy"
)

func main() { os.Exit(run()) }

func run() int {
	kernel := flag.String("kernel", "", "path to the pinned vmlinuz")
	initBin := flag.String("init", "", "guestinit binary (linux/arm)")
	pristine := flag.String("pristine", "", "smoke binary built with pristine modernc.org/libc (must be KILLED by the filter)")
	fork := flag.String("fork", "", "smoke binary built with the libc fork (must boot under the filter)")
	logPath := flag.String("log", "sim.log", "console log (always kept)")
	qemu := flag.String("qemu", "qemu-system-arm", "qemu-system-arm binary")
	deadline := flag.Duration("deadline", 900*time.Second, "overall qemu deadline")
	verbose := flag.Bool("v", false, "also print the whole console")
	flag.Parse()
	for n, v := range map[string]string{"-kernel": *kernel, "-init": *initBin, "-pristine": *pristine, "-fork": *fork} {
		if v == "" {
			fmt.Fprintf(os.Stderr, "armv7sim: %s is required\n", n)
			return 2
		}
	}

	prog, err := policy.Assemble()
	if err != nil {
		fmt.Fprintln(os.Stderr, "armv7sim: assemble:", err)
		return 2
	}
	wantSHA := policy.SHA256(prog)

	tmp, err := os.MkdirTemp("", "armv7sim-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "armv7sim:", err)
		return 2
	}
	defer os.RemoveAll(tmp)
	cpio := filepath.Join(tmp, "initramfs.cpio")
	if err := buildInitramfs(cpio, *initBin, *pristine, *fork); err != nil {
		fmt.Fprintln(os.Stderr, "armv7sim: initramfs:", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *deadline)
	defer cancel()
	cmd := exec.CommandContext(ctx, *qemu, "-M", "virt", "-cpu", "cortex-a15", "-smp", "2", "-m", "2048",
		"-nographic", "-no-reboot", "-nic", "none", "-kernel", *kernel, "-initrd", cpio,
		"-append", "console=ttyAMA0 rdinit=/init panic=-1 loglevel=1")
	var console bytes.Buffer
	cmd.Stdout, cmd.Stderr = &console, &console
	start := time.Now()
	runErr := cmd.Run()
	elapsed := time.Since(start).Round(time.Second)

	if err := os.WriteFile(*logPath, console.Bytes(), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "armv7sim: write log:", err)
	}
	if *verbose {
		os.Stdout.Write(console.Bytes())
	}
	fails := Verdict(console.String(), wantSHA)
	if ctx.Err() != nil {
		fails = append(fails, fmt.Sprintf("qemu did not exit within %s", *deadline))
	} else if runErr != nil {
		fails = append(fails, fmt.Sprintf("qemu: %v", runErr))
	}
	fmt.Printf("armv7sim: qemu ran %s, console log %s (%d bytes)\n", elapsed, *logPath, console.Len())
	if len(fails) == 0 {
		fmt.Println("armv7sim: GREEN: pristine libc is KILLED by the AOSP filter (SIGSYS, statx 397); the fork boots (OK 200); the control boots without the filter")
		return 0
	}
	fmt.Println("armv7sim: RED")
	for _, f := range fails {
		fmt.Println("  - " + f)
	}
	for _, l := range strings.Split(console.String(), "\n") {
		if strings.HasPrefix(l, "UTSIM ") {
			fmt.Println("  | " + strings.TrimRight(l, "\r"))
		}
	}
	return 1
}

func buildInitramfs(out, initBin, pristine, fork string) error {
	read := func(p string) ([]byte, error) { return os.ReadFile(p) }
	var files []cpioFile
	for _, e := range []struct{ name, src string }{{"init", initBin}, {"smoke-pristine", pristine}, {"smoke-fork", fork}} {
		b, err := read(e.src)
		if err != nil {
			return err
		}
		files = append(files, cpioFile{Name: e.name, Mode: 0o755, Data: b})
	}
	seed := make([]byte, 64)
	if _, err := rand.Read(seed); err != nil {
		return err
	}
	files = append(files, cpioFile{Name: "seed", Mode: 0o600, Data: seed})
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	defer f.Close()
	return writeCpio(f, files)
}
