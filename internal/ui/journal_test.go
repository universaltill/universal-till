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
