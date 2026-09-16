package print

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Transport delivers rendered bytes to a physical printer.
type Transport interface {
	Print(ctx context.Context, data []byte) error
	// Describe names the destination for logs/test-print feedback.
	Describe() string
}

// Config mirrors the printer.* settings.
type Config struct {
	Mode    string // off | network | device
	Address string // network: host[:port], default port 9100
	Device  string // device: character device path, e.g. /dev/usb/lp0
	Charset string // utf8 | ascii | cp858 | win1250 | win1257 | win1253 | win1254
	// ReceiptPolicy is the resolved shop-wide receipt policy (ADR-0089):
	// always | ask | never. AutoPrint is derived from it (== "always") so
	// the existing auto-print gate needs no change; callers that render
	// the sale-completion screen read ReceiptPolicy itself to decide
	// whether to show the "would you like a receipt?" prompt.
	ReceiptPolicy string
	AutoPrint     bool
	// DrawerPin is the drawer-kick connector pin: 2 (default) or 5
	// (ut-docs#1136). See Doc.DrawerPin in escpos.go for the byte mapping.
	DrawerPin int
	// KitchenAddress routes kitchen tickets to a SEPARATE printer — a
	// network host[:port] or a device path. Empty means kitchen printing is
	// off (see arch/restaurant-phone-orders.md). Charset is shared.
	KitchenAddress string
}

// KitchenEnabled reports whether a kitchen printer is configured.
func (c Config) KitchenEnabled() bool { return strings.TrimSpace(c.KitchenAddress) != "" }

// TransportForAddress builds a transport from a bare address, auto-detecting
// the mode: a leading "/" is a character device, anything else is a network
// host[:port]. An empty address returns (nil, nil) — the caller treats a nil
// transport as "printing off". Used for the kitchen printer, which is a plain
// address rather than a full mode/address/device Config.
func TransportForAddress(addr string) (Transport, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, nil
	}
	if strings.HasPrefix(addr, "/") {
		return NewTransport(Config{Mode: "device", Device: addr})
	}
	return NewTransport(Config{Mode: "network", Address: addr})
}

// Enabled reports whether a printer is configured at all.
func (c Config) Enabled() bool {
	return c.Mode == "network" || c.Mode == "device" || c.Mode == "system"
}

// NewTransport builds the transport for a config; nil when Mode is off.
func NewTransport(c Config) (Transport, error) {
	switch c.Mode {
	case "network":
		addr := strings.TrimSpace(c.Address)
		if addr == "" {
			return nil, fmt.Errorf("printer address required for network mode")
		}
		if !strings.Contains(addr, ":") {
			addr += ":9100" // raw-print standard port
		}
		return &networkTransport{addr: addr}, nil
	case "device":
		dev := strings.TrimSpace(c.Device)
		if dev == "" {
			return nil, fmt.Errorf("printer device path required for device mode")
		}
		return &deviceTransport{path: dev}, nil
	case "", "off":
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown printer mode %q", c.Mode)
	}
}

// dialFn is the dial function networkTransport uses to open the TCP
// connection to the printer. It's a package-level var (rather than a call
// to net.Dialer.DialContext inline) purely so tests can substitute a fake
// dialer to simulate connect failures/retries without a real listener.
var dialFn = (&net.Dialer{Timeout: 5 * time.Second}).DialContext

// destLocks holds one 1-buffered channel per printer destination, used as
// a ctx-aware mutex (ut-docs#2287). Most ESC/POS network printers, and raw
// USB character devices, accept only ONE connection/writer at a time; a
// sale fires the receipt print and the kitchen ticket print concurrently
// (internal/pages/print_api.go and kitchen_print.go, both via
// d.AsyncWork), and when both are configured to the SAME printer the
// second job used to dial/open while the first was still mid-flight —
// the printer refused or interleaved it, and it was silently lost (only
// an audit row recorded the failure). Keying by destination (not a single
// global lock) means two DIFFERENT printers still print concurrently;
// only jobs that actually share a destination queue behind each other.
var destLocks sync.Map // string -> chan struct{}

