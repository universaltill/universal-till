package cloudsync

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// channel_order / channel_order_update (ADR-0169 §9, ut-docs#4768): the cloud
// channel host's order for the main till to record. Payload shape and
// semantics: ut-docs reference/contracts/channel-order-v1.json and
// sales-channel-v1.md §5.1 (testdata/channel_order_v1.json is the example).
//
// The payload is untrusted: decoded strictly (unknown fields refused, so no
// customer name/e-mail/phone/address can ride in), size-capped, money only as
// whole non-negative minor units. A refusal text never echoes a value.

// ChannelOrderDirective is a decoded channel_order(_update). The hook fills in
// the stock location, the audit actor and the currency check. Its
// TotalMismatch (promoted from the input) is set when total_minor disagrees
// with the contract's formula: the order is still recorded, the hook raises a
// Problem and the repo stores the flag.
type ChannelOrderDirective struct {
	data.ChannelOrderInput
}

// channelOrderSchemaVersion is the channel-order-v1.json schema_version this
// till applies.
const channelOrderSchemaVersion = 1

const (
	maxChannelOrderLines = 500
	maxChannelOrderBytes = 64 << 10
	maxChannelBP         = 10000
	maxChannelMinor      = 1 << 50 // far above any real order, far below float64's integer limit
)

