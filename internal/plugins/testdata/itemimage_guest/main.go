//go:build wasip1

// Test guest for the "ut" item_image_open / item_image_read host functions
// (ADR-0121 R1, ut-docs#4005), built on the Go guest SDK so the host tests
// exercise the SDK's bindings. Runs payload.ops in order and records every
// outcome in plugin storage for the host-side test. An op's "h" names the
// index of an earlier op whose reader to use.
//
// Codes reported: 0 for a successful open (the SDK keeps handle numbers
// private), a read's byte count (0 at end), or the host's negative code.
// Bytes come back base64-encoded (they are JPEG, not UTF-8); "refs" reports
// a sha256 per image instead, so 60 images fit plugin storage's 64 KiB value.
package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"unsafe"

	"github.com/universaltill/universal-till/sdk/plugin"
)

// raw on purpose: a read aimed at a failed open has no SDK reader, and the
// SDK never makes a dstCap-0 call, yet the tests need the host's answer.
//
//go:wasmimport ut item_image_read
func rawItemImageRead(h int32, dstPtr, dstCap uint32) int32

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

type op struct {
	Op   string   `json:"op"`
	ID   string   `json:"id"`
	Role string   `json:"role"`
	IDs  []string `json:"ids"`
	H    int      `json:"h"`
	Cap  int      `json:"cap"`
}

type result struct {
	Code  int32    `json:"code"`
	Codes []int32  `json:"codes,omitempty"`
	Data  string   `json:"data,omitempty"`
	Datas []string `json:"datas,omitempty"`

	r *plugin.ItemImage
}

func target(o op, results []result) (*plugin.ItemImage, int32) {
	if o.H >= 0 && o.H < len(results) {
		t := results[o.H]
		if t.r != nil {
			return t.r, t.r.Handle()
		}
		return nil, t.Code
	}
	return nil, -999
}

func readAll(r *plugin.ItemImage, capacity int) result {
	if capacity <= 0 {
		capacity = 4096
	}
	buf := make([]byte, capacity)
	var res result
	var got []byte
	for i := 0; i < 100000; i++ {
		n, err := r.Read(buf)
		c := int32(n)
		if errors.Is(err, io.EOF) {
			c = 0
		} else if err != nil {
			c = code(err)
		}
		res.Codes = append(res.Codes, c)
		res.Code = c
		if c <= 0 {
			break
		}
		got = append(got, buf[:n]...)
	}
	res.Data = base64.StdEncoding.EncodeToString(got)
	return res
}

func run(o op, results []result) result {
	switch o.Op {
	case "open":
		r, err := plugin.ItemImageOpen(o.ID, o.Role)
		return result{Code: code(err), r: r}
	case "read_all":
		r, _ := target(o, results)
		if r == nil {
			return result{Code: -999}
		}
		return readAll(r, o.Cap)
	case "read": // raw: one call with dstCap = o.Cap
		_, h := target(o, results)
		buf := make([]byte, o.Cap+1)
		p := uint32(uintptr(unsafe.Pointer(&buf[0])))
		c := rawItemImageRead(h, p, uint32(o.Cap))
		runtime.KeepAlive(buf)
		return result{Code: c}
	case "refs": // open + read every id's `ref`, one after another (sha256 hex)
		var res result
		for _, id := range o.IDs {
			r, err := plugin.ItemImageOpen(id, "ref")
			if err != nil {
				res.Codes = append(res.Codes, code(err))
				res.Datas = append(res.Datas, "")
				continue
			}
			b, err := io.ReadAll(r)
			res.Codes = append(res.Codes, code(err))
			sum := sha256.Sum256(b)
			res.Datas = append(res.Datas, hex.EncodeToString(sum[:]))
		}
		return res
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
