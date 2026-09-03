package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/barcode"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/taxrate"
)

type CatalogRepo struct {
	db *sql.DB
	// settings reads the shop's enabled barcode symbologies (ADR-0059 §2)
	// for AddBarcode's untyped-inference path — same accessor the scan path
	// (POSRepo) and the ut-docs#935 settings checklist share.
	settings *SettingsRepo
}

var ErrInvalidEAN13 = errors.New("invalid EAN-13 barcode")

// ErrBarcodeNoSymbologyMatch reports that an untyped AddBarcode value
// matched none of the shop's enabled symbologies (ADR-0059 Decision §3's
// named rejection: no row is written, barcode_type is not set). Only
// reachable once a shop has narrowed its enabled set away from the
// CODE128/INTERNAL_PLU catch-alls — the default set accepts everything, by
// design (ADR-0059 Decision §2).
var ErrBarcodeNoSymbologyMatch = errors.New("barcode matches no enabled symbology")

// BarcodeConflictError reports that a barcode is already assigned to a
// different item/variant. It carries the conflicting target as structured
// data (not just prose) so a caller can resolve it to a name/SKU for the
// operator instead of exposing the raw internal ID (ut-docs#303).
type BarcodeConflictError struct {
	TargetType string // "item" or "variant"
	TargetID   string
}

func (e *BarcodeConflictError) Error() string {
	return fmt.Sprintf("barcode already assigned to %s %s", e.TargetType, e.TargetID)
}

// ErrTaxCodeNameExists reports that tax_codes.name's UNIQUE constraint
// rejected a CreateTaxCode/UpdateTaxCode call (ut-docs#259's tax-code
// management UI) -- the handler surfaces a validation message instead of a
// raw 500, same style as ErrPromotionCodeExists (pos_repo.go).
var ErrTaxCodeNameExists = errors.New("tax code name already exists")

// ErrTaxCodeNotFound reports that GetTaxCode/UpdateTaxCode's id doesn't
// match any tax_codes row.
var ErrTaxCodeNotFound = errors.New("tax code not found")

// ErrSKUExists reports that items.sku or item_variants.sku's UNIQUE
// constraint rejected a Create/UpdateItem or Create/UpdateVariant call
// (ut-docs#316's review) -- items and item_variants each carry exactly one
// UNIQUE column (sku, 001_init.sql), the same one-constraint assumption
// isUniqueViolation's ErrTaxCodeNameExists usage above already relies on,
// so the handler can surface a specific "that SKU is already in use"
// message instead of downgrading to the generic invalid-request one that
// names nothing.
var ErrSKUExists = errors.New("SKU already in use")

func NewCatalogRepo(db *sql.DB) *CatalogRepo {
	return &CatalogRepo{db: db, settings: NewSettingsRepo(db)}
}

// BarcodeExists reports whether a barcode is already attached to any item
// or variant (import dedupe).
//
// Tries the code exactly as given first, then — only on a miss — its
// canonical form (ADR-0059's zeroed embedded-data key, ut-docs#948 F6).
// Exact-first matters: a row added via AddBarcode's explicit-BarcodeType
// escape hatch (ut-docs#948 F1) is stored under the RAW code even when
// that code would otherwise infer to an embedded symbology, so
// canonicalising first would redirect the check to a key nothing was ever
// written under and miss the row that's actually there.
func (r *CatalogRepo) BarcodeExists(ctx context.Context, barcode string) (bool, error) {
	exists, err := r.barcodeExistsExact(ctx, barcode)
	if err != nil || exists {
		return exists, err
	}
	if canonical := r.canonicalBarcodeKey(ctx, barcode); canonical != barcode {
		return r.barcodeExistsExact(ctx, canonical)
	}
	return false, nil
}

func (r *CatalogRepo) barcodeExistsExact(ctx context.Context, barcode string) (bool, error) {
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

// matchBarcode resolves code against the shop's currently-enabled
// symbologies (ADR-0059 §2/§3) via the shared registry matcher — the same
// function the scan path uses, so AddBarcode and scanning can never
// disagree on what a code means. Never bricks barcode entry on a settings
// read failure: EnabledBarcodeSymbologies already returns the
// compatibility-preserving default set alongside the error, so callers
// degrade gracefully rather than erroring out.
//
// This is the single place computing a barcode match — AddBarcode's
// untyped-inference path and canonicalBarcodeKey (ut-docs#948 F6) both
// call this rather than each running their own copy of the enabledIDs
// read + Match call, per the #948 review's F-3 finding (an earlier draft
// of this method had a doc comment claiming that de-duplication while
// AddBarcode still inlined its own copy).
func (r *CatalogRepo) matchBarcode(ctx context.Context, code string) (dec barcode.Decoded, enabledIDs []string, ok bool) {
	enabledIDs, err := r.settings.EnabledBarcodeSymbologies(ctx)
	if err != nil {
		logging.L().Warnf("catalog: match barcode: enabled symbologies unavailable, using defaults: %v", err)
	}
	dec, ok = barcode.Default().Match(enabledIDs, code)
	return dec, enabledIDs, ok
}

// canonicalBarcodeKey returns the key AddBarcode's untyped-inference path
// would store for code under the shop's currently-enabled symbologies —
// the zeroed template for an embedded-data match, or code unchanged when
// nothing matches (same graceful fallback as a settings-read failure: see
// matchBarcode). Used by the BarcodeExists/DeleteBarcode pre-checks
// (ut-docs#948 F6).
func (r *CatalogRepo) canonicalBarcodeKey(ctx context.Context, code string) string {
	dec, _, ok := r.matchBarcode(ctx, code)
	if !ok {
		return code
	}
	return dec.LookupKey
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
	// COALESCE(sku, '') — ut-docs#1176: sku is nullable (no real SKU stores
	// NULL, not a UUID), and itm.SKU below is a plain string, so scanning a
	// NULL directly would error on every item that has no real SKU.
	rows, err := r.db.QueryContext(ctx, `SELECT id, COALESCE(sku, ''), name, description, category_id, brand_id, unit, base_price, tax_code_id, is_active, is_weighed, is_sample_data FROM items WHERE is_active = 1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []catalogtypes.ItemInput
	for rows.Next() {
		var itm catalogtypes.ItemInput
		var tax, cat, brand, desc sql.NullString
		if err := rows.Scan(&itm.ID, &itm.SKU, &itm.Name, &desc, &cat, &brand, &itm.Unit, &itm.BasePrice, &tax, &itm.IsActive, &itm.IsWeighed, &itm.IsSampleData); err != nil {
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

// GetItem loads ONE item row — the single-row counterpart to ListItems
// (identical column set and scan logic, filtered to one id) for the
// row-level re-render after a catalog mutation (ut-docs#1363). Deliberately
// no is_active filter: callers only ever fetch an item they just
// created/updated, and the deactivate path needs the miss/hit distinction
// from the bool, not an active-only view. A missing id is (zero, false,
// nil), not an error.
func (r *CatalogRepo) GetItem(ctx context.Context, itemID string) (catalogtypes.ItemInput, bool, error) {
	return getItemExec(ctx, r.db, itemID)
}

// getItemExec is GetItem's actual logic, generalized over the execer
// interface (see ensureInventoryRowExec) so UpdateItemReturningWasActive can
// run the same read against a caller-held *sql.Tx instead of the repo's
// *sql.DB.
func getItemExec(ctx context.Context, ex execer, itemID string) (catalogtypes.ItemInput, bool, error) {
	var itm catalogtypes.ItemInput
	var tax, cat, brand, desc sql.NullString
	err := ex.QueryRowContext(ctx, `SELECT id, COALESCE(sku, ''), name, description, category_id, brand_id, unit, base_price, tax_code_id, is_active, is_weighed, is_sample_data FROM items WHERE id = ?`, itemID).
		Scan(&itm.ID, &itm.SKU, &itm.Name, &desc, &cat, &brand, &itm.Unit, &itm.BasePrice, &tax, &itm.IsActive, &itm.IsWeighed, &itm.IsSampleData)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogtypes.ItemInput{}, false, nil
	}
	if err != nil {
		return catalogtypes.ItemInput{}, false, fmt.Errorf("get item: %w", err)
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
	return itm, true, nil
}

// ItemBarcodesFor returns ONE item's barcodes (primary first, then by
// code) — the single-item counterpart to ItemBarcodes, same ordering
// contract, for the row-level re-render (ut-docs#1363).
func (r *CatalogRepo) ItemBarcodesFor(ctx context.Context, itemID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT barcode FROM item_barcodes WHERE item_id = ? ORDER BY is_primary DESC, barcode`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var bc string
		if err := rows.Scan(&bc); err != nil {
			return nil, err
		}
		out = append(out, bc)
	}
	return out, rows.Err()
}

// ItemIDForVariant resolves a variant to its parent item id — the
// row-level re-render after a variant mutation needs the ITEM whose
// summary line changed, and the variant-deactivate form doesn't carry it
// (ut-docs#1363). No is_active filter: a just-deactivated variant still
// has a row, and its parent's summary is exactly what needs refreshing.
func (r *CatalogRepo) ItemIDForVariant(ctx context.Context, variantID string) (string, bool, error) {
	var itemID string
	err := r.db.QueryRowContext(ctx, `SELECT item_id FROM item_variants WHERE id = ?`, variantID).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("item id for variant: %w", err)
	}
	return itemID, true, nil
}

