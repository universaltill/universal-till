package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Item delete for the cloud's delete_item directive (ut-docs#3317): an item
// that was never sold and is not in use is deleted for good; anything else
// keeps its history and can only be deactivated. The till decides from its
// own tables, never from a flag the cloud sent along, so a stale "never
// sold" in my. cannot delete a sold item.
//
// Combos/bundles are not a product concept on the till yet; when they ship,
// a bundle that lists the item must refuse here too.

// The ways an item can be in use, in the order DeleteUnusedItem checks them.
const (
	ItemUseSold         = "sold"
	ItemUseStockHistory = "stock_history"
	ItemUseQuickButton  = "quick_button"
	ItemUseOpenBasket   = "open_basket"
)

// ItemInUseError refuses a delete. Its text is the owner-readable line the
// cloud shows verbatim ("sold 14 times — deactivate it instead").
type ItemInUseError struct {
	Reason string
	// Sales is the number of sales (live and archived) with a line for the
	// item or one of its variants. Set only for ItemUseSold.
	Sales int64
}

func (e *ItemInUseError) Error() string {
	switch e.Reason {
	case ItemUseSold:
		if e.Sales == 1 {
			return "sold once — deactivate it instead"
		}
		return fmt.Sprintf("sold %d times — deactivate it instead", e.Sales)
	case ItemUseStockHistory:
		return "it has stock history — deactivate it instead"
	case ItemUseQuickButton:
		return "it is on a quick button — deactivate it instead"
	default:
		return "it is in an open basket — deactivate it instead"
	}
}

// ItemDeleteResult reports what DeleteUnusedItem did.
type ItemDeleteResult struct {
	Name           string
	AlreadyDeleted bool
}

// itemSalesCountSQL counts the sales (live, and archived by a transaction
// reset) with a line for item ? or one of its variants. Takes the item id
// four times.
const itemSalesCountSQL = `
SELECT COUNT(DISTINCT sale_id) FROM (
  SELECT sale_id FROM sale_lines WHERE item_id = ?
  UNION ALL SELECT sale_id FROM sale_lines WHERE variant_id IN (SELECT id FROM item_variants WHERE item_id = ?)
  UNION ALL SELECT sale_id FROM sale_lines_archive WHERE item_id = ?
  UNION ALL SELECT sale_id FROM sale_lines_archive WHERE variant_id IN (SELECT id FROM item_variants WHERE item_id = ?)
)`

// itemStockHistorySQL: stock movements (live or archived) or a recorded
// void/comp/waste (live or archived, ut-docs#3452) for the item or its
// variants. Takes the item id six times. shrinkage_events.item_id has no
// cascade, so it would block the delete anyway; the archived row would
// make its reset batch unrestorable (ErrArchiveReferencesRemoved).
const itemStockHistorySQL = `
SELECT EXISTS (SELECT 1 FROM stock_movements WHERE item_id = ?)
    OR EXISTS (SELECT 1 FROM stock_movements WHERE variant_id IN (SELECT id FROM item_variants WHERE item_id = ?))
    OR EXISTS (SELECT 1 FROM stock_movements_archive WHERE item_id = ?)
    OR EXISTS (SELECT 1 FROM stock_movements_archive WHERE variant_id IN (SELECT id FROM item_variants WHERE item_id = ?))
    OR EXISTS (SELECT 1 FROM shrinkage_events WHERE item_id = ?)
    OR EXISTS (SELECT 1 FROM shrinkage_events_archive WHERE item_id = ?)`

// itemParkedSQL: a held (parked) sale or open tab, live or archived, with a
// line for the item or one of its variants — the same payload match
// obsoleteItemsPredicate uses. Takes the item id four times.
const itemParkedSQL = `
SELECT EXISTS (SELECT 1 FROM held_sales WHERE payload LIKE '%"item_id":"' || ? || '"%')
    OR EXISTS (SELECT 1 FROM held_sales h JOIN item_variants v ON v.item_id = ?
               WHERE h.payload LIKE '%"variant_id":"' || v.id || '"%')
    OR EXISTS (SELECT 1 FROM held_sales_archive WHERE payload LIKE '%"item_id":"' || ? || '"%')
    OR EXISTS (SELECT 1 FROM held_sales_archive h JOIN item_variants v ON v.item_id = ?
               WHERE h.payload LIKE '%"variant_id":"' || v.id || '"%')`

