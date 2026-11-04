package httpserver

import (
	"bufio"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestNewPinsTimeouts(t *testing.T) {
	h := http.NewServeMux()
	s := New(h)
	if s.Handler == nil {
		t.Fatal("handler missing")
	}
	if s.MaxHeaderBytes != MaxHeaderBytes || MaxHeaderBytes != 64<<10 {
		t.Fatalf("MaxHeaderBytes=%d const=%d", s.MaxHeaderBytes, MaxHeaderBytes)
	}
	if BodyReadGrace <= 0 || BodyReadGrace > 60*time.Second {
		t.Fatalf("BodyReadGrace %v out of bounds", BodyReadGrace)
	}
	if BodyMinRate <= 0 || BodyMinRate > 16<<10 {
		t.Fatalf("BodyMinRate %d out of bounds", BodyMinRate)
	}
	if s.ReadHeaderTimeout != ReadHeaderTimeout || s.IdleTimeout != IdleTimeout {
		t.Fatalf("got hdr=%v idle=%v", s.ReadHeaderTimeout, s.IdleTimeout)
	}
	if ReadHeaderTimeout <= 0 || ReadHeaderTimeout > 30*time.Second {
		t.Fatalf("ReadHeaderTimeout %v out of bounds", ReadHeaderTimeout)
	}
	// Must outlast http.DefaultTransport's 90s idle reuse window.
	if IdleTimeout <= 90*time.Second {
		t.Fatalf("IdleTimeout %v must exceed 90s", IdleTimeout)
	}
	if s.ReadTimeout != 0 || s.WriteTimeout != 0 {
		t.Fatal("Read/WriteTimeout must stay zero (SSE, long-poll, uploads)")
	}
}

// Both till servers must be built via New, not a bare literal (#5337), and no
// other non-test file under internal/ may build one either (#5338).
func TestRealServersUseNew(t *testing.T) {
	for _, p := range []string{"../server/server.go", "../recovery/serve.go"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "httpserver.New(") {
			t.Errorf("%s does not call httpserver.New", p)
		}
	}
	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") ||
			filepath.ToSlash(path) == "../httpserver/timeouts.go" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if m := bareServer.FindString(string(b)); m != "" {
			t.Errorf("%s uses %q (no timeouts, no body bound); use httpserver.New", path, m)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

var bareServer = regexp.MustCompile(`&?http\.Server\{|http\.(ListenAndServe|ListenAndServeTLS|Serve|ServeTLS)\(`)

func start(t *testing.T, h http.Handler, hdr, idle time.Duration) string {
	t.Helper()
	return startBody(t, h, hdr, idle, 200*time.Millisecond, 1<<10)
}

func startBody(t *testing.T, h http.Handler, hdr, idle, grace time.Duration, rate int64) string {
	t.Helper()
	l := testLimits()
	l.hdr, l.idle, l.bodyGrace, l.bodyRate = hdr, idle, grace, rate
	return startLimits(t, h, l)
}

// testLimits are the short write bounds the tests share: 300ms grace,
// 64 KiB/s, 64 KiB sendfile slices. Body limits are generous defaults.
func testLimits() limits {
	return limits{
		hdr: time.Second, idle: time.Minute,
		bodyGrace: 5 * time.Second, bodyRate: 1 << 10,
		writeGrace: 300 * time.Millisecond, writeRate: 64 << 10, writeChnk: 64 << 10,
	}
}

func startLimits(t *testing.T, h http.Handler, l limits) string {
	t.Helper()
	s := newWith(h, l)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(func() { _ = s.Close() })
	return ln.Addr().String()
}

func closedWithin(c net.Conn, d time.Duration) bool {
	_ = c.SetReadDeadline(time.Now().Add(d))
	_, err := io.ReadAll(c)
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		return false
	}
	return true
}

func TestStalledHeaderClosed(t *testing.T) {
	addr := start(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), 150*time.Millisecond, time.Minute)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n"))
	if !closedWithin(c, 2*time.Second) {
		t.Fatal("stalled-header conn was not closed")
	}
}

func TestIdleKeepAliveClosedButServed(t *testing.T) {
	addr := start(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }), time.Second, 200*time.Millisecond)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = c.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	if !closedWithin(c, 2*time.Second) {
		t.Fatal("idle keep-alive conn was not closed")
	}
}

func TestStreamingHandlerOutlivesTimeouts(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := w.(http.Flusher)
		_, _ = w.Write([]byte("a"))
		f.Flush()
		time.Sleep(600 * time.Millisecond) // > both timeouts
		_, _ = w.Write([]byte("b"))
		f.Flush()
	})
	addr := start(t, h, 150*time.Millisecond, 150*time.Millisecond)
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || string(b) != "ab" {
		t.Fatalf("body=%q err=%v", b, err)
	}
}
