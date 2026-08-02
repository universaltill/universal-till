package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/logging"
)

type CatalogRepo struct {
	db *sql.DB
}

var ErrInvalidEAN13 = errors.New("invalid EAN-13 barcode")

func NewCatalogRepo(db *sql.DB) *CatalogRepo {
	return &CatalogRepo{db: db}
}

// BarcodeExists reports whether a barcode is already attached to any item
// or variant (import dedupe).
func (r *CatalogRepo) BarcodeExists(ctx context.Context, barcode string) (bool, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
SELECT (SELECT COUNT(*) FROM item_barcodes WHERE barcode = ?)
     + (SELECT COUNT(*) FROM variant_barcodes WHERE barcode = ?)`,
		barcode, barcode).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("barcode exists: %w", err)
	}
	return n > 0, nil
}

// SKUExists reports whether an item SKU is taken (import dedupe).
func (r *CatalogRepo) SKUExists(ctx context.Context, sku string) (bool, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM items WHERE sku = ?`, sku).Scan(&n); err != nil {
		return false, fmt.Errorf("sku exists: %w", err)
	}
	return n > 0, nil
}

// EnsureCategory returns the id of a category by name, creating it if
// missing (imports carry category names, not ids).
func (r *CatalogRepo) EnsureCategory(ctx context.Context, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	var id string
	err := r.db.QueryRowContext(ctx,
		`SELECT id FROM categories WHERE name = ? COLLATE NOCASE`, name).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != sql.ErrNoRows {
		return "", fmt.Errorf("find category: %w", err)
	}
	id = uuid.NewString()
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO categories (id, name) VALUES (?, ?)`, id, name); err != nil {
		return "", fmt.Errorf("create category: %w", err)
	}
	return id, nil
}

// ItemLabel is what a printed product label needs.
type ItemLabel struct {
	Name       string
	PriceMinor int64
	Code       string // primary barcode, else SKU
}

// GetItemLabel loads label data for one item: name, price, and the primary
// barcode (falling back to any barcode, then the SKU).
func (r *CatalogRepo) GetItemLabel(ctx context.Context, itemID string) (ItemLabel, bool, error) {
	var l ItemLabel
	var sku string
	err := r.db.QueryRowContext(ctx, `
SELECT name, base_price, COALESCE(sku, '') FROM items WHERE id = ?`, itemID).
		Scan(&l.Name, &l.PriceMinor, &sku)
	if err == sql.ErrNoRows {
		return ItemLabel{}, false, nil
	}
	if err != nil {
		return ItemLabel{}, false, fmt.Errorf("item label: %w", err)
	}
	_ = r.db.QueryRowContext(ctx, `
SELECT barcode FROM item_barcodes WHERE item_id = ?
ORDER BY is_primary DESC LIMIT 1`, itemID).Scan(&l.Code)
	if l.Code == "" {
		l.Code = sku
	}
	return l, true, nil
}

// ItemExists reports whether an item row exists (any active state).
func (r *CatalogRepo) ItemExists(ctx context.Context, itemID string) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM items WHERE id = ?`, itemID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *CatalogRepo) ListItems(ctx context.Context) ([]catalogtypes.ItemInput, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, sku, name, description, category_id, brand_id, unit, base_price, tax_code_id, is_active, is_weighed FROM items WHERE is_active = 1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []catalogtypes.ItemInput
	for rows.Next() {
		var itm catalogtypes.ItemInput
		var tax, cat, brand, desc sql.NullString
		if err := rows.Scan(&itm.ID, &itm.SKU, &itm.Name, &desc, &cat, &brand, &itm.Unit, &itm.BasePrice, &tax, &itm.IsActive, &itm.IsWeighed); err != nil {
			return nil, err
		}
		if desc.Valid {
			itm.Description = desc.String
		}
		if tax.Valid {
			itm.TaxCodeID = &tax.String
		}
		if cat.Valid {
			itm.CategoryID = &cat.String
		}
		if brand.Valid {
			itm.BrandID = &brand.String
		}
		out = append(out, itm)
	}
	return out, rows.Err()
}

