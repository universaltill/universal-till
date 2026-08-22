package discovery

import (
	"context"
	"errors"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/mdns"
)

func TestCandidateFromEntry_ParsesNameAndID(t *testing.T) {
	e := &mdns.ServiceEntry{
		Name:       "till-abc123._unitill-sync._tcp.local.",
		InfoFields: []string{"v=1", "name=Task Runner", "id=till-abc123"},
		AddrV4:     net.IPv4(192, 168, 1, 50),
		Port:       8080,
	}
	c, ok := candidateFromEntry(e)
	if !ok {
		t.Fatal("expected a candidate to be extracted")
	}
	if c.Name != "Task Runner" || c.TillID != "till-abc123" {
		t.Fatalf("got %+v, want Name=%q TillID=%q", c, "Task Runner", "till-abc123")
	}
}

func TestCandidateFromEntry_RejectsEntryWithoutID(t *testing.T) {
	e := &mdns.ServiceEntry{
		Name:       "mystery._unitill-sync._tcp.local.",
		InfoFields: []string{"v=1", "name=Task Runner"},
		AddrV4:     net.IPv4(192, 168, 1, 50),
		Port:       8080,
	}
	if _, ok := candidateFromEntry(e); ok {
		t.Fatal("expected an entry with no id= field to be rejected — a candidate with no till id is useless downstream")
	}
}

func TestCandidateFromEntry_DefaultsNameWhenMissing(t *testing.T) {
	e := &mdns.ServiceEntry{
		Name:       "till-xyz._unitill-sync._tcp.local.",
		InfoFields: []string{"v=1", "id=till-xyz"},
		AddrV4:     net.IPv4(192, 168, 1, 50),
		Port:       8080,
	}
	c, ok := candidateFromEntry(e)
	if !ok {
		t.Fatal("expected a candidate even without a name= field")
	}
	if c.Name == "" {
		t.Fatal("expected a non-empty default name")
	}
	if c.TillID != "till-xyz" {
		t.Fatalf("got TillID=%q, want %q", c.TillID, "till-xyz")
	}
}

// TestCandidateFromEntry_BuildsBaseURLFromV4AddrAndPort — a replica needs an
// address to actually send POST /api/sync/pair-request to, per ADR-0033
// point 2 (universaltill/ut-docs#185). Without this, discovery only ever
// produced a name + id, useless for the pairing flow this card wires up.
func TestCandidateFromEntry_BuildsBaseURLFromV4AddrAndPort(t *testing.T) {
	e := &mdns.ServiceEntry{
		InfoFields: []string{"id=till-abc123", "name=Task Runner"},
		AddrV4:     net.IPv4(192, 168, 1, 50),
		Port:       8080,
	}
	c, ok := candidateFromEntry(e)
	if !ok {
		t.Fatal("expected a candidate to be extracted")
	}
	if want := "http://192.168.1.50:8080"; c.BaseURL != want {
		t.Fatalf("got BaseURL=%q, want %q", c.BaseURL, want)
	}
}

// TestCandidateFromEntry_FallsBackToV6Addr covers a responder answering only
// on IPv6 — AddrV4 is nil but AddrV6 is usable.
func TestCandidateFromEntry_FallsBackToV6Addr(t *testing.T) {
	e := &mdns.ServiceEntry{
		InfoFields: []string{"id=till-abc123", "name=Task Runner"},
		AddrV6:     net.ParseIP("fe80::1"),
		Port:       8080,
	}
	c, ok := candidateFromEntry(e)
	if !ok {
		t.Fatal("expected a candidate to be extracted")
	}
	if want := "http://[fe80::1]:8080"; c.BaseURL != want {
		t.Fatalf("got BaseURL=%q, want %q", c.BaseURL, want)
	}
}

// TestCandidateFromEntry_RejectsEntryWithoutUsableAddress — a candidate with
// no address is exactly as useless to the pairing flow as one with no id
// (see TestCandidateFromEntry_RejectsEntryWithoutID above): there is nothing
// to send a pair-request to.
func TestCandidateFromEntry_RejectsEntryWithoutUsableAddress(t *testing.T) {
	e := &mdns.ServiceEntry{
		InfoFields: []string{"id=till-abc123", "name=Task Runner"},
		Port:       8080,
	}
	if _, ok := candidateFromEntry(e); ok {
		t.Fatal("expected an entry with neither AddrV4 nor AddrV6 to be rejected")
	}
}

