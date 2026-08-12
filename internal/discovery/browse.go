package discovery

import (
	"context"
	"fmt"
	"net"
	"strconv"
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
	// BaseURL is where a replica sends POST /api/sync/pair-request
	// (universaltill/ut-docs#185) — http, LAN-only, same no-TLS posture as
	// the existing QR flow's own "http://"+r.Host default in sync_api.go's
	// enrol-token handler; this isn't a new assumption, just matching it.
	BaseURL string `json:"base_url"`
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
//
// A partial failure is not reported as a total one (ut-docs#538): a real
// LAN with no usable IPv6 multicast route ("write udp6 …: sendto: no
// route to host") used to make Browse discard every IPv4 peer it had
// already found, because hashicorp/mdns's own client.query() sends the v4
// leg then the v6 leg of one probe packet from a single sendQuery call and
// returns the v6 leg's error immediately — before it ever listens for a
// response (see the vendored client.go). So a v4+v6 attempt that fails
// this way has collected nothing to fall back on by itself; Browse
// recovers by retrying once with IPv6 disabled, which is exactly the
// scan the operator's own `dns-sd`/`avahi-browse` diagnosis proved works.
func Browse(ctx context.Context, timeout time.Duration) ([]Candidate, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	candidates, err := browseOnce(ctx, timeout, false)
	if err == nil {
		return candidates, nil
	}
	if len(candidates) > 0 {
		// The query errored, but real responses had already been collected
		// before it did — don't throw away peers a shop operator can
		// actually use over a transport-level hiccup on the other leg.
		return candidates, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		// Cancelled mid-scan (e.g. the manager closed the Tills tab) — this
		// is the caller giving up, not a network failure to retry through.
		return nil, ctxErr
	}

	v4Candidates, v4Err := browseOnce(ctx, timeout, true)
	if v4Err == nil || len(v4Candidates) > 0 {
		return v4Candidates, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		// Same reasoning as the pre-retry check above, for the retry's own
		// window: cancelling during the second attempt is still the caller
		// giving up, and must stay reported as context.Canceled /
		// DeadlineExceeded rather than being reshaped into a "lan scan
		// failed" network error the handler would log and 500 on.
		return nil, ctxErr
	}
	return nil, fmt.Errorf("lan scan failed (v4+v6: %w; v4-only retry: %w)", err, v4Err)
}

// browseOnce runs exactly one mdns.Query scan and reports whatever
// candidates it collected alongside whatever error mdnsQuery returned —
// both, independently; the caller (Browse) decides what a given
// combination means. disableIPv6 lets Browse retry with the IPv6 leg of
// the query turned off (see Browse's doc comment).
func browseOnce(ctx context.Context, timeout time.Duration, disableIPv6 bool) ([]Candidate, error) {
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
	params.DisableIPv6 = disableIPv6

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
		mu.Lock()
		defer mu.Unlock()
		return candidates, err
	case <-ctx.Done():
		// mdns.Query has no cancellation hook of its own; its query
		// goroutine keeps running until `timeout` elapses on its own and
		// then exits by itself. We deliberately don't block the caller on
		// that — timeout is bounded (~3-4s by design) — and both
		// goroutines now unwind on their own once it returns.
		return nil, ctx.Err()
	}
}

// candidateFromEntry extracts a Candidate from an mDNS answer's TXT
// fields. An entry with no "id=" field is rejected outright — a candidate
// with no till id is useless to a replica trying to independently compute
// the pairing verification code (ADR-0033 §4). Likewise an entry with no
// usable address is rejected — equally useless, since a replica has nothing
// to send POST /api/sync/pair-request to (ut-docs#185).
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
	ip := e.AddrV4
	if ip == nil {
		ip = e.AddrV6
	}
	if ip == nil || e.Port == 0 {
		return Candidate{}, false
	}
	baseURL := "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(e.Port))
	return Candidate{Name: name, TillID: id, BaseURL: baseURL}, true
}
