package catimport

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
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
	// TaxPercentage/TaxPercentage2 (ut-docs#512): dine-in/takeaway VAT rate
	// columns. nil leaves the cell NULL — most existing fixtures don't set
	// these, exercising "tax columns present but this row carries none".
	TaxPercentage  any
	TaxPercentage2 any
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
		ProductType INTEGER,
		TaxPercentage REAL,
		TaxPercentage2 REAL
	)`); err != nil {
		t.Fatalf("create Products table: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO Products
			(ProductNumber, ProductTextShort, SalesPrice, ProductGroupText, Status, ProductType, TaxPercentage, TaxPercentage2)
			VALUES (?,?,?,?,?,?,?,?)`,
			r.ProductNumber, r.ProductTextShort, r.SalesPrice, r.ProductGroupText, r.Status, r.ProductType, r.TaxPercentage, r.TaxPercentage2); err != nil {
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

// withBkpMaxDBSize lowers the package var for the duration of the test —
// avoids needing gigantic (100s-of-MB) fixtures to exercise the size-cap
// boundary in a fast unit test.
func withBkpMaxDBSize(t *testing.T, n int64) {
	t.Helper()
	orig := bkpMaxDBSize
	bkpMaxDBSize = n
	t.Cleanup(func() { bkpMaxDBSize = orig })
}

// buildBkpZipWithMismatchedBackupDBSize writes backup.db declaring an
// UncompressedSize64 that does NOT match the real content — see
// TestParseBkp_MismatchedDeclaredSizeRejected for what this proves.
func buildBkpZipWithMismatchedBackupDBSize(t *testing.T, dbBytes []byte, liedUncompressedSize uint64, metaBytes []byte) []byte {
	t.Helper()
	return buildBkpZipRawBackupDB(t, dbBytes, liedUncompressedSize, crc32.ChecksumIEEE(dbBytes), metaBytes)
}

// buildBkpZipRawBackupDB writes backup.db via zip.Writer's raw entry API
// (CreateRaw), so the test controls the recorded UncompressedSize64 and
// CRC32 independently of the bytes actually stored — the only way to build
// the corrupt-header fixtures ParseBkp's guards are supposed to catch.
// meta.inf still goes through the normal zw.Create path.
func buildBkpZipRawBackupDB(t *testing.T, dbBytes []byte, declaredSize uint64, declaredCRC32 uint32, metaBytes []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	var compressed bytes.Buffer
	fw, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("flate writer: %v", err)
	}
	if _, err := fw.Write(dbBytes); err != nil {
		t.Fatalf("flate write: %v", err)
	}
	if err := fw.Close(); err != nil {
		t.Fatalf("flate close: %v", err)
	}

	fh := &zip.FileHeader{
		Name:               "backup.db",
		Method:             zip.Deflate,
		UncompressedSize64: declaredSize,
		CompressedSize64:   uint64(compressed.Len()),
		CRC32:              declaredCRC32,
	}
	rw, err := zw.CreateRaw(fh)
	if err != nil {
		t.Fatalf("create raw backup.db entry: %v", err)
	}
	if _, err := rw.Write(compressed.Bytes()); err != nil {
		t.Fatalf("write raw backup.db entry: %v", err)
	}

	w, err := zw.Create("meta.inf")
	if err != nil {
		t.Fatalf("create meta.inf entry: %v", err)
	}
	if _, err := w.Write(metaBytes); err != nil {
		t.Fatalf("write meta.inf entry: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return buf.Bytes()
}

// ut-docs#594: a real speedy kasse backup can run into the hundreds of MB
// (the pilot café's own backup.db was 270MB) — well past the original
// 200MB cap, which made the importer unable to read the one real input it
// will ever be given. Rather than committing a real 270MB fixture, this
// pins the raised production constant itself as a regression guard: nobody
// can silently shrink it back below what this real customer needed without
// this test failing.
func TestBkpMaxDBSizeIsRaisedPastOriginalCap(t *testing.T) {
	const realPilotBackupSize = 270 << 20 // the actual café backup.db, ut-docs#594
	if bkpMaxDBSize <= realPilotBackupSize {
		t.Fatalf("bkpMaxDBSize = %d bytes, must be well above the real pilot backup (%d bytes)",
			bkpMaxDBSize, realPilotBackupSize)
	}
}

// The cap is enforced on bytes actually streamed to the temp file, not on
// backup.db's declared size — an accurately-declared (via the normal
// zw.Create path, so no lying involved) entry that's simply larger than the
// cap must still be rejected.
func TestParseBkp_BackupDBOverCapRejected(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "1", ProductTextShort: "Espresso", SalesPrice: 2.5, ProductGroupText: "Coffee", Status: 1, ProductType: 1},
	})
	// A fresh SQLite file already carries page-size overhead (several KB) —
	// no need to synthesize thousands of rows to exceed a tiny test cap.
	withBkpMaxDBSize(t, 50)
	zipBytes := buildBkpZip(t, map[string][]byte{
		"backup.db": dbBytes,
		"meta.inf":  []byte(validMetaInfNoChecksums),
	})
	_, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if !errors.Is(err, ErrBkpTooLarge) {
		t.Fatalf("err = %v, want ErrBkpTooLarge (backup.db is %d bytes, cap is 50)", err, len(dbBytes))
	}
}

