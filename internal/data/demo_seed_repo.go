package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/universaltill/universal-till/internal/data/seeddata"
)

// DemoSeedRepo manages the opt-in demo ("sample data") catalogue —
// ut-docs#539. The catalogue itself lives in internal/data/seeddata, the
// single source of truth for the opt-in seed/remove SQL (until the
// ADR-0074 squash, ut-docs#1425, this was also shared with migration
// 036_demo_seed_opt_in.sql, now deleted).
type DemoSeedRepo struct {
	db *sql.DB
}

func NewDemoSeedRepo(db *sql.DB) *DemoSeedRepo {
	return &DemoSeedRepo{db: db}
}

// SeedDemoCatalogue (re)inserts the demo catalogue, every item flagged
// is_sample_data = 1. Idempotent (INSERT OR IGNORE throughout) and atomic:
// either the whole catalogue lands or none of it does.
func (r *DemoSeedRepo) SeedDemoCatalogue(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin demo seed: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, seeddata.DemoCatalogueSQL); err != nil {
		return fmt.Errorf("seed demo catalogue: %w", err)
	}
	return tx.Commit()
}

// KeptDemoItem is one demo item RemoveDemoCatalogue could not remove, plus
// WHY (ut-docs#1840 AC2 — the old response reported only a bare count with
// one reason for all of them, "already in use", which is simply false for
// an item kept only because it was edited).
//
//   - ReasonEdited: sku/name/base_price no longer match the seeded values,
//     but the item has no trading history at all. Only reachable when
//     RemoveDemoCatalogue ran in STRICT mode (the till has real trading
//     history elsewhere) — in relaxed mode this reason can never remain,
//     since a relaxed-mode item blocked only by this predicate is removed,
//     not kept. Safe to remove on request (RemoveDemoItem) or to keep
//     permanently as the operator's own (KeepDemoItemAsOwn) — ut-docs#1840
//     AC3.
//   - ReasonHistory: the item (or a variant) has a live or archived
//     sale_lines/stock_movements row. Never offered a "remove anyway" —
//     doing so would either FK-fail or silently orphan a restorable
//     archive batch (ut-docs#1840's own "Do not regress" section). Points
//     the operator at Catalog cleanup instead (AC4), which handles this
//     case for any inactive, never-sold item, sample or not.
//   - ReasonHeld: the item (or a variant) is referenced by a parked
//     (held) basket, live or archived. Same non-negotiable as history —
//     removing it would FK-fail the moment that basket is tendered.
type KeptDemoItem struct {
	ID     string
	SKU    string
	Name   string
	Reason string
}

const (
	KeptReasonEdited   = "edited"
	KeptReasonHistory  = "history"
	KeptReasonHeld     = "held"
	KeptReasonTargeted = "targeted"
)

// demoTillHasNoRealHistorySQL is ut-docs#1840 AC1's till-level gate,
// exactly the definition its own acceptance criteria recommends: no
// sale_lines/stock_movements row, live or archived, that references a
// NON-sample item (directly or via a variant). It says nothing about demo
// items' own history — a demo item that has itself been sold or
// stock-adjusted while trying the till out is still individually protected
// by RemoveDemoRelaxedSQL's unchanged trading-history clauses either way,
// so relaxing this gate never widens those. When true, a shop has never
// traded for real and the pristine-match restriction has nothing left to
// protect.
const demoTillHasNoRealHistorySQL = `
SELECT NOT EXISTS (
	SELECT 1 FROM sale_lines sl JOIN items i ON i.id = sl.item_id WHERE i.is_sample_data = 0
	UNION ALL
	SELECT 1 FROM sale_lines sl JOIN item_variants v ON v.id = sl.variant_id
	                            JOIN items i ON i.id = v.item_id WHERE i.is_sample_data = 0
	UNION ALL
	SELECT 1 FROM stock_movements sm JOIN items i ON i.id = sm.item_id WHERE i.is_sample_data = 0
	UNION ALL
	SELECT 1 FROM stock_movements sm JOIN item_variants v ON v.id = sm.variant_id
	                                 JOIN items i ON i.id = v.item_id WHERE i.is_sample_data = 0
	UNION ALL
	SELECT 1 FROM sale_lines_archive sl JOIN items i ON i.id = sl.item_id WHERE i.is_sample_data = 0
	UNION ALL
	SELECT 1 FROM sale_lines_archive sl JOIN item_variants v ON v.id = sl.variant_id
	                                    JOIN items i ON i.id = v.item_id WHERE i.is_sample_data = 0
	UNION ALL
	SELECT 1 FROM stock_movements_archive sm JOIN items i ON i.id = sm.item_id WHERE i.is_sample_data = 0
	UNION ALL
	SELECT 1 FROM stock_movements_archive sm JOIN item_variants v ON v.id = sm.variant_id
	                                         JOIN items i ON i.id = v.item_id WHERE i.is_sample_data = 0
)`

