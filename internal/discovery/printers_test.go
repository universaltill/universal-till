package discovery

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/mdns"
)

// hpRealPDL is the verbatim pdl= TXT value published by the product owner's
// HP OfficeJet Pro 9025 (it advertises under the "9020 series" name), read
// off the wire with dns-sd on 2026-09-05. Five languages, none of them
// ESC/POS — which is exactly why it must never be written to.
const hpRealPDL = "application/vnd.hp-PCL,image/jpeg,image/urf,image/pwg-raster,application/PCLm"

// fakeConn is a net.Conn whose reads return a scripted reply, recording what
// was written so a test can assert that nothing was.
type fakeConn struct {
	mu      sync.Mutex
	written []byte
	reply   []byte
	delay   time.Duration
	closed  bool
}

func (c *fakeConn) Read(b []byte) (int, error) {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reply) == 0 {
		return 0, errors.New("no reply")
	}
	n := copy(b, c.reply)
	c.reply = c.reply[n:]
	return n, nil
}

func (c *fakeConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.written = append(c.written, b...)
	return len(b), nil
}

func (c *fakeConn) bytesWritten() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.written...)
}

func (c *fakeConn) Close() error                       { c.mu.Lock(); c.closed = true; c.mu.Unlock(); return nil }
func (c *fakeConn) LocalAddr() net.Addr                { return &net.TCPAddr{} }
func (c *fakeConn) RemoteAddr() net.Addr               { return &net.TCPAddr{} }
func (c *fakeConn) SetDeadline(t time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(t time.Time) error { return nil }

// device describes what a stubbed host does when dialled.
type device struct {
	reply []byte        // what it answers to a read; empty = silence
	delay time.Duration // how long it takes to answer
}

// dialRecorder captures every dial and every byte written, per address.
// Mutex-guarded because SweepPrinters and mergePrinterCandidates both work
// concurrently by design — an unsynchronised map here is a
// concurrent-map-write panic, which is how it first surfaced: intermittently,
// and only under load.
type dialRecorder struct {
	mu      sync.Mutex
	dialled []string
	conns   map[string][]*fakeConn
}

func (d *dialRecorder) writtenTo(addr string) []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []byte
	for _, c := range d.conns[addr] {
		out = append(out, c.bytesWritten()...)
	}
	return out
}

func (d *dialRecorder) dialledAddrs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.dialled...)
}

func stubDial(t *testing.T, devices map[string]device) *dialRecorder {
	t.Helper()
	orig := dialProbe
	t.Cleanup(func() { dialProbe = orig })
	rec := &dialRecorder{conns: map[string][]*fakeConn{}}
	dialProbe = func(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error) {
		dev, ok := devices[addr]
		rec.mu.Lock()
		rec.dialled = append(rec.dialled, addr)
		rec.mu.Unlock()
		if !ok {
			return nil, errors.New("connection refused")
		}
		c := &fakeConn{reply: append([]byte(nil), dev.reply...), delay: dev.delay}
		rec.mu.Lock()
		rec.conns[addr] = append(rec.conns[addr], c)
		rec.mu.Unlock()
		return c, nil
	}
	return rec
}

func stubSubnet(t *testing.T, hosts []string, err error) {
	t.Helper()
	orig := localSubnetHosts
	t.Cleanup(func() { localSubnetHosts = orig })
	localSubnetHosts = func() ([]string, error) { return hosts, err }
}

func stubMDNS(t *testing.T, entries ...*mdns.ServiceEntry) {
	t.Helper()
	forceIPv6Supported(t, true)
	orig := mdnsQuery
	t.Cleanup(func() { mdnsQuery = orig })
	mdnsQuery = func(p *mdns.QueryParam) error {
		for _, e := range entries {
			p.Entries <- e
		}
		return nil
	}
}