// A backup.db at or under the cap must still import normally — the cap
// isn't just a one-way trap.
func TestParseBkp_BackupDBUnderCapImportsFine(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "1", ProductTextShort: "Espresso", SalesPrice: 2.5, ProductGroupText: "Coffee", Status: 1, ProductType: 1},
	})
	withBkpMaxDBSize(t, int64(len(dbBytes))) // exactly at the boundary
	zipBytes := buildBkpZip(t, map[string][]byte{
		"backup.db": dbBytes,
		"meta.inf":  []byte(validMetaInfNoChecksums),
	})
	res, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if err != nil {
		t.Fatalf("ParseBkp at the exact cap boundary: %v", err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(res.Items))
	}
}

// TestParseBkp_MismatchedDeclaredSizeRejected checks the premise behind
// enforcing the cap on bytes actually copied rather than pre-checking
// backup.db's declared UncompressedSize64 (see ParseBkp's own comment):
// archive/zip itself already refuses to read an entry whose declared size
// doesn't match its real decompressed length — confirmed here directly
// against the stdlib, both directions (declares far less than real, and far
// more). There's no way to smuggle more or fewer real bytes past it than
// the header claims, so a declared-size pre-check would never catch
// anything the streamed byte-count enforcement doesn't already catch on its
// own; a mismatched header just surfaces as a corrupt-archive read error
// either way, which ParseBkp must — and does — handle without panicking.
func TestParseBkp_MismatchedDeclaredSizeRejected(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "1", ProductTextShort: "Espresso", SalesPrice: 2.5, ProductGroupText: "Coffee", Status: 1, ProductType: 1},
	})
	cases := []struct {
		name    string
		claimed uint64
	}{
		{"declares far less than the real content", 10},
		// Deliberately kept UNDER bkpMaxDBSize: a claim over the cap would
		// be short-circuited by ParseBkp's declared-size fast reject, and
		// this test is about what archive/zip itself does, not that gate.
		{"declares far more than the real content", 64 << 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			zipBytes := buildBkpZipWithMismatchedBackupDBSize(t, dbBytes, tc.claimed, []byte(validMetaInfNoChecksums))
			_, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
			if err == nil {
				t.Fatal("ParseBkp accepted a backup.db whose declared size doesn't match its real content — want a rejection")
			}
		})
	}
}

// countingReaderAt counts the bytes ParseBkp actually pulls out of the
// uploaded archive, so a test can tell "rejected after streaming the whole
// entry" apart from "rejected without ever touching the entry body".
type countingReaderAt struct {
	inner io.ReaderAt
	read  int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.inner.ReadAt(p, off)
	c.read += int64(n)
	return n, err
}

