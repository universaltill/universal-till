package print

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
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
	Mode      string // off | network | device
	Address   string // network: host[:port], default port 9100
	Device    string // device: character device path, e.g. /dev/usb/lp0
	Charset   string // utf8 | ascii
	AutoPrint bool
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

// networkTransport speaks raw TCP :9100 — every ethernet/wifi ESC/POS
// printer supports it.
type networkTransport struct{ addr string }

func (t *networkTransport) Describe() string { return "network " + t.addr }

func (t *networkTransport) Print(ctx context.Context, data []byte) error {
	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", t.addr)
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
		return fmt.Errorf("printer write %s: %w", t.addr, err)
	}
	return nil
}

// deviceTransport writes to a character device (USB printer on Linux:
// /dev/usb/lp0). Opening for append keeps it simple and driverless.
type deviceTransport struct{ path string }

func (t *deviceTransport) Describe() string { return "device " + t.path }

func (t *deviceTransport) Print(ctx context.Context, data []byte) error {
	f, err := os.OpenFile(t.path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("printer open %s: %w", t.path, err)
	}
	defer f.Close()
	done := make(chan error, 1)
	go func() {
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
