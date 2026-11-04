package httpserver

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const bigBody = 16 << 20

// dialStalled connects and sends a GET but never reads, with a tiny receive
// buffer so the server's send buffers fill quickly.
func dialStalled(t *testing.T, addr, path string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}
	if _, err := c.Write([]byte("GET " + path + " HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	return c
}

func waitDone(t *testing.T, done <-chan error, within time.Duration, what string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(within):
		t.Fatalf("%s: handler still blocked after %v", what, within)
		return nil
	}
}

func TestNonReaderCutWithinBound(t *testing.T) {
	done := make(chan error, 1)
	addr := startLimits(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 32<<10)
		for i := 0; i < 8*bigBody/len(buf); i++ {
			if _, err := w.Write(buf); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}), testLimits())
	dialStalled(t, addr, "/")
	if err := waitDone(t, done, 10*time.Second, "non-reader"); err == nil {
		t.Fatal("handler wrote everything to a peer that never reads")
	}
}

func TestSSEReaderSurvivesPastGrace(t *testing.T) {
	const events = 6
	addr := startLimits(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < events; i++ {
			if i == 3 {
				time.Sleep(600 * time.Millisecond) // idle gap of 2x grace
			} else {
				time.Sleep(200 * time.Millisecond)
			}
			_, _ = w.Write([]byte(": ping\n\n"))
			w.(http.Flusher).Flush()
		}
	}), testLimits())
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := strings.Count(string(b), ": ping"); got != events {
		t.Fatalf("got %d events, want %d", got, events)
	}
}

func TestSSENonReaderCut(t *testing.T) {
	done := make(chan error, 1)
	addr := startLimits(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		ev := []byte(": " + strings.Repeat("x", 32<<10) + "\n\n")
		for i := 0; i < 20000; i++ {
			if _, err := w.Write(ev); err != nil {
				done <- err
				return
			}
			w.(http.Flusher).Flush()
		}
		done <- nil
	}), testLimits())
	dialStalled(t, addr, "/")
	if err := waitDone(t, done, 10*time.Second, "sse non-reader"); err == nil {
		t.Fatal("SSE never failed for a non-reading peer")
	}
}

func TestSilentLongPollThenWrite(t *testing.T) {
	addr := startLimits(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(900 * time.Millisecond) // 3x grace of silence
		_, _ = w.Write([]byte("ok"))
	}), testLimits())
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "ok" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, b)
	}
}

// throttledRead reads c at about bytesPerSec until EOF/err or stop closes.
func throttledRead(c io.Reader, bytesPerSec int, stop <-chan struct{}) (int64, error) {
	buf := make([]byte, 32<<10)
	start := time.Now()
	var total int64
	for {
		select {
		case <-stop:
			return total, errors.New("stopped")
		default:
		}
		n, err := c.Read(buf)
		total += int64(n)
		if err != nil {
			return total, err
		}
		// Pace against the schedule, not per read: reads can be tiny.
		due := start.Add(time.Duration(total) * time.Second / time.Duration(bytesPerSec))
		time.Sleep(time.Until(due))
	}
}

func rateLimits() limits {
	l := testLimits()
	l.writeRate = 2 << 20
	// Loopback window updates can stall a throttled reader for a few hundred
	// ms; keep the grace well above that.
	l.writeGrace = time.Second
	return l
}

func TestSlowSteadyReaderCompletes(t *testing.T) {
	const size = 12 << 20
	done := make(chan error, 1)
	addr := startLimits(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 32<<10)
		var err error
		for i := 0; i < size/len(buf) && err == nil; i++ {
			_, err = w.Write(buf)
		}
		done <- err
	}), rateLimits())
	// Default receive buffer: a 4 KiB one makes TCP itself the bottleneck.
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	// 2x the configured rate.
	n, err := throttledRead(resp.Body, 4<<20, nil)
	if err != io.EOF || n != size {
		t.Fatalf("read %d bytes, err=%v", n, err)
	}
	if err := waitDone(t, done, 10*time.Second, "steady"); err != nil {
		t.Fatalf("handler write failed: %v", err)
	}
}

func TestBelowRateReaderCut(t *testing.T) {
	done := make(chan error, 1)
	addr := startLimits(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 32<<10)
		for i := 0; i < 8*bigBody/len(buf); i++ {
			if _, err := w.Write(buf); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}), rateLimits())
	c := dialStalled(t, addr, "/")
	stop := make(chan struct{})
	defer close(stop)
	go func() { _, _ = throttledRead(c, 16<<10, stop) }() // 1/128 of the rate
	if err := waitDone(t, done, 15*time.Second, "below-rate"); err == nil {
		t.Fatal("slow reader was never cut")
	}
}