// TestParseBkp_OversizedEntryRejectedWithoutStreamingIt is the zip-bomb
// half of the cap (review finding, ut-docs#594): an over-cap backup.db must
// be rejected on its declared size, BEFORE its body is decompressed and
// written to the till's storage — otherwise a few-MB crafted upload costs
// the till bkpMaxDBSize+1 bytes of temp writes and the CPU to inflate them
// before the streamed byte count notices. Proven by counting bytes read off
// the archive: an entry of incompressible data far larger than the cap must
// cost only the central-directory/local-header reads (~1.1KB measured), not
// the cap's worth of body the streaming copy would otherwise inflate
// (~97KB measured at the 64KB test cap — and 1GB at the production one).
func TestParseBkp_OversizedEntryRejectedWithoutStreamingIt(t *testing.T) {
	// Incompressible, so the entry's compressed body is ~the same size as
	// its content — that's what makes the byte count discriminating.
	body := make([]byte, 2<<20)
	if _, err := rand.Read(body); err != nil {
		t.Fatalf("rand: %v", err)
	}
	withBkpMaxDBSize(t, 64<<10)
	zipBytes := buildBkpZip(t, map[string][]byte{
		"backup.db": body,
		"meta.inf":  []byte(validMetaInfNoChecksums),
	})
	counting := &countingReaderAt{inner: bytes.NewReader(zipBytes)}
	_, err := ParseBkp(counting, int64(len(zipBytes)), 2)
	if !errors.Is(err, ErrBkpTooLarge) {
		t.Fatalf("err = %v, want ErrBkpTooLarge", err)
	}
	// Measured: ~1.1KB when the declared size is rejected up front, ~97KB
	// when the entry is streamed to the cap first. 16KB sits clearly
	// between the two — well above the directory/header reads (which vary
	// a little with the temp filenames baked into the fixture) and well
	// below anything that implies the body was inflated.
	if counting.read > 16<<10 {
		t.Fatalf("read %d bytes off the archive to reject an over-cap entry — it inflated the entry up to the cap instead of rejecting on the declared size", counting.read)
	}
}

// TestParseBkp_CorruptBackupDBCRCRejected pins the guarantee ut-docs#594's
// streaming rewrite had to preserve: backup.db no longer goes through
// readZipEntry, so nothing but the io.Copy below reaching the entry's real
// EOF makes archive/zip verify its CRC32. A backup.db whose recorded CRC32
// doesn't match its bytes must still be refused — silently importing a
// corrupt till backup is the one outcome this importer must never produce.
func TestParseBkp_CorruptBackupDBCRCRejected(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "1", ProductTextShort: "Espresso", SalesPrice: 2.5, ProductGroupText: "Coffee", Status: 1, ProductType: 1},
	})
	// Truthful declared size, deliberately wrong CRC32 — so the only thing
	// that can catch it is archive/zip's checksum verification at EOF.
	zipBytes := buildBkpZipRawBackupDB(t, dbBytes, uint64(len(dbBytes)),
		crc32.ChecksumIEEE(dbBytes)+1, []byte(validMetaInfNoChecksums))
	_, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if !errors.Is(err, zip.ErrChecksum) {
		t.Fatalf("err = %v, want zip.ErrChecksum — the streamed copy must still trigger archive/zip's CRC32 check", err)
	}
}

// ut-docs#512: the speedy kasse Products table carries two tax-rate
// columns (TaxPercentage = dine-in, TaxPercentage2 = takeaway) — the real
// motivating case this card exists for. ParseBkp must map them onto
// ImportItem's TaxRateBP/TakeawayRateBP the same way the CSV path's Parse
// does, covering the same four real pairs from the issue's own café data
// (19/7 override, 7/7 no-override, 19/19 no-override — the second distinct
// 19% group, 0/0 no-override) plus the equal-pair "no override" case.
func TestParseBkp_TaxColumnsMapToTaxRates(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "1", ProductTextShort: "Cappuccino", SalesPrice: 3.50, ProductGroupText: "Coffee", Status: 1, ProductType: 1, TaxPercentage: 19.0, TaxPercentage2: 7.0},
		{ProductNumber: "2", ProductTextShort: "Sparkling Water", SalesPrice: 2.50, ProductGroupText: "Drinks", Status: 1, ProductType: 1, TaxPercentage: 7.0, TaxPercentage2: 7.0},
		{ProductNumber: "3", ProductTextShort: "Espresso", SalesPrice: 2.20, ProductGroupText: "Coffee", Status: 1, ProductType: 1, TaxPercentage: 19.0, TaxPercentage2: 19.0},
		{ProductNumber: "4", ProductTextShort: "Gift Voucher", SalesPrice: 10.00, ProductGroupText: "Misc", Status: 1, ProductType: 1, TaxPercentage: 0.0, TaxPercentage2: 0.0},
		{ProductNumber: "5", ProductTextShort: "No Tax Data", SalesPrice: 1.00, ProductGroupText: "Misc", Status: 1, ProductType: 1},
	})
	zipBytes := buildBkpZip(t, map[string][]byte{"backup.db": dbBytes, "meta.inf": []byte(validMetaInfNoChecksums)})

	res, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if err != nil {
		t.Fatalf("ParseBkp: %v", err)
	}
	if len(res.Items) != 5 {
		t.Fatalf("items = %d, want 5", len(res.Items))
	}

	capp := res.Items[0]
	if !capp.HasTax || capp.TaxRateBP != 1900 || !capp.HasTakeaway || capp.TakeawayRateBP != 700 {
		t.Errorf("Cappuccino (19,7) parsed wrong: %+v", capp)
	}
	water := res.Items[1]
	if !water.HasTax || water.TaxRateBP != 700 || !water.HasTakeaway || water.TakeawayRateBP != 700 {
		t.Errorf("Sparkling Water (7,7) parsed wrong: %+v", water)
	}
	espresso := res.Items[2]
	if !espresso.HasTax || espresso.TaxRateBP != 1900 || !espresso.HasTakeaway || espresso.TakeawayRateBP != 1900 {
		t.Errorf("Espresso (19,19) parsed wrong: %+v", espresso)
	}
	voucher := res.Items[3]
	if !voucher.HasTax || voucher.TaxRateBP != 0 || !voucher.HasTakeaway || voucher.TakeawayRateBP != 0 {
		t.Errorf("Gift Voucher (0,0) parsed wrong: %+v", voucher)
	}
	noTax := res.Items[4]
	if noTax.HasTax || noTax.HasTakeaway {
		t.Errorf("row with NULL tax cells must leave HasTax/HasTakeaway false, got %+v", noTax)
	}
}

