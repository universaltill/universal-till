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
	// ut-docs#4778 (owner rule 2026-10-22: an item that was never sold must
	// be deletable): live voids, comps, wastes and ID checks no longer
	// refuse — they are detached (see deleteUnusedItemWrites). Only what a
	// restore of a transaction reset would need the item for still does:
	// ItemUseArchivedShrinkage is any shrinkage row in a reset archive,
	// ItemUseArchivedAgeCheck any ID check in one.
	ItemUseArchivedShrinkage = "archived_shrinkage"
	ItemUseArchivedAgeCheck  = "archived_age_check"
	ItemUseQuickButton       = "quick_button"
	ItemUseOpenBasket        = "open_basket"
	ItemUseOpenVouchers      = "open_vouchers"
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
	case ItemUseArchivedShrinkage:
		return "its voids, comps or wastes sit in an archived reset that can still be restored — deactivate it instead"
	case ItemUseArchivedAgeCheck:
		return "its ID checks sit in an archived reset that can still be restored — deactivate it instead"
	case ItemUseQuickButton:
		return "it is on a quick button — deactivate it instead"
	case ItemUseOpenVouchers:
		return "the shop has open gift vouchers — deactivate it instead"
	default:
		return "it is in an open basket — deactivate it instead"
	}
}

// ItemDeleteResult reports what DeleteUnusedItem did.
type ItemDeleteResult struct {
	Name           string
	AlreadyDeleted bool
	// Voids, Comps, Wastes and IDChecks are the numbers of live records
	// detached from the item (ut-docs#4746, #4778). Their item_name stays, so
	// the Shrinkage report and the ID-check log keep their totals.
	Voids, Comps, Wastes, IDChecks int64
}

