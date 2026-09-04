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

// PrinterServiceName is the standard Bonjour/mDNS service type for raw
// AppSocket/JetDirect network printing — the same "host:port, port 9100 by
// default" shape internal/print.TransportForAddress already speaks in
// "network" mode (see transport.go: no ":" means ":9100" is appended). This
// is deliberately NOT "_ipp._tcp": IPP is an HTTP-based protocol this
// codebase has no transport for, so discovering it would offer a device
// this till can't actually print to (ut-docs#140's scoped-down slice —
// IPP-only printers are a follow-up once/if an IPP transport exists).
const PrinterServiceName = "_pdl-datastream._tcp"

// PrinterCandidate is one network printer found by BrowsePrinters. JSON
// tags match Candidate's convention — snake_case for the discover-printers
// API response.
type PrinterCandidate struct {
	Name string `json:"name"`
	// Address is a bare "host:port" — exactly what internal/print.
	// TransportForAddress's "network" mode and the kitchen-stations
	// printer_address form field already expect, so a discovered candidate
	// can be written straight into that field with no reshaping.
	Address string `json:"address"`
}

// maxCandidates caps how many primaries (or printers) one scan will
// collect. Discovery is a LAN-open surface — any host can answer and no
// responder is authenticated — so a rogue or malfunctioning peer must not
// be able to grow the result slice (and the JSON response built from it)
// without bound. A real shop LAN has a handful of tills or printers; 64 is
// far above any legitimate deployment while still being a hard ceiling.
const maxCandidates = 64

// mdnsQuery is a seam over mdns.Query so tests can drive Browse's full
// lifecycle (including the mid-scan cancellation path) deterministically,
// without needing a real responder answering on real UDP multicast — the
// same package-var seam style as internal/pages's discoveryBrowse.
var mdnsQuery = mdns.Query

// ipv6Supported is a seam over detectIPv6Support so tests can force either
// answer deterministically, the same pattern as mdnsQuery above.
var ipv6Supported = detectIPv6Support

// detectIPv6Support reports whether this host's kernel can open an AF_INET6
// UDP socket at all — the exact condition hashicorp/mdns's own client bind
// fails on ("address family not supported by protocol") on a host without
// usable IPv6: containers, many Pi/kiosk images, IPv6-disabled sandboxes
// (ut-docs#272). mdns's client always tries udp6 first and logs two [ERR]
// lines straight to the global stdlib logger — bypassing internal/logging
// entirely — before Browse's existing v4-only retry below ever gets a
// chance to run. Checking this once, upfront, lets Browse skip straight to
// the v4-only attempt on such a host so that noise is never produced.
func detectIPv6Support() bool {
	conn, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6unspecified, Port: 0})
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

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
//
// On a host that can't open IPv6 sockets at all, Browse skips the v4+v6
// attempt entirely and goes straight to the same v4-only scan the retry
// above would have reached anyway — see ipv6Supported (ut-docs#272).
func Browse(ctx context.Context, timeout time.Duration) ([]Candidate, error) {
	return scan(ctx, timeout, ServiceName, candidateFromEntry)
}

// BrowsePrinters queries the LAN for network printers advertising
// PrinterServiceName (the Bonjour AppSocket/JetDirect service type) and
// returns whatever answers within timeout — the printer-discovery half of
// ut-docs#140, extending the same proven mechanism Browse already uses for
// till-to-till discovery to a second device class. Same bounded,
// synchronous, click-to-scan shape as Browse: never an ambient background
// browser, and results are OFFERED candidates only — nothing here pairs or
// trusts a device, the kitchen-stations form still requires the operator to
// pick one (or type an address by hand) and save.
func BrowsePrinters(ctx context.Context, timeout time.Duration) ([]PrinterCandidate, error) {
	return scan(ctx, timeout, PrinterServiceName, printerCandidateFromEntry)
}

