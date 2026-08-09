package catimport

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// bkpProductRow mirrors the documented speedy kasse Products columns
// (ut-docs#511) for building synthetic fixtures — never real customer data,
// see the ticket's own instruction not to commit the real backup file or
// any extract of it.
type bkpProductRow struct {
	ProductNumber    string
	ProductTextShort string
	SalesPrice       any // float64/string/etc — exercised deliberately in the bad-price test
	ProductGroupText string
	Status           int
	ProductType      int
}

// buildBkpDBBytes creates a temp SQLite file with a Products table matching
// the documented schema, populates it, and returns the raw file bytes ready
// to embed as backup.db in a synthetic ZIP.
func buildBkpDBBytes(t *testing.T, rows []bkpProductRow) []byte {
	t.Helper()
	tmp, err := os.CreateTemp("", "bkp-fixture-*.db")
	if err != nil {
		t.Fatalf("create temp db: %v", err)
	}
	path := tmp.Name()
	_ = tmp.Close()
	t.Cleanup(func() { _ = os.Remove(path) })

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE Products (
		ProductNumber TEXT,
		ProductTextShort TEXT,
		SalesPrice REAL,
		ProductGroupText TEXT,
		Status INTEGER,
		ProductType INTEGER
	)`); err != nil {
		t.Fatalf("create Products table: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO Products
			(ProductNumber, ProductTextShort, SalesPrice, ProductGroupText, Status, ProductType)
			VALUES (?,?,?,?,?,?)`,
			r.ProductNumber, r.ProductTextShort, r.SalesPrice, r.ProductGroupText, r.Status, r.ProductType); err != nil {
			t.Fatalf("insert product %+v: %v", r, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close temp db: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read temp db bytes: %v", err)
	}
	return b
}

// buildBkpZip assembles an in-memory ZIP with the given entries (name →
// bytes) — omit "backup.db" or "meta.inf" from entries to exercise the
// missing-file path.
func buildBkpZip(t *testing.T, entries map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %q: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("write zip entry %q: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return buf.Bytes()
}

// validMetaInf is a meta.inf with no recognisable checksum structure at all
// — ParseBkp must accept this (best-effort validator, see bkp.go's doc
// comment) and fall back to archive/zip's own CRC32 integrity check.
const validMetaInfNoChecksums = `{"app_version":"4.4.08","device":"iMin I22T01","licence_key":"TEST-0000"}`

// metaInfWithChecksum builds a meta.inf carrying a recognisable per-file
// checksum list, with backup.db's sha256 set to the given hex string
// (deliberately wrong in the mismatch test).
func metaInfWithChecksum(backupDBSHA256Hex string) string {
	return fmt.Sprintf(`{
		"app_version": "4.4.08",
		"device": "iMin I22T01",
		"licence_key": "TEST-0000",
		"files": [
			{"name": "backup.db", "sha256": %q},
			{"name": "documents.zip", "sha256": "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}
		]
	}`, backupDBSHA256Hex)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestParseBkp_NormalRowImports(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "10234", ProductTextShort: "Espresso", SalesPrice: 2.90, ProductGroupText: "Coffee", Status: 1, ProductType: 1},
	})
	zipBytes := buildBkpZip(t, map[string][]byte{
		"backup.db": dbBytes,
		"meta.inf":  []byte(validMetaInfNoChecksums),
	})

	res, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if err != nil {
		t.Fatalf("ParseBkp: %v", err)
	}
	if res.Format != "speedy-kasse" {
		t.Errorf("format = %q, want speedy-kasse", res.Format)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	it := res.Items[0]
	if it.Name != "Espresso" || it.SKU != "10234" || it.PriceMinor != 290 || it.Category != "Coffee" {
		t.Errorf("normal row parsed wrong: %+v", it)
	}
	if it.Barcode != "" {
		t.Errorf("ProductNumber must never populate Barcode, got %q", it.Barcode)
	}
	if it.IsWeighed {
		t.Error("bkp source has no weighed column, IsWeighed must be false")
	}
	if it.Issue != "" {
		t.Errorf("clean row must carry no issue, got %q", it.Issue)
	}
}

func TestParseBkp_StatusDeletedExcluded(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "1", ProductTextShort: "Old Item", SalesPrice: 1.00, ProductGroupText: "Misc", Status: 3, ProductType: 1},
	})
	zipBytes := buildBkpZip(t, map[string][]byte{"backup.db": dbBytes, "meta.inf": []byte(validMetaInfNoChecksums)})

	res, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if err != nil {
		t.Fatalf("ParseBkp: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	if res.Items[0].Issue != IssueSourceDeleted {
		t.Errorf("Issue = %q, want %q", res.Items[0].Issue, IssueSourceDeleted)
	}
}

