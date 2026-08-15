//go:build wasip1

// Test guest for the "ut" tcp_* host functions (local-hardware transport,
// ut-docs#542). Reads the event from stdin, drives the scenario named in the
// payload ("roundtrip" | "openonly" | "maxhandles" | "readtimeout") against a
// real TCP fixture server, and records every outcome in plugin storage so the
// host-side test can assert on it.
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

//go:wasmimport ut tcp_open
func tcpOpen(hostPtr, hostLen, port, timeoutMs uint32) int32

//go:wasmimport ut tcp_write
func tcpWrite(handle int32, ptr, length uint32) int32

//go:wasmimport ut tcp_read
func tcpRead(handle int32, dstPtr, dstCap, timeoutMs uint32) int32

//go:wasmimport ut tcp_close
func tcpClose(handle int32) int32

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

func open(host string, port, timeoutMs uint32) int32 {
	hp, hl := ptrOf([]byte(host))
	return tcpOpen(hp, hl, port, timeoutMs)
}

func record(results map[string]any) {
	raw, _ := json.Marshal(results)
	kp, kl := ptrOf([]byte("results"))
	vp, vl := ptrOf(raw)
	if code := storageSet(kp, kl, vp, vl); code != 0 {
		fmt.Fprintf(os.Stderr, "storing results failed: %d\n", code)
		os.Exit(1)
	}
	fmt.Println(string(raw))
}

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var event struct {
		Payload struct {
			Mode             string `json:"mode"`
			Host             string `json:"host"`
			Port             uint32 `json:"port"`
			ConnectTimeoutMs uint32 `json:"connect_timeout_ms"`
			ReadTimeoutMs    uint32 `json:"read_timeout_ms"`
			Send             string `json:"send"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(raw, &event)
	p := event.Payload

	switch p.Mode {
	case "maxhandles":
		// Five opens against the same live device: the registry caps a plugin
		// at 4 concurrent handles, so the fifth must fail with -4.
		codes := make([]int32, 0, 5)
		for i := 0; i < 5; i++ {
			codes = append(codes, open(p.Host, p.Port, p.ConnectTimeoutMs))
		}
		for _, h := range codes {
			if h >= 0 {
				tcpClose(h)
			}
		}
		record(map[string]any{"open_codes": codes})

	case "openonly":
		// Open and deliberately leak the handle — the host-side test asserts
		// the runtime's Sync (plugin unload) force-closes it via CloseAll.
		h := open(p.Host, p.Port, p.ConnectTimeoutMs)
		record(map[string]any{"open_code": h})

	case "invalidptrread":
		// ut-docs#614 proof: one tcp_read call with a deliberately
		// out-of-bounds dstPtr must fail WITHOUT consuming any bytes
		// already sitting in the socket's receive buffer — a second,
		// valid-buffer read must still get the full pushed payload.
		h := open(p.Host, p.Port, p.ConnectTimeoutMs)
		if h < 0 {
			record(map[string]any{"open_code": h})
			return
		}
		const invalidGuestPtr = 0xFFFFFF00
		invalidCode := tcpRead(h, invalidGuestPtr, 64, p.ReadTimeoutMs)
		buf := make([]byte, 4096)
		bp, bc := ptrOf(buf)
		rc := tcpRead(h, bp, bc, p.ReadTimeoutMs)
		got := ""
		if rc > 0 && int(rc) <= len(buf) {
			got = string(buf[:rc])
		}
		cc := tcpClose(h)
		record(map[string]any{
			"open_code":    h,
			"invalid_code": invalidCode,
			"read_code":    rc,
			"read_data":    got,
			"close_code":   cc,
		})

	case "readtimeout":
		// The fixture server accepts but never writes: the read must come
		// back -3 after the guest's own read deadline, not hang the event.
		h := open(p.Host, p.Port, p.ConnectTimeoutMs)
		if h < 0 {
			record(map[string]any{"open_code": h})
			return
		}
		buf := make([]byte, 4096)
		bp, bc := ptrOf(buf)
		rc := tcpRead(h, bp, bc, p.ReadTimeoutMs)
		cc := tcpClose(h)
		record(map[string]any{"open_code": h, "read_code": rc, "close_code": cc})

	default: // "roundtrip"
		h := open(p.Host, p.Port, p.ConnectTimeoutMs)
		if h < 0 {
			record(map[string]any{"open_code": h})
			return
		}
		msg := []byte(p.Send)
		mp, ml := ptrOf(msg)
		wc := tcpWrite(h, mp, ml)
		buf := make([]byte, 4096)
		bp, bc := ptrOf(buf)
		rc := tcpRead(h, bp, bc, p.ReadTimeoutMs)
		got := ""
		if rc > 0 && int(rc) <= len(buf) {
			got = string(buf[:rc])
		}
		cc := tcpClose(h)
		cc2 := tcpClose(h) // close is idempotent: second close also returns 0
		record(map[string]any{
			"open_code":        h,
			"write_code":       wc,
			"read_code":        rc,
			"read_data":        got,
			"close_code":       cc,
			"close_again_code": cc2,
		})
	}
}