// TestCandidateFromEntry_RejectsEntryWithoutPort — an address alone (port 0)
// can't be dialed either.
func TestCandidateFromEntry_RejectsEntryWithoutPort(t *testing.T) {
	e := &mdns.ServiceEntry{
		InfoFields: []string{"id=till-abc123", "name=Task Runner"},
		AddrV4:     net.IPv4(192, 168, 1, 50),
	}
	if _, ok := candidateFromEntry(e); ok {
		t.Fatal("expected an entry with port 0 to be rejected")
	}
}

// forceIPv6Supported overrides the ipv6Supported seam for the duration of
// the test, so Browse's control flow is deterministic regardless of
// whatever IPv6 support the machine actually running this test has —
// several tests below assert an exact mdnsQuery call sequence that only
// happens on the v4+v6-first path (ut-docs#272 added a v4-only fast path
// for hosts without IPv6 support at all, exercised by its own tests below).
func forceIPv6Supported(t *testing.T, v bool) {
	t.Helper()
	orig := ipv6Supported
	ipv6Supported = func() bool { return v }
	t.Cleanup(func() { ipv6Supported = orig })
}

// TestDetectIPv6Support_ReturnsWithoutPanicking is a light smoke test for
// the real (non-seamed) implementation — every other test below exercises
// Browse's control flow deterministically via the ipv6Supported seam
// instead; this just confirms the actual socket check is wired correctly.
func TestDetectIPv6Support_ReturnsWithoutPanicking(t *testing.T) {
	_ = detectIPv6Support()
}

// TestBrowse_RespectsAlreadyCancelledContext exercises Browse's ctx-aware
// exit path without needing a real mDNS responder on the network: an
// already-cancelled ctx must return immediately with ctx.Err(), not block
// for the full timeout.
func TestBrowse_RespectsAlreadyCancelledContext(t *testing.T) {
	forceIPv6Supported(t, true)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := Browse(ctx, 3*time.Second)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error for an already-cancelled context")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("expected Browse to return promptly on a cancelled context, took %s", elapsed)
	}
}

// TestBrowse_DoesNotLeakCollectorGoroutineWhenCancelledMidScan covers the
// case TestBrowse_RespectsAlreadyCancelledContext does NOT: a ctx that is
// cancelled *during* an in-flight scan, which reaches Browse's select
// rather than its early ctx.Err() return.
//
// Browse fans out two goroutines — the mdns query and a collector ranging
// over the entries channel. If the cancellation path returns without ever
// closing entries, that collector blocks on `range entries` forever: a
// permanent, per-request goroutine leak on a long-running POS process,
// triggered by something as ordinary as a manager closing the Tills tab
// mid-scan.
func TestBrowse_DoesNotLeakCollectorGoroutineWhenCancelledMidScan(t *testing.T) {
	forceIPv6Supported(t, true)

	queryReturned := make(chan struct{})
	orig := mdnsQuery
	t.Cleanup(func() { mdnsQuery = orig })
	mdnsQuery = func(p *mdns.QueryParam) error {
		// A real scan keeps running for its full timeout even after the
		// caller has given up; emulate that, then return as Query does.
		time.Sleep(100 * time.Millisecond)
		p.Entries <- &mdns.ServiceEntry{InfoFields: []string{"id=late-answer", "name=Late"}, AddrV4: net.IPv4(192, 168, 1, 51), Port: 8080}
		close(queryReturned)
		return nil
	}

	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := Browse(ctx, 3*time.Second); err == nil {
		t.Fatal("expected a cancellation error")
	}

	<-queryReturned // the scan itself is now finished; nothing legitimate is still running

	// Give the collector a generous window to wind down, then assert it did.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutine leak after cancelled Browse: %d goroutines before, %d still running after "+
		"the scan finished — the entries channel is never closed on the cancellation path, "+
		"so the collector goroutine blocks on `range entries` forever",
		before, runtime.NumGoroutine())
}

