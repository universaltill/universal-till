package sim

import (
	"bufio"
	"net"
	"testing"
	"time"
)

// TestSilentServer_CloseReleasesConnection proves the "Silent" mode's
// serving goroutine (and the TCP connection it holds) actually terminates
// when the server is closed, instead of leaking forever (ut-docs#2156 —
// found as the "surviving goroutine" in a go test -race timeout dump for
// internal/plugins). The simulator must still hang past the *client's
// own* read deadline for a live test (TestBridgeSale_SilentDeviceTimesOut
// in the parent okc package relies on exactly that behaviour), so this
// only asserts the leak clears once Close() is called — not that the
// connection never hangs at all.
func TestSilentServer_CloseReleasesConnection(t *testing.T) {
	s, err := Start("127.0.0.1:0", Options{Silent: true})
	if err != nil {
		t.Fatalf("start sim: %v", err)
	}
	conn, err := net.Dial("tcp", s.Addr())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(`{"op":"status","request_id":"x"}` + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Give the server a moment to read the line and enter its
	// "hang forever" branch before closing — loopback IO, generous bound.
	time.Sleep(50 * time.Millisecond)

	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err = bufio.NewReader(conn).ReadByte()
	if err == nil {
		t.Fatal("expected the server to close the connection after Close(), got a byte instead")
	}
	// A *timeout* error here means nothing arrived within our deadline —
	// indistinguishable from "still hanging" and NOT proof the server let
	// go. Only a non-timeout error (EOF, connection reset) coming back
	// well inside the deadline proves the serve goroutine actually woke
	// up and ran its deferred conn.Close() after Server.Close().
	if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatalf("connection still open %s after Close() — the serve goroutine leaked: %v", 2*time.Second, err)
	}
}