// hpEntry is the HP as it really advertises itself.
func hpEntry() *mdns.ServiceEntry {
	return &mdns.ServiceEntry{
		Name:       "HP OfficeJet Pro 9020 series [79A4B2]._pdl-datastream._tcp.local.",
		InfoFields: []string{"txtvers=1", "ty=HP OfficeJet Pro 9020 series", "pdl=" + hpRealPDL},
		AddrV4:     net.IPv4(192, 168, 1, 245),
		Port:       9100,
	}
}

// ---------------------------------------------------------------------------
// The safety property: an office printer must never be written to.
// ---------------------------------------------------------------------------

// TestDiscoverPrinters_NeverWritesToADeviceThatDeclaresAnotherLanguage is the
// most important test in this file.
//
// The ESC/POS status query is indistinguishable from print data to a printer
// that does not implement it. During development, probing the product owner's
// HP OfficeJet Pro 9025 on :9100 made it print ONE CHARACTER PER PAGE — real
// paper and ink, in a real shop, every time a manager taps "Find printers".
//
// The HP tells us what it speaks in its own mDNS advertisement. Reading that
// costs nothing; writing to find out costs paper. So a device whose pdl= names
// only non-ESC/POS languages must be excluded on the strength of that
// declaration alone, before a single byte is sent to it.
func TestDiscoverPrinters_NeverWritesToADeviceThatDeclaresAnotherLanguage(t *testing.T) {
	stubMDNS(t, hpEntry())
	stubSubnet(t, []string{"192.168.1.111", "192.168.1.245"}, nil)
	rec := stubDial(t, map[string]device{
		"192.168.1.111:9100": {reply: []byte{0x16}},
		"192.168.1.245:9100": {reply: []byte{0x16}}, // would answer — must never be asked
	})

	got, err := DiscoverPrinters(context.Background(), 3*time.Second)
	if err != nil {
		t.Fatalf("DiscoverPrinters: %v", err)
	}

	if w := rec.writtenTo("192.168.1.245:9100"); len(w) != 0 {
		t.Fatalf("wrote % x to the HP OfficeJet — it declares pdl=%s and must never be written to; "+
			"this is what made it print one character per page (ut-docs#1606)", w, hpRealPDL)
	}
	for _, c := range got {
		if strings.HasPrefix(c.Address, "192.168.1.245:") {
			t.Fatalf("offered the HP at %s as a printer — it cannot print an ESC/POS receipt", c.Address)
		}
	}
}

// TestSweepPrinters_DoesNotWriteToSkippedAddresses — the exclusion must reach
// the sweep too, not just the mDNS branch. The HP listens on :9100, so a
// sweep that ignored skip would find and probe it independently.
func TestSweepPrinters_DoesNotWriteToSkippedAddresses(t *testing.T) {
	stubSubnet(t, []string{"192.168.1.111", "192.168.1.245"}, nil)
	rec := stubDial(t, map[string]device{
		"192.168.1.111:9100": {reply: []byte{0x16}},
		"192.168.1.245:9100": {reply: []byte{0x16}},
	})

	got, err := SweepPrinters(context.Background(), map[string]bool{"192.168.1.245:9100": true})
	if err != nil {
		t.Fatalf("SweepPrinters: %v", err)
	}
	if w := rec.writtenTo("192.168.1.245:9100"); len(w) != 0 {
		t.Fatalf("sweep wrote % x to a skipped address", w)
	}
	for _, a := range rec.dialledAddrs() {
		if a == "192.168.1.245:9100" {
			t.Fatal("sweep dialled a skipped address — it must not be touched at all")
		}
	}
	if len(got) != 1 || got[0].Address != "192.168.1.111:9100" {
		t.Fatalf("got %+v, want only the thermal printer", got)
	}
}

// TestListens_WritesNothing pins phase 1's contract: finding out whether a
// device is there must never put anything on its paper.
func TestListens_WritesNothing(t *testing.T) {
	rec := stubDial(t, map[string]device{"192.168.1.245:9100": {reply: []byte{0x16}}})
	if !Listens(context.Background(), "192.168.1.245:9100", time.Second) {
		t.Fatal("expected the host to be reported as listening")
	}
	if w := rec.writtenTo("192.168.1.245:9100"); len(w) != 0 {
		t.Fatalf("Listens wrote % x — phase 1 must be connect-and-close only", w)
	}
}

