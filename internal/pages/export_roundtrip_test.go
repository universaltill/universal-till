package pages

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catimport"
	"github.com/universaltill/universal-till/internal/data"
)

// The export exists to round-trip: the CSV we write must come back through
// our own importer with nothing lost (G22b anti-lock-in). Calls the real
// writeCatalogCSV rather than re-implementing the writer inline, so this
// test also exercises the csvSafe defusing wired into it (ut-docs#321) —
// a duplicated-logic version of this test could pass while the real
// exporter silently diverged.
func TestExportCSVRoundTripsThroughImporter(t *testing.T) {
	rows := []data.ExportRow{
		{Name: "Cola Can 330ml", SKU: "SKU-1", Barcode: "5000112637922",
			PriceMinor: 120, Category: "Drinks", Description: "Fizzy", Stock: 24},
		{Name: "Bananas", SKU: "SKU-2", PriceMinor: 95, Category: "Produce",
			IsWeighed: true, Stock: 12.5},
	}

	var b strings.Builder
	writeCatalogCSV(&b, rows, 2)

	res, err := catimport.Parse(strings.NewReader(b.String()), 2)
	if err != nil {
		t.Fatalf("parse exported CSV: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("expected 2 items back, got %d", len(res.Items))
	}
	cola := res.Items[0]
	if cola.Name != "Cola Can 330ml" || cola.SKU != "SKU-1" ||
		cola.Barcode != "5000112637922" || cola.PriceMinor != 120 ||
		cola.Category != "Drinks" || cola.Description != "Fizzy" || cola.IsWeighed {
		t.Fatalf("cola did not round-trip: %+v", cola)
	}
	if !res.Items[1].IsWeighed || res.Items[1].PriceMinor != 95 {
		t.Fatalf("weighed item did not round-trip: %+v", res.Items[1])
	}
	if !cola.HasStock || cola.Stock != 24 || res.Items[1].Stock != 12.5 {
		t.Fatalf("stock did not round-trip: %+v / %+v", cola, res.Items[1])
	}
	if cola.Issue != "" || res.Items[1].Issue != "" {
		t.Fatalf("round-trip rows carry issues: %q %q", cola.Issue, res.Items[1].Issue)
	}
}

// Catalog CSV/formula injection (ut-docs#321 — same defect class as
// ut-docs#195, but harder: this export round-trips through our own
// importer as a documented migration path, so the leading-apostrophe
// mitigation must come back OFF on re-import, not just go on). Proves
// both halves at once: the malicious value is defused for Excel/
// LibreOffice on export, AND reimporting the till's own export recovers
// the original value byte-for-byte rather than being permanently
// polluted with a stray apostrophe.
func TestExportCatalogCSV_FormulaShapedValuesDefusedAndRoundTrip(t *testing.T) {
	rows := []data.ExportRow{
		{Name: `=cmd|'/c calc'!A1`, SKU: "-DANGER", Barcode: "5000112637922",
			PriceMinor: 120, Category: "@evil", Description: "+1+1", Stock: 1},
		// A genuine leading apostrophe — NOT our defuse marker, since the
		// next byte isn't a formula-trigger char — must survive untouched.
		{Name: "'Twas the night", SKU: "SKU-3", PriceMinor: 50, Category: "Seasonal", Stock: 2},
	}

	var b strings.Builder
	writeCatalogCSV(&b, rows, 2)

	raw := b.String()
	if !strings.Contains(raw, `'=cmd|'/c calc'!A1`) {
		t.Fatalf("formula-shaped Name was not defused in raw CSV:\n%s", raw)
	}
	if !strings.Contains(raw, "5000112637922") || strings.Contains(raw, "'5000112637922") {
		t.Fatalf("plain numeric Barcode should be untouched:\n%s", raw)
	}

	res, err := catimport.Parse(strings.NewReader(raw), 2)
	if err != nil {
		t.Fatalf("parse exported CSV: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("expected 2 items back, got %d", len(res.Items))
	}

	danger := res.Items[0]
	if danger.Name != `=cmd|'/c calc'!A1` {
		t.Errorf("Name did not round-trip clean: got %q", danger.Name)
	}
	if danger.SKU != "-DANGER" {
		t.Errorf("SKU did not round-trip clean: got %q", danger.SKU)
	}
	if danger.Category != "@evil" {
		t.Errorf("Category did not round-trip clean: got %q", danger.Category)
	}
	if danger.Description != "+1+1" {
		t.Errorf("Description did not round-trip clean: got %q", danger.Description)
	}
	if danger.Barcode != "5000112637922" {
		t.Errorf("Barcode did not round-trip clean: got %q", danger.Barcode)
	}

	seasonal := res.Items[1]
	if seasonal.Name != "'Twas the night" {
		t.Errorf("genuine leading apostrophe was stripped: got %q", seasonal.Name)
	}
}

func TestMinorToDecimal(t *testing.T) {
	cases := []struct {
		minor    int64
		decimals int
		want     string
	}{
		{120, 2, "1.20"}, {5, 2, "0.05"}, {0, 2, "0.00"},
		{-95, 2, "-0.95"}, {123, 0, "123"}, {1234, 3, "1.234"},
	}
	for _, c := range cases {
		if got := minorToDecimal(c.minor, c.decimals); got != c.want {
			t.Fatalf("minorToDecimal(%d,%d) = %q, want %q", c.minor, c.decimals, got, c.want)
		}
	}
}
