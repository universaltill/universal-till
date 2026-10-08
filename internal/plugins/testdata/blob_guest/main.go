//go:build wasip1

// Test guest for the "ut" blob_* host functions (ADR-0121 §3/§6 blob:own,
// ut-docs#3870), built on the Go guest SDK (ADR-0121 F4, ut-docs#3951) so the
// host tests exercise the SDK's bindings. Runs payload.ops in order and
// records every outcome in plugin storage so the host-side test can assert
// on it. An op's "h" names the index of an earlier op whose writer/reader to
// use.
//
// Codes reported: 0 for a successful open/commit/delete (the SDK keeps
// handle numbers private), a write's or read's byte count (0 at end of
// stream), the list JSON's length, or the host's negative code.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"unsafe"

	"github.com/universaltill/universal-till/sdk/plugin"
)

// raw on purpose: an op aimed at a failed open has no SDK writer/reader, yet
// the tests need the host's answer for that bogus handle (a denied plugin
// is refused before the handle is looked up).
//
//go:wasmimport ut blob_write
func rawBlobWrite(h int32, ptr, length uint32) int32

//go:wasmimport ut blob_commit
func rawBlobCommit(h int32) int32

//go:wasmimport ut blob_read
func rawBlobRead(h int32, dstPtr, dstCap uint32) int32

// raw on purpose: the SDK's BlobList always reads the whole list, so it
// cannot make the deliberately short buffer-ABI call that must return the
// full length.
//
//go:wasmimport ut blob_list
func rawBlobList(dstPtr, dstCap uint32) int32

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

// code maps an SDK result back to the host's numeric return: 0 for success,
// the negative host code for a *plugin.Error, -3 for any other failure.
func code(err error) int32 {
	var e *plugin.Error
	if errors.As(err, &e) {
		return e.Code
	}
	if err != nil {
		return -3
	}
	return 0
}

// countCode is a byte count, 0 at EOF, or the error code.
func countCode(n int, err error) int32 {
	switch {
	case errors.Is(err, io.EOF):
		return 0
	case err != nil:
		return code(err)
	}
	return int32(n)
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

	w *plugin.BlobWriter
	r *plugin.BlobReader
}

// target is the op o.H names: its writer/reader, if it opened one, and its
// code — the bogus handle a raw call uses when it did not.
func target(o op, results []result) (*plugin.BlobWriter, *plugin.BlobReader, int32) {
	if o.H >= 0 && o.H < len(results) {
		t := results[o.H]
		return t.w, t.r, t.Code
	}
	return nil, nil, -999
}

func write(w *plugin.BlobWriter, h int32, b []byte) int32 {
	if w != nil {
		return countCode(w.Write(b))
	}
	bp, bl := ptrOf(b)
	c := rawBlobWrite(h, bp, bl)
	runtime.KeepAlive(b)
	return c
}

func read(r *plugin.BlobReader, h int32, buf []byte) int32 {
	if r != nil {
		return countCode(r.Read(buf))
	}
	bp, bc := ptrOf(buf)
	c := rawBlobRead(h, bp, bc)
	runtime.KeepAlive(buf)
	return c
}

func run(o op, results []result) result {
	w, r, h := target(o, results)
	switch o.Op {
	case "put_open":
		bw, err := plugin.BlobCreate(o.Name)
		return result{Code: code(err), w: bw}
	case "get_open":
		br, err := plugin.BlobOpen(o.Name)
		return result{Code: code(err), r: br}
	case "delete":
		return result{Code: code(plugin.BlobDelete(o.Name))}
	case "write":
		return result{Code: write(w, h, []byte(o.Data))}
	case "write_n":
		buf := make([]byte, 64<<10)
		for i := range buf {
			buf[i] = 'x'
		}
		var res result
		for left := o.N; left > 0; {
			n := len(buf)
			if left < n {
				n = left
			}
			c := write(w, h, buf[:n])
			res.Codes = append(res.Codes, c)
			res.Code = c
			if c < 0 {
				break
			}
			left -= n
		}
		return res
	case "commit":
		if w != nil {
			return result{Code: code(w.Commit())}
		}
		return result{Code: rawBlobCommit(h)}
	case "read_all":
		capacity := o.Cap
		if capacity <= 0 {
			capacity = 4096
		}
		buf := make([]byte, capacity)
		var res result
		var got []byte
		for i := 0; i < 100000; i++ {
			c := read(r, h, buf)
			res.Codes = append(res.Codes, c)
			res.Code = c
			if c <= 0 {
				break
			}
			got = append(got, buf[:c]...)
		}
		res.Data = string(got)
		return res
	case "read":
		buf := make([]byte, o.Cap)
		c := read(r, h, buf)
		res := result{Code: c}
		if c > 0 {
			res.Data = string(buf[:c])
		}
		return res
	case "list":
		list, err := plugin.BlobList()
		if err != nil {
			return result{Code: code(err)}
		}
		j, _ := json.Marshal(list)
		if o.Cap >= len(j) {
			return result{Code: int32(len(j)), Data: string(j)}
		}
		// A buffer too short for the list: the host must still report the
		// full length.
		buf := make([]byte, o.Cap)
		bp, bc := ptrOf(buf)
		c := rawBlobList(bp, bc)
		runtime.KeepAlive(buf)
		return result{Code: c}
	}
	return result{Code: -999}
}

func main() { plugin.Run(plugin.Handlers{"*": handle}) }

func handle(ev plugin.Event) (any, error) {
	var p struct {
		Ops []op `json:"ops"`
	}
	if err := ev.Decode(&p); err != nil {
		return nil, fmt.Errorf("bad event: %v", err)
	}
	results := []result{}
	for _, o := range p.Ops {
		results = append(results, run(o, results))
	}
	out, _ := json.Marshal(map[string]any{"results": results})
	if err := plugin.StorageSet("results", out); err != nil {
		return nil, fmt.Errorf("storing results failed: %d", code(err))
	}
	return nil, nil
}
