package discovery

import (
	"context"
	"testing"
	"time"

	"github.com/hashicorp/mdns"
)

func TestCandidateFromEntry_ParsesNameAndID(t *testing.T) {
	e := &mdns.ServiceEntry{
		Name:       "till-abc123._unitill-sync._tcp.local.",
		InfoFields: []string{"v=1", "name=Task Runner", "id=till-abc123"},
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
	}
	if _, ok := candidateFromEntry(e); ok {
		t.Fatal("expected an entry with no id= field to be rejected — a candidate with no till id is useless downstream")
	}
}

func TestCandidateFromEntry_DefaultsNameWhenMissing(t *testing.T) {
	e := &mdns.ServiceEntry{
		Name:       "till-xyz._unitill-sync._tcp.local.",
		InfoFields: []string{"v=1", "id=till-xyz"},
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