// TestNonESCPOSPDL uses the HP's real advertisement and the shapes a receipt
// printer actually publishes.
func TestNonESCPOSPDL(t *testing.T) {
	for _, tc := range []struct {
		pdl  string
		want bool
		why  string
	}{
		{hpRealPDL, true, "the HP's real pdl: five languages, no ESC/POS"},
		{"application/vnd.hp-PCL", true, "PCL only"},
		{"", false, "no declaration at all — most thermal printers; must NOT be excluded"},
		{"   ", false, "blank is not a declaration"},
		{"application/octet-stream", false, "raw bytes: what an ESC/POS device advertises"},
		{"application/vnd.hp-PCL,application/octet-stream", false, "declares raw bytes among others"},
		{"application/escpos", false, "names ESC/POS explicitly"},
		{"APPLICATION/OCTET-STREAM", false, "case-insensitive"},
	} {
		if got := nonESCPOSPDL(tc.pdl); got != tc.want {
			t.Errorf("nonESCPOSPDL(%q) = %v, want %v — %s", tc.pdl, got, tc.want, tc.why)
		}
	}
}

// ---------------------------------------------------------------------------
// The ESC/POS probe itself.
// ---------------------------------------------------------------------------

// TestSpeaksESCPOS_AcceptsRealPrinterStatusByte uses the byte the product
// owner's actual thermal printer returned: DLE EOT 1 -> 0x16.
func TestSpeaksESCPOS_AcceptsRealPrinterStatusByte(t *testing.T) {
	rec := stubDial(t, map[string]device{"192.168.1.111:9100": {reply: []byte{0x16}}})
	if !SpeaksESCPOS(context.Background(), "192.168.1.111:9100") {
		t.Fatal("0x16 is the status byte the real thermal printer returned; it must be accepted")
	}
	if got := rec.writtenTo("192.168.1.111:9100"); string(got) != string(escposStatusQuery) {
		t.Fatalf("wrote % x, want only the DLE EOT 1 status query % x — "+
			"the probe must never send anything that could print", got, escposStatusQuery)
	}
}

// TestSpeaksESCPOS_WaitsLongerThanTheDialBudget is a regression test for a
// bug real hardware caught and stubs could not.
//
// The first version shared one 700ms timeout between connect and read. The
// product owner's thermal printer completes a TCP handshake in milliseconds
// but takes over a second to answer DLE EOT — so the sweep dialled it, gave
// up on the read, and reported no printers at all. It missed the very device
// it was written to find, and every unit test passed, because a stubbed
// connection answers instantly.
func TestSpeaksESCPOS_WaitsLongerThanTheDialBudget(t *testing.T) {
	if escposReadTimeout <= escposDialTimeout {
		t.Fatalf("escposReadTimeout (%v) must exceed escposDialTimeout (%v): cheap embedded "+
			"printers connect fast and answer slowly", escposReadTimeout, escposDialTimeout)
	}
	stubDial(t, map[string]device{
		"192.168.1.111:9100": {reply: []byte{0x16}, delay: escposDialTimeout + 300*time.Millisecond},
	})
	if !SpeaksESCPOS(context.Background(), "192.168.1.111:9100") {
		t.Fatal("a printer slower than the dial budget must still be found")
	}
}