// ItemIDForBarcode resolves an attached barcode to the item whose catalog
// row shows it — directly for an item barcode, via the parent item for a
// variant barcode (ut-docs#1363; called BEFORE DeleteBarcode so the row
// can still be found). Same exact-first-then-canonical resolution as
// BarcodeExists/DeleteBarcode (ut-docs#948 F6) so this names the row the
// delete will actually touch.
func (r *CatalogRepo) ItemIDForBarcode(ctx context.Context, barcode string) (string, bool, error) {
	id, ok, err := r.itemIDForBarcodeExact(ctx, barcode)
	if err != nil || ok {
		return id, ok, err
	}
	if canonical := r.canonicalBarcodeKey(ctx, barcode); canonical != barcode {
		return r.itemIDForBarcodeExact(ctx, canonical)
	}
	return "", false, nil
}

func (r *CatalogRepo) itemIDForBarcodeExact(ctx context.Context, barcode string) (string, bool, error) {
	var itemID string
	err := r.db.QueryRowContext(ctx, `SELECT item_id FROM item_barcodes WHERE barcode = ?`, barcode).Scan(&itemID)
	if err == nil {
		return itemID, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("item id for barcode: %w", err)
	}
	err = r.db.QueryRowContext(ctx, `
SELECT v.item_id FROM variant_barcodes b
JOIN item_variants v ON v.id = b.variant_id
WHERE b.barcode = ?`, barcode).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("item id for barcode: %w", err)
	}
	return itemID, true, nil
}

// HasActiveItems reports whether ANY active item exists — the cheap
// existence probe the empty-state placeholder row hangs off after a
// deactivate (ut-docs#1363).
func (r *CatalogRepo) HasActiveItems(ctx context.Context) (bool, error) {
	var exists int
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM items WHERE is_active = 1)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("has active items: %w", err)
	}
	return exists == 1, nil
}