// TestBrowse_CapsCandidatesFromAFloodingResponder — discovery is a
// LAN-open surface: any host on the network can answer, and nothing
// authenticates a responder. An unbounded append means one rogue or
// malfunctioning host can grow this slice (and the JSON response built
// from it) for the whole scan window. Results must be capped.
func TestBrowse_CapsCandidatesFromAFloodingResponder(t *testing.T) {
	forceIPv6Supported(t, true)

	orig := mdnsQuery
	t.Cleanup(func() { mdnsQuery = orig })
	mdnsQuery = func(p *mdns.QueryParam) error {
		for i := 0; i < maxCandidates*10; i++ {
			p.Entries <- &mdns.ServiceEntry{
				InfoFields: []string{"id=flood-" + strconv.Itoa(i), "name=Flood"},
				AddrV4:     net.IPv4(192, 168, 1, 52),
				Port:       8080,
			}
		}
		return nil
	}

	got, err := Browse(context.Background(), 3*time.Second)
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(got) > maxCandidates {
		t.Fatalf("Browse returned %d candidates from a flooding responder, want at most %d — "+
			"an unauthenticated LAN peer must not be able to grow this without bound",
			len(got), maxCandidates)
	}
}

// TestBrowse_ReturnsCollectedCandidatesDespiteALateQueryError covers
// ut-docs#538's acceptance criterion directly: candidates already collected
// before mdns.Query reports an error must survive, not be thrown away. A
// partial failure is not a total one.
func TestBrowse_ReturnsCollectedCandidatesDespiteALateQueryError(t *testing.T) {
	forceIPv6Supported(t, true)

	orig := mdnsQuery
	t.Cleanup(func() { mdnsQuery = orig })
	mdnsQuery = func(p *mdns.QueryParam) error {
		p.Entries <- &mdns.ServiceEntry{InfoFields: []string{"id=till-real", "name=Real Till"}, AddrV4: net.IPv4(192, 168, 1, 60), Port: 8080}
		return errors.New("write udp6 [::]:57143->[ff02::fb]:5353: sendto: no route to host")
	}

	got, err := Browse(context.Background(), 3*time.Second)
	if err != nil {
		t.Fatalf("Browse: unexpected error %v — a candidate was already collected before the query error", err)
	}
	if len(got) != 1 || got[0].TillID != "till-real" {
		t.Fatalf("got %+v, want the one candidate collected before the error to survive", got)
	}
}

