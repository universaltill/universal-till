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
			candidates = append(candidates, c)
			mu.Unlock()
		}
	}()

	params := mdns.DefaultParams(ServiceName)
	params.Timeout = timeout
	params.Entries = entries

	queryErr := make(chan error, 1)
	go func() { queryErr <- mdns.Query(params) }()

	select {
	case err := <-queryErr:
		close(entries)
		<-collected
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		// mdns.Query has no cancellation hook of its own; its query
		// goroutine keeps running until `timeout` elapses on its own and
		// then exits by itself (harmless — it only writes to a channel
		// nothing reads from after this point). Deliberately not blocking
		// the caller on that: timeout is bounded (~3-4s per the design),
		// so the leaked goroutine's lifetime is bounded too.
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
