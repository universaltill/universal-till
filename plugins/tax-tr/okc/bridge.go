package okc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// BridgeDriver speaks the Universal Till ÖKC bridge protocol v0: one JSON
// object per line over TCP, one request per connection. It is the reference
// wire format — the simulator (okc/sim) implements the device side, and a
// maker integration that cannot be reached directly from a WASM plugin
// (e.g. one that ships only a Windows DLL or a vendor SDK) can be wrapped by
// a small bridge process on the shop's LAN that translates this protocol to
// the maker's own. It is deliberately tiny so such a bridge is a day's work.
//
// Request  {"op":"sale","request_id":"…","currency":"TRY","amount":1234,
//
//	"total":1234,"tax_inclusive":true,"lines":[{"name":"Çay","qty":2,
//	"unit_price":1500,"tax_rate_bp":1000}]}
//
// Response {"ok":true,"serial":"SIM-0001","maker":"sim","receipt_no":"0000012",
//
//	"receipt_kind":"mali_fis","z_no":3,"issued_at":"2026-09-03T10:00:00+03:00"}
//
// or       {"ok":false,"error":"paper out"}
//
// Other ops: "refund" (same shape, receipt_kind "iade_fisi"), "status",
// "z_close". A request is answered exactly once; a request_id the device
// has already printed for is answered with the original evidence again
// (idempotent), which is what makes a retried authorize safe.
type BridgeDriver struct {
	Transport Transport
	Config    Config
}

// bridgeRequest is the flat wire shape (a sale's and a refund's fields
// side by side, omitted when unused) — deliberately NOT two embedded
// structs: encoding/json silently drops same-named fields that collide at
// one nesting depth, so embedding SaleRequest and RefundRequest together
// would erase request_id/currency/amount from every message.
type bridgeRequest struct {
	Op              string `json:"op"`
	RequestID       string `json:"request_id,omitempty"`
	Currency        string `json:"currency,omitempty"`
	Amount          int64  `json:"amount,omitempty"`
	Total           int64  `json:"total,omitempty"`
	TaxInclusive    bool   `json:"tax_inclusive,omitempty"`
	SaleDiscount    int64  `json:"sale_discount,omitempty"`
	ServiceCharge   int64  `json:"service_charge,omitempty"`
	Reference       string `json:"reference,omitempty"`
	Lines           []Line `json:"lines,omitempty"`
	OriginalSaleID  string `json:"original_sale_id,omitempty"`
	OriginalReceipt string `json:"original_receipt,omitempty"`
}

func saleMessage(req SaleRequest) bridgeRequest {
	return bridgeRequest{Op: "sale", RequestID: req.RequestID, Currency: req.Currency, Amount: req.Amount,
		Total: req.Total, TaxInclusive: req.TaxInclusive, SaleDiscount: req.SaleDiscount,
		ServiceCharge: req.ServiceCharge, Reference: req.Reference, Lines: req.Lines}
}

func refundMessage(req RefundRequest) bridgeRequest {
	return bridgeRequest{Op: "refund", RequestID: req.RequestID, Currency: req.Currency, Amount: req.Amount,
		OriginalSaleID: req.OriginalSaleID, OriginalReceipt: req.OriginalReceipt}
}

