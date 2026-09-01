package pos

import (
	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// SnapshotLine is a BasketLine with every field serialized — BasketLine hides
// ItemID/VariantID/TaxRateBP/IsWeighed from the wire (json:"-"), but a held
// sale must restore them so pricing and completion behave identically.
type SnapshotLine struct {
	LineKey      string      `json:"line_key,omitempty"`
	SKU          string      `json:"sku"`
	Name         string      `json:"name"`
	Qty          float64     `json:"qty"`
	PriceCents   money.Money `json:"price_cents"`
	LineDiscount money.Money `json:"line_discount,omitempty"`
	ImageURL     string      `json:"image_url,omitempty"`
	ItemID       string      `json:"item_id,omitempty"`
	VariantID    string      `json:"variant_id,omitempty"`
	TaxRateBP    int         `json:"tax_rate_bp,omitempty"`
	IsWeighed    bool        `json:"is_weighed,omitempty"`
	// TaxCodeID (ut-docs#1381): BasketLine's own copy is json:"-" (added in
	// ut-docs#1351, after this struct was written — never added here). A
	// dine-in/takeaway tax-rate plugin (TaxRateAsker) keys its override by
	// TaxCodeID, not TaxRateBP, so a restored line with this dropped falls
	// back to its own flat configured rate — silently losing any per-line
	// order-type VAT switch on resume regardless of whether OrderType
	// itself (below) round-trips correctly.
	TaxCodeID string                  `json:"tax_code_id,omitempty"`
	Modifiers []data.SelectedModifier `json:"modifiers,omitempty"` // ADR-0020: a held sale must not silently drop customizations on recall
	// QtyFromCode/NoMerge (ADR-0059 §3, ut-docs#934) must survive a
	// hold/resume cycle for the same reason as the fields above: dropping
	// NoMerge would let a later plain scan of the same item merge INTO a
	// restored price-embedded line, overwriting its absolute label price —
	// exactly the money bug the flag exists to prevent.
	QtyFromCode bool `json:"qty_from_code,omitempty"`
	NoMerge     bool `json:"no_merge,omitempty"`
	// OrderType (ut-docs#1181, ADR-0073) is this line's own consumption
	// mode. omitempty: absent on every pre-ADR-0073 payload, where Restore
	// falls back to the header (a legacy "takeaway" header meant EVERY line
	// was takeaway).
	OrderType string `json:"order_type,omitempty"`
}

// BasketSnapshot captures the full in-progress sale state for hold/resume.
type BasketSnapshot struct {
	Lines             []SnapshotLine `json:"lines"`
	DiscountType      string         `json:"discount_type,omitempty"`
	DiscountValue     money.Money    `json:"discount_value,omitempty"`
	DiscountPercentBP int64          `json:"discount_percent_bp,omitempty"`
	CustomerID        string         `json:"customer_id,omitempty"`
	CustomerName      string         `json:"customer_name,omitempty"`
	// OrderType (ut-docs#1381) carries the sale's dine-in/takeaway choice
	// through a hold/resume cycle. Without it, Restore's resetLocked() call
	// zeroed orderType back to "" (dine-in) on every resume, silently
	// changing a resumed takeaway sale's VAT basis (EffectiveLineTaxRateBP/
	// recomputeTotals both key off orderType, §12 UStG) — a compliance bug,
	// not just a display one. Same convention as TableID/TableLabel below.
	// Since ADR-0073 it is the derived SUMMARY (SummarizeOrderType) --
	// "", "takeaway" or "mixed" -- and the per-line truth rides on each
	// SnapshotLine.OrderType. A legacy payload (no line values) with a
	// "takeaway" header restores every line as takeaway; "mixed" is never
	// applied to a line.
	OrderType string `json:"order_type,omitempty"`
	// TableID/TableLabel (ut-docs#820) carry the assigned dining table
	// through a hold/resume cycle, same convention as CustomerID/Name.
	TableID    string      `json:"table_id,omitempty"`
	TableLabel string      `json:"table_label,omitempty"`
	Total      money.Money `json:"total"`
}

// HasItems reports whether the current basket has any lines.
func (s *Service) HasItems() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lines) > 0
}

