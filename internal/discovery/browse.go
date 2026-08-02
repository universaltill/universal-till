package discovery

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/mdns"
)

// Candidate is one primary found by Browse. JSON tags are for
// internal/pages/discovery_api.go's response — snake_case per this repo's
// API convention.
type Candidate struct {
	Name   string `json:"name"`
	TillID string `json:"till_id"`
}

// maxCandidates caps how many primaries one scan will collect. Discovery
// is a LAN-open surface — any host can answer and no responder is
// authenticated — so a rogue or malfunctioning peer must not be able to
// grow the result slice (and the JSON response built from it) without
// bound. A real shop LAN has a handful of tills; 64 is far above any
// legitimate deployment while still being a hard ceiling.
const maxCandidates = 64

// mdnsQuery is a seam over mdns.Query so tests can drive Browse's full
// lifecycle (including the mid-scan cancellation path) deterministically,
// without needing a real responder answering on real UDP multicast — the
// same package-var seam style as internal/pages's discoveryBrowse.
var mdnsQuery = mdns.Query

// Browse queries the LAN for tills advertising ServiceName and returns
// whatever answers within timeout. Bounded and synchronous — meant to be
// invoked per explicit user action (the Tills page "Find a primary"
// button), never as a background/ambient browser (ADR-0033 part 1 scope;
// the click-to-select flow itself is a separate future card, #185 — this
// only surfaces read-only results).
func Browse(ctx context.Context, timeout time.Duration) ([]Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	entries := make(chan *mdns.ServiceEntry, 32)
	var mu sync.Mutex
	candidates := make([]Candidate, 0)
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for e := range entries {
			c, ok := candidateFromEntry(e)
			if !ok {
				continue
			}
			mu.Lock()
			// Keep draining past the cap (so the query goroutine is never
			// blocked on a full channel and can finish and close it) but
			// stop accumulating — see maxCandidates.
			if len(candidates) < maxCandidates {
				candidates = append(candidates, c)
			}
			mu.Unlock()
		}
	}()

	params := mdns.DefaultParams(ServiceName)
	params.Timeout = timeout
	params.Entries = entries

	queryErr := make(chan error, 1)
	go func() {
		err := mdnsQuery(params)
		// Close entries HERE — in the query goroutine, unconditionally —
		// rather than on the caller's success path only. Two reasons:
		//
		//  1. Correctness of the close itself: mdns.Query's sends to
		//     params.Entries all happen inside its own receive loop, which
		//     has returned by the time Query returns, so nothing can send
		//     on the closed channel afterwards.
		//  2. It is the only way the collector goroutine below is
		//     guaranteed to terminate. Closing on the success path only
		//     leaked it permanently whenever the caller bailed out via
		//     ctx.Done() (manager closes the Tills tab mid-scan): the
		//     collector stayed blocked on `range entries` forever, one
		//     stuck goroutine per abandoned request, for the life of the
		//     process. Covered by
		//     TestBrowse_DoesNotLeakCollectorGoroutineWhenCancelledMidScan.
		close(entries)
		queryErr <- err
	}()

	select {
	case err := <-queryErr:
		<-collected
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		// mdns.Query has no cancellation hook of its own; its query
		// goroutine keeps running until `timeout` elapses on its own and
		// then exits by itself. We deliberately don't block the caller on
		// that — timeout is bounded (~3-4s by design) — and both
		// goroutines now unwind on their own once it returns.
		return nil, ctx.Err()
	}

	mu.Lock()
	defer mu.Unlock()
	return candidates, nil
}

// candidateFromEntry extracts a Candidate from an mDNS answer's TXT
// fields. An entry with no "id=" field is rejected outright — a candidate
// with no till id is useless to a replica trying to independently compute
// the pairing verification code (ADR-0033 §4).
func candidateFromEntry(e *mdns.ServiceEntry) (Candidate, bool) {
	var name, id string
	for _, field := range e.InfoFields {
		k, v, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch k {
		case "name":
			name = v
		case "id":
			id = v
		}
	}
	if id == "" {
		return Candidate{}, false
	}
	if name == "" {
		name = "this shop" // same fallback as storeNameOrDefault, for a malformed/older advertiser
	}
	return Candidate{Name: name, TillID: id}, true
}
