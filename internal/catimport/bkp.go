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
	// ErrBkpTooLarge is returned when meta.inf declares an uncompressed size
	// past bkpMaxMetaSize (checked before it's read into memory), or
	// backup.db exceeds bkpMaxDBSize — the latter checked both on its
	// declared size (a cheap fast reject) and, authoritatively, on the
	// actual byte count streamed to the temp file, so an oversized entry is
	// never fully decompressed into memory first (ut-docs#594). A zip-bomb
	// guard (review finding, ut-docs#511, 2026-08-09).
	ErrBkpTooLarge = errors.New("bkp: archive entry declares an uncompressed size that is too large")
)

// bkpMaxMetaSize bounds meta.inf's declared UncompressedSize64 — meta.inf is
// small JSON metadata (app version, device, licence key, per-file
// checksums) and never legitimately large, so the declared-size check
// remains a fine gate here: meta.inf is always fully buffered (see
// readZipEntry) regardless.
const bkpMaxMetaSize = 200 << 20 // 200MB

// bkpMaxDBSize bounds backup.db. It is enforced authoritatively on BYTES
// ACTUALLY WRITTEN while streaming to the temp file (see ParseBkp), so an
// enormous entry is rejected as it expands rather than after being fully
// decompressed into memory (ut-docs#594), with a cheap declared-size fast
// reject in front of it so a zip bomb doesn't get a gigabyte of temp writes
// out of the till first. A var, not a const, so tests can lower it to
// exercise the cap without needing gigantic fixtures.
//
// A real multi-year speedy kasse export can run into the hundreds of MB:
// the pilot café's own backup.db was 270MB (68,838 receipts / 159,408
// receipt lines over 22 months) — well past the original 200MB cap, which
// is what made #511's importer unable to read the one real input it will
// ever be given. 1GB leaves comfortable headroom above that.
var bkpMaxDBSize int64 = 1 << 30 // 1GB

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
	// Zip-bomb guard for meta.inf (review finding, ut-docs#511, 2026-08-09):
	// it's always fully buffered below, so checking its declared size first
	// bounds memory use regardless of how well it compresses. backup.db's
	// own guard is separate, further down — enforced on bytes actually
	// streamed, not this declared-size check (ut-docs#594).
	if metaFile.UncompressedSize64 > bkpMaxMetaSize {
		return Result{}, ErrBkpTooLarge
	}
	// backup.db's declared size is a sound FAST REJECT (review finding,
	// ut-docs#594): archive/zip refuses to read an entry whose declared
	// UncompressedSize64 doesn't match its real decompressed length in
	// either direction (verified against the stdlib — see the streaming
	// comment below), so "declares more than the cap" always means "really
	// is more than the cap": no valid archive is ever wrongly rejected
	// here. Without this, a crafted zip bomb — a few MB compressed,
	// truthfully declaring gigabytes — would have bkpMaxDBSize+1 bytes
	// (1GB) actually decompressed and written to the till's storage before
	// the byte-count check below rejected it, which is precisely the attack
	// the original guard (ut-docs#511) was added to stop for free. The
	// streamed byte count below stays the authoritative check; this is the
	// cheap gate in front of it.
	if dbFile.UncompressedSize64 > uint64(bkpMaxDBSize) {
		return Result{}, ErrBkpTooLarge
	}

	metaBytes, err := readZipEntry(metaFile)
	if err != nil {
		return Result{}, fmt.Errorf("read meta.inf: %w", err)
	}

	tmp, err := os.CreateTemp("", "ut-bkp-*.db")
	if err != nil {
		return Result{}, fmt.Errorf("create temp file for backup.db: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	// Stream backup.db straight from the zip to the temp file (ut-docs#594)
	// instead of buffering it whole in memory first — a real backup can run
	// into the hundreds of MB, and this importer targets low-memory Android
	// till hardware. Hash on the fly (for validateBkpMeta below) rather than
	// hashing a fully-buffered []byte, and cap on bytes ACTUALLY WRITTEN via
	// io.LimitReader — this is the AUTHORITATIVE cap check, so an entry that
	// somehow expands past the cap is cut off mid-stream rather than being
	// fully decompressed into memory first, which is the actual point of
	// ut-docs#594. (The declared-size gate further up is only the cheap fast
	// reject in front of this one.)
	//
	// The two can't disagree, because archive/zip refuses to let a
	// mismatched declared/actual size be read AT ALL — verified directly
	// against the stdlib's checksumReader (go1.25 archive/zip/reader.go):
	// reading past the declared size fails mid-read with zip.ErrFormat, and
	// reaching the real EOF short of the declared size fails with
	// io.ErrUnexpectedEOF, both discarding that Read's bytes. There is no
	// way to smuggle more (or fewer) real bytes through an entry than its
	// header declares, so the declared size is trustworthy once the entry
	// reads cleanly — which is exactly what makes the fast reject sound.
	//
	// The cap is checked as "bkpMaxDBSize+1": io.Copy drains the limited
	// reader to its EOF either way, so a backup.db at or under the cap still
	// reaches the real zip entry's own EOF and gets archive/zip's normal
	// CRC32 verification (surfacing as zip.ErrChecksum from the Read below);
	// only an oversized entry is cut short, and at that point the CRC never
	// gets checked — moot, since it's rejected as too large regardless.
	rc, err := dbFile.Open()
	if err != nil {
		tmp.Close()
		return Result{}, fmt.Errorf("open backup.db entry: %w", err)
	}
	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(rc, bkpMaxDBSize+1))
	rcCloseErr := rc.Close()
	closeErr := tmp.Close()
	if copyErr != nil {
		return Result{}, fmt.Errorf("write temp backup.db: %w", copyErr)
	}
	if rcCloseErr != nil {
		return Result{}, fmt.Errorf("close backup.db entry: %w", rcCloseErr)
	}
	if closeErr != nil {
		return Result{}, fmt.Errorf("close temp backup.db: %w", closeErr)
	}
	// Backstop, deliberately kept even though the declared-size gate above
	// pre-empts it for every archive archive/zip will actually read (a
	// mutation test confirmed no test in this package fails if this branch
	// is removed — that's expected, not missing coverage). It is what makes
	// the bound hold on the bytes themselves rather than on a header field,
	// so the guarantee survives anything the fast reject can't see.
	if written > bkpMaxDBSize {
		return Result{}, ErrBkpTooLarge
	}

	if err := validateBkpMeta(metaBytes, hex.EncodeToString(hasher.Sum(nil))); err != nil {
		return Result{}, err
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
		// Tax pairing (ut-docs#512): the real motivating case for this
		// card — TaxPercentage (dine-in) / TaxPercentage2 (takeaway) is
		// exactly the source data the issue's own café conversion
		// described. Same optional-cell, non-blocking-on-bad-value
		// treatment as the CSV path's Parse: blank/absent ⇒ unset, present
		// but unparseable ⇒ a warning (TaxIssue), never a blocked row —
		// compliance-sensitive, so unlike price a bad cell is reported,
		// not silently dropped. The two columns are independent: one being
		// unparseable never stops the other from parsing.
		if raw := p.TaxPercentageRaw; raw != "" {
			if bp, terr := ParseTaxRateBP(raw); terr == nil {
				item.TaxRateBP, item.HasTax = bp, true
			} else {
				item.TaxIssue, item.TaxIssueRaw = TaxIssueUnparseable, raw
			}
		}
		if raw := p.TaxPercentage2Raw; raw != "" {
			if bp, terr := ParseTaxRateBP(raw); terr == nil {
				item.TakeawayRateBP, item.HasTakeaway = bp, true
			} else {
				item.TakeawayTaxIssue, item.TakeawayTaxIssueRaw = TaxIssueUnparseable, raw
			}
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
// comment) into minor units. A bkp export can carry a German-formatted
// decimal comma ("2,90") if SalesPrice is ever TEXT-typed in the source
// (review finding, ut-docs#511, 2026-08-09: SQLite is dynamically typed
// regardless of a column's declared affinity, so a REAL column can still
// hold a comma-formatted string). The last-separator-wins heuristic this
// function pioneered for that case now lives in catimport.go's
// normalizeDecimalComma, which ParsePrice itself calls (ut-docs#586) — so
// this is just a TrimSpace wrapper. See normalizeDecimalComma's doc comment
// for the heuristic and its one accepted "2,900" ambiguity.
func parseBkpSalesPrice(raw string, currencyDecimals int) (int64, error) {
	return ParsePrice(strings.TrimSpace(raw), currencyDecimals)
}

// readZipEntry reads one zip.File fully into memory — used for meta.inf,
// which is always small. backup.db is streamed separately (see ParseBkp)
// rather than going through here, since it can run into the hundreds of MB
// (ut-docs#594). Reaching EOF is what makes archive/zip verify the entry's
// CRC32 against its recorded value — a corrupt/truncated entry surfaces as
// zip.ErrChecksum from here, the baseline archive integrity guarantee
// ParseBkp relies on for meta.inf regardless of what meta.inf's own content
// does or doesn't verify (see validateBkpMeta's doc comment).
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
//     that hash against dbSHA256Hex (the SHA-256 of the actual extracted
//     bytes, computed by the caller while streaming — see ParseBkp) and
//     hard-fails on a mismatch (ErrBkpChecksumMismatch).
//   - If NOTHING checksum-shaped is found anywhere, it does NOT hard-fail
//     the whole import over a schema guess nobody has confirmed — the
//     baseline integrity guarantee in that case is archive/zip's own CRC32
//     check, which both backup.db's streamed copy and readZipEntry (for
//     meta.inf) exercise, independent of this function.
//
// A human with a real customer's .bkp file should confirm meta.inf's
// actual shape and tighten (or loosen) this once it's known — this is a
// best-effort guess, not a verified contract.
func validateBkpMeta(metaBytes []byte, dbSHA256Hex string) error {
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

	for _, e := range backupDBEntries {
		if !strings.EqualFold(strings.TrimSpace(e.hash), dbSHA256Hex) {
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
