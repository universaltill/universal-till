package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/pos"
)

// The replica-side bridge (ADR-0079, ut-docs#1571): a background goroutine
// that, on a replica, holds GET {primary}/api/sync/orders/stream open with
// the sync bearer and republishes every event it receives onto THIS till's
// own OrderStatusBroadcaster — so the replica's browser-facing
// /api/orders/stream fans it out exactly as if the tap had happened here,
// and no page ever needs to know which till it is running on.
//
// Same "silent, retry, never blocks a sale" discipline as syncPushTick and
// fetchOrdersFromPrimary: Debugf on failure, bounded exponential backoff,
// exits promptly on ctx.Done().

// shortenBridgeTiming makes the bridge's retry/recheck cadence test-fast and
// restores it afterwards.
func shortenBridgeTiming(t *testing.T) {
	t.Helper()
	prevRecheck, prevMin, prevMax := orderStreamBridgeRecheck, orderStreamBridgeBackoffMin, orderStreamBridgeBackoffMax
	orderStreamBridgeRecheck = 20 * time.Millisecond
	orderStreamBridgeBackoffMin = 20 * time.Millisecond
	orderStreamBridgeBackoffMax = 80 * time.Millisecond
	t.Cleanup(func() {
		orderStreamBridgeRecheck, orderStreamBridgeBackoffMin, orderStreamBridgeBackoffMax = prevRecheck, prevMin, prevMax
	})
}

// writeSSEEvent writes one order-status frame the way the real primary does.
func writeSSEEvent(w http.ResponseWriter, ev pos.OrderStatusChanged) {
	fmt.Fprintf(w, "event: order-status\ndata: {\"receipt_no\":%q,\"status\":%q,\"actor_id\":%q,\"at\":%q}\n\n",
		ev.ReceiptNo, ev.Status, ev.ActorID, ev.At)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

func expectEvent(t *testing.T, ch <-chan pos.OrderStatusChanged, wantReceipt string) {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatal("subscriber channel closed unexpectedly")
		}
		if ev.ReceiptNo != wantReceipt {
			t.Fatalf("republished event = %+v, want receipt %s", ev, wantReceipt)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("event %s never reached the replica's own broadcaster", wantReceipt)
	}
}

// stopBridge cancels the bridge's ctx and asserts the goroutine actually
// exits — a bridge blocked mid-read that ignored ctx would hang app.Run's
// shutdown drain.
func stopBridge(t *testing.T, cancel context.CancelFunc, wg *sync.WaitGroup) {
	t.Helper()
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("bridge goroutine did not exit on ctx cancel — it would hang the shutdown drain")
	}
}