// TestValidESCPOSStatus_ChecksOnlyTheSpecFixedBits — b0=0, b1=1, b4=1, b7=0
// are fixed regardless of printer state; the rest carry drawer/offline/paper
// status and must NOT disqualify a printer, or a device merely out of paper
// would vanish from the operator's list.
func TestValidESCPOSStatus_ChecksOnlyTheSpecFixedBits(t *testing.T) {
	for _, tc := range []struct {
		b    byte
		want bool
		why  string
	}{
		{0x16, true, "the real printer's reply"},
		{0x12, true, "fixed bits only, all state clear"},
		{0x1e, true, "offline and paper-out still IS an ESC/POS printer"},
		{0x00, false, "b1/b4 clear"},
		{0xff, false, "b0/b7 set"},
		{0x13, false, "b0 set"},
		{0x92, false, "b7 set"},
	} {
		if got := validESCPOSStatus(tc.b); got != tc.want {
			t.Errorf("validESCPOSStatus(0x%02x) = %v, want %v — %s", tc.b, got, tc.want, tc.why)
		}
	}
}

// TestSpeaksESCPOS_RejectsSilentAndRefusedDevices — the common rejection is
// silence, not an error.
func TestSpeaksESCPOS_RejectsSilentAndRefusedDevices(t *testing.T) {
	stubDial(t, map[string]device{"192.168.1.200:9100": {}})
	if SpeaksESCPOS(context.Background(), "192.168.1.200:9100") {
		t.Error("a device that accepts the connection but never replies is not an ESC/POS printer")
	}
	if SpeaksESCPOS(context.Background(), "192.168.1.9:9100") {
		t.Error("a refused connection is not an ESC/POS printer")
	}
}

// ---------------------------------------------------------------------------
// The sweep.
// ---------------------------------------------------------------------------

// TestSweepPrinters_FindsPrinterThatAdvertisesNothing is the core of
// ut-docs#1606. The owner's thermal printer answers no mDNS service type at
// all — it only listens on :9100 — so browsing can never see it.
func TestSweepPrinters_FindsPrinterThatAdvertisesNothing(t *testing.T) {
	stubSubnet(t, []string{"192.168.1.110", "192.168.1.111", "192.168.1.112"}, nil)
	stubDial(t, map[string]device{"192.168.1.111:9100": {reply: []byte{0x16}}})

	got, err := SweepPrinters(context.Background(), nil)
	if err != nil {
		t.Fatalf("SweepPrinters: %v", err)
	}
	if len(got) != 1 || got[0].Address != "192.168.1.111:9100" {
		t.Fatalf("got %+v, want the non-advertising thermal printer at 192.168.1.111:9100", got)
	}
}

// TestSweepPrinters_SkipsListenersThatAreNotESCPOSPrinters — plenty of things
// listen on :9100. Reachability is not capability.
func TestSweepPrinters_SkipsListenersThatAreNotESCPOSPrinters(t *testing.T) {
	stubSubnet(t, []string{"192.168.1.111", "192.168.1.200"}, nil)
	stubDial(t, map[string]device{
		"192.168.1.111:9100": {reply: []byte{0x16}},
		"192.168.1.200:9100": {}, // listening, silent to DLE EOT
	})

	got, err := SweepPrinters(context.Background(), nil)
	if err != nil {
		t.Fatalf("SweepPrinters: %v", err)
	}
	if len(got) != 1 || got[0].Address != "192.168.1.111:9100" {
		t.Fatalf("got %+v, want only the ESC/POS printer", got)
	}
}

// TestSweepPrinters_ReturnsAddressOrderedNumerically — an operator rescanning
// must not see their own devices reshuffle, and .9 belongs before .111.
func TestSweepPrinters_ReturnsAddressOrderedNumerically(t *testing.T) {
	stubSubnet(t, []string{"192.168.1.111", "192.168.1.9", "192.168.1.50"}, nil)
	stubDial(t, map[string]device{
		"192.168.1.111:9100": {reply: []byte{0x16}},
		"192.168.1.9:9100":   {reply: []byte{0x16}},
		"192.168.1.50:9100":  {reply: []byte{0x16}},
	})

	got, err := SweepPrinters(context.Background(), nil)
	if err != nil {
		t.Fatalf("SweepPrinters: %v", err)
	}
	var addrs []string
	for _, c := range got {
		addrs = append(addrs, c.Address)
	}
	want := []string{"192.168.1.9:9100", "192.168.1.50:9100", "192.168.1.111:9100"}
	if strings.Join(addrs, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v — numeric octet order, not string order", addrs, want)
	}
}