// acquireDest blocks until the destination named by key is free, or ctx is
// done — whichever comes first. The returned release func must be called
// (typically via defer) once the caller is finished with the destination.
func acquireDest(ctx context.Context, key string) (release func(), err error) {
	v, _ := destLocks.LoadOrStore(key, make(chan struct{}, 1))
	sem := v.(chan struct{})
	select {
	case sem <- struct{}{}:
		return func() { <-sem }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// isRetryableDialErr reports whether a dial failure looks like a printer
// that was momentarily busy with someone else's connection (connection
// refused/reset) rather than a configuration problem (bad hostname) that
// a retry can't fix. io.EOF cannot come out of a real TCP DialContext
// (net.Dialer wraps errnos in *net.OpError); it is accepted only so an
// injected dialFn in tests can signal "the printer dropped the handshake".
func isRetryableDialErr(err error) bool {
	if err == nil {
		return false
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return false
	}
	return errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, io.EOF)
}

// dialRetryBackoff is the delay before each retry attempt, in order.
var dialRetryBackoff = []time.Duration{300 * time.Millisecond, 600 * time.Millisecond}

// dialWithRetry dials addr, retrying up to len(dialRetryBackoff)+1 attempts
// total on a connection-refused/reset-class error — a printer that
// momentarily refused a second connection is usually free a moment later.
// A "no such host" DNS error, or ctx cancellation, is never retried. This
// is deliberately dial-only: a write error after a successful dial is NOT
// retried by the caller, since the printer may already have printed part
// of the job and a retry would risk double-printing.
func dialWithRetry(ctx context.Context, addr string) (net.Conn, error) {
	var lastErr error
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			if lastErr == nil {
				lastErr = err
			}
			return nil, lastErr
		}
		conn, err := dialFn(ctx, "tcp", addr)
		if err == nil {
			return conn, nil
		}
		if conn != nil {
			// net.Dialer never returns both; a substituted dialFn could.
			_ = conn.Close()
		}
		lastErr = err
		if attempt >= len(dialRetryBackoff) || !isRetryableDialErr(err) {
			return nil, lastErr
		}
		select {
		case <-time.After(dialRetryBackoff[attempt]):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// networkTransport speaks raw TCP :9100 — every ethernet/wifi ESC/POS
// printer supports it.
//
// Print serialises jobs to the SAME address (ut-docs#2287): most network
// ESC/POS printers accept only one TCP connection at a time and refuse or
// reset a second, which is exactly what happens when a receipt print and
// a kitchen ticket print are both routed to the one printer. Jobs to
// DIFFERENT addresses are unaffected — the lock is per-destination, never
// global.
type networkTransport struct{ addr string }

func (t *networkTransport) Describe() string { return "network " + t.addr }

func (t *networkTransport) Print(ctx context.Context, data []byte) error {
	release, err := acquireDest(ctx, "network:"+t.addr)
	if err != nil {
		// "busy", not "connect": the job never dialled — it timed out queued
		// behind another job to the same printer, which is a different
		// diagnosis in the audit row than a printer that refused a dial.
		return fmt.Errorf("printer busy %s: %w", t.addr, err)
	}
	defer release()

	conn, err := dialWithRetry(ctx, t.addr)
	if err != nil {
		return fmt.Errorf("printer connect %s: %w", t.addr, err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(deadline)
	} else {
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	}
	if _, err := conn.Write(data); err != nil {
		// Deliberately not retried: the printer may already have started
		// printing part of the job, and a retry would risk double-print.
		return fmt.Errorf("printer write %s: %w", t.addr, err)
	}
	return nil
}

// deviceTransport writes to a character device (USB printer on Linux:
// /dev/usb/lp0). Opening for append keeps it simple and driverless.
//
// Print serialises jobs to the SAME device path (ut-docs#2287): a raw USB
// character device has no notion of concurrent independent writers — two
// goroutines each opening the path and writing at once can interleave
// bytes at the kernel/driver buffering boundary, corrupting both jobs'
// output. Jobs to DIFFERENT paths are unaffected.
type deviceTransport struct{ path string }

func (t *deviceTransport) Describe() string { return "device " + t.path }

func (t *deviceTransport) Print(ctx context.Context, data []byte) error {
	release, err := acquireDest(ctx, "device:"+t.path)
	if err != nil {
		return fmt.Errorf("printer busy %s: %w", t.path, err)
	}

	f, err := os.OpenFile(t.path, os.O_WRONLY, 0)
	if err != nil {
		release()
		return fmt.Errorf("printer open %s: %w", t.path, err)
	}
	// The destination stays held until write(2) actually RETURNS, not until
	// this call returns (ut-docs#2287 review, finding 2). On a non-pollable
	// character device a blocked write (printer out of paper) survives both
	// ctx expiry and f.Close() — Close only drops a reference — so releasing
	// on ctx.Done() would let the next job open a second fd and interleave
	// with the still-pending bytes the moment paper is refilled. Holding the
	// lock from inside the goroutine means a successor instead fails loudly
	// on its own lock wait ("printer busy", audited by the caller).
	done := make(chan error, 1)
	go func() {
		defer release()
		defer f.Close()
		_, werr := f.Write(data)
		done <- werr
	}()
	select {
	case werr := <-done:
		if werr != nil {
			return fmt.Errorf("printer write %s: %w", t.path, werr)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("printer write %s: %w", t.path, ctx.Err())
	}
}
