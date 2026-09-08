//go:build wasip1

// ut-plugin-tax-tr — Turkish fiscal-device (YN ÖKC) payment plugin.
//
// Runs in the till's WASM runtime (ADR-0001): one instance per event, the
// event JSON on stdin, the answer on stdout, exit 0 = approved, exit 1 =
// declined (the tender is refused and NO sale row is created — that is the
// fail-closed behaviour Law No. 3100 needs, see internal/fiscal/device.go).
//
// Events (payment-provider contract, ut-docs reference/payment-provider-
// contract.md, plus the additive basket fields core now sends):
//
//	payment.okc.authorize  → drive the device: it takes the money and prints
//	                         the mali fiş; answer {"status":"approved",
//	                         "fiscal_device":{…}} with the device's receipt.
//	payment.okc.refund     → device prints the refund slip (iade fişi).
//	payment.okc.requested  → settle notification after the sale is recorded;
//	                         nothing to do (the device already has it).
//
// Settings (plugin.json → plugin_settings, editable at
// /plugins/com.universaltill.tax-tr/settings): okc.driver, okc.host,
// okc.port, okc.maker, okc.connect_timeout_ms, okc.read_timeout_ms.
//
// Build: GOOS=wasip1 GOARCH=wasm go build -o dist/plugin.wasm ./plugins/tax-tr
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"github.com/universaltill/universal-till/plugins/tax-tr/okc"
)

//go:wasmimport ut settings_get
func settingsGet(keyPtr, keyLen, dstPtr, dstCap uint32) int32

//go:wasmimport ut log_write
func logWrite(ptr, length uint32)

//go:wasmimport ut tcp_open
func tcpOpen(hostPtr, hostLen, port, timeoutMs uint32) int32

//go:wasmimport ut tcp_write
func tcpWrite(handle int32, ptr, length uint32) int32

//go:wasmimport ut tcp_read
func tcpRead(handle int32, dstPtr, dstCap, timeoutMs uint32) int32

//go:wasmimport ut tcp_close
func tcpClose(handle int32) int32

func ptrOf(b []byte) (uint32, uint32) {
	if len(b) == 0 {
		return 0, 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0]))), uint32(len(b))
}

func logf(format string, args ...any) {
	msg := []byte(fmt.Sprintf(format, args...))
	p, l := ptrOf(msg)
	logWrite(p, l)
}

func setting(key string) string {
	buf := make([]byte, 4096)
	kp, kl := ptrOf([]byte(key))
	dp, dc := ptrOf(buf)
	n := settingsGet(kp, kl, dp, dc)
	if n <= 0 {
		return ""
	}
	return strings.TrimSpace(string(buf[:n]))
}

func settingInt(key string) int {
	v, _ := strconv.Atoi(setting(key))
	return v
}

// hostTransport adapts the till's tcp_* host functions to okc.Transport.
type hostTransport struct{ readTimeoutMs int }

type hostConn struct {
	handle        int32
	readTimeoutMs int
}

func (t hostTransport) Dial(host string, port int, connectTimeoutMs int) (io.ReadWriteCloser, error) {
	hp, hl := ptrOf([]byte(host))
	h := tcpOpen(hp, hl, uint32(port), uint32(connectTimeoutMs))
	if h < 0 {
		return nil, fmt.Errorf("tcp_open %s:%d failed (%d) — check the tcp:* permission grant and the device address", host, port, h)
	}
	return &hostConn{handle: h, readTimeoutMs: t.readTimeoutMs}, nil
}

func (c *hostConn) Write(p []byte) (int, error) {
	pp, pl := ptrOf(p)
	if code := tcpWrite(c.handle, pp, pl); code < 0 {
		return 0, fmt.Errorf("tcp_write failed (%d)", code)
	}
	return len(p), nil
}

