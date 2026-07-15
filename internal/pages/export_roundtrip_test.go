package pages

import (
	"encoding/csv"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/catimport"
	"github.com/universaltill/universal-till/internal/data"
)

// The export exists to round-trip: the CSV we write must come back through
// our own importer with nothing lost (G22b anti-lock-in).
func TestExportCSVRoundTripsThroughImporter(t *testing.T) {
	rows := []data.ExportRow{
		{Name: "Cola Can 330ml", SKU: "SKU-1", Barcode: "5000112637922",
			PriceMinor: 120, Category: "Drinks", Description: "Fizzy", Stock: 24},
		{Name: "Bananas", SKU: "SKU-2", PriceMinor: 95, Category: "Produce",
			IsWeighed: true, Stock: 12.5},
	}

	var b strings.Builder
	cw := csv.NewWriter(&b)
	_ = cw.Write([]string{"Name", "SKU", "Barcode", "Price", "Category",
		"Description", "Sold by weight", "In stock", "Active"})
	for _, e := range rows {
		yn := func(v bool) string {
			if v {
				return "Y"
			}
			return "N"
		}
		_ = cw.Write([]string{e.Name, e.SKU, e.Barcode, minorToDecimal(e.PriceMinor, 2),
			e.Category, e.Description, yn(e.IsWeighed),
			strconv.FormatFloat(e.Stock, 'f', -1, 64), yn(e.IsActive)})
	}
	cw.Flush()

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
