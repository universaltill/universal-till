package data

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/universaltill/universal-till/internal/catalogtypes"
)

// SeedDefaultGiftVoucherItem gives a new shop its gift voucher item
// (ut-docs#4259): one active item of type gift_voucher named name (the
// shop's own word for it), no price, so the voucher popup asks for the
// value, stock-untracked, with no category and a generated SKU. It is
// an ordinary item from then on, editable and hideable like any other.
//
// It does nothing (false, nil) when the shop already has a gift voucher
// item, active or not, so a wizard re-run or a till that already
// imported one never gets a second. The check and the insert share one
// write transaction.
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
	if _, err := r.CreateItemTx(ctx, tx, catalogtypes.ItemInput{
		Name:           name,
		IsActive:       true,
		StockUntracked: true,
		ItemType:       catalogtypes.ItemTypeGiftVoucher,
	}); err != nil {
		return false, fmt.Errorf("seed gift voucher item: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("seed gift voucher item: commit: %w", err)
	}
	return true, nil
}
