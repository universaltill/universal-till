//go:build wasip1

// Test guest for the plugin view upload host functions (ADR-0121 §3/§7,
// ut-docs#3793). Reads a ui.action.ask-shaped event from stdin whose
// payload carries upload_handles — opaque tokens, never the file bytes —
// and pulls the first upload through upload_open / upload_read /
// upload_close with a buffer smaller than the file, hashing as it goes.
// Writes a JSON answer to stdout.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"unsafe"
)

//go:wasmimport ut upload_open
func uploadOpen(tokPtr, tokLen uint32) int32

//go:wasmimport ut upload_read
func uploadRead(handle int32, dstPtr, dstCap uint32) int32

//go:wasmimport ut upload_close
func uploadClose(handle int32) int32

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

const readBufSize = 16 << 10

func open(token string) int32 {
	b := []byte(token)
	p, n := ptrOf(b)
	return uploadOpen(p, n)
}

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var ev struct {
		Payload struct {
			Mode    string `json:"mode"`
			Uploads []struct {
				Field  string `json:"field"`
				Handle string `json:"handle"`
			} `json:"upload_handles"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(raw, &ev)
	if len(ev.Payload.Uploads) == 0 {
		fmt.Println(`{"error":"no upload"}`)
		return
	}
	tok := ev.Payload.Uploads[0].Handle

	switch ev.Payload.Mode {
	case "probe":
		// Only report what upload_open answers (a foreign or malformed
		// token), never read or close.
		fmt.Printf(`{"open":%d}`+"\n", open(tok))
		return
	case "leave_open":
		h := open(tok)
		buf := make([]byte, 4)
		bp, bc := ptrOf(buf)
		n := uploadRead(h, bp, bc)
		fmt.Printf(`{"open_ok":%t,"read":%d}`+"\n", h >= 0, n)
		return
	}

	h := open(tok)
	if h < 0 {
		fmt.Printf(`{"error":"upload_open %d"}`+"\n", h)
		return
	}
	hasher := sha256.New()
	buf := make([]byte, readBufSize)
	bp, bc := ptrOf(buf)
	total, reads := 0, 0
	for {
		n := uploadRead(h, bp, bc)
		if n < 0 {
			fmt.Printf(`{"error":"upload_read %d"}`+"\n", n)
			return
		}
		if n == 0 {
			break
		}
		hasher.Write(buf[:n])
		total += int(n)
		reads++
	}
	cc := uploadClose(h)
	cc2 := uploadClose(h)
	reopen := open(tok) // the token is consumed by close
	fmt.Printf(`{"sha256":"%x","bytes":%d,"reads":%d,"close":%d,"close_again":%d,"reopen":%d}`+"\n",
		hasher.Sum(nil), total, reads, cc, cc2, reopen)
}