// TestBrowse_RetriesIPv4OnlyWhenTheFullQueryFailsOutright is the direct
// regression test for ut-docs#538. hashicorp/mdns's client.query() sends
// the v4 leg then the v6 leg of the same probe packet from one sendQuery
// call, and returns immediately — before ever listening for a response —
// if either leg's write fails (see vendor client.go: sendQuery aborts on
// the first error, and query() returns that error before entering its
// receive loop). So on a LAN where IPv6 multicast has no route (socket
// binds fine, sendto doesn't), the real v4+v6 attempt can never collect
// anything — the "just keep what was already collected" fix by itself
// (the test above) would still return zero candidates for this exact bug.
// The actual fix is a same-scan retry with IPv6 disabled.
func TestBrowse_RetriesIPv4OnlyWhenTheFullQueryFailsOutright(t *testing.T) {
	forceIPv6Supported(t, true)

	orig := mdnsQuery
	t.Cleanup(func() { mdnsQuery = orig })

	var mu sync.Mutex
	var calls []bool // recorded DisableIPv6 per call, in order
	mdnsQuery = func(p *mdns.QueryParam) error {
		mu.Lock()
		calls = append(calls, p.DisableIPv6)
		mu.Unlock()
		if !p.DisableIPv6 {
			// Mirrors the real library: fails instantly, nothing collected.
			return errors.New("write udp6 [::]:57143->[ff02::fb]:5353: sendto: no route to host")
		}
		p.Entries <- &mdns.ServiceEntry{InfoFields: []string{"id=till-v4only", "name=V4 Only Till"}, AddrV4: net.IPv4(192, 168, 1, 61), Port: 8080}
		return nil
	}

	got, err := Browse(context.Background(), 3*time.Second)
	if err != nil {
		t.Fatalf("Browse: unexpected error %v — the v4-only retry should have recovered the peer", err)
	}
	if len(got) != 1 || got[0].TillID != "till-v4only" {
		t.Fatalf("got %+v, want the v4-only retry's one candidate", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 || calls[0] != false || calls[1] != true {
		t.Fatalf("got mdnsQuery calls (DisableIPv6 per call) = %v, want [false, true] — "+
			"a v4+v6 attempt first, then a v4-only retry only after it fails", calls)
	}
}

// TestBrowse_DoesNotRetryOnAGenuineEmptyResult — a scan that genuinely
// finds nothing (err == nil, zero entries) is "no peers on this network,"
// not a failure, and must not trigger the v4-only retry path — the UI
// distinguishes "no peers found" from "the scan failed" (ut-docs#538's
// third acceptance criterion) via exactly this: err == nil here.
func TestBrowse_DoesNotRetryOnAGenuineEmptyResult(t *testing.T) {
	forceIPv6Supported(t, true)

	orig := mdnsQuery
	t.Cleanup(func() { mdnsQuery = orig })
	var calls int
	var mu sync.Mutex
	mdnsQuery = func(p *mdns.QueryParam) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return nil
	}

	got, err := Browse(context.Background(), 3*time.Second)
	if err != nil {
		t.Fatalf("Browse: unexpected error %v for a genuine empty result", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d candidates, want 0", len(got))
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("got %d mdnsQuery calls, want exactly 1 — a clean empty result must not trigger the v4-only retry", calls)
	}
}

// TestBrowse_ReturnsErrorWhenBothTheFullAndV4OnlyRetryFail — if even the
// v4-only retry can't complete a scan (e.g. no usable network interface at
// all), Browse must still surface an error rather than silently reporting
// "no peers found," which would look identical to a genuinely empty LAN.
func TestBrowse_ReturnsErrorWhenBothTheFullAndV4OnlyRetryFail(t *testing.T) {
	forceIPv6Supported(t, true)

	orig := mdnsQuery
	t.Cleanup(func() { mdnsQuery = orig })
	var calls int
	var mu sync.Mutex
	mdnsQuery = func(p *mdns.QueryParam) error {
		mu.Lock()
		calls++
		mu.Unlock()
		return errors.New("failed to bind to any multicast udp port")
	}

	got, err := Browse(context.Background(), 3*time.Second)
	if err == nil {
		t.Fatal("expected an error when both the v4+v6 attempt and the v4-only retry fail")
	}
	if len(got) != 0 {
		t.Fatalf("got %d candidates on a total failure, want 0", len(got))
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("got %d mdnsQuery calls, want exactly 2 (v4+v6 attempt, then v4-only retry)", calls)
	}
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		// Both legs failed for network reasons, so the wrapped errors must
		// still be inspectable by a caller (%w, not %v).
		if !strings.Contains(err.Error(), "failed to bind to any multicast udp port") {
			t.Fatalf("error %q does not carry the underlying failure", err)
		}
	}
}

// TestBrowse_ReportsCancellationDuringTheV4OnlyRetryAsAContextError — the
// retry ut-docs#538 added opened a second window in which the caller can
// give up (manager closes the Tills tab). Cancellation there is still the
// caller giving up, not a network failure: Browse must surface
// context.Canceled, exactly as it does when the first attempt is
// cancelled, rather than reshaping it into a "lan scan failed" error the
// handler logs and answers 500 to.
func TestBrowse_ReportsCancellationDuringTheV4OnlyRetryAsAContextError(t *testing.T) {
	forceIPv6Supported(t, true)

	orig := mdnsQuery
	t.Cleanup(func() { mdnsQuery = orig })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var mu sync.Mutex
	calls := 0
	mdnsQuery = func(p *mdns.QueryParam) error {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n == 1 {
			// The real bug's shape: instant failure, nothing collected.
			return errors.New("write udp6 [::]:57143->[ff02::fb]:5353: sendto: no route to host")
		}
		// The retry is in flight when the caller gives up.
		cancel()
		time.Sleep(50 * time.Millisecond)
		return nil
	}

	got, err := Browse(ctx, 3*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got error %v, want context.Canceled — cancelling during the v4-only retry is the caller giving up", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d candidates on a cancelled retry, want 0", len(got))
	}
}