func (c *hostConn) Read(p []byte) (int, error) {
	pp, pc := ptrOf(p)
	n := tcpRead(c.handle, pp, pc, uint32(c.readTimeoutMs))
	switch {
	case n > 0:
		return int(n), nil
	case n == 0:
		return 0, io.EOF
	default:
		return 0, fmt.Errorf("tcp_read failed (%d)", n)
	}
}

func (c *hostConn) Close() error {
	tcpClose(c.handle)
	return nil
}

// event is the till's envelope plus the payment-provider payload with the
// additive basket fields (ut-plugin-tax-tr contract v0).
type event struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Payload struct {
		Method          string     `json:"method"`
		Amount          int64      `json:"amount"`
		Reference       string     `json:"reference"`
		Currency        string     `json:"currency"`
		Total           int64      `json:"total"`
		TaxInclusive    bool       `json:"tax_inclusive"`
		SaleDiscount    int64      `json:"sale_discount"`
		ServiceCharge   int64      `json:"service_charge"`
		Lines           []okc.Line `json:"lines"`
		OriginalSaleID  string     `json:"original_sale_id"`
		OriginalReceipt string     `json:"original_receipt"`
	} `json:"payload"`
}

type answer struct {
	Status       string       `json:"status"`
	FiscalDevice okc.Evidence `json:"fiscal_device"`
}

func main() {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fail("read event: %v", err)
	}
	var ev event
	if err := json.Unmarshal(raw, &ev); err != nil {
		fail("parse event: %v", err)
	}

	cfg := okc.Config{
		Driver:           setting("okc.driver"),
		Host:             setting("okc.host"),
		Port:             settingInt("okc.port"),
		Maker:            setting("okc.maker"),
		ConnectTimeoutMs: settingInt("okc.connect_timeout_ms"),
		ReadTimeoutMs:    settingInt("okc.read_timeout_ms"),
	}.Normalize()

	driver, err := okc.NewDriver(hostTransport{readTimeoutMs: cfg.ReadTimeoutMs}, cfg)
	if err != nil {
		fail("%v", err)
	}

	switch {
	case strings.HasSuffix(ev.Type, ".authorize"):
		p := ev.Payload
		evidence, err := driver.Sale(okc.SaleRequest{
			RequestID: ev.ID, Currency: p.Currency, Amount: p.Amount, Total: p.Total,
			TaxInclusive: p.TaxInclusive, SaleDiscount: p.SaleDiscount, ServiceCharge: p.ServiceCharge,
			Reference: p.Reference, Lines: p.Lines,
		})
		if err != nil {
			declined(err)
		}
		approve(evidence)
	case strings.HasSuffix(ev.Type, ".refund"):
		p := ev.Payload
		evidence, err := driver.Refund(okc.RefundRequest{
			RequestID: ev.ID, Currency: p.Currency, Amount: p.Amount,
			OriginalSaleID: p.OriginalSaleID, OriginalReceipt: p.OriginalReceipt,
		})
		if err != nil {
			declined(err)
		}
		approve(evidence)
	default:
		// .requested (settle) and anything else: the device already holds
		// the record; nothing to do, and never a decline.
		os.Exit(0)
	}
}

func approve(ev okc.Evidence) {
	out, _ := json.Marshal(answer{Status: "approved", FiscalDevice: ev})
	os.Stdout.Write(out)
	os.Exit(0)
}

// declined logs the real reason (operator log, never the customer screen —
// core shows its own localized decline toast) and exits non-zero, which
// refuses the tender and keeps the basket.
func declined(err error) {
	switch {
	case errors.Is(err, okc.ErrSplitTender):
		logf("okc: tender refused — %v", err)
	case errors.Is(err, okc.ErrDriverNotImplemented), errors.Is(err, okc.ErrUnknownDriver):
		logf("okc: tender refused — plugin not ready for this device: %v", err)
	default:
		logf("okc: tender refused — %v", err)
	}
	fmt.Fprintln(os.Stderr, err.Error())
	os.Exit(1)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
