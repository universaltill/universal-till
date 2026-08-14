//go:build wasip1

// Test guest for the import dispatcher's staged-file host functions
// (ut-docs#599). Reads the incoming "import.requested.ask" event from stdin
// — whose payload carries a plugin-scoped file HANDLE, never the file
// bytes — then pulls the whole staged file through import_file_read in a
// loop with a deliberately small fixed-size buffer (64KB, smaller than the
// test file, so the test proves chunked reading actually chunks rather
// than one big read), hashing as it goes. Writes a JSON answer to stdout —
// stdout is the answer channel for ".ask"-suffixed events, same convention
// as export_guest.
package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"unsafe"
)

//go:wasmimport ut import_file_size
func importFileSize(handle int32) int64

//go:wasmimport ut import_file_read
func importFileRead(handle int32, dstPtr, dstCap uint32) int32

//go:wasmimport ut import_file_close
func importFileClose(handle int32) int32

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

// readBufSize is deliberately much smaller than the dispatch test's staged
// file so a correct total byte count is only reachable by looping.
const readBufSize = 64 << 10

// oversizedReadBufSize is deliberately far larger than the host's own
// importFileReadBufCap (256KB, unexported in the host package) — the
// "oversized_read" mode below claims this whole size as dstCap on a single
// import_file_read call, to prove the host clamps dstCap server-side
// regardless of what the guest claims it can hold (ut-docs#615).
const oversizedReadBufSize = 2 << 20 // 2MB

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var ev struct {
		Payload struct {
			EntryKey   string   `json:"entry_key"`
			Entities   []string `json:"entities"`
			FileHandle int32    `json:"file_handle"`
			FileName   string   `json:"file_name"`
			FileSize   int64    `json:"file_size"`
			Mode       string   `json:"mode"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(raw, &ev)
	h := ev.Payload.FileHandle

	if ev.Payload.Mode == "oversized_read" {
		runOversizedRead(h)
		return
	}

	size := importFileSize(h)

	hasher := sha256.New()
	buf := make([]byte, readBufSize)
	bp, bc := ptrOf(buf)
	var total int64
	reads := 0
	for {
		n := importFileRead(h, bp, bc)
		if n < 0 {
			fmt.Printf(`{"ok":false,"error":"import_file_read failed: %d"}`+"\n", n)
			return
		}
		if n == 0 {
			break // EOF
		}
		hasher.Write(buf[:n])
		total += int64(n)
		reads++
	}
	cc := importFileClose(h)
	cc2 := importFileClose(h) // close is idempotent: second close also returns 0

	fmt.Printf(`{"ok":true,"message":"sha256=%x size=%d bytes=%d reads=%d close=%d close_again=%d","counts":{"items":%d}}`+"\n",
		hasher.Sum(nil), size, total, reads, cc, cc2, reads)
}

// runOversizedRead issues exactly one import_file_read call against a 2MB
// buffer (oversizedReadBufSize), claiming the full 2MB as dstCap, then
// reports back exactly how many bytes the host actually wrote — the
// ut-docs#615 host-side dstCap-clamp proof. Deliberately does not loop to
// EOF or hash the content: the whole point is what happens in that ONE
// oversized call, not whether the file can eventually be drained.
func runOversizedRead(h int32) {
	buf := make([]byte, oversizedReadBufSize)
	bp, bc := ptrOf(buf)
	n := importFileRead(h, bp, bc)
	cc := importFileClose(h)
	if n < 0 {
		fmt.Printf(`{"ok":false,"error":"import_file_read failed: %d"}`+"\n", n)
		return
	}
	fmt.Printf(`{"ok":true,"message":"first_read_bytes=%d close=%d","counts":{"first_read_bytes":%d}}`+"\n", n, cc, int(n))
}
