//go:build wasip1

// Test guest for the "ut" http_open/http_write/http_status/http_read/
// http_close host functions (ADR-0121 §3 http:stream, ut-docs#3156). Reads
// the event from stdin, drives the scenario named in payload.mode, and
// records every outcome in plugin storage so the host-side test can assert
// on it.
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

//go:wasmimport ut http_open
func httpOpen(reqPtr, reqLen uint32) int32

//go:wasmimport ut http_write
func httpWrite(h int32, ptr, length uint32) int32

//go:wasmimport ut http_status
func httpStatus(h int32, dstPtr, dstCap uint32) int32

//go:wasmimport ut http_read
func httpRead(h int32, dstPtr, dstCap uint32) int32

//go:wasmimport ut http_close
func httpClose(h int32) int32

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

func record(results map[string]any) {
	raw, _ := json.Marshal(results)
	kp, kl := ptrOf([]byte("results"))
	vp, vl := ptrOf(raw)
	if code := storageSet(kp, kl, vp, vl); code != 0 {
		fmt.Fprintf(os.Stderr, "storing results failed: %d\n", code)
		os.Exit(1)
	}
}

type payload struct {
	Mode    string            `json:"mode"`
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`     // written in WriteChunk-sized pieces
	BodyLen int               `json:"body_len"` // or: this many 'x' bytes
	Chunk   int               `json:"write_chunk"`
	ReadCap int               `json:"read_cap"`
	Opens   int               `json:"opens"`
}

func open(p payload) int32 {
	req, _ := json.Marshal(map[string]any{"method": p.Method, "url": p.URL, "headers": p.Headers})
	rp, rl := ptrOf(req)
	return httpOpen(rp, rl)
}

// writeBody writes the request body in chunks; returns every write code.
func writeBody(h int32, p payload) []int32 {
	body := []byte(p.Body)
	if p.BodyLen > 0 {
		body = make([]byte, p.BodyLen)
		for i := range body {
			body[i] = 'x'
		}
	}
	chunk := p.Chunk
	if chunk <= 0 {
		chunk = 64 << 10
	}
	var codes []int32
	for off := 0; off < len(body); off += chunk {
		end := off + chunk
		if end > len(body) {
			end = len(body)
		}
		bp, bl := ptrOf(body[off:end])
		c := httpWrite(h, bp, bl)
		codes = append(codes, c)
		if c < 0 {
			break
		}
	}
	return codes
}

func status(h int32) (int32, string) {
	buf := make([]byte, 4096)
	bp, bc := ptrOf(buf)
	n := httpStatus(h, bp, bc)
	if n < 0 || int(n) > len(buf) {
		return n, ""
	}
	return n, string(buf[:n])
}

// readAll reads until EOF (0) or an error code; returns codes and data.
func readAll(h int32, readCap int) ([]int32, string, int) {
	if readCap <= 0 {
		readCap = 64 << 10
	}
	buf := make([]byte, readCap)
	bp, bc := ptrOf(buf)
	var codes []int32
	total := 0
	var data []byte
	for i := 0; i < 100000; i++ {
		n := httpRead(h, bp, bc)
		codes = append(codes, n)
		if n <= 0 {
			break
		}
		total += int(n)
		if room := 4096 - len(data); room > 0 {
			chunk := buf[:n]
			if len(chunk) > room {
				chunk = chunk[:room]
			}
			data = append(data, chunk...)
		}
	}
	return codes, string(data), total
}

// lastCodes keeps a results record small: a 1 MiB body read in 64 KiB
// pieces is many codes; the tests assert on the count and the tail.
func tail(codes []int32) []int32 {
	if len(codes) > 4 {
		return codes[len(codes)-4:]
	}
	return codes
}

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var event struct {
		Payload payload `json:"payload"`
	}
	_ = json.Unmarshal(raw, &event)
	p := event.Payload

	switch p.Mode {
	case "maxhandles":
		n := p.Opens
		if n == 0 {
			n = 5
		}
		codes := make([]int32, 0, n)
		for i := 0; i < n; i++ {
			codes = append(codes, open(p))
		}
		// A closed slot is reusable.
		var reopen int32 = -100
		if len(codes) > 0 && codes[0] >= 0 {
			httpClose(codes[0])
			reopen = open(p)
		}
		record(map[string]any{"open_codes": codes, "reopen_code": reopen})

	case "leak":
		// Open, send, read one chunk, and return WITHOUT closing: the host
		// must close the handle when the event returns.
		h := open(p)
		if h < 0 {
			record(map[string]any{"open_code": h})
			return
		}
		sc, _ := status(h)
		buf := make([]byte, 4096)
		bp, bc := ptrOf(buf)
		n := httpRead(h, bp, bc)
		got := ""
		if n > 0 {
			got = string(buf[:n])
		}
		record(map[string]any{"open_code": h, "status_code": sc, "read_code": n, "read_data": got})

	case "badhandle":
		buf := make([]byte, 64)
		bp, bc := ptrOf(buf)
		record(map[string]any{
			"read_code":   httpRead(99, bp, bc),
			"status_code": httpStatus(99, bp, bc),
			"write_code":  httpWrite(99, bp, bc),
			"close_code":  httpClose(99),
		})

	default: // "stream"
		h := open(p)
		if h < 0 {
			record(map[string]any{"open_code": h})
			return
		}
		wc := writeBody(h, p)
		sc, sj := status(h)
		sc2, _ := status(h) // idempotent
		// A write after the request was sent is invalid.
		late := []byte("late")
		lp, ll := ptrOf(late)
		lateWrite := httpWrite(h, lp, ll)
		codes, data, total := readAll(h, p.ReadCap)
		cc := httpClose(h)
		cc2 := httpClose(h)
		record(map[string]any{
			"open_code":        h,
			"write_codes":      tail(wc),
			"write_count":      len(wc),
			"status_code":      sc,
			"status_again":     sc2,
			"status_json":      sj,
			"late_write_code":  lateWrite,
			"read_codes":       tail(codes),
			"read_count":       len(codes),
			"read_data":        data,
			"read_total":       total,
			"close_code":       cc,
			"close_again_code": cc2,
		})
	}
}
