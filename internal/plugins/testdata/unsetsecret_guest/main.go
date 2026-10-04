//go:build wasip1

// Test guest for ut-docs#3633's regression coverage: a payment plugin whose
// required credential setting is NOT configured must decline, and the host
// must turn that decline into a fail-closed tender (402, no sale, basket
// kept). Mirrors the real shape of ut-plugin-payment-stripe /
// ut-plugin-payment-sumup: on ".authorize" it reads its own "secret_key"
// setting via the settings_get host function and exits non-zero when the
// setting is missing (negative host code) or empty. With a non-empty value
// it approves. Exercised through the real compiled module + wazero runtime
// by internal/pages' TestTenderHandler_RealWasmGuest* tests, not a fake Go
// handler.
package main

import (
	"fmt"
	"os"
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

func main() {
	key := []byte("secret_key")
	kp, kl := ptrOf(key)
	buf := make([]byte, 4096)
	bp, bc := ptrOf(buf)
	code := settingsGet(kp, kl, bp, bc)
	if code <= 0 {
		// Not found (negative) or empty (0): unconfigured -> decline.
		fmt.Fprintf(os.Stderr, "unsetsecret_guest: secret_key not configured (settings_get=%d)\n", code)
		fmt.Println(`{"approved":false,"error":"secret_key not configured"}`)
		os.Exit(2)
	}
	if int(code) > len(buf) {
		fmt.Fprintf(os.Stderr, "unsetsecret_guest: secret_key too large (%d)\n", code)
		os.Exit(2)
	}
	fmt.Println(`{"approved":true}`)
}