// TestSweepPrinters_OnlyTouchesPort9100 pins the security scope.
func TestSweepPrinters_OnlyTouchesPort9100(t *testing.T) {
	stubSubnet(t, []string{"192.168.1.10", "192.168.1.11"}, nil)
	rec := stubDial(t, map[string]device{})

	if _, err := SweepPrinters(context.Background(), nil); err != nil {
		t.Fatalf("SweepPrinters: %v", err)
	}
	dialled := rec.dialledAddrs()
	for _, a := range dialled {
		if _, port, _ := net.SplitHostPort(a); port != "9100" {
			t.Fatalf("sweep dialled %q — it must only ever touch the raw-print port 9100", a)
		}
	}
	if len(dialled) != 2 {
		t.Fatalf("dialled %d addresses, want exactly the 2 subnet hosts", len(dialled))
	}
}

// TestHostsIn_ExcludesNetworkAndBroadcast — .0 and .255 are not hosts.
func TestHostsIn_ExcludesNetworkAndBroadcast(t *testing.T) {
	_, n, err := net.ParseCIDR("192.168.1.0/24")
	if err != nil {
		t.Fatal(err)
	}
	hosts := hostsIn(n)
	if len(hosts) != 254 {
		t.Fatalf("got %d hosts in a /24, want 254", len(hosts))
	}
	for _, h := range hosts {
		if h.String() == "192.168.1.0" || h.String() == "192.168.1.255" {
			t.Fatalf("hostsIn included %s — network and broadcast are not hosts", h)
		}
	}
}

// ---------------------------------------------------------------------------
// The combined result.
// ---------------------------------------------------------------------------

// TestDiscoverPrinters_OffersTheThermalPrinterAndNotTheInkjet reproduces the
// product owner's LAN exactly and asserts the outcome they asked for: find
// .111, not the HP at .245. Before this change the list was precisely
// inverted — the HP was the only result, because it advertises and the
// thermal printer does not.
func TestDiscoverPrinters_OffersTheThermalPrinterAndNotTheInkjet(t *testing.T) {
	stubMDNS(t, hpEntry())
	stubSubnet(t, []string{"192.168.1.111", "192.168.1.245"}, nil)
	stubDial(t, map[string]device{
		"192.168.1.111:9100": {reply: []byte{0x16}},
		"192.168.1.245:9100": {reply: []byte{0x16}},
	})

	got, err := DiscoverPrinters(context.Background(), 3*time.Second)
	if err != nil {
		t.Fatalf("DiscoverPrinters: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v, want exactly the thermal printer", got)
	}
	if got[0].Address != "192.168.1.111:9100" {
		t.Fatalf("got %q, want the thermal printer at 192.168.1.111:9100", got[0].Address)
	}
}

// TestDiscoverPrinters_KeepsTheMDNSNameForASweptPrinter — the two sources are
// complementary. A sweep learns an address; only mDNS learns a name, and a
// name is what an operator actually picks between.
func TestDiscoverPrinters_KeepsTheMDNSNameForASweptPrinter(t *testing.T) {
	stubMDNS(t, &mdns.ServiceEntry{
		Name:       "Kitchen._pdl-datastream._tcp.local.",
		InfoFields: []string{"txtvers=1", "ty=Kitchen Printer"},
		AddrV4:     net.IPv4(192, 168, 1, 111),
		Port:       9100,
	})
	stubSubnet(t, []string{"192.168.1.111"}, nil)
	stubDial(t, map[string]device{"192.168.1.111:9100": {reply: []byte{0x16, 0x16}}})

	got, err := DiscoverPrinters(context.Background(), 3*time.Second)
	if err != nil {
		t.Fatalf("DiscoverPrinters: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %+v, want one candidate — the same device seen by both sources must not be listed twice", got)
	}
	if got[0].Name != "Kitchen Printer" {
		t.Fatalf("got Name=%q, want the mDNS name — a bare address is a poor thing to ask a manager to choose", got[0].Name)
	}
}

