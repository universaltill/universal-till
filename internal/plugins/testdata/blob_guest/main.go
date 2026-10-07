//go:build wasip1

// Test guest for the "ut" blob_* host functions (ADR-0121 §3/§6 blob:own,
// ut-docs#3870). Reads the event from stdin, runs payload.ops in order, and
// records every outcome in plugin storage so the host-side test can assert
// on it. An op's "h" names the index of an earlier op whose return code is
// the handle to use.
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

//go:wasmimport ut blob_put_open
func blobPutOpen(namePtr, nameLen uint32) int32

//go:wasmimport ut blob_write
func blobWrite(h int32, ptr, length uint32) int32

//go:wasmimport ut blob_commit
func blobCommit(h int32) int32

//go:wasmimport ut blob_get_open
func blobGetOpen(namePtr, nameLen uint32) int32

//go:wasmimport ut blob_read
func blobRead(h int32, dstPtr, dstCap uint32) int32

//go:wasmimport ut blob_delete
func blobDelete(namePtr, nameLen uint32) int32

//go:wasmimport ut blob_list
func blobList(dstPtr, dstCap uint32) int32

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

type op struct {
	Op   string `json:"op"`
	Name string `json:"name"`
	H    int    `json:"h"`
	Data string `json:"data"`
	N    int    `json:"n"`   // write_n: this many 'x' bytes, in 64 KiB pieces
	Cap  int    `json:"cap"` // read_all / list: buffer size
}

type result struct {
	Code  int32   `json:"code"`
	Codes []int32 `json:"codes,omitempty"`
	Data  string  `json:"data,omitempty"`
}

func run(o op, results []result) result {
	h := int32(-999)
	if o.H >= 0 && o.H < len(results) {
		h = results[o.H].Code
	}
	switch o.Op {
	case "put_open":
		np, nl := ptrOf([]byte(o.Name))
		return result{Code: blobPutOpen(np, nl)}
	case "get_open":
		np, nl := ptrOf([]byte(o.Name))
		return result{Code: blobGetOpen(np, nl)}
	case "delete":
		np, nl := ptrOf([]byte(o.Name))
		return result{Code: blobDelete(np, nl)}
	case "write":
		bp, bl := ptrOf([]byte(o.Data))
		return result{Code: blobWrite(h, bp, bl)}
	case "write_n":
		buf := make([]byte, 64<<10)
		for i := range buf {
			buf[i] = 'x'
		}
		var r result
		for left := o.N; left > 0; {
			n := len(buf)
			if left < n {
				n = left
			}
			bp, bl := ptrOf(buf[:n])
			c := blobWrite(h, bp, bl)
			r.Codes = append(r.Codes, c)
			r.Code = c
			if c < 0 {
				break
			}
			left -= n
		}
		return r
	case "commit":
		return result{Code: blobCommit(h)}
	case "read_all":
		capacity := o.Cap
		if capacity <= 0 {
			capacity = 4096
		}
		buf := make([]byte, capacity)
		bp, bc := ptrOf(buf)
		var r result
		var got []byte
		for i := 0; i < 100000; i++ {
			c := blobRead(h, bp, bc)
			r.Codes = append(r.Codes, c)
			r.Code = c
			if c <= 0 {
				break
			}
			got = append(got, buf[:c]...)
		}
		r.Data = string(got)
		return r
	case "read":
		buf := make([]byte, o.Cap)
		bp, bc := ptrOf(buf)
		c := blobRead(h, bp, bc)
		r := result{Code: c}
		if c > 0 {
			r.Data = string(buf[:c])
		}
		return r
	case "list":
		buf := make([]byte, o.Cap)
		bp, bc := ptrOf(buf)
		c := blobList(bp, bc)
		r := result{Code: c}
		if c > 0 && int(c) <= len(buf) {
			r.Data = string(buf[:c])
		}
		return r
	}
	return result{Code: -999}
}

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var ev struct {
		Payload struct {
			Ops []op `json:"ops"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(raw, &ev); err != nil {
		fmt.Fprintf(os.Stderr, "bad event: %v\n", err)
		os.Exit(1)
	}
	results := []result{}
	for _, o := range ev.Payload.Ops {
		results = append(results, run(o, results))
	}
	out, _ := json.Marshal(map[string]any{"results": results})
	kp, kl := ptrOf([]byte("results"))
	vp, vl := ptrOf(out)
	if code := storageSet(kp, kl, vp, vl); code != 0 {
		fmt.Fprintf(os.Stderr, "storing results failed: %d\n", code)
		os.Exit(1)
	}
}
