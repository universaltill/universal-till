package pages

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#5753: the printed Z-report's NO SALE (DRAWER OPENS) section.

func noSaleBaseReport() data.EODReport {
	return data.EODReport{Day: "2026-10-14", GeneratedAt: "not-a-timestamp", SalesCount: 2, Gross: 1000, Net: 900}
}

func buildNoSaleDoc(t *testing.T, rep data.EODReport) print.Doc {
	t.Helper()
	newPrintAPITestDeps(t)
	pinDefaultLocale(t, "en")
	return buildEODDoc(rep, "Shop", "", eodArticlePrintCapped, 2)
}

func TestEODPrint_NoSaleSection(t *testing.T) {
	base := buildNoSaleDoc(t, noSaleBaseReport())
	rep := noSaleBaseReport()
	rep.NoSaleCount = 3
	rep.NoSaleOperators = []data.NoSaleOperator{{CashierID: "a", DisplayName: "A", Count: 2}, {CashierID: "b", DisplayName: "B", Count: 1}}
	doc := buildNoSaleDoc(t, rep)

	want := append(append([]string{}, base.Footer...),
		"", "NO SALE (DRAWER OPENS)",
		footerRow("Drawer opens", "3"), footerRow("A", "2"), footerRow("B", "1"))
	if !reflect.DeepEqual(doc.Footer, want) {
		t.Fatalf("footer:\n got %q\nwant %q", doc.Footer, want)
	}
	if !reflect.DeepEqual(doc.Totals, base.Totals) {
		t.Fatalf("no-sale must never touch Totals:\n got %v\nwant %v", doc.Totals, base.Totals)
	}
}

func TestEODPrint_NoSaleOmittedWhenZero(t *testing.T) {
	doc := buildNoSaleDoc(t, noSaleBaseReport())
	for _, l := range doc.Footer {
		if strings.Contains(l, "NO SALE") || strings.Contains(l, "Drawer opens") {
			t.Fatalf("zero no-sale must print nothing, got line %q", l)
		}
	}
}

func TestEODPrint_NoSaleUnattributedRangeAndNoSalesDay(t *testing.T) {
	rep := data.EODReport{Day: "2026-10-14", GeneratedAt: "not-a-timestamp", NoSaleCount: 4,
		NoSaleOperators: []data.NoSaleOperator{{Count: 4}}}
	doc := buildNoSaleDoc(t, rep)
	joined := strings.Join(doc.Footer, "\n")
	if !strings.Contains(joined, footerRow("Unattributed", "4")) {
		t.Fatalf("empty cashier must print the unattributed label, got %q", doc.Footer)
	}
	// Range report: total only.
	rep.NoSaleOperators = nil
	doc = buildNoSaleDoc(t, rep)
	got := doc.Footer[len(doc.Footer)-3:]
	want := []string{"", "NO SALE (DRAWER OPENS)", footerRow("Drawer opens", "4")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("range tail = %q, want %q", got, want)
	}
}

// A no-sale on each side of a close boundary lands in consecutive closes
// through the real generateEOD path (close-to-close; ADR-0066).
func TestGenerateEOD_NoSaleWindowsAbut(t *testing.T) {
	dp := newEODTestDeps(t)
	repo := data.NewPOSRepo(dp.Db)
	ctx := t.Context()

	if _, err := repo.InsertNoSaleEvent(ctx, nil, data.NoSaleEvent{CreatedAt: time.Now().Add(-5 * time.Second).UTC().Format(time.RFC3339), ActorID: "x"}); err != nil {
		t.Fatal(err)
	}
	rep1, _, err := generateEOD(ctx, dp, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if rep1.NoSaleCount != 1 {
		t.Fatalf("first close NoSaleCount = %d, want 1", rep1.NoSaleCount)
	}
	boundary, err := repo.LatestArchivedAt(ctx, "eod")
	if err != nil || boundary == nil {
		t.Fatalf("boundary: %v %v", boundary, err)
	}
	// Exactly at the boundary belongs to the NEXT close.
	if _, err := repo.InsertNoSaleEvent(ctx, nil, data.NoSaleEvent{CreatedAt: boundary.UTC().Format(time.RFC3339), ActorID: "x"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond)
	rep2, _, err := generateEOD(ctx, dp, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if rep2.NoSaleCount != 1 {
		t.Fatalf("second close NoSaleCount = %d, want 1 (no double count, no gap)", rep2.NoSaleCount)
	}
}