// TestBrowse_SkipsV4V6AttemptWhenHostHasNoIPv6Support is the direct
// regression test for ut-docs#272: mdns's client always tries udp6 first
// and logs two [ERR] lines straight to the global stdlib logger — bypassing
// internal/logging entirely — before Browse's own v4-only retry ever gets a
// chance to run, on any host that can't open IPv6 sockets at all
// (containers, many Pi/kiosk images, IPv6-disabled sandboxes — including,
// per the original report, the CI sandbox itself). Browse must go straight
// to a single v4-only attempt on such a host instead of provoking that
// noise on every single scan.
func TestBrowse_SkipsV4V6AttemptWhenHostHasNoIPv6Support(t *testing.T) {
	forceIPv6Supported(t, false)

	orig := mdnsQuery
	t.Cleanup(func() { mdnsQuery = orig })
	var mu sync.Mutex
	var calls []bool // recorded DisableIPv6 per call, in order
	mdnsQuery = func(p *mdns.QueryParam) error {
		mu.Lock()
		calls = append(calls, p.DisableIPv6)
		mu.Unlock()
		p.Entries <- &mdns.ServiceEntry{InfoFields: []string{"id=till-noipv6", "name=No IPv6 Till"}, AddrV4: net.IPv4(192, 168, 1, 62), Port: 8080}
		return nil
	}

	got, err := Browse(context.Background(), 3*time.Second)
	if err != nil {
		t.Fatalf("Browse: unexpected error %v", err)
	}
	if len(got) != 1 || got[0].TillID != "till-noipv6" {
		t.Fatalf("got %+v, want the one candidate from the v4-only scan", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 || calls[0] != true {
		t.Fatalf("got mdnsQuery calls (DisableIPv6 per call) = %v, want exactly one call with DisableIPv6=true — "+
			"a host with no IPv6 support must never attempt the v4+v6 query that provokes mdns's own noisy bind failure", calls)
	}
}

// TestBrowse_ReturnsErrorWhenTheV4OnlyAttemptFailsOnAHostWithNoIPv6Support
// covers the fast path's own failure branch, mirroring
// TestBrowse_ReturnsErrorWhenBothTheFullAndV4OnlyRetryFail above for the
// no-IPv6-support case: a host that can't do IPv6 and also can't complete
// even a v4-only scan (e.g. no usable network interface at all) must still
// surface an error, not silently report "no peers found."
func TestBrowse_ReturnsErrorWhenTheV4OnlyAttemptFailsOnAHostWithNoIPv6Support(t *testing.T) {
	forceIPv6Supported(t, false)

	orig := mdnsQuery
	t.Cleanup(func() { mdnsQuery = orig })
	var mu sync.Mutex
	var calls []bool // recorded DisableIPv6 per call, in order
	mdnsQuery = func(p *mdns.QueryParam) error {
		mu.Lock()
		calls = append(calls, p.DisableIPv6)
		mu.Unlock()
		return errors.New("failed to bind to any multicast udp port")
	}

	got, err := Browse(context.Background(), 3*time.Second)
	if err == nil {
		t.Fatal("expected an error when the v4-only attempt fails on a host with no IPv6 support")
	}
	if len(got) != 0 {
		t.Fatalf("got %d candidates on a total failure, want 0", len(got))
	}
	if !strings.Contains(err.Error(), "failed to bind to any multicast udp port") {
		t.Fatalf("error %q does not carry the underlying failure", err)
	}

	mu.Lock()
	defer mu.Unlock()
	// The load-bearing assertion (ut-docs#272 review finding): without it,
	// this test can't distinguish the fast path (one v4-only call) from the
	// pre-fix v4+v6-then-retry path (two calls) — both produce a non-nil
	// error containing this same message, so both would pass otherwise.
	if len(calls) != 1 || calls[0] != true {
		t.Fatalf("got mdnsQuery calls (DisableIPv6 per call) = %v, want exactly one call with DisableIPv6=true — "+
			"a host with no IPv6 support must never attempt the v4+v6 query even on the failure path", calls)
	}
}
