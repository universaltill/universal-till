package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/iconid"
)

// This file holds the repository write paths behind the manage-shop
// catalog directives (ut-docs reference/manage-shop-catalog-api.md §3):
// save_item, save_category and delete_category. Each is ONE transaction —
// BEGIN IMMEDIATE via the DSN's _txlock=immediate — so a directive applies
// all or nothing, and each is idempotent: applying the same patch twice
// leaves the same state (the cloud re-serves a directive whose result post
// was lost). Validation errors are plain, owner-readable English sentences
// naming the object: the cloud shows them verbatim inside a translated
// frame.

// Limits shared with the cloud's pre-queue validation (contract §2.11).
const (
	maxItemBarcodes       = 20
	maxBarcodeLen         = 64
	maxItemModifierGroups = 50
	// MaxCategoryDepth is how many levels a category tree may have (a
	// top-level category is level 1). Contract §6 R2: one constant on each
	// side, the product owner can change it.
	MaxCategoryDepth = 3
)

// ItemPatch is a save_item directive: a nil field is absent (keep), a
// non-nil one is set. Barcodes, ModifierGroupIDs and ModifierOptOutIDs are
// FULL sets (an empty list clears).
type ItemPatch struct {
	ID                string
	Create            bool
	Name              *string
	PriceMinor        *int64
	SKU               *string
	CategoryID        *string
	Color             *string
	Barcodes          *[]string
	Active            *bool
	IsWeighed         *bool
	StockUntracked    *bool
	ModifierGroupIDs  *[]string
	ModifierOptOutIDs *[]string
}

// ItemSaveResult reports what SaveItem did.
type ItemSaveResult struct {
	Created bool
	Name    string
	// Changed lists the patch's fields that were present, for the audit row.
	Changed []string
}

// generatedItemSKU is the SKU SaveItem gives an item created with a blank
// one (ut-docs#1459: never an item without a SKU): "ITEM-" + 8 upper-case
// hex characters, the same shape as generatedVariantSKU.
func generatedItemSKU() string {
	return "ITEM-" + strings.ToUpper(uuid.NewString()[:8])
}

// validBarcodeText reports whether code is 1–64 printable ASCII characters
// (0x21–0x7E), the contract's barcode rule.
func validBarcodeText(code string) bool {
	if code == "" || len(code) > maxBarcodeLen {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < 0x21 || code[i] > 0x7E {
			return false
		}
	}
	return true
}

type resolvedBarcode struct {
	raw, key, typ string
}

