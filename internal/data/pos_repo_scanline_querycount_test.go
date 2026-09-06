package data

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/barcode"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/db"
)

// ean13tQC appends the GS1 mod-10 check digit to a 12-digit body — a
// package-local copy of pos_repo_scanline_test.go's ean13t (that copy lives
// in package data_test and isn't reachable from here; this file needs
// package data itself to reuse openCountingConn, which is unexported).
func ean13tQC(t *testing.T, body string) string {
	t.Helper()
	if len(body) != 12 {
		t.Fatalf("ean13tQC body must be 12 digits, got %q", body)
	}
	sum := 0
	weight := 3
	for i := len(body) - 1; i >= 0; i-- {
		sum += int(body[i]-'0') * weight
		if weight == 3 {
			weight = 1
		} else {
			weight = 3
		}
	}
	return body + string(byte((10-sum%10)%10)+'0')
}

// newScanLineQueryCountTestDB opens a fresh, real-migration-backed DB (a
// file, not :memory: — the counting harness below needs a second
// connection to the same on-disk database) and clears the demo catalog
// data migrations seed, so a barcode this test picks can never collide
// with sample data.
func newScanLineQueryCountTestDB(t *testing.T) (*db.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pos_scanline_querycount.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	ctx := context.Background()
	for _, tbl := range []string{"variant_barcodes", "item_barcodes", "item_variants", "item_images", "items"} {
		if _, err := d.DB.ExecContext(ctx, `DELETE FROM `+tbl); err != nil {
			t.Fatalf("clear seeded %s: %v", tbl, err)
		}
	}
	return d, path
}

