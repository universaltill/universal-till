//go:build wasip1

// Reactor guest (Go 1.24+ -buildmode=c-shared + //go:wasmexport): stays
// resident after _initialize and answers each event through an exported call,
// the mode ADR-0121 §8 adds only if per-event instantiation is too slow.
//
// ABI: the host asks alloc(n) for an input buffer, writes the event there,
// calls handle(ptr, n) and reads the answer at (result >> 32, result & 0xffffffff).
// A zero result means "no answer".
package main

import (
	"unsafe"

	"github.com/universaltill/universal-till/tools/wasmbench/guests/answer"
)

var in, out []byte // kept alive here so the host may read them between calls

//go:wasmexport alloc
func alloc(n uint32) uint32 {
	if uint32(cap(in)) < n {
		in = make([]byte, n)
	}
	in = in[:n]
	return uint32(uintptr(unsafe.Pointer(&in[0])))
}

//go:wasmexport handle
func handle(ptr, n uint32) uint64 {
	_ = ptr // always the buffer alloc returned
	res, ok := answer.Answer(in[:n])
	if !ok {
		return 0
	}
	out = res
	return uint64(uintptr(unsafe.Pointer(&out[0])))<<32 | uint64(len(out))
}

func main() {}
