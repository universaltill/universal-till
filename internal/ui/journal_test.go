package ui

import (
	"bytes"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// TestJournalView_ShowFiltersGatesCrossTillUI covers ut-docs#550: the
// sale-screen OOB mini-widget push (internal/pages/pos_api.go) builds
// JournalViewData{Entries, OOB: true} without setting the new fields, so
// ShowFilters/Tills/SelectedTill/Day must all be at their zero values there
// -- and the template must not render the till/day filter form or the Till
// column in that case, so the mini-widget's appearance is unchanged.
func TestJournalView_ShowFiltersGatesCrossTillUI(t *testing.T) {
	funcs := httpx.FuncsFor("en")
	view, err := NewJournalView(funcs)
	if err != nil {
		t.Fatalf("NewJournalView: %v", err)
	}
	entries := []data.SaleJournalEntry{
		{ReceiptNo: "R-1", Total: 100, TenderType: "cash", SyncStatus: "synced", CreatedAt: "2026-08-15T09:00:00Z", TillID: "till-2", TillName: "Kiosk 2"},
	}

	// OOB mini-widget shape: ShowFilters/Tills/SelectedTill/Day left zero.
	var oobBuf bytes.Buffer
	if err := view.Render(&oobBuf, JournalViewData{Entries: entries, OOB: true}); err != nil {
		t.Fatalf("render OOB: %v", err)
	}
	oob := oobBuf.String()
	if strings.Contains(oob, `name="till"`) || strings.Contains(oob, `name="day"`) {
		t.Fatalf("OOB widget must not render till/day filters: %s", oob)
	}
	if strings.Contains(oob, "Kiosk 2") {
		t.Fatalf("OOB widget must not render the Till column: %s", oob)
	}
	if !strings.Contains(oob, "R-1") {
		t.Fatalf("OOB widget must still render the receipt: %s", oob)
	}

	// Full /ui/journal shape: ShowFilters true renders both.
	var fullBuf bytes.Buffer
	if err := view.Render(&fullBuf, JournalViewData{
		Entries:      entries,
		ShowFilters:  true,
		Tills:        []data.TillRow{{ID: "till-2", Name: "Kiosk 2", LastSeenAt: "2026-08-15T08:00:00Z"}},
		SelectedTill: "all",
		Day:          "2026-08-15",
	}); err != nil {
		t.Fatalf("render full: %v", err)
	}
	full := fullBuf.String()
	if !strings.Contains(full, `name="till"`) || !strings.Contains(full, `name="day"`) {
		t.Fatalf("full journal view must render till/day filters: %s", full)
	}
	if !strings.Contains(full, "Kiosk 2") {
		t.Fatalf("full journal view must render the Till column/staleness line: %s", full)
	}
	if !strings.Contains(full, "2026-08-15T08:00:00Z") {
		t.Fatalf("full journal view must render the till's LastSeenAt: %s", full)
	}
}

// TestNewJournalView_LocaleNotStaleAcrossCachedCalls guards ut-docs#1320's
// fix directly: NewJournalView now parses its template ONCE per process
// (httpx.ClonedTemplate caches the parse) and hands back a Clone() bound to
// the caller's funcs on every call. If that clone/rebind step were ever
// dropped — e.g. a future edit executes the cached base template directly
// instead of a clone — every view would render with whichever locale's
// closures happened to be bound first, no matter what funcs a later caller
// passes. Money formatting is the sharpest signal available: fa digits are
// Persian numerals, en digits are ASCII, so a stale closure is unmissable.
func TestNewJournalView_LocaleNotStaleAcrossCachedCalls(t *testing.T) {
	httpx.InitCurrency("GBP")
	entries := []data.SaleJournalEntry{{ReceiptNo: "R-1", Total: 1234, TenderType: "cash", SyncStatus: "synced", CreatedAt: "2026-08-15T09:00:00Z"}}

	enView, err := NewJournalView(httpx.FuncsFor("en"))
	if err != nil {
		t.Fatalf("NewJournalView(en): %v", err)
	}
	var enBuf bytes.Buffer
	if err := enView.Render(&enBuf, JournalViewData{Entries: entries}); err != nil {
		t.Fatalf("render en: %v", err)
	}
	if !strings.Contains(enBuf.String(), "12.34") {
		t.Fatalf("en render should show ASCII digits (12.34): %s", enBuf.String())
	}

	faView, err := NewJournalView(httpx.FuncsFor("fa"))
	if err != nil {
		t.Fatalf("NewJournalView(fa): %v", err)
	}
	var faBuf bytes.Buffer
	if err := faView.Render(&faBuf, JournalViewData{Entries: entries}); err != nil {
		t.Fatalf("render fa: %v", err)
	}
	if strings.Contains(faBuf.String(), "12.34") {
		t.Fatalf("fa render must NOT show ASCII digits — got en's cached closure (stale funcmap): %s", faBuf.String())
	}
	if !strings.Contains(faBuf.String(), "۱۲") {
		t.Fatalf("fa render should show Persian digits (۱۲…): %s", faBuf.String())
	}

	// Re-render on the FIRST (en) view again, after the fa view was built —
	// proves the shared cached base template was never mutated by the fa
	// call (which would corrupt every other locale's already-built view).
	var enAgainBuf bytes.Buffer
	if err := enView.Render(&enAgainBuf, JournalViewData{Entries: entries}); err != nil {
		t.Fatalf("re-render en: %v", err)
	}
	if enAgainBuf.String() != enBuf.String() {
		t.Fatalf("en view's output changed after a different-locale view was built:\nfirst:  %s\nsecond: %s", enBuf.String(), enAgainBuf.String())
	}
}