// HasOtherActiveItems reports whether any active item EXCEPT itemID
// exists (ut-docs#1363): after an insert it decides whether the
// empty-state placeholder is in the DOM (the new item is the sole active
// item) and therefore whether the response may carry the placeholder's
// OOB delete — htmx logs a console error for an OOB delete with no
// matching element, so it can't just be emitted unconditionally.
func (r *CatalogRepo) HasOtherActiveItems(ctx context.Context, itemID string) (bool, error) {
	var exists int
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM items WHERE is_active = 1 AND id <> ?)`, itemID).Scan(&exists); err != nil {
		return false, fmt.Errorf("has other active items: %w", err)
	}
	return exists == 1, nil
}

// ItemVariantsFor returns ONE item's active variants (with each variant's
// primary barcode), name-ordered — the single-item counterpart to
// ItemVariants for the row-level re-render (ut-docs#1363).
func (r *CatalogRepo) ItemVariantsFor(ctx context.Context, itemID string) ([]VariantView, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT v.id, v.name, v.price,
       COALESCE((SELECT b.barcode FROM variant_barcodes b WHERE b.variant_id = v.id
                 ORDER BY b.is_primary DESC, b.barcode LIMIT 1), '')
FROM item_variants v
WHERE v.is_active = 1 AND v.item_id = ?
ORDER BY v.name`, itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VariantView
	for rows.Next() {
		var v VariantView
		if err := rows.Scan(&v.ID, &v.Name, &v.PriceMinor, &v.Barcode); err != nil {
			return nil, err
		}
		out = append(out, v)
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

// ItemNumberOnly is a minimal item projection — id, SKU, name — for the
// ut-docs#1356 "backfill barcodes from SKU" bulk action's preview list. Not
// reused from ItemLabel (name/price/code) or catalogtypes.ItemInput (the
// full item row): both carry fields this candidate list never needs and
// ItemLabel's Code is specifically "primary barcode falling back to SKU",
// the opposite of what this query selects FOR (items with NO barcode).
type ItemNumberOnly struct {
	ID   string
	SKU  string
	Name string
}

// ItemsWithoutBarcode returns active items that carry a non-empty SKU/item
// number but no row in item_barcodes at all — the candidate set for
// ut-docs#1356's bulk "backfill barcodes from SKU" action, ordered by name
// for a stable, operator-readable preview. Variant-only barcode gaps are
// out of scope (the feature backfills item numbers, not variant SKUs).
func (r *CatalogRepo) ItemsWithoutBarcode(ctx context.Context) ([]ItemNumberOnly, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT i.id, i.sku, i.name
FROM items i
WHERE i.is_active = 1
  AND COALESCE(i.sku, '') != ''
  AND NOT EXISTS (SELECT 1 FROM item_barcodes b WHERE b.item_id = i.id)
ORDER BY i.name
`)
	if err != nil {
		return nil, fmt.Errorf("items without barcode: %w", err)
	}
	defer rows.Close()
	var out []ItemNumberOnly
	for rows.Next() {
		var it ItemNumberOnly
		if err := rows.Scan(&it.ID, &it.SKU, &it.Name); err != nil {
			return nil, fmt.Errorf("items without barcode: scan: %w", err)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// BarcodeOwner is a read-only lookup for whether barcode is already
// assigned to an item or variant — checking both item_barcodes and
// variant_barcodes, same two tables ensureBarcodeAvailable checks inside
// AddBarcode's transaction. Deliberately NOT that function: this is for the
// ut-docs#1356 backfill preview's dry run, which must never hold AddBarcode's
// write lock (or run inside any transaction at all) just to show an
// operator what WOULD happen — ensureBarcodeAvailable's locking semantics
// stay exactly as they are, load-bearing for the real write path (see its
// own doc comment). Being non-transactional, the result is a snapshot, not
// a guarantee: the commit endpoint re-derives fresh against current data
// rather than trusting a preview built from this.
func (r *CatalogRepo) BarcodeOwner(ctx context.Context, barcode string) (targetType, targetID string, found bool, err error) {
	var itemID string
	scanErr := r.db.QueryRowContext(ctx, `SELECT item_id FROM item_barcodes WHERE barcode = ?`, barcode).Scan(&itemID)
	if scanErr == nil {
		return "item", itemID, true, nil
	}
	if !errors.Is(scanErr, sql.ErrNoRows) {
		return "", "", false, fmt.Errorf("barcode owner: %w", scanErr)
	}
	var variantID string
	scanErr = r.db.QueryRowContext(ctx, `SELECT variant_id FROM variant_barcodes WHERE barcode = ?`, barcode).Scan(&variantID)
	if scanErr == nil {
		return "variant", variantID, true, nil
	}
	if !errors.Is(scanErr, sql.ErrNoRows) {
		return "", "", false, fmt.Errorf("barcode owner: %w", scanErr)
	}
	return "", "", false, nil
}

// DeleteBarcode detaches a barcode wherever it is attached (item or variant).
// Fixing a mis-scanned or reassigned code is a normal back-office task.
//
// Same exact-first-then-canonical strategy as BarcodeExists (ut-docs#948
// F6), and for the same reason: deleting by the code exactly as given must
// not be shadowed by a canonicalisation that would target a different
// (embedded-data) key an explicit-type escape-hatch row was never stored
// under.
//
// Deliberately asymmetric with the scan path (ADR-0059 §6, ut-docs#958),
// not an inconsistency to fix: ResolveScanLine resolves the rare collision
// case canonical-first, because a genuine embedded-data decode (weight/
// price, hence money) must never be shadowed by an incidental raw-code
// match. Delete/exists stay exact-first here because acting on the code a
// caller actually named must never redirect to a DIFFERENT, unrelated
// item's row — a data-loss risk unifying the two orderings would trade the
// rare collision bug for. See ADR-0059 §6 and
// TestScanDeleteExists_CollisionResolutionIsDeliberatelyAsymmetric
// (internal/data/pos_repo_scanline_test.go) for the full analysis and the
// test pinning both properties together.
func (r *CatalogRepo) DeleteBarcode(ctx context.Context, barcode string) error {
	deleted, err := r.deleteBarcodeExact(ctx, barcode)
	if err != nil || deleted {
		return err
	}
	if canonical := r.canonicalBarcodeKey(ctx, barcode); canonical != barcode {
		_, err := r.deleteBarcodeExact(ctx, canonical)
		return err
	}
	return nil
}

// deleteBarcodeExact deletes barcode exactly as given and reports whether
// any row was actually removed, so DeleteBarcode knows whether to fall back
// to the canonical key.
func (r *CatalogRepo) deleteBarcodeExact(ctx context.Context, barcode string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM item_barcodes WHERE barcode = ?`, barcode)
	if err != nil {
		return false, err
	}
	itemRows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	res, err = r.db.ExecContext(ctx, `DELETE FROM variant_barcodes WHERE barcode = ?`, barcode)
	if err != nil {
		return false, err
	}
	variantRows, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return itemRows+variantRows > 0, nil
}

// FindOrCreateTaxCode returns the id of a tax_codes row matching the given
// (rate, takeaway rate) pair, creating one if none exists (ut-docs#512:
// catalog import groups items onto tax codes by pair, never one per item).
// takeawayBP is nil when the source carries no distinct takeaway rate — a
// pinned rate, no tax.rate.ask override needed for this code. Idempotent by
// (rate, takeaway) pair: re-running an import must not create duplicate tax
// codes, the same idempotency principle as the rest of catalog import.
func (r *CatalogRepo) FindOrCreateTaxCode(ctx context.Context, rateBP int, takeawayBP *int) (string, bool, error) {
	// SQLite: `= NULL` never matches, `IS ?` with a nullable bound
	// parameter compares NULL-safely, so one query covers both shapes.
	find := func() (string, error) {
		var id string
		err := r.db.QueryRowContext(ctx, `
SELECT id FROM tax_codes
WHERE rate_basis_points = ? AND takeaway_rate_basis_points IS ?
ORDER BY id LIMIT 1`, rateBP, nullableIntPtr(takeawayBP)).Scan(&id)
		return id, err
	}
	id, err := find()
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false, fmt.Errorf("find tax code: %w", err)
	}
	// Named deterministically and human-readably, so a manager looking at
	// the tax-code list later understands where the row came from. Two
	// different pairs can never generate the same name, but the UNIQUE
	// name constraint can still trip on a pre-existing manual code or a
	// concurrent import creating the identical pair — retry the lookup
	// before giving up, rather than failing a row that has a valid answer.
	name := fmt.Sprintf("Imported %s%%", taxrate.FormatPercent(rateBP))
	if takeawayBP != nil {
		name = fmt.Sprintf("Imported %s%% (takeaway %s%%)", taxrate.FormatPercent(rateBP), taxrate.FormatPercent(*takeawayBP))
	}
	id = uuid.NewString()
	_, insErr := r.db.ExecContext(ctx, `
INSERT INTO tax_codes (id, name, rate_basis_points, takeaway_rate_basis_points, is_active)
VALUES (?, ?, ?, ?, 1)`, id, name, rateBP, nullableIntPtr(takeawayBP))
	if insErr == nil {
		return id, true, nil
	}
	if id, err := find(); err == nil {
		return id, false, nil
	}
	return "", false, fmt.Errorf("create tax code: %w", insErr)
}

