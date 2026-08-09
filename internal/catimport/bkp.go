package catimport

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // pure-Go driver: read the extracted backup.db

	"github.com/universaltill/universal-till/internal/data"
)

// speedy kasse / pepperm cashbox source-schema constants (ut-docs#511,
// com.pepperm.cashbox, app v4.4.08): Status=3 marks a row deleted in the
// till's own Products table; ProductType=4 marks the "⚠️ToGo⚠️" order-type
// toggle buttons, which flip the receipt's VAT mode and are never
// themselves sellable items.
const (
	bkpStatusDeleted        = 3
	bkpProductTypeOrderMode = 4
)

// ParseBkp/bkp.go's own reason codes (ut-docs#303/#511 pattern): stable,
// machine-readable errors the caller (pages) can errors.Is() and turn into
// a translated, operator-facing message — never raw text on screen.
var (
	// ErrBkpMissingFiles is returned when the uploaded .bkp ZIP does not
	// contain both backup.db and meta.inf.
	ErrBkpMissingFiles = errors.New("bkp: backup.db and/or meta.inf missing from archive")
	// ErrBkpInvalidMeta is returned when meta.inf is present but is not
	// valid JSON at all.
	ErrBkpInvalidMeta = errors.New("bkp: meta.inf is not valid JSON")
	// ErrBkpChecksumMismatch is returned when meta.inf carries a
	// recognisable per-file checksum entry for backup.db that does not
	// match the actual extracted bytes.
	ErrBkpChecksumMismatch = errors.New("bkp: backup.db checksum recorded in meta.inf does not match the archive contents")
	// ErrBkpTooLarge is returned when backup.db or meta.inf declares an
	// uncompressed size past bkpMaxEntrySize — a zip-bomb guard (review
	// finding, ut-docs#511, 2026-08-09), checked before either entry is
	// read into memory.
	ErrBkpTooLarge = errors.New("bkp: archive entry declares an uncompressed size that is too large")
)

// bkpMaxEntrySize bounds backup.db/meta.inf's declared UncompressedSize64
// — generous for any real speedy kasse export (the ticket's own real
// example was well under 1MB for 409 products), purely to cap how much a
// crafted upload can expand to regardless of its compressed size.
const bkpMaxEntrySize = 200 << 20 // 200MB

