package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/catalogtypes"
)

// SeededGiftVoucherItemKey is the settings key holding the id of the gift
// voucher item setup seeded (ut-docs#4399), until an import replaces it or
// the shop is found using it.
const SeededGiftVoucherItemKey = "catalog.seeded_gift_voucher_item_id"

// seededGiftVoucherItemNameKey holds the name the seeder gave it, so an
// item the shop has renamed counts as edited.
const seededGiftVoucherItemNameKey = "catalog.seeded_gift_voucher_item_name"

// SeedDefaultGiftVoucherItem gives a new shop its gift voucher item
// (ut-docs#4259): one active item of type gift_voucher named name (the
// shop's own word for it), no price, so the voucher popup asks for the
// value, stock-untracked, with no category and a generated SKU. It is
// an ordinary item from then on, editable and hideable like any other.
//
// It does nothing (false, nil) when the shop already has a gift voucher
// item, active or not, so a wizard re-run or a till that already
// imported one never gets a second. The check, the insert and the
// SeededGiftVoucherItemKey marker share one write transaction.
func (r *CatalogRepo) SeedDefaultGiftVoucherItem(ctx context.Context, name string) (bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, errors.New("seed gift voucher item: name required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("seed gift voucher item: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM items WHERE item_type = ?)`, catalogtypes.ItemTypeGiftVoucher).Scan(&exists); err != nil {
		return false, fmt.Errorf("seed gift voucher item: check: %w", err)
	}
	if exists {
		return false, nil
	}
	id, err := r.CreateItemTx(ctx, tx, catalogtypes.ItemInput{
		Name:           name,
		IsActive:       true,
		StockUntracked: true,
		ItemType:       catalogtypes.ItemTypeGiftVoucher,
	})
	if err != nil {
		return false, fmt.Errorf("seed gift voucher item: %w", err)
	}
	for _, kv := range [][2]string{{SeededGiftVoucherItemKey, id}, {seededGiftVoucherItemNameKey, name}} {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			kv[0], kv[1], time.Now().UTC()); err != nil {
			return false, fmt.Errorf("seed gift voucher item: marker: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("seed gift voucher item: commit: %w", err)
	}
	return true, nil
}

// SeededGiftVoucherItemID returns the id SeededGiftVoucherItemKey holds.
func (r *CatalogRepo) SeededGiftVoucherItemID(ctx context.Context) (string, bool, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, SeededGiftVoucherItemKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("seeded gift voucher item: %w", err)
	}
	return id, id != "", nil
}

// RenameSeededGiftVoucherItem renames the gift voucher item setup seeded
// from from to to (ut-docs#4398): setup names it before a late language
// pack is live, so the shop got from, the English word. It acts only while
// the marker is set and the seeder wrote from, the item still carries
// from, and it is the shop's only gift voucher item; anything else means
// the shop has made its own choice, and it does nothing (false, nil). The
// seeded-name marker follows the rename, so RetireSeededGiftVoucherItem
// still sees the item as untouched. One write transaction.
func (r *CatalogRepo) RenameSeededGiftVoucherItem(ctx context.Context, from, to string) (bool, error) {
	to = strings.TrimSpace(to)
	if to == "" {
		return false, errors.New("rename seeded gift voucher item: name required")
	}
	if to == from {
		return false, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("rename seeded gift voucher item: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var id, seededName sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT value FROM settings WHERE key = ?), (SELECT value FROM settings WHERE key = ?)`,
		SeededGiftVoucherItemKey, seededGiftVoucherItemNameKey).Scan(&id, &seededName); err != nil {
		return false, fmt.Errorf("rename seeded gift voucher item: marker: %w", err)
	}
	if id.String == "" || seededName.String != from {
		return false, nil
	}
	res, err := tx.ExecContext(ctx, `
UPDATE items SET name = ?, updated_at = datetime('now')
WHERE id = ? AND name = ? AND item_type = ?
  AND (SELECT COUNT(*) FROM items WHERE item_type = ?) = 1`,
		to, id, from, catalogtypes.ItemTypeGiftVoucher, catalogtypes.ItemTypeGiftVoucher)
	if err != nil {
		return false, fmt.Errorf("rename seeded gift voucher item: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE settings SET value = ?, updated_at = ? WHERE key = ?`,
		to, time.Now().UTC(), seededGiftVoucherItemNameKey); err != nil {
		return false, fmt.Errorf("rename seeded gift voucher item: marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("rename seeded gift voucher item: commit: %w", err)
	}
	return true, nil
}

// SeededGiftVoucherRetireResult reports what RetireSeededGiftVoucherItem did.
type SeededGiftVoucherRetireResult struct {
	// Removed: the seeded item was deleted; ID and Name say which.
	Removed bool
	ID      string
	Name    string
	// InUse is the refusal when the shop already uses the seeded item.
	InUse *ItemInUseError
}

var (
	errNoOtherVoucherItem = errors.New("no other active gift voucher item")
	errSeededItemEdited   = errors.New("seeded gift voucher item was edited")
)

// seededItemUntouchedSQL: the item still looks exactly as the seeder wrote
// it (name, no price, no category or tax code, active, on the sale screen,
// no colour or icon). Takes the item id and the seeded name.
const seededItemUntouchedSQL = `
SELECT EXISTS (SELECT 1 FROM items WHERE id = ? AND name = ? AND base_price = 0
  AND category_id IS NULL AND tax_code_id IS NULL AND is_active = 1
  AND sell_screen_hidden = 0 AND sell_screen_removed = 0
  AND COALESCE(color, '') = '' AND COALESCE(icon, '') = '')`

// RetireSeededGiftVoucherItem removes the gift voucher item setup seeded
// once an import has brought the shop's own (ut-docs#4399), so the sale
// screen doesn't show two voucher tiles on day one. It acts only when the
// marker is set, another active gift voucher item exists, and the seeded
// item is still exactly as seeded; it deletes only through
// DeleteUnusedItem's rules (never sold, no stock history, not on a quick
// button or in a parked sale, no active voucher in the shop). Those
// checks and the delete share one write transaction. The caller checks
// the in-memory baskets first, as for the delete_item directive.
//
// Once decided (removed, already gone, edited by the shop, or kept
// because it is in use) the marker is cleared, so no later import removes
// an item the shop has made its own. With no other voucher item the
// marker stays.
func (r *POSRepo) RetireSeededGiftVoucherItem(ctx context.Context) (SeededGiftVoucherRetireResult, error) {
	var res SeededGiftVoucherRetireResult
	id, ok, err := NewCatalogRepo(r.db).SeededGiftVoucherItemID(ctx)
	if err != nil || !ok {
		return res, err
	}
	var seededName string
	if err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, seededGiftVoucherItemNameKey).Scan(&seededName); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return res, fmt.Errorf("retire seeded gift voucher item: %w", err)
	}
	del, err := r.deleteUnusedItem(ctx, id, func(tx *sql.Tx) error {
		var other, untouched bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM items WHERE item_type = ? AND is_active = 1 AND id <> ?)`,
			catalogtypes.ItemTypeGiftVoucher, id).Scan(&other); err != nil {
			return fmt.Errorf("retire seeded gift voucher item: %w", err)
		}
		if !other {
			return errNoOtherVoucherItem
		}
		if err := tx.QueryRowContext(ctx, seededItemUntouchedSQL, id, seededName).Scan(&untouched); err != nil {
			return fmt.Errorf("retire seeded gift voucher item: %w", err)
		}
		if !untouched {
			return errSeededItemEdited
		}
		return nil
	})
	var inUse *ItemInUseError
	switch {
	case errors.Is(err, errNoOtherVoucherItem):
		return res, nil
	case errors.Is(err, errSeededItemEdited):
	case errors.As(err, &inUse):
		res.InUse = inUse
	case err != nil:
		return res, fmt.Errorf("retire seeded gift voucher item: %w", err)
	case !del.AlreadyDeleted:
		res.Removed, res.ID, res.Name = true, id, del.Name
	}
	return res, r.ForgetSeededGiftVoucherItem(ctx)
}

// ForgetSeededGiftVoucherItem clears SeededGiftVoucherItemKey: the seeded
// item is the shop's own from now on and no import retires it.
func (r *POSRepo) ForgetSeededGiftVoucherItem(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM settings WHERE key IN (?, ?)`, SeededGiftVoucherItemKey, seededGiftVoucherItemNameKey); err != nil {
		return fmt.Errorf("forget seeded gift voucher item: %w", err)
	}
	return nil
}

// ActiveGiftVoucherItemID returns the id of the shop's first active gift
// voucher item (oldest first) — the base of a top-up basket line added from
// the voucher page (ut-docs#4252). ok=false with a nil error when the shop
// has none.
func (r *CatalogRepo) ActiveGiftVoucherItemID(ctx context.Context) (string, bool, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM items WHERE item_type = ? AND is_active = 1 ORDER BY rowid LIMIT 1`, catalogtypes.ItemTypeGiftVoucher).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("active gift voucher item: %w", err)
	}
	return id, true, nil
}