// TaxCodeView is one active tax code as listed for a plugin settings editor
// (ut-docs#190's takeaway-rate-overrides UI): the dine-in rate to show
// alongside an override input, and any pinned takeaway rate to suggest as a
// placeholder. JSON tags (ut-docs#655) follow this payload's existing
// basis-point naming -- takeaway_rate_bp matches data.ExportRow's field of
// the same name; rate_bp (the dine-in rate) matches the sibling
// data.ExportSaleTaxLine.RateBP already in the same export payload -- now
// that ListAllTaxCodes' rows are ALSO marshaled as the wire payload for a
// dispatched export-type plugin (internal/pages/data_api.go). This type was
// never JSON-marshaled before #655, so adding tags changes no existing wire
// format.
type TaxCodeView struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	RateBP         int64  `json:"rate_bp"`
	TakeawayRateBP *int64 `json:"takeaway_rate_bp"`
	// IsActive is set by GetTaxCode/ListAllTaxCodes (ut-docs#259's tax-code
	// management UI, which needs to show and reactivate retired codes).
	// ListTaxCodes/FindOrCreateTaxCode leave it at its zero value (false) --
	// neither of their callers reads it, since ListTaxCodes only ever
	// returns already-active rows.
	IsActive bool `json:"is_active"`
}

// ListTaxCodes returns every active tax code, highest dine-in rate first
// (then name) — the set a shop owner picks from when entering a takeaway
// override per tax code (ut-docs#190). Inactive/retired codes are excluded;
// they're not a valid override target going forward.
func (r *CatalogRepo) ListTaxCodes(ctx context.Context) ([]TaxCodeView, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, rate_basis_points, takeaway_rate_basis_points
FROM tax_codes
WHERE is_active = 1
ORDER BY rate_basis_points DESC, name`)
	if err != nil {
		return nil, fmt.Errorf("list tax codes: %w", err)
	}
	defer rows.Close()
	var out []TaxCodeView
	for rows.Next() {
		var v TaxCodeView
		var takeaway sql.NullInt64
		if err := rows.Scan(&v.ID, &v.Name, &v.RateBP, &takeaway); err != nil {
			return nil, fmt.Errorf("scan tax code: %w", err)
		}
		if takeaway.Valid {
			tv := takeaway.Int64
			v.TakeawayRateBP = &tv
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tax codes: %w", err)
	}
	return out, nil
}

// CreateTaxCode adds a new, active tax code by hand (ut-docs#259's tax-code
// management UI) -- distinct from FindOrCreateTaxCode's import-driven
// (rate, takeaway) pair matching above, this always inserts. tax_codes.name
// is UNIQUE; a conflict is reported as ErrTaxCodeNameExists rather than a
// raw 500 so the handler can surface a validation message.
func (r *CatalogRepo) CreateTaxCode(ctx context.Context, name string, rateBP int, takeawayBP *int) (string, error) {
	id := uuid.NewString()
	_, err := r.db.ExecContext(ctx, `
INSERT INTO tax_codes (id, name, rate_basis_points, takeaway_rate_basis_points, is_active)
VALUES (?, ?, ?, ?, 1)`, id, name, rateBP, nullableIntPtr(takeawayBP))
	if err != nil {
		if isUniqueViolation(err) {
			return "", ErrTaxCodeNameExists
		}
		return "", fmt.Errorf("create tax code: %w", err)
	}
	return id, nil
}

// UpdateTaxCode edits an existing tax code's name/rate/takeaway rate/active
// flag in one write (ut-docs#259) -- the SAME endpoint the manage-UI's
// activate/deactivate toggle uses (just isActive flipped, other fields
// resubmitted unchanged); there is no separate delete method since
// tax_codes.id is FK-referenced by items.tax_code_id (001_init.sql).
func (r *CatalogRepo) UpdateTaxCode(ctx context.Context, id, name string, rateBP int, takeawayBP *int, isActive bool) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE tax_codes SET name = ?, rate_basis_points = ?, takeaway_rate_basis_points = ?, is_active = ?
WHERE id = ?`, name, rateBP, nullableIntPtr(takeawayBP), boolToInt(isActive), id)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrTaxCodeNameExists
		}
		return fmt.Errorf("update tax code: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTaxCodeNotFound
	}
	return nil
}

// GetTaxCode looks up a single tax code by id, wrapping sql.ErrNoRows as
// ErrTaxCodeNotFound so the handler can respond 404 cleanly (ut-docs#259).
func (r *CatalogRepo) GetTaxCode(ctx context.Context, id string) (TaxCodeView, error) {
	var v TaxCodeView
	var takeaway sql.NullInt64
	var active int
	err := r.db.QueryRowContext(ctx, `
SELECT id, name, rate_basis_points, takeaway_rate_basis_points, is_active
FROM tax_codes WHERE id = ?`, id).Scan(&v.ID, &v.Name, &v.RateBP, &takeaway, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return TaxCodeView{}, ErrTaxCodeNotFound
	}
	if err != nil {
		return TaxCodeView{}, fmt.Errorf("get tax code: %w", err)
	}
	if takeaway.Valid {
		tv := takeaway.Int64
		v.TakeawayRateBP = &tv
	}
	v.IsActive = active == 1
	return v, nil
}

