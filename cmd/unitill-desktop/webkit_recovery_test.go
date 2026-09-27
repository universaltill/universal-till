package main

import (
	"testing"
	"time"
)

// webkit_recovery.go (ut-docs#2991, AC2) is the pure policy behind the
// Linux WebKit crash/load-failure recovery — untagged so plain `go test
// ./...` covers it; webkit_recovery_linux.go is only the cgo wiring.

func TestShouldRecoverLoadFailure(t *testing.T) {
	const base = "http://127.0.0.1:8080"
	cases := []struct {
		name   string
		uri    string
		domain string
		code   int
		want   bool
	}{
		{"connection refused on the till", "http://127.0.0.1:8080/sale", "WebKitNetworkError", 300, true},
		{"till root", "http://127.0.0.1:8080/", "WebKitNetworkError", 399, true},
		{"till root without path", "http://127.0.0.1:8080", "WebKitNetworkError", 399, true},
		{"internal error from a dead web process", "http://127.0.0.1:8080/sale?x=1#y", "WebKitPolicyError", 199, true},
		{"soup/gio transport error", "http://127.0.0.1:8080/admin", "g-io-error-quark", 39, true},
		{"navigation cancelled", "http://127.0.0.1:8080/sale", "WebKitNetworkError", 302, false},
		{"download interrupts the load", "http://127.0.0.1:8080/export.csv", "WebKitPolicyError", 102, false},
		{"plugin will handle load", "http://127.0.0.1:8080/x", "WebKitPluginError", 204, false},
		{"other origin", "https://example.com/", "WebKitNetworkError", 300, false},
		{"same host other port", "http://127.0.0.1:9090/sale", "WebKitNetworkError", 300, false},
		{"same host other scheme", "https://127.0.0.1:8080/sale", "WebKitNetworkError", 300, false},
		{"localhost is not 127.0.0.1", "http://localhost:8080/sale", "WebKitNetworkError", 300, false},
		{"about:blank", "about:blank", "WebKitNetworkError", 300, false},
		{"empty uri", "", "WebKitNetworkError", 300, false},
		{"garbage uri", "http://[::1", "WebKitNetworkError", 300, false},
		{"code 302 in another domain is not a cancel", "http://127.0.0.1:8080/", "WebKitPolicyError", 302, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRecoverLoadFailure(base, tc.uri, tc.domain, tc.code); got != tc.want {
				t.Fatalf("shouldRecoverLoadFailure(%q, %q, %q, %d) = %v, want %v", base, tc.uri, tc.domain, tc.code, got, tc.want)
			}
		})
	}
}

func TestShouldRecoverLoadFailure_DefaultPortsAndCase(t *testing.T) {
	if !shouldRecoverLoadFailure("http://127.0.0.1", "HTTP://127.0.0.1:80/x", "WebKitNetworkError", 300) {
		t.Fatal("explicit default port / upper-case scheme should be the same origin")
	}
	if shouldRecoverLoadFailure("::bad base", "http://127.0.0.1:8080/", "WebKitNetworkError", 300) {
		t.Fatal("an unparsable base URL must never match")
	}
}

func TestRecoveryTarget(t *testing.T) {
	const base = "http://127.0.0.1:8080"
	cases := map[string]string{
		"":                                 base,
		"about:blank":                      base,
		"http://127.0.0.1:8080/admin/item": "http://127.0.0.1:8080/admin/item",
		"https://example.com/":             base,
		"data:text/html,oops":              base,
	}
	for cur, want := range cases {
		if got := recoveryTarget(base, cur); got != want {
			t.Errorf("recoveryTarget(%q) = %q, want %q", cur, got, want)
		}
	}
}

func TestRecoveryBackoff_Sequence(t *testing.T) {
	var b recoveryBackoff
	want := []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		if got := b.next(); got != w {
			t.Fatalf("attempt %d: next() = %v, want %v", i+1, got, w)
		}
	}
	// Keeps retrying at the cap indefinitely — never gives up, never
	// overflows into a negative/zero delay after many attempts.
	for i := range 1000 {
		if got := b.next(); got != recoveryBackoffMax {
			t.Fatalf("attempt %d past the cap: next() = %v, want %v", len(want)+i+1, got, recoveryBackoffMax)
		}
	}
}

func TestRecoveryBackoff_Reset(t *testing.T) {
	var b recoveryBackoff
	for range 5 {
		b.next()
	}
	b.reset()
	if got := b.next(); got != 0 {
		t.Fatalf("after reset next() = %v, want 0", got)
	}
	if got := b.next(); got != time.Second {
		t.Fatalf("second after reset next() = %v, want 1s", got)
	}
}

// fakeAfter records scheduled callbacks so the test fires them by hand.
type fakeAfter struct {
	delays []time.Duration
	fns    []func()
}

