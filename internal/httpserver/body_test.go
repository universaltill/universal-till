package httpserver

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func dial(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func deny(w http.ResponseWriter, r *http.Request) { http.Error(w, "forbidden", 403) }

func TestOversizedHeaderRejected(t *testing.T) {
	addr := start(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) }), time.Second, time.Minute)
	c := dial(t, addr)
	_, _ = fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: x\r\nX-Big: %s\r\n\r\n", strings.Repeat("a", 70<<10))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil || resp.StatusCode != 431 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	c2 := dial(t, addr)
	_, _ = fmt.Fprintf(c2, "GET / HTTP/1.1\r\nHost: x\r\nCookie: %s\r\nAuthorization: Bearer %s\r\n\r\n",
		strings.Repeat("c", 4<<10), strings.Repeat("b", 4<<10))
	resp, err = http.ReadResponse(bufio.NewReader(c2), nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
}

func earlyResponseClosed(t *testing.T, head string) {
	addr := start(t, http.HandlerFunc(deny), time.Second, time.Minute)
	c := dial(t, addr)
	_, _ = c.Write([]byte(head))
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil || resp.StatusCode != 403 {
		t.Fatalf("no 403 within 2s: resp=%v err=%v", resp, err)
	}
	if !closedWithin(c, 2*time.Second) {
		t.Fatal("conn held after early response")
	}
}

func TestStalledBodyEarlyResponseClosed(t *testing.T) {
	earlyResponseClosed(t, "POST /x HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\n")
}

func TestStalledChunkedBodyClosed(t *testing.T) {
	earlyResponseClosed(t, "POST /x HTTP/1.1\r\nHost: x\r\nTransfer-Encoding: chunked\r\n\r\n")
}

func TestTrickledBodyCut(t *testing.T) {
	errc := make(chan error, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		errc <- err
		w.WriteHeader(400)
	})
	addr := start(t, h, time.Second, time.Minute)
	c := dial(t, addr)
	_, _ = c.Write([]byte("POST /x HTTP/1.1\r\nHost: x\r\nContent-Length: 1000\r\n\r\n"))
	go func() {
		for i := 0; i < 40; i++ {
			if _, err := c.Write([]byte("a")); err != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("ReadAll succeeded on a trickled body")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("trickled body not cut within 2s")
	}
}

func TestSteadyBodyAboveRateCompletes(t *testing.T) {
	got := make(chan int, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- len(b)
	})
	addr := start(t, h, time.Second, time.Minute)
	c := dial(t, addr)
	_, _ = c.Write([]byte("POST /x HTTP/1.1\r\nHost: x\r\nContent-Length: 4096\r\n\r\n"))
	chunk := []byte(strings.Repeat("a", 256))
	start := time.Now()
	for i := 0; i < 16; i++ {
		_, _ = c.Write(chunk)
		time.Sleep(50 * time.Millisecond)
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Fatal("test did not outlast the grace")
	}
	select {
	case n := <-got:
		if n != 4096 {
			t.Fatalf("handler got %d bytes", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler never finished")
	}
}

func TestBodyReadThenSlowHandlerKeepsContext(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		_, _ = r.Body.Read(make([]byte, 1)) // a second read after EOF must not re-arm
		time.Sleep(600 * time.Millisecond)  // 3x grace
		if err := r.Context().Err(); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
	addr := start(t, h, time.Second, time.Minute)
	resp, err := http.Post("http://"+addr+"/x", "text/plain", strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "ok" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, b)
	}
}

func TestHijackedGetOutlivesBodyGrace(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(600 * time.Millisecond)
		_, _ = brw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 4\r\nConnection: close\r\n\r\necho")
		_ = brw.Flush()
	})
	addr := start(t, h, time.Second, time.Minute)
	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

// R1 (ut-docs#5338 review): the middleware must not replace net/http's own
// request body, or the server loses its immediate close of unread big bodies.
func answeredWithin(t *testing.T, head string, max time.Duration) {
	t.Helper()
	addr := startBody(t, http.HandlerFunc(deny), time.Second, time.Minute, 15*time.Second, 1<<10)
	c := dial(t, addr)
	begin := time.Now()
	_, _ = c.Write([]byte(head))
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil || resp.StatusCode != 403 {
		t.Fatalf("no 403: resp=%v err=%v", resp, err)
	}
	if d := time.Since(begin); d > max {
		t.Fatalf("403 took %v, want < %v (grace is 15s)", d, max)
	}
}

func TestUnreadBigBodyAnsweredAtOnce(t *testing.T) {
	answeredWithin(t, "POST /x HTTP/1.1\r\nHost: x\r\nContent-Length: 10000000\r\n\r\n", 3*time.Second)
}

func TestUnreadExpectContinueAnsweredAtOnce(t *testing.T) {
	answeredWithin(t, "POST /x HTTP/1.1\r\nHost: x\r\nExpect: 100-continue\r\nContent-Length: 1000\r\n\r\n", 3*time.Second)
}

// R2: a handler that works before its first Read keeps the full grace.
func TestSlowHandlerBeforeFirstReadKeepsGrace(t *testing.T) {
	got := make(chan error, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(400 * time.Millisecond) // longer than the 200ms grace
		b, err := io.ReadAll(r.Body)
		if err == nil && len(b) != 5 {
			err = fmt.Errorf("got %d bytes", len(b))
		}
		got <- err
	})
	addr := start(t, h, time.Second, time.Minute)
	c := dial(t, addr)
	_, _ = c.Write([]byte("POST /x HTTP/1.1\r\nHost: x\r\nContent-Length: 5\r\n\r\n"))
	time.Sleep(100 * time.Millisecond)
	_, _ = c.Write([]byte("hello"))
	select {
	case err := <-got:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("handler never finished")
	}
}

// R4: degenerate rates and huge counts must not panic or overflow.
func TestRateBodyDeadlineArithmetic(t *testing.T) {
	now := time.Now()
	if d := bodyDeadline(now, time.Second, 0, 100); !d.After(now) {
		t.Fatal("rate 0 gave a past deadline")
	}
	if d := bodyDeadline(now, time.Second, 1, 1<<62); !d.After(now) {
		t.Fatalf("overflow: %v", d)
	}
	if d := bodyDeadline(now, time.Second, 1<<10, 2<<10); d != now.Add(3*time.Second) {
		t.Fatalf("got %v", d.Sub(now))
	}
}
