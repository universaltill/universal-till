package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// watchShellMode (ADR-0064, ut-docs#1039) is the shell half of the polled
// window-control channel — untagged, like control.go/window_mode.go, so
// `go test ./...` (never run with -tags desktop) actually exercises it.

// pollServer scripts GET /api/window-mode?control=live responses: each call
// pops the next scripted (mode, rev); when the script is exhausted it
// blocks until release or the request context ends (a parked long poll).
type pollServer struct {
	t      *testing.T
	mu     sync.Mutex
	script []struct {
		mode string
		rev  uint64
	}
	reqs    []string // RawQuery per request, in order
	release chan struct{}
}

func (p *pollServer) handler(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	p.reqs = append(p.reqs, r.URL.RawQuery)
	var next *struct {
		mode string
		rev  uint64
	}
	if len(p.script) > 0 {
		next = &p.script[0]
		p.script = p.script[1:]
	}
	p.mu.Unlock()
	if next == nil {
		select {
		case <-p.release:
		case <-r.Context().Done():
			return
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":  map[string]any{"window_mode": next.mode, "launch_on_startup": false, "rev": next.rev},
		"error": nil,
	})
}

func (p *pollServer) queries() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.reqs...)
}

func TestWatchShellMode_AppliesChangesAndSendsControlParams(t *testing.T) {
	ps := &pollServer{t: t, release: make(chan struct{})}
	ps.script = []struct {
		mode string
		rev  uint64
	}{
		{"kiosk", 2},
		{"normal", 3},
	}
	srv := httptest.NewServer(http.HandlerFunc(ps.handler))
	defer srv.Close()
	defer close(ps.release)

	applied := make(chan string, 8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		watchShellMode(ctx, srv.Client(), srv.URL, "normal", 1, func(mode string) { applied <- mode })
		close(done)
	}()

	want := []string{"kiosk", "normal"}
	for _, w := range want {
		select {
		case got := <-applied:
			if got != w {
				t.Fatalf("applied %q, want %q", got, w)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("apply(%q) never happened", w)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watchShellMode did not stop on context cancel")
	}

	qs := ps.queries()
	if len(qs) < 2 {
		t.Fatalf("got %d requests, want >= 2: %v", len(qs), qs)
	}
	// First poll: advertises control=live, starts from the launch fetch's
	// rev, reports the launch-applied mode.
	first := mustParseQuery(t, qs[0])
	if first.Get("control") != "live" {
		t.Fatalf("first poll control = %q, want live: %s", first.Get("control"), qs[0])
	}
	if first.Get("since") != "1" {
		t.Fatalf("first poll since = %q, want 1 (the launch fetch's rev): %s", first.Get("since"), qs[0])
	}
	if first.Get("applied") != "normal" {
		t.Fatalf("first poll applied = %q, want normal: %s", first.Get("applied"), qs[0])
	}
	if first.Get("wait") == "" || first.Get("wait") == "0" {
		t.Fatalf("first poll wait = %q, want a long-poll hold: %s", first.Get("wait"), qs[0])
	}
	// Second poll: acknowledges the applied kiosk and moves since forward —
	// the ack that releases the server's exit-to-os WaitApplied.
	second := mustParseQuery(t, qs[1])
	if second.Get("since") != "2" {
		t.Fatalf("second poll since = %q, want 2: %s", second.Get("since"), qs[1])
	}
	if second.Get("applied") != "kiosk" {
		t.Fatalf("second poll applied = %q, want kiosk: %s", second.Get("applied"), qs[1])
	}
}

func TestWatchShellMode_UnchangedModeNotReapplied(t *testing.T) {
	ps := &pollServer{t: t, release: make(chan struct{})}
	// Same mode the shell already applied at launch — a wait timeout
	// answer, not a change.
	ps.script = []struct {
		mode string
		rev  uint64
	}{{"kiosk", 1}}
	srv := httptest.NewServer(http.HandlerFunc(ps.handler))
	defer srv.Close()
	defer close(ps.release)

	applied := make(chan string, 8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	// Join on exit so no watcher goroutine outlives this test and races a
	// later test's shellPollRetryDelay override.
	defer func() {
		cancel()
		<-done
	}()
	go func() {
		watchShellMode(ctx, srv.Client(), srv.URL, "kiosk", 1, func(mode string) { applied <- mode })
		close(done)
	}()

	select {
	case got := <-applied:
		t.Fatalf("apply(%q) called for an unchanged mode", got)
	case <-time.After(300 * time.Millisecond):
		// good — no spurious reapply
	}
}

// TestWatchShellMode_RetriesAfterErrorsWithBackoff: the watcher must
// survive both an error status and a dead server — retrying forever,
// paced by the backoff, never exiting and never hot-looping.
func TestWatchShellMode_RetriesAfterErrorsWithBackoff(t *testing.T) {
	oldDelay := shellPollRetryDelay
	shellPollRetryDelay = 20 * time.Millisecond
	defer func() { shellPollRetryDelay = oldDelay }()

	var mu sync.Mutex
	calls := 0
	fail := true
	ps := &pollServer{t: t, release: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		f := fail
		mu.Unlock()
		if f {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		ps.handler(w, r)
	}))
	defer srv.Close()
	defer close(ps.release)
	ps.script = []struct {
		mode string
		rev  uint64
	}{{"fullscreen", 5}}

	applied := make(chan string, 8)
	ctx, cancel := context.WithCancel(context.Background())
	watcherDone := make(chan struct{})
	// Join the watcher before the deferred shellPollRetryDelay restore runs
	// (defers are LIFO), or the still-running goroutine races the restore.
	defer func() {
		cancel()
		<-watcherDone
	}()
	go func() {
		watchShellMode(ctx, srv.Client(), srv.URL, "normal", 1, func(mode string) { applied <- mode })
		close(watcherDone)
	}()

	// Let it hit the failing server a few times.
	time.Sleep(150 * time.Millisecond)
	mu.Lock()
	afterErrors := calls
	fail = false
	mu.Unlock()
	if afterErrors < 2 {
		t.Fatalf("calls during failure window = %d, want >= 2 (must keep retrying)", afterErrors)
	}
	if afterErrors > 20 {
		t.Fatalf("calls during failure window = %d — hot loop, backoff not honoured", afterErrors)
	}

	// Once the server recovers, the change is applied.
	select {
	case got := <-applied:
		if got != "fullscreen" {
			t.Fatalf("applied %q after recovery, want fullscreen", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no apply after the server recovered — watcher gave up")
	}
}

func TestWatchShellMode_SurvivesConnectionFailure(t *testing.T) {
	oldDelay := shellPollRetryDelay
	shellPollRetryDelay = 20 * time.Millisecond

	// Nothing listening at all — the loop must not exit; it must still be
	// running (and stoppable) after several failed dials.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	// Join before restoring the delay var (see the retry test's reasoning).
	defer func() {
		cancel()
		<-done
		shellPollRetryDelay = oldDelay
	}()
	go func() {
		watchShellMode(ctx, &http.Client{Timeout: time.Second}, "http://127.0.0.1:1", "normal", 1, func(string) {
			t.Error("apply called with no server")
		})
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("watchShellMode exited on connection failure — must retry forever")
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("watchShellMode did not stop on context cancel")
	}
}

// TestShellAppliesWindowModeGatesTheAdvertise: only a platform with a real
// applyWindowMode may claim control=live (window_mode_linux.go's init sets
// it) — an untagged build, like macOS and Windows today, must NOT advertise
// a capability it lacks, or the server would serve it a chrome-hiding mode
// it can never leave.
func TestShellAppliesWindowModeGatesTheAdvertise(t *testing.T) {
	if shellAppliesWindowMode {
		t.Fatal("shellAppliesWindowMode = true in an untagged build — only desktop&&linux may set it")
	}
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":{"window_mode":"normal","launch_on_startup":false,"rev":7},"error":null}`)
	}))
	defer srv.Close()

	// Gated off: a plain read, no capability claim.
	_ = fetchShellPrefsWithControl(srv.URL, false)
	if q := mustParseQuery(t, gotQuery); q.Get("control") != "" {
		t.Fatalf("live=false sent control=%q, want no control param (query %q)", q.Get("control"), gotQuery)
	}

	// Gated on: the claim, and the rev comes back for the watcher to start
	// from.
	prefs := fetchShellPrefsWithControl(srv.URL, true)
	if q := mustParseQuery(t, gotQuery); q.Get("control") != "live" {
		t.Fatalf("live=true sent control=%q, want live (query %q)", q.Get("control"), gotQuery)
	}
	if prefs.Rev != 7 {
		t.Fatalf("prefs.Rev = %d, want 7", prefs.Rev)
	}
}

func mustParseQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	q, err := url.ParseQuery(raw)
	if err != nil {
		t.Fatalf("parse query %q: %v", raw, err)
	}
	return q
}
