// Package okc is the device-side logic of ut-plugin-tax-tr, the Turkish
// fiscal-device (YN ÖKC) payment plugin: what to send a cash register, what
// it answers, and how that answer becomes the `fiscal_device` evidence the
// till persists (internal/fiscal.DeviceEvidence).
//
// It is plain Go with no build tags so it compiles and tests on the host;
// the wasip1 entrypoint (plugins/tax-tr/main.go) only adapts the till's
// tcp_* host functions to the Transport interface below and reads plugin
// settings. Every maker-specific wire format lives behind Driver, so adding
// a maker never touches the entrypoint or core.
package okc

import (
	"errors"
	"io"
)

// Line is one basket line as the till sends it on payment.<key>.authorize
// (additive fields, ut-plugin-tax-tr contract v0 — see README). Amounts are
// integer minor units (kuruş); qty is a decimal for weighed items.
type Line struct {
	Name         string  `json:"name"`
	Qty          float64 `json:"qty"`
	UnitPrice    int64   `json:"unit_price"`
	TaxRateBP    int     `json:"tax_rate_bp"`
	LineDiscount int64   `json:"line_discount,omitempty"`
}

// SaleRequest is what a Driver needs to have the device take a payment and
// print the fiscal receipt (mali fiş) for a sale.
type SaleRequest struct {
	// RequestID is the till's event id — the device-side idempotency key so
	// a retried authorize never prints twice.
	RequestID     string `json:"request_id"`
	Currency      string `json:"currency"`
	Amount        int64  `json:"amount"`
	Total         int64  `json:"total"`
	TaxInclusive  bool   `json:"tax_inclusive"`
	SaleDiscount  int64  `json:"sale_discount,omitempty"`
	ServiceCharge int64  `json:"service_charge,omitempty"`
	Reference     string `json:"reference,omitempty"`
	Lines         []Line `json:"lines"`
}

// RefundRequest asks the device to print a refund slip (iade fişi) and, for
// a card tender, reverse the money on its own reader.
type RefundRequest struct {
	RequestID       string `json:"request_id"`
	Currency        string `json:"currency"`
	Amount          int64  `json:"amount"`
	OriginalSaleID  string `json:"original_sale_id,omitempty"`
	OriginalReceipt string `json:"original_receipt,omitempty"`
}

// Evidence is what the device reports back once it has printed. Field
// names match internal/fiscal.DeviceEvidence's JSON exactly, so main.go can
// embed it under "fiscal_device" without translation.
type Evidence struct {
	Kind        string `json:"kind"`
	Maker       string `json:"maker,omitempty"`
	Serial      string `json:"serial,omitempty"`
	ReceiptNo   string `json:"receipt_no"`
	ReceiptKind string `json:"receipt_kind,omitempty"`
	ZNo         int64  `json:"z_no,omitempty"`
	IssuedAt    string `json:"issued_at,omitempty"`
}

// Status is a device health probe's answer.
type Status struct {
	Serial        string `json:"serial"`
	Maker         string `json:"maker,omitempty"`
	ZNo           int64  `json:"z_no"`
	ReceiptsToday int64  `json:"receipts_today"`
	Online        bool   `json:"online"`
}

// Transport dials the device. The wasip1 entrypoint implements it over the
// till's tcp_open/tcp_read/tcp_write/tcp_close host functions; tests use a
// real loopback socket.
type Transport interface {
	Dial(host string, port int, connectTimeoutMs int) (io.ReadWriteCloser, error)
}

// Driver is one maker's wire protocol. Sale and Refund return only once the
// device has printed (or refused): the till's tender is blocked on this
// call, which is the whole point — no receipt, no sale.
type Driver interface {
	Sale(req SaleRequest) (Evidence, error)
	Refund(req RefundRequest) (Evidence, error)
	Status() (Status, error)
}

// Config is the plugin's settings block (plugin_settings, read via the
// settings_get host function). Zero values fall back to defaults in
// Normalize.
type Config struct {
	Driver           string
	Host             string
	Port             int
	ConnectTimeoutMs int
	ReadTimeoutMs    int
	Maker            string
}

// Normalize fills defaults: bridge driver, localhost, port 4711, 3 s
// connect, 8 s read. Read is generous because the device waits for the
// customer to present a card — but it must stay under the till's own
// authorize deadline for tcp: plugins, which is the hard ceiling.
func (c Config) Normalize() Config {
	if c.Driver == "" {
		c.Driver = "bridge"
	}
	if c.Host == "" {
		c.Host = "127.0.0.1"
	}
	if c.Port <= 0 || c.Port > 65535 {
		c.Port = 4711
	}
	if c.ConnectTimeoutMs <= 0 {
		c.ConnectTimeoutMs = 3000
	}
	if c.ReadTimeoutMs <= 0 {
		c.ReadTimeoutMs = 8000
	}
	return c
}

var (
	// ErrDeviceDeclined: the device answered and refused (customer
	// cancelled on the reader, paper out, device in Z-close, …).
	ErrDeviceDeclined = errors.New("okc: device declined")
	// ErrDeviceUnreachable: no answer at all — the till must NOT complete
	// the sale (fail closed), but the basket is kept for a retry.
	ErrDeviceUnreachable = errors.New("okc: device unreachable")
	// ErrSplitTender: the device prints one fiscal receipt for the whole
	// sale, so the ÖKC method must be the sale's only tender.
	ErrSplitTender = errors.New("okc: the fiscal device must take the whole sale amount (split tender not supported)")
	// ErrDriverNotImplemented: a maker driver whose wire format has not been
	// filled in yet (needs the maker's integrator documentation and a test
	// device — docs/arch/turkey-launch-playbook.md step 3/4).
	ErrDriverNotImplemented = errors.New("okc: this device driver is not implemented yet")
	// ErrUnknownDriver: okc.driver names no driver this build knows.
	ErrUnknownDriver = errors.New("okc: unknown driver")
)
