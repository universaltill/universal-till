package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// The repository write path behind the save_item_variant directive
// (ut-docs#3477; manage-shop-catalog-api §3): an item's variants added,
// edited, deactivated and reactivated from my. Like SaveItem it is ONE
// transaction (BEGIN IMMEDIATE via the DSN's _txlock=immediate) and
// idempotent — the cloud re-serves a directive whose result post was lost,
// and re-applying leaves the same state. A variant is never deleted here:
// active=false is the only way to retire one.

// MaxItemVariants caps how many variants (active or not) one item may
// have, the same bound as the cloud's pre-queue check (≤100 per item,
// ut-docs#3477) and the catalog snapshot's per-item variant cap.
const MaxItemVariants = 100

// ErrVariantNotFound reports a save_item_variant edit for a variant id
// this till does not have.
var ErrVariantNotFound = errors.New("variant not found on this till")

// VariantSave is a save_item_variant directive: nil = absent (keep).
// Barcodes is the FULL variant-level set (an empty list clears it; the
// first barcode is the primary).
type VariantSave struct {
	ItemID     string
	ID         string
	Create     bool
	Name       *string
	SKU        *string
	PriceMinor *int64
	Active     *bool
	Barcodes   *[]string
	// RetiredWithItem, when set, is written as given — the cloud
	// catalogue pull mirrors the cloud's flag (ut-docs#4521). It only
	// holds on an inactive variant: an active one always stores false.
	// nil = the directive rule: a write that changes is_active clears it.
	RetiredWithItem *bool
	// OptionValueIDs, when set, is the variant's FULL set of option values
	// (item_variant_options, the generator's provenance; [] clears it):
	// at most maxOptionSetAxes values, each an option value on this till,
	// at most one per option set. nil = keep.
	OptionValueIDs *[]string
}

// VariantSaveResult reports what SaveVariant did. Name is the variant's
// own name ("Large"), ItemName its item's ("Flat White").
type VariantSaveResult struct {
	Created  bool
	Name     string
	ItemName string
	// Changed lists the patch's fields that were present, for the audit row.
	Changed []string
}

type variantRow struct {
	itemID, name, sku string
	price             int64
	active            bool
}

