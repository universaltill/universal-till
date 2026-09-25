package data

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type ShortcutsRepo struct {
	db *sql.DB
}

func NewShortcutsRepo(db *sql.DB) *ShortcutsRepo {
	return &ShortcutsRepo{db: db}
}

var shortcutsObs = newRepoObservability("shortcuts")

type ShortcutButton struct {
	Label      string
	Barcode    string
	ItemID     string
	ImageURL   string
	Price      int64  // item base price in minor units, for display on the tile
	CategoryID string // the item's category, empty when uncategorized
	// Color is the item's tile swatch (ut-docs#1901, catalogtypes.
	// ItemColors) — empty when the item has none set. Only meaningful for
	// a photo-less tile (product-tile, buttons.html): an item WITH an
	// image keeps showing its photo regardless of Color.
	Color string
	// Hidden (ut-docs#2698) marks a row whose item is hidden from the sell
	// screen (items.sell_screen_hidden). Only LoadGridButtons ever returns
	// such a row; LoadButtons leaves it out.
	Hidden bool
}

// LoadButtons returns the quick buttons as the sell screen shows them AT
// REST: rows whose item is hidden are left out. It is what the cloud
// heartbeat reports and what a cloud layout directive is validated against
// (cloudsync_wire.go), so sell_screen_hidden keeps meaning "not on the sell
// screen" there (ut-docs#2698).
func (r *ShortcutsRepo) LoadButtons(ctx context.Context) ([]ShortcutButton, error) {
	return r.loadButtons(ctx, "load_buttons", false)
}

// LoadGridButtons (ut-docs#2698) is LoadButtons plus the rows whose item is
// hidden, each marked Hidden, in the same sort order: the rendered grid
// carries hidden tiles in their own spot so edit mode (the sale screen's
// client-side jiggle mode, and the Designer) can show them greyed with an
// Unhide badge. CSS keeps them out of sight at rest.
func (r *ShortcutsRepo) LoadGridButtons(ctx context.Context) ([]ShortcutButton, error) {
	return r.loadButtons(ctx, "load_grid_buttons", true)
}

func (r *ShortcutsRepo) loadButtons(ctx context.Context, op string, withHidden bool) ([]ShortcutButton, error) {
	var err error
	done := shortcutsObs.trace(op)
	defer func() { done(err) }()
	// image_path falls back to the item's own catalog image (item_images,
	// role='thumbnail' — same source the catalog list and barcode/scan
	// resolvers already use) when the button has no image of its own set
	// explicitly. Without this, a shop's catalog-uploaded item photo showed
	// up in the catalog list but never on the actual sale-screen tile,
	// since shortcut_buttons.image_path is a separate column nothing ever
	// populates from a plain catalog image upload — confirmed live 2026-07-29.
	// INNER JOIN (not LEFT) is deliberate (ut-docs#2281 cause A): a button
	// whose item was soft-deleted (is_active=0, Catalog "Delete item") or
	// whose item row is gone entirely must not come back as a tile — it
	// used to via the old LEFT JOIN with no is_active filter, so the tile
	// stayed on the sell screen after "deletion" and tapping it silently
	// did nothing, since POSRepo.ResolveShortcutLineDecoded already filters
	// i.is_active = 1 when actually resolving the tap. This makes
	// LoadButtons agree with that resolver.
	// ut-docs#2541/#2698: a hidden item's explicit row is KEPT (it holds the
	// tile's position) and filtered here instead -- withHidden=false (the
	// rest view) drops it, withHidden=true returns it marked Hidden. A
	// removed item (sell_screen_removed) has no row left at all
	// (CatalogRepo.RemoveFromSellScreen deletes it); the removed = 0 filter
	// is defence in depth against a direct DB write that skips that method.
	// ut-docs#2541 review finding 1: COALESCE(NULLIF(sb.label,''), i.name) --
	// ButtonStore.UpdateOrder materializes an implicit tile with an
	// intentionally EMPTY label (see ShortcutsRepo.MaterializeAndReorder)
	// rather than freezing the item's name as it was at drag time, so a
	// later rename in the catalog still shows on the tile. NULLIF turns
	// that empty string back into NULL so COALESCE falls through to the
	// item's own (live) name — an explicitly-labelled row (Add/SaveButtons,
	// a real operator-chosen label) is untouched, since its label is never
	// empty in the first place (ButtonStore.Add rejects a blank label).
	rows, err := r.db.QueryContext(ctx, `
SELECT COALESCE(NULLIF(sb.label, ''), i.name), sb.barcode, sb.item_id,
       COALESCE(sb.image_path, (SELECT path FROM item_images img WHERE img.item_id = sb.item_id AND img.role = 'thumbnail' LIMIT 1)),
       COALESCE(i.base_price, 0), COALESCE(i.category_id, ''), COALESCE(i.color, ''), i.sell_screen_hidden
FROM shortcut_buttons sb
JOIN items i ON i.id = sb.item_id AND i.is_active = 1 AND i.sell_screen_removed = 0 AND (? OR i.sell_screen_hidden = 0)
ORDER BY sb.sort_order, sb.label`, withHidden)
	if err != nil {
		return nil, shortcutsObs.wrap(op, err)
	}
	defer rows.Close()
	var out []ShortcutButton
	for rows.Next() {
		var b ShortcutButton
		var img sql.NullString
		var hidden int
		if err := rows.Scan(&b.Label, &b.Barcode, &b.ItemID, &img, &b.Price, &b.CategoryID, &b.Color, &hidden); err != nil {
			err = shortcutsObs.wrap(op, err)
			return nil, err
		}
		b.Hidden = hidden == 1
		if img.Valid {
			b.ImageURL = img.String
		}
		out = append(out, b)
	}
	err = rows.Err()
	if err != nil {
		err = shortcutsObs.wrap(op, err)
	}
	return out, err
}

