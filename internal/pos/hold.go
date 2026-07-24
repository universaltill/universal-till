package pos

import (
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
)

// SnapshotLine is a BasketLine with every field serialized — BasketLine hides
// ItemID/VariantID/TaxRateBP/IsWeighed from the wire (json:"-"), but a held
// sale must restore them so pricing and completion behave identically.
type SnapshotLine struct {
	SKU          string                  `json:"sku"`
	Name         string                  `json:"name"`
	Qty          float64                 `json:"qty"`
	PriceCents   money.Money             `json:"price_cents"`
	LineDiscount money.Money             `json:"line_discount,omitempty"`
	ImageURL     string                  `json:"image_url,omitempty"`
	ItemID       string                  `json:"item_id,omitempty"`
	VariantID    string                  `json:"variant_id,omitempty"`
	TaxRateBP    int                     `json:"tax_rate_bp,omitempty"`
	IsWeighed    bool                    `json:"is_weighed,omitempty"`
	Modifiers    []data.SelectedModifier `json:"modifiers,omitempty"` // ADR-0020: a held sale must not silently drop customizations on recall
}

// BasketSnapshot captures the full in-progress sale state for hold/resume.
type BasketSnapshot struct {
	Lines             []SnapshotLine `json:"lines"`
	DiscountType      string         `json:"discount_type,omitempty"`
	DiscountValue     money.Money    `json:"discount_value,omitempty"`
	DiscountPercentBP int64          `json:"discount_percent_bp,omitempty"`
	CustomerID        string         `json:"customer_id,omitempty"`
	CustomerName      string         `json:"customer_name,omitempty"`
	Total             money.Money    `json:"total"`
}

// HasItems reports whether the current basket has any lines.
func (s *Service) HasItems() bool {
	return len(s.lines) > 0
}

// Snapshot captures the current basket so it can be held and later restored.
func (s *Service) Snapshot() BasketSnapshot {
	s.recomputeTotals()
	snap := BasketSnapshot{
		DiscountType:      s.discountType,
		DiscountValue:     s.discountValue,
		DiscountPercentBP: s.discountPercentBP,
		CustomerID:        s.customerID,
		CustomerName:      s.customerName,
		Total:             s.basket.Total,
	}
	for _, l := range s.lines {
		snap.Lines = append(snap.Lines, SnapshotLine{
			SKU: l.SKU, Name: l.Name, Qty: l.Qty,
			PriceCents: l.PriceCents, LineDiscount: l.LineDiscount,
			ImageURL: l.ImageURL, ItemID: l.ItemID, VariantID: l.VariantID,
			TaxRateBP: l.TaxRateBP, IsWeighed: l.IsWeighed,
			Modifiers: l.Modifiers,
		})
	}
	return snap
}

// Restore replaces the current basket with a previously held snapshot.
func (s *Service) Restore(snap BasketSnapshot) {
	s.Reset()
	for _, l := range snap.Lines {
		s.lines = append(s.lines, BasketLine{
			SKU: l.SKU, Name: l.Name, Qty: l.Qty,
			PriceCents: l.PriceCents, LineDiscount: l.LineDiscount,
			ImageURL: l.ImageURL, ItemID: l.ItemID, VariantID: l.VariantID,
			TaxRateBP: l.TaxRateBP, IsWeighed: l.IsWeighed,
			Modifiers: l.Modifiers,
		})
	}
	s.discountType = snap.DiscountType
	s.discountValue = snap.DiscountValue
	s.discountPercentBP = snap.DiscountPercentBP
	s.SetCustomer(snap.CustomerID, snap.CustomerName)
	s.recomputeTotals()
}