// bridgeResponse is likewise flat (Evidence's and Status's serial/maker/z_no
// would collide if embedded).
type bridgeResponse struct {
	OK            bool   `json:"ok"`
	Error         string `json:"error,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Maker         string `json:"maker,omitempty"`
	Serial        string `json:"serial,omitempty"`
	ReceiptNo     string `json:"receipt_no,omitempty"`
	ReceiptKind   string `json:"receipt_kind,omitempty"`
	ZNo           int64  `json:"z_no,omitempty"`
	IssuedAt      string `json:"issued_at,omitempty"`
	ReceiptsToday int64  `json:"receipts_today,omitempty"`
}

func (r bridgeResponse) evidence() Evidence {
	return Evidence{Kind: r.Kind, Maker: r.Maker, Serial: r.Serial, ReceiptNo: r.ReceiptNo,
		ReceiptKind: r.ReceiptKind, ZNo: r.ZNo, IssuedAt: r.IssuedAt}
}

// NewBridgeDriver wires a BridgeDriver against t with cfg (normalized).
func NewBridgeDriver(t Transport, cfg Config) *BridgeDriver {
	return &BridgeDriver{Transport: t, Config: cfg.Normalize()}
}

func (d *BridgeDriver) Sale(req SaleRequest) (Evidence, error) {
	if req.Total != 0 && req.Amount != req.Total {
		return Evidence{}, ErrSplitTender
	}
	if req.Currency == "" {
		req.Currency = "TRY"
	}
	resp, err := d.roundTrip(saleMessage(req))
	if err != nil {
		return Evidence{}, err
	}
	ev := resp.evidence()
	if ev.ReceiptKind == "" {
		ev.ReceiptKind = "mali_fis"
	}
	return normalizeEvidence(ev, d.Config), nil
}

func (d *BridgeDriver) Refund(req RefundRequest) (Evidence, error) {
	if req.Currency == "" {
		req.Currency = "TRY"
	}
	resp, err := d.roundTrip(refundMessage(req))
	if err != nil {
		return Evidence{}, err
	}
	ev := resp.evidence()
	if ev.ReceiptKind == "" {
		ev.ReceiptKind = "iade_fisi"
	}
	return normalizeEvidence(ev, d.Config), nil
}

func (d *BridgeDriver) Status() (Status, error) {
	resp, err := d.roundTrip(bridgeRequest{Op: "status"})
	if err != nil {
		return Status{}, err
	}
	return Status{Serial: resp.Serial, Maker: resp.Maker, ZNo: resp.ZNo, ReceiptsToday: resp.ReceiptsToday, Online: true}, nil
}

// ZClose asks the device to run its daily close (Z raporu). Not on the
// Driver interface: core never drives day-close from the till today; the
// simulator and the status page use it for testing and display.
func (d *BridgeDriver) ZClose() (int64, error) {
	resp, err := d.roundTrip(bridgeRequest{Op: "z_close"})
	if err != nil {
		return 0, err
	}
	return resp.ZNo, nil
}

func (d *BridgeDriver) roundTrip(req bridgeRequest) (bridgeResponse, error) {
	conn, err := d.Transport.Dial(d.Config.Host, d.Config.Port, d.Config.ConnectTimeoutMs)
	if err != nil {
		return bridgeResponse{}, fmt.Errorf("%w: %v", ErrDeviceUnreachable, err)
	}
	defer conn.Close()
	return roundTripOn(conn, req)
}

// roundTripOn is the connection-level exchange, split out so tests can run
// it over an in-memory pipe and the wasm adapter can reuse it verbatim.
func roundTripOn(conn io.ReadWriter, req bridgeRequest) (bridgeResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return bridgeResponse{}, fmt.Errorf("okc: encode request: %w", err)
	}
	body = append(body, '\n')
	if _, err := conn.Write(body); err != nil {
		return bridgeResponse{}, fmt.Errorf("%w: write: %v", ErrDeviceUnreachable, err)
	}
	line, err := bufio.NewReaderSize(conn, 64*1024).ReadBytes('\n')
	if err != nil && len(strings.TrimSpace(string(line))) == 0 {
		return bridgeResponse{}, fmt.Errorf("%w: read: %v", ErrDeviceUnreachable, err)
	}
	var resp bridgeResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return bridgeResponse{}, fmt.Errorf("%w: unparseable answer %q", ErrDeviceUnreachable, strings.TrimSpace(string(line)))
	}
	if !resp.OK {
		msg := resp.Error
		if msg == "" {
			msg = "no reason given"
		}
		return bridgeResponse{}, fmt.Errorf("%w: %s", ErrDeviceDeclined, msg)
	}
	return resp, nil
}

// normalizeEvidence fills the constant fields a maker's answer may omit.
func normalizeEvidence(ev Evidence, cfg Config) Evidence {
	if ev.Kind == "" {
		ev.Kind = "okc"
	}
	if ev.Maker == "" {
		ev.Maker = cfg.Maker
	}
	return ev
}
