package pages

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Order-status push over SSE (ADR-0079, ut-docs#1571): GET /api/orders/stream
// (session-authed, browser-facing) and GET /api/sync/orders/stream (bearer-
// authed, replica-bridge-facing) both stream one `event: order-status` frame
// per pos.OrderStatusChanged published on common.Deps.OrderStatus, share ONE
// writer loop (streamOrderStatus), heartbeat with a comment line, and
// unsubscribe the moment the client goes away.
//
// httptest.NewRecorder can't model a long-lived response, so the streaming
// tests drive the mux through httptest.NewServer with a real client and a
// cancellable request context — exactly what a browser EventSource (or the
// replica bridge) does on the wire.

// openOrderStream GETs url (with an optional bearer) and returns the response
// once its HEADERS have arrived — the handler flushes them before it ever
// blocks on the broadcaster — plus a line reader over the still-open body.
func openOrderStream(t *testing.T, ctx context.Context, url, bearer string) (*http.Response, *bufio.Reader) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, bufio.NewReader(resp.Body)
}

// readSSEBlock reads one SSE block: every line up to (excluding) the blank
// terminator line. The caller's request context bounds a hang.
func readSSEBlock(t *testing.T, br *bufio.Reader) []string {
	t.Helper()
	var lines []string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before a full block arrived (got %q): %v", lines, err)
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(lines) == 0 {
				continue // tolerate a leading blank
			}
			return lines
		}
		lines = append(lines, line)
	}
}

func assertStreamHeaders(t *testing.T, resp *http.Response) {
	t.Helper()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control = %q, want no-cache", cc)
	}
	if xb := resp.Header.Get("X-Accel-Buffering"); xb != "no" {
		t.Fatalf("X-Accel-Buffering = %q, want no (buffering proxies must pass frames through)", xb)
	}
}

// assertOrderStatusFrame checks one block is a correctly-framed order-status
// event whose data is the snake_case JSON form of want.
func assertOrderStatusFrame(t *testing.T, block []string, want pos.OrderStatusChanged) {
	t.Helper()
	var event, data string
	for _, l := range block {
		switch {
		case strings.HasPrefix(l, "event: "):
			event = strings.TrimPrefix(l, "event: ")
		case strings.HasPrefix(l, "data: "):
			data = strings.TrimPrefix(l, "data: ")
		}
	}
	if event != "order-status" {
		t.Fatalf("frame event = %q, want order-status (block %q)", event, block)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(data), &got); err != nil {
		t.Fatalf("data is not JSON: %v (%q)", err, data)
	}
	if got["receipt_no"] != want.ReceiptNo || got["status"] != want.Status || got["actor_id"] != want.ActorID || got["at"] != want.At {
		t.Fatalf("data = %v, want snake_case form of %+v", got, want)
	}
	for _, k := range []string{"receipt_no", "status", "actor_id", "at"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("data missing snake_case key %q: %q", k, data)
		}
	}
}