func seedQCItem(t *testing.T, d *db.DB, id, sku, name string, basePrice int64) {
	t.Helper()
	if _, err := d.DB.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES (?, ?, ?, ?, 1)`,
		id, sku, name, basePrice); err != nil {
		t.Fatalf("seed item %s: %v", id, err)
	}
}

func seedQCVariant(t *testing.T, d *db.DB, id, itemID, name string, price int64) {
	t.Helper()
	if _, err := d.DB.Exec(`INSERT INTO item_variants (id, item_id, name, price, is_active) VALUES (?, ?, ?, ?, 1)`,
		id, itemID, name, price); err != nil {
		t.Fatalf("seed variant %s: %v", id, err)
	}
}

// TestResolveScanLine_QueryCount is the ut-docs#1360 regression: before this
// card, a single ResolveScanLine call issued up to four sequential
// resolveVariant/resolveItem round trips (probing tier by tier) plus the
// (unchanged, out-of-scope) price-resolution round trips — a plain
// correctness test can't tell that shape apart from a batched one, since
// both return the same resolved line. This opens a second connection to
// the same on-disk DB through
// export_repo_querycount_test.go's counting driver.Connector (same
// package, so its unexported helper is reachable here) and asserts the
// SELECT count directly, for three shapes: a direct item-barcode hit, a
// direct variant-barcode hit, and the ut-docs#934 raw-code fallback (the
// shape that used to cost the most — all four barcode tiers probed before
// falling through to the raw-code item tier).
func TestResolveScanLine_QueryCount(t *testing.T) {
	d, path := newScanLineQueryCountTestDB(t)

	seedQCItem(t, d, "i-plain", "SKU-PLAIN", "Plain Item", 250)
	plainCode := ean13tQC(t, "500000100000")
	if _, err := d.DB.Exec(`INSERT INTO item_barcodes (barcode, item_id) VALUES (?, ?)`, plainCode, "i-plain"); err != nil {
		t.Fatalf("seed plain item barcode: %v", err)
	}

	seedQCItem(t, d, "i-parent", "SKU-PARENT", "Parent Item", 100)
	seedQCVariant(t, d, "v-1", "i-parent", "Variant One", 300)
	variantCode := ean13tQC(t, "500000200000")
	if _, err := d.DB.Exec(`INSERT INTO variant_barcodes (barcode, variant_id) VALUES (?, ?)`, variantCode, "v-1"); err != nil {
		t.Fatalf("seed variant barcode: %v", err)
	}

	// Raw-code fallback fixture (ut-docs#934): a scale symbology enabled
	// alongside plain EAN13 means a structurally-valid-looking scale label
	// decodes to a zeroed LookupKey that differs from the raw code. This
	// item's barcode is stored under its RAW code (explicit type, the
	// AddBarcode escape hatch), so no row exists at the zeroed key and
	// resolution only succeeds via the raw-code tier — the shape that used
	// to cost all four barcode-tier round trips before falling through.
	seedQCItem(t, d, "i-raw", "SKU-RAW", "Raw Fallback Item", 175)
	rawCode := ean13tQC(t, "231234501234") // prefix 23 also parses as a scale label
	ctx := context.Background()
	catalog := NewCatalogRepo(d.DB)
	if err := catalog.AddBarcode(ctx, catalogtypes.BarcodeInput{
		Barcode: rawCode, ItemID: "i-raw", BarcodeType: "EAN13",
	}); err != nil {
		t.Fatalf("seed raw-fallback barcode: %v", err)
	}

	enabledPlain := []string{"EAN13"}
	enabledWeight := []string{"EAN13", "EAN13_WEIGHT_PREFIX2X"}

	countFor := func(code string, enabled []string) (int64, ShortcutLine, barcode.Decoded, bool) {
		counter := new(int64)
		countingDB := openCountingConn(t, path, counter)
		repo := NewPOSRepo(countingDB)
		line, dec, ok := repo.ResolveScanLine(ctx, code, enabled)
		return *counter, line, dec, ok
	}

	// Independent-review guard (same reasoning as
	// TestSalesForExport_ConstantQueryCount): a harness that silently stops
	// counting would make every assertion below vacuously true, so require
	// at least one SELECT was actually observed on the hit paths before
	// trusting the upper bound.
	requireCounted := func(t *testing.T, got int64) {
		t.Helper()
		if got < 1 {
			t.Fatalf("harness counted %d SELECTs -- it stopped counting (assertion would be vacuous)", got)
		}
	}

	// Every hit case below costs 1 barcode-tier query plus 2 price-resolution
	// queries (lookupPriceHistory misses -- these items have no scheduled
	// future price change, the ordinary case -- then falls back to the
	// items/item_variants base-price query). That price-resolution shape is
	// unchanged by this card (out of scope: ResolveCurrentPrice is shared
	// well beyond scan-line resolution, see ut-docs#1360's own acceptance
	// criteria, scoped to "the barcode fallback tiers"), so 3 is the
	// expected total for a hit, not 1.

	t.Run("direct item barcode hit", func(t *testing.T) {
		got, line, _, ok := countFor(plainCode, enabledPlain)
		if !ok || line.ItemID != "i-plain" {
			t.Fatalf("resolved ok=%v line=%+v, want i-plain", ok, line)
		}
		requireCounted(t, got)
		// Before ut-docs#1360: resolveVariant miss (1) + resolveItem hit
		// (1) + price (2) = 4. Batched: 1 barcode-tier query (the database
		// picks the winning tier) + price (2) = 3 -- the exact count is
		// asserted (not just "fewer than before") so a future change that
		// reintroduces even one extra round trip on the common case fails
		// this test.
		if got != 3 {
			t.Fatalf("direct item-barcode hit: got %d SELECTs, want exactly 3 (was 4 before batching)", got)
		}
	})

	t.Run("direct variant barcode hit", func(t *testing.T) {
		got, line, _, ok := countFor(variantCode, enabledPlain)
		if !ok || line.ItemID != "i-parent" || line.VariantID != "v-1" {
			t.Fatalf("resolved ok=%v line=%+v, want i-parent/v-1", ok, line)
		}
		requireCounted(t, got)
		// A variant match already won at tier 1 before this card too (no
		// wasted item-tier probe), so this shape was already 3 (1 + price
		// 2) -- unchanged by batching. Pinned so a regression that starts
		// probing item after variant already matched would be caught.
		if got != 3 {
			t.Fatalf("direct variant-barcode hit: got %d SELECTs, want exactly 3", got)
		}
	})

	t.Run("raw-code fallback hit", func(t *testing.T) {
		got, line, dec, ok := countFor(rawCode, enabledWeight)
		if !ok || line.ItemID != "i-raw" {
			t.Fatalf("resolved ok=%v line=%+v, want i-raw (raw-code fallback tier)", ok, line)
		}
		// Independent-review finding: before this pin, nothing distinguished
		// a tier-3/4 (raw-code) match reporting the real embedded-weight/
		// price Decoded from it reporting a zero one -- mutating
		// resolveScanBarcodeTiers's `tier <= 2` to always-true passed the
		// whole suite. This tier's match is money/weight-sensitive (a
		// mis-attributed embedded weight/price would silently apply to the
		// wrong row's sale line), so pin it directly: a raw-code-tier match
		// must report a zero Decoded{}, exactly as the two literal
		// `barcode.Decoded{}` returns inside the pre-batching `if
		// dec.LookupKey != code` block always did.
		if dec != (barcode.Decoded{}) {
			t.Fatalf("raw-code fallback hit: dec = %+v, want zero Decoded{} (an embedded weight/price must never be attributed to a raw-code-tier match)", dec)
		}
		requireCounted(t, got)
		// This is the shape ut-docs#1360 targeted most directly. Before
		// batching: a miss at both LookupKey tiers (variant, item), a miss
		// at the raw-code variant tier, a hit at the raw-code item tier --
		// 4 barcode-tier round trips -- plus price (2) = 6. Batched: all
		// four tiers in one UNION (1) + price (2) = 3.
		if got != 3 {
			t.Fatalf("raw-code fallback hit: got %d SELECTs, want exactly 3 (was 6 before batching)", got)
		}
	})

	t.Run("no match at all", func(t *testing.T) {
		got, _, _, ok := countFor(ean13tQC(t, "999999900000"), enabledPlain)
		if ok {
			t.Fatal("expected no match for an unseeded barcode")
		}
		requireCounted(t, got)
		// No price query when nothing resolved -- just the one barcode-tier
		// query (2 branches, since LookupKey == code for plain EAN13).
		if got != 1 {
			t.Fatalf("no-match: got %d SELECTs, want exactly 1", got)
		}
	})
}