// ListAllTaxCodes returns every tax code, active and inactive alike --
// same ordering as ListTaxCodes, just without the WHERE is_active = 1
// filter, so the tax-code management UI (ut-docs#259) can show and
// reactivate a retired code. ListTaxCodes itself is UNCHANGED: it must keep
// excluding inactive codes, exactly as today, because the catalog page's
// tax-code select (web/ui/pages/catalog.html) depends on that.
func (r *CatalogRepo) ListAllTaxCodes(ctx context.Context) ([]TaxCodeView, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, rate_basis_points, takeaway_rate_basis_points, is_active
FROM tax_codes
ORDER BY rate_basis_points DESC, name`)
	if err != nil {
		return nil, fmt.Errorf("list all tax codes: %w", err)
	}
	defer rows.Close()
	// Non-nil (ut-docs#655 review, mirroring #600 review finding F4 on
	// ExportRows): a nil slice marshals as JSON null, indistinguishable
	// from the export payload's "not declared"/"not granted" omission —
	// plugin-manifest.md promises []/empty (never null) for the
	// declared+granted-but-empty case.
	out := make([]TaxCodeView, 0)
	for rows.Next() {
		var v TaxCodeView
		var takeaway sql.NullInt64
		var active int
		if err := rows.Scan(&v.ID, &v.Name, &v.RateBP, &takeaway, &active); err != nil {
			return nil, fmt.Errorf("scan tax code: %w", err)
		}
		if takeaway.Valid {
			tv := takeaway.Int64
			v.TakeawayRateBP = &tv
		}
		v.IsActive = active == 1
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list all tax codes: %w", err)
	}
	return out, nil
}

// ExportRow is one catalog line for the CSV export (G22b — the
// anti-lock-in half of import: a shop can always take its data and leave).
// Columns are chosen so our own importer round-trips the file. JSON tags
// (ut-docs#600 review finding F1) match ADR-0051's interchange-format
// naming (price_minor etc.) now that this type is ALSO marshaled as the
// wire payload for a dispatched export-type plugin (internal/pages/
// data_api.go's exportRequestPayload.Items) — writeCatalogCSV
// (internal/pages/import_page.go) reads these fields directly by name, not
// via reflection/tags, so adding tags is safe for that existing consumer.
type ExportRow struct {
	Name        string `json:"name"`
	SKU         string `json:"sku"`
	Barcode     string `json:"barcode"` // primary barcode preferred, else any
	PriceMinor  int64  `json:"price_minor"`
	Category    string `json:"category"`
	Description string `json:"description"`
	IsWeighed   bool   `json:"is_weighed"`
	// Stock (ut-docs#600 review finding F5, known limitation): item-scoped
	// only (SUM(inventory.quantity) WHERE item_id = i.id) — a variant-
	// tracked item's stock lives on variant-scoped inventory rows
	// (item_id NULL, ADR-0043) and is NOT included here, unlike the same
	// payload's separate stock[] ledger (data.ExportStockRow), which IS
	// variant-aware since ut-docs#240. A plugin summing items[].Stock will
	// under-count a variant-tracked shop's true stock; stock[] is the
	// source of truth for stock levels, this field is catalog-export
	// convenience only.
	Stock    float64 `json:"stock"`
	IsActive bool    `json:"is_active"`
	// Tax pairing (ut-docs#512): HasTax when the item carries a tax code,
	// HasTakeaway additionally when that code has a distinct takeaway rate —
	// so export → import round-trips the (dine-in, takeaway) grouping.
	TaxRateBP      int  `json:"tax_rate_bp"`
	HasTax         bool `json:"has_tax"`
	TakeawayRateBP int  `json:"takeaway_rate_bp"`
	HasTakeaway    bool `json:"has_takeaway"`
}

// ExportRows reads the whole catalog for export, active items first.
// Returns a non-nil (possibly empty) slice — ut-docs#600 review finding F4:
// an empty catalog must marshal as `[]`, not `null`, so a dispatched
// export-type plugin can tell "declared+granted but genuinely nothing to
// export" apart from "omitted" (the same []-vs-null contract
// ExportStockRow already documents in plugin-manifest.md).
func (r *CatalogRepo) ExportRows(ctx context.Context) ([]ExportRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT i.name, COALESCE(i.sku, ''),
       COALESCE((SELECT b.barcode FROM item_barcodes b WHERE b.item_id = i.id
                 ORDER BY b.is_primary DESC, b.barcode LIMIT 1), ''),
       i.base_price, COALESCE(c.name, ''), COALESCE(i.description, ''),
       i.is_weighed,
       COALESCE((SELECT SUM(v.quantity) FROM inventory v WHERE v.item_id = i.id), 0),
       i.is_active,
       t.rate_basis_points, t.takeaway_rate_basis_points
FROM items i LEFT JOIN categories c ON c.id = i.category_id
             LEFT JOIN tax_codes t ON t.id = i.tax_code_id
ORDER BY i.is_active DESC, i.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]ExportRow, 0)
	for rows.Next() {
		var e ExportRow
		var rate, takeaway sql.NullInt64
		if err := rows.Scan(&e.Name, &e.SKU, &e.Barcode, &e.PriceMinor, &e.Category,
			&e.Description, &e.IsWeighed, &e.Stock, &e.IsActive, &rate, &takeaway); err != nil {
			return nil, err
		}
		if rate.Valid {
			e.TaxRateBP, e.HasTax = int(rate.Int64), true
			if takeaway.Valid {
				e.TakeawayRateBP, e.HasTakeaway = int(takeaway.Int64), true
			}
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

// ErrLookupNotFound reports that GetLookup's id doesn't exist in the given
// lookup table (ut-docs#1430).
var ErrLookupNotFound = errors.New("lookup not found")

// GetLookup looks up a single row (category/brand/etc.) by id, wrapping
// sql.ErrNoRows as ErrLookupNotFound. Single-row equivalent of ReadLookup,
// for call sites (a per-mutation row re-render) that need just one name and
// would otherwise pay a whole-table read for it -- same rationale as
// GetTaxCode/ErrTaxCodeNotFound alongside ListAllTaxCodes (ut-docs#1430,
// mirroring ut-docs#1363's row_oob.go single-row taxCodeName resolution).
func (r *CatalogRepo) GetLookup(ctx context.Context, table string, id string) (Lookup, error) {
	var l Lookup
	err := r.db.QueryRowContext(ctx, `SELECT id, name FROM `+table+` WHERE id = ?`, id).Scan(&l.ID, &l.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return Lookup{}, ErrLookupNotFound
	}
	if err != nil {
		return Lookup{}, err
	}
	return l, nil
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
	// ut-docs#1176: an item with no source SKU used to get its own internal
	// UUID copied into the sku column here, which then leaked verbatim into
	// every staff-facing surface that displays SKU (inventory grid, item
	// search, receipts) — meaningless and confusing, and it defeated
	// SKU-based search since nobody types a UUID. sku is a nullable UNIQUE
	// column (001_init.sql); storing NULL for "no real SKU" is enough to
	// satisfy uniqueness (SQLite treats NULLs as distinct from each other
	// under UNIQUE, unlike duplicate empty strings) without inventing a
	// display value. nullableString leaves in.SKU as "" for the caller.
	active := 1
	if !in.IsActive {
		active = 0
	}
	_, err := r.db.ExecContext(ctx, `
INSERT INTO items (id, sku, name, description, category_id, brand_id, unit, base_price, tax_code_id, is_active, is_weighed)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, in.ID, nullableString(in.SKU), in.Name, in.Description, nullable(in.CategoryID), nullable(in.BrandID), in.Unit, in.BasePrice, nullable(in.TaxCodeID), active, boolToInt(in.IsWeighed))
	if err != nil {
		if isUniqueViolation(err) {
			return "", ErrSKUExists
		}
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
	return ensureInventoryRowExec(ctx, r.db, itemID, variantID)
}

