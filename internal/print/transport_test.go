package print

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// --- networkTransport: per-destination serialisation ---------------------

// TestNetworkTransport_SameAddressJobsAreSerialisedAndBothDelivered proves
// two things: (1) the two jobs' CRITICAL SECTIONS — from a successful dial
// to the connection being closed — never overlap, and (2) both are still
// delivered to the printer in full. This is ut-docs#2287's bug: receipt +
// kitchen ticket to the same printer, one silently lost.
//
// Two earlier designs for (1) were tried and dropped as genuinely flaky
// under -race (not just slow):
//   - A fake printer that REJECTS a second connection accepted while one
//     is open, and a variant that tracked the PEAK number of
//     simultaneously-open connections server-side. Both depend on
//     comparing two independent, asynchronous clocks — when the server
//     notices job 1's EOF vs. when job 2's dial arrives — and a server
//     needs real wall-clock time (several recv() calls via io.ReadAll) to
//     read 64KiB to EOF, comfortably longer than a fresh loopback dial;
//     that made the server's "connection open" interval end LATER than
//     the client's own critical section, so it falsely looked unserialised
//     even when the client-side lock worked correctly.
//   - Timing the client's whole Print() call (start just before the call,
//     stop just after) instead of the critical section: that measures the
//     BLOCKED-waiting-for-the-lock time too, which — by the very nature of
//     blocking — always overlaps the busy call. A correct implementation
//     can never pass that assertion; it was testing the wrong thing.
//
// Instrumenting dialFn (already a seam for the retry tests) to timestamp
// dial success and wrapping the returned conn to timestamp Close() brackets
// exactly the span acquireDest guards — a purely client-side measurement
// with nothing external left to race.
func TestNetworkTransport_SameAddressJobsAreSerialisedAndBothDelivered(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var mu sync.Mutex
	var payloads [][]byte
	done := make(chan struct{}, 2)

	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func(c net.Conn) {
				data, _ := io.ReadAll(c)
				c.Close()
				mu.Lock()
				payloads = append(payloads, data)
				mu.Unlock()
				done <- struct{}{}
			}(conn)
		}
	}()

	origDial := dialFn
	defer func() { dialFn = origDial }()

	type interval struct{ start, end time.Time }
	var imu sync.Mutex
	var intervals []*interval
	dialFn = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, derr := origDial(ctx, network, addr)
		if derr != nil {
			return nil, derr
		}
		iv := &interval{start: time.Now()}
		imu.Lock()
		intervals = append(intervals, iv)
		imu.Unlock()
		return &closeTimestampingConn{Conn: conn, onClose: func() { iv.end = time.Now() }}, nil
	}

	tr := &networkTransport{addr: ln.Addr().String()}
	payloadA := bytes.Repeat([]byte("A"), 64*1024)
	payloadB := bytes.Repeat([]byte("B"), 64*1024)

	errs := make([]error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = tr.Print(context.Background(), payloadA) }()
	go func() { defer wg.Done(); errs[1] = tr.Print(context.Background(), payloadB) }()
	wg.Wait()

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("both prints should succeed, got %v / %v", errs[0], errs[1])
	}

	imu.Lock()
	if len(intervals) != 2 {
		imu.Unlock()
		t.Fatalf("expected exactly 2 successful dials (no retries expected), got %d", len(intervals))
	}
	a, b := *intervals[0], *intervals[1]
	imu.Unlock()
	if a.start.Before(b.end) && b.start.Before(a.end) {
		t.Fatalf("job critical sections overlapped in time (job A %v-%v, job B %v-%v) — not serialised", a.start, a.end, b.start, b.end)
	}

	for i := 0; i < 2; i++ {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for fake printer to process both connections")
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(payloads) != 2 {
		t.Fatalf("expected 2 full deliveries, got %d", len(payloads))
	}
	total := len(payloads[0]) + len(payloads[1])
	if total != len(payloadA)+len(payloadB) {
		t.Fatalf("expected %d total bytes delivered, got %d", len(payloadA)+len(payloadB), total)
	}
	for _, p := range payloads {
		if !bytes.Equal(p, payloadA) && !bytes.Equal(p, payloadB) {
			t.Fatalf("delivered payload does not match either full job (len %d) — jobs interleaved or corrupted", len(p))
		}
	}
}

// closeTimestampingConn wraps a net.Conn to record when Close() is called,
// used only by TestNetworkTransport_SameAddressJobsAreSerialisedAndBothDelivered
// to bracket the client-side critical section under test.
type closeTimestampingConn struct {
	net.Conn
	onClose func()
	once    sync.Once
}

func (c *closeTimestampingConn) Close() error {
	c.once.Do(c.onClose)
	return c.Conn.Close()
}

