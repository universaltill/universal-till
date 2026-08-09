package pages

import (
	"fmt"
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

// TestCatalogCSVFormulaTriggersStaySynced is a drift guard, not a security
// test (ut-docs#356, follow-up from the ut-docs#321 review — see
// docs/code-reviews/2026-08-06-catalog-csv-formula-injection.md finding 2).
// csvSafe's trigger-char switch (this package's csv_export.go) and
// catimport's csvFormulaTriggers const (internal/catimport/catimport.go)
// describe the same characters as two independently-maintained literals —
// catimport can't import this package to share one (this package already
// imports catimport; the reverse would cycle) — kept in sync only by a
// code comment before this test existed.
//
// Rather than hardcoding a third copy of the trigger set (which could
// itself silently drift from both), this sweeps candidate bytes as the
// LEADING byte of two round-tripped values through the real
// writeCatalogCSV (csvSafe) and catimport.Parse (stripCSVDefuse):
//
//   - plain(b): no leading apostrophe. If csvSafe defuses b but catimport's
//     set doesn't recognize it to strip the apostrophe back off on import,
//     the round trip comes back with a stray leading "'" — this catches a
//     trigger char added to csvSafe without mirroring it to
//     csvFormulaTriggers, for any byte this sweep covers (tab, CR, and
//     printable ASCII '!'-'~' — see candidates below; other C0 controls,
//     DEL and bytes >=0x80 aren't swept, matching every trigger char this
//     codebase has used so far), not just today's known set.
//   - genuine(b): a REAL leading apostrophe followed by b, for b that
//     csvSafe does not currently treat as a trigger. If catimport's set
//     treats b as a trigger but csvSafe never defuses it, catimport wrongly
//     strips the genuine apostrophe on import — this catches a trigger
//     char added to csvFormulaTriggers without mirroring it to csvSafe.
//     (Restricted to non-trigger b: for a byte csvSafe already treats as a
//     trigger, "genuine apostrophe + trigger char" is indistinguishable
//     from a defused value by design — the accepted, separately-documented
//     non-injectivity trade-off on stripCSVDefuse, not drift. Whether b is
//     "currently a trigger" is decided by calling the real csvSafe below,
//     not a fourth hardcoded copy of the set — so a legitimate, synced
//     addition of a new trigger char to both csvSafe and csvFormulaTriggers
//     together stays covered by this skip, exactly as an existing one is.)
//
// Each value also embeds a comma and a quote, exercising real
// encoding/csv quoting alongside the defuse/strip logic.
func TestCatalogCSVFormulaTriggersStaySynced(t *testing.T) {
	// isCurrentTrigger asks the real csvSafe whether it treats b as a
	// trigger, rather than hardcoding a copy of the set. Probes with a
	// second byte appended so the field is never exactly "-" — csvSafe's
	// own sentinel exemption for the literal string "-" (InsertAudit's
	// "no entity ID" marker) would otherwise misclassify '-' as a
	// non-trigger here, even though it is one for every other value.
	isCurrentTrigger := func(b byte) bool {
		in := string(b) + "y"
		out := csvSafe(in)
		return len(out) == len(in)+1 && out[0] == '\''
	}

	var candidates []byte
	candidates = append(candidates, '\t', '\r') // current triggers outside printable ASCII
	for b := byte('!'); b <= '~'; b++ {         // printable ASCII, space excluded (see below)
		candidates = append(candidates, b)
	}
	// Space is excluded: a plain value starting with a literal space that
	// csvSafe leaves untouched gets trimmed by catimport's own
	// strings.TrimSpace on read — a pre-existing, unrelated behavior, not
	// something this drift guard is about.

	roundTrip := func(t *testing.T, name string) string {
		t.Helper()
		rows := []data.ExportRow{{Name: name, SKU: "SKU-1", PriceMinor: 100, Category: "Cat", Stock: 1}}
		var out strings.Builder
		writeCatalogCSV(&out, rows, 2)
		res, err := catimport.Parse(strings.NewReader(out.String()), 2)
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if len(res.Items) != 1 {
			t.Fatalf("expected 1 item, got %d", len(res.Items))
		}
		return res.Items[0].Name
	}

	for _, b := range candidates {
		suffix := `est, "value"` // embeds a comma + quote (ut-docs#356 AC)

		plain := string(b) + suffix
		t.Run(fmt.Sprintf("plain %#02x", b), func(t *testing.T) {
			if got := roundTrip(t, plain); got != plain {
				t.Errorf("did not round-trip: got %q, want %q", got, plain)
			}
		})

		if isCurrentTrigger(b) {
			continue
		}
		genuine := "'" + string(b) + suffix
		t.Run(fmt.Sprintf("genuine apostrophe %#02x", b), func(t *testing.T) {
			if got := roundTrip(t, genuine); got != genuine {
				t.Errorf("did not round-trip: got %q, want %q", got, genuine)
			}
		})
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