func TestOrderStatusStreamBridge_RepublishesPrimaryEvents(t *testing.T) {
	_, dp, _ := newOrderStatusTestDeps(t)
	shortenBridgeTiming(t)

	var gotPath, gotAuth atomic.Value
	hold := make(chan struct{})
	defer close(hold)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.Method + " " + r.URL.Path)
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, ": hello\n\n") // a comment the bridge must ignore
		writeSSEEvent(w, pos.OrderStatusChanged{ReceiptNo: "P-1", Status: "ready", ActorID: "op-9", At: "2026-09-04T12:00:00Z"})
		// Hold the connection open like a real primary would.
		select {
		case <-r.Context().Done():
		case <-hold:
		}
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	ch, cancelSub := dp.OrderStatus.Subscribe()
	defer cancelSub()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel() // safety net: stopBridge below also cancels, but a t.Fatal before reaching it must not skip cancellation and leave defer primary.Close() blocked on a still-open connection
	var wg sync.WaitGroup
	StartOrderStatusStreamBridge(ctx, dp, &wg)

	select {
	case ev := <-ch:
		want := pos.OrderStatusChanged{ReceiptNo: "P-1", Status: "ready", ActorID: "op-9", At: "2026-09-04T12:00:00Z"}
		if ev != want {
			t.Fatalf("republished event = %+v, want %+v", ev, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("primary's event never reached the replica's own broadcaster")
	}
	if p, _ := gotPath.Load().(string); p != "GET /api/sync/orders/stream" {
		t.Fatalf("bridge called %q, want GET /api/sync/orders/stream", p)
	}
	if a, _ := gotAuth.Load().(string); a != "Bearer b-123" {
		t.Fatalf("bridge must present the sync bearer, got %q", a)
	}

	// Shutdown while CONNECTED and blocked mid-read: must still exit promptly.
	stopBridge(t, cancel, &wg)
}

// A primary that drops the stream (restart, network blip) is reconnected to
// — two events served across two separate connections both arrive.
func TestOrderStatusStreamBridge_ReconnectsAfterPrimaryCloses(t *testing.T) {
	_, dp, _ := newOrderStatusTestDeps(t)
	shortenBridgeTiming(t)

	var conns atomic.Int64
	hold := make(chan struct{})
	defer close(hold)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := conns.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		writeSSEEvent(w, pos.OrderStatusChanged{ReceiptNo: fmt.Sprintf("P-%d", n), Status: "preparing"})
		if n == 1 {
			return // primary drops the first connection right after the event
		}
		select {
		case <-r.Context().Done():
		case <-hold:
		}
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	ch, cancelSub := dp.OrderStatus.Subscribe()
	defer cancelSub()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel() // safety net: stopBridge below also cancels, but a t.Fatal before reaching it must not skip cancellation and leave defer primary.Close() blocked on a still-open connection
	var wg sync.WaitGroup
	StartOrderStatusStreamBridge(ctx, dp, &wg)

	expectEvent(t, ch, "P-1")
	expectEvent(t, ch, "P-2")
	if got := conns.Load(); got < 2 {
		t.Fatalf("want a reconnect (>=2 connections), got %d", got)
	}
	stopBridge(t, cancel, &wg)
}

// Not a replica (no primary URL) → never calls anything, keeps re-checking;
// enrolment happens after boot, so a till that BECOMES a replica later must
// be picked up without a restart.
func TestOrderStatusStreamBridge_PicksUpLateEnrolment(t *testing.T) {
	_, dp, _ := newOrderStatusTestDeps(t)
	shortenBridgeTiming(t)

	var calls atomic.Int64
	hold := make(chan struct{})
	defer close(hold)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-hold:
		}
	}))
	defer primary.Close()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel() // safety net: stopBridge below also cancels, but a t.Fatal before reaching it must not skip cancellation and leave defer primary.Close() blocked on a still-open connection
	var wg sync.WaitGroup
	StartOrderStatusStreamBridge(ctx, dp, &wg) // standalone till: nothing configured

	time.Sleep(150 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("a non-replica must never call a primary, got %d calls", got)
	}

	setReplicaSettings(t, dp.Settings, primary.URL, "b-123") // enrolled now
	if !waitFor(t, 3*time.Second, func() bool { return calls.Load() >= 1 }) {
		t.Fatal("bridge never noticed the till became a replica")
	}
	stopBridge(t, cancel, &wg)
}