// TestDiscoverPrinters_SurvivesOneSourceFailing — a LAN with broken multicast
// must still get sweep results. Only both failing is fatal.
func TestDiscoverPrinters_SurvivesOneSourceFailing(t *testing.T) {
	forceIPv6Supported(t, true)
	orig := mdnsQuery
	t.Cleanup(func() { mdnsQuery = orig })
	mdnsQuery = func(p *mdns.QueryParam) error { return errors.New("no route to host") }

	stubSubnet(t, []string{"192.168.1.111"}, nil)
	stubDial(t, map[string]device{"192.168.1.111:9100": {reply: []byte{0x16}}})

	got, err := DiscoverPrinters(context.Background(), 3*time.Second)
	if err != nil {
		t.Fatalf("DiscoverPrinters: %v — a broken-multicast LAN must still get sweep results", err)
	}
	if len(got) != 1 || got[0].Address != "192.168.1.111:9100" {
		t.Fatalf("got %+v, want the swept printer despite mDNS failing", got)
	}
}

// TestPrinterCandidateFromEntry_CapturesPDL — the declaration has to survive
// parsing, or the exclusion above has nothing to work with.
func TestPrinterCandidateFromEntry_CapturesPDL(t *testing.T) {
	c, ok := printerCandidateFromEntry(hpEntry())
	if !ok {
		t.Fatal("expected a candidate")
	}
	if c.PDL != hpRealPDL {
		t.Fatalf("got PDL=%q, want the advertised value %q", c.PDL, hpRealPDL)
	}
	if !nonESCPOSPDL(c.PDL) {
		t.Fatal("the HP's own advertisement must classify it as non-ESC/POS")
	}
}

// TestDiscoverPrinters_ProtectsAnOfficePrinterWhenTheBrowseFails is the
// ut-docs#1607 hole, and it matters more than it looks.
//
// The "never write to an office printer" property shipped in ut-docs#1606
// rests entirely on the mDNS browse succeeding: a browse error is
// deliberately non-fatal (a LAN with no usable multicast must still get sweep
// results), but it leaves the exclusion set EMPTY, so phase 2 writes the
// ESC/POS status query to every :9100 listener it found — including the
// inkjet. Guest Wi-Fi, an AP filtering multicast, or a wired/wireless segment
// split all produce exactly this, and the operator sees the same stray pages
// ut-docs#1606 was filed about.
//
// The device shapes below are the product owner's two real printers, measured
// on 2026-09-05: the HP answers on :631, the thermal printer does not.
func TestDiscoverPrinters_ProtectsAnOfficePrinterWhenTheBrowseFails(t *testing.T) {
	forceIPv6Supported(t, true)
	origQuery := mdnsQuery
	t.Cleanup(func() { mdnsQuery = origQuery })
	mdnsQuery = func(p *mdns.QueryParam) error { return errors.New("no multicast route") }

	stubSubnet(t, []string{"192.168.1.111", "192.168.1.245"}, nil)
	rec := stubDial(t, map[string]device{
		"192.168.1.111:9100": {reply: []byte{0x16}}, // thermal printer, answers DLE EOT
		"192.168.1.245:9100": {},                    // the HP: listening, silent
		"192.168.1.245:631":  {reply: ippHTTPReply(ippResponse(hpRealIPPFormats))},
	})

	got, err := DiscoverPrinters(context.Background(), 5*time.Second)
	if err != nil {
		t.Fatalf("DiscoverPrinters with a failed browse: %v", err)
	}

	if w := rec.writtenTo("192.168.1.245:9100"); len(w) != 0 {
		t.Errorf("wrote %d bytes (%#v) to the office printer's raw port — that is paper", len(w), w)
	}
	if len(got) != 1 || got[0].Address != "192.168.1.111:9100" {
		t.Fatalf("offered %+v, want only the thermal printer at 192.168.1.111:9100", got)
	}
}

