package okc_test

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/plugins/tax-tr/okc"
	"github.com/universaltill/universal-till/plugins/tax-tr/okc/sim"
)

// netTransport is the host-side Transport: a real loopback socket with the
// same connect/read deadlines the wasm adapter applies.
type netTransport struct{ readTimeout time.Duration }

func (n netTransport) Dial(host string, port int, connectTimeoutMs int) (io.ReadWriteCloser, error) {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), time.Duration(connectTimeoutMs)*time.Millisecond)
	if err != nil {
		return nil, err
	}
	rt := n.readTimeout
	if rt == 0 {
		rt = 2 * time.Second
	}
	_ = conn.SetReadDeadline(time.Now().Add(rt))
	return conn, nil
}

func startSim(t *testing.T, opts sim.Options) *sim.Server {
	t.Helper()
	s, err := sim.Start("127.0.0.1:0", opts)
	if err != nil {
		t.Fatalf("start sim: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func sale(lines int) okc.SaleRequest {
	req := okc.SaleRequest{RequestID: "ev-1", Currency: "TRY", Amount: 3000, Total: 3000, TaxInclusive: true}
	for i := 0; i < lines; i++ {
		req.Lines = append(req.Lines, okc.Line{Name: "Çay", Qty: 1, UnitPrice: 1500, TaxRateBP: 1000})
	}
	return req
}

func TestBridgeSale_PrintsAndReturnsEvidence(t *testing.T) {
	s := startSim(t, sim.Options{Serial: "AV12345", Maker: "beko", ZNo: 3})
	d := okc.NewBridgeDriver(netTransport{}, okc.Config{Host: "127.0.0.1", Port: s.Port()})

	ev, err := d.Sale(sale(2))
	if err != nil {
		t.Fatalf("Sale: %v", err)
	}
	if ev.Kind != "okc" || ev.Serial != "AV12345" || ev.Maker != "beko" || ev.ReceiptNo != "0000001" || ev.ReceiptKind != "mali_fis" || ev.ZNo != 3 || ev.IssuedAt == "" {
		t.Fatalf("evidence = %+v", ev)
	}
	log := s.Log()
	if len(log) != 1 || log[0].Op != "sale" || log[0].Amount != 3000 || log[0].Lines != 2 {
		t.Fatalf("device log = %+v", log)
	}
}

// A retried authorize (same request_id) must not print twice.
func TestBridgeSale_IdempotentOnRequestID(t *testing.T) {
	s := startSim(t, sim.Options{})
	d := okc.NewBridgeDriver(netTransport{}, okc.Config{Host: "127.0.0.1", Port: s.Port()})
	first, err := d.Sale(sale(1))
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.Sale(sale(1))
	if err != nil {
		t.Fatal(err)
	}
	if first.ReceiptNo != second.ReceiptNo {
		t.Fatalf("retry printed a new receipt: %s then %s", first.ReceiptNo, second.ReceiptNo)
	}
	if n := len(s.Log()); n != 1 {
		t.Fatalf("device printed %d receipts, want 1", n)
	}
}

func TestBridgeSale_DeclinedByDevice(t *testing.T) {
	s := startSim(t, sim.Options{DeclineAll: true})
	d := okc.NewBridgeDriver(netTransport{}, okc.Config{Host: "127.0.0.1", Port: s.Port()})
	_, err := d.Sale(sale(1))
	if !errors.Is(err, okc.ErrDeviceDeclined) {
		t.Fatalf("err = %v, want ErrDeviceDeclined", err)
	}
	if n := len(s.Log()); n != 0 {
		t.Fatalf("declined sale printed %d receipts", n)
	}
}

func TestBridgeSale_SplitTenderRefusedBeforeDial(t *testing.T) {
	// No device at all: the refusal must come from the driver, not the wire.
	d := okc.NewBridgeDriver(netTransport{}, okc.Config{Host: "127.0.0.1", Port: 1})
	req := sale(1)
	req.Amount = 1000 // of a 3000 total
	if _, err := d.Sale(req); !errors.Is(err, okc.ErrSplitTender) {
		t.Fatalf("err = %v, want ErrSplitTender", err)
	}
}

func TestBridgeSale_Unreachable(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close() // nothing listens here any more
	d := okc.NewBridgeDriver(netTransport{}, okc.Config{Host: "127.0.0.1", Port: port, ConnectTimeoutMs: 500})
	if _, err := d.Sale(sale(1)); !errors.Is(err, okc.ErrDeviceUnreachable) {
		t.Fatalf("err = %v, want ErrDeviceUnreachable", err)
	}
}

func TestBridgeSale_SilentDeviceTimesOut(t *testing.T) {
	s := startSim(t, sim.Options{Silent: true})
	d := okc.NewBridgeDriver(netTransport{readTimeout: 300 * time.Millisecond}, okc.Config{Host: "127.0.0.1", Port: s.Port()})
	start := time.Now()
	_, err := d.Sale(sale(1))
	if !errors.Is(err, okc.ErrDeviceUnreachable) {
		t.Fatalf("err = %v, want ErrDeviceUnreachable", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatalf("silent device blocked for %s", time.Since(start))
	}
}

func TestBridgeRefundStatusZClose(t *testing.T) {
	s := startSim(t, sim.Options{ZNo: 9})
	d := okc.NewBridgeDriver(netTransport{}, okc.Config{Host: "127.0.0.1", Port: s.Port()})
	if _, err := d.Sale(sale(1)); err != nil {
		t.Fatal(err)
	}
	ev, err := d.Refund(okc.RefundRequest{RequestID: "r-1", Amount: 1500, OriginalReceipt: "0000001"})
	if err != nil || ev.ReceiptKind != "iade_fisi" || ev.ReceiptNo != "0000002" {
		t.Fatalf("refund: ev=%+v err=%v", ev, err)
	}
	st, err := d.Status()
	if err != nil || !st.Online || st.ZNo != 9 || st.ReceiptsToday != 2 {
		t.Fatalf("status: %+v err=%v", st, err)
	}
	z, err := d.ZClose()
	if err != nil || z != 10 {
		t.Fatalf("z close: %d err=%v", z, err)
	}
	st, _ = d.Status()
	if st.ReceiptsToday != 0 {
		t.Fatalf("receipts_today after Z = %d", st.ReceiptsToday)
	}
}

func TestNewDriver(t *testing.T) {
	for _, name := range okc.DriverNames {
		d, err := okc.NewDriver(netTransport{}, okc.Config{Driver: name})
		if err != nil || d == nil {
			t.Fatalf("driver %q: %v", name, err)
		}
		if name == "bridge" {
			continue
		}
		// Maker scaffolds fail closed, loudly, until filled in.
		if _, err := d.Sale(okc.SaleRequest{Amount: 1, Total: 1}); !errors.Is(err, okc.ErrDriverNotImplemented) {
			t.Fatalf("%s Sale err = %v, want ErrDriverNotImplemented", name, err)
		}
	}
	if _, err := okc.NewDriver(netTransport{}, okc.Config{Driver: "beko-magic"}); !errors.Is(err, okc.ErrUnknownDriver) || !strings.Contains(err.Error(), "bridge") {
		t.Fatalf("unknown driver err = %v", err)
	}
}

func TestConfigNormalize(t *testing.T) {
	c := okc.Config{}.Normalize()
	if c.Driver != "bridge" || c.Host != "127.0.0.1" || c.Port != 4711 || c.ConnectTimeoutMs != 3000 || c.ReadTimeoutMs != 8000 {
		t.Fatalf("defaults: %+v", c)
	}
	c = okc.Config{Port: 70000}.Normalize()
	if c.Port != 4711 {
		t.Fatalf("out-of-range port kept: %d", c.Port)
	}
}
