package data

import (
	"math"
	"testing"
)

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
//
// universaltill/ut-docs#440 hardened this further: DaysLeftAt is now the
// single shared floor(qty/rate) computation, used by both this boolean
// and /inventory's displayed "days left" number, so the two can't
// silently desync; IsRunningOut also rejects a NaN rate explicitly (NaN
// fails a plain "<= 0" comparison in Go, so the original guard let it
// through to an implementation-defined float64→int conversion).
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
		{"lead-time-aware floor-divergence boundary warns", 21, 10, 2, true},  // floor(10.5)=10 <= 10 — the actual raw/floor divergence point for this lead time, not just "past the window" like the case above
		{"negative rate never warns", 5, 0, -1, false},
		{"rate check wins over the qty<=0 always-warns branch (guard ordering)", 0, 0, -1, false},
		{"NaN rate never warns", 5, 0, math.NaN(), false},
		{"NaN rate never warns even when qty<=0 would otherwise always-warn", 0, 0, math.NaN(), false}, // pins IsRunningOut's own NaN guard specifically: without it, the qty<=0 branch returns true before DaysLeftAt's NaN handling is ever reached
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

// TestLowStockItem_DaysLeftAt covers the shared floor(qty/rate) helper
// directly — extracted by universaltill/ut-docs#440 so /inventory's
// displayed "days left" number and IsRunningOut's boundary check can't
// silently desync (both now call this one method instead of each
// re-deriving int(CurrentQty/rate)). Also covers the float64→int
// conversion guard: unreachable through today's three call sites (rate
// is always a positive, finite positive_qty/28), but DaysLeftAt is
// exported from internal/data, so a future caller could pass anything.
func TestLowStockItem_DaysLeftAt(t *testing.T) {
	tests := []struct {
		name       string
		currentQty float64
		rate       float64
		want       int
	}{
		{"plain floor", 7.5, 1, 7},
		{"exact integer", 21, 2, 10},
		{"NaN rate clamps to MaxInt (never runs out)", 5, math.NaN(), math.MaxInt},
		{"a near-zero rate overflows int range and clamps to MaxInt", 1, 1e-300, math.MaxInt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := LowStockItem{CurrentQty: tt.currentQty}
			if got := l.DaysLeftAt(tt.rate); got != tt.want {
				t.Errorf("DaysLeftAt(qty=%v, rate=%v) = %v, want %v", tt.currentQty, tt.rate, got, tt.want)
			}
		})
	}
}

// TestLowStockItem_DaysLeftAtDrivesIsRunningOut proves the display path
// (DaysLeftAt, what /inventory shows as "days left") and the boolean path
// (IsRunningOut, what drives the warning) can't disagree for any
// positive qty/rate: both are defined in terms of the same shared
// computation, so a future change to the formula moves both together —
// this is the desync ut-docs#440 flagged as previously untested.
func TestLowStockItem_DaysLeftAtDrivesIsRunningOut(t *testing.T) {
	cases := []struct {
		currentQty float64
		leadTime   int
		rate       float64
	}{
		{7.5, 0, 1},
		{8, 0, 1},
		{21, 10, 2},
		{22, 10, 2},
		{100, 30, 3},
	}
	for _, tt := range cases {
		l := LowStockItem{CurrentQty: tt.currentQty, LeadTimeDays: tt.leadTime}
		want := l.DaysLeftAt(tt.rate) <= l.EffectiveWarnDays()
		if got := l.IsRunningOut(tt.rate); got != want {
			t.Errorf("qty=%v leadTime=%d rate=%v: IsRunningOut=%v, want %v (DaysLeftAt=%d, EffectiveWarnDays=%d)",
				tt.currentQty, tt.leadTime, tt.rate, got, want, l.DaysLeftAt(tt.rate), l.EffectiveWarnDays())
		}
	}
}
