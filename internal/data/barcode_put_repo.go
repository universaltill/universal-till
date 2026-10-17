package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// One barcode row at a time — the cloud catalogue pull's write path for
// the feed's item_barcode kind (ADR-0162 §4, ut-docs#4521). save_item and
// save_item_variant replace a whole barcode set; the feed sends one row
// per barcode instead, under its own cloud_id, so a row is put (created,
// changed or moved) and removed on its own.

// ResolvedBarcode is one barcode resolved exactly as AddBarcode does
// (ADR-0059 §3): Raw as sent, Key the stored lookup key, Type the
// symbology the shop's enabled set matched.
type ResolvedBarcode struct {
	Raw, Key, Type string
}

// ResolveBarcode checks one barcode (1–64 printable characters, a match
// in the shop's enabled symbologies) and resolves its stored key. It reads
// the shop's settings through the pool, so a caller that will hold a write
// transaction runs it first (resolveBarcodeSet's rule). Unlike save_item's
// path it does not fall back to the default symbologies when the settings
// read fails on storage: it returns that error, because the cloud pull
// records a refusal for good where a directive is retried. A value that
// does not parse still resolves on the defaults, as save_item does.
func (r *CatalogRepo) ResolveBarcode(ctx context.Context, code string) (ResolvedBarcode, error) {
	if _, err := r.settings.EnabledBarcodeSymbologies(ctx); IsStorageError(err) {
		return ResolvedBarcode{}, fmt.Errorf("resolve barcode: enabled symbologies: %w", err)
	}
	out, err := r.resolveBarcodeSet(ctx, []string{code}, "a barcode row")
	if err != nil {
		return ResolvedBarcode{}, err
	}
	return ResolvedBarcode{Raw: out[0].raw, Key: out[0].key, Type: out[0].typ}, nil
}

// BarcodePut is one barcode row written whole.
type BarcodePut struct {
	Barcode ResolvedBarcode
	// ItemID is the item; for a variant barcode, the variant's item (the
	// write refuses a variant of another item).
	ItemID string
	// VariantID is the variant of a variant barcode; "" = an item barcode.
	VariantID string
	// IsPrimary makes it the owner's one primary barcode: the owner's
	// other barcodes lose the flag (save_item's one-primary rule).
	IsPrimary bool
}

// PutBarcodeTx writes one barcode row inside the caller's transaction and
// reports whether it created the row (false = it refreshed a barcode the
// owner already held). The barcode must not belong to another item or
// variant (*BarcodeConflictError, as SaveItem), and an owner keeps at most
// maxItemBarcodes barcodes. Like save_item_variant, an inactive item's or
// variant's barcodes are writable.
func (r *CatalogRepo) PutBarcodeTx(ctx context.Context, tx *sql.Tx, p BarcodePut) (bool, error) {
	if p.Barcode.Key == "" || p.Barcode.Type == "" {
		return false, errors.New("put barcode: barcode not resolved")
	}
	if p.ItemID == "" {
		return false, errors.New("put barcode: missing item_id")
	}
	var one int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM items WHERE id = ?`, p.ItemID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("item %s does not exist on this till", p.ItemID)
	}
	if err != nil {
		return false, fmt.Errorf("put barcode: load item: %w", err)
	}
	targetType, targetID, table, column, owner := "item", p.ItemID, "item_barcodes", "item_id", "an item"
	if p.VariantID != "" {
		var itemID string
		err := tx.QueryRowContext(ctx, `SELECT item_id FROM item_variants WHERE id = ?`, p.VariantID).Scan(&itemID)
		if errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("variant %s does not exist on this till", p.VariantID)
		}
		if err != nil {
			return false, fmt.Errorf("put barcode: load variant: %w", err)
		}
		if itemID != p.ItemID {
			return false, fmt.Errorf("variant %s belongs to another item", p.VariantID)
		}
		targetType, targetID, table, column, owner = "variant", p.VariantID, "variant_barcodes", "variant_id", "a variant"
	}
	if err := ensureBarcodeAvailable(ctx, tx, p.Barcode.Key, targetType, targetID); err != nil {
		var conflict *BarcodeConflictError
		if errors.As(err, &conflict) {
			conflict.Barcode = p.Barcode.Raw
		}
		return false, fmt.Errorf("barcode %s: %w", p.Barcode.Raw, err)
	}
	// table and column are fixed above, never input.
	var held int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE barcode = ?`, p.Barcode.Key).Scan(&held); err != nil {
		return false, fmt.Errorf("put barcode: load: %w", err)
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE `+column+` = ? AND barcode <> ?`, targetID, p.Barcode.Key).Scan(&n); err != nil {
		return false, fmt.Errorf("put barcode: count: %w", err)
	}
	if n >= maxItemBarcodes {
		return false, fmt.Errorf("%s can have at most %d barcodes", owner, maxItemBarcodes)
	}
	if p.IsPrimary {
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET is_primary = 0 WHERE `+column+` = ? AND barcode <> ? AND is_primary <> 0`, targetID, p.Barcode.Key); err != nil {
			return false, fmt.Errorf("put barcode: demote primary: %w", err)
		}
	}
	// The WHERE guard is addBarcodeInTx's: the upsert only ever refreshes a
	// barcode the same owner already holds.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO `+table+` (barcode, `+column+`, barcode_type, is_primary) VALUES (?, ?, ?, ?)
ON CONFLICT(barcode) DO UPDATE SET barcode_type = excluded.barcode_type, is_primary = excluded.is_primary
WHERE `+table+`.`+column+` = excluded.`+column,
		p.Barcode.Key, targetID, p.Barcode.Type, boolToInt(p.IsPrimary)); err != nil {
		return false, fmt.Errorf("put barcode %s: %w", p.Barcode.Raw, err)
	}
	return held == 0, nil
}

// DeleteOwnedBarcodeTx removes the barcode stored under exactly key from
// the one owner named — the item's own barcodes when variantID is "",
// else that variant's — inside the caller's transaction, and reports
// whether a row went. A barcode another item or variant holds now is left
// alone, and unlike DeleteBarcode it never falls back to a canonical key:
// the caller names the key and owner it stored.
func (r *CatalogRepo) DeleteOwnedBarcodeTx(ctx context.Context, tx *sql.Tx, key, itemID, variantID string) (bool, error) {
	q, owner := `DELETE FROM item_barcodes WHERE barcode = ? AND item_id = ?`, itemID
	if variantID != "" {
		q, owner = `DELETE FROM variant_barcodes WHERE barcode = ? AND variant_id = ?`, variantID
	}
	res, err := tx.ExecContext(ctx, q, key, owner)
	if err != nil {
		return false, fmt.Errorf("delete barcode: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete barcode: %w", err)
	}
	return n > 0, nil
}
