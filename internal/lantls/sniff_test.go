package lantls

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

// serve starts an http.Server on a sniffing listener and returns its
// address. The handler reports whether the request arrived over TLS.
func serve(t *testing.T, c *Cert, firstByte time.Duration) string {
	t.Helper()
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newSniffListener(inner, c.TLSConfig(), firstByte)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "tls=%v", r.TLS != nil)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return inner.Addr().String()
}

// pinnedClient trusts exactly one SPKI pin and nothing else — the shape
// the replica side (slice C) will use.
func pinnedClient(pin string) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // replaced by the pin check below
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				leaf, err := x509.ParseCertificate(raw[0])
				if err != nil {
					return err
				}
				sum := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
				if hex.EncodeToString(sum[:]) != pin {
					return errors.New("pin mismatch")
				}
				return nil
			},
		},
	}}
}

func get(t *testing.T, c *http.Client, url string) string {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestSniff_PlainAndTLSOnOnePort(t *testing.T) {
	c, err := loadOrCreate(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	addr := serve(t, c, 2*time.Second)

	if got := get(t, &http.Client{Timeout: 5 * time.Second}, "http://"+addr+"/healthz"); got != "tls=false" {
		t.Errorf("plain HTTP: %q, want tls=false", got)
	}
	if got := get(t, pinnedClient(c.Pin()), "https://"+addr+"/healthz"); got != "tls=true" {
		t.Errorf("TLS: %q, want tls=true (r.TLS set)", got)
	}
	// A client pinning some other key refuses the connection.
	if _, err := pinnedClient("00").Get("https://" + addr + "/"); err == nil {
		t.Error("wrong pin accepted")
	}
}

func TestSniff_TLS13AndHTTP11Only(t *testing.T) {
	c, err := loadOrCreate(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	addr := serve(t, c, 2*time.Second)
	conn, err := tls.Dial("tcp", addr, &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // test inspects the handshake only
		NextProtos:         []string{"h2", "http/1.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	st := conn.ConnectionState()
	if st.Version != tls.VersionTLS13 {
		t.Errorf("version %x, want TLS 1.3", st.Version)
	}
	if st.NegotiatedProtocol != "http/1.1" {
		t.Errorf("ALPN %q, want http/1.1", st.NegotiatedProtocol)
	}
	if _, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12}); err == nil { //nolint:gosec // test
		t.Error("TLS 1.2 client accepted, want 1.3 minimum")
	}
}

func TestSniff_SilentConnectionDoesNotBlockOthers(t *testing.T) {
	c, err := loadOrCreate(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	addr := serve(t, c, 10*time.Second)
	silent, err := net.Dial("tcp", addr) // connects, never writes
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	done := make(chan string, 1)
	go func() {
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Get("http://" + addr + "/")
		if err != nil {
			done <- err.Error()
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		done <- string(b)
	}()
	select {
	case got := <-done:
		if got != "tls=false" {
			t.Fatalf("second client: %q", got)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("a silent connection blocked the accept loop")
	}
}

func TestSniff_SilentConnectionFallsBackToPlainAfterDeadline(t *testing.T) {
	c, err := loadOrCreate(t.TempDir(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	addr := serve(t, c, 100*time.Millisecond)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(300 * time.Millisecond) // past the sniff deadline: handed over as plain HTTP, as today
	if _, err := io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	b, _ := io.ReadAll(conn)
	if want := "tls=false"; len(b) == 0 || string(b[len(b)-len(want):]) != want {
		t.Fatalf("late plain request: %q", b)
	}
}

func TestSniff_CloseUnblocksAccept(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newSniffListener(inner, &tls.Config{}, time.Second)
	errc := make(chan error, 1)
	go func() {
		_, err := ln.Accept()
		errc <- err
	}()
	time.Sleep(50 * time.Millisecond)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-errc:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("Accept after Close: %v, want net.ErrClosed", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept still blocked after Close")
	}
	if ln.Addr().String() != inner.Addr().String() {
		t.Errorf("Addr() = %v, want inner %v", ln.Addr(), inner.Addr())
	}
	_ = ln.Close() // idempotent
}

type tempErr struct{}

func (tempErr) Error() string   { return "accept: too many open files" }
func (tempErr) Temporary() bool { return true }
func (tempErr) Timeout() bool   { return false }

// flakyListener fails its first Accept with a temporary error (EMFILE),
// then behaves.
type flakyListener struct {
	net.Listener
	failed bool
}

func (f *flakyListener) Accept() (net.Conn, error) {
	if !f.failed {
		f.failed = true
		return nil, tempErr{}
	}
	return f.Listener.Accept()
}

// A temporary Accept error must be passed on (net/http backs off) without
// ending the listener; before this, the server would hang forever after
// one EMFILE.
func TestSniff_TemporaryAcceptErrorKeepsListening(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newSniffListener(&flakyListener{Listener: inner}, &tls.Config{}, time.Second)
	defer ln.Close()
	if _, err := ln.Accept(); !isTemporary(err) {
		t.Fatalf("first Accept: %v, want the temporary error", err)
	}
	go func() {
		if c, err := net.Dial("tcp", inner.Addr().String()); err == nil {
			_, _ = io.WriteString(c, "GET / HTTP/1.1\r\n\r\n")
			time.Sleep(time.Second)
			c.Close()
		}
	}()
	got := make(chan error, 1)
	go func() {
		c, err := ln.Accept()
		if c != nil {
			c.Close()
		}
		got <- err
	}()
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("Accept after a temporary error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("listener stopped accepting after a temporary error")
	}
}

// Plain conns keep the *net.TCPConn fast paths net/http looks for
// (ut-docs#2736 review): CloseWrite for the lingering close after an
// unread request body, ReadFrom for sendfile.
func TestSniff_PlainConnKeepsTCPInterfaces(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newSniffListener(inner, &tls.Config{}, time.Second)
	defer ln.Close()
	go func() {
		if c, err := net.Dial("tcp", inner.Addr().String()); err == nil {
			_, _ = io.WriteString(c, "G")
			time.Sleep(time.Second)
			c.Close()
		}
	}()
	c, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, ok := c.(interface{ CloseWrite() error }); !ok {
		t.Error("plain conn lost CloseWrite: an early 401/413 would reach the client as a reset")
	}
	if _, ok := c.(io.ReaderFrom); !ok {
		t.Error("plain conn lost io.ReaderFrom: file downloads lose sendfile")
	}
}

func TestSniff_CloseClosesConnsStillBeingSniffed(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln := newSniffListener(inner, &tls.Config{}, 10*time.Second)
	conn, err := net.Dial("tcp", inner.Addr().String()) // silent: still being sniffed
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	time.Sleep(100 * time.Millisecond)
	_ = ln.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	_, err = conn.Read(make([]byte, 1))
	if err == nil || isTimeout(err) || time.Since(start) > 2*time.Second {
		t.Fatalf("conn mid-sniff not closed by Close (err %v after %v)", err, time.Since(start))
	}
	if _, err := ln.Accept(); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("Accept after Close: %v", err)
	}
}
