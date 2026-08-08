package data

import "testing"

// Boundary coverage for LowStockItem.IsRunningOut — the single shared
// "is this item running out" decision used identically by /inventory
// (internal/pages/inventory_page.go), the low-stock digest
// (internal/alerts/alerts.go) and the /reports header chip
// (internal/pages/reports_page.go). Before this method existed, the
// three sites disagreed at an exact boundary: /inventory floored the
// days-left prediction before comparing (int(qty/rate) <= warnDays),
// while alerts.go and reports_page.go compared the raw float directly
// (qty/rate <= float64(warnDays)) — qty/rate=7.5 against a 7-day warn
// window warned on /inventory but not on the digest/reports chip
// (universaltill/ut-docs#275). Floor-then-compare is now the single
// standardized behavior (it matches /inventory, the primary surface,
// and is the more conservative choice — it never warns later than
// raw-compare would).
func TestLowStockItem_IsRunningOut(t *testing.T) {
	tests := []struct {
		name       string
		currentQty float64
		leadTime   int // 0 = default 7-day warn window
		rate       float64
		want       bool
	}{
		{"no sell rate never warns", 3, 0, 0, false},
		{"zero stock with a rate always warns", 0, 0, 1, true},
		{"negative stock with a rate always warns", -1, 0, 1, true},
		{"exact boundary at the default 7-day window warns", 7.5, 0, 1, true}, // floor(7.5)=7 <= 7
		{"just past the default 7-day window does not warn", 8, 0, 1, false},  // floor(8)=8 > 7
		{"exact integer boundary warns", 7, 0, 1, true},                       // floor(7)=7 <= 7
		{"lead-time-aware boundary warns", 16, 10, 2, true},                   // floor(8)=8 <= 10
		{"lead-time-aware just past does not warn", 22, 10, 2, false},         // floor(11)=11 > 10
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := LowStockItem{CurrentQty: tt.currentQty, LeadTimeDays: tt.leadTime}
			if got := l.IsRunningOut(tt.rate); got != tt.want {
				t.Errorf("IsRunningOut(qty=%v, leadTime=%d, rate=%v) = %v, want %v",
					tt.currentQty, tt.leadTime, tt.rate, got, tt.want)
			}
		})
	}
}