// DeleteUnusedItem permanently deletes one item that was never sold and is
// not in use, with its operational children (inventory levels and price
// history; barcodes, images, variants and links cascade), in one write
// transaction — the checks and the delete share it, so a sale committed
// meanwhile cannot slip between them. Any use refuses with *ItemInUseError
// and changes nothing. An item that is not there reports AlreadyDeleted
// (a re-applied directive). The live cashier/kiosk baskets are in memory
// only: the caller checks them first, as the catalog cleanup does.
//
// The final DELETE also carries obsoleteItemsPredicate, the catalog
// cleanup's own rule, so the two delete paths can never disagree about
// what is safe to remove.
func (r *POSRepo) DeleteUnusedItem(ctx context.Context, itemID string) (ItemDeleteResult, error) {
	var res ItemDeleteResult
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return res, errors.New("missing id")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("delete item: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := tx.QueryRowContext(ctx, `SELECT name FROM items WHERE id = ?`, itemID).Scan(&res.Name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			res.AlreadyDeleted = true
			return res, nil
		}
		return res, fmt.Errorf("delete item: load: %w", err)
	}
	var sales int64
	if err := tx.QueryRowContext(ctx, itemSalesCountSQL, itemID, itemID, itemID, itemID).Scan(&sales); err != nil {
		return res, fmt.Errorf("delete item: sales: %w", err)
	}
	if sales > 0 {
		return res, &ItemInUseError{Reason: ItemUseSold, Sales: sales}
	}
	for _, c := range []struct {
		q      string
		n      int
		reason string
	}{
		{itemStockHistorySQL, 6, ItemUseStockHistory},
		{`SELECT EXISTS (SELECT 1 FROM shortcut_buttons WHERE item_id = ?)`, 1, ItemUseQuickButton},
		{itemParkedSQL, 4, ItemUseOpenBasket},
	} {
		args := make([]any, c.n)
		for i := range args {
			args[i] = itemID
		}
		var used bool
		if err := tx.QueryRowContext(ctx, c.q, args...).Scan(&used); err != nil {
			return res, fmt.Errorf("delete item: %s: %w", c.reason, err)
		}
		if used {
			return res, &ItemInUseError{Reason: c.reason}
		}
	}

	variants := `SELECT id FROM item_variants WHERE item_id = ?`
	for _, s := range []string{
		`DELETE FROM inventory     WHERE item_id = ? OR variant_id IN (` + variants + `)`,
		`DELETE FROM price_history WHERE item_id = ? OR variant_id IN (` + variants + `)`,
	} {
		if _, err := tx.ExecContext(ctx, s, itemID, itemID); err != nil {
			return res, fmt.Errorf("delete item: children: %w", err)
		}
	}
	del, err := tx.ExecContext(ctx, `DELETE FROM items WHERE id = ? AND `+obsoleteItemsPredicate(true), itemID)
	if err != nil {
		return res, fmt.Errorf("delete item: %w", err)
	}
	if n, err := del.RowsAffected(); err != nil {
		return res, fmt.Errorf("delete item: %w", err)
	} else if n != 1 {
		// The checks above passed, so the cleanup rule only disagrees if it
		// grew a clause they lack: refuse rather than guess.
		return res, &ItemInUseError{Reason: ItemUseStockHistory}
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("delete item: commit: %w", err)
	}
	return res, nil
}

// EverSoldItemIDs returns the ids of every item that has a sale line, live
// or archived, for itself or one of its variants. The catalog snapshot
// reports it per item (ever_sold) so my. offers Delete only for an item
// that was never sold. One indexed probe per item (migration 055), so the
// cost follows the catalog's size, not the sales history's.
func (r *POSRepo) EverSoldItemIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT i.id FROM items i
WHERE EXISTS (SELECT 1 FROM sale_lines WHERE item_id = i.id)
   OR EXISTS (SELECT 1 FROM sale_lines_archive WHERE item_id = i.id)
   OR EXISTS (SELECT 1 FROM item_variants v WHERE v.item_id = i.id AND (
        EXISTS (SELECT 1 FROM sale_lines WHERE variant_id = v.id)
     OR EXISTS (SELECT 1 FROM sale_lines_archive WHERE variant_id = v.id)))`)
	if err != nil {
		return nil, fmt.Errorf("ever sold items: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("ever sold items: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}
