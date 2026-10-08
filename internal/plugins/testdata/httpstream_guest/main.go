//go:build wasip1

// Test guest for the "ut" http_open/http_write/http_status/http_read/
// http_close host functions (ADR-0121 §3 http:stream, ut-docs#3156), built
// on the Go guest SDK (ADR-0121 F4, ut-docs#3951) so the host tests exercise
// the SDK's bindings. Drives the scenario named in payload.mode and records
// every outcome in plugin storage so the host-side test can assert on it.
//
// Codes reported: 0 for success, the host's negative code for a failure; a
// write or read reports its byte count (0 at end of stream), http_status
// the length of the status JSON.
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

// raw on purpose: badhandle calls with a handle the host never issued; the
// SDK only holds handles http_open returned.
//
//go:wasmimport ut http_write
func rawHTTPWrite(h int32, ptr, length uint32) int32

//go:wasmimport ut http_status
func rawHTTPStatus(h int32, dstPtr, dstCap uint32) int32

//go:wasmimport ut http_read
func rawHTTPRead(h int32, dstPtr, dstCap uint32) int32

//go:wasmimport ut http_close
func rawHTTPClose(h int32) int32

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

func record(results map[string]any) error {
	raw, _ := json.Marshal(results)
	if err := plugin.StorageSet("results", raw); err != nil {
		return fmt.Errorf("storing results failed: %d", code(err))
	}
	return nil
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

func request(p payload) plugin.HTTPRequest {
	return plugin.HTTPRequest{Method: p.Method, URL: p.URL, Headers: p.Headers}
}

// open is HTTPOpen with its code (0, or the host's error).
func open(p payload) (*plugin.HTTPStream, int32) {
	s, err := plugin.HTTPOpen(request(p))
	return s, code(err)
}

// openHandle is HTTPOpen reporting the host's handle number, or its error.
func openHandle(p payload) (*plugin.HTTPStream, int32) {
	s, err := plugin.HTTPOpen(request(p))
	if err != nil {
		return nil, code(err)
	}
	return s, s.Handle()
}

// writeBody writes the request body in chunks, one Write per chunk; returns
// every write's code (its byte count, or the host's error).
func writeBody(s *plugin.HTTPStream, p payload) []int32 {
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
		n, err := s.Write(body[off:end])
		if err != nil {
			codes = append(codes, code(err))
			break
		}
		codes = append(codes, int32(n))
	}
	return codes
}

// status is Status with the host's convention: the status JSON's length (the
// host's own encoding, byte for byte) or the error code.
func status(s *plugin.HTTPStatus, err error) (int32, string) {
	if err != nil {
		return code(err), ""
	}
	j, _ := json.Marshal(map[string]any{"status": s.Status, "headers": s.Headers})
	return int32(len(j)), string(j)
}

func streamStatus(s *plugin.HTTPStream) (int32, string) {
	st, err := s.Status()
	return status(&st, err)
}

// readCode maps one Read back to the host's return: n bytes, 0 at EOF, or
// the error code.
func readCode(n int, err error) int32 {
	switch {
	case errors.Is(err, io.EOF):
		return 0
	case err != nil:
		return code(err)
	}
	return int32(n)
}

// readAll reads until EOF (0) or an error code; returns codes and data.
func readAll(s *plugin.HTTPStream, readCap int) ([]int32, string, int) {
	if readCap <= 0 {
		readCap = 64 << 10
	}
	buf := make([]byte, readCap)
	var codes []int32
	total := 0
	var data []byte
	for i := 0; i < 100000; i++ {
		n := readCode(s.Read(buf))
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

func main() { plugin.Run(plugin.Handlers{"*": handle}) }

func handle(ev plugin.Event) (any, error) {
	var p payload
	_ = ev.Decode(&p)

	switch p.Mode {
	case "maxhandles":
		// The result is the host's handle numbering.
		n := p.Opens
		if n == 0 {
			n = 5
		}
		codes := make([]int32, 0, n)
		var first *plugin.HTTPStream
		for i := 0; i < n; i++ {
			s, c := openHandle(p)
			if i == 0 {
				first = s
			}
			codes = append(codes, c)
		}
		// A closed slot is reusable.
		var reopen int32 = -100
		if first != nil {
			_ = first.Close()
			_, reopen = openHandle(p)
		}
		return nil, record(map[string]any{"open_codes": codes, "reopen_code": reopen})

	case "leak":
		// Open, send, read one chunk, and return WITHOUT closing: the host
		// must close the handle when the event returns.
		s, oc := open(p)
		if oc < 0 {
			return nil, record(map[string]any{"open_code": oc})
		}
		sc, _ := streamStatus(s)
		buf := make([]byte, 4096)
		n := readCode(s.Read(buf))
		got := ""
		if n > 0 {
			got = string(buf[:n])
		}
		return nil, record(map[string]any{"open_code": oc, "status_code": sc, "read_code": n, "read_data": got})

	case "badhandle":
		// Raw: handle 99 was never issued; the SDK only holds handles
		// http_open returned.
		buf := make([]byte, 64)
		bp, bc := ptrOf(buf)
		res := map[string]any{
			"read_code":   rawHTTPRead(99, bp, bc),
			"status_code": rawHTTPStatus(99, bp, bc),
			"write_code":  rawHTTPWrite(99, bp, bc),
			"close_code":  rawHTTPClose(99),
		}
		runtime.KeepAlive(buf)
		return nil, record(res)

	default: // "stream"
		s, oc := open(p)
		if oc < 0 {
			return nil, record(map[string]any{"open_code": oc})
		}
		wc := writeBody(s, p)
		sc, sj := streamStatus(s)
		sc2, _ := streamStatus(s) // idempotent
		// A write after the request was sent is invalid.
		_, lerr := s.Write([]byte("late"))
		lateWrite := code(lerr)
		codes, data, total := readAll(s, p.ReadCap)
		cc := code(s.Close())
		cc2 := code(s.Close())
		return nil, record(map[string]any{
			"open_code":        oc,
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
