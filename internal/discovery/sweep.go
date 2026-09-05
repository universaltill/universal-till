package discovery

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
)

// RawPrintPort is the AppSocket/JetDirect port every network ESC/POS printer
// listens on, and the only port the sweep ever touches.
const RawPrintPort = 9100

// sweepConcurrency bounds how many hosts are dialled at once. A /24 is 254
// addresses; at 64 in flight with a short per-host timeout the whole sweep
// finishes in a couple of seconds, without opening a burst of sockets a
// small till (or a cheap consumer router's NAT table) would struggle with.
const sweepConcurrency = 64

// localSubnetHosts is a seam over interface enumeration so the sweep can be
// driven deterministically in tests.
var localSubnetHosts = realLocalSubnetHosts

// SweepPrinters finds ESC/POS receipt printers by looking for :9100 across
// the till's own subnet, rather than waiting for them to announce themselves.
//
// This exists because mDNS discovery structurally cannot find most receipt
// printers. Cheap network ESC/POS units — the Epson TM-T clones, Xprinter and
// generic POS58/80 boxes a small shop actually buys — ship no Bonjour
// responder at all; they just listen on 9100. The product owner's thermal
// printer answered none of _pdl-datastream._tcp, _printer._tcp, _ipp._tcp or
// _http._tcp, while the one device that DID advertise was an inkjet that
// cannot print a receipt (ut-docs#1606). Browsing alone therefore offers the
// wrong printer and hides the right one.
//
// # It runs in two phases, and the order matters
//
//  1. Connect to :9100 and close immediately. This writes NOTHING. Every
//     printer discards an empty job, so a device that turns out not to be a
//     receipt printer is never made to put anything on paper by being found.
//  2. Only then, and only for hosts not already excluded by `skip`, send the
//     ESC/POS status query.
//
// The split is not tidiness. The first version probed every :9100 listener
// directly, and the product owner's HP OfficeJet printed one character per
// page for each probe it received — an office printer treats those three
// bytes as a print job, because to it that is exactly what they are. Any
// device the caller can identify by other means (its own mDNS advertisement
// naming PCL, say) must be in `skip` so it is never written to.
//
// Scope is deliberately narrow, per this ecosystem's security-first rule:
// the till's own IPv4 /24 and no wider, port 9100 and no other.
func SweepPrinters(ctx context.Context, skip map[string]bool) ([]PrinterCandidate, error) {
	listeners, err := sweepListeners(ctx, skip)
	if err != nil {
		return nil, err
	}
	return probeListeners(ctx, listeners, skip, nil), nil
}

// sweepListeners is phase 1: who is accepting connections on :9100?
//
// It writes NOTHING. Every printer discards an empty job, so a device that
// turns out not to be a receipt printer is never made to put anything on
// paper merely by being found. That property is what lets DiscoverPrinters
// start this phase before it knows which devices to exclude — the exclusion
// list only has to exist before phase 2, which is the part that writes.
//
// A host whose ARP entry is cold gets one retry, after the rest of the
// subnet has been dialled once (ut-docs#1608). escposDialTimeout budgets
// 700ms per host, and for a host with no ARP entry that has to cover ARP
// resolution as well as the TCP handshake — the sweep's own 253 simultaneous
// dials flood the ARP queue, so the one host that matters can lose that race
// even though it is genuinely online. This is worst on a till's first boot,
// when every host on the LAN is cold. By the time the first pass has
// finished, every host it dialled (including the miss) has an ARP entry, so
// a retry no longer races the whole subnet for THAT host.
//
// This is not a cheap, narrow retry in the common case, and callers of
// SweepPrinters must budget for that (see discoverPrintersTimeout in
// internal/pages): on a typical shop LAN almost every one of the ~253
// addresses has no device at all, and an address with no host behind it
// times out the same way whether or not its ARP entry was ever going to
// resolve — so `missed` is usually close to the whole subnet, and this is
// a second near-full sweep, not a handful of retries.
func sweepListeners(ctx context.Context, skip map[string]bool) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	hosts, err := localSubnetHosts()
	if err != nil {
		return nil, fmt.Errorf("enumerate local subnet: %w", err)
	}

	listeners := make([]string, 0, 8)
	var missed []string
	var mu sync.Mutex
	forEachBounded(ctx, hosts, func(h string) {
		addr := net.JoinHostPort(h, strconv.Itoa(RawPrintPort))
		if skip[addr] {
			return
		}
		if !Listens(ctx, addr, escposDialTimeout) {
			mu.Lock()
			missed = append(missed, addr)
			mu.Unlock()
			return
		}
		mu.Lock()
		listeners = append(listeners, addr)
		mu.Unlock()
	})

	// ctx.Err() is checked again here (as well as inside forEachBounded, which
	// governs the dials themselves) so a subnet-full of misses against an
	// already-expired context doesn't spin up one abandoned goroutine per
	// miss for no reason.
	if len(missed) > 0 && ctx.Err() == nil {
		forEachBounded(ctx, missed, func(addr string) {
			// missed is built entirely from addresses that already passed
			// the skip check above, so this is defensive, not load-bearing —
			// but skip is a security-scoped exclusion list (an excluded
			// device must never be dialled at all, not even read-only), so
			// re-checking costs nothing and keeps that guarantee explicit
			// here too, not just implied by where missed is populated.
			if skip[addr] {
				return
			}
			if !Listens(ctx, addr, escposDialTimeout) {
				return
			}
			mu.Lock()
			listeners = append(listeners, addr)
			mu.Unlock()
		})
	}
	return listeners, nil
}

