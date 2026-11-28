package cloudsync

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"time"
	"unicode"
	"unicode/utf8"

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
	// delivery only (ADR-0215): checked against the delivery object below
	"rejected": true, "preparing": true, "ready": true, "collected": true,
}

// webshopOnlyStatuses and deliveryOnlyStatuses are the chain steps that exist
// on one kind of order only.
var (
	webshopOnlyStatuses   = map[string]bool{"picked": true, "packed": true, "shipped": true}
	deliveryOnlyStatuses  = map[string]bool{"rejected": true, "preparing": true, "ready": true, "collected": true}
	channelDeliveryFields = map[string]bool{
		"service": true, "accept_by": true, "scheduled_start_at": true, "confirm_at": true, "prep_minutes": true,
		"courier_ref": true, "pickup_code": true, "tip_minor": true, "fees": true,
	}
	channelFeeFields      = map[string]bool{"kind": true, "amount_minor": true}
	channelModifierFields = map[string]bool{"external_id": true, "name": true, "qty": true, "price_delta_minor": true}
)

const (
	maxChannelNoteBytes   = 500
	maxChannelPrepMinutes = 1440
	maxChannelFees        = 20
	maxChannelModifiers   = 50
)

// channelNoteText reports whether s is a bounded, printable customer note:
// valid UTF-8, at most 500 bytes, no control character other than newline and
// tab, no bidi override or zero-width format character (so a note can neither
// scramble the screen nor hide text). The note is only shown and printed,
// never logged.
func channelNoteText(s string) bool {
	if len(s) == 0 || len(s) > maxChannelNoteBytes || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
		case r <= 0x1f, r >= 0x7f && r <= 0x9f, r == 0xad:
			return false
		case r >= 0x200b && r <= 0x200f, r >= 0x2028 && r <= 0x202e, r >= 0x2060 && r <= 0x2069, r == 0xfeff:
			return false
		case unicode.Is(unicode.Cf, r):
			return false
		}
	}
	return true
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
	"delivery":         true, "note": true,
}

var channelLineFields = map[string]bool{
	"external_id": true, "cloud_item_id": true, "cloud_variant_id": true, "sku": true, "name": true, "qty": true,
	"unit_price_minor": true, "tax_bp": true, "tax_included": true, "discount_line_minor": true,
	"refunded_quantity": true, "restocked_quantity": true,
	"modifiers": true, "note": true,
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
	if msg := decodeChannelDelivery(d.Payload, in); msg != "" {
		return out, msg
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
		if in.Delivery != nil {
			if msg := decodeChannelLineDelivery(lm, &l); msg != "" {
				return out, "line: " + msg
			}
		} else if _, present := lm["modifiers"]; present {
			return out, "line: modifiers need a delivery order"
		} else if _, present := lm["note"]; present {
			return out, "line: note needs a delivery order"
		}
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

// decodeChannelDelivery reads the optional `delivery` object and `note`
// (ADR-0215 decision 9). The object is closed (unknown keys refused), so no
// customer name, phone, address or e-mail can ride in; the order `note` is
// only valid on a delivery order. It also checks the status against the
// order's kind. A non-empty result is the refusal text (never a value).
func decodeChannelDelivery(p map[string]any, in *data.ChannelOrderInput) string {
	raw, present := p["delivery"]
	if !present {
		if _, hasNote := p["note"]; hasNote {
			return "note needs a delivery order"
		}
		if deliveryOnlyStatuses[in.Status] {
			return "status needs a delivery order"
		}
		return ""
	}
	dm, ok := raw.(map[string]any)
	if !ok {
		return "delivery: not an object"
	}
	if k := unknownKey(dm, channelDeliveryFields); k != "" {
		return "delivery has an unknown field: " + k
	}
	if webshopOnlyStatuses[in.Status] {
		return "status is not valid on a delivery order"
	}
	r := channelReader{p: dm}
	d := &data.ChannelDeliveryInput{}
	d.Service = r.str("service", 1, 16)
	if r.err == "" && d.Service != "delivery" && d.Service != "collection" {
		r.err = "service: invalid"
	}
	for _, f := range []struct {
		k   string
		dst *string
	}{{"accept_by", &d.AcceptBy}, {"scheduled_start_at", &d.ScheduledStartAt}, {"confirm_at", &d.ConfirmAt}} {
		if _, has := dm[f.k]; !has {
			continue
		}
		v := r.str(f.k, 1, 64)
		if r.err == "" {
			if _, err := time.Parse(time.RFC3339, v); err != nil || !channelPlacedAtRe.MatchString(v) {
				r.fail(f.k, "invalid time")
			}
		}
		*f.dst = v
	}
	if _, has := dm["prep_minutes"]; has {
		d.PrepMinutes = r.minor("prep_minutes", 0)
		if r.err == "" && d.PrepMinutes > maxChannelPrepMinutes {
			r.fail("prep_minutes", "too large")
		}
	}
	d.CourierRef = r.optStr("courier_ref", 64)
	d.PickupCode = r.optStr("pickup_code", 32)
	if _, has := dm["tip_minor"]; has {
		d.Tip = money.FromMinor(r.minor("tip_minor", 0))
	}
	if r.err != "" {
		return "delivery: " + r.err
	}
	if rawFees, has := dm["fees"]; has {
		fees, ok := rawFees.([]any)
		if !ok || len(fees) > maxChannelFees {
			return "delivery: fees invalid"
		}
		for _, rf := range fees {
			fm, ok := rf.(map[string]any)
			if !ok {
				return "delivery: fee invalid"
			}
			if k := unknownKey(fm, channelFeeFields); k != "" {
				return "delivery fee has an unknown field: " + k
			}
			fr := channelReader{p: fm}
			fee := data.ChannelDeliveryFee{Kind: fr.str("kind", 1, 64), Amount: money.FromMinor(fr.minor("amount_minor", 0))}
			if fr.err != "" {
				return "delivery fee: " + fr.err
			}
			d.Fees = append(d.Fees, fee)
		}
	}
	if rawNote, has := p["note"]; has {
		s, ok := rawNote.(string)
		if !ok || !channelNoteText(s) {
			return "note: invalid"
		}
		in.Note = s
	}
	in.Delivery = d
	return ""
}

// decodeChannelLineDelivery reads a delivery line's `note` and `modifiers`.
func decodeChannelLineDelivery(lm map[string]any, l *data.ChannelOrderLineInput) string {
	if rawNote, has := lm["note"]; has {
		s, ok := rawNote.(string)
		if !ok || !channelNoteText(s) {
			return "note: invalid"
		}
		l.Note = s
	}
	rawMods, has := lm["modifiers"]
	if !has {
		return ""
	}
	mods, ok := rawMods.([]any)
	if !ok || len(mods) > maxChannelModifiers {
		return "modifiers invalid"
	}
	for _, rm := range mods {
		mm, ok := rm.(map[string]any)
		if !ok {
			return "modifier invalid"
		}
		if k := unknownKey(mm, channelModifierFields); k != "" {
			return "modifier has an unknown field: " + k
		}
		mr := channelReader{p: mm}
		m := data.ChannelLineModifier{
			ExternalID: mr.str("external_id", 1, 200), Name: mr.str("name", 1, 256),
			Qty: mr.minor("qty", 1), PriceDelta: money.FromMinor(mr.minor("price_delta_minor", -maxChannelMinor)),
		}
		if mr.err != "" {
			return "modifier: " + mr.err
		}
		l.Modifiers = append(l.Modifiers, m)
	}
	return ""
}
