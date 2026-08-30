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
	// TaxPercentageRaw/TaxPercentage2Raw (ut-docs#512): the source's
	// dine-in / takeaway VAT rate columns — the real motivating case this
	// card exists for (Germany's §12 UStG split; a real café's catalog
	// conversion carries these). Formatted back to plain strings the same
	// way SalesPriceRaw is, fed through catimport.ParseTaxRateBP by the
	// caller. Blank when the column is absent from this backup.db's schema
	// (see the fallback query below) or the cell itself is NULL/empty —
	// both are "no tax data for this row", never an error.
	TaxPercentageRaw  string
	TaxPercentage2Raw string
	// ProductImagePath (ut-docs#1223): the source's own reference to a
	// product photo — a path resolved against the .bkp archive's
	// documents.zip member names by the caller (internal/catimport/bkp.go).
	// Blank when absent from this backup.db's schema (older exports) or
	// the cell itself is NULL/empty — both mean "no image for this row",
	// same optional-column treatment as the tax columns above.
	ProductImagePath string
}

// The Products query is BUILT FROM THE SCHEMA, not hard-coded (ut-docs#968).
//
// ut-docs#511 and #512 had to guess this schema from a ticket's prose and
// guessed wrong in the same way twice: the real speedy kasse PRO 4.4.08
// Products table has **no ProductGroupText column at all** — the category
// name lives on ProductGroups, keyed by Products.ProductGroupID — and the
// old "no such column" fallback then re-ran the SAME missing column minus
// the tax columns, so a real backup failed twice and reported it as a
// "no-tax fallback" error that named the wrong cause.
//
// Introspecting first is what stops the next unseen variant becoming the
// next outage: every column below is optional, and a backup missing any of
// them reads narrower rather than failing.
//
// ORDER BY rowid (review finding, ut-docs#511, 2026-08-09): without an
// explicit order, which of several same-PLU rows "wins" (registers first,
// so a later duplicate is the one flagged) is whatever order SQLite's query
// planner happens to pick — not contractual, and liable to change if the
// source ever gains an index on ProductNumber (its natural PLU lookup key).
// rowid is the source file's own insertion order, the only deterministic
// ordering available without assuming anything about the source schema.

// bkpTableColumns returns the column names of table t, lowercased, or an
// empty set if the table does not exist. A missing table is not an error:
// ProductGroups is absent from older exports, and the caller degrades to an
// empty category rather than refusing the import.
func bkpTableColumns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, fmt.Errorf("introspect %s: %w", table, err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan %s column name: %w", table, err)
		}
		cols[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s columns: %w", table, err)
	}
	return cols, nil
}

// buildBkpProductsQuery assembles the SELECT for the schema actually present.
// It always yields the same eight result columns, in the same order, with a
// literal NULL standing in for anything this backup does not carry — so the
// scan below never has to branch on shape.
func buildBkpProductsQuery(productCols, groupCols map[string]bool) string {
	col := func(name, fallback string) string {
		if productCols[strings.ToLower(name)] {
			return "p." + name
		}
		return fallback
	}

	// Category: prefer a denormalised column when a backup has one (older
	// exports and this package's own older fixtures do), otherwise join.
	category := "NULL"
	join := ""
	switch {
	case productCols["productgrouptext"]:
		category = "p.ProductGroupText"
	// The join KEY is introspected on both sides, not just the display
	// column (independent review, ut-docs#968): emitting
	// "g.ProductGroupID" because Products happens to have that column,
	// without confirming ProductGroups has it too, re-creates this card's
	// own bug one table over — the query fails with "no such column:
	// g.ProductGroupID" and the entire import dies rather than degrading
	// to an empty category, which is exactly the outcome the introspection
	// above exists to prevent.
	case productCols["productgroupid"] && groupCols["productgroupid"] &&
		(groupCols["productgrouptext"] || groupCols["productgroupname"]):
		switch {
		case groupCols["productgrouptext"] && groupCols["productgroupname"]:
			// ProductGroupText is the display name; fall back to
			// ProductGroupName when a group left it blank.
			category = "COALESCE(NULLIF(g.ProductGroupText, ''), g.ProductGroupName)"
		case groupCols["productgrouptext"]:
			category = "g.ProductGroupText"
		default:
			category = "g.ProductGroupName"
		}
		// LEFT, never INNER: a product pointing at a group id that no
		// longer exists must still import, with no category.
		join = " LEFT JOIN ProductGroups g ON g.ProductGroupID = p.ProductGroupID"
	}

	return "SELECT " + strings.Join([]string{
		col("ProductNumber", "NULL"),
		col("ProductTextShort", "NULL"),
		col("SalesPrice", "NULL"),
		category,
		col("Status", "NULL"),
		col("ProductType", "NULL"),
		col("TaxPercentage", "NULL"),
		col("TaxPercentage2", "NULL"),
		col("ProductImagePath", "NULL"),
	}, ", ") + " FROM Products p" + join + " ORDER BY p.rowid"
}

// ReadBkpProducts reads every row of an extracted speedy kasse backup.db's
// Products table. db is NOT this application's own catalog database — it's
// a *sql.DB the caller (catimport.ParseBkp) opened against a temp file
// extracted from the operator's uploaded .bkp archive, closed and removed
// by the caller once this returns.
func ReadBkpProducts(ctx context.Context, db *sql.DB) ([]BkpProductRow, error) {
	productCols, err := bkpTableColumns(ctx, db, "Products")
	if err != nil {
		return nil, err
	}
	if len(productCols) == 0 {
		return nil, fmt.Errorf("query Products: backup contains no Products table")
	}
	groupCols, err := bkpTableColumns(ctx, db, "ProductGroups")
	if err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, buildBkpProductsQuery(productCols, groupCols))
	if err != nil {
		return nil, fmt.Errorf("query Products: %w", err)
	}
	defer rows.Close()

	var out []BkpProductRow
	for rows.Next() {
		var productNumber, textShort, salesPrice, groupText, status, productType any
		var taxPct, taxPct2, imagePath any
		// Always nine columns: buildBkpProductsQuery substitutes NULL for
		// anything the backup doesn't carry, so there is no shape to branch on.
		scanErr := rows.Scan(&productNumber, &textShort, &salesPrice, &groupText, &status, &productType, &taxPct, &taxPct2, &imagePath)
		if scanErr != nil {
			return nil, fmt.Errorf("scan Products row: %w", scanErr)
		}
		out = append(out, BkpProductRow{
			ProductNumber:     bkpScanString(productNumber),
			ProductTextShort:  bkpScanString(textShort),
			SalesPriceRaw:     bkpScanString(salesPrice),
			ProductGroupText:  bkpScanString(groupText),
			Status:            bkpScanInt(status),
			ProductType:       bkpScanInt(productType),
			TaxPercentageRaw:  bkpScanString(taxPct),
			TaxPercentage2Raw: bkpScanString(taxPct2),
			ProductImagePath:  bkpScanString(imagePath),
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