func TestParseBkp_OrderToggleExcluded(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "999", ProductTextShort: "⚠️ToGo⚠️", SalesPrice: 0, ProductGroupText: "", Status: 1, ProductType: 4},
	})
	zipBytes := buildBkpZip(t, map[string][]byte{"backup.db": dbBytes, "meta.inf": []byte(validMetaInfNoChecksums)})

	res, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if err != nil {
		t.Fatalf("ParseBkp: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	if res.Items[0].Issue != IssueNotSellable {
		t.Errorf("Issue = %q, want %q", res.Items[0].Issue, IssueNotSellable)
	}
}

func TestParseBkp_BlankNameExcluded(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "2", ProductTextShort: "   ", SalesPrice: 1.00, ProductGroupText: "Misc", Status: 1, ProductType: 1},
	})
	zipBytes := buildBkpZip(t, map[string][]byte{"backup.db": dbBytes, "meta.inf": []byte(validMetaInfNoChecksums)})

	res, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if err != nil {
		t.Fatalf("ParseBkp: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	if res.Items[0].Issue != IssueMissingName {
		t.Errorf("Issue = %q, want %q", res.Items[0].Issue, IssueMissingName)
	}
}

func TestParseBkp_MultiLineNameCollapsed(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "3", ProductTextShort: "Cappuccino\nGroß", SalesPrice: 3.50, ProductGroupText: "Coffee", Status: 1, ProductType: 1},
	})
	zipBytes := buildBkpZip(t, map[string][]byte{"backup.db": dbBytes, "meta.inf": []byte(validMetaInfNoChecksums)})

	res, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if err != nil {
		t.Fatalf("ParseBkp: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	if res.Items[0].Name != "Cappuccino Groß" {
		t.Errorf("Name = %q, want collapsed single line", res.Items[0].Name)
	}
}

func TestParseBkp_DuplicateProductNumber(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "555", ProductTextShort: "Latte", SalesPrice: 3.00, ProductGroupText: "Coffee", Status: 1, ProductType: 1},
		{ProductNumber: "555", ProductTextShort: "Latte (dup)", SalesPrice: 3.20, ProductGroupText: "Coffee", Status: 1, ProductType: 1},
	})
	zipBytes := buildBkpZip(t, map[string][]byte{"backup.db": dbBytes, "meta.inf": []byte(validMetaInfNoChecksums)})

	res, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if err != nil {
		t.Fatalf("ParseBkp: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(res.Items))
	}
	if res.Items[0].Issue != "" {
		t.Errorf("first occurrence must import normally, got Issue %q", res.Items[0].Issue)
	}
	if res.Items[1].Issue != IssueDuplicateSKUInFile {
		t.Errorf("second occurrence Issue = %q, want %q (not the DB-level sku_already_in_catalog code)", res.Items[1].Issue, IssueDuplicateSKUInFile)
	}
}

func TestParseBkp_BadPrice(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "4", ProductTextShort: "Mystery Item", SalesPrice: "N/A", ProductGroupText: "Misc", Status: 1, ProductType: 1},
	})
	zipBytes := buildBkpZip(t, map[string][]byte{"backup.db": dbBytes, "meta.inf": []byte(validMetaInfNoChecksums)})

	res, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if err != nil {
		t.Fatalf("ParseBkp: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
	it := res.Items[0]
	if it.Issue != IssueBadPrice {
		t.Errorf("Issue = %q, want %q", it.Issue, IssueBadPrice)
	}
	if it.IssueDetail != "N/A" {
		t.Errorf("IssueDetail = %q, want raw value %q", it.IssueDetail, "N/A")
	}
}

// TestParseBkp_GermanDecimalCommaPrice is a regression test for a review
// finding (ut-docs#511, 2026-08-09): SQLite's dynamic typing means a REAL
// SalesPrice column can still hold a German-formatted comma-decimal string
// ("2,90") — the shared CSV-path ParsePrice treats ',' as a thousands
// separator to strip, which silently turned "2,90" into 290 (i.e. €290.00
// instead of €2.90, a 100x error) before parseBkpSalesPrice existed.
func TestParseBkp_GermanDecimalCommaPrice(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "10", ProductTextShort: "Kaffee", SalesPrice: "2,90", ProductGroupText: "Coffee", Status: 1, ProductType: 1},
		{ProductNumber: "11", ProductTextShort: "Kuchen", SalesPrice: "1.234,50", ProductGroupText: "Cake", Status: 1, ProductType: 1},
		{ProductNumber: "12", ProductTextShort: "Wasser", SalesPrice: "1.40", ProductGroupText: "Drinks", Status: 1, ProductType: 1},
	})
	zipBytes := buildBkpZip(t, map[string][]byte{"backup.db": dbBytes, "meta.inf": []byte(validMetaInfNoChecksums)})

	res, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if err != nil {
		t.Fatalf("ParseBkp: %v", err)
	}
	if len(res.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(res.Items))
	}
	if res.Items[0].Issue != "" || res.Items[0].PriceMinor != 290 {
		t.Errorf("Kaffee (2,90) = %+v, want PriceMinor 290 and no issue", res.Items[0])
	}
	if res.Items[1].Issue != "" || res.Items[1].PriceMinor != 123450 {
		t.Errorf("Kuchen (1.234,50) = %+v, want PriceMinor 123450 and no issue", res.Items[1])
	}
	if res.Items[2].Issue != "" || res.Items[2].PriceMinor != 140 {
		t.Errorf("Wasser (1.40, plain dot-decimal, must still work) = %+v, want PriceMinor 140 and no issue", res.Items[2])
	}
}

