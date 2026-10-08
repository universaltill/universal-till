//go:build wasip1

// Test guest for the "ut" view_query host function (ADR-0121 §5 core read
// views, ut-docs#3158). Reads the event from stdin, calls view_query with
// payload.view / payload.args into a payload.cap-byte buffer, and records
// the return code and the bytes written in plugin storage so the host-side
// test can assert on them.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"github.com/universaltill/universal-till/sdk/plugin"
)

// raw on purpose: the "cap" mode hands the host a caller-chosen, undersized
// dstCap to observe the buffer ABI itself (full length back, prefix
// written); plugin.ViewQuery always retries at the full size.
//
//go:wasmimport ut view_query
func rawViewQuery(namePtr, nameLen, argsPtr, argsLen, dstPtr, dstCap uint32) int32

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

type viewPayload struct {
	View   string `json:"view"`
	Args   string `json:"args"`
	Cap    int    `json:"cap"`
	Repeat int    `json:"repeat"`
}

// code maps an SDK error back to the host's negative return code.
func code(err error) int32 {
	var e *plugin.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return -3
}

func main() {
	plugin.Run(plugin.Handlers{"*": handle})
}

func handle(ev plugin.Event) (any, error) {
	var p viewPayload
	_ = ev.Decode(&p)

	var first int32
	var written string
	var codes []int32
	if p.Cap > 0 {
		first, written = rawCappedQuery(p.View, p.Args, p.Cap)
		codes = []int32{first}
	} else {
		var args any // nil: the SDK sends {}
		if p.Args != "" {
			args = json.RawMessage(p.Args)
		}
		calls := max(p.Repeat, 1)
		for i := range calls {
			out, err := plugin.ViewQuery(p.View, args)
			c := int32(len(out))
			if err != nil {
				c = code(err)
			}
			if i == 0 {
				first = c
				if err == nil {
					written = string(out)
				}
			}
			codes = append(codes, c)
		}
	}

	out, _ := json.Marshal(map[string]any{"code": first, "codes": codes, "result": written})
	if err := plugin.StorageSet("results", out); err != nil {
		return nil, fmt.Errorf("storing results failed: %w", err)
	}
	return nil, nil
}

// rawCappedQuery is one view_query into a capBytes-byte buffer: the host's
// return (the full length, or a negative code) and the bytes it wrote.
func rawCappedQuery(view, args string, capBytes int) (int32, string) {
	name, a, dst := []byte(view), []byte(args), make([]byte, capBytes)
	np, nl := ptrOf(name)
	ap, al := ptrOf(a)
	dp, dc := ptrOf(dst)
	c := rawViewQuery(np, nl, ap, al, dp, dc)
	runtime.KeepAlive(name)
	runtime.KeepAlive(a)
	runtime.KeepAlive(dst)
	if c <= 0 {
		return c, ""
	}
	return c, string(dst[:min(int(c), len(dst))])
}