func (f *fakeAfter) after(d time.Duration, fn func()) {
	f.delays = append(f.delays, d)
	f.fns = append(f.fns, fn)
}

func (f *fakeAfter) fire(t *testing.T, i int) {
	t.Helper()
	if i >= len(f.fns) {
		t.Fatalf("no scheduled callback #%d (have %d)", i, len(f.fns))
	}
	f.fns[i]()
}

func TestReloadScheduler_CollapsesDuplicatesAndBacksOff(t *testing.T) {
	fa := &fakeAfter{}
	clk := &recoveryClock{t: time.Unix(1_000_000, 0)}
	s := newReloadScheduler(fa.after, clk.now)
	var ran []string
	run := func(tag string) func() { return func() { ran = append(ran, tag) } }

	if d, ok := s.request(run("a")); !ok || d != 0 {
		t.Fatalf("first request = (%v, %v), want (0, true)", d, ok)
	}
	if _, ok := s.request(run("dup")); ok {
		t.Fatal("second request while one is pending was scheduled, want collapsed")
	}
	fa.fire(t, 0)
	if len(ran) != 1 || ran[0] != "a" {
		t.Fatalf("ran = %v, want [a]", ran)
	}
	// Reload failed again → next attempt is backed off.
	if d, ok := s.request(run("b")); !ok || d != time.Second {
		t.Fatalf("second attempt = (%v, %v), want (1s, true)", d, ok)
	}
	fa.fire(t, 1)
	if d, ok := s.request(run("c")); !ok || d != 2*time.Second {
		t.Fatalf("third attempt = (%v, %v), want (2s, true)", d, ok)
	}
	fa.fire(t, 2)
	// A commit that then stays up for the stable window resets the backoff.
	s.committed()
	clk.advance(recoveryStableAfter)
	if d, ok := s.request(run("d")); !ok || d != 0 {
		t.Fatalf("after commit = (%v, %v), want (0, true)", d, ok)
	}
	if got := len(fa.fns); got != 4 {
		t.Fatalf("scheduled %d callbacks, want 4", got)
	}
}

func TestReloadScheduler_ClosedStopsEverything(t *testing.T) {
	fa := &fakeAfter{}
	clk := &recoveryClock{t: time.Unix(1_000_000, 0)}
	s := newReloadScheduler(fa.after, clk.now)
	ran := 0
	if _, ok := s.request(func() { ran++ }); !ok {
		t.Fatal("request refused before close")
	}
	s.close()
	fa.fire(t, 0) // timer already armed before close fires afterwards
	if ran != 0 {
		t.Fatalf("callback ran %d times after close, want 0", ran)
	}
	if _, ok := s.request(func() { ran++ }); ok {
		t.Fatal("request scheduled after close")
	}
	if len(fa.fns) != 1 {
		t.Fatalf("scheduled %d callbacks, want 1", len(fa.fns))
	}
}

func TestReloadScheduler_CommitDropsStaleReload(t *testing.T) {
	fa := &fakeAfter{}
	clk := &recoveryClock{t: time.Unix(1_000_000, 0)}
	s := newReloadScheduler(fa.after, clk.now)
	ran := 0
	if _, ok := s.request(func() { ran++ }); !ok {
		t.Fatal("request refused")
	}
	s.committed() // the page loaded fine on its own meanwhile
	clk.advance(recoveryStableAfter)
	fa.fire(t, 0)
	if ran != 0 {
		t.Fatalf("stale reload ran %d times after a commit, want 0", ran)
	}
	// Not left stuck "pending": a later failure schedules again, from 0.
	if d, ok := s.request(func() { ran++ }); !ok || d != 0 {
		t.Fatalf("request after commit = (%v, %v), want (0, true)", d, ok)
	}
	fa.fire(t, 1)
	if ran != 1 {
		t.Fatalf("ran = %d, want 1", ran)
	}
}

type recoveryClock struct{ t time.Time }

func (c *recoveryClock) now() time.Time          { return c.t }
func (c *recoveryClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// Review of ut-docs#2991, MAJOR 1: a web process that crashes right after
// every commit (the post-upgrade library mismatch, or a page that OOMs the
// web process) must NOT reset the backoff on each commit — otherwise the
// shell reloads at 0s forever, as fast as WebKit can relaunch.
func TestReloadScheduler_CommitThenCrashLoopStaysBackedOff(t *testing.T) {
	fa := &fakeAfter{}
	clk := &recoveryClock{t: time.Unix(1_000_000, 0)}
	s := newReloadScheduler(fa.after, clk.now)
	want := []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, w := range want {
		d, ok := s.request(func() {})
		if !ok || d != w {
			t.Fatalf("attempt %d = (%v, %v), want (%v, true)", i, d, ok, w)
		}
		clk.advance(d)
		fa.fire(t, i)
		s.committed()            // the reload commits…
		clk.advance(time.Second) // …and the web process dies a second later
	}
}
