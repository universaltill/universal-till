package pages

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/print"
)

// The "recorded late from other tills" note of a Z (ut-docs#5776, ADR-0066
// amendment Decision 3): rows made on another till before this Z's window
// opened but received after. They are ALREADY inside the Z's totals; the note
// only explains whose they are and never holds them apart, so it is printed
// in doc.Footer (never Totals) and no figure here is added to anything.

// lateTillShortID is the first 8 runes of a till id, so two tills missing from
// the roster stay distinguishable.
func lateTillShortID(id string) string { return truncateRunes(id, 8) }

// lateTillEntries is the count of every stream the till made late.
func lateTillEntries(t data.EODLateTill) int {
	return t.SalesCount + t.RefundCount + t.NoSaleCount + t.ClearedBasketCount
}

// lateTillLabel is the till's name, or "Other till <short id>" when it has
// none. It never falls back to this till's own name or reports.this_till: an
// unnamed row is another till (ut-docs#5777 review M1). Edge case: an own
// promote row with neither till.name nor a cloud id is labelled plain "Other
// till" (indistinguishable from another till, vanishingly rare).
func lateTillLabel(t data.EODLateTill, T func(string) string) string {
	if t.Name != "" {
		return t.Name
	}
	return strings.TrimSpace(fmt.Sprintf(T("reports.eod.late_other_till"), lateTillShortID(t.TillID)))
}

// hasPerTillEarliest reports whether any till carries its own earliest origin
// (a Z closed since ut-docs#5776). Then the overall value is not repeated;
// an older Z shows the overall one instead.
func hasPerTillEarliest(le *data.EODLateEntries) bool {
	for _, t := range le.Tills {
		if t.EarliestOrigin != "" {
			return true
		}
	}
	return false
}

// eodLateNoteFooter is the paper section, starting with the blank separator
// line; nil when there is nothing late. every line is clipped or built with
// footerRow, so none can pass print.Width and an amount is never cut.
func eodLateNoteFooter(le *data.EODLateEntries, T func(string) string, money func(int64) string, when func(rfc3339 string) string) []string {
	if le == nil || len(le.Tills) == 0 {
		return nil
	}
	clip := func(s string) string { return truncateRunes(s, print.Width) }
	out := []string{"",
		clip(T("reports.zpaper.late_title")),
		clip(T("reports.zpaper.late_made_before")),
		clip(T("reports.zpaper.late_included")),
	}
	perTill := hasPerTillEarliest(le)
	if !perTill {
		if w := when(le.EarliestOrigin); w != "" {
			out = append(out, footerRow(T("reports.zpaper.late_earliest"), w))
		}
	}
	for _, t := range le.Tills {
		out = append(out, clip(lateTillLabel(t, T)),
			footerRow("  "+T("reports.zpaper.late_entries"), strconv.Itoa(lateTillEntries(t))))
		if t.SalesCount > 0 {
			out = append(out, footerRow("  "+fmt.Sprintf(T("reports.zpaper.late_sales"), t.SalesCount), money(t.Gross)))
		}
		if t.RefundCount > 0 {
			out = append(out, footerRow("  "+fmt.Sprintf(T("reports.zpaper.late_refunds"), t.RefundCount), "-"+money(t.RefundTotal)))
		}
		if t.ClearedBasketCount > 0 {
			label := "  " + fmt.Sprintf(T("reports.zpaper.late_cleared"), t.ClearedBasketCount)
			if t.ClearedBasketTotal != nil {
				out = append(out, footerRow(label, money(*t.ClearedBasketTotal)))
			} else { // archived before ut-docs#5776: a count, never a made-up 0.00
				out = append(out, clip(label))
			}
		}
		if t.NoSaleCount > 0 {
			out = append(out, footerRow("  "+T("reports.zpaper.late_no_sale"), strconv.Itoa(t.NoSaleCount)))
		}
		if w := when(t.EarliestOrigin); w != "" {
			out = append(out, footerRow("  "+T("reports.zpaper.late_earliest"), w))
		}
	}
	return out
}

// eodLateTillView is one table row of the screen note. Plain values only: the
// money helper takes int64, not *int64.
type eodLateTillView struct {
	Name, ShortID  string
	Entries        int
	Gross          int64
	RefundCount    int
	RefundTotal    int64
	ClearedCount   int
	ClearedTotal   int64 // meaningful only when !eodLateView.ClearedAsCount
	NoSaleCount    int
	EarliestOrigin string
}

// eodLateView is the screen note; the Has* flags drive column visibility.
type eodLateView struct {
	EarliestOrigin     string // overall; shown only when !HasPerTillEarliest
	Tills              []eodLateTillView
	HasRefunds         bool
	HasCleared         bool
	HasNoSale          bool
	HasPerTillEarliest bool
	// ClearedAsCount: some till has cleared baskets with no stored total (a Z
	// archived before ut-docs#5776), so the column shows counts, not money.
	ClearedAsCount bool
}

// eodLateNoteView is nil when there is nothing late.
func eodLateNoteView(le *data.EODLateEntries) *eodLateView {
	if le == nil || len(le.Tills) == 0 {
		return nil
	}
	v := &eodLateView{EarliestOrigin: le.EarliestOrigin, HasPerTillEarliest: hasPerTillEarliest(le)}
	for _, t := range le.Tills {
		tv := eodLateTillView{Entries: lateTillEntries(t), Gross: t.Gross, RefundCount: t.RefundCount, RefundTotal: t.RefundTotal,
			ClearedCount: t.ClearedBasketCount, NoSaleCount: t.NoSaleCount, EarliestOrigin: t.EarliestOrigin,
			Name: t.Name, ShortID: lateTillShortID(t.TillID)}
		if t.ClearedBasketTotal != nil {
			tv.ClearedTotal = *t.ClearedBasketTotal
		} else if t.ClearedBasketCount > 0 {
			v.ClearedAsCount = true
		}
		v.HasRefunds = v.HasRefunds || t.RefundCount > 0
		v.HasCleared = v.HasCleared || t.ClearedBasketCount > 0
		v.HasNoSale = v.HasNoSale || t.NoSaleCount > 0
		v.Tills = append(v.Tills, tv)
	}
	return v
}