// TestNetworkTransport_DifferentAddressesRunConcurrently proves the fix
// does not introduce a global lock: jobs to two DIFFERENT printers must
// run concurrently, not queue behind each other.
//
// The hold sits INSIDE the guarded region — in dialFn, before the conn is
// handed back — not on the server's read (ut-docs#2287 review, finding 1):
// Print returns as soon as its bytes land in the kernel buffer, so a
// server-side delay never made the elapsed time depend on the lock at all,
// and the test passed with a deliberately global lock. With the hold in
// the dial, a global lock costs 2×hold and a per-destination lock ~1×hold.
func TestNetworkTransport_DifferentAddressesRunConcurrently(t *testing.T) {
	const hold = 300 * time.Millisecond

	realDial := dialFn
	dialFn = func(ctx context.Context, network, addr string) (net.Conn, error) {
		time.Sleep(hold)
		return realDial(ctx, network, addr)
	}
	t.Cleanup(func() { dialFn = realDial })

	newHoldingListener := func(t *testing.T) net.Listener {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listen: %v", err)
		}
		go func() {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			_, _ = io.ReadAll(conn)
			conn.Close()
		}()
		return ln
	}

	ln1 := newHoldingListener(t)
	defer ln1.Close()
	ln2 := newHoldingListener(t)
	defer ln2.Close()

	tr1 := &networkTransport{addr: ln1.Addr().String()}
	tr2 := &networkTransport{addr: ln2.Addr().String()}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := time.Now()
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = tr1.Print(context.Background(), []byte("x")) }()
	go func() { defer wg.Done(); errs[1] = tr2.Print(context.Background(), []byte("y")) }()
	wg.Wait()
	elapsed := time.Since(start)

	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("unexpected errors: %v / %v", errs[0], errs[1])
	}
	if elapsed >= 2*hold-50*time.Millisecond {
		t.Fatalf("prints to different addresses appear serialised: took %v (2x hold = %v)", elapsed, 2*hold)
	}
}

// TestNetworkTransport_RetriesRefusedConnection exercises the bounded
// retry on connect failures, via a fake dialFn (dial behind a function
// var, per design) rather than a real listener trick.
func TestNetworkTransport_RetriesRefusedConnection(t *testing.T) {
	orig := dialFn
	defer func() { dialFn = orig }()

	var attempts int32
	dialFn = func(ctx context.Context, network, addr string) (net.Conn, error) {
		n := atomic.AddInt32(&attempts, 1)
		if n == 1 {
			return nil, &net.OpError{Op: "dial", Net: network, Err: syscall.ECONNREFUSED}
		}
		client, server := net.Pipe()
		go io.Copy(io.Discard, server)
		return client, nil
	}

	tr := &networkTransport{addr: "127.0.0.1:1"}
	start := time.Now()
	err := tr.Print(context.Background(), []byte("hello"))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expected success after retry, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("expected 2 dial attempts (1 fail + 1 retry), got %d", got)
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("expected a backoff delay (~300ms) before the retry, only took %v", elapsed)
	}

	// A context already cancelled must return promptly without dialing at
	// all, let alone retrying.
	atomic.StoreInt32(&attempts, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start2 := time.Now()
	err2 := tr.Print(ctx, []byte("hello"))
	elapsed2 := time.Since(start2)
	if err2 == nil {
		t.Fatal("expected an error for an already-cancelled context")
	}
	if got := atomic.LoadInt32(&attempts); got != 0 {
		t.Fatalf("expected no dial attempts with an already-cancelled context, got %d", got)
	}
	if elapsed2 > 200*time.Millisecond {
		t.Fatalf("expected a prompt return for an already-cancelled context, took %v", elapsed2)
	}
}

// TestNetworkTransport_WriteErrorIsNotRetried: a write failure (the
// connection succeeded, then broke) must NOT be retried — the printer may
// already have printed part of the job, and a retry would double-print.
//
// Uses a fake dialFn returning a net.Pipe() end whose peer is already
// closed, rather than a real listener forcing a TCP RST: a real RST
// requires the client's Write to lose a race against the RST packet's
// arrival (it doesn't always — a real flake was observed with that
// design), whereas net.Pipe documents that a write against an
// already-closed peer fails every time, deterministically.
func TestNetworkTransport_WriteErrorIsNotRetried(t *testing.T) {
	orig := dialFn
	defer func() { dialFn = orig }()

	var dialCount int32
	dialFn = func(ctx context.Context, network, addr string) (net.Conn, error) {
		atomic.AddInt32(&dialCount, 1)
		client, server := net.Pipe()
		_ = server.Close() // peer already gone: client's Write fails deterministically
		return client, nil
	}

	tr := &networkTransport{addr: "127.0.0.1:9100"}
	err := tr.Print(context.Background(), []byte("hello"))
	if err == nil {
		t.Fatal("expected a write error")
	}
	if !strings.Contains(err.Error(), "printer write") {
		t.Fatalf("expected a %q error, got %v", "printer write", err)
	}
	if got := atomic.LoadInt32(&dialCount); got != 1 {
		t.Fatalf("expected exactly 1 dial attempt (no retry on write error), got %d", got)
	}
}

// --- deviceTransport: per-destination serialisation ----------------------
