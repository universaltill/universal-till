//go:build wasip1

// Test guest for ut-docs#3633's regression coverage: a minimal payment
// plugin that answers payment.<key>.authorize the way ut-plugin-payment-
// stripe/-sumup do when they are installed but not yet set up — it reads its
// credential setting (stripe_secret_key) through the REAL settings_get host
// function and declines (exit 2) when the setting is missing or empty,
// approves (exit 0) only when a value is present. The host-side test drives
// it through the real POST /api/pos/tender handler to prove an unconfigured
// payment plugin can never complete a sale or print a receipt.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unsafe"
)

//go:wasmimport ut settings_get
func settingsGet(kPtr, kLen, dstPtr, dstCap uint32) int32

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

// readSetting returns the plain setting value, or "" when the host reports
// it unset (any negative code) — the buffer ABI's "call again with a bigger
// buffer" is honoured so a long key never reads as empty.
func readSetting(key string) string {
	kp, kl := ptrOf([]byte(key))
	buf := make([]byte, 256)
	bp, bc := ptrOf(buf)
	n := settingsGet(kp, kl, bp, bc)
	if n > int32(len(buf)) {
		buf = make([]byte, n)
		bp, bc = ptrOf(buf)
		n = settingsGet(kp, kl, bp, bc)
	}
	if n <= 0 || n > int32(len(buf)) {
		return ""
	}
	return string(buf[:n])
}

func main() {
	_, _ = io.ReadAll(os.Stdin) // the event; this guest's verdict depends only on its setting
	if strings.TrimSpace(readSetting("stripe_secret_key")) == "" {
		fmt.Fprintln(os.Stderr, "payment plugin not configured: stripe_secret_key is unset")
		os.Exit(2)
	}
	fmt.Println(`{"approved":true}`)
}