// SaveItem applies a save_item directive (contract §3.1) in one
// transaction: create-with-id or update, full barcode-set replace (the
// first barcode is the primary; variant barcodes are untouched),
// deactivate/reactivate, and the item's direct modifier-group links and
// opt-outs. A barcode used by another item or variant fails with a
// *BarcodeConflictError (wrapped with the barcode) so the caller can name
// the other item.
func (r *CatalogRepo) SaveItem(ctx context.Context, p ItemPatch) (ItemSaveResult, error) {
	var res ItemSaveResult
	p.ID = strings.TrimSpace(p.ID)
	if p.ID == "" {
		return res, errors.New("missing id")
	}
	// Validate everything that needs no database first, so a malformed
	// patch never takes the write lock.
	if p.Name != nil {
		n := strings.TrimSpace(*p.Name)
		if n == "" {
			return res, errors.New("the item name must not be blank")
		}
		p.Name = &n
	}
	if p.PriceMinor != nil && *p.PriceMinor < 0 {
		return res, errors.New("the price must not be negative")
	}
	if p.SKU != nil {
		s := strings.TrimSpace(*p.SKU)
		p.SKU = &s
	}
	if p.Color != nil {
		c := strings.TrimSpace(*p.Color)
		if !catalogtypes.ValidItemColor(c) {
			return res, fmt.Errorf("colour %q is not one of the palette colours", c)
		}
		p.Color = &c
	}
	var barcodes []resolvedBarcode
	if p.Barcodes != nil {
		if len(*p.Barcodes) > maxItemBarcodes {
			return res, fmt.Errorf("an item can have at most %d barcodes", maxItemBarcodes)
		}
		seen := map[string]bool{}
		for _, raw := range *p.Barcodes {
			raw = strings.TrimSpace(raw)
			if !validBarcodeText(raw) {
				return res, fmt.Errorf("barcode %q must be 1–%d printable characters with no spaces", raw, maxBarcodeLen)
			}
			// Resolve to the stored key exactly as AddBarcode does (ADR-0059
			// §3) — outside the transaction: it reads the shop's enabled
			// symbologies, which this write never changes.
			dec, enabled, ok := r.matchBarcode(ctx, raw)
			if !ok {
				return res, fmt.Errorf("%w: %q (enabled: %s)", ErrBarcodeNoSymbologyMatch, raw, strings.Join(enabled, ", "))
			}
			if seen[dec.LookupKey] {
				return res, fmt.Errorf("barcode %s is listed twice", raw)
			}
			seen[dec.LookupKey] = true
			barcodes = append(barcodes, resolvedBarcode{raw: raw, key: dec.LookupKey, typ: strings.ToUpper(dec.SymbologyID)})
		}
	}
	for _, list := range []*[]string{p.ModifierGroupIDs, p.ModifierOptOutIDs} {
		if list != nil && len(*list) > maxItemModifierGroups {
			return res, fmt.Errorf("an item can have at most %d modifier groups", maxItemModifierGroups)
		}
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("save item: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cur, exists, err := getItemExec(ctx, tx, p.ID)
	if err != nil {
		return res, err
	}
	if !exists {
		if !p.Create {
			return res, ErrItemNotFound
		}
		if p.Name == nil {
			return res, errors.New("a new item needs a name")
		}
		if p.PriceMinor == nil {
			return res, errors.New("a new item needs a price")
		}
		in := catalogtypes.ItemInput{ID: p.ID, Name: *p.Name, BasePrice: *p.PriceMinor, IsActive: true}
		if p.SKU != nil {
			in.SKU = *p.SKU
		}
		if p.StockUntracked != nil {
			// Decided at insert time: CreateItemTx skips the inventory
			// placeholder row for an untracked item (ut-docs#1850).
			in.StockUntracked = *p.StockUntracked
		}
		autoSKU := in.SKU == ""
		for attempt := 0; ; attempt++ {
			if autoSKU {
				in.SKU = generatedItemSKU()
			}
			_, err = r.CreateItemTx(ctx, tx, in)
			if !errors.Is(err, ErrSKUExists) || !autoSKU || attempt >= 4 {
				break
			}
		}
		if errors.Is(err, ErrSKUExists) {
			return res, r.skuTakenError(ctx, tx, in.SKU, p.ID)
		}
		if err != nil {
			return res, err
		}
		cur, _, err = getItemExec(ctx, tx, p.ID)
		if err != nil {
			return res, err
		}
		res.Created = true
		p.SKU = nil // already written
	} else if p.SKU != nil && *p.SKU == "" {
		return res, errors.New("the sku must not be blank")
	}

	// Scalar fields, then one row write through the same updateItemExec
	// the local item editor uses.
	oldPrice, priceKnown, _ := resolveCurrentPriceExec(ctx, tx, p.ID, "")
	if p.Name != nil {
		cur.Name = *p.Name
		res.Changed = append(res.Changed, "name")
	}
	if p.PriceMinor != nil {
		cur.BasePrice = *p.PriceMinor
		res.Changed = append(res.Changed, "price_minor")
	}
	if p.SKU != nil {
		cur.SKU = *p.SKU
		res.Changed = append(res.Changed, "sku")
	}
	if p.CategoryID != nil {
		cat := strings.TrimSpace(*p.CategoryID)
		if cat == "" {
			cur.CategoryID = nil
		} else {
			if ok, err := rowExists(ctx, tx, `SELECT 1 FROM categories WHERE id = ? AND is_active = 1`, cat); err != nil {
				return res, fmt.Errorf("save item: check category: %w", err)
			} else if !ok {
				return res, fmt.Errorf("category %s does not exist on this till or is deleted", cat)
			}
			cur.CategoryID = &cat
		}
		res.Changed = append(res.Changed, "category_id")
	}
	if p.Color != nil {
		cur.Color = *p.Color
		res.Changed = append(res.Changed, "color")
	}
	if p.IsWeighed != nil {
		cur.IsWeighed = *p.IsWeighed
		res.Changed = append(res.Changed, "is_weighed")
	}
	if p.StockUntracked != nil {
		cur.StockUntracked = *p.StockUntracked
		res.Changed = append(res.Changed, "stock_untracked")
	}
	deactivate := false
	if p.Active != nil {
		deactivate = cur.IsActive && !*p.Active
		cur.IsActive = *p.Active
		res.Changed = append(res.Changed, "active")
	}
	if err := updateItemExec(ctx, tx, cur); err != nil {
		if errors.Is(err, ErrSKUExists) {
			return res, r.skuTakenError(ctx, tx, cur.SKU, p.ID)
		}
		return res, err
	}
	if priceKnown && !res.Created {
		if err := recordPriceChangeExec(ctx, tx, p.ID, "", oldPrice, cur.BasePrice, time.Now()); err != nil {
			return res, fmt.Errorf("save item: record price change: %w", err)
		}
	}
	if deactivate {
		// The same cascade DeactivateItem applies: an item's variants retire
		// with it. Reactivating never revives variants (their own state is
		// not recoverable from here); the operator reactivates those one by
		// one in the item editor.
		if _, err := tx.ExecContext(ctx, `UPDATE item_variants SET is_active = 0 WHERE item_id = ?`, p.ID); err != nil {
			return res, fmt.Errorf("save item: deactivate variants: %w", err)
		}
	}

	if p.Barcodes != nil {
		if err := replaceItemBarcodesTx(ctx, tx, p.ID, barcodes); err != nil {
			return res, err
		}
		res.Changed = append(res.Changed, "barcodes")
	}
	if p.ModifierGroupIDs != nil {
		ids := dedupeIDs(*p.ModifierGroupIDs)
		if err := requireGroupsTx(ctx, tx, ids); err != nil {
			return res, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM item_modifier_group_links WHERE item_id = ?`, p.ID); err != nil {
			return res, fmt.Errorf("save item: clear group links: %w", err)
		}
		for i, gid := range ids {
			if _, err := tx.ExecContext(ctx, `INSERT INTO item_modifier_group_links (item_id, group_id, sort_order) VALUES (?, ?, ?)`, p.ID, gid, i); err != nil {
				return res, fmt.Errorf("save item: link group: %w", err)
			}
		}
		res.Changed = append(res.Changed, "modifier_group_ids")
	}
	if p.ModifierOptOutIDs != nil {
		ids := dedupeIDs(*p.ModifierOptOutIDs)
		if err := requireGroupsTx(ctx, tx, ids); err != nil {
			return res, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM item_modifier_group_opt_outs WHERE item_id = ?`, p.ID); err != nil {
			return res, fmt.Errorf("save item: clear opt-outs: %w", err)
		}
		for _, gid := range ids {
			if _, err := tx.ExecContext(ctx, `INSERT INTO item_modifier_group_opt_outs (item_id, group_id) VALUES (?, ?)`, p.ID, gid); err != nil {
				return res, fmt.Errorf("save item: opt out: %w", err)
			}
		}
		res.Changed = append(res.Changed, "modifier_opt_out_ids")
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("save item: commit: %w", err)
	}
	res.Name = cur.Name
	return res, nil
}

// skuTakenError names the item that already holds sku.
func (r *CatalogRepo) skuTakenError(ctx context.Context, tx *sql.Tx, sku, selfID string) error {
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM items WHERE sku = ? AND id <> ?`, sku, selfID).Scan(&owner); err == nil {
		return fmt.Errorf("SKU %s is already used by %s: %w", sku, owner, ErrSKUExists)
	}
	return fmt.Errorf("SKU %s is already used by another item: %w", sku, ErrSKUExists)
}

// requireGroupsTx fails unless every id names an existing modifier group
// (active or not: a direct link to a deactivated group is kept, never
// silently dropped by a full-set save).
func requireGroupsTx(ctx context.Context, tx *sql.Tx, ids []string) error {
	for _, gid := range ids {
		if ok, err := rowExists(ctx, tx, `SELECT 1 FROM item_modifier_groups WHERE id = ?`, gid); err != nil {
			return fmt.Errorf("check modifier group: %w", err)
		} else if !ok {
			return fmt.Errorf("%w: %s", ErrModifierGroupNotFound, gid)
		}
	}
	return nil
}

// replaceItemBarcodesTx makes want (already resolved to stored keys) the
// item's full item-level barcode set, want[0] primary: barcodes no longer
// listed are deleted, the rest upserted. Availability is checked with the
// same ensureBarcodeAvailable AddBarcode uses, but WITHOUT AddBarcode's
// active-item check — an inactive item's barcodes are editable too (the
// Inactive filter's inspector).
func replaceItemBarcodesTx(ctx context.Context, tx *sql.Tx, itemID string, want []resolvedBarcode) error {
	keep := make(map[string]bool, len(want))
	for _, b := range want {
		keep[b.key] = true
		if err := ensureBarcodeAvailable(ctx, tx, b.key, "item", itemID); err != nil {
			var conflict *BarcodeConflictError
			if errors.As(err, &conflict) {
				conflict.Barcode = b.raw
			}
			return fmt.Errorf("barcode %s: %w", b.raw, err)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT barcode FROM item_barcodes WHERE item_id = ?`, itemID)
	if err != nil {
		return fmt.Errorf("save item: read barcodes: %w", err)
	}
	var drop []string
	for rows.Next() {
		var bc string
		if err := rows.Scan(&bc); err != nil {
			rows.Close()
			return fmt.Errorf("save item: read barcodes: %w", err)
		}
		if !keep[bc] {
			drop = append(drop, bc)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("save item: read barcodes: %w", err)
	}
	for _, bc := range drop {
		if _, err := tx.ExecContext(ctx, `DELETE FROM item_barcodes WHERE barcode = ? AND item_id = ?`, bc, itemID); err != nil {
			return fmt.Errorf("save item: delete barcode: %w", err)
		}
	}
	for i, b := range want {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO item_barcodes (barcode, item_id, barcode_type, is_primary)
VALUES (?, ?, ?, ?)
ON CONFLICT(barcode) DO UPDATE SET barcode_type = excluded.barcode_type, is_primary = excluded.is_primary
WHERE item_barcodes.item_id = excluded.item_id`, b.key, itemID, b.typ, boolToInt(i == 0)); err != nil {
			return fmt.Errorf("save item: write barcode %s: %w", b.raw, err)
		}
	}
	return nil
}

// CategorySave is a save_category directive (contract §3.2): nil = keep.
type CategorySave struct {
	ID               string
	Create           bool
	Name             *string
	ParentID         *string
	Color            *string
	Icon             *string
	ShowOnSaleScreen *bool
	GroupIDs         *[]string
	StationIDs       *[]string
}

// CategorySaveResult reports what SaveCategory did.
type CategorySaveResult struct {
	Created bool
	Name    string
	Changed []string
}

type catNode struct {
	parent string
	active bool
}

// loadCategoryTreeTx reads every category's parent and active flag.
func loadCategoryTreeTx(ctx context.Context, tx *sql.Tx) (map[string]catNode, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(parent_id, ''), is_active FROM categories`)
	if err != nil {
		return nil, fmt.Errorf("category tree: %w", err)
	}
	defer rows.Close()
	out := map[string]catNode{}
	for rows.Next() {
		var id string
		var n catNode
		if err := rows.Scan(&id, &n.parent, &n.active); err != nil {
			return nil, fmt.Errorf("category tree: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}

// isDescendant reports whether candidate sits below id (walking
// candidate's parents up; a seen-set bounds a malformed cycle).
func isDescendant(tree map[string]catNode, id, candidate string) bool {
	seen := map[string]bool{}
	for cur := tree[candidate].parent; cur != "" && !seen[cur]; cur = tree[cur].parent {
		if cur == id {
			return true
		}
		seen[cur] = true
	}
	return false
}

// levelOf is id's level (1 = top level), counting parents up.
func levelOf(tree map[string]catNode, id string) int {
	level := 1
	seen := map[string]bool{id: true}
	for cur := tree[id].parent; cur != "" && !seen[cur]; cur = tree[cur].parent {
		seen[cur] = true
		level++
	}
	return level
}

// subtreeHeight is how many levels hang below id among ACTIVE categories
// (0 = no active children).
func subtreeHeight(tree map[string]catNode, id string) int {
	children := map[string][]string{}
	for cid, n := range tree {
		if n.active && n.parent != "" {
			children[n.parent] = append(children[n.parent], cid)
		}
	}
	var walk func(string, map[string]bool) int
	walk = func(n string, seen map[string]bool) int {
		best := 0
		for _, c := range children[n] {
			if seen[c] {
				continue
			}
			seen[c] = true
			if h := 1 + walk(c, seen); h > best {
				best = h
			}
		}
		return best
	}
	return walk(id, map[string]bool{id: true})
}

// SaveCategory applies a save_category directive (contract §3.2) in one
// transaction: create-with-id or update, a parent move (the new parent must
// exist and be active, must not be the category itself or below it, and
// the whole moved subtree must stay within MaxCategoryDepth levels), the
// icon id (format only — the sale screen renders only ids the till's
// registry knows), the show-on-sale-screen flag, and the modifier-group and
// kitchen-station link sets.
func (r *CatalogRepo) SaveCategory(ctx context.Context, p CategorySave) (CategorySaveResult, error) {
	var res CategorySaveResult
	p.ID = strings.TrimSpace(p.ID)
	if p.ID == "" {
		return res, errors.New("missing id")
	}
	if p.Name != nil {
		n := strings.TrimSpace(*p.Name)
		if n == "" {
			return res, ErrCategoryNameRequired
		}
		p.Name = &n
	}
	if p.Color != nil {
		c := strings.TrimSpace(*p.Color)
		if !catalogtypes.ValidItemColor(c) {
			return res, fmt.Errorf("colour %q is not one of the palette colours", c)
		}
		p.Color = &c
	}
	if p.Icon != nil {
		ic := strings.TrimSpace(*p.Icon)
		if ic != "" && !iconid.ValidFormat(ic) {
			return res, fmt.Errorf("icon %q is not a valid icon id", ic)
		}
		p.Icon = &ic
	}
	if p.ParentID != nil {
		pid := strings.TrimSpace(*p.ParentID)
		p.ParentID = &pid
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("save category: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var name, parent, color, icon string
	var hidden, active bool
	err = tx.QueryRowContext(ctx, `
SELECT name, COALESCE(parent_id, ''), COALESCE(color, ''), COALESCE(icon, ''), sell_screen_hidden, is_active
FROM categories WHERE id = ?`, p.ID).Scan(&name, &parent, &color, &icon, &hidden, &active)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if !p.Create {
			return res, ErrCategoryNotFound
		}
		if p.Name == nil {
			return res, errors.New("a new category needs a name")
		}
		var maxOrder sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(sort_order) FROM categories`).Scan(&maxOrder); err != nil {
			return res, fmt.Errorf("save category: sort order: %w", err)
		}
		sortOrder := 0
		if maxOrder.Valid {
			sortOrder = int(maxOrder.Int64) + 1
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO categories (id, name, sort_order, is_active) VALUES (?, ?, ?, 1)`, p.ID, *p.Name, sortOrder); err != nil {
			return res, fmt.Errorf("save category: insert: %w", err)
		}
		name, active, res.Created = *p.Name, true, true
	case err != nil:
		return res, fmt.Errorf("save category: load: %w", err)
	}

	if p.Name != nil {
		if active {
			var other string
			err := tx.QueryRowContext(ctx, `SELECT name FROM categories WHERE is_active = 1 AND id <> ? AND lower(name) = lower(?) LIMIT 1`, p.ID, *p.Name).Scan(&other)
			if err == nil {
				return res, fmt.Errorf("a category named %s already exists", *p.Name)
			} else if !errors.Is(err, sql.ErrNoRows) {
				return res, fmt.Errorf("save category: name check: %w", err)
			}
		}
		name = *p.Name
		res.Changed = append(res.Changed, "name")
	}
	if p.ParentID != nil {
		newParent := *p.ParentID
		if newParent != "" {
			tree, err := loadCategoryTreeTx(ctx, tx)
			if err != nil {
				return res, err
			}
			pn, ok := tree[newParent]
			if !ok || !pn.active {
				return res, fmt.Errorf("parent category %s does not exist on this till or is deleted", newParent)
			}
			if newParent == p.ID || isDescendant(tree, p.ID, newParent) {
				return res, fmt.Errorf("a category cannot be moved inside itself")
			}
			if levelOf(tree, newParent)+1+subtreeHeight(tree, p.ID) > MaxCategoryDepth {
				return res, fmt.Errorf("categories cannot be nested deeper than %d levels", MaxCategoryDepth)
			}
		}
		parent = newParent
		res.Changed = append(res.Changed, "parent_id")
	}
	if p.Color != nil {
		color = *p.Color
		res.Changed = append(res.Changed, "color")
	}
	if p.Icon != nil {
		icon = *p.Icon
		res.Changed = append(res.Changed, "icon")
	}
	if p.ShowOnSaleScreen != nil {
		hidden = !*p.ShowOnSaleScreen
		res.Changed = append(res.Changed, "show_on_sale_screen")
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE categories SET name = ?, parent_id = ?, color = ?, icon = ?, sell_screen_hidden = ? WHERE id = ?`,
		name, nullableString(parent), nullableString(color), nullableString(icon), boolToInt(hidden), p.ID); err != nil {
		return res, fmt.Errorf("save category: row: %w", err)
	}
	if _, _, err := writeCategoryLinksTx(ctx, tx, p.ID, p.GroupIDs, p.StationIDs); err != nil {
		return res, err
	}
	if p.GroupIDs != nil {
		res.Changed = append(res.Changed, "modifier_group_ids")
	}
	if p.StationIDs != nil {
		res.Changed = append(res.Changed, "station_ids")
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("save category: commit: %w", err)
	}
	res.Name = name
	return res, nil
}

// CategoryDeleteResult reports what DeleteCategoryMoving did.
type CategoryDeleteResult struct {
	Name           string
	AlreadyDeleted bool
	MovedItems     int
	MovedChildren  int
}

// DeleteCategoryMoving applies a delete_category directive (contract §3.3)
// in one transaction: its subcategories move up to its own parent, every
// item in it (active and inactive) moves to moveItemsTo ("" =
// uncategorised), its modifier-group links and kitchen-station routes are
// deleted, and it is soft-deleted (is_active = 0) so past sales and
// reports keep its name. A target that is missing, deleted, the category
// itself or one of its subcategories fails the whole delete. An already
// deleted category reports AlreadyDeleted and changes nothing.
func (r *CatalogRepo) DeleteCategoryMoving(ctx context.Context, id, moveItemsTo string) (CategoryDeleteResult, error) {
	var res CategoryDeleteResult
	id, moveItemsTo = strings.TrimSpace(id), strings.TrimSpace(moveItemsTo)
	if id == "" {
		return res, errors.New("missing id")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return res, fmt.Errorf("delete category: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var parent string
	var active bool
	if err := tx.QueryRowContext(ctx, `SELECT name, COALESCE(parent_id, ''), is_active FROM categories WHERE id = ?`, id).Scan(&res.Name, &parent, &active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return res, ErrCategoryNotFound
		}
		return res, fmt.Errorf("delete category: load: %w", err)
	}
	if !active {
		res.AlreadyDeleted = true
		return res, nil
	}
	if moveItemsTo != "" {
		tree, err := loadCategoryTreeTx(ctx, tx)
		if err != nil {
			return res, err
		}
		t, ok := tree[moveItemsTo]
		switch {
		case !ok || !t.active:
			return res, fmt.Errorf("category %s to move the items to does not exist on this till or is deleted", moveItemsTo)
		case moveItemsTo == id || isDescendant(tree, id, moveItemsTo):
			return res, fmt.Errorf("the items cannot move to the category being deleted or one of its subcategories")
		}
	}
	out, err := tx.ExecContext(ctx, `UPDATE categories SET parent_id = ? WHERE parent_id = ?`, nullableString(parent), id)
	if err != nil {
		return res, fmt.Errorf("delete category: re-parent: %w", err)
	}
	n, _ := out.RowsAffected()
	res.MovedChildren = int(n)
	out, err = tx.ExecContext(ctx, `UPDATE items SET category_id = ? WHERE category_id = ?`, nullableString(moveItemsTo), id)
	if err != nil {
		return res, fmt.Errorf("delete category: move items: %w", err)
	}
	n, _ = out.RowsAffected()
	res.MovedItems = int(n)
	for _, q := range []string{
		`DELETE FROM category_modifier_group_links WHERE category_id = ?`,
		`DELETE FROM category_station_routes WHERE category_id = ?`,
		`UPDATE categories SET is_active = 0 WHERE id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return res, fmt.Errorf("delete category: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return res, fmt.Errorf("delete category: commit: %w", err)
	}
	return res, nil
}

// runeLen is utf8.RuneCountInString, named for the validation below.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// SetCategorySellScreenHidden sets categories.sell_screen_hidden — the
// local category editor's "Show on the sale screen" box (the till-side
// twin of save_category's show_on_sale_screen). Unknown id →
// ErrCategoryNotFound; nothing else on the row is touched.
func (r *CatalogRepo) SetCategorySellScreenHidden(ctx context.Context, id string, hidden bool) error {
	res, err := r.db.ExecContext(ctx, `UPDATE categories SET sell_screen_hidden = ? WHERE id = ?`, boolToInt(hidden), id)
	if err != nil {
		return fmt.Errorf("set category sell screen hidden: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrCategoryNotFound
	}
	return nil
}
