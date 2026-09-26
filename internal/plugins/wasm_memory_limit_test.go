package plugins

import (
	"context"
	"testing"
)

// Hand-assembled wasm modules for the memory cap (ut-docs#2891); no
// toolchain needed. See the WebAssembly binary format, sections 1/3/5/7/10.
var (
	// (module (memory 1100)) — asks for ~69 MiB up front.
	wasmBigInitialMemory = []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x05, 0x04, 0x01, 0x00, 0xcc, 0x08, // memory: min 1100 pages, no max
	}
	// (module (memory 1)
	//   (func (export "grow") (param i32) (result i32) local.get 0 memory.grow))
	wasmGrowMemory = []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x06, 0x01, 0x60, 0x01, 0x7f, 0x01, 0x7f, // type: (i32)->i32
		0x03, 0x02, 0x01, 0x00, // func 0 has type 0
		0x05, 0x03, 0x01, 0x00, 0x01, // memory: min 1 page, no max
		0x07, 0x08, 0x01, 0x04, 'g', 'r', 'o', 'w', 0x00, 0x00, // export "grow"
		0x0a, 0x08, 0x01, 0x06, 0x00, 0x20, 0x00, 0x40, 0x00, 0x0b, // body
	}
)

func TestWasmMemoryCapRefusesOversizedInitialMemory(t *testing.T) {
	w := NewWasmRuntime(t.TempDir())
	defer w.rt.Close(context.Background())
	ctx := context.Background()
	mod, err := w.rt.Instantiate(ctx, wasmBigInitialMemory)
	if err == nil {
		_ = mod.Close(ctx)
		t.Fatalf("a module asking for 1100 pages (> %d-page cap) instantiated", wasmMemoryLimitPages)
	}
	t.Logf("refused as expected: %v", err)
}

func TestWasmMemoryCapFailsGrowPastLimit(t *testing.T) {
	w := NewWasmRuntime(t.TempDir())
	defer w.rt.Close(context.Background())
	ctx := context.Background()
	mod, err := w.rt.Instantiate(ctx, wasmGrowMemory)
	if err != nil {
		t.Fatalf("instantiate grow module: %v", err)
	}
	defer mod.Close(ctx)
	grow := mod.ExportedFunction("grow")

	// Past the cap: memory.grow must return -1, not allocate.
	res, err := grow.Call(ctx, uint64(wasmMemoryLimitPages))
	if err != nil {
		t.Fatalf("grow call trapped instead of returning -1: %v", err)
	}
	if int32(uint32(res[0])) != -1 {
		t.Fatalf("memory.grow(%d) from 1 page = %d, want -1 (cap %d pages)", wasmMemoryLimitPages, int32(uint32(res[0])), wasmMemoryLimitPages)
	}
	// Up to the cap still works, and the module keeps running.
	res, err = grow.Call(ctx, uint64(wasmMemoryLimitPages-1))
	if err != nil || int32(uint32(res[0])) != 1 {
		t.Fatalf("memory.grow(%d) within the cap = %v, %v; want old size 1", wasmMemoryLimitPages-1, res, err)
	}
	if got := mod.Memory().Size(); got != wasmMemoryLimitPages*65536 {
		t.Fatalf("memory size = %d bytes, want exactly the cap %d", got, wasmMemoryLimitPages*65536)
	}
}