// A reachable primary that refuses (401 — e.g. a bearer revoked on the
// primary) or is otherwise unhappy is retried with bounded backoff: enough
// attempts to recover quickly, never a busy loop hammering the LAN.
func TestOrderStatusStreamBridge_BacksOffOnRefusal(t *testing.T) {
	_, dp, _ := newOrderStatusTestDeps(t)
	shortenBridgeTiming(t) // min 20ms, max 80ms

	var calls atomic.Int64
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"data":null,"error":"unauthorized"}`)
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-stale")

	ctx, cancel := context.WithCancel(t.Context())
	var wg sync.WaitGroup
	StartOrderStatusStreamBridge(ctx, dp, &wg)
	time.Sleep(500 * time.Millisecond)
	stopBridge(t, cancel, &wg)

	// 500ms with backoff 20→40→80→80…: roughly 2 + ~5 = ≤ ~10 attempts. A
	// busy loop would make thousands; no retry at all would make exactly 1.
	got := calls.Load()
	if got < 2 {
		t.Fatalf("a refused stream must be retried, got %d attempt(s)", got)
	}
	if got > 30 {
		t.Fatalf("retry must back off, not busy-loop: %d attempts in 500ms", got)
	}
}

// The idle cutoff (independent review, ADR-0079): the failure mode a dropped
// connection does NOT cover — a primary that still holds the TCP connection
// open but has stopped producing anything at all (a half-dead process, a NAT
// that black-holes the return path). Nothing ever errors the reader, so
// without orderStreamBridgeIdleCutoff the bridge would sit on a live-looking
// socket for the rest of the shop's day and the replica's board would
// silently be back to the 15s poll with no push at all — exactly the bug
// ut-docs#1571 reported, reintroduced through a different door. A silent
// stream must be treated as dead and re-dialled; the primary's `: ping`
// heartbeat (orderStreamHeartbeat, well under the cutoff) is what keeps a
// healthy-but-quiet shop from tripping this.
func TestOrderStatusStreamBridge_ReDialsASilentStream(t *testing.T) {
	_, dp, _ := newOrderStatusTestDeps(t)
	shortenBridgeTiming(t)
	prevIdle := orderStreamBridgeIdleCutoff
	orderStreamBridgeIdleCutoff = 100 * time.Millisecond
	t.Cleanup(func() { orderStreamBridgeIdleCutoff = prevIdle })

	var conns atomic.Int64
	hold := make(chan struct{})
	defer close(hold)
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conns.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Deliberately silent from here: no events, and no heartbeat either.
		select {
		case <-r.Context().Done():
		case <-hold:
		}
	}))
	defer primary.Close()
	setReplicaSettings(t, dp.Settings, primary.URL, "b-123")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel() // stop the bridge before httptest.Server.Close waits on its connection
	var wg sync.WaitGroup
	StartOrderStatusStreamBridge(ctx, dp, &wg)

	if !waitFor(t, 5*time.Second, func() bool { return conns.Load() >= 2 }) {
		t.Fatalf("a silent stream must trip the idle cutoff and be re-dialled, got %d connection(s)", conns.Load())
	}
	stopBridge(t, cancel, &wg)
}

// The SSE parser, table-driven: only `data:` lines of an order-status (or
// unnamed) frame are decoded; comments, blank lines, unknown fields, foreign
// event names and malformed JSON are skipped without aborting the stream.
func TestReadOrderStatusSSE(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  []string // receipt numbers, in order
	}{
		{
			name:  "single named frame",
			input: "event: order-status\ndata: {\"receipt_no\":\"A\",\"status\":\"ready\"}\n\n",
			want:  []string{"A"},
		},
		{
			name:  "comments, blank lines and unknown fields ignored",
			input: ": ping\n\n\n: ping\nid: 7\nretry: 3000\nevent: order-status\ndata: {\"receipt_no\":\"B\"}\n\n: ping\n\n",
			want:  []string{"B"},
		},
		{
			name:  "unnamed frame still decodes",
			input: "data: {\"receipt_no\":\"C\"}\n\n",
			want:  []string{"C"},
		},
		{
			name:  "foreign event name skipped, stream continues",
			input: "event: kds-ticket\ndata: {\"receipt_no\":\"X\"}\n\nevent: order-status\ndata: {\"receipt_no\":\"D\"}\n\n",
			want:  []string{"D"},
		},
		{
			name:  "malformed JSON skipped, stream continues",
			input: "data: not json\n\ndata: {\"receipt_no\":\"E\"}\n\n",
			want:  []string{"E"},
		},
		{
			name:  "no space after colon and CRLF line endings",
			input: "event:order-status\r\ndata:{\"receipt_no\":\"F\"}\r\n\r\n",
			want:  []string{"F"},
		},
		{
			name:  "multiple frames in order",
			input: "data: {\"receipt_no\":\"G\"}\n\ndata: {\"receipt_no\":\"H\"}\n\n",
			want:  []string{"G", "H"},
		},
		{
			name:  "final frame without trailing blank line at EOF still decodes",
			input: "data: {\"receipt_no\":\"I\"}\n",
			want:  []string{"I"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			err := readOrderStatusSSE(strings.NewReader(tc.input), func(ev pos.OrderStatusChanged) {
				got = append(got, ev.ReceiptNo)
			})
			if err != nil {
				t.Fatalf("readOrderStatusSSE: %v", err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("decoded %v, want %v", got, tc.want)
			}
		})
	}
}