// demoItemReasonCaseSQL is the per-item CASE that both keptDemoItems (bulk,
// scans every remaining is_sample_data=1 item) and RemoveDemoItem's own
// inline server-side re-check share — one definition so the two can't
// silently drift apart. `i` is the items row alias the caller's FROM/WHERE
// supplies. Mirrors remove_demo.sql's own safety predicate exactly: held
// (parked basket) outranks history (sold/adjusted) outranks edited
// (pristine mismatch only) — the two hard blockers are checked first
// regardless of which also applies, since "held" and "history" are never
// relaxed by mode but "edited" always is.
const demoItemReasonCaseSQL = `
CASE
	WHEN EXISTS (SELECT 1 FROM held_sales h WHERE h.payload LIKE '%"item_id":"' || i.id || '"%')
	  OR EXISTS (SELECT 1 FROM held_sales h JOIN item_variants v ON v.item_id = i.id
	             WHERE h.payload LIKE '%"variant_id":"' || v.id || '"%')
	  OR EXISTS (SELECT 1 FROM held_sales_archive h WHERE h.payload LIKE '%"item_id":"' || i.id || '"%')
	  OR EXISTS (SELECT 1 FROM held_sales_archive h JOIN item_variants v ON v.item_id = i.id
	             WHERE h.payload LIKE '%"variant_id":"' || v.id || '"%')
	THEN 'held'
	WHEN EXISTS (SELECT 1 FROM sale_lines sl WHERE sl.item_id = i.id)
	  OR EXISTS (SELECT 1 FROM sale_lines sl JOIN item_variants v ON v.id = sl.variant_id WHERE v.item_id = i.id)
	  OR EXISTS (SELECT 1 FROM stock_movements sm WHERE sm.item_id = i.id)
	  OR EXISTS (SELECT 1 FROM stock_movements sm JOIN item_variants v ON v.id = sm.variant_id WHERE v.item_id = i.id)
	  OR EXISTS (SELECT 1 FROM sale_lines_archive sl WHERE sl.item_id = i.id)
	  OR EXISTS (SELECT 1 FROM sale_lines_archive sl JOIN item_variants v ON v.id = sl.variant_id WHERE v.item_id = i.id)
	  OR EXISTS (SELECT 1 FROM stock_movements_archive sm WHERE sm.item_id = i.id)
	  OR EXISTS (SELECT 1 FROM stock_movements_archive sm JOIN item_variants v ON v.id = sm.variant_id WHERE v.item_id = i.id)
	THEN 'history'
	ELSE 'edited'
END`