// ensureInventoryRowExec is ensureInventoryRow's actual logic, generalized to
// run against either the repo's *sql.DB (ensureInventoryRow's caller) or a
// caller-supplied *sql.Tx (CreateItemTx) — both satisfy database/sql's
// ExecContext/QueryRowContext, so a plain execer interface covers both
// without duplicating the statements.
func ensureInventoryRowExec(ctx context.Context, ex execer, itemID, variantID string) error {
	var locationID string
	err := ex.QueryRowContext(ctx, `SELECT id FROM stock_locations WHERE name = 'Main' OR id = 'loc_main' ORDER BY id LIMIT 1`).Scan(&locationID)
	if errors.Is(err, sql.ErrNoRows) {
		locationID = "loc_main"
		if _, err := ex.ExecContext(ctx, `INSERT INTO stock_locations (id, name) VALUES (?, ?)`, locationID, "Main"); err != nil {
			return fmt.Errorf("create default location: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("find default location: %w", err)
	}
	_, err = ex.ExecContext(ctx, `
INSERT OR IGNORE INTO inventory (id, item_id, variant_id, location_id, quantity)
VALUES (?, ?, ?, ?, 0)
`, uuid.NewString(), nullIfEmpty(itemID), nullIfEmpty(variantID), locationID)
	if err != nil {
		return fmt.Errorf("insert inventory row: %w", err)
	}
	return nil
}

// execer is satisfied by both *sql.DB and *sql.Tx — lets a single statement
// helper run against whichever handle the caller already holds, without a
// separate copy of the query text per handle type (guard-data-access.sh
// still sees exactly one place these statements live).
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// CreateItemTx is CreateItem's item insert, run inside a caller-supplied
// transaction instead of autocommitting on its own — additive alongside
// CreateItem (ut-docs#310), which keeps its existing autocommit behavior
// for its other callers unchanged. The inventory-row step stays exactly as
// best-effort as CreateItem's own (logged, never fails item creation): a
// stockless deployment — no stock_locations table at all — must still be
// able to import a catalog, same as it can today (see
// TestCreateItem_SucceedsWithoutStockLocationsTable and
// TestImport_LocationLookupFailureWarnsStockNotCarried, both of which rely
// on that). What the transaction actually buys is the item + inventory row
// + a subsequent same-tx stock movement landing or rolling back together —
// see the import commit loop in internal/pages/import_page.go.
func (r *CatalogRepo) CreateItemTx(ctx context.Context, tx *sql.Tx, in catalogtypes.ItemInput) (string, error) {
	if in.Name == "" {
		return "", errors.New("name required")
	}
	if in.Unit == "" {
		in.Unit = "each"
	}
	if in.ID == "" {
		in.ID = uuid.NewString()
	}
	// ut-docs#1176: see CreateItem's identical comment — don't fall back to
	// the item's own UUID as a display SKU, store NULL instead.
	active := 1
	if !in.IsActive {
		active = 0
	}
	_, err := tx.ExecContext(ctx, `
INSERT INTO items (id, sku, name, description, category_id, brand_id, unit, base_price, tax_code_id, is_active, is_weighed)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, in.ID, nullableString(in.SKU), in.Name, in.Description, nullable(in.CategoryID), nullable(in.BrandID), in.Unit, in.BasePrice, nullable(in.TaxCodeID), active, boolToInt(in.IsWeighed))
	if err != nil {
		// ut-docs#1510: unlike CreateItem, this branch never translated a
		// UNIQUE(sku) violation into the distinguishable ErrSKUExists — a
		// caller committing many rows in a loop (the catalog importer) could
		// only tell "some DB error" from "that SKU is already in use," so a
		// genuine race between two concurrent imports of the same SKU surfaced
		// as a raw failed-row instead of the same clean "already in catalog"
		// skip a sequential re-import gets. Mirrors CreateItem's own check.
		if isUniqueViolation(err) {
			return "", ErrSKUExists
		}
		return "", fmt.Errorf("insert item: %w", err)
	}
	// Same reasoning as CreateItem: the item is already valid and sellable
	// without a stock row (stock tracking is opt-in), so this must never
	// fail — or roll back — item creation, only get logged.
	if err := ensureInventoryRowExec(ctx, tx, in.ID, ""); err != nil {
		logging.L().Warnf("catalog: create item %s: inventory row not created: %v", in.ID, err)
	}
	return in.ID, nil
}

// EnsureDefaultThumbnail gives an item a thumbnail (item_images, role=
// thumbnail) IF it has none — never overwrites an existing row, whatever
// put it there. Used by the catalog importer (ut-docs#1189 Phase 1) to
// fall back a freshly imported, imageless item to a bundled generic
// category icon rather than leaving it a blank tile; path is the caller's
// already-resolved "/public/assets/..." value (see
// catimport.PlaceholderIconPath) — this method does no icon selection of
// its own, only the existence-gated insert, same division of labor as the
// rest of this package (catimport stays a pure, DB-free parser).
//
// See SetItemThumbnail below for the unconditional-overwrite counterpart
// a real photo upload needs — an item that only ever got here via THIS
// method (no operator upload has happened yet) has nothing worth
// protecting, which is exactly when SetItemThumbnail's overwrite is safe.
//
// Deliberately run OUTSIDE any item-creation transaction, the same
// after-commit placement CreateItemTx's own doc comment describes for
// barcode attach: it's a separate, best-effort concern the caller should
// log-and-continue on failure, never roll the item itself back for.
func (r *CatalogRepo) EnsureDefaultThumbnail(ctx context.Context, itemID, path string) error {
	if itemID == "" || path == "" {
		return errors.New("itemID and path required")
	}
	var exists int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM item_images WHERE item_id = ? AND role = 'thumbnail' LIMIT 1`, itemID,
	).Scan(&exists)
	if err == nil {
		return nil // already has a thumbnail — never overwrite it
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("check existing thumbnail: %w", err)
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO item_images (id, item_id, path, role) VALUES (?, ?, ?, 'thumbnail')`,
		uuid.NewString(), itemID, path,
	); err != nil {
		return fmt.Errorf("insert placeholder thumbnail: %w", err)
	}
	return nil
}

// SetItemThumbnail unconditionally sets an item's thumbnail (item_images,
// role=thumbnail) to path — updating an existing row if one exists,
// inserting one if not. Unlike EnsureDefaultThumbnail above, this DOES
// overwrite: it's what a real photo (manual upload, or a barcode-lookup
// match) is supposed to do — replace whatever placeholder or older photo
// was there. Review finding F2/F7 (ut-docs#1189): before this method
// existed, the manual upload handler
// (internal/pages/catalog/handlers.go's POST /api/catalog/item/image and
// saveLookupImage) wrote ONLY the thumb.png file, never touching
// item_images at all — so the admin Catalog table (which resolves a
// thumbnail by checking the file on disk) showed the new photo, but the
// POS sale-screen grid, basket, self-order kiosk and search suggestions
// (which all resolve via item_images/ImageURL, see internal/data's own
// SELECT ... item_images JOINs) kept showing whatever was there before —
// an imported item's placeholder icon, forever, with no in-app way to
// clear it. See internal/data/shortcuts_repo.go's own doc comment for the
// same gap independently observed from the shortcuts-button angle.
func (r *CatalogRepo) SetItemThumbnail(ctx context.Context, itemID, path string) error {
	if itemID == "" || path == "" {
		return errors.New("itemID and path required")
	}
	res, err := r.db.ExecContext(ctx,
		`UPDATE item_images SET path = ? WHERE item_id = ? AND role = 'thumbnail'`,
		path, itemID,
	)
	if err != nil {
		return fmt.Errorf("update thumbnail: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("update thumbnail: %w", err)
	} else if n > 0 {
		return nil
	}
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO item_images (id, item_id, path, role) VALUES (?, ?, ?, 'thumbnail')`,
		uuid.NewString(), itemID, path,
	); err != nil {
		return fmt.Errorf("insert thumbnail: %w", err)
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
	return updateItemExec(ctx, r.db, in)
}

// UpdateItemReturningWasActive wraps the read of the item's previous
// is_active state and the update itself in a single BEGIN IMMEDIATE
// transaction (ut-docs#1399, follow-up to ut-docs#1365). The catalog-update
// handler's OOB-mode decision — re-render the row in place, or APPEND a new
// row, because an inactive item has none rendered yet — is derived from
// that previous state. With the read outside the write (the original shape:
// GetItem then UpdateItem as two separate calls), two genuinely concurrent
// updates on the SAME item can both read the pre-update is_active before
// either write lands, so both decide APPEND and both emit a row — the
// server-side half of #1365's duplicate-row bug that a client-side
// double-click fix can't reach.
//
// BEGIN IMMEDIATE (the DSN's _txlock=immediate, ut-docs#311 — same idiom as
// AddBarcode's check-then-insert fix, ut-docs#304) takes the write lock at
// BEGIN, before the read runs, so a second concurrent call blocks until the
// first commits and then reads the ALREADY-updated is_active — it correctly
// sees the row as active and chooses in-place update, not another append.
func (r *CatalogRepo) UpdateItemReturningWasActive(ctx context.Context, in catalogtypes.ItemInput) (bool, error) {
	// Reject an input that is invalid on its face BEFORE opening the
	// transaction (review, ut-docs#1399). BEGIN IMMEDIATE takes the
	// database-wide write lock at BEGIN and waits up to busy_timeout(5000)
	// for it, so validating only inside updateItemExec would make a
	// malformed request queue behind a live sale's writer for up to five
	// seconds just to be told "id required" — and hold that lock itself
	// once acquired. UpdateItem's own validation is unchanged; this is the
	// same check run earlier, so the error text callers match on is too.
	if err := validateItemUpdate(in); err != nil {
		return true, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return true, fmt.Errorf("update item: begin: %w", err)
	}
	// No-op after a successful Commit (sql.ErrTxDone).
	defer func() { _ = tx.Rollback() }()
	// Same conservative default as the pre-fix handler: a read error (as
	// opposed to a clean "not found") is swallowed here, not propagated —
	// only the write's own error is a reason to fail the request.
	wasActive := true
	if prev, ok, err := getItemExec(ctx, tx, in.ID); err == nil && ok {
		wasActive = prev.IsActive
	}
	if err := updateItemExec(ctx, tx, in); err != nil {
		return true, err
	}
	if err := tx.Commit(); err != nil {
		return true, fmt.Errorf("update item: commit: %w", err)
	}
	return wasActive, nil
}

// validateItemUpdate is the item-update input validation shared by
// updateItemExec and UpdateItemReturningWasActive — the latter runs it
// before BEGIN so a malformed input never takes the write lock. Keeping one
// copy keeps the two paths' error text identical.
func validateItemUpdate(in catalogtypes.ItemInput) error {
	if in.ID == "" {
		return errors.New("id required")
	}
	if in.Name == "" {
		return errors.New("name required")
	}
	return nil
}

// updateItemExec is UpdateItem's actual logic, generalized over the execer
// interface (see ensureInventoryRowExec) so UpdateItemReturningWasActive can
// run it against a caller-held *sql.Tx instead of the repo's *sql.DB.
func updateItemExec(ctx context.Context, ex execer, in catalogtypes.ItemInput) error {
	if err := validateItemUpdate(in); err != nil {
		return err
	}
	if in.Unit == "" {
		in.Unit = "each"
	}
	active := 1
	if !in.IsActive {
		active = 0
	}
	_, err := ex.ExecContext(ctx, `
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
		if isUniqueViolation(err) {
			return ErrSKUExists
		}
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
		if isUniqueViolation(err) {
			return "", ErrSKUExists
		}
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
		if isUniqueViolation(err) {
			return ErrSKUExists
		}
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
		// Untyped inference (ADR-0059 Decision §3): match against the shop's
		// enabled symbologies via matchBarcode — the same helper the scan
		// path and canonicalBarcodeKey use, so AddBarcode, scanning and the
		// BarcodeExists/DeleteBarcode pre-checks can never disagree on what
		// a code means. Under the DEFAULT enabled set, every code that
		// resolved before this card still resolves — the recorded
		// barcode_type is just more specific now for shapes that used to
		// fall through to CODE128 (e.g. an 8-digit code is now EAN8, a
		// 14-digit GTIN is now GTIN14). barcode_type is write-only (never
		// read to drive scan behaviour, ADR-0059 §3), so this drift has no
		// resolution-behaviour impact — LookupKey still equals the typed
		// code for every one of these plain shapes.
		//
		// ut-docs#934 review finding F1: once a shop enables
		// EAN13_WEIGHT_PREFIX2X (prefix 20-29) or EAN13_PRICE_PREFIX02
		// (prefix 02), this untyped path classifies ANY check-digit-valid
		// EAN-13 in that prefix range as embedded-data first (specificity
		// order, ADR-0059 §3) — even a genuine plain retail product whose
		// EAN-13 happens to start with 20-29/02 — storing the zeroed
		// LookupKey instead of the code as typed. ut-docs#948 (the
		// forcePlainBarcode escape hatch on the catalog barcode-entry
		// forms, internal/pages/catalog/handlers.go) is that fast-follow —
		// an operator can now pass an explicit BarcodeType to take the
		// `else if EAN13` branch below instead of this one, before shops
		// can actually reach this state (SetEnabledBarcodeSymbologies
		// still has no non-production caller as of #948 — see #935).
		dec, enabledIDs, ok := r.matchBarcode(ctx, in.Barcode)
		if !ok {
			// Named rejection (ADR-0059 §3): say what was scanned and what is
			// enabled; write nothing. Only reachable once the shop disabled
			// the default catch-alls.
			return fmt.Errorf("%w: %q (enabled: %s)",
				ErrBarcodeNoSymbologyMatch, in.Barcode, strings.Join(enabledIDs, ", "))
		}
		in.BarcodeType = strings.ToUpper(dec.SymbologyID)
		// Store the decoded LookupKey (same value canonicalBarcodeKey would
		// compute for this code), not the raw scan. For a plain symbology
		// LookupKey == the typed code, so this is a no-op; for the two
		// embedded-data symbologies it is the zeroed template (prefix + item
		// code, weight/price and check digits zeroed) — storing the raw
		// label would pin the row to ONE specific label's weight/price and no
		// other label of the same item could ever resolve (ADR-0059 §3).
		in.Barcode = dec.LookupKey
	} else if strings.EqualFold(in.BarcodeType, "EAN13") {
		in.BarcodeType = "EAN13"
		if !validEAN13(in.Barcode) {
			return fmt.Errorf("%w %q: expected 13 digits with a valid check digit", ErrInvalidEAN13, in.Barcode)
		}
	}
	// The availability check and the insert must be atomic (ut-docs#304):
	// without a transaction two concurrent callers can both pass the check,
	// and the second ON CONFLICT DO UPDATE silently reassigns the barcode to
	// a different item/variant — it then scans to the wrong product at the
	// till. The DSN's _txlock=immediate (ut-docs#311) makes this BeginTx run
	// BEGIN IMMEDIATE, taking the write lock at BEGIN — before the SELECT
	// checks — which is what closes the race (a deferred BEGIN would only
	// take the write lock at the first write statement, after both checks
	// have run). That DSN guarantee is why this can be the repo's standard
	// BeginTx idiom rather than the dedicated-connection raw BEGIN
	// IMMEDIATE dance this method used to hand-roll: sql.Tx's own
	// Rollback/Commit bookkeeping also handles a cancelled request ctx
	// without leaking a mid-transaction pooled connection, which the raw
	// statement approach had to guard against by hand.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("add barcode: begin: %w", err)
	}
	// No-op after a successful Commit (sql.ErrTxDone).
	defer func() { _ = tx.Rollback() }()
	if err := addBarcodeInTx(ctx, tx, in); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("add barcode: commit: %w", err)
	}
	return nil
}

// addBarcodeInTx runs AddBarcode's check-then-insert inside the caller's
// transaction (IMMEDIATE by DSN, so the write lock is already held before
// the checks run — see AddBarcode). The WHERE guard on each DO UPDATE is
// defence-in-depth on top of that lock: even called outside this locking
// path, the upsert can only ever refresh a barcode that already belongs to
// the SAME target — never steal it from another owner.
func addBarcodeInTx(ctx context.Context, tx *sql.Tx, in catalogtypes.BarcodeInput) error {
	if in.VariantID != "" {
		if err := ensureBarcodeAvailable(ctx, tx, in.Barcode, "variant", in.VariantID); err != nil {
			return err
		}
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT is_active FROM item_variants WHERE id = ?`, in.VariantID).Scan(&active); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("variant not found: %s", in.VariantID)
			}
			return err
		}
		if active == 0 {
			return fmt.Errorf("variant %s inactive", in.VariantID)
		}
		res, err := tx.ExecContext(ctx, `
INSERT INTO variant_barcodes (barcode, variant_id, barcode_type, is_primary)
VALUES (?, ?, ?, ?)
ON CONFLICT(barcode) DO UPDATE SET barcode_type=excluded.barcode_type, is_primary=excluded.is_primary
WHERE variant_barcodes.variant_id = excluded.variant_id
`, in.Barcode, in.VariantID, in.BarcodeType, boolToInt(in.IsPrimary))
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n == 0 {
			return fmt.Errorf("barcode %q already assigned to a different variant", in.Barcode)
		}
		return nil
	}
	// item barcode
	if err := ensureBarcodeAvailable(ctx, tx, in.Barcode, "item", in.ItemID); err != nil {
		return err
	}
	var active int
	if err := tx.QueryRowContext(ctx, `SELECT is_active FROM items WHERE id = ?`, in.ItemID).Scan(&active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("item not found: %s", in.ItemID)
		}
		return err
	}
	if active == 0 {
		return fmt.Errorf("item %s inactive", in.ItemID)
	}
	res, err := tx.ExecContext(ctx, `
INSERT INTO item_barcodes (barcode, item_id, barcode_type, is_primary)
VALUES (?, ?, ?, ?)
ON CONFLICT(barcode) DO UPDATE SET barcode_type=excluded.barcode_type, is_primary=excluded.is_primary
WHERE item_barcodes.item_id = excluded.item_id
`, in.Barcode, in.ItemID, in.BarcodeType, boolToInt(in.IsPrimary))
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return fmt.Errorf("barcode %q already assigned to a different item", in.Barcode)
	}
	return nil
}