// DetachedNote is the audit-log line for what the delete detached, or ""
// when nothing: "item deleted with 2 voids, 1 waste, 1 ID check".
func (r ItemDeleteResult) DetachedNote() string {
	var parts []string
	for _, k := range []struct {
		n         int64
		one, many string
	}{
		{r.Voids, "void", "voids"},
		{r.Comps, "comp", "comps"},
		{r.Wastes, "waste", "wastes"},
		{r.IDChecks, "ID check", "ID checks"},
	} {
		switch {
		case k.n == 1:
			parts = append(parts, "1 "+k.one)
		case k.n > 1:
			parts = append(parts, fmt.Sprintf("%d %s", k.n, k.many))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "item deleted with " + strings.Join(parts, ", ")
}

// AddDetachedToAudit adds the detached counts (voids, comps, wastes,
// id_checks, each only when > 0) and the DetachedNote to an audit payload.
// Both delete paths (the delete_item directive and the catalogue tombstone)
// use it so their audit rows read alike.
func (r ItemDeleteResult) AddDetachedToAudit(audit map[string]any) {
	note := r.DetachedNote()
	if note == "" {
		return
	}
	for k, n := range map[string]int64{"voids": r.Voids, "comps": r.Comps, "wastes": r.Wastes, "id_checks": r.IDChecks} {
		if n > 0 {
			audit[k] = n
		}
	}
	audit["note"] = note
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

// itemStockHistorySQL: a real stock movement (live with a non-zero
// quantity, or any archived one) for the item or its variants. Takes the
// item id four times. Zero-quantity live movements move no stock and are
// removed by the delete (ut-docs#4746).
const itemStockHistorySQL = `
SELECT EXISTS (SELECT 1 FROM stock_movements WHERE item_id = ? AND quantity <> 0)
    OR EXISTS (SELECT 1 FROM stock_movements WHERE quantity <> 0 AND variant_id IN (SELECT id FROM item_variants WHERE item_id = ?))
    OR EXISTS (SELECT 1 FROM stock_movements_archive WHERE item_id = ?)
    OR EXISTS (SELECT 1 FROM stock_movements_archive WHERE variant_id IN (SELECT id FROM item_variants WHERE item_id = ?))`

// itemArchivedShrinkageSQL: the item in an archived shrinkage row. Such a
// row would make its reset batch unrestorable (ErrArchiveReferencesRemoved)
// once the item is gone. Takes the item id once.
const itemArchivedShrinkageSQL = `SELECT EXISTS (SELECT 1 FROM shrinkage_events_archive WHERE item_id = ?)`

// itemAgeCheckSQL: an ID check in a transaction-reset archive
// (age_verifications_archive, ut-docs#3340): a restore of that batch would
// need the item (ErrArchiveReferencesRemoved), so it refuses by name before
// anything changes (independent review of ut-docs#4746). LIVE ID checks do
// not refuse any more (ut-docs#4778): they are detached with the item. Takes
// the item id once.
const itemAgeCheckSQL = `SELECT EXISTS (SELECT 1 FROM age_verifications_archive WHERE item_id = ?)`

// itemOpenVouchersSQL (ut-docs#4259): a gift voucher item while the shop
// has any active voucher. A voucher sold through the item is never a sale
// line (ut-docs#4214) and carries no item id, so the guard is shop-wide.
// Takes the item id once. obsoleteItemsPredicate carries the same clause.
const itemOpenVouchersSQL = `
SELECT EXISTS (SELECT 1 FROM items WHERE id = ? AND item_type = 'gift_voucher')
   AND EXISTS (SELECT 1 FROM vouchers WHERE status = 'active')`

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
	return r.deleteUnusedItem(ctx, itemID, nil)
}

// deleteUnusedItem is DeleteUnusedItem with an optional precondition run
// inside the same write transaction, after the item is loaded and before
// any use check: a non-nil error from it aborts with nothing changed.
func (r *POSRepo) deleteUnusedItem(ctx context.Context, itemID string, pre func(*sql.Tx) error) (ItemDeleteResult, error) {
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
	if res, err = deleteUnusedItemTx(ctx, tx, itemID, pre); err != nil || res.AlreadyDeleted {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("delete item: commit: %w", err)
	}
	return res, nil
}

// DeleteUnusedItemTx is DeleteUnusedItem inside the caller's transaction
// (the cloud catalogue pull's tombstone rule, ADR-0162 §4, ut-docs#4463):
// the same use checks, the same *ItemInUseError refusal with nothing
// changed. The caller checks the in-memory baskets first, as
// DeleteUnusedItem's do.
func (r *POSRepo) DeleteUnusedItemTx(ctx context.Context, tx *sql.Tx, itemID string) (ItemDeleteResult, error) {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return ItemDeleteResult{}, errors.New("missing id")
	}
	return deleteUnusedItemTx(ctx, tx, itemID, nil)
}

func deleteUnusedItemTx(ctx context.Context, tx *sql.Tx, itemID string, pre func(*sql.Tx) error) (ItemDeleteResult, error) {
	var res ItemDeleteResult
	if err := tx.QueryRowContext(ctx, `SELECT name FROM items WHERE id = ?`, itemID).Scan(&res.Name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			res.AlreadyDeleted = true
			return res, nil
		}
		return res, fmt.Errorf("delete item: load: %w", err)
	}
	if pre != nil {
		if err := pre(tx); err != nil {
			return res, err
		}
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
		{itemStockHistorySQL, 4, ItemUseStockHistory},
		{itemArchivedShrinkageSQL, 1, ItemUseArchivedShrinkage},
		{itemAgeCheckSQL, 1, ItemUseArchivedAgeCheck},
		{`SELECT EXISTS (SELECT 1 FROM shortcut_buttons WHERE item_id = ?)`, 1, ItemUseQuickButton},
		{itemParkedSQL, 4, ItemUseOpenBasket},
		{itemOpenVouchersSQL, 1, ItemUseOpenVouchers},
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

	// Live voids, comps, wastes and ID checks do not keep the item
	// (ut-docs#4778, extending #4746): the rows are detached below and keep
	// their item_name, so the reports keep their totals.

	// Every write below sits in one savepoint: a refusal after it (the
	// cleanup predicate disagreeing) undoes them even inside a caller's
	// transaction that commits after a refusal (DeleteUnusedItemTx: the
	// catalogue pull deactivates instead), so a refused delete never leaves
	// records detached from a kept item (independent review, ut-docs#4746).
	if _, err := tx.ExecContext(ctx, `SAVEPOINT delete_unused_item`); err != nil {
		return res, fmt.Errorf("delete item: savepoint: %w", err)
	}
	res, err := deleteUnusedItemWrites(ctx, tx, itemID, res)
	if err != nil {
		if _, rbErr := tx.ExecContext(ctx, `ROLLBACK TO delete_unused_item`); rbErr != nil {
			return res, errors.Join(err, fmt.Errorf("delete item: rollback to savepoint: %w", rbErr))
		}
	}
	if _, relErr := tx.ExecContext(ctx, `RELEASE delete_unused_item`); relErr != nil && err == nil {
		return res, fmt.Errorf("delete item: release savepoint: %w", relErr)
	}
	return res, err
}

// deleteUnusedItemWrites is deleteUnusedItemTx's write half, run inside its
// savepoint once every use check passed.
func deleteUnusedItemWrites(ctx context.Context, tx *sql.Tx, itemID string, res ItemDeleteResult) (ItemDeleteResult, error) {
	variants := `SELECT id FROM item_variants WHERE item_id = ?`
	// ut-docs#4778: count each kind first, then detach them all in one
	// statement (item_id is nullable in both tables: migrations 035, 056).
	rows, err := tx.QueryContext(ctx, `SELECT reason_category, COUNT(*) FROM shrinkage_events WHERE item_id = ? GROUP BY reason_category`, itemID)
	if err != nil {
		return res, fmt.Errorf("delete item: count shrinkage: %w", err)
	}
	for rows.Next() {
		var cat string
		var n int64
		if err := rows.Scan(&cat, &n); err != nil {
			_ = rows.Close()
			return res, fmt.Errorf("delete item: count shrinkage: %w", err)
		}
		switch cat {
		case "void":
			res.Voids = n
		case "comp":
			res.Comps = n
		case "waste":
			res.Wastes = n
		}
	}
	if err := rows.Close(); err != nil {
		return res, fmt.Errorf("delete item: count shrinkage: %w", err)
	}
	if err := rows.Err(); err != nil {
		return res, fmt.Errorf("delete item: count shrinkage: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE shrinkage_events SET item_id = NULL WHERE item_id = ?`, itemID); err != nil {
		return res, fmt.Errorf("delete item: detach shrinkage: %w", err)
	}
	checks, err := tx.ExecContext(ctx, `UPDATE age_verifications SET item_id = NULL WHERE item_id = ?`, itemID)
	if err != nil {
		return res, fmt.Errorf("delete item: detach ID checks: %w", err)
	}
	res.IDChecks, _ = checks.RowsAffected()
	if _, err := tx.ExecContext(ctx, `DELETE FROM stock_movements WHERE quantity = 0 AND (item_id = ? OR variant_id IN (`+variants+`))`, itemID, itemID); err != nil {
		return res, fmt.Errorf("delete item: zero movements: %w", err)
	}
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