// A tax cell that's present but not a parseable percentage must warn
// (TaxIssue), not silently drop the compliance-relevant value or block the
// row — same non-blocking treatment as the CSV path (ut-docs#512).
func TestParseBkp_UnparseableTaxWarnsButStillImports(t *testing.T) {
	dbBytes := buildBkpDBBytes(t, []bkpProductRow{
		{ProductNumber: "1", ProductTextShort: "Weird Row", SalesPrice: 3.00, ProductGroupText: "Misc", Status: 1, ProductType: 1, TaxPercentage: "n/a", TaxPercentage2: 7.0},
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
	if it.Issue != "" {
		t.Errorf("unparseable tax must not block the row's own Issue, got %q", it.Issue)
	}
	if it.HasTax {
		t.Error("unparseable tax cell must leave HasTax false")
	}
	if it.TaxIssue != TaxIssueUnparseable || it.TaxIssueRaw != "n/a" {
		t.Errorf("TaxIssue = (%q,%q), want (%q,%q)", it.TaxIssue, it.TaxIssueRaw, TaxIssueUnparseable, "n/a")
	}
	if !it.HasTakeaway || it.TakeawayRateBP != 700 {
		t.Errorf("the OTHER column (takeaway) must still parse independently: %+v", it)
	}
}

// A backup.db predating the tax columns must still import cleanly end to
// end — the data-layer fallback (bkp_products_repo_test.go) already covers
// ReadBkpProducts itself; this confirms ParseBkp doesn't choke on it either.
func TestParseBkp_NoTaxColumnsInSourceSchemaStillImports(t *testing.T) {
	tmp, err := os.CreateTemp("", "bkp-notax-*.db")
	if err != nil {
		t.Fatal(err)
	}
	path := tmp.Name()
	_ = tmp.Close()
	t.Cleanup(func() { _ = os.Remove(path) })
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`CREATE TABLE Products (
		ProductNumber TEXT, ProductTextShort TEXT, SalesPrice REAL,
		ProductGroupText TEXT, Status INTEGER, ProductType INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO Products VALUES ('1','Old Export',2.00,'Misc',1,1)`); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	dbBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	zipBytes := buildBkpZip(t, map[string][]byte{"backup.db": dbBytes, "meta.inf": []byte(validMetaInfNoChecksums)})

	res, err := ParseBkp(bytes.NewReader(zipBytes), int64(len(zipBytes)), 2)
	if err != nil {
		t.Fatalf("ParseBkp must not error on a pre-tax-column schema: %v", err)
	}
	if len(res.Items) != 1 || res.Items[0].HasTax || res.Items[0].HasTakeaway {
		t.Errorf("got %+v, want one item with no tax data", res.Items)
	}
}