// scan runs Browse/BrowsePrinters' shared retry policy against whichever
// mDNS service type and entry parser the caller supplies — the v4+v6
// attempt, the "already collected something, keep it" partial-failure
// rule, and the v4-only retry (or v4-only-first fast path on a host with no
// IPv6 support) are all identical between device classes; only the wire
// format (service name) and how a candidate is extracted from an answer
// differ. See Browse's doc comment above for why each branch exists — this
// is that same logic, generalized.
func scan[T any](ctx context.Context, timeout time.Duration, serviceName string, parse func(*mdns.ServiceEntry) (T, bool)) ([]T, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if !ipv6Supported() {
		v4Candidates, v4Err := scanOnce(ctx, timeout, serviceName, true, parse)
		if v4Err == nil || len(v4Candidates) > 0 {
			return v4Candidates, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("lan scan failed (v4-only, no IPv6 support: %w)", v4Err)
	}

	candidates, err := scanOnce(ctx, timeout, serviceName, false, parse)
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
		// Cancelled mid-scan (e.g. the manager closed the tab) — this is
		// the caller giving up, not a network failure to retry through.
		return nil, ctxErr
	}

	v4Candidates, v4Err := scanOnce(ctx, timeout, serviceName, true, parse)
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

// scanOnce runs exactly one mdns.Query scan against serviceName and reports
// whatever candidates it collected alongside whatever error mdnsQuery
// returned — both, independently; the caller (scan) decides what a given
// combination means. disableIPv6 lets scan retry with the IPv6 leg of the
// query turned off (see Browse's doc comment).
func scanOnce[T any](ctx context.Context, timeout time.Duration, serviceName string, disableIPv6 bool, parse func(*mdns.ServiceEntry) (T, bool)) ([]T, error) {
	entries := make(chan *mdns.ServiceEntry, 32)
	var mu sync.Mutex
	candidates := make([]T, 0)
	collected := make(chan struct{})
	go func() {
		defer close(collected)
		for e := range entries {
			c, ok := parse(e)
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

	params := mdns.DefaultParams(serviceName)
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
	ip := entryAddr(e)
	if ip == nil || e.Port == 0 {
		return Candidate{}, false
	}
	baseURL := "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(e.Port))
	return Candidate{Name: name, TillID: id, BaseURL: baseURL}, true
}

// printerCandidateFromEntry extracts a PrinterCandidate from an mDNS
// AppSocket/JetDirect answer. Per the Bonjour Printing Specification, a
// compliant printer's TXT record carries a "ty=" key with a friendly
// description ("HP LaserJet 4100 series") — preferred as Name when present.
// Falling that, the mDNS instance name (e.g.
// "HP LaserJet 4100._pdl-datastream._tcp.local.") is trimmed to just the
// instance portion. A printer answering with neither still gets a usable
// candidate — unlike till discovery, there is no id to key on, so name is
// cosmetic, not load-bearing — but Name is left empty rather than defaulted
// to an English literal here: this is Go, not a template, so it has no way
// to route a fallback through web/locales/*.json (guard-i18n.sh's Go-side
// check doesn't reach this value either, since it never touches an HTTP
// response body directly, just a JSON field an operator later reads). The
// UI supplies a localized generic label when Name is empty (see
// kitchen_stations.html's discover-printers script). An entry with no
// usable address or port is rejected — nothing to print to.
func printerCandidateFromEntry(e *mdns.ServiceEntry) (PrinterCandidate, bool) {
	var name string
	for _, field := range e.InfoFields {
		k, v, ok := strings.Cut(field, "=")
		if ok && k == "ty" && v != "" {
			name = v
			break
		}
	}
	if name == "" {
		if instance, _, ok := strings.Cut(e.Name, "."+PrinterServiceName); ok {
			name = instance
		}
	}
	ip := entryAddr(e)
	if ip == nil || e.Port == 0 {
		return PrinterCandidate{}, false
	}
	return PrinterCandidate{Name: name, Address: net.JoinHostPort(ip.String(), strconv.Itoa(e.Port))}, true
}

// entryAddr picks AddrV4, falling back to AddrV6 — shared by both entry
// parsers above (Browse's IPv4-preferred, IPv6-fallback address choice was
// previously inlined in candidateFromEntry only).
func entryAddr(e *mdns.ServiceEntry) net.IP {
	if e.AddrV4 != nil {
		return e.AddrV4
	}
	return e.AddrV6
}
