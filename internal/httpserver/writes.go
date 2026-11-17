package httpserver

import (
	"bufio"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"time"
)

// copyBufSize is the buffer for the non-file ReadFrom path.
const copyBufSize = 32 << 10

// boundWrites is the write-side twin of boundBodies (ut-docs#5429): before
// every response Write, WriteHeader, Flush and ReadFrom slice it sets a write
// deadline of now + grace + n/rate, so a LAN peer that stops reading is cut
// instead of holding the handler goroutine, fd and any open file or DB rows
// until the process exits. A steady reader, however slow above the rate, and
// however large the body, completes.
//
// Nothing is armed at handler entry: the clock starts at the first write, so
// the silent 25s shell long-poll and slow-computing handlers (reports) are
// never cut. Streaming (SSE) needs no route list: each event re-arms before it
// writes, and a deadline that expired during an idle gap only fails a write
// issued after it, never an idle connection.
//
// Hijack: net/http clears both deadlines on hijack itself; the wrapper arms
// the grace for the pre-hijack flush, records the hijack and never sets a
// deadline afterwards (response.SetWriteDeadline has no hijack check), so
// websockets keep their own timeouts.
//
// When the handler returns, the deadline is re-armed to now + grace: the
// server flushes the buffered tail and chunk trailer after the handler, under
// whatever deadline was last set, and a handler that computed for longer than
// the grace after its last write would otherwise lose that flush. net/http
// then clears the deadline itself for the next keep-alive request.
//
// ReadFrom (io.Copy, http.ServeFile/ServeContent) bypasses Write: a *os.File
// source is fed to net/http in chunk-sized slices, re-arming per slice with a
// single io.LimitedReader layer so sendfile is kept; any other source is
// copied through Write, re-arming after each source Read so a slow upstream
// is never charged to the client.
//
// Fail open like boundBodies: if the writer cannot take a deadline the
// request is served unbounded. It must be outermost. A handler that wants to
// own the write deadline itself is not supported (nothing does today).
func boundWrites(next http.Handler, grace time.Duration, rate, chunk int64) http.Handler {
	if chunk <= 0 {
		chunk = 1
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bw := &boundWriter{rw: w, rc: http.NewResponseController(w), grace: grace, rate: rate, chunk: chunk}
		defer bw.arm(0)
		next.ServeHTTP(bw, r)
	})
}

type boundWriter struct {
	rw       http.ResponseWriter
	rc       *http.ResponseController // on the raw writer, never on bw
	grace    time.Duration
	rate     int64
	chunk    int64
	hijacked bool
	disabled bool
}

// arm sets the deadline for an upcoming write of n bytes.
func (b *boundWriter) arm(n int64) {
	if b.disabled || b.hijacked {
		return
	}
	if err := b.rc.SetWriteDeadline(rateDeadline(time.Now(), b.grace, b.rate, n)); err != nil {
		b.disabled = true
	}
}

func (b *boundWriter) Header() http.Header { return b.rw.Header() }

func (b *boundWriter) WriteHeader(code int) {
	b.arm(0)
	b.rw.WriteHeader(code)
}

// Write feeds p in chunk-sized slices, each with its own deadline, so one
// large Write (a rendered page, an image, an export) gives a stalled peer
// grace + chunk/rate per slice, not grace + len(p)/rate for the whole of it.
func (b *boundWriter) Write(p []byte) (int, error) {
	var total int
	for {
		n := min(int64(len(p)), b.chunk)
		b.arm(n)
		k, err := b.rw.Write(p[:n])
		total += k
		p = p[n:]
		if err != nil || len(p) == 0 {
			return total, err
		}
	}
}

// Flush pushes up to a buffer of bytes; the grace covers that.
func (b *boundWriter) Flush() { _ = b.FlushError() }

// FlushError is what http.ResponseController prefers, so a handler's flush
// error is not swallowed.
func (b *boundWriter) FlushError() error {
	b.arm(0)
	return b.rc.Flush()
}

// Hijack arms the grace first, because response.Hijack flushes the pending
// header (a websocket's 101) before net/http clears the deadlines, and a
// handler that wrote it then worked past the grace would otherwise lose it. It then records the
// takeover before delegating (hijackLocked can fail after it has already
// hijacked and cleared the deadlines) and never sets a deadline afterwards.
func (b *boundWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	b.arm(0)
	b.hijacked = true
	return b.rc.Hijack()
}

// Unwrap lets http.ResponseController users inside (boundBodies'
// SetReadDeadline, EnableFullDuplex) reach the real writer.
func (b *boundWriter) Unwrap() http.ResponseWriter { return b.rw }

type writerOnly struct{ io.Writer }

func (b *boundWriter) ReadFrom(src io.Reader) (int64, error) {
	base, remaining := src, int64(math.MaxInt64)
	lr, limited := src.(*io.LimitedReader)
	if limited {
		base, remaining = lr.R, lr.N
	}
	f, isFile := base.(*os.File)
	inner, hasInner := b.rw.(io.ReaderFrom)
	if !isFile || !hasInner || b.disabled || b.hijacked {
		if hasInner && (b.disabled || b.hijacked) {
			return inner.ReadFrom(src)
		}
		return io.CopyBuffer(writerOnly{b}, src, make([]byte, copyBufSize))
	}
	var total int64
	for remaining > 0 {
		n := min(b.chunk, remaining)
		b.arm(n)
		k, err := inner.ReadFrom(&io.LimitedReader{R: f, N: n})
		total += k
		remaining -= k
		if limited {
			lr.N -= k
		}
		if err != nil {
			return total, err
		}
		if k < n {
			break // source ended early
		}
	}
	return total, nil
}
