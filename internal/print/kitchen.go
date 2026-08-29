package print

import (
	"bytes"
	"strings"
	"unicode/utf8"
)

// KitchenTicket is one kitchen-facing order ticket (docs:
// arch/restaurant-phone-orders.md). It carries what a cook needs and nothing
// else: item names print large so they read across the line, quantities and
// modifiers/notes sit with each item, and there are NO prices — the kitchen
// cooks food, it doesn't handle money. Like Doc, the model is plain strings:
// callers translate labels and format the timestamp; this stays byte-testable.
type KitchenTicket struct {
	// Station is an optional header ("KITCHEN", "BAR", "GRILL") identifying
	// the printer/station this ticket routes to.
	Station string
	// OrderNo is the receipt/order number the ticket belongs to.
	OrderNo string
	// OrderLabel is the pre-translated word printed before OrderNo ("ORDER"
	// in English). Optional — defaults to "ORDER" at render time if the
	// caller doesn't set it, so existing callers/tests that don't care
	// about i18n keep working unchanged. Same "callers translate labels"
	// convention as OrderType/Timestamp below.
	OrderLabel string
	// OrderType is the pre-translated, display-ready order-type text (e.g.
	// "Takeaway" in the shop's configured locale) — dine-in / takeaway /
	// delivery / phone, printed upper-cased at render time. Empty for
	// dine-in (optional; prints nothing, a deliberate product decision —
	// ut-docs#261 — not a missing translation). Callers translate before
	// assigning; this package has no i18n dependency of its own and stays
	// byte-testable.
	OrderType string
	// Table is a table or tab label (optional).
	Table string
	// Timestamp is the pre-formatted time the order was placed.
	Timestamp string
	Items     []KitchenItem
	Charset   string // "utf8" (default), "ascii", or "cp858", as Doc.Charset
}

// KitchenItem is one line of the order: a quantity, a name, and any
// modifiers/notes ("no onions", "extra hot", allergen flags) printed under it.
type KitchenItem struct {
	Qty       string // pre-formatted ("2")
	Name      string
	Modifiers []string
}

// orderLabel returns t.OrderLabel, or "ORDER" if the caller left it unset —
// keeps existing/i18n-agnostic callers producing legible output.
func orderLabel(t KitchenTicket) string {
	if s := strings.TrimSpace(t.OrderLabel); s != "" {
		return s
	}
	return "ORDER"
}

// RenderKitchenTicket produces the ESC/POS byte stream for a kitchen ticket:
// a bold centred station/order header, each item in double-width type with its
// modifiers indented beneath, then a cut. No barcode, no totals, no drawer.
func RenderKitchenTicket(t KitchenTicket) []byte {
	var b bytes.Buffer
	enc := func(s string) []byte { return encodeText(s, t.Charset) }
	line := func(s string) {
		b.Write(enc(s))
		b.WriteByte('\n')
	}

	b.Write(cmdInit)
	if cmd := codepageSelectCmd(t.Charset); cmd != nil {
		b.Write(cmd) // once per ticket, before any text (ut-docs#1243)
	}

	b.Write(cmdAlignMid)
	b.Write(cmdBoldOn)
	if s := strings.TrimSpace(t.Station); s != "" {
		b.Write(cmdDoubleOn)
		line(clip(s, Width/2))
		b.Write(cmdDoubleOff)
	}
	if s := strings.TrimSpace(t.OrderNo); s != "" {
		line(clip(orderLabel(t)+" "+s, Width))
	}
	b.Write(cmdBoldOff)
	if s := strings.TrimSpace(t.OrderType); s != "" {
		// strings.ToUpper isn't Turkish-locale-aware (dotless/dotted I) —
		// a pre-existing cosmetic quirk, more reachable now that this can
		// carry real translated text, not just English. Not solved here.
		line(clip(strings.ToUpper(s), Width))
	}
	if s := strings.TrimSpace(t.Table); s != "" {
		line(clip(s, Width))
	}
	if s := strings.TrimSpace(t.Timestamp); s != "" {
		line(clip(s, Width))
	}
	b.Write(cmdAlignLeft)
	line(strings.Repeat("-", Width))

	for _, it := range t.Items {
		b.Write(cmdDoubleOn)
		label := strings.TrimSpace(it.Name)
		if q := strings.TrimSpace(it.Qty); q != "" && q != "1" {
			label = q + " x " + label
		}
		line(clip(label, Width/2)) // double-width halves the columns
		b.Write(cmdDoubleOff)
		for _, m := range it.Modifiers {
			if m = strings.TrimSpace(m); m != "" {
				line(clip("  - "+m, Width))
			}
		}
	}
	line(strings.Repeat("-", Width))

	b.Write(cmdFeedCut)
	return b.Bytes()
}

// RenderKitchenTicketText lays a kitchen ticket out as plain text — the same
// content as RenderKitchenTicket for on-screen preview (no ESC/POS control
// bytes), mirroring RenderText for receipts.
func RenderKitchenTicketText(t KitchenTicket) string {
	var b strings.Builder
	center := func(s string) {
		s = clip(s, Width)
		pad := (Width - utf8.RuneCountInString(s)) / 2
		if pad < 0 {
			pad = 0
		}
		b.WriteString(strings.Repeat(" ", pad) + s + "\n")
	}
	if s := strings.TrimSpace(t.Station); s != "" {
		center(s)
	}
	if s := strings.TrimSpace(t.OrderNo); s != "" {
		center(orderLabel(t) + " " + s)
	}
	if s := strings.TrimSpace(t.OrderType); s != "" {
		center(strings.ToUpper(s))
	}
	if s := strings.TrimSpace(t.Table); s != "" {
		center(s)
	}
	if s := strings.TrimSpace(t.Timestamp); s != "" {
		center(s)
	}
	b.WriteString(strings.Repeat("-", Width) + "\n")
	for _, it := range t.Items {
		label := strings.TrimSpace(it.Name)
		if q := strings.TrimSpace(it.Qty); q != "" && q != "1" {
			label = q + " x " + label
		}
		b.WriteString(clip(label, Width) + "\n")
		for _, m := range it.Modifiers {
			if m = strings.TrimSpace(m); m != "" {
				b.WriteString(clip("  - "+m, Width) + "\n")
			}
		}
	}
	b.WriteString(strings.Repeat("-", Width) + "\n")
	return b.String()
}