// keptDemoItems reports every remaining is_sample_data=1 item, with why it
// wasn't removed. Called AFTER the removal script has run: at that point
// "still flagged is_sample_data=1" and "kept" are the same set by
// construction, so this needs no separate id list. Ordered by name for a
// stable, readable Settings-page list.
//
// Deliberately does NOT join demo_seed_items (unlike the removal scripts
// themselves): any is_sample_data=1 row outside the seeded id set falls
// through demoItemReasonCaseSQL's ELSE into ReasonEdited (ut-docs#1840
// review finding F7). Harmless today — demo_catalogue.sql is the only
// writer of is_sample_data=1, and it seeds exactly seeddata.ItemIDs — but
// if that invariant ever changes, such a row would be offered "remove
// anyway" under a reason that doesn't actually apply to it.
func keptDemoItems(ctx context.Context, tx *sql.Tx) ([]KeptDemoItem, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT i.id, COALESCE(i.sku, ''), i.name, `+demoItemReasonCaseSQL+`
FROM items i
WHERE i.is_sample_data = 1
ORDER BY i.name`)
	if err != nil {
		return nil, fmt.Errorf("list kept demo items: %w", err)
	}
	defer rows.Close()
	var out []KeptDemoItem
	for rows.Next() {
		var it KeptDemoItem
		if err := rows.Scan(&it.ID, &it.SKU, &it.Name, &it.Reason); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// RemoveDemoCatalogue deletes every demo item RemoveDemoRelaxedSQL/
// RemoveDemoSQL's safety predicate allows (plus dependents and any demo
// category/brand nothing references any more), and reports how many it
// removed and which ones it had to keep, and why (ut-docs#1840 AC1/AC2).
//
// Which script runs is decided once per call, by demoTillHasNoRealHistorySQL:
// a till that has never traded for real gets the relaxed variant (an edited
// demo item is removable too — AC1); a till that has real trading history
// anywhere gets the strict variant, unchanged from before this card (an
// edited demo item is kept, offered "remove anyway"/"keep as my own item"
// via RemoveDemoItem/KeepDemoItemAsOwn instead — AC3).
//
// The whole operation runs in one transaction: the TEMP ID tables the shared
// scripts use are per-connection, and a transaction is also what pins
// database/sql to a single connection between the script executions.
func (r *DemoSeedRepo) RemoveDemoCatalogue(ctx context.Context) (removed int, kept []KeptDemoItem, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("begin demo removal: %w", err)
	}
	defer tx.Rollback()

	before, err := sampleCount(ctx, tx, "items")
	if err != nil {
		return 0, nil, err
	}

	var relaxed bool
	if err := tx.QueryRowContext(ctx, demoTillHasNoRealHistorySQL).Scan(&relaxed); err != nil {
		return 0, nil, fmt.Errorf("check till trading history: %w", err)
	}

	if _, err := tx.ExecContext(ctx, seeddata.DemoIDsSQL); err != nil {
		return 0, nil, fmt.Errorf("load demo id lists: %w", err)
	}
	removeSQL := seeddata.RemoveDemoSQL
	if relaxed {
		removeSQL = seeddata.RemoveDemoRelaxedSQL
	}
	if _, err := tx.ExecContext(ctx, removeSQL); err != nil {
		return 0, nil, fmt.Errorf("remove demo catalogue: %w", err)
	}

	kept, err = keptDemoItems(ctx, tx)
	if err != nil {
		return 0, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	return before - len(kept), kept, nil
}

// ErrDemoItemNotFound: RemoveDemoItem/KeepDemoItemAsOwn's target id doesn't
// exist, or isn't (or is no longer) a sample-data item — nothing to act on.
var ErrDemoItemNotFound = errors.New("demo item not found")

// ErrDemoItemHasHistory: RemoveDemoItem was asked to remove an item this
// package would never remove regardless of mode — it (or a variant) has a
// live/archived sale, stock movement, or parked basket referencing it.
// Wraps the specific reason (KeptReasonHistory or KeptReasonHeld) so the
// caller can render the right message rather than a generic refusal.
var ErrDemoItemHasHistory = errors.New("demo item has trading history")

// RemoveDemoItem removes exactly one demo item on request — ut-docs#1840
// AC3's "remove anyway" action for an item whose ONLY reason for being kept
// is KeptReasonEdited (this is always safe: that reason means every
// trading-history/held-basket check already passed). Re-checks server-side
// rather than trusting the client's last-seen reason — the item may have
// been sold or parked in the basket since the page last rendered — and
// refuses (ErrDemoItemHasHistory) if the live reason is now "history" or
// "held". Not offered at all for an item that was never sample data.
func (r *DemoSeedRepo) RemoveDemoItem(ctx context.Context, itemID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin demo item removal: %w", err)
	}
	defer tx.Rollback()

	var reason string
	err = tx.QueryRowContext(ctx, `
SELECT `+demoItemReasonCaseSQL+`
FROM items i
WHERE i.id = ? AND i.is_sample_data = 1`, itemID).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDemoItemNotFound
	}
	if err != nil {
		return fmt.Errorf("check demo item %s: %w", itemID, err)
	}
	if reason == KeptReasonHistory || reason == KeptReasonHeld {
		return fmt.Errorf("%w: %s", ErrDemoItemHasHistory, reason)
	}

	// Same cascade cleanup as the bulk scripts (inventory/price_history have
	// no ON DELETE CASCADE); the item delete itself cascades to
	// item_barcodes/item_images/item_variants/shortcut_buttons/related_items/
	// item_modifiers/item_station_routes exactly as remove_demo.sql documents.
	// Demo category/brand cleanup is deliberately left to the next bulk
	// "Remove sample data" run rather than duplicated here — a category with
	// no items left is harmless to leave briefly, and there's no data-safety
	// reason to repeat that pass for a single-item action.
	if _, err := tx.ExecContext(ctx, `
DELETE FROM inventory
 WHERE item_id = ?
    OR variant_id IN (SELECT id FROM item_variants WHERE item_id = ?)`, itemID, itemID); err != nil {
		return fmt.Errorf("clear inventory for demo item %s: %w", itemID, err)
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM price_history
 WHERE item_id = ?
    OR variant_id IN (SELECT id FROM item_variants WHERE item_id = ?)`, itemID, itemID); err != nil {
		return fmt.Errorf("clear price history for demo item %s: %w", itemID, err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM items WHERE id = ? AND is_sample_data = 1`, itemID)
	if err != nil {
		return fmt.Errorf("remove demo item %s: %w", itemID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDemoItemNotFound
	}
	return tx.Commit()
}

// KeepDemoItemAsOwn clears one demo item's is_sample_data flag — ut-docs#1840
// AC3's "keep as my own item" resolution for an item kept only because it
// was edited. The item becomes a normal, permanent catalog item: it stops
// counting toward SampleItemCount, stops appearing in the Settings "kept"
// list, and a later "Remove sample data" run never touches it again.
func (r *DemoSeedRepo) KeepDemoItemAsOwn(ctx context.Context, itemID string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE items SET is_sample_data = 0 WHERE id = ? AND is_sample_data = 1`, itemID)
	if err != nil {
		return fmt.Errorf("keep demo item %s as own: %w", itemID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDemoItemNotFound
	}
	return nil
}

// SampleItemCount reports how many sample-data items are currently in the
// catalogue (drives the Settings "sample data present" note).
func (r *DemoSeedRepo) SampleItemCount(ctx context.Context) (int, error) {
	return sampleCount(ctx, r.db, "items")
}

// IsSampleItem reports whether itemID currently names an is_sample_data=1
// item — the cheap, no-mutation existence check RemoveDemoItem/
// KeepDemoItemAsOwn's HTTP handlers run BEFORE checkOrElevate (ut-docs#1840
// review finding F3, mirroring the established convention at
// dismiss-pending-base-plugin's own `matched` check, settings_page.go): a
// request that was always going to be a no-op shouldn't burn an approver's
// live PIN entry, and validating the id early means an invalid one gets a
// plain 404 instead of first rendering a PIN prompt with caller-chosen text
// in its approver-facing summary.
func (r *DemoSeedRepo) IsSampleItem(ctx context.Context, itemID string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM items WHERE id = ? AND is_sample_data = 1`, itemID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check sample item %s: %w", itemID, err)
	}
	return n > 0, nil
}

// SeedDemoCustomersPromos (re)inserts the 3 demo customers + 3 demo promo
// codes (ut-docs#567), every row flagged is_sample_data = 1. Idempotent
// (INSERT OR IGNORE) and atomic — the opt-in companion to
// SeedDemoCatalogue, run by the same setup-wizard checkbox.
func (r *DemoSeedRepo) SeedDemoCustomersPromos(ctx context.Context) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin demo customers/promos seed: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, seeddata.DemoCustomersPromosSQL); err != nil {
		return fmt.Errorf("seed demo customers/promos: %w", err)
	}
	return tx.Commit()
}

// KeptDemoCustomer is one demo customer RemoveDemoCustomersPromos could not
// remove, plus WHY (ut-docs#1858, mirroring KeptDemoItem for the customer
// side). Unlike an item, a demo customer has no "edited" reason at all —
// its fields are never pristine-checked (see
// remove_demo_customers_promos.sql's own header) — so every reason here is
// a genuine, unresolvable live reference: ReasonHistory (sold to, live or
// archived), ReasonHeld (referenced by a parked sale, live or archived), or
// ReasonTargeted (a promotion's customer_id points at them). None of these
// is ever offered a per-record "remove anyway"/"keep as mine" resolution —
// unlike a demo item or promo, there is nothing here relaxable by mode.
type KeptDemoCustomer struct {
	ID     string
	Name   string
	Reason string
}

// KeptDemoPromo is one demo promo code RemoveDemoCustomersPromos could not
// remove, plus WHY (ut-docs#1858, mirroring KeptDemoItem for the promo
// side).
//   - ReasonTargeted: customer_id is set — a real, durable reference this
//     schema can track for a promo. Never relaxed by mode, same as an
//     item's ReasonHistory/ReasonHeld. Not offered "remove anyway".
//   - ReasonEdited: type/value/description/is_active/starts_at/ends_at no
//     longer match the seeded values, but the code is untargeted. Only
//     reachable in STRICT mode (the till has real trading history
//     elsewhere) — in relaxed mode a promo blocked only by this predicate
//     is removed, not kept, exactly like an edited demo item. Safe to
//     remove on request (RemoveDemoPromo) or keep permanently as the
//     operator's own (KeepDemoPromoAsOwn).
type KeptDemoPromo struct {
	Code        string
	Description string
	Reason      string
}

// demoCustomerReasonCaseSQL mirrors demoItemReasonCaseSQL's pattern for
// demo customers: `c` is the customers row alias the caller's FROM/WHERE
// supplies. Priority order matches remove_demo_customers_promos.sql's own
// safety predicate: held (parked sale) outranks history (sold) outranks
// targeted (referenced by a promotion) — none of the three is ever relaxed
// by mode, so the order only affects which single reason a customer
// blocked by more than one signal is reported under.
const demoCustomerReasonCaseSQL = `
CASE
	WHEN EXISTS (SELECT 1 FROM held_sales h WHERE h.payload LIKE '%"customer_id":"' || c.id || '"%')
	  OR EXISTS (SELECT 1 FROM held_sales_archive h WHERE h.payload LIKE '%"customer_id":"' || c.id || '"%')
	THEN 'held'
	WHEN EXISTS (SELECT 1 FROM sales s WHERE s.customer_id = c.id)
	  OR EXISTS (SELECT 1 FROM sales_archive s WHERE s.customer_id = c.id)
	THEN 'history'
	ELSE 'targeted'
END`

// demoPromoReasonCaseSQL mirrors demoItemReasonCaseSQL's pattern for demo
// promo codes: `p` is the promotions row alias the caller's FROM/WHERE
// supplies. Only two reasons exist for a promo (unlike an item's three) —
// targeted is the hard blocker, never relaxed by mode; edited is the soft
// one, reachable only in strict mode (RemoveDemoCustomersPromos's relaxed
// variant never leaves a promo blocked by field mismatch alone for this to
// report).
const demoPromoReasonCaseSQL = `
CASE
	WHEN p.customer_id IS NOT NULL THEN 'targeted'
	ELSE 'edited'
END`

// keptDemoCustomers reports every remaining is_sample_data=1 customer, with
// why it wasn't removed. Called AFTER the removal script has run, mirroring
// keptDemoItems exactly — see that function's own doc comment for why this
// needs no separate id list and deliberately doesn't join
// demo_seed_customers.
func keptDemoCustomers(ctx context.Context, tx *sql.Tx) ([]KeptDemoCustomer, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT c.id, c.name, `+demoCustomerReasonCaseSQL+`
FROM customers c
WHERE c.is_sample_data = 1
ORDER BY c.name`)
	if err != nil {
		return nil, fmt.Errorf("list kept demo customers: %w", err)
	}
	defer rows.Close()
	var out []KeptDemoCustomer
	for rows.Next() {
		var c KeptDemoCustomer
		if err := rows.Scan(&c.ID, &c.Name, &c.Reason); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// keptDemoPromos reports every remaining is_sample_data=1 promo code, with
// why it wasn't removed. Mirrors keptDemoCustomers/keptDemoItems.
func keptDemoPromos(ctx context.Context, tx *sql.Tx) ([]KeptDemoPromo, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT p.code, COALESCE(p.description, ''), `+demoPromoReasonCaseSQL+`
FROM promotions p
WHERE p.is_sample_data = 1
ORDER BY p.code`)
	if err != nil {
		return nil, fmt.Errorf("list kept demo promos: %w", err)
	}
	defer rows.Close()
	var out []KeptDemoPromo
	for rows.Next() {
		var p KeptDemoPromo
		if err := rows.Scan(&p.Code, &p.Description, &p.Reason); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RemoveDemoCustomersPromos deletes every demo customer/promo code the
// chosen removal script allows (see seeddata.RemoveDemoCustomersPromosSQL/
// RemoveDemoCustomersPromosRelaxedSQL for the exact rule), and reports how
// many it removed and which ones it had to keep, and why (ut-docs#1858,
// mirroring RemoveDemoCatalogue's own AC1/AC2 exactly).
//
// Which script runs is decided once per call, by the SAME till-wide
// demoTillHasNoRealHistorySQL gate RemoveDemoCatalogue uses: a till that
// has never traded for real gets the relaxed variant (an edited demo promo
// is removable too); a till with real trading history anywhere gets the
// strict variant, unchanged from before this card (an edited demo promo is
// kept, offered "remove anyway"/"keep as my own" via RemoveDemoPromo/
// KeepDemoPromoAsOwn instead). Demo customers are unaffected by either
// variant — they have no "edited" rule at all, only genuine reference
// checks, which are identical in both scripts.
//
// The whole operation runs in one transaction, same reasoning as
// RemoveDemoCatalogue: the TEMP ID tables are per-connection, and a
// transaction pins database/sql to a single connection between the script
// executions.
func (r *DemoSeedRepo) RemoveDemoCustomersPromos(ctx context.Context) (removed int, keptCustomers []KeptDemoCustomer, keptPromos []KeptDemoPromo, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, nil, fmt.Errorf("begin demo customers/promos removal: %w", err)
	}
	defer tx.Rollback()

	before, err := sampleCustomerPromoCount(ctx, tx)
	if err != nil {
		return 0, nil, nil, err
	}

	var relaxed bool
	if err := tx.QueryRowContext(ctx, demoTillHasNoRealHistorySQL).Scan(&relaxed); err != nil {
		return 0, nil, nil, fmt.Errorf("check till trading history: %w", err)
	}

	if _, err := tx.ExecContext(ctx, seeddata.DemoCustomersPromosIDsSQL); err != nil {
		return 0, nil, nil, fmt.Errorf("load demo customer/promo id lists: %w", err)
	}
	removeSQL := seeddata.RemoveDemoCustomersPromosSQL
	if relaxed {
		removeSQL = seeddata.RemoveDemoCustomersPromosRelaxedSQL
	}
	if _, err := tx.ExecContext(ctx, removeSQL); err != nil {
		return 0, nil, nil, fmt.Errorf("remove demo customers/promos: %w", err)
	}

	keptCustomers, err = keptDemoCustomers(ctx, tx)
	if err != nil {
		return 0, nil, nil, err
	}
	keptPromos, err = keptDemoPromos(ctx, tx)
	if err != nil {
		return 0, nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, nil, err
	}
	return before - len(keptCustomers) - len(keptPromos), keptCustomers, keptPromos, nil
}

// ErrDemoPromoNotFound: RemoveDemoPromo/KeepDemoPromoAsOwn's target code
// doesn't exist, or isn't (or is no longer) a sample-data promo — nothing
// to act on. Mirrors ErrDemoItemNotFound for the promo side.
var ErrDemoPromoNotFound = errors.New("demo promo not found")

// ErrDemoPromoTargeted: RemoveDemoPromo was asked to remove a promo this
// package would never remove regardless of mode — its customer_id is set,
// a real, durable reference this schema can track. Mirrors
// ErrDemoItemHasHistory for the promo side (a promo has only the one
// unconditional blocker, unlike an item's two).
var ErrDemoPromoTargeted = errors.New("demo promo is targeted at a customer")

// RemoveDemoPromo removes exactly one demo promo code on request —
// ut-docs#1858's "remove anyway" action for a promo whose ONLY reason for
// being kept is KeptReasonEdited (always safe: that reason means the
// targeting check already passed). Re-checks server-side rather than
// trusting the client's last-seen reason — the promo could have been
// targeted at a customer since the page last rendered — and refuses
// (ErrDemoPromoTargeted) if the live reason is now "targeted". Not offered
// at all for a code that was never sample data. Mirrors RemoveDemoItem.
func (r *DemoSeedRepo) RemoveDemoPromo(ctx context.Context, code string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin demo promo removal: %w", err)
	}
	defer tx.Rollback()

	var reason string
	err = tx.QueryRowContext(ctx, `
SELECT `+demoPromoReasonCaseSQL+`
FROM promotions p
WHERE p.code = ? AND p.is_sample_data = 1`, code).Scan(&reason)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrDemoPromoNotFound
	}
	if err != nil {
		return fmt.Errorf("check demo promo %s: %w", code, err)
	}
	if reason == KeptReasonTargeted {
		return fmt.Errorf("%w: %s", ErrDemoPromoTargeted, code)
	}

	// Unlike an item, a promo has no dependent rows to clean up first —
	// sale_discounts records only the resulting discount amount, never
	// which code produced it (remove_demo_customers_promos.sql's own
	// header), so there is nothing here for a plain DELETE to orphan.
	res, err := tx.ExecContext(ctx, `DELETE FROM promotions WHERE code = ? AND is_sample_data = 1`, code)
	if err != nil {
		return fmt.Errorf("remove demo promo %s: %w", code, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDemoPromoNotFound
	}
	return tx.Commit()
}

// KeepDemoPromoAsOwn clears one demo promo code's is_sample_data flag —
// ut-docs#1858's "keep as my own" resolution for a promo kept only because
// it was edited. The promo becomes a normal, permanent code: it stops
// counting toward SampleCustomerPromoCount, stops appearing in the Settings
// "kept" list, and a later "Remove sample data" run never touches it again.
// Mirrors KeepDemoItemAsOwn.
func (r *DemoSeedRepo) KeepDemoPromoAsOwn(ctx context.Context, code string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE promotions SET is_sample_data = 0 WHERE code = ? AND is_sample_data = 1`, code)
	if err != nil {
		return fmt.Errorf("keep demo promo %s as own: %w", code, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrDemoPromoNotFound
	}
	return nil
}

// IsSamplePromo reports whether code currently names an is_sample_data=1
// promo — the cheap, no-mutation existence check RemoveDemoPromo/
// KeepDemoPromoAsOwn's HTTP handlers run BEFORE checkOrElevate, mirroring
// IsSampleItem exactly (ut-docs#1840 review finding F3's convention).
func (r *DemoSeedRepo) IsSamplePromo(ctx context.Context, code string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM promotions WHERE code = ? AND is_sample_data = 1`, code).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("check sample promo %s: %w", code, err)
	}
	return n > 0, nil
}

// SampleCustomerPromoCount reports how many sample-data customers + promo
// codes are currently present, combined (drives the Settings "sample data
// present" note alongside SampleItemCount).
func (r *DemoSeedRepo) SampleCustomerPromoCount(ctx context.Context) (int, error) {
	return sampleCustomerPromoCount(ctx, r.db)
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func sampleCount(ctx context.Context, q queryRower, table string) (int, error) {
	var n int
	if err := q.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table+` WHERE is_sample_data = 1`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count sample %s: %w", table, err)
	}
	return n, nil
}

func sampleCustomerPromoCount(ctx context.Context, q queryRower) (int, error) {
	customers, err := sampleCount(ctx, q, "customers")
	if err != nil {
		return 0, err
	}
	promos, err := sampleCount(ctx, q, "promotions")
	if err != nil {
		return 0, err
	}
	return customers + promos, nil
}