// TestDiscoverPrinters_IPPGuardDoesNotHideAThermalPrinter guards the
// regression that would be worse than the bug: the sweep exists because the
// shop's real receipt printer advertises nothing at all, so a guard that
// excluded it would put discovery back where ut-docs#1606 found it.
func TestDiscoverPrinters_IPPGuardDoesNotHideAThermalPrinter(t *testing.T) {
	forceIPv6Supported(t, true)
	origQuery := mdnsQuery
	t.Cleanup(func() { mdnsQuery = origQuery })
	mdnsQuery = func(p *mdns.QueryParam) error { return errors.New("no multicast route") }

	stubSubnet(t, []string{"192.168.1.111"}, nil)
	stubDial(t, map[string]device{"192.168.1.111:9100": {reply: []byte{0x16}}})

	got, err := DiscoverPrinters(context.Background(), 5*time.Second)
	if err != nil {
		t.Fatalf("DiscoverPrinters: %v", err)
	}
	if len(got) != 1 || got[0].Address != "192.168.1.111:9100" {
		t.Fatalf("offered %+v, want the thermal printer at 192.168.1.111:9100", got)
	}
}

// TestDiscoverPrinters_MDNSDeclarationBeatsTheIPPGuard: when a device's own
// advertisement positively names a raw/ESC/POS format, that is a better
// signal than the IPP guard's inference, and it wins. Receipt printers with
// an IPP-capable Ethernet interface exist; they must stay offered.
func TestDiscoverPrinters_MDNSDeclarationBeatsTheIPPGuard(t *testing.T) {
	entry := &mdns.ServiceEntry{
		Name:       "TM-T88VI._pdl-datastream._tcp.local.",
		InfoFields: []string{"txtvers=1", "ty=TM-T88VI", "pdl=application/octet-stream"},
		AddrV4:     net.IPv4(192, 168, 1, 111),
		Port:       9100,
	}
	stubMDNS(t, entry)
	stubSubnet(t, []string{"192.168.1.111"}, nil)
	stubDial(t, map[string]device{
		"192.168.1.111:9100": {reply: []byte{0x16}},
		// Also speaks IPP, and lists raster formats alongside raw — the
		// shape that would trip the guard if the advertisement were ignored.
		"192.168.1.111:631": {reply: ippHTTPReply(ippResponse([]string{"image/urf", "application/octet-stream"}))},
	})

	got, err := DiscoverPrinters(context.Background(), 5*time.Second)
	if err != nil {
		t.Fatalf("DiscoverPrinters: %v", err)
	}
	if len(got) != 1 || got[0].Address != "192.168.1.111:9100" {
		t.Fatalf("offered %+v, want the advertised receipt printer", got)
	}
}

// TestDiscoverPrinters_AmbiguousAdvertisementDoesNotExemptTheIPPGuard is the
// hole the first draft of ut-docs#1607's guard shipped with.
//
// `trusted` — the set that bypasses the IPP guard — was built from "whatever
// nonESCPOSPDL did not reject". But nonESCPOSPDL returns false the moment ANY
// entry contains octet-stream, and the browse is of _pdl-datastream._tcp, the
// RAW DATASTREAM service, where an office printer listing octet-stream
// alongside its real languages is entirely normal. Such a device was neither
// excluded nor guarded, and got written to — the whole bug, reintroduced by
// the fix for it.
func TestDiscoverPrinters_AmbiguousAdvertisementDoesNotExemptTheIPPGuard(t *testing.T) {
	stubMDNS(t, &mdns.ServiceEntry{
		Name:       "HP OfficeJet Pro 9020 series [79A4B2]._pdl-datastream._tcp.local.",
		InfoFields: []string{"txtvers=1", "pdl=application/octet-stream,application/vnd.hp-PCL,image/urf"},
		AddrV4:     net.IPv4(192, 168, 1, 245),
		Port:       9100,
	})
	stubSubnet(t, []string{"192.168.1.245"}, nil)
	rec := stubDial(t, map[string]device{
		"192.168.1.245:9100": {},
		"192.168.1.245:631":  {reply: ippHTTPReply(ippResponse(hpRealIPPFormats))},
	})

	got, err := DiscoverPrinters(context.Background(), 5*time.Second)
	if err != nil {
		t.Fatalf("DiscoverPrinters: %v", err)
	}
	if w := rec.writtenTo("192.168.1.245:9100"); len(w) != 0 {
		t.Errorf("wrote %d bytes (%#v) to the office printer's raw port — that is paper", len(w), w)
	}
	if len(got) != 0 {
		t.Errorf("offered %+v, want nothing: the device's own IPP answer names four page description languages", got)
	}
}