var (
	channelUUIDRe     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	channelPlacedAtRe = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?Z$`)
	channelCurrencyRe = regexp.MustCompile(`^[A-Z]{3}$`)
	channelCountryRe  = regexp.MustCompile(`^[A-Z]{2}$`)
)

var channelOrderStatuses = map[string]bool{
	"new": true, "accepted": true, "picked": true, "packed": true, "shipped": true,
	"cancelled": true, "refunded": true, "partially_refunded": true,
}

var channelPaymentStates = map[string]bool{
	"pending": true, "authorized": true, "paid": true, "partially_refunded": true, "refunded": true, "voided": true,
}

var channelOrderFields = map[string]bool{
	"schema_version": true, "connection_id": true, "plugin_key": true, "external_order_id": true, "external_version": true,
	"revision": true, "order_number": true, "placed_at": true, "currency": true, "status": true, "payment_state": true,
	"lines": true, "shipping_minor": true, "shipping_tax_bp": true, "shipping_tax_included": true,
	"order_discount_minor": true, "tax_added_minor": true, "refund_total_minor": true, "total_minor": true,
	"customer_country": true,
}

var channelLineFields = map[string]bool{
	"external_id": true, "cloud_item_id": true, "cloud_variant_id": true, "sku": true, "name": true, "qty": true,
	"unit_price_minor": true, "tax_bp": true, "tax_included": true, "discount_line_minor": true,
	"refunded_quantity": true, "restocked_quantity": true,
}

// decodeChannelOrder reads the payload. A non-empty msg is the failure text;
// it never contains a payload value.
func decodeChannelOrder(d directive) (ChannelOrderDirective, string) {
	var out ChannelOrderDirective
	if raw, err := json.Marshal(d.Payload); err != nil || len(raw) > maxChannelOrderBytes {
		return out, "channel order payload too large"
	}
	if k := unknownKey(d.Payload, channelOrderFields); k != "" {
		return out, "channel order has an unknown field: " + k
	}
	r := channelReader{p: d.Payload}
	in := &out.ChannelOrderInput
	// The directive's identity is the envelope id, never the payload
	// (sales-channel-v1.md §5.1); an unknown schema_version is refused
	// visibly, never guessed at.
	if v, ok := d.Payload["schema_version"].(float64); !ok {
		return out, "missing schema_version"
	} else if v != channelOrderSchemaVersion {
		return out, fmt.Sprintf("unsupported channel order schema_version %g (this till supports %d)", v, channelOrderSchemaVersion)
	}
	in.DirectiveID = d.ID
	in.ConnectionID = r.uuid("connection_id")
	in.PluginKey = r.str("plugin_key", 1, 200)
	in.ExternalOrderID = r.str("external_order_id", 1, 200)
	in.ExternalVersion = r.optStr("external_version", 200)
	in.Revision = r.minor("revision", 1)
	in.OrderNumber = r.str("order_number", 1, 64)
	in.PlacedAt = r.str("placed_at", 1, 64)
	if r.err == "" {
		if _, err := time.Parse(time.RFC3339, in.PlacedAt); err != nil || !channelPlacedAtRe.MatchString(in.PlacedAt) {
			r.err = "invalid placed_at"
		}
	}
	in.Currency = r.pattern("currency", channelCurrencyRe)
	in.Status = r.str("status", 1, 40)
	if r.err == "" && !channelOrderStatuses[in.Status] {
		r.err = "invalid status"
	}
	in.PaymentState = r.optStr("payment_state", 40)
	if r.err == "" && in.PaymentState != "" && !channelPaymentStates[in.PaymentState] {
		r.err = "invalid payment_state"
	}
	in.Shipping = money.FromMinor(r.minor("shipping_minor", 0))
	in.ShippingTaxBP = r.bp("shipping_tax_bp")
	in.ShippingTaxIncluded = r.boolean("shipping_tax_included")
	in.OrderDiscount = money.FromMinor(r.minor("order_discount_minor", 0))
	in.TaxAdded = money.FromMinor(r.minor("tax_added_minor", 0))
	in.RefundTotal = money.FromMinor(r.minor("refund_total_minor", 0))
	in.Total = money.FromMinor(r.minor("total_minor", 0))
	if _, present := d.Payload["customer_country"]; present {
		in.CustomerCountry = r.pattern("customer_country", channelCountryRe)
	}
	if r.err != "" {
		return out, r.err
	}
	if in.RefundTotal > in.Total {
		return out, "refund_total_minor exceeds total_minor"
	}

	rawLines, ok := d.Payload["lines"].([]any)
	if !ok || len(rawLines) == 0 {
		return out, "missing lines"
	}
	if len(rawLines) > maxChannelOrderLines {
		return out, "too many lines"
	}
	sum := money.Money(0)
	seen := make(map[string]bool, len(rawLines))
	for _, rl := range rawLines {
		lm, ok := rl.(map[string]any)
		if !ok {
			return out, "invalid line"
		}
		if k := unknownKey(lm, channelLineFields); k != "" {
			return out, "line has an unknown field: " + k
		}
		lr := channelReader{p: lm}
		l := data.ChannelOrderLineInput{
			LineID:            lr.str("external_id", 1, 200),
			CloudItemID:       lr.optStr("cloud_item_id", 200),
			CloudVariantID:    lr.optStr("cloud_variant_id", 200),
			SKU:               lr.optStr("sku", 200),
			Name:              lr.str("name", 1, 500),
			Qty:               lr.minor("qty", 1),
			UnitPrice:         money.FromMinor(lr.minor("unit_price_minor", 0)),
			TaxBP:             lr.bp("tax_bp"),
			TaxIncluded:       lr.boolean("tax_included"),
			Discount:          money.FromMinor(lr.minor("discount_line_minor", 0)),
			RefundedQuantity:  lr.minor("refunded_quantity", 0),
			RestockedQuantity: lr.minor("restocked_quantity", 0),
		}
		if _, present := lm["sku"]; !present && lr.err == "" {
			lr.err = "missing sku"
		}
		if lr.err == "" && l.RefundedQuantity > l.Qty {
			lr.err = "refunded_quantity exceeds qty"
		}
		if lr.err == "" && l.RestockedQuantity > l.RefundedQuantity {
			lr.err = "restocked_quantity exceeds refunded_quantity"
		}
		if lr.err == "" && l.Discount > l.UnitPrice.Scale(l.Qty) {
			lr.err = "discount_line_minor exceeds the line"
		}
		for _, k := range []string{"cloud_item_id", "cloud_variant_id"} {
			if v, present := lm[k]; present && v == "" && lr.err == "" {
				lr.err = k + " is empty"
			}
		}
		if lr.err == "" && seen[l.LineID] {
			lr.err = "duplicate line external_id"
		}
		seen[l.LineID] = true
		if lr.err != "" {
			return out, "line: " + lr.err
		}
		sum = sum.Add(l.UnitPrice.Scale(l.Qty)).Sub(l.Discount)
		in.Lines = append(in.Lines, l)
	}
	want := sum.Add(in.Shipping).Sub(in.OrderDiscount).Add(in.TaxAdded)
	out.TotalMismatch = want != in.Total
	return out, ""
}

func unknownKey(m map[string]any, allowed map[string]bool) string {
	var bad []string
	for k := range m {
		if !allowed[k] {
			bad = append(bad, k)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	sort.Strings(bad)
	return bad[0]
}

// channelReader reads typed fields and keeps the first failure.
type channelReader struct {
	p   map[string]any
	err string
}

func (r *channelReader) fail(k, why string) {
	if r.err == "" {
		r.err = fmt.Sprintf("%s: %s", k, why)
	}
}

func (r *channelReader) str(k string, minLen, maxLen int) string {
	s, ok := r.p[k].(string)
	if !ok || len(s) < minLen || len(s) > maxLen {
		r.fail(k, "missing or invalid")
		return ""
	}
	return s
}

func (r *channelReader) optStr(k string, maxLen int) string {
	v, present := r.p[k]
	if !present {
		return ""
	}
	s, ok := v.(string)
	if !ok || len(s) > maxLen {
		r.fail(k, "invalid")
		return ""
	}
	return s
}

func (r *channelReader) uuid(k string) string {
	s := r.str(k, 1, 64)
	if s != "" && !channelUUIDRe.MatchString(s) {
		r.fail(k, "not a uuid")
		return ""
	}
	return s
}

func (r *channelReader) pattern(k string, re *regexp.Regexp) string {
	s := r.str(k, 1, 64)
	if s != "" && !re.MatchString(s) {
		r.fail(k, "invalid")
		return ""
	}
	return s
}

func (r *channelReader) boolean(k string) bool {
	b, ok := r.p[k].(bool)
	if !ok {
		r.fail(k, "missing or invalid")
	}
	return b
}

// bp reads basis points, 0 to 10000.
func (r *channelReader) bp(k string) int64 {
	v := r.minor(k, 0)
	if v > maxChannelBP {
		r.fail(k, "above 100 percent")
		return 0
	}
	return v
}

// minor reads a whole number of at least min (and far below overflow).
func (r *channelReader) minor(k string, min int64) int64 {
	f, ok := r.p[k].(float64)
	if !ok || f != math.Trunc(f) || f < float64(min) || f > maxChannelMinor {
		r.fail(k, "missing or invalid")
		return 0
	}
	return int64(f)
}