// ItemBarcodes returns each active item's barcodes (primary first), keyed by
// item id — so the catalog page can SHOW what's attached to an item.
func (r *CatalogRepo) ItemBarcodes(ctx context.Context) (map[string][]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT item_id, barcode FROM item_barcodes ORDER BY is_primary DESC, barcode`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, bc string
		if err := rows.Scan(&id, &bc); err != nil {
			return nil, err
		}
		out[id] = append(out[id], bc)
	}
	return out, rows.Err()
}

// VariantView is one variant shown on the catalog page (with its own barcode,
// if any — variants can each carry a barcode).
type VariantView struct {
	ID         string
	Name       string
	Barcode    string
	PriceMinor int64
}

// ItemVariants returns each active item's variants (with each variant's primary
// barcode), keyed by item id.
func (r *CatalogRepo) ItemVariants(ctx context.Context) (map[string][]VariantView, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT v.item_id, v.id, v.name, v.price,
       COALESCE((SELECT b.barcode FROM variant_barcodes b WHERE b.variant_id = v.id
                 ORDER BY b.is_primary DESC, b.barcode LIMIT 1), '')
FROM item_variants v
WHERE v.is_active = 1
ORDER BY v.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]VariantView{}
	for rows.Next() {
		var itemID string
		var v VariantView
		if err := rows.Scan(&itemID, &v.ID, &v.Name, &v.PriceMinor, &v.Barcode); err != nil {
			return nil, err
		}
		out[itemID] = append(out[itemID], v)
	}
	return out, rows.Err()
}

// VariantEditView is one variant in the item's edit panel: everything the
// operator can change, including inactive variants (so they can be
// reactivated) and every barcode attached to the variant.
type VariantEditView struct {
	ID         string
	Name       string
	SKU        string
	PriceMinor int64
	CostMinor  int64 // cost price, 0 = unset (margin report input)
	IsActive   bool
	Barcodes   []string
}

// VariantLabel is what a shelf/product label needs for ONE variant: the
// composed display name, the variant's own price and its scannable code
// (primary variant barcode, else the variant SKU).
type VariantLabel struct {
	Name       string
	PriceMinor int64
	Code       string
}

// GetVariantLabel loads a variant's label data (item name + variant name).
func (r *CatalogRepo) GetVariantLabel(ctx context.Context, variantID string) (VariantLabel, bool, error) {
	var l VariantLabel
	var itemName, vName, sku string
	err := r.db.QueryRowContext(ctx, `
SELECT i.name, v.name, COALESCE(v.sku, ''), v.price,
       COALESCE((SELECT b.barcode FROM variant_barcodes b WHERE b.variant_id = v.id
                 ORDER BY b.is_primary DESC, b.barcode LIMIT 1), '')
FROM item_variants v JOIN items i ON i.id = v.item_id
WHERE v.id = ?`, variantID).Scan(&itemName, &vName, &sku, &l.PriceMinor, &l.Code)
	if err == sql.ErrNoRows {
		return l, false, nil
	}
	if err != nil {
		return l, false, err
	}
	l.Name = strings.TrimSpace(itemName + " " + vName)
	if l.Code == "" {
		l.Code = sku
	}
	return l, true, nil
}

// VariantsForItem returns ALL of one item's variants (active and retired)
// with their barcodes — the catalog's per-item edit panel.
func (r *CatalogRepo) VariantsForItem(ctx context.Context, itemID string) ([]VariantEditView, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, COALESCE(sku, ''), price, COALESCE(cost_price, 0), is_active
FROM item_variants WHERE item_id = ? ORDER BY is_active DESC, name`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VariantEditView
	byID := map[string]int{}
	for rows.Next() {
		var v VariantEditView
		if err := rows.Scan(&v.ID, &v.Name, &v.SKU, &v.PriceMinor, &v.CostMinor, &v.IsActive); err != nil {
			return nil, err
		}
		byID[v.ID] = len(out)
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	brows, err := r.db.QueryContext(ctx, `
SELECT b.variant_id, b.barcode FROM variant_barcodes b
JOIN item_variants v ON v.id = b.variant_id
WHERE v.item_id = ? ORDER BY b.is_primary DESC, b.barcode`, itemID)
	if err != nil {
		return nil, err
	}
	defer brows.Close()
	for brows.Next() {
		var vid, bc string
		if err := brows.Scan(&vid, &bc); err != nil {
			return nil, err
		}
		if i, ok := byID[vid]; ok {
			out[i].Barcodes = append(out[i].Barcodes, bc)
		}
	}
	return out, brows.Err()
}

// ItemBarcode is one whole-item barcode row for the edit panel.
type ItemBarcode struct {
	Barcode   string
	IsPrimary bool
}

// BarcodesForItem returns one item's own (whole-item) barcodes.
func (r *CatalogRepo) BarcodesForItem(ctx context.Context, itemID string) ([]ItemBarcode, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT barcode, is_primary FROM item_barcodes WHERE item_id = ?
ORDER BY is_primary DESC, barcode`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ItemBarcode
	for rows.Next() {
		var b ItemBarcode
		if err := rows.Scan(&b.Barcode, &b.IsPrimary); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteBarcode detaches a barcode wherever it is attached (item or variant).
// Fixing a mis-scanned or reassigned code is a normal back-office task.
func (r *CatalogRepo) DeleteBarcode(ctx context.Context, barcode string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM item_barcodes WHERE barcode = ?`, barcode); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM variant_barcodes WHERE barcode = ?`, barcode)
	return err
}

// ExportRow is one catalog line for the CSV export (G22b — the
// anti-lock-in half of import: a shop can always take its data and leave).
// Columns are chosen so our own importer round-trips the file.
type ExportRow struct {
	Name        string
	SKU         string
	Barcode     string // primary barcode preferred, else any
	PriceMinor  int64
	Category    string
	Description string
	IsWeighed   bool
	Stock       float64
	IsActive    bool
}

// ExportRows reads the whole catalog for export, active items first.
func (r *CatalogRepo) ExportRows(ctx context.Context) ([]ExportRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT i.name, COALESCE(i.sku, ''),
       COALESCE((SELECT b.barcode FROM item_barcodes b WHERE b.item_id = i.id
                 ORDER BY b.is_primary DESC, b.barcode LIMIT 1), ''),
       i.base_price, COALESCE(c.name, ''), COALESCE(i.description, ''),
       i.is_weighed,
       COALESCE((SELECT SUM(v.quantity) FROM inventory v WHERE v.item_id = i.id), 0),
       i.is_active
FROM items i LEFT JOIN categories c ON c.id = i.category_id
ORDER BY i.is_active DESC, i.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExportRow
	for rows.Next() {
		var e ExportRow
		if err := rows.Scan(&e.Name, &e.SKU, &e.Barcode, &e.PriceMinor, &e.Category,
			&e.Description, &e.IsWeighed, &e.Stock, &e.IsActive); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

type Lookup struct {
	ID   string
	Name string
}

// CategoryNode is one row of the category table, flat — callers assemble
// the parent/child tree themselves from ParentID.
type CategoryNode struct {
	ID        string
	Name      string
	ParentID  string // empty for a top-level category
	SortOrder int
	Color     string // empty when no explicit color is set
}

// ListCategories returns every category (flat, parent_id/color intact) for
// building the sale-screen's nested, color-coded category grid.
func (r *CatalogRepo) ListCategories(ctx context.Context) ([]CategoryNode, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, COALESCE(parent_id, ''), sort_order, COALESCE(color, '')
FROM categories
ORDER BY sort_order, name`)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	defer rows.Close()
	var out []CategoryNode
	for rows.Next() {
		var c CategoryNode
		if err := rows.Scan(&c.ID, &c.Name, &c.ParentID, &c.SortOrder, &c.Color); err != nil {
			return nil, fmt.Errorf("list categories: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *CatalogRepo) ReadLookup(ctx context.Context, table string) ([]Lookup, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name FROM `+table+` WHERE (is_active IS NULL OR is_active = 1) ORDER BY name`)
	if err != nil {
		if strings.Contains(err.Error(), "no such column: is_active") {
			rows, err = r.db.QueryContext(ctx, `SELECT id, name FROM `+table+` ORDER BY name`)
		}
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []Lookup
	for rows.Next() {
		var l Lookup
		if err := rows.Scan(&l.ID, &l.Name); err != nil {
			return nil, err
		}
		res = append(res, l)
	}
	return res, rows.Err()
}

// ValidateLookup checks existence of ids in a lookup table.
func (r *CatalogRepo) ValidateLookup(ctx context.Context, table string, id string, required bool) error {
	if id == "" && !required {
		return nil
	}
	var exists int
	if err := r.db.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = ?`, id).Scan(&exists); err != nil {
		return errors.New("invalid " + table + " id")
	}
	return nil
}

func (r *CatalogRepo) DeactivateItem(ctx context.Context, itemID string) error {
	if itemID == "" {
		return errors.New("itemID required")
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE items SET is_active = 0 WHERE id = ?`, itemID); err != nil {
		return fmt.Errorf("deactivate item: %w", err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE item_variants SET is_active = 0 WHERE item_id = ?`, itemID); err != nil {
		return fmt.Errorf("deactivate item variants: %w", err)
	}
	return nil
}

func (r *CatalogRepo) DeactivateVariant(ctx context.Context, variantID string) error {
	if variantID == "" {
		return errors.New("variantID required")
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE item_variants SET is_active = 0 WHERE id = ?`, variantID); err != nil {
		return fmt.Errorf("deactivate variant: %w", err)
	}
	return nil
}

func (r *CatalogRepo) CreateItem(ctx context.Context, in catalogtypes.ItemInput) (string, error) {
	if in.Name == "" {
		return "", errors.New("name required")
	}
	if in.Unit == "" {
		in.Unit = "each"
	}
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	if in.SKU == "" {
		in.SKU = in.ID
	}
	active := 1
	if !in.IsActive {
		active = 0
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO items (id, sku, name, description, category_id, brand_id, unit, base_price, tax_code_id, is_active, is_weighed)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, in.ID, in.SKU, in.Name, in.Description, nullable(in.CategoryID), nullable(in.BrandID), in.Unit, in.BasePrice, nullable(in.TaxCodeID), active, boolToInt(in.IsWeighed))
	if err != nil {
		return "", fmt.Errorf("insert item: %w", err)
	}
	// Without this, a newly created item has NO row in inventory at all
	// (RecordStockMovement's UPDATE can't create one, only adjust an
	// existing one) — invisible on the Inventory page until some other
	// stock action happened to touch it first. Confirmed live: an item
	// added through this form showed up in the catalog list but nowhere in
	// Inventory. Genuinely best-effort, not just in name: the item is
	// already committed and perfectly usable for sale without a stock
	// row (stock tracking is opt-in), so a failure here — e.g. no
	// stock_locations table at all, a legitimately stockless deployment —
	// must never fail item creation itself, only get logged.
	if err := r.ensureInventoryRow(ctx, in.ID, ""); err != nil {
		logging.L().Warnf("catalog: create item %s: inventory row not created: %v", in.ID, err)
	}
	return in.ID, nil
}

// ensureInventoryRow creates a zero-quantity inventory row for a new item or
// variant (exactly one of itemID/variantID set) at the default stock
// location, if one doesn't already exist. See CreateItem/CreateVariant.
func (r *CatalogRepo) ensureInventoryRow(ctx context.Context, itemID, variantID string) error {
	var locationID string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM stock_locations WHERE name = 'Main' OR id = 'loc_main' ORDER BY id LIMIT 1`).Scan(&locationID)
	if errors.Is(err, sql.ErrNoRows) {
		locationID = "loc_main"
		if _, err := r.db.ExecContext(ctx, `INSERT INTO stock_locations (id, name) VALUES (?, ?)`, locationID, "Main"); err != nil {
			return fmt.Errorf("create default location: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("find default location: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
INSERT OR IGNORE INTO inventory (id, item_id, variant_id, location_id, quantity)
VALUES (?, ?, ?, ?, 0)
`, uuid.NewString(), nullIfEmpty(itemID), nullIfEmpty(variantID), locationID)
	if err != nil {
		return fmt.Errorf("insert inventory row: %w", err)
	}
	return nil
}

// ItemCostPrice returns the item's cost price in minor units (0 = unset).
func (r *CatalogRepo) ItemCostPrice(ctx context.Context, itemID string) (int64, error) {
	var cost sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT cost_price FROM items WHERE id = ?`, itemID).Scan(&cost)
	if err != nil {
		return 0, err
	}
	return cost.Int64, nil
}

// SetItemPrice updates just the selling price (minor units) — the cloud's
// remote price directive (ADR-0018); everything else on the item is
// untouched. The id may be an item OR a variant (the snapshot lists both);
// a miss on both reports "item not found" so the directive result says WHY.
func (r *CatalogRepo) SetItemPrice(ctx context.Context, itemID string, priceMinor int64) error {
	if itemID == "" {
		return errors.New("id required")
	}
	res, err := r.db.ExecContext(ctx, `UPDATE items SET base_price = ?, updated_at = datetime('now') WHERE id = ?`, priceMinor, itemID)
	if err != nil {
		return fmt.Errorf("set item price: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	res, err = r.db.ExecContext(ctx, `UPDATE item_variants SET price = ? WHERE id = ? AND is_active = 1`, priceMinor, itemID)
	if err != nil {
		return fmt.Errorf("set variant price: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("item not found")
	}
	return nil
}

// FindActiveItemByName returns the id of an active item with this exact
// name, if any — the idempotency check for the cloud's create directive
// (a retried create must not duplicate the item).
func (r *CatalogRepo) FindActiveItemByName(ctx context.Context, name string) (string, bool, error) {
	var id string
	err := r.db.QueryRowContext(ctx,
		`SELECT id FROM items WHERE name = ? AND is_active = 1 LIMIT 1`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find item by name: %w", err)
	}
	return id, true, nil
}

// SetItemName renames an item (or, on fall-through, a variant) — the cloud's
// remote rename directive (ADR-0018). Same shape as SetItemPrice.
func (r *CatalogRepo) SetItemName(ctx context.Context, id, name string) error {
	if id == "" || strings.TrimSpace(name) == "" {
		return errors.New("id and name required")
	}
	res, err := r.db.ExecContext(ctx, `UPDATE items SET name = ?, updated_at = datetime('now') WHERE id = ?`, name, id)
	if err != nil {
		return fmt.Errorf("set item name: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	res, err = r.db.ExecContext(ctx, `UPDATE item_variants SET name = ? WHERE id = ? AND is_active = 1`, name, id)
	if err != nil {
		return fmt.Errorf("set variant name: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("item not found")
	}
	return nil
}

// SetItemCostPrice records what the shop pays for the item (minor units) —
// the input the margin report needs.
func (r *CatalogRepo) SetItemCostPrice(ctx context.Context, itemID string, costMinor int64) error {
	if itemID == "" {
		return errors.New("id required")
	}
	_, err := r.db.ExecContext(ctx, `UPDATE items SET cost_price = ?, updated_at = datetime('now') WHERE id = ?`, costMinor, itemID)
	return err
}

// ItemLeadTimeDays returns the item's configured reorder lead time in days
// (0 = unset) — the inventory page's warn/reorder-suggestion thresholds
// fall back to their flat defaults when this is 0.
func (r *CatalogRepo) ItemLeadTimeDays(ctx context.Context, itemID string) (int, error) {
	var days int
	err := r.db.QueryRowContext(ctx, `SELECT lead_time_days FROM items WHERE id = ?`, itemID).Scan(&days)
	if err != nil {
		return 0, err
	}
	return days, nil
}

// SetItemLeadTimeDays records how many days it takes to receive a reorder
// for this item (universaltill/ut-docs#85).
func (r *CatalogRepo) SetItemLeadTimeDays(ctx context.Context, itemID string, days int) error {
	if itemID == "" {
		return errors.New("id required")
	}
	_, err := r.db.ExecContext(ctx, `UPDATE items SET lead_time_days = ?, updated_at = datetime('now') WHERE id = ?`, days, itemID)
	return err
}

func (r *CatalogRepo) UpdateItem(ctx context.Context, in catalogtypes.ItemInput) error {
	if in.ID == "" {
		return errors.New("id required")
	}
	if in.Name == "" {
		return errors.New("name required")
	}
	if in.Unit == "" {
		in.Unit = "each"
	}
	active := 1
	if !in.IsActive {
		active = 0
	}
	_, err := r.db.ExecContext(ctx, `
UPDATE items
SET sku = COALESCE(NULLIF(?, ''), sku),
    name = ?,
    description = ?,
    category_id = ?,
    brand_id = ?,
    unit = ?,
    base_price = ?,
    tax_code_id = ?,
    is_active = ?,
    is_weighed = ?
WHERE id = ?
`, nullableString(in.SKU), in.Name, in.Description, nullable(in.CategoryID), nullable(in.BrandID), in.Unit, in.BasePrice, nullable(in.TaxCodeID), active, boolToInt(in.IsWeighed), in.ID)
	if err != nil {
		return fmt.Errorf("update item: %w", err)
	}
	return nil
}

func (r *CatalogRepo) CreateVariant(ctx context.Context, in catalogtypes.VariantInput) (string, error) {
	if in.ItemID == "" {
		return "", errors.New("item_id required")
	}
	if in.Name == "" {
		return "", errors.New("name required")
	}
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	active := 1
	if !in.IsActive {
		active = 0
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO item_variants (id, item_id, sku, name, price, cost_price, is_active)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, in.ID, in.ItemID, nullableString(in.SKU), in.Name, in.Price, nullableInt64(in.CostPrice), active)
	if err != nil {
		return "", fmt.Errorf("insert variant: %w", err)
	}
	// Same reasoning as CreateItem: without this a new variant has no
	// inventory row and is invisible on the Inventory page. Best-effort,
	// same as CreateItem — never fails variant creation itself.
	if err := r.ensureInventoryRow(ctx, "", in.ID); err != nil {
		logging.L().Warnf("catalog: create variant %s: inventory row not created: %v", in.ID, err)
	}
	return in.ID, nil
}

func (r *CatalogRepo) UpdateVariant(ctx context.Context, in catalogtypes.VariantInput) error {
	if in.ID == "" {
		return errors.New("id required")
	}
	if in.Name == "" {
		return errors.New("name required")
	}
	active := 1
	if !in.IsActive {
		active = 0
	}
	_, err := r.db.ExecContext(ctx, `
UPDATE item_variants
SET sku = COALESCE(NULLIF(?, ''), sku),
    name = ?,
    price = ?,
    cost_price = ?,
    is_active = ?
WHERE id = ?
`, nullableString(in.SKU), in.Name, in.Price, nullableInt64(in.CostPrice), active, in.ID)
	if err != nil {
		return fmt.Errorf("update variant: %w", err)
	}
	return nil
}

func (r *CatalogRepo) AddBarcode(ctx context.Context, in catalogtypes.BarcodeInput) error {
	if in.Barcode == "" {
		return errors.New("barcode required")
	}
	if (in.ItemID == "" && in.VariantID == "") || (in.ItemID != "" && in.VariantID != "") {
		return errors.New("provide exactly one of item_id or variant_id")
	}
	in.BarcodeType = strings.TrimSpace(in.BarcodeType)
	if in.BarcodeType == "" {
		// The catalog accepts arbitrary scanner and internal PLU values (ADR-0021).
		// Treat a 13-digit value as the retail EAN-13 it appears to be, while
		// retaining CODE128 as the permissive default for other shapes.
		if len(in.Barcode) == 13 {
			in.BarcodeType = "EAN13"
		} else {
			in.BarcodeType = "CODE128"
		}
	}
	if strings.EqualFold(in.BarcodeType, "EAN13") {
		in.BarcodeType = "EAN13"
		if !validEAN13(in.Barcode) {
			return fmt.Errorf("%w %q: expected 13 digits with a valid check digit", ErrInvalidEAN13, in.Barcode)
		}
	}
	if in.VariantID != "" {
		if err := ensureBarcodeAvailable(ctx, r.db, in.Barcode, "variant", in.VariantID); err != nil {
			return err
		}
		var active int
		if err := r.db.QueryRowContext(ctx, `SELECT is_active FROM item_variants WHERE id = ?`, in.VariantID).Scan(&active); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("variant not found: %s", in.VariantID)
			}
			return err
		}
		if active == 0 {
			return fmt.Errorf("variant %s inactive", in.VariantID)
		}
		_, err := r.db.ExecContext(ctx, `
INSERT INTO variant_barcodes (barcode, variant_id, barcode_type, is_primary)
VALUES (?, ?, ?, ?)
ON CONFLICT(barcode) DO UPDATE SET variant_id=excluded.variant_id, barcode_type=excluded.barcode_type, is_primary=excluded.is_primary
`, in.Barcode, in.VariantID, in.BarcodeType, boolToInt(in.IsPrimary))
		return err
	}
	// item barcode
	if err := ensureBarcodeAvailable(ctx, r.db, in.Barcode, "item", in.ItemID); err != nil {
		return err
	}
	var active int
	if err := r.db.QueryRowContext(ctx, `SELECT is_active FROM items WHERE id = ?`, in.ItemID).Scan(&active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("item not found: %s", in.ItemID)
		}
		return err
	}
	if active == 0 {
		return fmt.Errorf("item %s inactive", in.ItemID)
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO item_barcodes (barcode, item_id, barcode_type, is_primary)
VALUES (?, ?, ?, ?)
ON CONFLICT(barcode) DO UPDATE SET item_id=excluded.item_id, barcode_type=excluded.barcode_type, is_primary=excluded.is_primary
`, in.Barcode, in.ItemID, in.BarcodeType, boolToInt(in.IsPrimary))
	return err
}

func validEAN13(barcode string) bool {
	if len(barcode) != 13 {
		return false
	}
	sum := 0
	for i := 0; i < 12; i++ {
		digit := barcode[i] - '0'
		if digit > 9 {
			return false
		}
		if i%2 == 0 {
			sum += int(digit)
		} else {
			sum += 3 * int(digit)
		}
	}
	checkDigit := barcode[12] - '0'
	if checkDigit > 9 {
		return false
	}
	return int(checkDigit) == (10-sum%10)%10
}

func nullable(s *string) any {
	if s == nil || *s == "" {
		return nil
	}
	return *s
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ensureBarcodeAvailable enforces the (item_id XOR variant_id) rule across barcode tables.
// It permits re-inserting the same barcode for the same target, but blocks cross-target moves.
func ensureBarcodeAvailable(ctx context.Context, db *sql.DB, barcode, targetType, targetID string) error {
	var existing string
	switch targetType {
	case "item":
		if err := db.QueryRowContext(ctx, `SELECT item_id FROM item_barcodes WHERE barcode = ?`, barcode).Scan(&existing); err == nil {
			if existing != targetID {
				return fmt.Errorf("barcode already assigned to item %s", existing)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := db.QueryRowContext(ctx, `SELECT variant_id FROM variant_barcodes WHERE barcode = ?`, barcode).Scan(&existing); err == nil {
			return fmt.Errorf("barcode already assigned to variant %s", existing)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	case "variant":
		if err := db.QueryRowContext(ctx, `SELECT variant_id FROM variant_barcodes WHERE barcode = ?`, barcode).Scan(&existing); err == nil {
			if existing != targetID {
				return fmt.Errorf("barcode already assigned to variant %s", existing)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := db.QueryRowContext(ctx, `SELECT item_id FROM item_barcodes WHERE barcode = ?`, barcode).Scan(&existing); err == nil {
			return fmt.Errorf("barcode already assigned to item %s", existing)
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	default:
		return errors.New("invalid targetType for barcode")
	}
	return nil
}