// probeListeners is phase 2: of the hosts that are listening, which actually
// speak ESC/POS?
//
// This is the part that writes, so it runs only against the short list phase
// 1 produced — never against a whole subnet — and never against anything in
// skip. Excluding by advertisement before reaching here is what stops an
// office printer emitting a page for every probe it receives (ut-docs#1606).
//
// skip carries what the mDNS browse ruled out; trusted carries what it ruled
// IN — an address whose own advertisement names a raw/ESC/POS format, which
// therefore bypasses the IPP guard below. Both are empty when the browse
// failed, which is the case ut-docs#1607 is about: with no advertisements at
// all, officePrinterOverIPP is the only thing standing between an office
// printer and a stray page.
func probeListeners(ctx context.Context, listeners []string, skip, trusted map[string]bool) []PrinterCandidate {
	found := make([]PrinterCandidate, 0, len(listeners))
	var mu sync.Mutex
	forEachBounded(ctx, listeners, func(addr string) {
		if skip[addr] {
			return
		}
		if !trusted[addr] && isOfficePrinter(ctx, addr) {
			return
		}
		if !SpeaksESCPOS(ctx, addr) {
			return
		}
		mu.Lock()
		// Same ceiling as the mDNS scan — a sweep of one /24 cannot
		// legitimately produce more printers than maxCandidates.
		if len(found) < maxCandidates {
			// Name is left empty: a sweep learns an address, not an
			// identity. The UI supplies its localized generic label, and a
			// matching mDNS answer supplies a real name — see
			// mergePrinterCandidates.
			found = append(found, PrinterCandidate{Address: addr})
		}
		mu.Unlock()
	})

	// Deterministic order: goroutines finish in whatever order the network
	// allows, and an operator rescanning should not see the list reshuffle.
	sort.Slice(found, func(i, j int) bool { return lessAddr(found[i].Address, found[j].Address) })
	return found
}

// forEachBounded runs fn over items with at most sweepConcurrency in flight,
// abandoning what is left if ctx is cancelled.
func forEachBounded(ctx context.Context, items []string, fn func(string)) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, sweepConcurrency)
	for _, it := range items {
		wg.Add(1)
		go func(it string) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			if ctx.Err() != nil {
				return
			}
			fn(it)
		}(it)
	}
	wg.Wait()
}

// lessAddr orders "host:port" numerically by IPv4 octet rather than by
// string, so .9 sorts before .111 the way an operator reading a list of
// their own devices expects.
func lessAddr(a, b string) bool {
	ah, _, _ := net.SplitHostPort(a)
	bh, _, _ := net.SplitHostPort(b)
	aip, bip := net.ParseIP(ah), net.ParseIP(bh)
	if aip == nil || bip == nil {
		return a < b
	}
	a4, b4 := aip.To4(), bip.To4()
	if a4 == nil || b4 == nil {
		return a < b
	}
	for i := 0; i < 4; i++ {
		if a4[i] != b4[i] {
			return a4[i] < b4[i]
		}
	}
	return false
}

// realLocalSubnetHosts lists every other host address on the till's own IPv4
// subnet. Only /24 or smaller is swept: a /16 is 65k addresses, which is not
// a scan a till should be starting on a network it does not own, and no shop
// LAN needs it.
func realLocalSubnetHosts() ([]string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	var hosts []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() == nil {
				continue
			}
			ones, bits := ipnet.Mask.Size()
			if bits != 32 || ones < 24 || ones > 30 {
				continue
			}
			self := ipnet.IP.To4()
			for _, h := range hostsIn(ipnet) {
				if h.Equal(self) {
					continue // no point dialling ourselves
				}
				s := h.String()
				if _, dup := seen[s]; dup {
					continue
				}
				seen[s] = struct{}{}
				hosts = append(hosts, s)
			}
		}
	}
	return hosts, nil
}

// hostsIn enumerates the usable host addresses of an IPv4 network, excluding
// the network and broadcast addresses.
func hostsIn(n *net.IPNet) []net.IP {
	ip := n.IP.To4()
	mask := net.IP(n.Mask).To4()
	if ip == nil || mask == nil {
		return nil
	}
	base := make(net.IP, 4)
	for i := range base {
		base[i] = ip[i] & mask[i]
	}
	toU32 := func(p net.IP) uint32 {
		return uint32(p[0])<<24 | uint32(p[1])<<16 | uint32(p[2])<<8 | uint32(p[3])
	}
	start := toU32(base)
	size := ^toU32(mask)
	if size < 2 {
		return nil
	}
	out := make([]net.IP, 0, size-1)
	for i := uint32(1); i < size; i++ { // skip network (.0) and broadcast (.255)
		v := start + i
		out = append(out, net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)))
	}
	return out
}
