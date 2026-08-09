package data

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// BkpProductRow is one row of a speedy kasse / pepperm cashbox .bkp
// backup's Products table (ut-docs#511), read directly off the operator's
// own extracted backup.db. See internal/catimport/bkp.go for the mapping/
// exclusion logic that turns these rows into catimport.ImportItem values —
// that package stays a "no literal SQL" pure parser, exactly like it is for
// the CSV path.
//
// The SQL query itself lives here, not in catimport, even though backup.db
// is a foreign file the operator uploaded — never this application's own
// catalog database — purely to keep faith with this repo's
// mechanically-enforced rule that raw SQL query text lives only under
// internal/data / internal/db (scripts/ci/guard-data-access.sh treats any
// literal SELECT/INSERT/... text elsewhere under internal/ as a CI
// failure, regardless of which database it targets).
type BkpProductRow struct {
	ProductNumber    string
	ProductTextShort string
	// SalesPriceRaw is the column's value formatted back to a plain string
	// so the caller can feed it straight through catimport.ParsePrice — the
	// same conversion+rounding path the CSV importer uses, one source of
	// truth for currency-decimals handling — regardless of whether the
	// driver handed the column back as a float64, an int64, or (a bad-price
	// row) a non-numeric string.
	SalesPriceRaw    string
	ProductGroupText string
	Status           int64
	ProductType      int64
}

// bkpProductsQuery is deliberately unexported and only ever used by
// ReadBkpProducts below — see BkpProductRow's doc comment for why the query
// text lives in this package and not in internal/catimport.
// ORDER BY rowid (review finding, ut-docs#511, 2026-08-09): without an
// explicit order, which of several same-PLU rows "wins" (registers first,
// so a later duplicate is the one flagged) is whatever order SQLite's
// query planner happens to pick — not contractual, and liable to change if
// the source ever gains an index on ProductNumber (its natural PLU lookup
// key). rowid is the source file's own insertion order, the only
// deterministic ordering available without assuming anything about the
// source schema beyond what's already documented.
const bkpProductsQuery = `SELECT ProductNumber, ProductTextShort, SalesPrice, ProductGroupText, Status, ProductType FROM Products ORDER BY rowid`

// ReadBkpProducts reads every row of an extracted speedy kasse backup.db's
// Products table. db is NOT this application's own catalog database — it's
// a *sql.DB the caller (catimport.ParseBkp) opened against a temp file
// extracted from the operator's uploaded .bkp archive, closed and removed
// by the caller once this returns.
func ReadBkpProducts(ctx context.Context, db *sql.DB) ([]BkpProductRow, error) {
	rows, err := db.QueryContext(ctx, bkpProductsQuery)
	if err != nil {
		return nil, fmt.Errorf("query Products: %w", err)
	}
	defer rows.Close()

	var out []BkpProductRow
	for rows.Next() {
		var productNumber, textShort, salesPrice, groupText, status, productType any
		if err := rows.Scan(&productNumber, &textShort, &salesPrice, &groupText, &status, &productType); err != nil {
			return nil, fmt.Errorf("scan Products row: %w", err)
		}
		out = append(out, BkpProductRow{
			ProductNumber:    bkpScanString(productNumber),
			ProductTextShort: bkpScanString(textShort),
			SalesPriceRaw:    bkpScanString(salesPrice),
			ProductGroupText: bkpScanString(groupText),
			Status:           bkpScanInt(status),
			ProductType:      bkpScanInt(productType),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Products rows: %w", err)
	}
	return out, nil
}

// bkpScanString converts whatever dynamic type SQLite/the driver handed
// back (string, []byte, int64, float64, nil, …) into a plain string.
// backup.db's real column typing is only known from the one file ut-docs#511
// describes in prose — nobody on this project has seen the actual bytes
// (see bkp.go's meta.inf validator doc comment for the same caveat) — so
// this stays defensive rather than assuming a single Go type per column.
func bkpScanString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return fmt.Sprint(t)
	}
}

// bkpScanInt is bkpScanString's counterpart for the integer-flavoured
// Status/ProductType columns, equally defensive about the driver's actual
// dynamic type.
func bkpScanInt(v any) int64 {
	switch t := v.(type) {
	case nil:
		return 0
	case int64:
		return t
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	case []byte:
		n, _ := strconv.ParseInt(strings.TrimSpace(string(t)), 10, 64)
		return n
	default:
		return 0
	}
}