// TestDiscoverPrinters_BrowsedCandidateWithNoPDLIsGuarded covers the OTHER
// probe path. mergePrinterCandidates probes browsed candidates itself, so a
// guard wired only into the sweep leaves it open: appearing in
// _pdl-datastream._tcp says a device speaks raw-socket printing, not that it
// speaks ESC/POS, and an advertisement with no pdl= at all declares nothing.
func TestDiscoverPrinters_BrowsedCandidateWithNoPDLIsGuarded(t *testing.T) {
	stubMDNS(t, &mdns.ServiceEntry{
		Name:       "Some Printer._pdl-datastream._tcp.local.",
		InfoFields: []string{"txtvers=1"}, // no pdl= at all
		AddrV4:     net.IPv4(192, 168, 1, 245),
		Port:       9100,
	})
	// Subnet enumeration fails, so the sweep contributes nothing and the
	// only path to this device is the browsed one.
	stubSubnet(t, nil, errors.New("no subnet"))
	rec := stubDial(t, map[string]device{
		"192.168.1.245:9100": {},
		"192.168.1.245:631":  {reply: ippHTTPReply(ippResponse(hpRealIPPFormats))},
	})

	got, err := DiscoverPrinters(context.Background(), 5*time.Second)
	if err != nil {
		t.Fatalf("DiscoverPrinters: %v", err)
	}
	if w := rec.writtenTo("192.168.1.245:9100"); len(w) != 0 {
		t.Errorf("wrote %d bytes (%#v) to the office printer's raw port — that is paper", len(w), w)
	}
	if len(got) != 0 {
		t.Errorf("offered %+v, want nothing", got)
	}
}

// TestDiscoverPrinters_ReceiptPrinterWithIPPIsStillOffered: a receipt printer
// whose Ethernet interface happens to speak IPP, on a LAN where the browse
// failed, so no advertisement can rescue it. Its format list names raw and
// text/plain — no page description language — so the guard must let it
// through. Getting this wrong hides the shop's only receipt printer, which is
// the worse of the two bugs.
func TestDiscoverPrinters_ReceiptPrinterWithIPPIsStillOffered(t *testing.T) {
	forceIPv6Supported(t, true)
	origQuery := mdnsQuery
	t.Cleanup(func() { mdnsQuery = origQuery })
	mdnsQuery = func(p *mdns.QueryParam) error { return errors.New("no multicast route") }

	stubSubnet(t, []string{"192.168.1.111"}, nil)
	stubDial(t, map[string]device{
		"192.168.1.111:9100": {reply: []byte{0x16}},
		"192.168.1.111:631":  {reply: ippHTTPReply(ippResponse([]string{"application/octet-stream", "text/plain"}))},
	})

	got, err := DiscoverPrinters(context.Background(), 5*time.Second)
	if err != nil {
		t.Fatalf("DiscoverPrinters: %v", err)
	}
	if len(got) != 1 || got[0].Address != "192.168.1.111:9100" {
		t.Fatalf("offered %+v, want the receipt printer at 192.168.1.111:9100", got)
	}
}
