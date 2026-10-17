package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/universaltill/universal-till/internal/catalogtypes"
)

// Catalogue-sync reads and writes (ADR-0161, ut-docs#4429): what
// internal/catsync plans and applies an API-source import through. Every
// read takes the run's executor (the commit's *sql.Tx, or the pool for a
// preview); every write takes the commit's *sql.Tx.

// SyncItemState is the part of an item an API-source import compares a
// submitted row against.
type SyncItemState struct {
	ID         string
	Name       string
	SKU        string
	BasePrice  int64
	CategoryID string // "" = none
	TaxCodeID  string // "" = none
	// TaxRateBP is the dine-in rate of TaxCodeID (HasTax false without one).
	TaxRateBP      int64
	HasTax         bool
	IsActive       bool
	StockUntracked bool
	Description    string // "" = none
	AgeRestricted  bool
	// NetQuantityValue/NetQuantityUnit are the stored pair (both nil = none).
	NetQuantityValue *int64
	NetQuantityUnit  *string
	// Barcodes are the item's own barcodes (not its variants').
	Barcodes []string
}

// SyncItemState reads one item's comparable state; ok=false when the row
// is gone (a link whose local item was deleted).
func (r *CatalogRepo) SyncItemState(ctx context.Context, q SyncQuerier, itemID string) (SyncItemState, bool, error) {
	var st SyncItemState
	var cat, tax sql.NullString
	var rate, nqValue sql.NullInt64
	var nqUnit sql.NullString
	err := q.QueryRowContext(ctx, `
SELECT i.id, i.name, COALESCE(i.sku, ''), i.base_price, i.category_id, i.tax_code_id, t.rate_basis_points, i.is_active, i.stock_untracked,
       COALESCE(i.description, ''), i.age_restricted, i.net_quantity_value, i.net_quantity_unit
FROM items i LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE i.id = ?`, itemID).Scan(&st.ID, &st.Name, &st.SKU, &st.BasePrice, &cat, &tax, &rate, &st.IsActive, &st.StockUntracked,
		&st.Description, &st.AgeRestricted, &nqValue, &nqUnit)
	if errors.Is(err, sql.ErrNoRows) {
		return SyncItemState{}, false, nil
	}
	if err != nil {
		return SyncItemState{}, false, fmt.Errorf("sync item state: %w", err)
	}
	st.CategoryID = cat.String
	st.TaxCodeID = tax.String
	st.TaxRateBP, st.HasTax = rate.Int64, tax.Valid && rate.Valid
	if nqValue.Valid {
		st.NetQuantityValue = &nqValue.Int64
	}
	if nqUnit.Valid {
		st.NetQuantityUnit = &nqUnit.String
	}
	rows, err := q.QueryContext(ctx, `SELECT barcode FROM item_barcodes WHERE item_id = ? ORDER BY is_primary DESC, barcode`, itemID)
	if err != nil {
		return SyncItemState{}, false, fmt.Errorf("sync item barcodes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			return SyncItemState{}, false, fmt.Errorf("sync item barcodes: %w", err)
		}
		st.Barcodes = append(st.Barcodes, b)
	}
	return st, true, rows.Err()
}

// SyncBarcodeOwner resolves a normalized barcode to the item that shows it
// — directly, or as the parent of the variant that carries it (the same
// resolution itemIDForBarcodeExact uses for the catalogue screen).
func (r *CatalogRepo) SyncBarcodeOwner(ctx context.Context, q SyncQuerier, barcode string) (string, bool, error) {
	var itemID string
	err := q.QueryRowContext(ctx, `SELECT item_id FROM item_barcodes WHERE barcode = ?`, barcode).Scan(&itemID)
	if err == nil {
		return itemID, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("sync barcode owner: %w", err)
	}
	err = q.QueryRowContext(ctx, `
SELECT v.item_id FROM variant_barcodes b JOIN item_variants v ON v.id = b.variant_id
WHERE b.barcode = ?`, barcode).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("sync barcode owner: %w", err)
	}
	return itemID, true, nil
}

// SyncSKUOwner resolves a SKU to the item that carries it.
func (r *CatalogRepo) SyncSKUOwner(ctx context.Context, q SyncQuerier, sku string) (string, bool, error) {
	var itemID string
	err := q.QueryRowContext(ctx, `SELECT id FROM items WHERE sku = ?`, sku).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("sync sku owner: %w", err)
	}
	return itemID, true, nil
}

// GetItemTx is GetItem through the caller's transaction.
func (r *CatalogRepo) GetItemTx(ctx context.Context, tx *sql.Tx, itemID string) (catalogtypes.ItemInput, bool, error) {
	return getItemExec(ctx, tx, itemID)
}

// UpdateItemTx is UpdateItemReturningWasActive's write inside the caller's
// transaction: the full-row update, the active state cascaded to the
// item's variants, and a price change recorded in price_history — the same
// three steps the item editor's save takes, so a synced price change is
// indistinguishable from a manual one.
func (r *CatalogRepo) UpdateItemTx(ctx context.Context, tx *sql.Tx, in catalogtypes.ItemInput) error {
	if err := validateItemUpdate(in); err != nil {
		return err
	}
	prev, ok, err := getItemExec(ctx, tx, in.ID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("update item: %s not found", in.ID)
	}
	oldPrice, priceKnown, _ := resolveCurrentPriceExec(ctx, tx, in.ID, "")
	if err := updateItemExec(ctx, tx, in); err != nil {
		return err
	}
	if err := cascadeItemActiveToVariants(ctx, tx, in.ID, prev.IsActive, in.IsActive); err != nil {
		return fmt.Errorf("update item: %w", err)
	}
	if priceKnown {
		if err := recordPriceChangeExec(ctx, tx, in.ID, "", oldPrice, in.BasePrice, time.Now()); err != nil {
			return fmt.Errorf("update item: record price change: %w", err)
		}
	}
	return nil
}

// ErrSavepoint marks a savepoint statement itself failing: the caller's
// transaction is then in an unknown state and must be rolled back whole.
var ErrSavepoint = errors.New("savepoint failed")

var savepointName = regexp.MustCompile(`^[a-z_]{1,32}$`)

// WithSavepoint runs fn inside a named savepoint of tx: fn's error rolls
// back only what fn wrote, leaving the rest of the transaction intact (an
// API-source import turns one row's failure into a per-row issue, never a
// failed batch — ADR-0161 §3). name is a fixed identifier ([a-z_]), never
// input. An error wrapping ErrSavepoint means the transaction itself is no
// longer usable.
func WithSavepoint(ctx context.Context, tx *sql.Tx, name string, fn func() error) error {
	if !savepointName.MatchString(name) {
		return fmt.Errorf("%w: bad name", ErrSavepoint)
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT `+name); err != nil {
		return fmt.Errorf("%w: %v", ErrSavepoint, err)
	}
	if ferr := fn(); ferr != nil {
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT `+name); err != nil {
			return fmt.Errorf("%w: rollback: %v (after %v)", ErrSavepoint, err, ferr)
		}
		if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT `+name); err != nil {
			return fmt.Errorf("%w: release: %v (after %v)", ErrSavepoint, err, ferr)
		}
		return ferr
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT `+name); err != nil {
		return fmt.Errorf("%w: release: %v", ErrSavepoint, err)
	}
	return nil
}
