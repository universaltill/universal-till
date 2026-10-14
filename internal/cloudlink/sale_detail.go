package cloudlink

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/fleetlink"
)

// One live sale's detail on request (ADR-0155, ut-docs#4150): the cloud
// asks for a sale by id when a my. viewer opens it, and the till answers
// from its own database. Nothing is stored in the cloud; the till never
// acts on a request beyond this one read.
const (
	typeSaleDetailRequest = "sale_detail_request" // cloud → till: {sale_id}
	typeSaleDetail        = "sale_detail"         // till → cloud: the reply, reply_to the request
	capSaleDetail         = "sale_detail"         // the hello's caps entry (§2)
)

// The handler's own error codes (§1); fleetlink answers busy, timeout,
// internal, unknown_type and bad_message itself.
const (
	codeNotFound    = "not_found"    // no such sale on this till — final
	codeRateLimited = "rate_limited" // over the till's request budget — retryable
	codeTooLarge    = "too_large"    // over the frame budget with no lines left — final
)

// §4's till-side bounds.
const (
	// detailBurst/detailEvery: at most 5 requests/s, burst 10.
	detailBurst = 10
	detailEvery = 200 * time.Millisecond
	// maxSaleDetailFrame is the whole encoded reply envelope's budget,
	// under the cloud's 16 KiB read limit (fleetlink drops a frame over
	// the limit at enqueue, silently, so the trim measures the frame).
	maxSaleDetailFrame = 15 << 10
	// saleDetailIDAllowance is what the trim counts for each of the
	// reply's id and reply_to, which it can't see: fleetlink's ids and
	// the cloud's (ut-cloud tilllink.peer.nextID) are both at most 23
	// bytes ("t"/"c" + 8 hex + "-" + ≤ 13 base36 digits), so this
	// over-counts; a longer cloud id still lands under the 16 KiB limit.
	saleDetailIDAllowance = 64
)

// SaleDetail is the sale_detail payload: ADR-0155 §3's allow-list and
// nothing else. internal/pages maps the till's sale onto it field by field
// — never by marshalling data.SaleDetail, the LAN journal contract, which
// grows fields the cloud must not see. Money is integer minor units in
// Currency; a return's header totals (subtotal, discount, tax, service
// charge, total) are negative, as in the sale summary, while its lines and
// payments stay the positive magnitudes the till stores.
type SaleDetail struct {
	ID                 string `json:"id"`
	ReceiptNo          string `json:"receipt_no"`
	DisplayNo          string `json:"display_no"`
	CreatedAt          string `json:"created_at"` // RFC 3339
	Status             string `json:"status"`
	SaleType           string `json:"sale_type"` // sale | return
	OrderType          string `json:"order_type"`
	TableLabel         string `json:"table_label"`
	Currency           string `json:"currency"`
	SubtotalMinor      int64  `json:"subtotal_minor"`
	DiscountTotalMinor int64  `json:"discount_total_minor"`
	TaxTotalMinor      int64  `json:"tax_total_minor"`
	ServiceChargeMinor int64  `json:"service_charge_minor"`
	TotalMinor         int64  `json:"total_minor"`
	CashierName        string `json:"cashier_name"`
	// Provenance: TillID is the replica a journaled sale came from, "" =
	// the main till itself; the register fields may be empty (a replica's
	// sale keeps no register on the main till).
	TillID       string              `json:"till_id"`
	RegisterID   string              `json:"register_id"`
	RegisterName string              `json:"register_name"`
	Lines        []SaleDetailLine    `json:"lines"`
	Payments     []SaleDetailPayment `json:"payments"`
	// LinesOmitted counts the lines dropped from the end to fit §4's frame
	// budget.
	LinesOmitted int `json:"lines_omitted"`
}

// SaleDetailLine is one line of a SaleDetail.
type SaleDetailLine struct {
	Name              string   `json:"name"`
	SKU               string   `json:"sku"`
	ItemID            string   `json:"item_id"`
	VariantID         string   `json:"variant_id"`
	Qty               float64  `json:"qty"` // decimal: weighed items
	UnitPriceMinor    int64    `json:"unit_price_minor"`
	LineDiscountMinor int64    `json:"line_discount_minor"`
	TaxRateBP         int      `json:"tax_rate_bp"`
	TaxMinor          int64    `json:"tax_minor"`
	LineTotalMinor    int64    `json:"line_total_minor"`
	Modifiers         []string `json:"modifiers"`
	OrderType         string   `json:"order_type"`
}

