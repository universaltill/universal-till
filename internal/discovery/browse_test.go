package discovery

import (
	"context"
	"net"
	"runtime"
	"strconv"
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

// TestBrowse_RespectsAlreadyCancelledContext exercises Browse's ctx-aware
// exit path without needing a real mDNS responder on the network: an
// already-cancelled ctx must return immediately with ctx.Err(), not block
// for the full timeout.
func TestBrowse_RespectsAlreadyCancelledContext(t *testing.T) {
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
