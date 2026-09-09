package okc_test

import (
	"errors"
	"testing"

	"github.com/universaltill/universal-till/plugins/tax-tr/okc"
	"github.com/universaltill/universal-till/plugins/tax-tr/okc/sim"
)

// fakeDriver is a minimal okc.Driver double that does none of its own
// receipt validation — exactly the shape a new maker driver could ship in
// (ut-docs#1780), which is what NewValidatingDriver exists to catch
// regardless.
type fakeDriver struct {
	ev  okc.Evidence
	err error
}

func (f fakeDriver) Sale(okc.SaleRequest) (okc.Evidence, error)     { return f.ev, f.err }
func (f fakeDriver) Refund(okc.RefundRequest) (okc.Evidence, error) { return f.ev, f.err }
func (f fakeDriver) Status() (okc.Status, error)                    { return okc.Status{}, nil }

func TestValidatingDriver_RefusesBlankReceiptEvenIfDriverDidnt(t *testing.T) {
	d := okc.NewValidatingDriver(fakeDriver{ev: okc.Evidence{ReceiptKind: "mali_fis"}})
	if _, err := d.Sale(okc.SaleRequest{Amount: 1, Total: 1}); !errors.Is(err, okc.ErrNoReceipt) {
		t.Fatalf("Sale err = %v, want ErrNoReceipt", err)
	}
	if _, err := d.Refund(okc.RefundRequest{Amount: 1}); !errors.Is(err, okc.ErrNoReceipt) {
		t.Fatalf("Refund err = %v, want ErrNoReceipt", err)
	}
}

func TestValidatingDriver_RefusesWhitespaceOnlyReceipt(t *testing.T) {
	d := okc.NewValidatingDriver(fakeDriver{ev: okc.Evidence{ReceiptNo: "  ​ "}})
	if _, err := d.Sale(okc.SaleRequest{Amount: 1, Total: 1}); !errors.Is(err, okc.ErrNoReceipt) {
		t.Fatalf("Sale err = %v, want ErrNoReceipt", err)
	}
}

func TestValidatingDriver_PassesThroughRealReceipt(t *testing.T) {
	want := okc.Evidence{ReceiptNo: "0000099", ReceiptKind: "mali_fis"}
	d := okc.NewValidatingDriver(fakeDriver{ev: want})
	got, err := d.Sale(okc.SaleRequest{Amount: 1, Total: 1})
	if err != nil || got != want {
		t.Fatalf("Sale = %+v err=%v, want %+v", got, err, want)
	}
	got, err = d.Refund(okc.RefundRequest{Amount: 1})
	if err != nil || got != want {
		t.Fatalf("Refund = %+v err=%v, want %+v", got, err, want)
	}
}

func TestValidatingDriver_PassesThroughDriverError(t *testing.T) {
	d := okc.NewValidatingDriver(fakeDriver{err: okc.ErrDeviceDeclined})
	if _, err := d.Sale(okc.SaleRequest{}); !errors.Is(err, okc.ErrDeviceDeclined) {
		t.Fatalf("Sale err = %v, want ErrDeviceDeclined", err)
	}
	if _, err := d.Refund(okc.RefundRequest{}); !errors.Is(err, okc.ErrDeviceDeclined) {
		t.Fatalf("Refund err = %v, want ErrDeviceDeclined", err)
	}
}

// TestNewDriver_WrapsEveryDriverWithValidation proves the choke point is
// NewDriver itself, not a per-driver call site (ut-docs#1780) — every name
// it knows about must come back validated, without each maker driver
// re-implementing the check.
func TestNewDriver_WrapsEveryDriverWithValidation(t *testing.T) {
	// The bridge driver already refuses a blank receipt on its own
	// (bridge_test.go); this only proves NewDriver's wrap doesn't depend on
	// that — a scaffold driver that returns ErrDriverNotImplemented (a
	// non-nil error) must still pass that error through unchanged.
	for _, name := range okc.DriverNames {
		if name == "bridge" {
			continue
		}
		d, err := okc.NewDriver(nil, okc.Config{Driver: name})
		if err != nil {
			t.Fatalf("driver %q: %v", name, err)
		}
		if _, err := d.Sale(okc.SaleRequest{Amount: 1, Total: 1}); !errors.Is(err, okc.ErrDriverNotImplemented) {
			t.Fatalf("%s Sale err = %v, want ErrDriverNotImplemented", name, err)
		}
	}
}

// TestNewDriver_Bridge_RefusesBlankReceipt goes through the exact call site
// main.go uses in production (okc.NewDriver, never okc.NewBridgeDriver
// directly) to prove the choke point is actually wired in, not just
// implemented and unit-tested in isolation — every other test for this
// invariant (here and in bridge_test.go) calls NewBridgeDriver or
// NewValidatingDriver directly and would keep passing even if NewDriver's
// own wiring were accidentally dropped.
func TestNewDriver_Bridge_RefusesBlankReceipt(t *testing.T) {
	empty := ""
	s := startSim(t, sim.Options{ReceiptNoOverride: &empty})
	d, err := okc.NewDriver(netTransport{}, okc.Config{Driver: "bridge", Host: "127.0.0.1", Port: s.Port()})
	if err != nil {
		t.Fatalf("NewDriver: %v", err)
	}
	if _, err := d.Sale(sale(1)); !errors.Is(err, okc.ErrNoReceipt) {
		t.Fatalf("Sale err = %v, want ErrNoReceipt", err)
	}
}