// SaveVariant applies a save_item_variant directive in one transaction:
// create-with-id (a blank SKU generates a VAR- code, as CreateVariant
// does) or update (a price change appends price history, as UpdateVariant
// does), the full variant-barcode-set replace, and deactivate/reactivate.
// A barcode used by another item or variant fails with a
// *BarcodeConflictError carrying the barcode; a SKU used by an item or
// another variant fails with ErrSKUExists naming the owner.
func (r *CatalogRepo) SaveVariant(ctx context.Context, p VariantSave) (VariantSaveResult, error) {
	p, barcodes, err := r.prepareVariantSave(ctx, p, true)
	if err != nil {
		return VariantSaveResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return VariantSaveResult{}, fmt.Errorf("save variant: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := r.saveVariantTx(ctx, tx, p, barcodes)
	if err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("save variant: commit: %w", err)
	}
	return res, nil
}

// SaveVariantTx is SaveVariant inside the caller's transaction — the
// cloud catalogue pull applies up to 100 rows per transaction (ADR-0162
// §4, ut-docs#4521) through the same validation and write path, as
// SaveItemTx does. Barcodes are refused here for SaveItemTx's reason:
// resolving them reads the shop's symbology settings through the pool,
// which must not run inside a held write transaction (the pull writes
// barcodes one row at a time through PutBarcodeTx instead).
func (r *CatalogRepo) SaveVariantTx(ctx context.Context, tx *sql.Tx, p VariantSave) (VariantSaveResult, error) {
	if p.Barcodes != nil {
		return VariantSaveResult{}, errors.New("save variant: barcodes cannot be saved inside a caller's transaction")
	}
	p, _, err := r.prepareVariantSave(ctx, p, false)
	if err != nil {
		return VariantSaveResult{}, err
	}
	return r.saveVariantTx(ctx, tx, p, nil)
}

// prepareVariantSave is SaveVariant's validation that needs no write
// transaction: it trims and checks every field, and (resolveBarcodes)
// resolves the barcode set through the pool. A malformed patch never takes
// the write lock.
func (r *CatalogRepo) prepareVariantSave(ctx context.Context, p VariantSave, resolveBarcodes bool) (VariantSave, []resolvedBarcode, error) {
	res := p
	p.ID, p.ItemID = strings.TrimSpace(p.ID), strings.TrimSpace(p.ItemID)
	if p.ID == "" {
		return res, nil, errors.New("missing variant_id")
	}
	if p.ItemID == "" {
		return res, nil, errors.New("missing item_id")
	}
	// Everything that needs no database first, so a malformed patch never
	// takes the write lock.
	if p.Name != nil {
		n := strings.TrimSpace(*p.Name)
		if n == "" {
			return res, nil, errors.New("the variant name must not be blank")
		}
		p.Name = &n
	}
	if p.PriceMinor != nil && *p.PriceMinor < 0 {
		return res, nil, errors.New("the price must not be negative")
	}
	if p.PriceMinor != nil && *p.PriceMinor > maxItemPriceMinor {
		return res, nil, fmt.Errorf("the price must be at most %d in minor units", maxItemPriceMinor)
	}
	if p.SKU != nil {
		s := strings.TrimSpace(*p.SKU)
		p.SKU = &s
	}
	if p.OptionValueIDs != nil {
		if len(*p.OptionValueIDs) > maxOptionSetAxes {
			return res, nil, fmt.Errorf("a variant combines at most %d option values", maxOptionSetAxes)
		}
		ids := make([]string, 0, len(*p.OptionValueIDs))
		seen := make(map[string]bool, len(*p.OptionValueIDs))
		for _, id := range *p.OptionValueIDs {
			id = strings.TrimSpace(id)
			if id == "" {
				return res, nil, errors.New("an option value id must not be blank")
			}
			if seen[id] {
				return res, nil, fmt.Errorf("option value %s is listed twice", id)
			}
			seen[id] = true
			ids = append(ids, id)
		}
		p.OptionValueIDs = &ids
	}
	var barcodes []resolvedBarcode
	if p.Barcodes != nil && resolveBarcodes {
		var err error
		if barcodes, err = r.resolveBarcodeSet(ctx, *p.Barcodes, "a variant"); err != nil {
			return res, nil, err
		}
	}
	return p, barcodes, nil
}

// saveVariantTx is SaveVariant's write inside tx, for a patch
// prepareVariantSave returned.
func (r *CatalogRepo) saveVariantTx(ctx context.Context, tx *sql.Tx, p VariantSave, barcodes []resolvedBarcode) (VariantSaveResult, error) {
	var res VariantSaveResult
	var itemName string
	var itemUntracked bool
	err := tx.QueryRowContext(ctx, `SELECT name, stock_untracked FROM items WHERE id = ?`, p.ItemID).Scan(&itemName, &itemUntracked)
	if errors.Is(err, sql.ErrNoRows) {
		return res, fmt.Errorf("item %s does not exist on this till", p.ItemID)
	}
	if err != nil {
		return res, fmt.Errorf("save variant: load item: %w", err)
	}
	res.ItemName = itemName

	cur, exists, err := getVariantTx(ctx, tx, p.ID)
	if err != nil {
		return res, err
	}
	if exists && cur.itemID != p.ItemID {
		return res, fmt.Errorf("variant %s belongs to another item", p.ID)
	}
	if !exists {
		if !p.Create {
			return res, ErrVariantNotFound
		}
		if p.Name == nil {
			return res, errors.New("a new variant needs a name")
		}
		if p.PriceMinor == nil {
			return res, errors.New("a new variant needs a price")
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM item_variants WHERE item_id = ?`, p.ItemID).Scan(&n); err != nil {
			return res, fmt.Errorf("save variant: count variants: %w", err)
		}
		if n >= MaxItemVariants {
			return res, fmt.Errorf("%s already has %d variants; an item can have at most %d", itemName, n, MaxItemVariants)
		}
		sku := ""
		if p.SKU != nil {
			sku = *p.SKU
		}
		active := p.Active == nil || *p.Active
		if err := r.insertVariantTx(ctx, tx, p.ID, p.ItemID, sku, *p.Name, *p.PriceMinor, active); err != nil {
			return res, err
		}
		if !itemUntracked {
			// Best-effort, same as CreateVariant: a stockless deployment
			// still gets its variant.
			if err := ensureInventoryRowExec(ctx, tx, "", p.ID); err != nil {
				logging.L().Warnf("catalog: save variant %s: inventory row not created: %v", p.ID, err)
			}
		}
		if cur, _, err = getVariantTx(ctx, tx, p.ID); err != nil {
			return res, err
		}
		res.Created = true
		res.Changed = append(res.Changed, "name", "price_minor", "sku", "active")
		p.Name, p.PriceMinor, p.SKU, p.Active = nil, nil, nil, nil // already written
	} else if p.SKU != nil && *p.SKU == "" {
		if p.Create {
			// A re-served create (its result post was lost): blank means
			// "generate one" on create, and the variant already has its
			// SKU, so the field reads as absent.
			p.SKU = nil
		} else {
			return res, errors.New("the sku must not be blank")
		}
	}

	if p.Name != nil || p.PriceMinor != nil || p.SKU != nil || p.Active != nil {
		oldPrice, priceKnown, _ := resolveCurrentPriceExec(ctx, tx, "", p.ID)
		if p.Name != nil {
			cur.name = *p.Name
			res.Changed = append(res.Changed, "name")
		}
		if p.PriceMinor != nil {
			cur.price = *p.PriceMinor
			res.Changed = append(res.Changed, "price_minor")
		}
		if p.SKU != nil {
			if *p.SKU != cur.sku {
				if err := variantSKUFreeTx(ctx, tx, *p.SKU); err != nil {
					return res, err
				}
			}
			cur.sku = *p.SKU
			res.Changed = append(res.Changed, "sku")
		}
		if p.Active != nil {
			cur.active = *p.Active
			res.Changed = append(res.Changed, "active")
		}
		// ut-docs#4348: only a write that actually CHANGES is_active is an
		// explicit per-variant switch and clears retired_with_item (the
		// variant no longer follows its item back on). my. may echo the
		// unchanged active state on any edit, which keeps the flag — same
		// rule as UpdateVariant. (SQLite evaluates the CASE against the
		// pre-update is_active.)
		if _, err := tx.ExecContext(ctx, `UPDATE item_variants SET name = ?, sku = ?, price = ?, retired_with_item = CASE WHEN is_active = ? THEN retired_with_item ELSE 0 END, is_active = ?, updated_at = datetime('now') WHERE id = ?`,
			cur.name, nullableString(cur.sku), cur.price, boolToInt(cur.active), boolToInt(cur.active), p.ID); err != nil {
			if isUniqueViolation(err) {
				return res, variantSKUTakenError(ctx, tx, cur.sku, p.ID)
			}
			return res, fmt.Errorf("save variant: %w", err)
		}
		if priceKnown {
			if err := recordPriceChangeExec(ctx, tx, "", p.ID, oldPrice, cur.price, time.Now()); err != nil {
				return res, fmt.Errorf("save variant: record price change: %w", err)
			}
		}
	}

	if p.RetiredWithItem != nil {
		retired := *p.RetiredWithItem && !cur.active
		if _, err := tx.ExecContext(ctx, `UPDATE item_variants SET retired_with_item = ? WHERE id = ?`, boolToInt(retired), p.ID); err != nil {
			return res, fmt.Errorf("save variant: retired_with_item: %w", err)
		}
		res.Changed = append(res.Changed, "retired_with_item")
	}
	if p.OptionValueIDs != nil {
		if err := replaceVariantOptionValuesTx(ctx, tx, p.ID, *p.OptionValueIDs); err != nil {
			return res, err
		}
		res.Changed = append(res.Changed, "option_value_ids")
	}
	if p.Barcodes != nil {
		if err := replaceVariantBarcodesTx(ctx, tx, p.ID, barcodes); err != nil {
			return res, err
		}
		res.Changed = append(res.Changed, "barcodes")
	}
	res.Name = cur.name
	return res, nil
}

// replaceVariantOptionValuesTx makes ids the variant's full
// item_variant_options set. Every id must be an option value on this till,
// at most one per option set (a combination); anything else refuses the
// whole save. A value of a set the item no longer uses is kept: it is
// provenance only, and the cloud may keep it after detaching the set, so
// refusing it would refuse every later edit of the variant.
func replaceVariantOptionValuesTx(ctx context.Context, tx *sql.Tx, variantID string, ids []string) error {
	sets := make(map[string]bool, len(ids))
	for _, id := range ids {
		var set string
		err := tx.QueryRowContext(ctx, `SELECT option_set_id FROM option_set_values WHERE id = ?`, id).Scan(&set)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("option value %s does not exist on this till", id)
		}
		if err != nil {
			return fmt.Errorf("save variant: check option value: %w", err)
		}
		if sets[set] {
			return fmt.Errorf("option value %s repeats an option set the variant already has a value of", id)
		}
		sets[set] = true
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM item_variant_options WHERE variant_id = ?`, variantID); err != nil {
		return fmt.Errorf("save variant: clear option values: %w", err)
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO item_variant_options (variant_id, option_set_value_id) VALUES (?, ?)`, variantID, id); err != nil {
			return fmt.Errorf("save variant: write option value: %w", err)
		}
	}
	return nil
}

// DeactivateVariantTx retires a variant inside the caller's transaction
// — the cloud pull's tombstone (a variant is never deleted, SaveVariant's
// rule). Like DeactivateVariant it is an explicit switch-off, so
// retired_with_item clears. It reports whether an active variant was
// switched off; a missing or already inactive one is a no-op.
func (r *CatalogRepo) DeactivateVariantTx(ctx context.Context, tx *sql.Tx, variantID string) (bool, error) {
	res, err := tx.ExecContext(ctx, `UPDATE item_variants SET is_active = 0, retired_with_item = 0, updated_at = datetime('now') WHERE id = ? AND is_active = 1`, variantID)
	if err != nil {
		return false, fmt.Errorf("deactivate variant: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("deactivate variant: %w", err)
	}
	return n > 0, nil
}

func getVariantTx(ctx context.Context, tx *sql.Tx, id string) (variantRow, bool, error) {
	var v variantRow
	err := tx.QueryRowContext(ctx, `SELECT item_id, name, COALESCE(sku, ''), price, is_active FROM item_variants WHERE id = ?`, id).
		Scan(&v.itemID, &v.name, &v.sku, &v.price, &v.active)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	if err != nil {
		return v, false, fmt.Errorf("save variant: load variant: %w", err)
	}
	return v, true, nil
}

// insertVariantTx is CreateVariant's insert inside the caller's
// transaction: a blank sku gets a generated VAR- code (ut-docs#1900),
// retried a bounded number of times on the astronomically unlikely
// collision. A SQLite constraint failure rolls back only its statement, so
// the retry stays inside the same transaction.
func (r *CatalogRepo) insertVariantTx(ctx context.Context, tx *sql.Tx, id, itemID, sku, name string, price int64, active bool) error {
	autoSKU := sku == ""
	attempts := 1
	if autoSKU {
		attempts = 3
	} else if err := variantSKUFreeTx(ctx, tx, sku); err != nil {
		return err
	}
	for attempt := 0; attempt < attempts; attempt++ {
		if autoSKU {
			sku = generatedVariantSKU()
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO item_variants (id, item_id, sku, name, price, is_active) VALUES (?, ?, ?, ?, ?, ?)`,
			id, itemID, sku, name, price, boolToInt(active))
		if err == nil {
			return nil
		}
		if !isUniqueViolation(err) {
			return fmt.Errorf("save variant: insert: %w", err)
		}
		if !autoSKU {
			return variantSKUTakenError(ctx, tx, sku, id)
		}
	}
	return fmt.Errorf("save variant: could not generate a free SKU: %w", ErrSKUExists)
}

// variantSKUFreeTx refuses a variant SKU an item already uses: item and
// variant SKUs are separate UNIQUE columns, but a till (and the cloud's
// pre-queue check) treats them as one namespace so a typed SKU finds one
// thing. Another variant's SKU is caught by the UNIQUE constraint.
func variantSKUFreeTx(ctx context.Context, tx *sql.Tx, sku string) error {
	if sku == "" {
		return nil
	}
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT name FROM items WHERE sku = ?`, sku).Scan(&owner)
	if err == nil {
		return fmt.Errorf("SKU %s is already used by %s: %w", sku, owner, ErrSKUExists)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("save variant: check sku: %w", err)
	}
	return nil
}

// variantSKUTakenError names the variant that already holds sku, as
// "<item name> <variant name>".
func variantSKUTakenError(ctx context.Context, tx *sql.Tx, sku, selfID string) error {
	var owner string
	if err := tx.QueryRowContext(ctx, `
SELECT TRIM(i.name || ' ' || v.name) FROM item_variants v JOIN items i ON i.id = v.item_id
WHERE v.sku = ? AND v.id <> ?`, sku, selfID).Scan(&owner); err == nil {
		return fmt.Errorf("SKU %s is already used by %s: %w", sku, owner, ErrSKUExists)
	}
	return fmt.Errorf("SKU %s is already used by another variant: %w", sku, ErrSKUExists)
}

// replaceVariantBarcodesTx is replaceItemBarcodesTx for one variant: want
// (already resolved to stored keys) becomes the variant's full barcode
// set, want[0] primary — unlisted barcodes are deleted, the rest upserted.
// Availability uses the same ensureBarcodeAvailable AddBarcode does, but
// WITHOUT AddBarcode's active check: an inactive variant's barcodes are
// editable too.
func replaceVariantBarcodesTx(ctx context.Context, tx *sql.Tx, variantID string, want []resolvedBarcode) error {
	keep := make(map[string]bool, len(want))
	for _, b := range want {
		keep[b.key] = true
		if err := ensureBarcodeAvailable(ctx, tx, b.key, "variant", variantID); err != nil {
			var conflict *BarcodeConflictError
			if errors.As(err, &conflict) {
				conflict.Barcode = b.raw
			}
			return fmt.Errorf("barcode %s: %w", b.raw, err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT barcode FROM variant_barcodes WHERE variant_id = ?`, variantID)
	if err != nil {
		return fmt.Errorf("save variant: read barcodes: %w", err)
	}
	var drop []string
	for rows.Next() {
		var bc string
		if err := rows.Scan(&bc); err != nil {
			rows.Close()
			return fmt.Errorf("save variant: read barcodes: %w", err)
		}
		if !keep[bc] {
			drop = append(drop, bc)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("save variant: read barcodes: %w", err)
	}
	for _, bc := range drop {
		if _, err := tx.ExecContext(ctx, `DELETE FROM variant_barcodes WHERE barcode = ? AND variant_id = ?`, bc, variantID); err != nil {
			return fmt.Errorf("save variant: delete barcode: %w", err)
		}
	}
	for i, b := range want {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO variant_barcodes (barcode, variant_id, barcode_type, is_primary)
VALUES (?, ?, ?, ?)
ON CONFLICT(barcode) DO UPDATE SET barcode_type = excluded.barcode_type, is_primary = excluded.is_primary
WHERE variant_barcodes.variant_id = excluded.variant_id`, b.key, variantID, b.typ, boolToInt(i == 0)); err != nil {
			return fmt.Errorf("save variant: write barcode %s: %w", b.raw, err)
		}
	}
	return nil
}