// waitFor polls cond until true or the deadline passes.
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func TestOrderStatusStream_DeliversPublishedEvent(t *testing.T) {
	mux, dp, _ := newOrderStatusTestDeps(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp, br := openOrderStream(t, ctx, srv.URL+"/api/orders/stream", "")
	assertStreamHeaders(t, resp)

	// The handler subscribed BEFORE it flushed headers, so a publish that
	// happens after Do() returned is guaranteed to reach this connection.
	want := pos.OrderStatusChanged{ReceiptNo: "R-S1", Status: pos.OrderStatusReady, ActorID: "op-1", At: "2026-09-04T10:00:00Z"}
	dp.OrderStatus.Publish(want)
	assertOrderStatusFrame(t, readSSEBlock(t, br), want)

	// A second event on the same connection — the loop keeps going.
	want2 := pos.OrderStatusChanged{ReceiptNo: "R-S2", Status: pos.OrderStatusPreparing, ActorID: "op-2", At: "2026-09-04T10:00:01Z"}
	dp.OrderStatus.Publish(want2)
	assertOrderStatusFrame(t, readSSEBlock(t, br), want2)
}

// The heartbeat is a comment line — invisible to EventSource, but it keeps
// an idle connection alive through proxies/NAT and lets the replica bridge's
// idle cutoff distinguish "quiet shop" from "dead primary".
func TestOrderStatusStream_HeartbeatKeepsConnectionAlive(t *testing.T) {
	mux, _, _ := newOrderStatusTestDeps(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	prev := orderStreamHeartbeat
	orderStreamHeartbeat = 30 * time.Millisecond
	t.Cleanup(func() { orderStreamHeartbeat = prev })

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp, br := openOrderStream(t, ctx, srv.URL+"/api/orders/stream", "")
	assertStreamHeaders(t, resp)

	block := readSSEBlock(t, br)
	if len(block) != 1 || block[0] != ": ping" {
		t.Fatalf("idle stream must emit a `: ping` comment heartbeat, got %q", block)
	}
}

// THE resource-cleanup path: when the browser navigates away (request
// context cancelled), the handler must return and its broadcaster
// subscription must be released — otherwise every page view leaks a
// subscriber for the process lifetime.
func TestOrderStatusStream_UnsubscribesOnClientDisconnect(t *testing.T) {
	mux, dp, _ := newOrderStatusTestDeps(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	resp, _ := openOrderStream(t, ctx, srv.URL+"/api/orders/stream", "")
	assertStreamHeaders(t, resp)
	if !waitFor(t, 2*time.Second, func() bool { return dp.OrderStatus.SubscriberCount() == 1 }) {
		t.Fatalf("open stream must hold exactly one subscription, got %d", dp.OrderStatus.SubscriberCount())
	}

	cancel() // client goes away
	if !waitFor(t, 3*time.Second, func() bool { return dp.OrderStatus.SubscriberCount() == 0 }) {
		t.Fatalf("subscription leaked after client disconnect: %d subscribers", dp.OrderStatus.SubscriberCount())
	}
	// Publishing afterwards must be a clean no-op (no send on a closed/
	// orphaned channel, no panic).
	dp.OrderStatus.Publish(pos.OrderStatusChanged{ReceiptNo: "R-AFTER"})
}

// Process shutdown: the replica bridge holds its stream open indefinitely,
// so without this every primary restart (self-update included) would sit
// out http.Server.Shutdown's full timeout. Closing the broadcaster (wired to
// bgCtx in init.go) ends every open stream immediately.
func TestOrderStatusStream_EndsWhenBroadcasterCloses(t *testing.T) {
	mux, dp, _ := newOrderStatusTestDeps(t)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp, br := openOrderStream(t, ctx, srv.URL+"/api/orders/stream", "")
	assertStreamHeaders(t, resp)
	if !waitFor(t, 2*time.Second, func() bool { return dp.OrderStatus.SubscriberCount() == 1 }) {
		t.Fatal("stream never subscribed")
	}

	dp.OrderStatus.Close()
	done := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(br)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stream must end cleanly (EOF) on broadcaster close, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream still open after the broadcaster closed — shutdown would hang on it")
	}
}

// noFlushWriter hides http.Flusher: a wrapper that can't flush can't stream.
type noFlushWriter struct{ http.ResponseWriter }

// Defensive refusals, same style as the rest of this package: no broadcaster
// wired (bare Deps) or a ResponseWriter that can't flush is a 500 up front,
// never a silently-buffered stream that looks connected and delivers nothing.
func TestOrderStatusStream_RefusesWhenItCannotStream(t *testing.T) {
	_, fullDeps, _ := newOrderStatusTestDeps(t)
	cases := []struct {
		name string
		deps *common.Deps
		wrap func(http.ResponseWriter) http.ResponseWriter
	}{
		{"no broadcaster", &common.Deps{Db: fullDeps.Db, Settings: fullDeps.Settings}, nil},
		{"writer cannot flush", fullDeps, func(w http.ResponseWriter) http.ResponseWriter { return noFlushWriter{w} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := http.NewServeMux()
			registerOrderStatus(mux, tc.deps)
			req := httptest.NewRequest(http.MethodGet, "/api/orders/stream", nil)
			rec := httptest.NewRecorder()
			var w http.ResponseWriter = rec
			if tc.wrap != nil {
				w = tc.wrap(rec)
			}
			mux.ServeHTTP(w, req)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500", rec.Code)
			}
		})
	}
}

// The bearer-authed sibling: 401 without / with a wrong bearer, in the same
// JSON envelope as GET /api/sync/orders — and nothing is subscribed for a
// refused caller.
func TestSyncOrdersStream_RequiresBearer(t *testing.T) {
	mux, dp, _ := newSyncOrdersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")

	for _, bearer := range []string{"", "wrong"} {
		req := httptest.NewRequest(http.MethodGet, "/api/sync/orders/stream", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("bearer %q: status = %d, want 401 (body %q)", bearer, rec.Code, rec.Body.String())
		}
		var envelope struct {
			Data  any `json:"data"`
			Error any `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("bearer %q: 401 must carry the JSON envelope, got %q", bearer, rec.Body.String())
		}
		if envelope.Error != "unauthorized" {
			t.Fatalf("bearer %q: error = %v, want unauthorized", bearer, envelope.Error)
		}
	}
	if got := dp.OrderStatus.SubscriberCount(); got != 0 {
		t.Fatalf("refused callers must not subscribe, got %d", got)
	}
}

func TestSyncOrdersStream_DeliversPublishedEvent(t *testing.T) {
	mux, dp, _ := newSyncOrdersTestDeps(t)
	seedSyncOrdersTill(t, dp, "Till 2", "bearer-t2")
	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	resp, br := openOrderStream(t, ctx, srv.URL+"/api/sync/orders/stream", "bearer-t2")
	assertStreamHeaders(t, resp)

	want := pos.OrderStatusChanged{ReceiptNo: "R-S9", Status: pos.OrderStatusCollected, ActorID: "Till 1", At: "2026-09-04T11:00:00Z"}
	dp.OrderStatus.Publish(want)
	assertOrderStatusFrame(t, readSSEBlock(t, br), want)
}