// ParseBkp parses a speedy kasse / pepperm cashbox till backup (ut-docs#511)
// into the same neutral Result/ImportItem shape Parse produces for CSV
// exports, so the pages layer's preview/commit flow (dedup, category
// creation, stock, barcode attach) needs no format-specific branching
// beyond choosing which parser to call.
//
// r/size are the upload's own io.ReaderAt/size — a *multipart.FileHeader's
// Size alongside its multipart.File, which already implements ReaderAt
// (see import_page.go) — so the ZIP is read directly off the upload,
// never buffered to disk as a whole file first.
func ParseBkp(r io.ReaderAt, size int64, currencyDecimals int) (Result, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return Result{}, fmt.Errorf("open zip: %w", err)
	}

	var dbFile, metaFile *zip.File
	for _, f := range zr.File {
		switch f.Name {
		case "backup.db":
			dbFile = f
		case "meta.inf":
			metaFile = f
		}
	}
	if dbFile == nil || metaFile == nil {
		return Result{}, ErrBkpMissingFiles
	}
	// Zip-bomb guard (review finding, ut-docs#511, 2026-08-09): a small
	// upload can still declare an enormous UncompressedSize64 (DEFLATE on
	// pathological input measured >1000:1 in review) — checking the
	// declared size before reading either entry bounds memory use
	// regardless of how well it compresses. The cap is generous (well
	// above any real backup.db this vendor format would produce) purely to
	// stop a crafted upload from expanding past it.
	if dbFile.UncompressedSize64 > bkpMaxEntrySize || metaFile.UncompressedSize64 > bkpMaxEntrySize {
		return Result{}, ErrBkpTooLarge
	}

	metaBytes, err := readZipEntry(metaFile)
	if err != nil {
		return Result{}, fmt.Errorf("read meta.inf: %w", err)
	}
	// Reading each zip entry fully (readZipEntry → io.ReadAll) is also the
	// baseline archive integrity guarantee: archive/zip surfaces
	// zip.ErrChecksum here if the entry's own CRC32 doesn't match, whether
	// or not meta.inf turns out to carry a verifiable checksum below.
	dbBytes, err := readZipEntry(dbFile)
	if err != nil {
		return Result{}, fmt.Errorf("read backup.db: %w", err)
	}
	if err := validateBkpMeta(metaBytes, dbBytes); err != nil {
		return Result{}, err
	}

	tmp, err := os.CreateTemp("", "ut-bkp-*.db")
	if err != nil {
		return Result{}, fmt.Errorf("create temp file for backup.db: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	_, writeErr := tmp.Write(dbBytes)
	closeErr := tmp.Close()
	if writeErr != nil {
		return Result{}, fmt.Errorf("write temp backup.db: %w", writeErr)
	}
	if closeErr != nil {
		return Result{}, fmt.Errorf("close temp backup.db: %w", closeErr)
	}

	sqlDB, err := sql.Open("sqlite", tmpPath)
	if err != nil {
		return Result{}, fmt.Errorf("open backup.db: %w", err)
	}
	defer sqlDB.Close()

	products, err := data.ReadBkpProducts(context.Background(), sqlDB)
	if err != nil {
		return Result{}, fmt.Errorf("read Products table: %w", err)
	}

	res := Result{Format: "speedy-kasse"}
	seen := map[string]bool{}
	for _, p := range products {
		name := collapseWhitespace(p.ProductTextShort)
		item := ImportItem{
			SKU:      p.ProductNumber,
			Name:     name,
			Category: p.ProductGroupText,
			// Barcode is deliberately left empty: the source carries no
			// barcodes at all, and ProductNumber is a 5-digit PLU, not a
			// barcode — it must never be run through normalizeBarcode.
		}
		switch {
		case p.Status == bkpStatusDeleted:
			item.Issue = IssueSourceDeleted
		case p.ProductType == bkpProductTypeOrderMode:
			item.Issue = IssueNotSellable
		case name == "":
			item.Issue = IssueMissingName
		case p.ProductNumber != "" && seen[p.ProductNumber]:
			// Distinct from import_page.go's DB-level
			// import.status.sku_already_in_catalog: this fires purely on a
			// collision within THIS uploaded file, before the DB is ever
			// consulted.
			item.Issue = IssueDuplicateSKUInFile
		default:
			price, perr := parseBkpSalesPrice(p.SalesPriceRaw, currencyDecimals)
			if perr != nil {
				item.Issue = IssueBadPrice
				item.IssueDetail = p.SalesPriceRaw
			} else {
				item.PriceMinor = price
			}
		}
		// Only a row that cleanly imports (no Issue at all) registers its
		// PLU (review finding, ut-docs#511, 2026-08-09): a stray Status=3
		// or ProductType=4 row sharing a PLU with a real product must never
		// falsely flag that product as a same-file duplicate — unchanged
		// from the original design. But a BAD-PRICE row must not register
		// either: if it did, a later row with the same PLU that would
		// otherwise import cleanly gets wrongly marked as a duplicate of a
		// row that itself never actually imported, silently losing a real
		// product the operator never sees a path to recover. A true
		// same-file duplicate (both rows otherwise clean) is still caught:
		// the first clean row registers, the second is flagged.
		if p.ProductNumber != "" && item.Issue == "" {
			seen[p.ProductNumber] = true
		}
		res.Items = append(res.Items, item)
	}
	return res, nil
}

// collapseWhitespace turns the source's multi-line button labels
// ("Cappuccino\nGroß") into one line with runs of whitespace collapsed —
// strings.Fields already splits on any whitespace (including newlines) and
// drops empty fields, so rejoining with a single space does exactly what's
// needed in one pass.
func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// parseBkpSalesPrice converts a raw SalesPrice cell (whatever textual or
// numeric shape modernc.org/sqlite handed back — see BkpProductRow's doc
// comment) into minor units. Unlike the CSV path's ParsePrice — built for
// machine-generated Loyverse/Square/generic exports that always use a
// plain '.' decimal point — a bkp export can carry a German-formatted
// decimal comma ("2,90") if SalesPrice is ever TEXT-typed in the source
// (review finding, ut-docs#511, 2026-08-09: SQLite is dynamically typed
// regardless of a column's declared affinity, so a REAL column can still
// hold a comma-formatted string). Silently running "2,90" through
// ParsePrice's comma-as-thousands-separator stripping produces "290" —
// a 100x price error on exactly the German till this feature exists to
// serve — so this wrapper normalises the separator first.
//
// Heuristic: whichever of ',' or '.' appears LAST in the string is treated
// as the decimal point; the other, if present, is stripped as a thousands
// separator. A string with only one kind of separator uses it as the
// decimal point outright — covers plain "2.90", German "2,90", and
// thousands-grouped "1.234,50" / "1,234.50" alike. The one accepted
// ambiguity (documented, not silently wrong): a three-digit group with no
// other separator, e.g. "2,900", could mean either €2,900.00 or a
// thousands-grouped €2.90 — treated as the decimal-comma case (€2.90) per
// this heuristic, consistent with the German source this format is for.
// This whole function is exercised only by bkp.go — the CSV path's
// ParsePrice and its own comma-as-thousands-separator behaviour
// (documented on ParsePrice itself) are unchanged.
func parseBkpSalesPrice(raw string, currencyDecimals int) (int64, error) {
	s := strings.TrimSpace(raw)
	lastComma := strings.LastIndexByte(s, ',')
	lastDot := strings.LastIndexByte(s, '.')
	if lastComma > lastDot {
		// Comma is the decimal point (German/European style) — strip '.'
		// thousands separators, then use the comma as the decimal point.
		s = strings.ReplaceAll(s, ".", "")
		s = strings.ReplaceAll(s, ",", ".")
	} else {
		// '.' is the decimal point, or there's no separator at all — strip
		// any ',' thousands separators (ParsePrice's existing behaviour).
		s = strings.ReplaceAll(s, ",", "")
	}
	return ParsePrice(s, currencyDecimals)
}

// readZipEntry reads one zip.File fully into memory. Reaching EOF is what
// makes archive/zip verify the entry's CRC32 against its recorded value —
// a corrupt/truncated entry surfaces as zip.ErrChecksum from here, the
// baseline archive integrity guarantee ParseBkp relies on regardless of
// what meta.inf does or doesn't verify (see validateBkpMeta's doc comment).
func readZipEntry(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// bkpChecksumEntry is one recognised {name-like, hash-like} pair found
// somewhere inside meta.inf's JSON — see validateBkpMeta's doc comment for
// how uncertain this shape genuinely is.
type bkpChecksumEntry struct {
	name string
	hash string
}

var bkpChecksumNameKeys = []string{"name", "file", "path"}
var bkpChecksumHashKeys = []string{"sha256", "checksum", "hash"}

// validateBkpMeta is ut-docs#511's meta.inf validator.
//
// ASSUMPTION, NOT VERIFIED — flag for a human with the real file: the
// ticket describes meta.inf only in prose ("JSON with app version, device,
// licence key and per-file checksums"); nobody on this project has actually
// seen the real bytes — the real customer's .bkp is explicitly not
// committable anywhere (see ut-docs#511's own instruction). This function
// is therefore deliberately defensive rather than strict about the
// checksum shape:
//
//   - It hard-fails if meta.inf isn't valid JSON at all (ErrBkpInvalidMeta).
//   - It recursively looks anywhere in the parsed JSON (top-level array,
//     top-level object, or nested under any key — e.g. "files", "checksums")
//     for something shaped like {<name-ish key>: "...", <hash-ish key>: "..."}
//     — name-ish being one of name/file/path, hash-ish one of
//     sha256/checksum/hash. If it finds one naming "backup.db" (matched by
//     base filename, so a path-qualified name still matches), it verifies
//     that hash against the SHA-256 of the actual extracted bytes and
//     hard-fails on a mismatch (ErrBkpChecksumMismatch).
//   - If NOTHING checksum-shaped is found anywhere, it does NOT hard-fail
//     the whole import over a schema guess nobody has confirmed — the
//     baseline integrity guarantee in that case is archive/zip's own CRC32
//     check, which readZipEntry already exercises on every entry it reads
//     (backup.db and meta.inf both), independent of this function.
//
// A human with a real customer's .bkp file should confirm meta.inf's
// actual shape and tighten (or loosen) this once it's known — this is a
// best-effort guess, not a verified contract.
func validateBkpMeta(metaBytes, dbBytes []byte) error {
	var parsed any
	if err := json.Unmarshal(metaBytes, &parsed); err != nil {
		return fmt.Errorf("%w: %v", ErrBkpInvalidMeta, err)
	}

	var backupDBEntries []bkpChecksumEntry
	for _, e := range findBkpChecksumEntries(parsed) {
		if strings.EqualFold(filepath.Base(e.name), "backup.db") {
			backupDBEntries = append(backupDBEntries, e)
		}
	}
	if len(backupDBEntries) == 0 {
		// No recognisable checksum structure for backup.db anywhere in
		// meta.inf — see the doc comment above: this is not treated as a
		// hard failure.
		return nil
	}

	sum := sha256.Sum256(dbBytes)
	actualHex := hex.EncodeToString(sum[:])
	for _, e := range backupDBEntries {
		if !strings.EqualFold(strings.TrimSpace(e.hash), actualHex) {
			return ErrBkpChecksumMismatch
		}
	}
	return nil
}

// findBkpChecksumEntries recursively walks parsed JSON (maps/slices from
// encoding/json's `any` decoding) looking for anything shaped like a
// {name, hash} checksum entry, at any depth and under any key name.
func findBkpChecksumEntries(v any) []bkpChecksumEntry {
	var out []bkpChecksumEntry
	switch val := v.(type) {
	case map[string]any:
		if e, ok := bkpChecksumEntryFromMap(val); ok {
			out = append(out, e)
		}
		for _, vv := range val {
			out = append(out, findBkpChecksumEntries(vv)...)
		}
	case []any:
		for _, vv := range val {
			out = append(out, findBkpChecksumEntries(vv)...)
		}
	}
	return out
}

// bkpChecksumEntryFromMap reports whether m looks like one checksum-list
// entry: a filename-like string field plus a hash-like string field.
func bkpChecksumEntryFromMap(m map[string]any) (bkpChecksumEntry, bool) {
	var name, hash string
	for _, k := range bkpChecksumNameKeys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			name = s
			break
		}
	}
	for _, k := range bkpChecksumHashKeys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			hash = s
			break
		}
	}
	if name != "" && hash != "" {
		return bkpChecksumEntry{name: name, hash: hash}, true
	}
	return bkpChecksumEntry{}, false
}
