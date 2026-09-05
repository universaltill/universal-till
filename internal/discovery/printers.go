package discovery

import (
	"context"
	"sort"
	"sync"
	"time"
)

// DiscoverPrinters is what the operator's "Find printers on the network"
// button calls. It combines the two sources that each see something the other
// cannot, and offers only devices that can actually print a receipt.
//
// The two sources are complementary, not redundant:
//
//   - mDNS (BrowsePrinters) learns a device's NAME, and its declared page
//     description languages. A sweep can learn neither.
//   - The sweep finds printers that do not advertise AT ALL, which is most
//     cheap network ESC/POS hardware. mDNS never can.
//
// Everything offered must clear the same gate: it has to answer an ESC/POS
// status query. Being findable is not the same as being able to print what
// this till sends — the product owner's LAN offered exactly one discovered
// device, an HP OfficeJet inkjet that advertises itself as a printer, is a
// printer, and would render a receipt as binary garbage, while the real
// thermal printer advertised nothing and was invisible (ut-docs#1606).
//
// # Reading an advertisement before writing a byte
//
// A device that publishes a "pdl=" list naming only non-ESC/POS languages has
// told us it cannot print our receipts. That is enough to exclude it, and
// excluding it that way means it is never written to at all. This matters
// because the ESC/POS probe is indistinguishable from print data to a printer
// that does not implement it: probing the HP directly made it print one
// character per page. So the exclusion set is built FIRST, from advertisements
// alone, and handed to the sweep so those addresses are skipped outright.
//
// A source failing is not fatal to the other: a LAN with no usable multicast
// still gets sweep results, and a machine that cannot enumerate its subnet
// still gets mDNS results. Only both failing is an error.
func DiscoverPrinters(ctx context.Context, timeout time.Duration) ([]PrinterCandidate, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// The mDNS browse gets a third of the budget. A responder either answers
	// within a second or two or is not there; the rest of the time is worth
	// far more to the sweep, which has a whole subnet to get through.
	browseTimeout := timeout / 3

	type browseResult struct {
		candidates []PrinterCandidate
		err        error
	}
	browsedCh := make(chan browseResult, 1)
	go func() {
		c, err := BrowsePrinters(ctx, browseTimeout)
		browsedCh <- browseResult{c, err}
	}()

	// Phase 1 of the sweep runs CONCURRENTLY with the browse, which is only
	// safe because it writes nothing — it just asks who is listening. Waiting
	// for the browse first would stack the two scans end to end and roughly
	// double how long a manager stares at a spinner.
	listeners, sweepErr := sweepListeners(ctx, nil)

	b := <-browsedCh
	if b.err != nil && sweepErr != nil {
		// Nothing worked. Report the browse error: it is the one an operator
		// on a broken-multicast LAN is most likely to be able to act on, and
		// it carries the retry detail scan() built up.
		return nil, b.err
	}

	// Now — and only now — decide who must not be written to, from what the
	// devices said about themselves. Phase 2 writes, so this has to be
	// settled before it runs.
	skip := make(map[string]bool, len(b.candidates))
	for _, c := range b.candidates {
		if nonESCPOSPDL(c.PDL) {
			skip[c.Address] = true
		}
	}

	swept := probeListeners(ctx, listeners, skip)
	return mergePrinterCandidates(ctx, b.candidates, swept, skip), nil
}

// mergePrinterCandidates applies the ESC/POS gate and folds the two sources
// into one list, keyed by address.
//
// mDNS entries are verified rather than trusted: advertising
// _pdl-datastream._tcp is a claim about speaking raw-socket printing, not a
// claim about ESC/POS. But an entry already in skip is rejected on its own
// declaration and never probed — that is what keeps office printers from
// printing a stray page every time a manager taps "Find printers".
//
// Sweep entries arrive already verified — SpeaksESCPOS is how they were
// found — so they are not probed twice.
//
// Where both sources see the same device, mDNS wins on name: a real name
// always beats a bare address in front of an operator.
func mergePrinterCandidates(ctx context.Context, browsed, swept []PrinterCandidate, skip map[string]bool) []PrinterCandidate {
	byAddr := make(map[string]PrinterCandidate, len(browsed)+len(swept))
	order := make([]string, 0, len(browsed)+len(swept))

	add := func(c PrinterCandidate) {
		if existing, ok := byAddr[c.Address]; ok {
			// Keep whichever source actually knows a name.
			if existing.Name == "" && c.Name != "" {
				existing.Name = c.Name
				byAddr[c.Address] = existing
			}
			return
		}
		byAddr[c.Address] = c
		order = append(order, c.Address)
	}

	// Probe the browsed candidates concurrently, bounded the same way the
	// sweep is. Sequentially this was a latency bug waiting to happen: each
	// non-answering device costs a full dial+read timeout, and mDNS results
	// are capped at maxCandidates (64), so a noisy LAN could hold the
	// operator on a spinner for minutes before showing them anything.
	verified := make([]PrinterCandidate, len(browsed))
	var wg sync.WaitGroup
	sem := make(chan struct{}, sweepConcurrency)
	for i, c := range browsed {
		if skip[c.Address] {
			// Declared itself non-ESC/POS. Not offered, and NOT written to.
			continue
		}
		wg.Add(1)
		go func(i int, c PrinterCandidate) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				return
			}
			if SpeaksESCPOS(ctx, c.Address) {
				verified[i] = c
			}
			// Otherwise: advertised as a printer, but cannot print what this
			// till sends. Left as the zero value and skipped below.
		}(i, c)
	}
	wg.Wait()
	for _, c := range verified {
		if c.Address == "" {
			continue
		}
		add(c)
	}
	for _, c := range swept {
		add(c)
	}

	out := make([]PrinterCandidate, 0, len(order))
	for _, a := range order {
		out = append(out, byAddr[a])
	}
	sort.Slice(out, func(i, j int) bool { return lessAddr(out[i].Address, out[j].Address) })
	return out
}