func (r *ShortcutsRepo) SaveButtons(ctx context.Context, list []ShortcutButton) error {
	var err error
	done := shortcutsObs.trace("save_buttons")
	defer func() { done(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		err = shortcutsObs.wrap("save_buttons", err)
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM shortcut_buttons`); err != nil {
		tx.Rollback()
		err = shortcutsObs.wrap("save_buttons", err)
		return err
	}
	stmt, err := tx.PrepareContext(ctx, `INSERT INTO shortcut_buttons(barcode,label,item_id,image_path,sort_order) VALUES(?,?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		err = shortcutsObs.wrap("save_buttons", err)
		return err
	}
	defer stmt.Close()
	for i, b := range list {
		if _, err := stmt.ExecContext(ctx, b.Barcode, b.Label, b.ItemID, nullIfEmptyButton(b.ImageURL), i); err != nil {
			tx.Rollback()
			err = shortcutsObs.wrap("save_buttons", err)
			return err
		}
	}
	err = tx.Commit()
	if err != nil {
		err = shortcutsObs.wrap("save_buttons", err)
	}
	return err
}

// UpdateOrder persists the drag&drop tile order: sort_order = position in codes.
func (r *ShortcutsRepo) UpdateOrder(ctx context.Context, codes []string) error {
	var err error
	done := shortcutsObs.trace("update_order")
	defer func() { done(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return shortcutsObs.wrap("update_order", err)
	}
	stmt, err := tx.PrepareContext(ctx, `UPDATE shortcut_buttons SET sort_order = ? WHERE barcode = ?`)
	if err != nil {
		tx.Rollback()
		return shortcutsObs.wrap("update_order", err)
	}
	defer stmt.Close()
	for i, code := range codes {
		if _, err = stmt.ExecContext(ctx, i, code); err != nil {
			tx.Rollback()
			return shortcutsObs.wrap("update_order", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return shortcutsObs.wrap("update_order", err)
	}
	return nil
}

// MaterializeAndReorder inserts a real shortcut_buttons row for every
// IMPLICIT tile a drag touched (materialize -- see ButtonStore.UpdateOrder
// for how that set is computed) and rewrites sort_order for the whole
// posted order, all in ONE transaction (ut-docs#2541 review finding 1).
// Before this, UpdateOrder ran one AddButton call (its own transaction) PER
// implicit tile, then a separate UpdateOrder transaction — a drag touching
// many implicit tiles cost 2N+1 round trips/transactions; this costs one.
// materialize rows are inserted with ON CONFLICT DO NOTHING (not AddButton's
// own upsert): the caller already filtered out codes with an existing row
// (ExistingBarcodes), so a conflict here means a race, and doing nothing is
// safer than clobbering a row that appeared concurrently. Every materialize
// row's Label is expected to be "" (see LoadButtons' own COALESCE(NULLIF(...),
// i.name) fallback) so a materialized tile always shows the item's LIVE
// name, never one frozen at drag time.
func (r *ShortcutsRepo) MaterializeAndReorder(ctx context.Context, materialize []ShortcutButton, codes []string) error {
	var err error
	done := shortcutsObs.trace("materialize_and_reorder")
	defer func() { done(err) }()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		err = shortcutsObs.wrap("materialize_and_reorder", err)
		return err
	}
	if len(materialize) > 0 {
		insertStmt, ierr := tx.PrepareContext(ctx, `INSERT INTO shortcut_buttons(barcode,label,item_id,image_path,sort_order)
VALUES(?,?,?,?, (SELECT COALESCE(MAX(sort_order)+1, 0) FROM shortcut_buttons))
ON CONFLICT(barcode) DO NOTHING`)
		if ierr != nil {
			tx.Rollback()
			err = shortcutsObs.wrap("materialize_and_reorder", ierr)
			return err
		}
		for _, b := range materialize {
			if _, ierr := insertStmt.ExecContext(ctx, b.Barcode, b.Label, b.ItemID, nullIfEmptyButton(b.ImageURL)); ierr != nil {
				insertStmt.Close()
				tx.Rollback()
				err = shortcutsObs.wrap("materialize_and_reorder", ierr)
				return err
			}
		}
		insertStmt.Close()
	}
	orderStmt, oerr := tx.PrepareContext(ctx, `UPDATE shortcut_buttons SET sort_order = ? WHERE barcode = ?`)
	if oerr != nil {
		tx.Rollback()
		err = shortcutsObs.wrap("materialize_and_reorder", oerr)
		return err
	}
	for i, code := range codes {
		if _, oerr := orderStmt.ExecContext(ctx, i, code); oerr != nil {
			orderStmt.Close()
			tx.Rollback()
			err = shortcutsObs.wrap("materialize_and_reorder", oerr)
			return err
		}
	}
	orderStmt.Close()
	if err = tx.Commit(); err != nil {
		err = shortcutsObs.wrap("materialize_and_reorder", err)
		return err
	}
	return nil
}

// AddButton puts itemID on the quick buttons (ButtonStore.Add), in one
// transaction:
//
//   - if the item already has a shortcut_buttons row, that row is kept --
//     its code and its sort position -- and only its label/image are
//     refreshed (ut-docs#2698 review F1). Hide keeps the row and search
//     offers "Add to quick buttons" on a hidden result; the item's
//     resolvable tile code may have changed since the row was written (a
//     barcode plugin enabled after it was materialised with the SKU), and
//     upserting on the NEW code would leave two live tiles for one item.
//   - otherwise a row is inserted at the end, upserting ON CONFLICT(barcode)
//     as before.
//   - both sell-screen flags are cleared (ut-docs#2541/#2698): the operator
//     just configured a tile for the item, so a stale hidden/removed flag
//     must not keep it off the grid.
func (r *ShortcutsRepo) AddButton(ctx context.Context, b ShortcutButton) error {
	var err error
	done := shortcutsObs.trace("add_button")
	defer func() { done(err) }()
	b.Label = strings.TrimSpace(b.Label)
	b.Barcode = strings.TrimSpace(b.Barcode)
	b.ItemID = strings.TrimSpace(b.ItemID)
	if b.Label == "" || b.Barcode == "" || b.ItemID == "" {
		err = errors.New("label, barcode, and itemId are required")
		return err
	}
	tx, terr := r.db.BeginTx(ctx, nil)
	if terr != nil {
		err = shortcutsObs.wrap("add_button", terr)
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	var exists int
	if err = tx.QueryRowContext(ctx, `SELECT 1 FROM items WHERE id = ? AND is_active = 1`, b.ItemID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = errors.New("item not found or inactive")
			return err
		}
		err = shortcutsObs.wrap("add_button", err)
		return err
	}
	var existing string
	switch qerr := tx.QueryRowContext(ctx, `SELECT barcode FROM shortcut_buttons WHERE item_id = ? ORDER BY sort_order, barcode LIMIT 1`, b.ItemID).Scan(&existing); {
	case qerr == nil:
		_, err = tx.ExecContext(ctx, `UPDATE shortcut_buttons SET label = ?, image_path = ? WHERE barcode = ?`,
			b.Label, nullIfEmptyButton(b.ImageURL), existing)
	case errors.Is(qerr, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO shortcut_buttons(barcode,label,item_id,image_path,sort_order)
VALUES(?,?,?,?, (SELECT COALESCE(MAX(sort_order)+1, 0) FROM shortcut_buttons))
ON CONFLICT(barcode) DO UPDATE SET label=excluded.label, item_id=excluded.item_id, image_path=excluded.image_path`,
			b.Barcode, b.Label, b.ItemID, nullIfEmptyButton(b.ImageURL))
	default:
		err = qerr
	}
	if err != nil {
		err = shortcutsObs.wrap("add_button", err)
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE items SET sell_screen_hidden = 0, sell_screen_removed = 0 WHERE id = ?`, b.ItemID); err != nil {
		err = shortcutsObs.wrap("add_button", err)
		return err
	}
	if err = tx.Commit(); err != nil {
		err = shortcutsObs.wrap("add_button", err)
		return err
	}
	return nil
}

func (r *ShortcutsRepo) RemoveButton(ctx context.Context, code string) error {
	var err error
	done := shortcutsObs.trace("remove_button")
	defer func() { done(err) }()
	_, err = r.db.ExecContext(ctx, `DELETE FROM shortcut_buttons WHERE barcode=?`, strings.TrimSpace(code))
	err = shortcutsObs.wrap("remove_button", err)
	return err
}

// ItemIDForBarcode looks up the item a shortcut_buttons row's own code
// points at (ut-docs#2541) -- used by ButtonStore.Remove/Hide when the
// caller only has the tile's code (a legacy hx-vals payload, e.g.
// buttons_admin.html's search-result form) and not the item id directly.
// ok is false both when the code has no row and on a genuine query error --
// callers of this narrow lookup only ever need to know "resolved or not".
func (r *ShortcutsRepo) ItemIDForBarcode(ctx context.Context, code string) (itemID string, ok bool) {
	err := r.db.QueryRowContext(ctx, `SELECT item_id FROM shortcut_buttons WHERE barcode = ?`, strings.TrimSpace(code)).Scan(&itemID)
	if err != nil {
		return "", false
	}
	return itemID, true
}

// ExistingBarcodes reports which of codes already have a shortcut_buttons
// row, keyed by code (ut-docs#2541) -- ButtonStore.UpdateOrder uses this to
// tell an IMPLICIT tile (every active, non-hidden item with no row of its
// own -- see ButtonStore.Load) apart from an explicit one before a drag:
// only the implicit ones need a fresh row materialised so their new
// position actually has something to persist onto.
func (r *ShortcutsRepo) ExistingBarcodes(ctx context.Context, codes []string) (map[string]bool, error) {
	out := make(map[string]bool, len(codes))
	if len(codes) == 0 {
		return out, nil
	}
	for _, chunk := range ChunkStrings(codes, IDChunkSize) {
		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for i, c := range chunk {
			placeholders[i] = "?"
			args[i] = c
		}
		rows, err := r.db.QueryContext(ctx, `SELECT barcode FROM shortcut_buttons WHERE barcode IN (`+strings.Join(placeholders, ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		scanErr := func() error {
			defer rows.Close()
			for rows.Next() {
				var b string
				if err := rows.Scan(&b); err != nil {
					return err
				}
				out[b] = true
			}
			return rows.Err()
		}()
		if scanErr != nil {
			return nil, scanErr
		}
	}
	return out, nil
}

func nullIfEmptyButton(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
