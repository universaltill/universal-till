package print

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// ut-docs#2558: "No sale" opens the cash drawer without a sale. The drawer
// hangs off the receipt printer's kick connector, so OpenDrawer sends the
// kick pulse and NOTHING else — no init, no text, no feed, no cut (a blank
// slip on every No sale would waste paper and confuse the customer).

func TestDrawerKickBytes_OnlyTheKickPulseForThePin(t *testing.T) {
	pin2 := []byte{0x1b, 0x70, 0x00, 0x19, 0xfa}
	pin5 := []byte{0x1b, 0x70, 0x01, 0x19, 0xfa}
	for _, c := range []struct {
		pin  int
		want []byte
	}{{0, pin2}, {2, pin2}, {5, pin5}, {9, pin2}} {
		if got := DrawerKickBytes(c.pin); !bytes.Equal(got, c.want) {
			t.Errorf("DrawerKickBytes(%d) = % x, want % x", c.pin, got, c.want)
		}
	}
}

// The device transport writes to a plain file in a test, so the exact bytes
// that would reach the printer can be read back.
func TestOpenDrawer_DeviceSendsOnlyKickBytes(t *testing.T) {
	for _, pin := range []int{2, 5} {
		dev := filepath.Join(t.TempDir(), "lp0")
		if err := os.WriteFile(dev, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := OpenDrawer(context.Background(), Config{Mode: "device", Device: dev, DrawerPin: pin}); err != nil {
			t.Fatalf("pin %d: OpenDrawer = %v", pin, err)
		}
		got, err := os.ReadFile(dev)
		if err != nil {
			t.Fatal(err)
		}
		if want := DrawerKickBytes(pin); !bytes.Equal(got, want) {
			t.Fatalf("pin %d: printer received % x, want only the kick % x (no paper, no cut)", pin, got, want)
		}
		if bytes.Contains(got, cmdFeedCut) || bytes.Contains(got, cmdInit) {
			t.Fatalf("pin %d: drawer-only kick must not init/feed/cut: % x", pin, got)
		}
	}
}

func TestOpenDrawer_NoPrinterOrSystemPrinterIsASentinel(t *testing.T) {
	for _, mode := range []string{"", "off"} {
		if err := OpenDrawer(context.Background(), Config{Mode: mode}); !errors.Is(err, ErrNoDrawerPrinter) {
			t.Fatalf("mode %q: OpenDrawer = %v, want ErrNoDrawerPrinter", mode, err)
		}
	}
	// A CUPS/lp office printer has no kick connector; ESC/POS bytes there
	// would print as garbage.
	if err := OpenDrawer(context.Background(), Config{Mode: "system"}); !errors.Is(err, ErrDrawerUnsupported) {
		t.Fatalf("system: OpenDrawer = %v, want ErrDrawerUnsupported", err)
	}
}

func TestOpenDrawer_TransportFailureIsReturned(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-dir", "lp0")
	err := OpenDrawer(context.Background(), Config{Mode: "device", Device: missing})
	if err == nil {
		t.Fatal("OpenDrawer to a missing device must fail")
	}
	if errors.Is(err, ErrNoDrawerPrinter) || errors.Is(err, ErrDrawerUnsupported) {
		t.Fatalf("a transport failure must not look like a configuration sentinel: %v", err)
	}
	// A misconfigured thermal printer (no device path) is a failure too.
	if err := OpenDrawer(context.Background(), Config{Mode: "device"}); err == nil {
		t.Fatal("device mode with no path must fail")
	}
}
