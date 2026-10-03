package fleetlink

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// brokenWriteConn is a Conn whose writes start failing once broken is
// closed — the main till has already torn its socket down — while frames
// it sent before that can still be read. Read never returns a frame once
// Close has run, the way a closed socket drops what it had buffered.
type brokenWriteConn struct {
	in         chan []byte
	broken     chan struct{}
	writeFail  chan struct{} // closed on the first failed write
	failOnce   sync.Once
	closeOnce  sync.Once
	done       chan struct{}
	closedAtMu sync.Mutex
	closedAt   time.Time
}

func newBrokenWriteConn() *brokenWriteConn {
	return &brokenWriteConn{
		in:        make(chan []byte, 8),
		broken:    make(chan struct{}),
		writeFail: make(chan struct{}),
		done:      make(chan struct{}),
	}
}

func (b *brokenWriteConn) Read(ctx context.Context) ([]byte, error) {
	select {
	case <-b.done:
		return nil, errors.New("use of closed network connection")
	default:
	}
	select {
	case m := <-b.in:
		return m, nil
	case <-b.done:
		return nil, errors.New("use of closed network connection")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (b *brokenWriteConn) Write(context.Context, []byte) error {
	select {
	case <-b.broken:
		b.failOnce.Do(func() { close(b.writeFail) })
		return errors.New("write: broken pipe")
	default:
		return nil
	}
}

func (b *brokenWriteConn) Close(CloseCode, string) {
	b.closeOnce.Do(func() {
		b.closedAtMu.Lock()
		b.closedAt = time.Now()
		b.closedAtMu.Unlock()
		close(b.done)
	})
}

func (b *brokenWriteConn) send(t *testing.T, e Envelope) {
	t.Helper()
	m, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	b.in <- m
}

// runBrokenLink links a client over conn, breaks its writes, and returns
// how the link ended once onWriteFail has run.
func runBrokenLink(t *testing.T, onWriteFail func(*brokenWriteConn)) (linkEnd, []string, *brokenWriteConn) {
	t.Helper()
	cfg := fastConfig()
	rec := &clientRec{}
	tt := &testTarget{}
	tt.set(Target{BaseURL: "http://main.invalid", Bearer: "good-till-2"})
	c := NewClient(fastClientOptions(cfg, tt, rec, advertised()))
	conn := newBrokenWriteConn()

	type result struct {
		end         linkEnd
		established bool
	}
	res := make(chan result, 1)
	go func() {
		end, est := c.runLink(context.Background(), Target{BaseURL: "http://main.invalid", Bearer: "good-till-2"}, conn)
		res <- result{end, est}
	}()

	conn.send(t, mustMsg(t, "m1", TypeHello, "", Hello{TillID: "main", Version: "v1.0.0", SyncProtocol: SyncProtocolLevel}))
	waitFor(t, "linked", c.Linked)

	close(conn.broken) // the next ping (PingInterval) fails
	select {
	case <-conn.writeFail:
	case <-time.After(2 * time.Second):
		t.Fatal("no write failed")
	}
	onWriteFail(conn)

	select {
	case r := <-res:
		if !r.established {
			t.Fatal("link never established")
		}
		_, _, lost, _ := rec.get()
		return r.end, lost, conn
	case <-time.After(3 * time.Second):
		t.Fatal("link did not end after its write failed")
	}
	return 0, nil, nil
}

// A main till that says bye and closes its socket can make the replica's
// in-flight ping fail before the replica has read the bye. The bye frame
// already arrived, so the link ended as a bye — not a lost link, and no
// OnLost (ut-docs#3571).
func TestClient_WriteFailureStillReadsAQueuedBye(t *testing.T) {
	end, lost, _ := runBrokenLink(t, func(conn *brokenWriteConn) {
		// The bye lands after the writer saw the failure (on a real
		// socket it is already queued; the reader just hasn't run yet).
		conn.send(t, mustMsg(t, "m2", TypeBye, "", ByePayload{Reason: ByeShutdown}))
	})
	if end != endBye {
		t.Fatalf("link ended as %d, want endBye (%d)", end, endBye)
	}
	if len(lost) != 0 {
		t.Fatalf("a bye counted as a failed contact: %v", lost)
	}
}

// A write failure with nothing more to read is still a lost link
// (ADR-0114 §4), and the grace for a queued frame is bounded: the conn is
// closed soon after the failure, not held open.
func TestClient_WriteFailureWithNoByeIsStillLost(t *testing.T) {
	var failedAt time.Time
	end, lost, conn := runBrokenLink(t, func(*brokenWriteConn) { failedAt = time.Now() })
	if end != endLost {
		t.Fatalf("link ended as %d, want endLost (%d)", end, endLost)
	}
	if len(lost) != 1 {
		t.Fatalf("OnLost calls = %v, want exactly one", lost)
	}
	conn.closedAtMu.Lock()
	held := conn.closedAt.Sub(failedAt)
	conn.closedAtMu.Unlock()
	if limit := 2 * fastConfig().WriteTimeout; held > limit {
		t.Fatalf("conn held open %v after the write failed, want at most ~WriteTimeout (%v)", held, limit)
	}
}
