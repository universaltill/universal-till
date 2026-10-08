//go:build wasip1

// Test guest for the "ut" view_query host function (ADR-0121 §5 core read
// views, ut-docs#3158). Reads the event from stdin, calls view_query with
// payload.view / payload.args into a payload.cap-byte buffer, and records
// the return code and the bytes written in plugin storage so the host-side
// test can assert on them.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"unsafe"
)

//go:wasmimport ut storage_set
func storageSet(kPtr, kLen, vPtr, vLen uint32) int32

//go:wasmimport ut view_query
func viewQuery(namePtr, nameLen, argsPtr, argsLen, dstPtr, dstCap uint32) int32

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var event struct {
		Payload struct {
			View   string `json:"view"`
			Args   string `json:"args"`
			Cap    int    `json:"cap"`
			Repeat int    `json:"repeat"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(raw, &event)
	p := event.Payload
	if p.Cap == 0 {
		p.Cap = 512 << 10
	}

	name := []byte(p.View)
	args := []byte(p.Args)
	dst := make([]byte, p.Cap)
	np, nl := ptrOf(name)
	ap, al := ptrOf(args)
	dp, dc := ptrOf(dst)
	code := viewQuery(np, nl, ap, al, dp, dc)
	codes := []int32{code}
	for i := 1; i < p.Repeat; i++ {
		codes = append(codes, viewQuery(np, nl, ap, al, dp, dc))
	}

	written := ""
	if code > 0 {
		n := int(code)
		if n > len(dst) {
			n = len(dst)
		}
		written = string(dst[:n])
	}
	out, _ := json.Marshal(map[string]any{"code": code, "codes": codes, "result": written})
	kp, kl := ptrOf([]byte("results"))
	vp, vl := ptrOf(out)
	if c := storageSet(kp, kl, vp, vl); c != 0 {
		fmt.Fprintf(os.Stderr, "storing results failed: %d\n", c)
		os.Exit(1)
	}
}
