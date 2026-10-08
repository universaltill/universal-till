//go:build wasip1

// Test guest for the "ut" tcp_* host functions (local-hardware transport,
// ut-docs#542), built on the Go guest SDK (ADR-0121 F4, ut-docs#3951) so the
// host tests exercise the SDK's bindings. Drives the scenario named in the
// payload ("roundtrip" | "openonly" | "maxhandles" | "readtimeout" |
// "invalidptrread") against a real TCP fixture server, and records every
// outcome in plugin storage so the host-side test can assert on it.
//
// Codes reported: the handle for an open, a write's or read's byte count
// (0 at end of stream), 0 for a close, or the host's negative code.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/universaltill/universal-till/sdk/plugin"
)

// raw on purpose: the SDK only passes real guest buffers, so it cannot make
// the out-of-bounds dstPtr call the ut-docs#614 proof needs.
//
//go:wasmimport ut tcp_read
func rawTCPRead(handle int32, dstPtr, dstCap, timeoutMs uint32) int32

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

type payload struct {
	Mode             string `json:"mode"`
	Host             string `json:"host"`
	Port             uint32 `json:"port"`
	ConnectTimeoutMs uint32 `json:"connect_timeout_ms"`
	ReadTimeoutMs    uint32 `json:"read_timeout_ms"`
	Send             string `json:"send"`
}

func ms(n uint32) time.Duration { return time.Duration(n) * time.Millisecond }

// open dials and returns the conn plus its code: the handle, or the error.
func open(p payload) (*plugin.TCPConn, int32) {
	c, err := plugin.TCPOpen(p.Host, int(p.Port), ms(p.ConnectTimeoutMs))
	if err != nil {
		return nil, code(err)
	}
	c.ReadTimeout = ms(p.ReadTimeoutMs)
	return c, c.Handle()
}

func record(results map[string]any) (any, error) {
	raw, _ := json.Marshal(results)
	if err := plugin.StorageSet("results", raw); err != nil {
		return nil, fmt.Errorf("storing results failed: %d", code(err))
	}
	return append(raw, '\n'), nil
}

// readOnce is one Read into a 4 KiB buffer: its code and the data read.
func readOnce(c *plugin.TCPConn) (int32, string) {
	buf := make([]byte, 4096)
	rc := countCode(c.Read(buf))
	if rc > 0 {
		return rc, string(buf[:rc])
	}
	return rc, ""
}

func main() { plugin.Run(plugin.Handlers{"*": handle}) }

func handle(ev plugin.Event) (any, error) {
	var p payload
	_ = ev.Decode(&p)

	switch p.Mode {
	case "maxhandles":
		// Five opens against the same live device: the registry caps a plugin
		// at 4 concurrent handles, so the fifth must fail with -4.
		codes := make([]int32, 0, 5)
		var conns []*plugin.TCPConn
		for i := 0; i < 5; i++ {
			c, oc := open(p)
			codes = append(codes, oc)
			if c != nil {
				conns = append(conns, c)
			}
		}
		for _, c := range conns {
			_ = c.Close()
		}
		return record(map[string]any{"open_codes": codes})

	case "openonly":
		// Open and deliberately leak the handle — the host-side test asserts
		// the runtime's Sync (plugin unload) force-closes it via CloseAll.
		_, oc := open(p)
		return record(map[string]any{"open_code": oc})

	case "invalidptrread":
		// ut-docs#614 proof: one tcp_read call with a deliberately
		// out-of-bounds dstPtr must fail WITHOUT consuming any bytes
		// already sitting in the socket's receive buffer — a second,
		// valid-buffer read must still get the full pushed payload.
		c, oc := open(p)
		if c == nil {
			return record(map[string]any{"open_code": oc})
		}
		const invalidGuestPtr = 0xFFFFFF00
		invalidCode := rawTCPRead(c.Handle(), invalidGuestPtr, 64, p.ReadTimeoutMs)
		rc, got := readOnce(c)
		cc := code(c.Close())
		return record(map[string]any{
			"open_code":    oc,
			"invalid_code": invalidCode,
			"read_code":    rc,
			"read_data":    got,
			"close_code":   cc,
		})

	case "readtimeout":
		// The fixture server accepts but never writes: the read must come
		// back -3 after the guest's own read deadline, not hang the event.
		c, oc := open(p)
		if c == nil {
			return record(map[string]any{"open_code": oc})
		}
		rc, _ := readOnce(c)
		cc := code(c.Close())
		return record(map[string]any{"open_code": oc, "read_code": rc, "close_code": cc})

	default: // "roundtrip"
		c, oc := open(p)
		if c == nil {
			return record(map[string]any{"open_code": oc})
		}
		wc := countCode(c.Write([]byte(p.Send)))
		rc, got := readOnce(c)
		cc := code(c.Close())
		cc2 := code(c.Close()) // close is idempotent: second close also returns 0
		return record(map[string]any{
			"open_code":        oc,
			"write_code":       wc,
			"read_code":        rc,
			"read_data":        got,
			"close_code":       cc,
			"close_again_code": cc2,
		})
	}
}