// Snapshot captures the current basket so it can be held and later restored.
func (s *Service) Snapshot() BasketSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recomputeTotals()
	snap := BasketSnapshot{
		DiscountType:      s.discountType,
		DiscountValue:     s.discountValue,
		DiscountPercentBP: s.discountPercentBP,
		CustomerID:        s.customerID,
		CustomerName:      s.customerName,
		OrderType:         SummarizeOrderType(s.lines, s.orderType),
		TableID:           s.tableID,
		TableLabel:        s.tableLabel,
		Total:             s.basket.Total,
	}
	for _, l := range s.lines {
		snap.Lines = append(snap.Lines, SnapshotLine{
			LineKey: l.LineKey,
			SKU:     l.SKU, Name: l.Name, Qty: l.Qty,
			PriceCents: l.PriceCents, LineDiscount: l.LineDiscount,
			ImageURL: l.ImageURL, ItemID: l.ItemID, VariantID: l.VariantID,
			TaxRateBP: l.TaxRateBP, IsWeighed: l.IsWeighed,
			TaxCodeID:   l.TaxCodeID,
			Modifiers:   l.Modifiers,
			QtyFromCode: l.QtyFromCode, NoMerge: l.NoMerge,
			OrderType: l.OrderType,
		})
	}
	return snap
}

// Restore replaces the current basket with a previously held snapshot.
func (s *Service) Restore(snap BasketSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Lock-free cores, NOT s.Reset()/s.SetCustomer() — s.mu is non-reentrant
	// and already held here (see the locking-pattern comment on Service.mu).
	s.resetLocked()
	// ADR-0073 legacy rule: a pre-ADR-0073 payload has no per-line values
	// and its header IS the mode of every line. A new payload always writes
	// an explicit "takeaway" on every takeaway line, so only a "takeaway"
	// header ever needs to fill in omitted lines; "mixed"/"" fall to dine-in.
	legacyLineMode := ""
	if snap.OrderType == OrderTypeTakeaway {
		legacyLineMode = OrderTypeTakeaway
	}
	for _, l := range snap.Lines {
		key := l.LineKey
		lineMode := NormalizeLineOrderType(l.OrderType)
		if l.OrderType == "" {
			lineMode = legacyLineMode
		}
		if key == "" {
			// Self-heal a held sale saved before LineKey existed — an empty
			// key would otherwise collide across every such line restored
			// together (RemoveLine("") would match all of them).
			key = uuid.NewString()
		}
		s.lines = append(s.lines, BasketLine{
			LineKey: key,
			SKU:     l.SKU, Name: l.Name, Qty: l.Qty,
			PriceCents: l.PriceCents, LineDiscount: l.LineDiscount,
			ImageURL: l.ImageURL, ItemID: l.ItemID, VariantID: l.VariantID,
			TaxRateBP: l.TaxRateBP, IsWeighed: l.IsWeighed,
			TaxCodeID:   l.TaxCodeID,
			Modifiers:   l.Modifiers,
			QtyFromCode: l.QtyFromCode, NoMerge: l.NoMerge,
			OrderType: lineMode,
		})
	}
	s.discountType = snap.DiscountType
	s.discountValue = snap.DiscountValue
	s.discountPercentBP = snap.DiscountPercentBP
	s.setCustomerLocked(snap.CustomerID, snap.CustomerName)
	// ut-docs#1381: restored directly (not via SetOrderType) same as
	// TableID/TableLabel below — the Takeaway-clears-table invariant
	// SetOrderType/SetTable enforce is normally already satisfied by the
	// time a snapshot is taken. Independent review: that's not quite
	// guaranteed for every caller — internal/pages/hold_api.go's Resume
	// handler overwrites TableID/TableLabel from the held_sales.table_id
	// DB column (the authoritative source post-move, ut-docs#820) AFTER
	// unmarshalling, and a defensive caller-side check there can itself
	// fail open on error — so enforce it here too, structurally, rather
	// than trust every caller to have kept it true. A no-op whenever the
	// invariant already holds (the normal case); harmless on a legacy
	// pre-#1381 payload (OrderType decodes to "", never Takeaway).
	// The default for NEW lines after a resume: takeaway only when the held
	// sale was uniformly takeaway; a mixed or dine-in sale defaults to
	// dine-in (the product's standing default). Never OrderTypeMixed.
	s.orderType = legacyLineMode
	s.tableID = snap.TableID
	s.tableLabel = snap.TableLabel
	s.applyTablePolicyLocked()
	s.recomputeTotals()
}