func TestHijackedConnNotCut(t *testing.T) {
	addr := startLimits(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x")) // arms a write deadline
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		go func() {
			defer conn.Close()
			time.Sleep(900 * time.Millisecond) // 3x grace
			_, err := conn.Write(make([]byte, 1<<20))
			if err != nil {
				t.Errorf("hijacked write: %v", err)
			}
		}()
	}), testLimits())
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	b, _ := io.ReadAll(c)
	if len(b) < 1<<20 {
		t.Fatalf("got %d bytes, want >= 1 MiB", len(b))
	}
}

func TestWebsocketOutlivesGrace(t *testing.T) {
	addr := startLimits(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		time.Sleep(900 * time.Millisecond)
		if err := c.Write(context.Background(), websocket.MessageBinary, make([]byte, 1<<20)); err != nil {
			t.Errorf("ws write: %v", err)
		}
		_ = c.Close(websocket.StatusNormalClosure, "")
	}), testLimits())
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, _, err := websocket.Dial(ctx, "ws://"+addr+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	c.SetReadLimit(-1)
	_, msg, err := c.Read(ctx)
	if err != nil || len(msg) != 1<<20 {
		t.Fatalf("len=%d err=%v", len(msg), err)
	}
}

func bigFile(t *testing.T, size int) (string, [32]byte) {
	t.Helper()
	b := make([]byte, size)
	_, _ = rand.Read(b)
	p := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p, sha256.Sum256(b)
}

func TestServeFileNonReaderCut(t *testing.T) {
	p, _ := bigFile(t, bigBody)
	done := make(chan struct{}, 1)
	addr := startLimits(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, p)
		done <- struct{}{}
	}), testLimits())
	dialStalled(t, addr, "/")
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("ServeFile still blocked on a peer that never reads")
	}
}

func TestServeFileReaderCompletes(t *testing.T) {
	p, want := bigFile(t, bigBody)
	addr := startLimits(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, p)
	}), testLimits())
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	h := sha256.New()
	n, err := io.Copy(h, resp.Body)
	if err != nil || n != bigBody {
		t.Fatalf("n=%d err=%v", n, err)
	}
	var got [32]byte
	copy(got[:], h.Sum(nil))
	if got != want {
		t.Fatal("sha256 mismatch")
	}
}

func TestServeContentCompletes(t *testing.T) {
	data := make([]byte, 4<<20)
	_, _ = rand.Read(data)
	addr := startLimits(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "x.bin", time.Time{}, bytes.NewReader(data))
	}), testLimits())
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("len=%d err=%v", len(got), err)
	}
}

// deadlineRW is a ResponseWriter that supports SetWriteDeadline and records
// what was set; its ReadFrom records the reader it was handed.
type deadlineRW struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	readers   []io.Reader
	hijacks   int
}

func (d *deadlineRW) SetWriteDeadline(t time.Time) error {
	d.deadlines = append(d.deadlines, t)
	return nil
}

func (d *deadlineRW) ReadFrom(r io.Reader) (int64, error) {
	d.readers = append(d.readers, r)
	return io.Copy(d.ResponseRecorder, r)
}

func (d *deadlineRW) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	d.hijacks++
	return nil, nil, errors.New("no hijack in tests")
}

func TestReadFromKeepsLimitedReaderSingleLayer(t *testing.T) {
	p, _ := bigFile(t, 200<<10)
	f, err := os.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rw := &deadlineRW{ResponseRecorder: httptest.NewRecorder()}
	h := boundWrites(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(w, io.LimitReader(f, 150000))
		if err != nil || n != 150000 {
			t.Errorf("n=%d err=%v", n, err)
		}
	}), time.Second, 1<<20, 64<<10)
	h.ServeHTTP(rw, httptest.NewRequest("GET", "/", nil))
	if len(rw.readers) != 3 {
		t.Fatalf("inner ReadFrom called %d times, want 3", len(rw.readers))
	}
	for i, r := range rw.readers {
		lr, ok := r.(*io.LimitedReader)
		if !ok || lr.R != io.Reader(f) {
			t.Fatalf("call %d got %T, want *io.LimitedReader over the *os.File", i, r)
		}
	}
	if rw.Body.Len() != 150000 {
		t.Fatalf("body %d", rw.Body.Len())
	}
	if len(rw.deadlines) < 3 {
		t.Fatalf("deadlines armed %d times, want >= 3", len(rw.deadlines))
	}
}

