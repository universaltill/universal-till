// Command armv7smoke boots the real mobile entry point (the one the Android
// app calls), makes one HTTP request against it and prints "OK <status>".
//
// It exists for scripts/ci/guard-armv7-seccomp.sh (ut-docs#6300, ADR-0188):
// cross-built for linux/arm and run under qemu-arm with strace, the syscalls
// it makes are compared with what an Android 9 app seccomp filter allows.
// Test support only; never shipped.
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/universaltill/universal-till/mobile"
)

func fail(what string, err error) {
	fmt.Println("FAIL", what, err)
	os.Exit(1)
}

func main() {
	dir, err := os.MkdirTemp("", "armv7smoke")
	if err != nil {
		fail("tempdir", err)
	}
	defer os.RemoveAll(dir)

	addr, err := mobile.Start(dir)
	if err != nil {
		fail("start", err)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get("http://" + addr)
	if err != nil {
		mobile.Stop()
		fail("get", err)
	}
	resp.Body.Close()
	mobile.Stop()
	if resp.StatusCode != http.StatusOK {
		fail("status", fmt.Errorf("%d", resp.StatusCode))
	}
	fmt.Println("OK", resp.StatusCode)
}