// validEAN13 delegates to the shared internal/barcode checksum (ADR-0059
// Decision §1 — "reuse/extract ... rather than duplicating it") instead of
// keeping its own copy of the GS1 check-digit algorithm.
func validEAN13(code string) bool {
	return barcode.ValidEAN13Checksum(code)
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

func nullableIntPtr(v *int) any {
	if v == nil {
		return nil
	}
	return *v
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
// It runs inside AddBarcode's transaction — IMMEDIATE by DSN
// (_txlock=immediate, ut-docs#311), so the write lock is held before this
// check runs and the check and the subsequent insert are atomic.
func ensureBarcodeAvailable(ctx context.Context, tx *sql.Tx, barcode, targetType, targetID string) error {
	var existing string
	switch targetType {
	case "item":
		if err := tx.QueryRowContext(ctx, `SELECT item_id FROM item_barcodes WHERE barcode = ?`, barcode).Scan(&existing); err == nil {
			if existing != targetID {
				return &BarcodeConflictError{TargetType: "item", TargetID: existing}
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT variant_id FROM variant_barcodes WHERE barcode = ?`, barcode).Scan(&existing); err == nil {
			return &BarcodeConflictError{TargetType: "variant", TargetID: existing}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	case "variant":
		if err := tx.QueryRowContext(ctx, `SELECT variant_id FROM variant_barcodes WHERE barcode = ?`, barcode).Scan(&existing); err == nil {
			if existing != targetID {
				return &BarcodeConflictError{TargetType: "variant", TargetID: existing}
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT item_id FROM item_barcodes WHERE barcode = ?`, barcode).Scan(&existing); err == nil {
			return &BarcodeConflictError{TargetType: "item", TargetID: existing}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	default:
		return errors.New("invalid targetType for barcode")
	}
	return nil
}