// TestParseBkp_BadPriceRowDoesNotBlockLaterGoodRowSamePLU is a regression
// test for a review finding (ut-docs#511, 2026-08-09): a bad-price row used
// to register its PLU as "seen" too, so a later, otherwise-clean row
// sharing that PLU was wrongly flagged as a same-file duplicate — silently
// losing a real, importable product. The bad-price row must still surface
// its own bad_price reason; the good row must still import.
func TestParseBkp_BadPriceRowDoesNotBlockLaterGoodRowSamePLU(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "777", ProductTextShort: "Broken price", SalesPrice: "N/A", ProductGroupText: "Misc", Status: 1, ProductType: 1},
		{ProductNumber: "777", ProductTextShort: "Good product", SalesPrice: 4.50, ProductGroupText: "Misc", Status: 1, ProductType: 1},
	})
	zipBytes := buildBkpZip(t, map[string][]byte{"backup.db": dbBytes, "meta.inf": []byte(validMetaInfNoChecksums)})

	res, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if err != nil {
		t.Fatalf("ParseBkp: %v", err)
	}
	if len(res.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(res.Items))
	}
	if res.Items[0].Issue != IssueBadPrice {
		t.Errorf("first (bad price) Issue = %q, want %q", res.Items[0].Issue, IssueBadPrice)
	}
	if res.Items[1].Issue != "" || res.Items[1].PriceMinor != 450 {
		t.Errorf("second (good, same PLU) = %+v, want it to import cleanly at 450, not be flagged as a duplicate", res.Items[1])
	}
}

func TestParseBkp_MissingBackupDB(t *testing.T) {
	zipBytes := buildBkpZip(t, map[string][]byte{"meta.inf": []byte(validMetaInfNoChecksums)})
	_, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if !errors.Is(err, ErrBkpMissingFiles) {
		t.Fatalf("err = %v, want ErrBkpMissingFiles", err)
	}
}

func TestParseBkp_MissingMetaInf(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "1", ProductTextShort: "Espresso", SalesPrice: 2.5, ProductGroupText: "Coffee", Status: 1, ProductType: 1},
	})
	zipBytes := buildBkpZip(t, map[string][]byte{"backup.db": dbBytes})
	_, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if !errors.Is(err, ErrBkpMissingFiles) {
		t.Fatalf("err = %v, want ErrBkpMissingFiles", err)
	}
}

func TestParseBkp_InvalidMetaJSON(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "1", ProductTextShort: "Espresso", SalesPrice: 2.5, ProductGroupText: "Coffee", Status: 1, ProductType: 1},
	})
	zipBytes := buildBkpZip(t, map[string][]byte{"backup.db": dbBytes, "meta.inf": []byte("not json at all {{{")})
	_, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if !errors.Is(err, ErrBkpInvalidMeta) {
		t.Fatalf("err = %v, want ErrBkpInvalidMeta", err)
	}
}

// TestParseBkp_ChecksumMismatchFailsLoudly exercises the checksum-mismatch
// code path directly: meta.inf recognisably names backup.db with a
// deliberately wrong sha256, which ParseBkp must catch and refuse rather
// than silently importing from a file that doesn't match its own manifest.
func TestParseBkp_ChecksumMismatchFailsLoudly(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "1", ProductTextShort: "Espresso", SalesPrice: 2.5, ProductGroupText: "Coffee", Status: 1, ProductType: 1},
	})
	wrongHex := strings.Repeat("0", 64)
	realHex := sha256Hex(dbBytes)
	if wrongHex == realHex {
		t.Fatal("test setup bug: wrong hex accidentally matches the real one")
	}
	zipBytes := buildBkpZip(t, map[string][]byte{
		"backup.db": dbBytes,
		"meta.inf":  []byte(metaInfWithChecksum(wrongHex)),
	})
	_, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if !errors.Is(err, ErrBkpChecksumMismatch) {
		t.Fatalf("err = %v, want ErrBkpChecksumMismatch", err)
	}
}

// TestParseBkp_ChecksumMatchPasses is the positive counterpart: a
// meta.inf-recorded checksum that DOES match must not block the import.
func TestParseBkp_ChecksumMatchPasses(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "1", ProductTextShort: "Espresso", SalesPrice: 2.5, ProductGroupText: "Coffee", Status: 1, ProductType: 1},
	})
	zipBytes := buildBkpZip(t, map[string][]byte{
		"backup.db": dbBytes,
		"meta.inf":  []byte(metaInfWithChecksum(sha256Hex(dbBytes))),
	})
	res, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if err != nil {
		t.Fatalf("ParseBkp: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
}