func TestKeepAliveSecondRequestNotCut(t *testing.T) {
	addr := startLimits(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/one" {
			_, _ = w.Write([]byte("a"))
			time.Sleep(600 * time.Millisecond) // 2x grace, then the final flush
			return
		}
		_, _ = w.Write(make([]byte, 1<<20))
	}), testLimits())
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReader(c)
	_, _ = c.Write([]byte("GET /one HTTP/1.1\r\nHost: x\r\n\r\n"))
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(resp.Body); err != nil || string(b) != "a" {
		t.Fatalf("one: %q %v", b, err)
	}
	time.Sleep(600 * time.Millisecond)
	_, _ = c.Write([]byte("GET /two HTTP/1.1\r\nHost: x\r\n\r\n"))
	resp, err = http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := io.Copy(io.Discard, resp.Body); err != nil || n != 1<<20 {
		t.Fatalf("two: %d %v", n, err)
	}
}

func TestHandlerReturnsAfterLongComputeFlushes(t *testing.T) {
	addr := startLimits(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, 1<<10)) // buffered, not on the wire
		time.Sleep(600 * time.Millisecond)
	}), testLimits())
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	n, err := io.Copy(io.Discard, resp.Body)
	if err != nil || n != 1<<10 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestWriteBoundOverTLS(t *testing.T) {
	done := make(chan error, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 32<<10)
		n := bigBody / len(buf)
		if r.URL.Path == "/stall" {
			n *= 8
		}
		for i := 0; i < n; i++ {
			if _, err := w.Write(buf); err != nil {
				if r.URL.Path == "/stall" {
					done <- err
				}
				return
			}
		}
	})
	srv := httptest.NewUnstartedServer(http.NotFoundHandler())
	srv.Config = newWith(h, testLimits())
	srv.StartTLS()
	t.Cleanup(srv.Close)

	resp, err := srv.Client().Get(srv.URL + "/ok")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := io.Copy(io.Discard, resp.Body); err != nil || n != bigBody {
		t.Fatalf("reader: %d %v", n, err)
	}
	_ = resp.Body.Close()

	c, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetReadBuffer(4096)
	}
	tc := tls.Client(c, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}}) //nolint:gosec // test cert
	if _, err := tc.Write([]byte("GET /stall HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	if err := waitDone(t, done, 15*time.Second, "tls non-reader"); err == nil {
		t.Fatal("tls non-reader was not cut")
	}
}

func TestFailOpenWhenUnsupported(t *testing.T) {
	rec := httptest.NewRecorder() // no SetWriteDeadline
	h := boundWrites(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(201)
		_, _ = w.Write([]byte("hi"))
		w.(http.Flusher).Flush()
		_, _ = io.Copy(w, strings.NewReader("!"))
	}), time.Second, 1<<10, 4<<10)
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 201 || rec.Body.String() != "hi!" {
		t.Fatalf("code=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestNoRearmAfterHijack(t *testing.T) {
	rw := &deadlineRW{ResponseRecorder: httptest.NewRecorder()}
	h := boundWrites(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("x"))
		_, _, _ = w.(http.Hijacker).Hijack()
	}), time.Second, 1<<10, 4<<10)
	h.ServeHTTP(rw, httptest.NewRequest("GET", "/", nil))
	if rw.hijacks != 1 || len(rw.deadlines) != 1 {
		t.Fatalf("hijacks=%d deadlines=%d, want 1 and 1 (no re-arm at exit)", rw.hijacks, len(rw.deadlines))
	}
}

func TestNewWrapsWithBoundWrites(t *testing.T) {
	rw := &deadlineRW{ResponseRecorder: httptest.NewRecorder()}
	s := New(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("x")) }))
	s.Handler.ServeHTTP(rw, httptest.NewRequest("GET", "/", nil))
	if len(rw.deadlines) == 0 || !rw.deadlines[0].After(time.Now()) {
		t.Fatalf("New's handler did not arm a write deadline: %v", rw.deadlines)
	}
	if WriteGrace <= 0 || WriteGrace > 60*time.Second || WriteMinRate <= 0 || WriteMinRate > 16<<10 {
		t.Fatalf("write bounds out of range: %v %d", WriteGrace, WriteMinRate)
	}
}
