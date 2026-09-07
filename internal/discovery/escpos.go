package discovery

import (
	"context"
	"net"
	"strings"
	"time"
)

// ESC/POS real-time status query: DLE EOT n, n=1 (printer status).
//
// On a genuine ESC/POS printer this is handled immediately by the firmware
// and prints nothing. On a device that is NOT an ESC/POS printer it is just
// three bytes of job data — and an office printer will faithfully put them
// on paper. That happened: probing an HP OfficeJet on :9100 during
// development made it print one character per page (ut-docs#1606). Never
// send this to a device without first ruling out that it is something else;
// see nonESCPOSPDL and the two-phase sweep.
var escposStatusQuery = []byte{0x10, 0x04, 0x01}

// A DLE EOT reply has four bits the spec fixes regardless of printer state:
// b0=0, b1=1, b4=1, b7=0. Masking with 0x93 must therefore leave exactly
// 0x12. The remaining bits carry drawer/offline/paper state, which varies and
// is deliberately NOT checked — this asks "is this an ESC/POS printer at
// all", not "is it ready".
//
// Verified against the product owner's real thermal printer: 0x16 & 0x93 == 0x12.
const (
	escposStatusFixedMask = 0x93
	escposStatusFixedBits = 0x12
)

// escposDialTimeout is the per-host connect budget. A device on the same LAN
// completes a TCP handshake in single-digit milliseconds.
const escposDialTimeout = 700 * time.Millisecond

// escposReadTimeout is how long to wait for the status byte, and is
// deliberately much longer than the dial budget. Measured on the product
// owner's thermal printer (ut-docs#1606): it answers reliably at 3s but NOT
// within 700ms. Sharing one short timeout between connect and read is what
// made the first version of this sweep miss the very printer it was written
// to find — the unit tests could not catch it because a stubbed conn answers
// instantly. Cheap embedded printers are slow; budget for them.
const escposReadTimeout = 3 * time.Second

// validESCPOSStatus reports whether b is a well-formed DLE EOT reply.
func validESCPOSStatus(b byte) bool {
	return b&escposStatusFixedMask == escposStatusFixedBits
}

// dialProbe is a seam over net.Dialer.DialContext so the probe and the sweep
// can be driven deterministically in tests, without binding real listeners.
var dialProbe = func(ctx context.Context, addr string, timeout time.Duration) (net.Conn, error) {
	d := net.Dialer{Timeout: timeout}
	return d.DialContext(ctx, "tcp", addr)
}

// Listens reports whether anything accepts a TCP connection at addr. It
// writes NOTHING — the connection is opened and immediately closed.
//
// This is the safe first phase of finding printers. An empty job is
// discarded by every printer, so a device that turns out not to be an
// ESC/POS printer is never made to put anything on paper by being looked at.
func Listens(ctx context.Context, addr string, timeout time.Duration) bool {
	conn, err := dialProbe(ctx, addr, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// SpeaksESCPOS reports whether the device at addr answers an ESC/POS
// real-time status query — the one reliable way to tell a receipt printer
// from anything else listening on the raw-print port.
//
// CALLERS MUST NOT call this on a device already known to be something else.
// It writes to the socket, and a non-ESC/POS printer prints what it receives.
// Use nonESCPOSPDL to exclude anything whose own mDNS advertisement declares
// a different page description language, before any bytes are sent.
//
// Why the check is needed at all: being reachable on :9100 does not mean a
// device can print a receipt. The product owner's LAN has an HP OfficeJet Pro
// 9020 on :9100 that advertises itself as a printer and is one — but an
// inkjet speaking PCL and PWG-raster, which renders an ESC/POS receipt as
// garbage. Meanwhile the real thermal printer advertises no mDNS at all and
// was invisible to discovery. Offering the first and hiding the second is
// exactly backwards for the operator.
func SpeaksESCPOS(ctx context.Context, addr string) bool {
	conn, err := dialProbe(ctx, addr, escposDialTimeout)
	if err != nil {
		return false
	}
	defer conn.Close()

	deadline := time.Now().Add(escposReadTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	if _, err := conn.Write(escposStatusQuery); err != nil {
		return false
	}
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil {
		return false
	}
	return validESCPOSStatus(buf[0])
}

// nonESCPOSPDL reports whether an mDNS "pdl=" TXT value positively declares
// page description languages that are NOT ESC/POS — i.e. whether the device
// has told us, in its own advertisement, that it cannot print our receipts.
//
// This is what keeps the probe away from office printers. The HP OfficeJet
// publishes:
//
//	pdl=application/vnd.hp-PCL,image/jpeg,image/urf,image/pwg-raster,application/PCLm
//
// which names five languages, none of them ESC/POS. Excluding it on that
// basis means it is never written to, so it never prints a stray page — the
// bug this function exists to prevent.
//
// Deliberately conservative: it returns true only when the device declares a
// non-empty list that contains no ESC/POS-compatible entry. An empty or
// absent pdl (typical of cheap thermal printers, which advertise nothing at
// all) is NOT a declaration and must not be treated as one, or the sweep
// would exclude the printers it exists to find.
func nonESCPOSPDL(pdl string) bool {
	pdl = strings.TrimSpace(strings.ToLower(pdl))
	if pdl == "" {
		return false
	}
	for _, part := range strings.Split(pdl, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		// application/octet-stream is what an ESC/POS-capable device
		// advertises when it advertises anything: raw bytes, no page
		// description language. Anything naming escpos/esc-pos explicitly
		// counts too.
		if strings.Contains(part, "octet-stream") ||
			strings.Contains(part, "escpos") ||
			strings.Contains(part, "esc-pos") {
			return false
		}
	}
	return true
}