// SaleDetailPayment is one payment of a SaleDetail.
type SaleDetailPayment struct {
	Method           string `json:"method"`
	AmountMinor      int64  `json:"amount_minor"`
	ChangeGivenMinor int64  `json:"change_given_minor"`
	TipMinor         int64  `json:"tip_minor"`
}

// answerSaleDetail is the sale_detail_request handler. fleetlink runs it on
// its own goroutine under the in-flight cap and RequestTimeout, so the
// link's reader, its pings and every sale go on while it reads.
func (c *Client) answerSaleDetail(ctx context.Context, _ string, raw json.RawMessage) (any, error) {
	// Validated before the budget is spent: a malformed request must not
	// cost a well-formed one its token.
	var req struct {
		SaleID string `json:"sale_id"`
	}
	if json.Unmarshal(raw, &req) != nil || strings.TrimSpace(req.SaleID) == "" {
		return nil, &fleetlink.RemoteError{Code: fleetlink.CodeBadMessage}
	}
	if !c.takeDetailToken(time.Now()) {
		return nil, &fleetlink.RemoteError{Code: codeRateLimited, Retryable: true}
	}
	d, found, err := c.o.SaleDetail(ctx, req.SaleID)
	if err != nil {
		return nil, err // fleetlink: timeout past the deadline, else internal
	}
	if !found {
		return nil, &fleetlink.RemoteError{Code: codeNotFound}
	}
	d, ok := fitSaleDetail(d)
	if !ok {
		return nil, &fleetlink.RemoteError{Code: codeTooLarge}
	}
	return fleetlink.Reply{Type: typeSaleDetail, Payload: d}, nil
}

// fitSaleDetail keeps the longest prefix of d's lines whose reply frame is
// within maxSaleDetailFrame, counting the rest in LinesOmitted; the header
// and payments are never dropped. False when even zero lines is over.
func fitSaleDetail(d SaleDetail) (SaleDetail, bool) {
	all, omitted := d.Lines, d.LinesOmitted
	with := func(k int) SaleDetail {
		out := d
		out.Lines = all[:k:k]
		out.LinesOmitted = omitted + len(all) - k
		return out
	}
	fits := func(k int) bool {
		n, err := saleDetailFrameLen(with(k))
		return err == nil && n <= maxSaleDetailFrame
	}
	if fits(len(all)) {
		return d, true
	}
	if !fits(0) {
		return SaleDetail{}, false
	}
	// The frame grows with every line kept (an empty line encodes to
	// ~170 B; lines_omitted loses at most one digit per line kept): the
	// first k that doesn't fit, less one, is the longest prefix that does.
	k := sort.Search(len(all)+1, func(k int) bool { return !fits(k) }) - 1
	return with(k), true
}

// saleDetailFrameLen is the encoded reply envelope's length, as fleetlink
// would put it on the wire, with saleDetailIDAllowance for each id.
func saleDetailFrameLen(d SaleDetail) (int, error) {
	raw, err := json.Marshal(d)
	if err != nil {
		return 0, err
	}
	id := strings.Repeat("x", saleDetailIDAllowance)
	b, err := fleetlink.Encode(fleetlink.Envelope{ID: id, Type: typeSaleDetail, ReplyTo: id, Payload: raw})
	return len(b), err
}

// takeDetailToken is §4's till-side request budget (takeSaleToken's
// bucket, its own tokens).
func (c *Client) takeDetailToken(now time.Time) bool {
	c.dmu.Lock()
	defer c.dmu.Unlock()
	if !c.detailAt.IsZero() {
		c.detailTokens += float64(now.Sub(c.detailAt)) / float64(detailEvery)
		if c.detailTokens > detailBurst {
			c.detailTokens = detailBurst
		}
	}
	c.detailAt = now
	if c.detailTokens < 1 {
		return false
	}
	c.detailTokens--
	return true
}
