package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/barcode"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/logging"
)

// POSRepo centralizes DB access for POS handlers.
type POSRepo struct {
	db *sql.DB
	// settings reads the shop's enabled barcode symbologies (ADR-0059 §2)
	// for the scan-path lookup — same accessor CatalogRepo's AddBarcode
	// inference and the ut-docs#935 settings checklist share.
	settings *SettingsRepo
}

var posObs = newRepoObservability("pos")

func NewPOSRepo(db *sql.DB) *POSRepo {
	return &POSRepo{db: db, settings: NewSettingsRepo(db)}
}

// ShortcutSearchResult is used for shortcut item selection.
type ShortcutSearchResult struct {
	ItemID  string
	Name    string
	Barcode string
	SKU     string
	Image   string
}

// ShortcutLine represents a priced line derived from a barcode/lookup for shortcuts.
type ShortcutLine struct {
	SKU       string
	Name      string
	ItemID    string
	VariantID string
	Price     int64
	TaxRateBP int
	// TaxCodeID identifies which tax code this rate came from ("" if the
	// item has none) — a tax plugin (internal/pos.TaxRateAsker) uses it to
	// tell item categories apart; core itself never interprets it.
	TaxCodeID  string
	IsWeighed  bool
	ImageURL   string
	Label      string
	HasVariant bool
}

// SaleLineSnapshot represents stored sale_line fields used by returns.
type SaleLineSnapshot struct {
	ID         string
	ItemID     string
	VariantID  string
	Name       string
	SKU        string
	Barcode    string
	Qty        float64
	UnitPrice  int64
	TaxRateBP  int
	LocationID string
	// OrderType (ut-docs#1181, ADR-0073): the line's own consumption mode
	// ("" dine-in | "takeaway"), copied verbatim onto a refund/return line
	// so provenance survives even though the persisted TaxRateBP already
	// fixes the money.
	OrderType string
}

type SaleJournalEntry struct {
	ReceiptNo  string
	Total      int64
	TenderType string
	SyncStatus string
	CreatedAt  string
	// TillID/TillName are this sale's provenance (ADR-0011 D3): TillID is ''
	// for a sale made on this till (or pre-sync history), else the till id
	// it was journaled in from; TillName is that till's enrolled name (''
	// when TillID is '' or the till is no longer enrolled).
	TillID   string
	TillName string
}

// SalesJournalFilter narrows ListSalesJournal. AllTills=true ignores TillID
// and returns every till's sales; otherwise TillID selects one till's sales
// ("" = this till's own local sales, till_id=”). Day, if set, is
// "YYYY-MM-DD" and restricts to that calendar day (by created_at). Limit
// <= 0 defaults to 5, same convention as ListRecentSales.
type SalesJournalFilter struct {
	TillID   string
	AllTills bool
	Day      string
	Limit    int
}

type QueuedSale struct {
	ID                string
	ReceiptNo         string
	SyncAttempts      int
	SyncNextAttemptAt string
	SyncLastError     string
	Total             int64
	TenderType        string
}

type PluginVersionRow struct {
	ID      string
	Version string
}

// StockMovementInput captures data for a stock movement (receipt, adjustment, transfer, waste)
type StockMovementInput struct {
	ItemID     string
	VariantID  string
	LocationID string
	SaleLineID string
	Type       string  // receive|adjust|transfer|waste
	Quantity   float64 // positive for receipt/adjust-up, negative for adjust-down/waste
	CostPrice  int64   // optional, minor units
	Reason     string
	ActorID    string
}

// OverrideNegativeInventory captures a manager override allowing negative inventory with audit.
type OverrideNegativeInventory struct {
	ActorID string
	// RequestedBy is the originally-blocked user who asked for the
	// override, when different from ActorID (a cashier whose action a
	// manager's PIN then approved). Equal to ActorID when the actor
	// authorized their own override directly — the caller isn't required
	// to leave it empty in that case; the payload check below only emits
	// requested_by when it actually differs. ut-docs#780: without this, a
	// PIN-approved override's audit row reads as if the approving manager
	// performed the action directly, and the original actor's identity is
	// lost.
	RequestedBy string
	Reason      string
	ItemID      string
	VariantID   string
	LocationID  string
	QtyBefore   float64
}

// LowStockItem represents an item with low stock.
type LowStockItem struct {
	ItemID       string  `json:"item_id"`
	Name         string  `json:"name"`
	SKU          string  `json:"sku"`
	LocationID   string  `json:"location_id"`
	LocationName string  `json:"location_name"`
	CurrentQty   float64 `json:"current_qty"`
	ReorderLevel int     `json:"reorder_level"`
	LeadTimeDays int     `json:"lead_time_days"` // days to receive a reorder; 0 = unset
}

// defaultWarnDays is the running-out threshold for an item with no lead
// time configured.
const defaultWarnDays = 7

// EffectiveWarnDays is the days-of-stock-left threshold below which this
// item counts as running out: its own lead time once set, otherwise
// defaultWarnDays. The single source of truth for this threshold — the
// inventory page, the reports header chip, and the daily low-stock digest
// all call this rather than each keeping their own copy (universaltill/ut-docs#85
// found two of the three had drifted out of sync before this existed).
func (l LowStockItem) EffectiveWarnDays() int {
	if l.LeadTimeDays > 0 {
		return l.LeadTimeDays
	}
	return defaultWarnDays
}

// DaysLeftAt returns floor(CurrentQty / rate) — the shared "days of stock
// left" computation used by both /inventory's displayed number (see
// stockLevelsForDisplay in internal/pages/inventory_page.go) and
// IsRunningOut's boundary decision below (universaltill/ut-docs#440),
// extracted so a future change to the formula (e.g. math.Ceil, a safety
// margin) can't silently desync the displayed number from the warning
// flag the way two independently-maintained copies could.
//
// Guards the float64→int conversion, which Go leaves implementation-
// defined for a NaN or out-of-int-range result: a rate small enough that
// CurrentQty/rate overflows int range, or a NaN input, clamps to
// math.MaxInt ("effectively never running out at this rate") rather than
// converting directly — the same direction a raw-float comparison against
// a small warn-days threshold would also land on. Unreachable through
// today's three call sites (rate is always a positive, finite
// positive_qty/28 from ItemDailySellRates), but this method is exported
// from internal/data, so a future caller isn't guaranteed the same input.
func (l LowStockItem) DaysLeftAt(rate float64) int {
	days := l.CurrentQty / rate
	if math.IsNaN(days) || days > float64(math.MaxInt) {
		return math.MaxInt
	}
	return int(days)
}

// IsRunningOut is the single shared "is this item running out" decision
// given a sell rate (units/day), used identically by the /inventory page,
// the low-stock digest and the /reports header chip
// (universaltill/ut-docs#275 — before this method existed, /inventory
// floored the days-left prediction before comparing while the other two
// compared the raw float directly, so they could disagree at an exact
// boundary, e.g. qty/rate=7.5 against a 7-day window). Floor-then-compare
// is the standardized behavior: it matches /inventory (the primary
// surface) for the common case, and is the more conservative of the two —
// it never warns later than a raw-float compare would.
func (l LowStockItem) IsRunningOut(rate float64) bool {
	if rate <= 0 || math.IsNaN(rate) {
		return false
	}
	if l.CurrentQty <= 0 {
		return true
	}
	return l.DaysLeftAt(rate) <= l.EffectiveWarnDays()
}

// SearchActiveItems finds active items matching name/sku/barcode with optional pagination.
func (r *POSRepo) SearchActiveItems(ctx context.Context, q string, offset, limit int) ([]catalogtypes.ItemInput, error) {
	var err error
	done := posObs.trace("search_active_items")
	defer func() { done(err) }()
	like := "%" + strings.TrimSpace(q) + "%"
	if limit <= 0 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT i.id, COALESCE(i.sku, ''), i.name, i.description, i.category_id, i.brand_id, i.unit, i.base_price, i.tax_code_id, i.is_active, i.is_weighed
FROM items i
WHERE i.is_active = 1 AND (
      i.name LIKE ?
   OR i.sku LIKE ?
   OR EXISTS (
        SELECT 1 FROM item_barcodes ib WHERE ib.item_id = i.id AND ib.barcode LIKE ?
   )
)
ORDER BY i.name
LIMIT ? OFFSET ?
`, like, like, like, limit, offset)
	if err != nil {
		err = posObs.wrap("search_active_items", err)
		return nil, err
	}
	defer rows.Close()
	var out []catalogtypes.ItemInput
	for rows.Next() {
		var itm catalogtypes.ItemInput
		var desc, cat, brand, tax sql.NullString
		if err := rows.Scan(&itm.ID, &itm.SKU, &itm.Name, &desc, &cat, &brand, &itm.Unit, &itm.BasePrice, &tax, &itm.IsActive, &itm.IsWeighed); err != nil {
			err = posObs.wrap("search_active_items", err)
			return nil, err
		}
		if desc.Valid {
			itm.Description = desc.String
		}
		if cat.Valid {
			itm.CategoryID = &cat.String
		}
		if brand.Valid {
			itm.BrandID = &brand.String
		}
		if tax.Valid {
			itm.TaxCodeID = &tax.String
		}
		out = append(out, itm)
	}
	err = rows.Err()
	if err != nil {
		err = posObs.wrap("search_active_items", err)
	}
	return out, err
}

// LookupActiveVariant returns a variant if active; otherwise error.
func (r *POSRepo) LookupActiveVariant(ctx context.Context, variantID string) (*catalogtypes.VariantInput, error) {
	var err error
	done := posObs.trace("lookup_active_variant")
	defer func() { done(err) }()
	if strings.TrimSpace(variantID) == "" {
		return nil, errors.New("variantID required")
	}
	var v catalogtypes.VariantInput
	var cost sql.NullInt64
	// COALESCE(sku, '') — ut-docs#1205 (same landmine class as ut-docs#1176):
	// item_variants.sku is a nullable UNIQUE column, and CatalogRepo.CreateVariant
	// stores NULL for a variant created with no SKU. Scanning that straight
	// into VariantInput.SKU (a non-nullable string) fails with "converting
	// NULL to string is unsupported" the first time this hits such a variant.
	if err = r.db.QueryRowContext(ctx, `SELECT id, item_id, COALESCE(sku, ''), name, price, cost_price, is_active FROM item_variants WHERE id = ?`, variantID).
		Scan(&v.ID, &v.ItemID, &v.SKU, &v.Name, &v.Price, &cost, &v.IsActive); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("variant not found: %s", variantID)
		}
		err = posObs.wrap("lookup_active_variant", err)
		return nil, err
	}
	if cost.Valid {
		v.CostPrice = &cost.Int64
	}
	if !v.IsActive {
		return nil, fmt.Errorf("variant inactive: %s", variantID)
	}
	return &v, nil
}

// LookupUserRole returns a user's role if present.
func (r *POSRepo) LookupUserRole(ctx context.Context, userID string) (string, bool, error) {
	var role string
	err := r.db.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?`, userID).Scan(&role)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("lookup user role: %w", err)
	}
	return role, true, nil
}

// FindSaleIDByReceipt returns sale ID for a receipt number.
func (r *POSRepo) FindSaleIDByReceipt(ctx context.Context, receiptNo string) (string, bool, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM sales WHERE receipt_no = ?`, receiptNo).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("find sale by receipt: %w", err)
	}
	return id, true, nil
}

// ListSaleLineSnapshots returns sale line snapshots for a sale.
func (r *POSRepo) ListSaleLineSnapshots(ctx context.Context, saleID string) ([]SaleLineSnapshot, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, item_id, variant_id, name_snapshot, sku_snapshot, barcode_snapshot, quantity, unit_price, tax_rate_bp, COALESCE(order_type, '')
FROM sale_lines
WHERE sale_id = ?
ORDER BY line_no
`, saleID)
	if err != nil {
		return nil, fmt.Errorf("fetch lines: %w", err)
	}
	defer rows.Close()
	var res []SaleLineSnapshot
	for rows.Next() {
		var (
			id, itemID, variantID, name, sku, barcode sql.NullString
			qty                                       float64
			unitPrice                                 int64
			taxRateBP                                 int
			orderType                                 string
		)
		if err := rows.Scan(&id, &itemID, &variantID, &name, &sku, &barcode, &qty, &unitPrice, &taxRateBP, &orderType); err != nil {
			return nil, fmt.Errorf("scan line: %w", err)
		}
		res = append(res, SaleLineSnapshot{
			ID:         id.String,
			ItemID:     itemID.String,
			VariantID:  variantID.String,
			Name:       name.String,
			SKU:        sku.String,
			Barcode:    barcode.String,
			Qty:        qty,
			UnitPrice:  unitPrice,
			TaxRateBP:  taxRateBP,
			LocationID: "",
			OrderType:  orderType,
		})
	}
	return res, rows.Err()
}

// GetReceiptNo returns the receipt number for a sale.
func (r *POSRepo) GetReceiptNo(ctx context.Context, saleID string) (string, bool, error) {
	var receipt string
	err := r.db.QueryRowContext(ctx, `SELECT receipt_no FROM sales WHERE id = ?`, saleID).Scan(&receipt)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("get receipt: %w", err)
	}
	return receipt, true, nil
}

// GetShiftOpeningCash returns opening_cash for an open shift.
func (r *POSRepo) GetShiftOpeningCash(ctx context.Context, shiftID string) (int64, bool, error) {
	var opening int64
	err := r.db.QueryRowContext(ctx, `
SELECT opening_cash FROM shifts WHERE id = ? AND closed_at IS NULL
`, shiftID).Scan(&opening)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("query shift: %w", err)
	}
	return opening, true, nil
}

// AggregateInventory retrieves current inventory quantity from inventory table for a given item+location.
func (r *POSRepo) AggregateInventory(ctx context.Context, tx *sql.Tx, locationID, itemID, variantID string) (float64, error) {
	var err error
	done := posObs.trace("aggregate_inventory")
	defer func() { done(err) }()
	if locationID == "" {
		return 0, errors.New("locationID required")
	}
	if itemID == "" && variantID == "" {
		return 0, errors.New("itemID or variantID required")
	}
	if itemID != "" && variantID != "" {
		return 0, errors.New("cannot specify both itemID and variantID")
	}
	q := r.exec(tx)
	var qty float64
	err = q.QueryRowContext(ctx, `
SELECT COALESCE(quantity, 0)
FROM inventory
WHERE location_id = ?
  AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL))
`, locationID, nullIfEmpty(itemID), nullIfEmpty(variantID)).Scan(&qty)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		err = posObs.wrap("aggregate_inventory", fmt.Errorf("aggregate inventory: %w", err))
		return 0, err
	}
	return qty, nil
}

// CheckNegativeInventory validates that a proposed sale won't create negative inventory.
func (r *POSRepo) CheckNegativeInventory(ctx context.Context, tx *sql.Tx, locationID, itemID, variantID string, requestedQty float64) error {
	if requestedQty <= 0 {
		return nil // no check needed for zero/negative requests
	}
	if tx == nil {
		return errors.New("transaction required")
	}

	var currentQty float64
	err := tx.QueryRowContext(ctx, `
SELECT COALESCE(quantity, 0)
FROM inventory
WHERE location_id = ?
  AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL))
`, locationID, nullIfEmpty(itemID), nullIfEmpty(variantID)).Scan(&currentQty)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read inventory: %w", err)
	}

	if currentQty < requestedQty {
		itemRef := valueOrDefault(itemID, variantID)
		return fmt.Errorf("insufficient stock for item %s at location %s (have %.2f, need %.2f)", itemRef, locationID, currentQty, requestedQty)
	}
	return nil
}

// RecordStockMovement creates a stock_movements entry and updates inventory aggregate.
func (r *POSRepo) RecordStockMovement(ctx context.Context, tx *sql.Tx, in StockMovementInput) (string, error) {
	if in.LocationID == "" {
		return "", errors.New("locationID required")
	}
	if in.ItemID == "" && in.VariantID == "" {
		return "", errors.New("itemID or variantID required")
	}
	if in.ItemID != "" && in.VariantID != "" {
		return "", errors.New("cannot specify both itemID and variantID")
	}
	if in.Type == "" {
		return "", errors.New("type required")
	}
	if in.Quantity == 0 {
		return "", errors.New("quantity must be non-zero")
	}

	useTx := tx
	createdTx := false
	if useTx == nil {
		var err error
		useTx, err = r.db.BeginTx(ctx, nil)
		if err != nil {
			return "", fmt.Errorf("begin tx: %w", err)
		}
		createdTx = true
	}

	movementID := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := useTx.ExecContext(ctx, `
INSERT INTO stock_movements (id, item_id, variant_id, location_id, sale_line_id, type, quantity, cost_price, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, movementID, nullIfEmpty(in.ItemID), nullIfEmpty(in.VariantID), in.LocationID, nullIfEmpty(in.SaleLineID), in.Type, in.Quantity, nullInt64(in.CostPrice), now)
	if err != nil {
		// Fallback for schemas without cost_price column.
		if strings.Contains(err.Error(), "cost_price") {
			_, err = useTx.ExecContext(ctx, `
INSERT INTO stock_movements (id, item_id, variant_id, location_id, sale_line_id, type, quantity, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, movementID, nullIfEmpty(in.ItemID), nullIfEmpty(in.VariantID), in.LocationID, nullIfEmpty(in.SaleLineID), in.Type, in.Quantity, now)
		}
	}
	if err != nil {
		if createdTx {
			useTx.Rollback()
		}
		return "", fmt.Errorf("insert stock movement: %w", err)
	}

	res, err := useTx.ExecContext(ctx, `
UPDATE inventory
SET quantity = quantity + ?, updated_at = ?
WHERE location_id = ?
  AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL))
`, in.Quantity, now, in.LocationID, nullIfEmpty(in.ItemID), nullIfEmpty(in.VariantID))
	if err != nil {
		if createdTx {
			useTx.Rollback()
		}
		return "", fmt.Errorf("update inventory: %w", err)
	}
	aff, _ := res.RowsAffected()
	if aff == 0 {
		if _, err := useTx.ExecContext(ctx, `
INSERT INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
`, uuid.NewString(), nullIfEmpty(in.ItemID), nullIfEmpty(in.VariantID), in.LocationID, in.Quantity, now); err != nil {
			if createdTx {
				useTx.Rollback()
			}
			return "", fmt.Errorf("insert inventory: %w", err)
		}
	}

	payload := map[string]any{
		"type":     in.Type,
		"quantity": in.Quantity,
		"reason":   in.Reason,
	}
	payloadJSON, _ := json.Marshal(payload)
	if _, err := useTx.ExecContext(ctx, `
INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, data_json, created_at)
VALUES (?, ?, 'inventory', ?, ?, ?, ?)
`, uuid.NewString(), nullIfEmpty(in.ActorID), movementID, in.Type, string(payloadJSON), now); err != nil {
		if createdTx {
			useTx.Rollback()
		}
		return "", fmt.Errorf("insert audit: %w", err)
	}

	if createdTx {
		if err := useTx.Commit(); err != nil {
			return "", fmt.Errorf("commit stock movement: %w", err)
		}
	}

	return movementID, nil
}

// RecordStockMovementSavepoint runs RecordStockMovement inside a SAVEPOINT
// on the caller's tx, so a failure partway through it (any of its four
// statements — stock_movements insert, inventory update, the conditional
// inventory insert, or the audit insert) never leaves a partial write
// sitting in the caller's still-open transaction. Unlike RecordStockMovement
// called with tx == nil, this always requires a caller-supplied tx: when
// tx != nil, RecordStockMovement only rolls back on failure if it opened
// the transaction itself (createdTx) — a caller-supplied tx is left exactly
// as-is on error, by design, so the caller can decide whether that failure
// should fail its own surrounding transaction. Use this method instead of
// calling RecordStockMovement(ctx, tx, in) directly whenever the caller
// wants the OPPOSITE of that: the movement to be atomic on its own, but its
// failure to NOT force the surrounding transaction to roll back (ut-docs#310
// — the catalog import row transaction treats a stock-recording failure as
// warn-and-continue, not a row failure, so the row's item + inventory row
// must still be committable after this returns an error).
func (r *POSRepo) RecordStockMovementSavepoint(ctx context.Context, tx *sql.Tx, in StockMovementInput) (string, error) {
	if tx == nil {
		return "", errors.New("transaction required")
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT record_stock_movement`); err != nil {
		return "", fmt.Errorf("savepoint: %w", err)
	}
	id, recErr := r.RecordStockMovement(ctx, tx, in)
	if recErr != nil {
		if _, err := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT record_stock_movement`); err != nil {
			// The savepoint itself failed to roll back — the caller's tx is
			// in an unknown state. Logged, not swallowed: this is exactly
			// the operationally-interesting case that must not go silent.
			logging.L().Warnf("pos: record stock movement: rollback to savepoint failed: %v", err)
			return "", recErr
		}
		if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT record_stock_movement`); err != nil {
			logging.L().Warnf("pos: record stock movement: release savepoint after rollback failed: %v", err)
		}
		return "", recErr
	}
	if _, err := tx.ExecContext(ctx, `RELEASE SAVEPOINT record_stock_movement`); err != nil {
		// The movement itself fully succeeded — all four statements are
		// sitting in the caller's tx and an unreleased savepoint does not
		// block tx.Commit() from landing them (SQLite pops any remaining
		// savepoints on commit). Failing this call would tell the caller
		// the movement didn't happen when it did: same defect class as the
		// one this whole method exists to close, just on the success path.
		// Logged, not swallowed, same as the rollback-failure branch above.
		logging.L().Warnf("pos: record stock movement: release savepoint after success failed: %v", err)
	}
	return id, nil
}

// RecordNegativeInventoryOverride writes an audit entry noting the override.
func (r *POSRepo) RecordNegativeInventoryOverride(ctx context.Context, override OverrideNegativeInventory) (string, error) {
	if override.ActorID == "" {
		return "", errors.New("actorID required")
	}
	if override.Reason == "" {
		return "", errors.New("reason required")
	}

	overrideID := uuid.NewString()
	snapshot := map[string]any{
		"item_id":     override.ItemID,
		"variant_id":  override.VariantID,
		"location_id": override.LocationID,
		"qty_before":  override.QtyBefore,
	}
	payload := map[string]any{
		"reason":   override.Reason,
		"snapshot": snapshot,
	}
	// Dual attribution (ut-docs#780), same convention as fiscal_api.go's
	// createTSEOverride: only recorded when it actually differs from the
	// audit actor, so a self-authorized override's payload stays as-is.
	if override.RequestedBy != "" && override.RequestedBy != override.ActorID {
		payload["requested_by"] = override.RequestedBy
	}
	payloadJSON, _ := json.Marshal(payload)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := r.db.ExecContext(ctx, `
INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, data_json, created_at)
VALUES (?, ?, 'inventory', ?, 'negative_inventory_override', ?, ?)
`, overrideID, override.ActorID, fmt.Sprintf("%s:%s", override.LocationID, valueOrDefault(override.ItemID, override.VariantID)), string(payloadJSON), now); err != nil {
		return "", fmt.Errorf("insert override audit: %w", err)
	}

	return overrideID, nil
}

// GetLowStockItems returns all items where current inventory is below reorder level.
func (r *POSRepo) GetLowStockItems(ctx context.Context, locationID string) ([]LowStockItem, error) {
	query := `
SELECT 
	i.id,
	i.name,
	COALESCE(i.sku, ''),
	COALESCE(inv.location_id, ''),
	COALESCE(sl.name, ''),
	COALESCE(inv.quantity, 0),
	i.reorder_level
FROM items i
LEFT JOIN inventory inv ON inv.item_id = i.id
LEFT JOIN stock_locations sl ON sl.id = inv.location_id
WHERE i.reorder_level > 0
  AND COALESCE(inv.quantity, 0) < i.reorder_level
`
	args := []any{}
	if locationID != "" {
		// inv.location_id IS NULL = never stocked anywhere (LEFT JOIN
		// missed; the column itself is NOT NULL so this can't over-match)
		// — a new item awaiting its first delivery belongs on every
		// location's reorder list.
		query += ` AND (inv.location_id = ? OR inv.location_id IS NULL)`
		args = append(args, locationID)
	}
	query += ` ORDER BY i.name`

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query low stock: %w", err)
	}
	defer rows.Close()

	var items []LowStockItem
	for rows.Next() {
		var item LowStockItem
		if err := rows.Scan(&item.ItemID, &item.Name, &item.SKU, &item.LocationID, &item.LocationName, &item.CurrentQty, &item.ReorderLevel); err != nil {
			return nil, fmt.Errorf("scan low stock item: %w", err)
		}
		items = append(items, item)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate low stock: %w", err)
	}

	return items, nil
}

// ---- Reports ----

// reportWindowFmt is the text format calendar-aligned report queries render
// their [from, to) bound params in. created_at itself is NOT reliably in
// this format — the sales table's schema default is datetime('now') (this
// format, space-separated), but every real INSERT path
// (internal/pos/sales.go) supplies an explicit RFC3339 value instead
// ("...THH:MM:SSZ"), so production rows are actually RFC3339. A raw string
// compare between the two formats is wrong on top of that: PeriodComparison
// already documents the exact failure (same calendar day, 'T' sorts after
// ' ', so a same-day row silently fails a naive `>=` check). Every window
// query below therefore wraps BOTH the stored column and the bound param in
// SQLite's datetime(...), which parses either format and re-emits one
// canonical text form before comparing — the same fix PeriodComparison used,
// now applied uniformly instead of only where the bug had already been hit.
const reportWindowFmt = "2006-01-02 15:04:05"

// windowArgs renders a [from, to) pair as the two datetime(...)-comparable
// bound params every report query below takes, in order.
func windowArgs(from, to time.Time) (string, string) {
	return from.UTC().Format(reportWindowFmt), to.UTC().Format(reportWindowFmt)
}

// instantWindow renders the close-to-close [from, to) comparison for one
// timestamp column (ADR-0066 Decision 2, ut-docs#1140):
// datetime(col) >= datetime(?) AND datetime(col) < datetime(?) — a true
// half-open INSTANT compare, never the date(...) calendar-day bucketing the
// date-string sibling queries use. Both sides go through SQLite's
// datetime(...) because created_at is not stored in one canonical text form
// (see reportWindowFmt's doc comment: schema default vs RFC3339 insert
// paths); datetime(...) normalizes the form without bucketing to a day.
// Bound params are rendered by windowArgs, same as every other window query
// in this file.
//
// A zero `from` is the till's first-ever close (ADR-0066 Decision 3): the
// lower bound is omitted ENTIRELY — "since the beginning of recorded
// history" — never backfilled with a synthetic epoch/install date, so every
// completed sale not covered by a previous close is in scope for the first
// one. Returns the WHERE fragment (no leading AND) and its args, ready to
// splice into the "eod" kind's instant-windowed queries.
func instantWindow(col string, from, to time.Time) (string, []any) {
	fromStr, toStr := windowArgs(from, to)
	if from.IsZero() {
		return "datetime(" + col + ") < datetime(?)", []any{toStr}
	}
	// Parenthesized even though every current call site splices this into
	// an all-AND WHERE (review finding N5, ut-docs#1140): free insurance
	// against a future caller splicing it after an OR and silently getting
	// wrong precedence.
	return "(datetime(" + col + ") >= datetime(?) AND datetime(" + col + ") < datetime(?))",
		[]any{fromStr, toStr}
}

type DailySales struct {
	Day      string `json:"day"`
	Count    int    `json:"count"`
	Total    int64  `json:"total"`
	TaxTotal int64  `json:"tax_total"`
}

type TopItem struct {
	Name    string  `json:"name"`
	Qty     float64 `json:"qty"`
	Revenue int64   `json:"revenue"`
}

type MethodTotal struct {
	Method string `json:"method"`
	Count  int    `json:"count"`
	Amount int64  `json:"amount"`
}

// DeptSales is one department's revenue for a reporting window. A department is
// an item's top-level (root) category — department stores merchandise and
// report along that axis (menswear, electronics, grocery…). See
// docs/arch/enterprise-department-stores.md (increment E1).
type DeptSales struct {
	Department string  `json:"department"`
	Qty        float64 `json:"qty"`
	Revenue    int64   `json:"revenue"`
}

// deptRootsCTE maps every category id to its top-level (department) category by
// walking parent_id up to the root. Shared by the day and window queries.
const deptRootsCTE = `
WITH RECURSIVE dept_roots(id, root_name) AS (
    SELECT id, name FROM categories WHERE parent_id IS NULL
    UNION ALL
    SELECT c.id, d.root_name FROM categories c JOIN dept_roots d ON c.parent_id = d.id
)`

// SalesByDepartment aggregates completed-sale revenue by department over
// [from, to). Items with no category (or since-deleted) roll up to
// "Uncategorized". Variant lines resolve through their parent item.
func (r *POSRepo) SalesByDepartment(ctx context.Context, from, to time.Time) ([]DeptSales, error) {
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, deptRootsCTE+`
SELECT COALESCE(dr.root_name, '') AS department,
       SUM(sl.quantity) AS qty,
       COALESCE(SUM(sl.total_after_tax), 0) AS revenue
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
LEFT JOIN item_variants iv ON iv.id = sl.variant_id
LEFT JOIN items it ON it.id = COALESCE(sl.item_id, iv.item_id)
LEFT JOIN dept_roots dr ON dr.id = it.category_id
WHERE s.status = 'completed' AND s.sale_type = 'sale'
  AND datetime(s.created_at) >= datetime(?) AND datetime(s.created_at) < datetime(?)
GROUP BY department
ORDER BY revenue DESC`, fromStr, toStr)
	if err != nil {
		return nil, fmt.Errorf("sales by department: %w", err)
	}
	defer rows.Close()
	var out []DeptSales
	for rows.Next() {
		var d DeptSales
		if err := rows.Scan(&d.Department, &d.Qty, &d.Revenue); err != nil {
			return nil, fmt.Errorf("scan dept sales: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// TillSales is one register's revenue for a reporting window. Department stores
// run many tills; per-register totals drive cash-up and reconciliation
// (docs/arch/enterprise-department-stores.md, increment E4).
type TillSales struct {
	TillID  string `json:"till_id"`
	Name    string `json:"name"`
	Count   int    `json:"count"`
	Revenue int64  `json:"revenue"`
}

// SalesByTill aggregates completed-sale revenue per till over [from, to).
// sales.till_id is ” for the primary till / pre-sync history (ADR-0011 D3);
// that rolls up under an empty id, which the UI labels "This till". Named
// replicas resolve through the tills table.
func (r *POSRepo) SalesByTill(ctx context.Context, from, to time.Time) ([]TillSales, error) {
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT s.till_id, COALESCE(t.name, '') AS name,
       COUNT(*) AS cnt, COALESCE(SUM(s.total), 0) AS revenue
FROM sales s
LEFT JOIN tills t ON t.id = s.till_id
WHERE s.status = 'completed' AND s.sale_type = 'sale'
  AND datetime(s.created_at) >= datetime(?) AND datetime(s.created_at) < datetime(?)
GROUP BY s.till_id
ORDER BY revenue DESC`, fromStr, toStr)
	if err != nil {
		return nil, fmt.Errorf("sales by till: %w", err)
	}
	defer rows.Close()
	var out []TillSales
	for rows.Next() {
		var t TillSales
		if err := rows.Scan(&t.TillID, &t.Name, &t.Count, &t.Revenue); err != nil {
			return nil, fmt.Errorf("scan till sales: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DepartmentsForDay is SalesByDepartment for a single business day (used by the
// EOD Z-report). day is "YYYY-MM-DD", matched on the shop's LOCAL calendar
// day (ut-docs#869) — see dateRangeSummary's doc comment for why.
func (r *POSRepo) DepartmentsForDay(ctx context.Context, day string) ([]DeptSales, error) {
	rows, err := r.db.QueryContext(ctx, deptRootsCTE+`
SELECT COALESCE(dr.root_name, '') AS department,
       SUM(sl.quantity) AS qty,
       COALESCE(SUM(sl.total_after_tax), 0) AS revenue
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
LEFT JOIN item_variants iv ON iv.id = sl.variant_id
LEFT JOIN items it ON it.id = COALESCE(sl.item_id, iv.item_id)
LEFT JOIN dept_roots dr ON dr.id = it.category_id
WHERE s.status = 'completed' AND s.sale_type = 'sale' AND date(s.created_at, 'localtime') = date(?)
GROUP BY department
ORDER BY revenue DESC`, day)
	if err != nil {
		return nil, fmt.Errorf("departments for day: %w", err)
	}
	defer rows.Close()
	var out []DeptSales
	for rows.Next() {
		var d DeptSales
		if err := rows.Scan(&d.Department, &d.Qty, &d.Revenue); err != nil {
			return nil, fmt.Errorf("scan dept day: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DepartmentsForInstantWindow is DepartmentsForDay's close-to-close sibling
// (ADR-0066 Decision 2, ut-docs#1140): the same deptRootsCTE department
// rollup over a half-open [from, to) INSTANT window instead of one local
// calendar day — see instantWindow's doc comment for the comparison form
// and the zero-`from` (till's first-ever close) unbounded case. A genuinely
// parallel query, not a wrapper: DepartmentsForDay stays calendar-day
// untouched for EndOfDayRange, per the ADR's "parallel siblings, not a
// retrofit" decision.
func (r *POSRepo) DepartmentsForInstantWindow(ctx context.Context, from, to time.Time) ([]DeptSales, error) {
	win, args := instantWindow("s.created_at", from, to)
	rows, err := r.db.QueryContext(ctx, deptRootsCTE+`
SELECT COALESCE(dr.root_name, '') AS department,
       SUM(sl.quantity) AS qty,
       COALESCE(SUM(sl.total_after_tax), 0) AS revenue
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
LEFT JOIN item_variants iv ON iv.id = sl.variant_id
LEFT JOIN items it ON it.id = COALESCE(sl.item_id, iv.item_id)
LEFT JOIN dept_roots dr ON dr.id = it.category_id
WHERE s.status = 'completed' AND s.sale_type = 'sale' AND `+win+`
GROUP BY department
ORDER BY revenue DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("departments for instant window: %w", err)
	}
	defer rows.Close()
	var out []DeptSales
	for rows.Next() {
		var d DeptSales
		if err := rows.Scan(&d.Department, &d.Qty, &d.Revenue); err != nil {
			return nil, fmt.Errorf("scan dept instant window: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// SalesByDay aggregates completed sales per day over [from, to). day is
// grouped by the shop's LOCAL business day, not the raw stored UTC calendar
// date: created_at is converted to local time ('localtime', the same
// process/host timezone time.Local resolves to in this single-process till,
// consistent with businessDateFor's own design in reports_page.go) and then
// shifted back by the configured business-day-start hh:mm (parseBusinessDayStart)
// before truncating to a date — so a trading night that spans local midnight
// or the configured boundary collapses into one row instead of two
// (ut-docs#559). hh=mm=0 (the default, calendar-local-midnight) is a no-op
// vs. the previous query when the host timezone is UTC.
// Returns are excluded, matching DayTotal on the same dashboard (and
// SlowItems/busyBuckets); the Reports "Tax" tab (computeTaxSummary,
// ut-docs#1115) is the fiscal view and nets them.
func (r *POSRepo) SalesByDay(ctx context.Context, from, to time.Time, hh, mm int) ([]DailySales, error) {
	fromStr, toStr := windowArgs(from, to)
	hourMod := fmt.Sprintf("%d hours", -hh)
	minMod := fmt.Sprintf("%d minutes", -mm)
	rows, err := r.db.QueryContext(ctx, `
SELECT date(created_at, 'localtime', ?, ?) AS day, COUNT(*), COALESCE(SUM(total), 0), COALESCE(SUM(tax_total), 0)
FROM sales
WHERE status = 'completed' AND sale_type = 'sale'
  AND datetime(created_at) >= datetime(?) AND datetime(created_at) < datetime(?)
GROUP BY day ORDER BY day DESC`, hourMod, minMod, fromStr, toStr)
	if err != nil {
		return nil, fmt.Errorf("sales by day: %w", err)
	}
	defer rows.Close()
	var out []DailySales
	for rows.Next() {
		var d DailySales
		if err := rows.Scan(&d.Day, &d.Count, &d.Total, &d.TaxTotal); err != nil {
			return nil, fmt.Errorf("scan daily sales: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// RefundsByWindow sums completed returns over [from, to) — the counterpart
// SalesByDay excludes (SalesByDay's own doc comment), so callers that show
// gross-of-returns revenue can still surface refunds alongside it (e.g.
// /reports' Refunds/Net KPIs).
func (r *POSRepo) RefundsByWindow(ctx context.Context, from, to time.Time) (total int64, count int, err error) {
	fromStr, toStr := windowArgs(from, to)
	err = r.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(total), 0), COUNT(*)
FROM sales
WHERE status = 'completed' AND sale_type = 'return'
  AND datetime(created_at) >= datetime(?) AND datetime(created_at) < datetime(?)`,
		fromStr, toStr).Scan(&total, &count)
	if err != nil {
		return 0, 0, fmt.Errorf("refunds by window: %w", err)
	}
	return total, count, nil
}

// TopItems returns the best sellers by revenue over [from, to).
func (r *POSRepo) TopItems(ctx context.Context, from, to time.Time, limit int) ([]TopItem, error) {
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT sl.name_snapshot, SUM(sl.quantity), COALESCE(SUM(sl.total_after_tax), 0) AS revenue
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
WHERE s.status = 'completed' AND s.sale_type = 'sale'
  AND datetime(s.created_at) >= datetime(?) AND datetime(s.created_at) < datetime(?)
GROUP BY sl.name_snapshot ORDER BY revenue DESC LIMIT ?`, fromStr, toStr, limit)
	if err != nil {
		return nil, fmt.Errorf("top items: %w", err)
	}
	defer rows.Close()
	var out []TopItem
	for rows.Next() {
		var t TopItem
		if err := rows.Scan(&t.Name, &t.Qty, &t.Revenue); err != nil {
			return nil, fmt.Errorf("scan top item: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SlowItems mirrors TopItems but ascending: the WORST sellers that still had
// at least one sale in the window — candidates for delisting or promotion.
func (r *POSRepo) SlowItems(ctx context.Context, from, to time.Time, limit int) ([]TopItem, error) {
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT sl.name_snapshot, SUM(sl.quantity) AS qty, COALESCE(SUM(sl.total_after_tax), 0) AS revenue
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
WHERE s.status = 'completed' AND s.sale_type = 'sale'
  AND datetime(s.created_at) >= datetime(?) AND datetime(s.created_at) < datetime(?)
GROUP BY sl.name_snapshot HAVING qty > 0 ORDER BY revenue ASC LIMIT ?`, fromStr, toStr, limit)
	if err != nil {
		return nil, fmt.Errorf("slow items: %w", err)
	}
	defer rows.Close()
	var out []TopItem
	for rows.Next() {
		var t TopItem
		if err := rows.Scan(&t.Name, &t.Qty, &t.Revenue); err != nil {
			return nil, fmt.Errorf("scan slow item: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeadStockRow is an active item holding stock that hasn't sold at all in the
// reporting window — capital sitting on the shelf.
type DeadStockRow struct {
	Name       string
	SKU        string
	Qty        float64
	StockValue int64 // qty × base_price, minor units
}

// DeadStock lists active items with on-hand stock and ZERO sales over
// [from, to), most tied-up value first.
func (r *POSRepo) DeadStock(ctx context.Context, from, to time.Time, limit int) ([]DeadStockRow, error) {
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT i.name, COALESCE(i.sku, ''), SUM(inv.quantity) AS qty,
       CAST(SUM(inv.quantity) * i.base_price AS INTEGER) AS value
FROM inventory inv
JOIN items i ON i.id = inv.item_id
WHERE i.is_active = 1 AND inv.quantity > 0
  AND i.id NOT IN (
    SELECT DISTINCT COALESCE(NULLIF(sl.item_id, ''), v.item_id) FROM sale_lines sl
    JOIN sales s ON s.id = sl.sale_id
    LEFT JOIN item_variants v ON v.id = sl.variant_id
    WHERE s.status = 'completed' AND s.sale_type = 'sale'
      AND COALESCE(NULLIF(sl.item_id, ''), v.item_id) IS NOT NULL
      AND datetime(s.created_at) >= datetime(?) AND datetime(s.created_at) < datetime(?)
  )
GROUP BY i.id ORDER BY value DESC LIMIT ?`, fromStr, toStr, limit)
	if err != nil {
		return nil, fmt.Errorf("dead stock: %w", err)
	}
	defer rows.Close()
	var out []DeadStockRow
	for rows.Next() {
		var d DeadStockRow
		if err := rows.Scan(&d.Name, &d.SKU, &d.Qty, &d.StockValue); err != nil {
			return nil, fmt.Errorf("scan dead stock: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PeriodTotals is one period's headline numbers for comparisons.
type PeriodTotals struct {
	Total int64
	Count int
}

// PeriodComparison returns totals for [from, to) vs the SAME window shifted
// back one calendar year (via AddDate, so leap years land correctly) — the
// honest year-over-year comparison (empty year-ago data simply reports
// zeros; the page hides the card until there is history).
func (r *POSRepo) PeriodComparison(ctx context.Context, from, to time.Time) (current, yearAgo PeriodTotals, err error) {
	// created_at is stored as RFC3339 ("...THH:MM:SSZ") in practice, though
	// the column's schema DEFAULT is datetime('now')'s own space-separated
	// form; both sides must go through datetime(...) or a same-calendar-day
	// comparison becomes a raw string compare where 'T' sorts after ' ',
	// silently dropping every same-day sale out of the "current period"
	// upper bound.
	q := `SELECT COUNT(*), COALESCE(SUM(total), 0) FROM sales
WHERE status = 'completed' AND sale_type = 'sale'
  AND datetime(created_at) >= datetime(?) AND datetime(created_at) < datetime(?)`
	fromStr, toStr := windowArgs(from, to)
	if err = r.db.QueryRowContext(ctx, q, fromStr, toStr).
		Scan(&current.Count, &current.Total); err != nil {
		return current, yearAgo, fmt.Errorf("current period: %w", err)
	}
	yFromStr, yToStr := windowArgs(from.AddDate(-1, 0, 0), to.AddDate(-1, 0, 0))
	if err = r.db.QueryRowContext(ctx, q, yFromStr, yToStr).
		Scan(&yearAgo.Count, &yearAgo.Total); err != nil {
		return current, yearAgo, fmt.Errorf("year-ago period: %w", err)
	}
	return current, yearAgo, nil
}

// TaxBand is one tax rate's totals over the reporting window — the VAT
// summary an owner (or their accountant) needs per return period. JSON tags
// are snake_case per this repo's API convention: EODReport.TaxBands
// (ut-docs#1003) marshals these into the archived/downloaded Z-report;
// the Reports "Tax" tab's template-rendered use (internal/pages'
// computeTaxSummary, ut-docs#1115) reads the Go fields directly and never
// marshals, so the tags change no pre-existing wire format.
type TaxBand struct {
	RateBP int   `json:"rate_bp"` // basis points (2000 = 20%)
	Net    int64 `json:"net"`     // taxable amount before tax, minor units
	Tax    int64 `json:"tax"`     // tax collected, minor units
	Gross  int64 `json:"gross"`   // Net + Tax: tax-inclusive, minor units
}

// MethodTaxBand is one (payment method, VAT rate)'s totals over the
// reporting window — the cell the DATEV/accounting export posting batch
// is generated from (debit account = method, credit account = rate).
// ut-docs#1004.
type MethodTaxBand struct {
	Method string `json:"method"`
	RateBP int    `json:"rate_bp"`
	Net    int64  `json:"net"`
	Tax    int64  `json:"tax"`
	Gross  int64  `json:"gross"`
}

// SalesForTaxWindow returns completed sales (with their lines) whose
// created_at falls in the half-open window [from, to) — the raw fetch the
// Reports "Tax" tab bands off of (ut-docs#1115, replacing the old
// TaxSummary, which aggregated sale_lines directly and so — like the
// pre-ut-docs#1003 Z-report — couldn't see a whole-sale discount or a
// service charge, neither of which has a sale_lines row).
//
// Deliberately time.Time/windowArgs-based, not SalesForTaxBands' calendar-
// day BETWEEN: the Tax tab takes an arbitrary rolling window (e.g. "last 30
// days"), not a Z-report's single calendar day. Reuses EODTaxBandSale/
// EODTaxBandLine and the same zero-value-line exclusion (see
// SalesForTaxBands' own doc comment) so the pages layer can band these
// sales with the exact same pos.VATBandsForSale call the day-close Z-report
// bands with — the two banding math paths can never disagree over the SAME
// set of sales. The window ITSELF can still differ from a Z-report's,
// though: this uses windowArgs' raw datetime bounds with no business-day
// shift, while EndOfDay/EndOfDayRange apply reports.business_day_start
// (ut-docs#519) before picking a calendar day — same set at the default
// midnight boundary, genuinely different sales for a shop that has pushed
// it later. Payments aren't fetched here: nothing downstream of this
// builds a MethodTaxBand cross-tab.
func (r *POSRepo) SalesForTaxWindow(ctx context.Context, from, to time.Time) ([]EODTaxBandSale, error) {
	fromStr, toStr := windowArgs(from, to)
	saleRows, err := r.db.QueryContext(ctx, `
SELECT id, sale_type, subtotal, discount_total, tax_total, total,
       service_charge_amount, service_charge_tax_basis_bp, voucher_issue_total
FROM sales
WHERE status = 'completed'
  AND datetime(created_at) >= datetime(?) AND datetime(created_at) < datetime(?)
ORDER BY created_at, id`, fromStr, toStr)
	if err != nil {
		return nil, fmt.Errorf("tax window sales: %w", err)
	}
	defer saleRows.Close()
	var out []EODTaxBandSale
	idx := map[string]int{}
	for saleRows.Next() {
		var s EODTaxBandSale
		if err := saleRows.Scan(&s.ID, &s.SaleType, &s.Subtotal, &s.DiscountTotal,
			&s.TaxTotal, &s.Total, &s.ServiceCharge, &s.ServiceChargeTaxBasisBP, &s.VoucherIssueTotal); err != nil {
			return nil, fmt.Errorf("scan tax window sale: %w", err)
		}
		idx[s.ID] = len(out)
		out = append(out, s)
	}
	if err := saleRows.Err(); err != nil {
		return nil, err
	}

	lineRows, err := r.db.QueryContext(ctx, `
SELECT sl.sale_id, COALESCE(sl.tax_rate_bp, 0), sl.tax_amount, sl.total_after_tax
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
WHERE s.status = 'completed'
  AND datetime(s.created_at) >= datetime(?) AND datetime(s.created_at) < datetime(?)
  AND (sl.total_before_tax != 0 OR sl.total_after_tax != 0)
ORDER BY sl.sale_id, sl.line_no`, fromStr, toStr)
	if err != nil {
		return nil, fmt.Errorf("tax window lines: %w", err)
	}
	defer lineRows.Close()
	for lineRows.Next() {
		var saleID string
		var l EODTaxBandLine
		if err := lineRows.Scan(&saleID, &l.RateBP, &l.TaxAmount, &l.LineTotal); err != nil {
			return nil, fmt.Errorf("scan tax window line: %w", err)
		}
		if i, ok := idx[saleID]; ok {
			out[i].Lines = append(out[i].Lines, l)
		}
	}
	return out, lineRows.Err()
}

// MarginRow is one item's revenue vs cost over the reporting window (only
// items with a recorded cost price appear).
type MarginRow struct {
	Name    string
	Revenue int64
	Cost    int64
	Margin  int64
}

// MarginByItem computes per-item margin (revenue − qty×cost) over
// [from, to), using the variant's cost when present, else the item's. Lines
// with no known cost are excluded — honest numbers only.
func (r *POSRepo) MarginByItem(ctx context.Context, from, to time.Time, limit int) ([]MarginRow, error) {
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT sl.name_snapshot,
       COALESCE(SUM(sl.total_after_tax), 0) AS revenue,
       CAST(SUM(sl.quantity * COALESCE(v.cost_price, i.cost_price)) AS INTEGER) AS cost
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
LEFT JOIN item_variants v ON v.id = sl.variant_id
LEFT JOIN items i ON i.id = COALESCE(NULLIF(sl.item_id, ''), v.item_id)
WHERE s.status = 'completed' AND s.sale_type = 'sale'
  AND datetime(s.created_at) >= datetime(?) AND datetime(s.created_at) < datetime(?)
  AND COALESCE(v.cost_price, i.cost_price) IS NOT NULL
GROUP BY sl.name_snapshot
ORDER BY (revenue - cost) DESC LIMIT ?`, fromStr, toStr, limit)
	if err != nil {
		return nil, fmt.Errorf("margins: %w", err)
	}
	defer rows.Close()
	var out []MarginRow
	for rows.Next() {
		var m MarginRow
		if err := rows.Scan(&m.Name, &m.Revenue, &m.Cost); err != nil {
			return nil, fmt.Errorf("scan margin: %w", err)
		}
		m.Margin = m.Revenue - m.Cost
		out = append(out, m)
	}
	return out, rows.Err()
}

// DayTotal returns one calendar day's completed-sale revenue (local time),
// offset days back from ref (1 = the day before ref).
//
// ref is a caller-supplied instant, not SQLite's own 'now' — a caller doing
// several DayTotal reads to compare days against each other (e.g. "yesterday"
// against several weeks of baseline) must pass the SAME ref to every call so
// "today" can't drift between two independent reads of the real clock a few
// milliseconds apart. That drift is real: around a local day boundary, a Go
// caller's own time.Now() and a later SQLite 'now' evaluated a moment
// afterward can land on different calendar days, silently shifting every
// daysAgo offset by one and misaligning "yesterday" against its own baseline
// weekday (ut-docs#969 — reproduced this way, not as a real weekday-specific
// detector bug).
func (r *POSRepo) DayTotal(ctx context.Context, daysAgo int, ref time.Time) (int64, int, error) {
	var total int64
	var count int
	err := r.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(total), 0), COUNT(*) FROM sales
WHERE status = 'completed' AND sale_type = 'sale'
  AND date(created_at, 'localtime') = date(?, 'localtime', ?)`,
		ref.UTC().Format(time.RFC3339), fmt.Sprintf("-%d days", daysAgo)).Scan(&total, &count)
	if err != nil {
		return 0, 0, fmt.Errorf("day total: %w", err)
	}
	return total, count, nil
}

// SeasonalItem is one item's expected demand for the upcoming window — the
// order-ahead signal — averaged across up to three prior years. Each prior
// year is inspected through TWO windows: the same solar-calendar window
// (k×365 days back) and the same lunar-calendar window (k×354 days back —
// lunar-tied demand such as Ramadan shifts ~11 days earlier every Gregorian
// year). A year contributes whichever signal is stronger, so both fixed-date
// and moving-holiday demand are caught without any hardcoded holiday list.
// Deliberate bias: for k=1 the two windows overlap ~17 of 28 days, so max()
// can lean on sales up to ~11 days outside the solar window and nudge the
// suggestion UP for lumpy demand — accepted, because missing a real
// moving-holiday spike costs a shop more than a modestly generous advisory
// order hint. Likewise an item that stopped selling is averaged DOWN year
// by year but only disappears once its last signal ages past the 3-year
// horizon — the fade-out is the signal that it's dying, not a bug.
type SeasonalItem struct {
	Name       string
	Category   string  // category name; empty when uncategorized
	Expected   float64 // average units per prior year, rounded to 1 decimal
	Years      int     // the average's divisor: years back to the item's oldest signal (gap years included)
	Lunar      bool    // the lunar-window signal outweighed the solar one
	OnHand     float64
	SuggestQty int // ceil(Expected − OnHand), 0 when covered
}

// SeasonalCategory is the same forecast rolled up per category. SuggestQty
// sums the member items' suggestions (one item's surplus can't cover
// another), so it can exceed ceil(Expected − OnHand).
type SeasonalCategory struct {
	Name       string // empty = uncategorized bucket
	Expected   float64
	OnHand     float64
	SuggestQty int
}

// lunarYearDays is the mean lunar (Hijri) year length in days.
const lunarYearDays = 354.37

// seasonalWindow is one historical solar- or lunar-calendar window: units
// sold in the `days`-long span starting `offset` days before now, for prior
// year k (1..3).
type seasonalWindow struct {
	k      int
	lunar  bool
	offset int
}

// seasonalWindowQuery builds ONE query that sums units per item across ALL
// given windows in a single sale_lines/sales/items scan, instead of one
// query per window (ut-docs#199 — up to 6 full scans per SeasonalForecast
// call, each unindexable: created_at is stored RFC3339 ('...T...Z'), while
// datetime('now', ...) yields SQLite's own space-separated format, so the
// column side of the comparison MUST go through datetime() too (see the
// PeriodComparison trap above) — that wrapper is what makes idx_sales_created
// unusable, on every one of the old per-window queries alike. Collapsing to
// one query doesn't fix that, but it does cut the table scan count from up
// to 6 down to 1, which is the structural fix this ticket asked for.
// Each window contributes one SUM(CASE WHEN <window bound> ...) column, and
// the same bound also gates which rows the query even considers (the WHERE
// OR), so an item with no MATCHING ROW in any window never appears in the
// result. That's weaker than the old per-window queries' HAVING units > 0,
// though: a row that exists inside a window but sums to zero/net-zero
// quantity still passes the WHERE gate and comes back as an all-zero row —
// SeasonalForecast's caller is the one that must skip it (see the firstK==0
// guard there), this builder only guarantees "no window, no row".
func seasonalWindowQuery(windows []seasonalWindow, days int) (string, []any) {
	if len(windows) == 0 {
		return "", nil
	}
	type bound struct{ from, to string }
	bounds := make([]bound, len(windows))
	for i, w := range windows {
		bounds[i] = bound{
			from: fmt.Sprintf("-%d days", w.offset),
			to:   fmt.Sprintf("-%d days", w.offset-days),
		}
	}
	cols := make([]string, len(windows))
	preds := make([]string, len(windows))
	for i := range windows {
		pred := "(datetime(s.created_at) >= datetime('now', ?) AND datetime(s.created_at) < datetime('now', ?))"
		cols[i] = fmt.Sprintf("SUM(CASE WHEN %s THEN sl.quantity ELSE 0 END) AS w%d", pred, i)
		preds[i] = pred
	}
	query := fmt.Sprintf(`
SELECT i.id, i.name, COALESCE(c.name, ''), %s
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
JOIN items i ON i.id = COALESCE(NULLIF(sl.item_id, ''),
                                (SELECT v.item_id FROM item_variants v WHERE v.id = sl.variant_id))
LEFT JOIN categories c ON c.id = i.category_id
WHERE s.status = 'completed' AND s.sale_type = 'sale' AND i.is_active = 1
  AND (%s)
GROUP BY i.id`, strings.Join(cols, ",\n       "), strings.Join(preds, "\n    OR "))

	args := make([]any, 0, len(windows)*4)
	for _, b := range bounds { // one pass per SELECT CASE column
		args = append(args, b.from, b.to)
	}
	for _, b := range bounds { // one pass per WHERE OR predicate
		args = append(args, b.from, b.to)
	}
	return query, args
}

// SeasonalForecast looks at the NEXT `days` days across up to three prior
// years (solar + lunar windows, see SeasonalItem) and returns the items that
// sold then with current stock and a suggested top-up, plus the same
// forecast rolled up by category. The category rollup always covers ALL
// forecast items; `limit` trims only the item list (limit <= 0 = unlimited,
// unlike the old SQL LIMIT which returned nothing for 0). Both slices are
// empty when the shop has no year-old history — the UI hides the card.
func (r *POSRepo) SeasonalForecast(ctx context.Context, days, limit int) ([]SeasonalItem, []SeasonalCategory, error) {
	if days <= 0 {
		days = 28
	}
	if days > 180 {
		days = 180 // clamp, don't silently reinterpret a long window as 28
	}
	// A prior year k contributes only once history reaches back to its
	// solar window's end; cap at 3 years.
	var ageDays sql.NullFloat64
	if err := r.db.QueryRowContext(ctx, `
SELECT julianday('now') - julianday(MIN(created_at)) FROM sales
WHERE status = 'completed' AND sale_type = 'sale'`).Scan(&ageDays); err != nil {
		return nil, nil, fmt.Errorf("seasonal history age: %w", err)
	}
	yearsAvail := 0
	for k := 1; k <= 3; k++ {
		if ageDays.Valid && ageDays.Float64 >= float64(k*365-days) {
			yearsAvail = k
		}
	}
	if yearsAvail == 0 {
		return nil, nil, nil
	}

	windows := make([]seasonalWindow, 0, yearsAvail*2)
	for k := 1; k <= yearsAvail; k++ {
		windows = append(windows,
			seasonalWindow{k: k, lunar: false, offset: k * 365},
			seasonalWindow{k: k, lunar: true, offset: int(math.Round(float64(k) * lunarYearDays))})
	}

	type acc struct {
		name, category string
		solar, lunar   [4]float64 // indexed by year k (1..3)
	}
	accs := map[string]*acc{}
	query, args := seasonalWindowQuery(windows, days)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, fmt.Errorf("seasonal windows: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, category string
		sums := make([]float64, len(windows))
		dest := make([]any, 0, 3+len(windows))
		dest = append(dest, &id, &name, &category)
		for i := range sums {
			dest = append(dest, &sums[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, fmt.Errorf("scan seasonal windows: %w", err)
		}
		a := &acc{name: name, category: category}
		accs[id] = a
		for i, w := range windows {
			if w.lunar {
				a.lunar[w.k] = sums[i]
			} else {
				a.solar[w.k] = sums[i]
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("seasonal windows: %w", err)
	}
	if len(accs) == 0 {
		return nil, nil, nil
	}

	// On-hand per parent item, folding variant-level stock up to the item
	// (a variant row has item_id NULL by schema CHECK — without the join,
	// variant-tracked items would look out of stock and drive phantom orders).
	onHand := map[string]float64{}
	scanOnHand := func() error {
		rows, err := r.db.QueryContext(ctx, `
SELECT COALESCE(inv.item_id, v.item_id) AS iid, SUM(inv.quantity)
FROM inventory inv
LEFT JOIN item_variants v ON v.id = inv.variant_id
WHERE COALESCE(inv.item_id, v.item_id) IS NOT NULL
GROUP BY iid`)
		if err != nil {
			return fmt.Errorf("seasonal on-hand: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var qty float64
			if err := rows.Scan(&id, &qty); err != nil {
				return fmt.Errorf("scan seasonal on-hand: %w", err)
			}
			onHand[id] = qty
		}
		return rows.Err()
	}
	if err := scanOnHand(); err != nil {
		return nil, nil, err
	}

	type sortableItem struct {
		SeasonalItem
		id string // final sort tie-break: display names can collide
	}
	sortable := make([]sortableItem, 0, len(accs))
	catAccs := map[string]*SeasonalCategory{}
	for id, a := range accs {
		// Average from the item's oldest signal forward: a newcomer isn't
		// diluted by shop history predating it; a faded one-off is.
		firstK := 0
		for k := yearsAvail; k >= 1; k-- {
			if a.solar[k] > 0 || a.lunar[k] > 0 {
				firstK = k
				break
			}
		}
		if firstK == 0 {
			// seasonalWindowQuery's WHERE only guarantees a matching ROW
			// existed in some window, not that it summed positive (a
			// zero/net-zero-quantity row, or a solar/lunar wash, can still
			// pass it — the old per-window HAVING units > 0 caught this,
			// this doesn't). Without this guard, sum/firstK below is 0/0 =
			// NaN, which then poisons this item's WHOLE category rollup
			// (Expected += NaN) on the shop's live reports page.
			continue
		}
		var sum, sumSolar, sumLunar float64
		for k := 1; k <= firstK; k++ {
			sum += math.Max(a.solar[k], a.lunar[k])
			sumSolar += a.solar[k]
			sumLunar += a.lunar[k]
		}
		it := SeasonalItem{
			Name:     a.name,
			Category: a.category,
			Expected: math.Round(sum/float64(firstK)*10) / 10,
			Years:    firstK,
			Lunar:    sumLunar > sumSolar,
			OnHand:   math.Round(onHand[id]*10) / 10, // SUM of REALs drifts; this renders raw
		}
		if need := it.Expected - it.OnHand; need > 0 {
			it.SuggestQty = int(math.Ceil(need))
		}
		sortable = append(sortable, sortableItem{SeasonalItem: it, id: id})

		c := catAccs[a.category]
		if c == nil {
			c = &SeasonalCategory{Name: a.category}
			catAccs[a.category] = c
		}
		c.Expected += it.Expected
		c.OnHand += it.OnHand
		c.SuggestQty += it.SuggestQty
	}
	sort.Slice(sortable, func(i, j int) bool {
		if sortable[i].Expected != sortable[j].Expected {
			return sortable[i].Expected > sortable[j].Expected
		}
		if sortable[i].Name != sortable[j].Name {
			return sortable[i].Name < sortable[j].Name
		}
		return sortable[i].id < sortable[j].id
	})
	if limit > 0 && len(sortable) > limit {
		sortable = sortable[:limit]
	}
	items := make([]SeasonalItem, 0, len(sortable))
	for _, s := range sortable {
		items = append(items, s.SeasonalItem)
	}
	cats := make([]SeasonalCategory, 0, len(catAccs))
	for _, c := range catAccs {
		// Re-round the accumulated floats: summing 1-decimal values drifts
		// (1.1+2.2 = 3.3000000000000003) and this renders straight into HTML.
		c.Expected = math.Round(c.Expected*10) / 10
		c.OnHand = math.Round(c.OnHand*10) / 10
		cats = append(cats, *c)
	}
	sort.Slice(cats, func(i, j int) bool {
		if cats[i].Expected != cats[j].Expected {
			return cats[i].Expected > cats[j].Expected
		}
		return cats[i].Name < cats[j].Name
	})
	return items, cats, nil
}

// BusySlot is one weekday or hour bucket of sales activity.
type BusySlot struct {
	Slot  int   // weekday 0=Sunday..6, or hour 0..23 (local time)
	Count int   // completed sales
	Total int64 // revenue, minor units
}

// SalesByWeekday buckets completed sales by local weekday over [from, to),
// shifted by the shop's configured business-day-start hh:mm boundary
// (parseBusinessDayStart) the same way SalesByDay's date() grouping is
// (ut-docs#559) — so a trading night that spans the boundary buckets into
// the weekday its business day belongs to, not the raw calendar weekday of
// the stored timestamp, keeping every chart on the Sales-trend tab
// consistent (ut-docs#653). hh=mm=0 (the default) is a no-op vs. the
// previous 2-arg query when the host timezone is UTC.
func (r *POSRepo) SalesByWeekday(ctx context.Context, from, to time.Time, hh, mm int) ([]BusySlot, error) {
	return r.busyBuckets(ctx, from, to, hh, mm, `CAST(strftime('%w', s.created_at, 'localtime', ?, ?) AS INTEGER)`)
}

// SalesByHour buckets completed sales by local hour of day over [from, to),
// shifted by the same business-day-start boundary as SalesByWeekday/
// SalesByDay (ut-docs#653). hh=mm=0 (the default) is a no-op.
func (r *POSRepo) SalesByHour(ctx context.Context, from, to time.Time, hh, mm int) ([]BusySlot, error) {
	return r.busyBuckets(ctx, from, to, hh, mm, `CAST(strftime('%H', s.created_at, 'localtime', ?, ?) AS INTEGER)`)
}

// busyBuckets runs bucketExpr, which must consume the business-day-start
// shift as its own two '?' placeholders (mirroring SalesByDay's
// date(created_at, 'localtime', ?, ?) — see the two callers above) ahead of
// the window bounds' placeholders.
func (r *POSRepo) busyBuckets(ctx context.Context, from, to time.Time, hh, mm int, bucketExpr string) ([]BusySlot, error) {
	fromStr, toStr := windowArgs(from, to)
	hourMod := fmt.Sprintf("%d hours", -hh)
	minMod := fmt.Sprintf("%d minutes", -mm)
	rows, err := r.db.QueryContext(ctx, `
SELECT `+bucketExpr+` AS slot, COUNT(*), COALESCE(SUM(s.total), 0)
FROM sales s
WHERE s.status = 'completed' AND s.sale_type = 'sale'
  AND datetime(s.created_at) >= datetime(?) AND datetime(s.created_at) < datetime(?)
GROUP BY slot ORDER BY slot`, hourMod, minMod, fromStr, toStr)
	if err != nil {
		return nil, fmt.Errorf("busy buckets: %w", err)
	}
	defer rows.Close()
	var out []BusySlot
	for rows.Next() {
		var b BusySlot
		if err := rows.Scan(&b.Slot, &b.Count, &b.Total); err != nil {
			return nil, fmt.Errorf("scan busy bucket: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// PaymentBreakdown sums applied payments per method over [from, to).
func (r *POSRepo) PaymentBreakdown(ctx context.Context, from, to time.Time) ([]MethodTotal, error) {
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT p.method_id, COUNT(*), COALESCE(SUM(p.amount - p.change_given), 0) AS applied
FROM payments p
JOIN sales s ON s.id = p.sale_id
WHERE s.status = 'completed' AND s.sale_type = 'sale'
  AND datetime(s.created_at) >= datetime(?) AND datetime(s.created_at) < datetime(?)
GROUP BY p.method_id ORDER BY applied DESC`, fromStr, toStr)
	if err != nil {
		return nil, fmt.Errorf("payment breakdown: %w", err)
	}
	defer rows.Close()
	var out []MethodTotal
	for rows.Next() {
		var m MethodTotal
		if err := rows.Scan(&m.Method, &m.Count, &m.Amount); err != nil {
			return nil, fmt.Errorf("scan method total: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// CashAdjustmentReasonTotal is one grouped total of manual cash
// adjustments/payouts (RecordCashAdjustment) for a reporting window —
// e.g. "how much Pfandrückgabe was paid out this week" (ut-docs#267).
// Amount is the net signed total in minor units: negative for a reason
// dominated by payouts, positive for one dominated by cash-in
// adjustments (a float top-up).
type CashAdjustmentReasonTotal struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
	Amount int64  `json:"amount"`
}

// CashAdjustmentsByReason groups manual cash adjustments/payouts
// (audit_log action='cash_adjustment' on entity_type='shift', written by
// RecordCashAdjustment) by their reason within [from, to). Fills the gap
// ut-docs#267 found: a fixed reason like CashAdjustmentReasonPfandrueckgabe
// already makes a payout structurally distinct in the audit trail, but
// nothing read that field back out grouped — SumShiftAdjustments only ever
// returns one net total per shift, with no reason breakdown, so the only
// way to see individual payouts was the raw data_json blob on /audit.
func (r *POSRepo) CashAdjustmentsByReason(ctx context.Context, from, to time.Time) ([]CashAdjustmentReasonTotal, error) {
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT COALESCE(json_extract(data_json, '$.reason'), ''), COUNT(*),
       COALESCE(SUM(CAST(json_extract(data_json, '$.amount') AS INTEGER)), 0) AS net
FROM audit_log
WHERE entity_type = 'shift' AND action = 'cash_adjustment'
  AND datetime(created_at) >= datetime(?) AND datetime(created_at) < datetime(?)
GROUP BY 1
ORDER BY ABS(net) DESC, COUNT(*) DESC, 1`, fromStr, toStr)
	if err != nil {
		return nil, fmt.Errorf("cash adjustments by reason: %w", err)
	}
	defer rows.Close()
	var out []CashAdjustmentReasonTotal
	for rows.Next() {
		var c CashAdjustmentReasonTotal
		if err := rows.Scan(&c.Reason, &c.Count, &c.Amount); err != nil {
			return nil, fmt.Errorf("scan cash adjustment reason total: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EODMethod is one payment method's day: taken in on sales, paid out on
// refunds (minor units).
type EODMethod struct {
	Method string `json:"method"`
	In     int64  `json:"in"`
	Out    int64  `json:"out"`
}

// CashReconciliation is the day-level cash-drawer reconciliation on the
// Z-report (ut-docs#1006, German pilot): every shift CLOSED on the shop-local
// day, summed. All amounts are minor units. Skim and PayOuts are stored as
// the (negative) amounts recorded — cash that left the drawer.
//
// CashSales is taken from EODMethod{Method:"cash"}.In (see dateRangeSummary)
// MINUS any cash tip_amount for the day (ut-docs#1046) — EODTip's own doc
// comment below explains why EODMethod.In on its own is the full tendered
// amount and so would otherwise include a CASH tip, commingling it into the
// drawer's expected cash exactly the way ut-docs#1007 already prevents for
// revenue. The subtracted amount is surfaced separately as TipsHeldOut so
// the two figures reconcile: CashSales + TipsHeldOut == EODMethod{cash}.In -
// EODMethod{cash}.Out. Cash tipping is off in the German pilot today (no
// current path writes tip_amount on a cash payment via the till UI), but
// nothing at the API layer rejects one, so this holds correct as soon as it
// is ever turned on rather than waiting to be noticed then.
type CashReconciliation struct {
	OpeningFloat int64 `json:"opening_float"`
	CashSales    int64 `json:"cash_sales"`
	// TipsHeldOut is the day's cash tip_amount, subtracted out of CashSales
	// above (ut-docs#1046) — zero on every day with no cash tips, which is
	// every day today. Held out of the drawer figure the same way #1007
	// holds tips out of revenue; not itself a separate stored component of
	// Calculated/Counted (those come from the shift's own opening/closing
	// cash counts, which already reflect whatever cash — tips included —
	// actually sat in the drawer). So the Z-report's printed CASH
	// RECONCILIATION block still visibly closes once cash tipping is on:
	// OpeningFloat + CashSales + TipsHeldOut + PayIns + PayOuts ==
	// Calculated always (ut-docs#1124, ut-docs#1146; regression-tested in
	// TestEndOfDay_CashReconciliation_ExcludesCashTips and
	// TestCashReconciliationForLocalDay_MidShiftSkimIncludedInPayOuts). Skim
	// (below) is deliberately NOT part of that sum -- it reports only a
	// CLOSE-TIME skim (pos.CloseShift's own skim-to-safe field, the only
	// path the shipped operator UI offers: shifts.html's mid-shift
	// adjustment form has no "skim" option). expected_cash is computed and
	// persisted BEFORE that close-time skim audit row is written, so it
	// never factors into Calculated.
	//
	// CashReconciliationForLocalDay identifies a close-time skim by an
	// explicit `at_close: true` marker CloseShift stamps into that audit
	// row's payload (ut-docs#1146 review finding F1) -- NOT by comparing
	// timestamps. An earlier version of this fix tried to infer it from
	// the skim row's created_at exactly matching the shift's own
	// closed_at (both come from the same `now` in CloseShift, in the same
	// transaction), but that string match is only second-precision
	// (time.RFC3339): a skim recorded WHILE THE SHIFT IS STILL OPEN (via
	// pos.RecordCashAdjustment, Type:"skim" -- not reachable via the
	// shipped UI, but not guarded at the API layer either, and a
	// deliberately valid mid-shift adjustment type per
	// TestRecordCashAdjustment_SkimType) landing in the same wall-clock
	// SECOND as its own shift's close would collide with that timestamp
	// and be misclassified right back into the #1146 bug. The explicit
	// flag has no such race: pos.RecordCashAdjustment never sets it, so a
	// mid-shift skim is unambiguous regardless of timing, and
	// CashReconciliationForLocalDay buckets anything without it into
	// PayIns/PayOuts by sign, same as any other adjustment, keeping the
	// identity whole. This intentionally does NOT fall back to the
	// timestamp match for a skim row that predates this flag (there is no
	// real production data yet to protect, matching this pipeline's
	// standing "no real users yet" auto-push authorization) -- a schema
	// migration would be the right fix if that ever stops being true.
	TipsHeldOut  int64 `json:"tips_held_out"`
	PayIns       int64 `json:"pay_ins"`    // sum of positive adjustments, excluding a close-time skim (a mid-shift skim counts as a payout, see Skim below)
	PayOuts      int64 `json:"pay_outs"`   // sum of negative adjustments, excluding a close-time skim (a mid-shift skim counts as a payout, see Skim below)
	Calculated   int64 `json:"calculated"` // sum of each closed shift's expected_cash
	Counted      int64 `json:"counted"`    // sum of each closed shift's closing_cash
	Variance     int64 `json:"variance"`   // Counted - Calculated
	Skim         int64 `json:"skim"`       // sum of CLOSE-TIME skim adjustments only (negative value) -- a mid-shift skim is in PayIns/PayOuts instead, see the doc comment above
	NewFloat     int64 `json:"new_float"`  // sum of each closed shift's new_float (fallback closing_cash)
	ShiftsClosed int   `json:"shifts_closed"`
}

// EODTip is one payment method's tips for the day (minor units), held OUT
// of revenue (ut-docs#1007): the reference day-close reports tips by
// payment method, separately from revenue, and posts them to their own
// ledger account rather than a revenue account. NOTE: EODMethod.In is the
// full tendered amount (sale + tip, per InsertPayment's own convention —
// payments.amount already includes any tip_amount, see
// internal/pos/sales_test.go's tip round-trip coverage), so a card
// method's In DOES include its tips -- Tips is an additional breakdown
// alongside Methods, not a carve-out from it. What tips are genuinely
// excluded from is revenue: Gross/Net/TaxNet come from sale/sale_lines
// totals, never from payments, so a payment's tip_amount can never
// inflate them by construction -- see dateRangeSummary's doc comment on
// the tips query. Only methods with at least one tipped payment appear
// (mirrors the EODMethod query's own convention of one row per method
// actually seen,
// not a zero row for every configured method) -- a card terminal with
// tipping disabled and zero cash tips legitimately produces no rows at
// all for a given day.
type EODTip struct {
	Method string `json:"method"`
	Count  int    `json:"count"`
	Amount int64  `json:"amount"`
}

// EODReport is the classic Z-report for one business day, or — when From/To
// are set instead of Day — a date-ranged summary spanning multiple days.
type EODReport struct {
	Day         string `json:"day,omitempty"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	SalesCount  int    `json:"sales_count"`
	Gross       int64  `json:"gross"`
	RefundCount int    `json:"refund_count"`
	RefundTotal int64  `json:"refund_total"`
	// CancelCount/CancelTotal (ut-docs#1012) are sales VOIDED (status =
	// 'voided') in this window — a completed sale later cancelled/
	// reversed (a "Storno" of a receipt), distinct from RefundCount/
	// RefundTotal above (a formal return processed afterward — a
	// "Retoure"). This is NOT a pre-tender abandoned-basket count: a
	// live sale in this till lives only in memory until CompleteSale
	// (the tender path, the only writer of a 'sales' row — see
	// dateRangeSummary's own doc comment on this query) runs, or in
	// held_sales if explicitly parked, so there is nothing in the
	// 'sales' table to count before completion. A voided sale is
	// excluded from Gross/Net entirely (dateRangeSummary only scans
	// status = 'completed'), so this is purely informational — it never
	// participates in the Net calculation below.
	CancelCount int   `json:"cancel_count"`
	CancelTotal int64 `json:"cancel_total"`
	Net         int64 `json:"net"`
	TaxNet      int64 `json:"tax_net"`
	// TaxBands is the per-VAT-rate net/tax/gross breakdown (ut-docs#1003).
	// Filled by internal/pages' attachEODTaxBands, NOT by EndOfDay/
	// EndOfDayRange themselves — the banding math (discount proration +
	// ADR-0061 service-charge apportionment) lives in internal/pos, which
	// this package cannot import. See dateRangeSummary's inline note.
	TaxBands []TaxBand `json:"tax_bands"`
	// MethodTaxBands is the payment-method x VAT-rate cross-tab
	// (ut-docs#1004) the DATEV/accounting export posting batch is generated
	// from. Filled by internal/pages' attachEODMethodTaxBands, NOT by
	// EndOfDay/EndOfDayRange themselves — same layering reason as TaxBands
	// above (the banding math lives in internal/pos, which this package
	// cannot import).
	MethodTaxBands []MethodTaxBand `json:"method_tax_bands"`
	Methods        []EODMethod     `json:"methods"`
	Tips           []EODTip        `json:"tips"`        // tips by payment method, held out of revenue (ut-docs#1007)
	Departments    []DeptSales     `json:"departments"` // per-department sales (E1b)
	Tills          []TillSales     `json:"tills"`       // per-register sales (multi-till)
	// Voucher liability flows (ut-docs#1008): count + amount (minor units) of
	// vouchers issued and redeemed in the window, from voucher_transactions.
	// Reported DISTINCTLY from article revenue: an issue is a 0% liability
	// already inside the sale's total (so inside Gross) but never inside any
	// sale_lines-derived figure (departments, per-rate VAT bands); a
	// redemption is a payment method, not revenue. Never sum these into an
	// Artikelumsatz figure.
	VouchersIssuedCount   int    `json:"vouchers_issued_count"`
	VouchersIssued        int64  `json:"vouchers_issued"`
	VouchersRedeemedCount int    `json:"vouchers_redeemed_count"`
	VouchersRedeemed      int64  `json:"vouchers_redeemed"`
	FirstReceipt          string `json:"first_receipt"`
	LastReceipt           string `json:"last_receipt"`
	GeneratedAt           string `json:"generated_at"`
	// GeneratedBy/Annotation (ut-docs#1012) are filled by internal/pages'
	// generateEOD, NOT by EndOfDay/EndOfDayRange themselves (same
	// layering convention as TaxBands/MethodTaxBands above — the actor
	// resolution and the annotation form value both come from the HTTP
	// handler layer, which this package cannot import). GeneratedBy is
	// the resolved operator display name ("system" for the unattended
	// scheduler tick); Annotation is an optional free-text note supplied
	// on a manual run. Both are embedded straight in content_json when
	// the report is archived — no separate report_archive column, same
	// as every other EODReport field that isn't one of the two
	// ut-docs#1080 queryable columns (first/last receipt).
	GeneratedBy string `json:"generated_by,omitempty"`
	Annotation  string `json:"annotation,omitempty"`
	// CashReconciliation is single-day only (same from==to gate as
	// Departments/Tills): nil on range reports, and nil when no shift was
	// closed that day — a day-close still completes without one.
	CashReconciliation *CashReconciliation `json:"cash_reconciliation,omitempty"`
}

// CashReconciliationForLocalDay aggregates the cash-drawer reconciliation
// for every shift closed on the shop-local calendar day (ADR-0057 —
// date(closed_at,'localtime'), the same convention dateRangeSummary uses
// for sales). Returns nil, nil when zero shifts were closed that day: EOD
// generation must never fail or block because nobody closed a shift
// (offline-first / never-block-day-close). CashSales is NOT filled here —
// the caller takes it from the report's own payment-method breakdown so
// the two figures can never disagree on the same report.
func (r *POSRepo) CashReconciliationForLocalDay(ctx context.Context, day string) (*CashReconciliation, error) {
	var rec CashReconciliation
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*),
  COALESCE(SUM(opening_cash), 0),
  COALESCE(SUM(closing_cash), 0),
  COALESCE(SUM(expected_cash), 0)
FROM shifts
WHERE closed_at IS NOT NULL AND date(closed_at, 'localtime') = date(?)`, day).
		Scan(&rec.ShiftsClosed, &rec.OpeningFloat, &rec.Counted, &rec.Calculated)
	if err != nil {
		return nil, fmt.Errorf("cash reconciliation shifts: %w", err)
	}
	if rec.ShiftsClosed == 0 {
		return nil, nil
	}
	rec.Variance = rec.Counted - rec.Calculated

	// New float is NOT additive across sequential shifts on the SAME
	// register the way Opening/Counted/Calculated are (ut-docs#1006 review
	// finding 3): a register that closed twice in one day physically holds
	// only its LAST close's new float, not the sum of both closes'. Sum
	// only the most-recent close per register (a window function, not a
	// second full-table scan) — this matches LastClosedShiftCarryForward,
	// which also only ever looks at the latest close per register.
	if err := r.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(new_float_or_closing), 0)
FROM (
  SELECT COALESCE(new_float, closing_cash) AS new_float_or_closing,
         ROW_NUMBER() OVER (PARTITION BY register_id ORDER BY closed_at DESC) AS rn
  FROM shifts
  WHERE closed_at IS NOT NULL AND date(closed_at, 'localtime') = date(?)
) WHERE rn = 1`, day).Scan(&rec.NewFloat); err != nil {
		return nil, fmt.Errorf("cash reconciliation new float: %w", err)
	}

	// Adjustments recorded against those shifts, split by declared type:
	// "skim" (cash moved to the safe) apart from ordinary pay-ins/pay-outs,
	// which split by sign — the same sign-over-label convention the
	// manager-PIN gate uses (a row with no type at all is treated as a
	// plain adjustment, not a skim). A "skim" row only counts as the
	// close-time skim (excluded from this sum, see CashReconciliation's own
	// doc comment) when it explicitly carries `at_close: true` — the
	// marker pos.CloseShift stamps on the skim row it writes at close, and
	// ONLY that path ever sets (ut-docs#1146 review finding F1: an earlier
	// version of this fix compared created_at to closed_at instead, but
	// that string match is only second-precision and could misclassify a
	// mid-shift skim landing in the same wall-clock second as its own
	// shift's close). A skim row without the flag — including one
	// recorded while the shift was still open, via
	// pos.RecordCashAdjustment, which never sets it — falls through to the
	// plain sign-based split below, same as any other mid-shift
	// adjustment, keeping Calculated and this sum in agreement regardless
	// of timing. COALESCE guards both the type and the flag check against
	// SQL's three-valued NULL logic: a missing `$.type` or `$.at_close`
	// key must resolve to a definite false, never NULL, or the row would
	// silently disappear from every bucket instead of landing in one.
	err = r.db.QueryRowContext(ctx, `
SELECT
  COALESCE(SUM(CASE WHEN COALESCE(json_extract(a.data_json, '$.type'), '') = 'skim'
                     AND COALESCE(json_extract(a.data_json, '$.at_close') = 1, 0)
                    THEN CAST(json_extract(a.data_json, '$.amount') AS INTEGER) END), 0),
  COALESCE(SUM(CASE WHEN NOT (COALESCE(json_extract(a.data_json, '$.type'), '') = 'skim'
                          AND COALESCE(json_extract(a.data_json, '$.at_close') = 1, 0))
                     AND CAST(json_extract(a.data_json, '$.amount') AS INTEGER) > 0
                    THEN CAST(json_extract(a.data_json, '$.amount') AS INTEGER) END), 0),
  COALESCE(SUM(CASE WHEN NOT (COALESCE(json_extract(a.data_json, '$.type'), '') = 'skim'
                          AND COALESCE(json_extract(a.data_json, '$.at_close') = 1, 0))
                     AND CAST(json_extract(a.data_json, '$.amount') AS INTEGER) < 0
                    THEN CAST(json_extract(a.data_json, '$.amount') AS INTEGER) END), 0)
FROM audit_log a
JOIN shifts s ON s.id = a.entity_id
WHERE a.entity_type = 'shift' AND a.action = 'cash_adjustment'
  AND s.closed_at IS NOT NULL AND date(s.closed_at, 'localtime') = date(?)`, day).
		Scan(&rec.Skim, &rec.PayIns, &rec.PayOuts)
	if err != nil {
		return nil, fmt.Errorf("cash reconciliation adjustments: %w", err)
	}
	return &rec, nil
}

// CashReconciliationForInstantWindow is CashReconciliationForLocalDay's
// close-to-close sibling (ADR-0066 Decision 2, ut-docs#1140): the same
// three-query aggregation (shift totals, latest-close-per-register new
// float, adjustments split), matched on shifts.closed_at over a half-open
// [from, to) INSTANT window instead of one local calendar day — see
// instantWindow's doc comment for the form and the zero-`from` unbounded
// case. Windowing shift CLOSURES by an arbitrary instant range is a genuine
// semantic shift the ADR calls out as intended, not a bug: a shift closed
// at 19:25 after a 19:19 EOD close falls into the NEXT Z-Bon's
// reconciliation, because its close is after this period ended. Same
// nil, nil on zero shifts closed in the window: EOD generation must never
// fail or block because nobody closed a shift (offline-first /
// never-block-day-close), and CashSales is likewise NOT filled here — the
// caller takes it from the report's own payment-method breakdown so the two
// figures can never disagree on the same report.
func (r *POSRepo) CashReconciliationForInstantWindow(ctx context.Context, from, to time.Time) (*CashReconciliation, error) {
	win, args := instantWindow("closed_at", from, to)
	var rec CashReconciliation
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*),
  COALESCE(SUM(opening_cash), 0),
  COALESCE(SUM(closing_cash), 0),
  COALESCE(SUM(expected_cash), 0)
FROM shifts
WHERE closed_at IS NOT NULL AND `+win, args...).
		Scan(&rec.ShiftsClosed, &rec.OpeningFloat, &rec.Counted, &rec.Calculated)
	if err != nil {
		return nil, fmt.Errorf("cash reconciliation shifts (instant): %w", err)
	}
	if rec.ShiftsClosed == 0 {
		return nil, nil
	}
	rec.Variance = rec.Counted - rec.Calculated

	// Same latest-close-per-register rule as CashReconciliationForLocalDay
	// (ut-docs#1006 review finding 3): a register that closed twice in one
	// window physically holds only its LAST close's new float.
	if err := r.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(new_float_or_closing), 0)
FROM (
  SELECT COALESCE(new_float, closing_cash) AS new_float_or_closing,
         ROW_NUMBER() OVER (PARTITION BY register_id ORDER BY closed_at DESC) AS rn
  FROM shifts
  WHERE closed_at IS NOT NULL AND `+win+`
) WHERE rn = 1`, args...).Scan(&rec.NewFloat); err != nil {
		return nil, fmt.Errorf("cash reconciliation new float (instant): %w", err)
	}

	// Adjustments against those shifts, split exactly as
	// CashReconciliationForLocalDay does (close-time skim apart via the
	// explicit at_close marker, ut-docs#1146 review finding F1; everything
	// else by sign) — only the shift-selection window differs.
	swin, sargs := instantWindow("s.closed_at", from, to)
	err = r.db.QueryRowContext(ctx, `
SELECT
  COALESCE(SUM(CASE WHEN COALESCE(json_extract(a.data_json, '$.type'), '') = 'skim'
                     AND COALESCE(json_extract(a.data_json, '$.at_close') = 1, 0)
                    THEN CAST(json_extract(a.data_json, '$.amount') AS INTEGER) END), 0),
  COALESCE(SUM(CASE WHEN NOT (COALESCE(json_extract(a.data_json, '$.type'), '') = 'skim'
                          AND COALESCE(json_extract(a.data_json, '$.at_close') = 1, 0))
                     AND CAST(json_extract(a.data_json, '$.amount') AS INTEGER) > 0
                    THEN CAST(json_extract(a.data_json, '$.amount') AS INTEGER) END), 0),
  COALESCE(SUM(CASE WHEN NOT (COALESCE(json_extract(a.data_json, '$.type'), '') = 'skim'
                          AND COALESCE(json_extract(a.data_json, '$.at_close') = 1, 0))
                     AND CAST(json_extract(a.data_json, '$.amount') AS INTEGER) < 0
                    THEN CAST(json_extract(a.data_json, '$.amount') AS INTEGER) END), 0)
FROM audit_log a
JOIN shifts s ON s.id = a.entity_id
WHERE a.entity_type = 'shift' AND a.action = 'cash_adjustment'
  AND s.closed_at IS NOT NULL AND `+swin, sargs...).
		Scan(&rec.Skim, &rec.PayIns, &rec.PayOuts)
	if err != nil {
		return nil, fmt.Errorf("cash reconciliation adjustments (instant): %w", err)
	}
	return &rec, nil
}

// EndOfDay aggregates one day's completed sales and returns.
func (r *POSRepo) EndOfDay(ctx context.Context, day string) (EODReport, error) {
	rep, err := r.dateRangeSummary(ctx, day, day)
	rep.Day = day
	return rep, err
}

// EndOfDayRange aggregates completed sales over [from, to] inclusive of both
// ends (ut-docs#57) — the ad-hoc, on-demand counterpart to EndOfDay's
// single-day scheduled/archived report. Unlike EndOfDay this is never
// archived or auto-printed; a caller downloads the result directly.
func (r *POSRepo) EndOfDayRange(ctx context.Context, from, to string) (EODReport, error) {
	rep, err := r.dateRangeSummary(ctx, from, to)
	rep.From, rep.To = from, to
	return rep, err
}

// dateRangeSummary is the shared aggregation body behind EndOfDay and
// EndOfDayRange. date(created_at, 'localtime') BETWEEN date(?) AND date(?)
// is equivalent to date(created_at, 'localtime') = date(?) when from == to,
// so EndOfDay's behavior (and its existing tests) are unaffected by sharing
// this with the range query. Every from/to comparison in this function
// (and DepartmentsForDay's) wraps its RHS in date(...) too, even though
// from/to always arrive as canonical "YYYY-MM-DD" text — one consistent
// convention across all four fragments this file uses for a "day" argument,
// rather than three bare and one wrapped.
//
// from/to are matched on the shop's LOCAL calendar day (ut-docs#869) — the
// same convention DayTotal and ListSalesJournal's Day filter already use
// (ut-docs#774/PR#417), NOT SalesByDay's business-day-start shift (a
// different semantic for trading-night merging, out of scope here — see
// ADR-0057). This matters because eodSchedulerTick (eod_api.go) computes
// its day argument from Go's local wall-clock time.Now(): before this
// fix, that local calendar day was being matched against a bare UTC
// date(created_at), so on any non-UTC host the scheduled/archived Z-report
// silently aggregated the wrong calendar day's transactions. This only
// changes report generation going forward — already-archived
// report_archive rows are effectively immutable: ArchiveReport's
// ON CONFLICT (kind, period) DO NOTHING makes each (kind, period) a
// write-once row, so this fix cannot retroactively alter a report already
// generated.
func (r *POSRepo) dateRangeSummary(ctx context.Context, from, to string) (EODReport, error) {
	rep := EODReport{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	err := r.db.QueryRowContext(ctx, `
SELECT
  COALESCE(SUM(CASE WHEN sale_type = 'sale'   THEN 1 END), 0),
  COALESCE(SUM(CASE WHEN sale_type = 'sale'   THEN total END), 0),
  COALESCE(SUM(CASE WHEN sale_type = 'return' THEN 1 END), 0),
  COALESCE(SUM(CASE WHEN sale_type = 'return' THEN total END), 0),
  COALESCE(SUM(CASE WHEN sale_type = 'sale' THEN tax_total ELSE -tax_total END), 0),
  COALESCE(MIN(receipt_no), ''), COALESCE(MAX(receipt_no), '')
FROM sales
WHERE status = 'completed' AND date(created_at, 'localtime') BETWEEN date(?) AND date(?)`,
		from, to).Scan(&rep.SalesCount, &rep.Gross, &rep.RefundCount, &rep.RefundTotal,
		&rep.TaxNet, &rep.FirstReceipt, &rep.LastReceipt)
	if err != nil {
		return rep, fmt.Errorf("eod totals: %w", err)
	}
	rep.Net = rep.Gross - rep.RefundTotal

	// Cancellations (ut-docs#1012) — a completed sale later voided/
	// reversed (a "Storno"), a separate scan from the totals query above
	// because a voided sale's status is 'voided', never 'completed', so
	// it never matches that query's WHERE at all. This is NOT a
	// pre-tender abandoned-basket count: see CancelCount's own doc
	// comment on EODReport for why the 'sales' table has nothing to
	// count before a sale completes. Matched on voided_at's local
	// calendar day (not created_at): a sale can complete one day and be
	// voided the next, and the cancellation as an audit event belongs to
	// the day it was actually cancelled. COALESCE(voided_at, created_at)
	// makes a legacy/hand-inserted row with a NULL voided_at fail
	// visible (it still lands on SOME day) rather than silently
	// vanishing from every window's count — every row this repo itself
	// writes always stamps voided_at (UpdateSaleStatus's own CASE WHEN),
	// so this only guards a row this codebase didn't create. Never
	// folded into Gross/Net/RefundTotal above — a voided sale carries no
	// revenue and this is purely an informational count/total, same as
	// the reference day-close's separate "Stornos" column.
	err = r.db.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(SUM(total), 0)
FROM sales
WHERE status = 'voided' AND date(COALESCE(voided_at, created_at), 'localtime') BETWEEN date(?) AND date(?)`,
		from, to).Scan(&rep.CancelCount, &rep.CancelTotal)
	if err != nil {
		return rep, fmt.Errorf("eod cancellations: %w", err)
	}

	// Voucher flows (ut-docs#1008) — same local-calendar-day window as the
	// totals query above, range-capable like Methods (not gated on from==to).
	vouchers, err := r.VouchersIssuedRedeemedForRange(ctx, from, to)
	if err != nil {
		return rep, fmt.Errorf("eod vouchers: %w", err)
	}
	rep.VouchersIssuedCount = vouchers.IssuedCount
	rep.VouchersIssued = vouchers.IssuedMinor
	rep.VouchersRedeemedCount = vouchers.RedeemedCount
	rep.VouchersRedeemed = vouchers.RedeemedMinor

	rows, err := r.db.QueryContext(ctx, `
SELECT p.method_id,
  COALESCE(SUM(CASE WHEN s.sale_type = 'sale'   THEN p.amount - p.change_given END), 0),
  COALESCE(SUM(CASE WHEN s.sale_type = 'return' THEN p.amount - p.change_given END), 0)
FROM payments p
JOIN sales s ON s.id = p.sale_id
WHERE s.status = 'completed' AND date(s.created_at, 'localtime') BETWEEN date(?) AND date(?)
GROUP BY p.method_id ORDER BY 2 DESC`, from, to)
	if err != nil {
		return rep, fmt.Errorf("eod methods: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m EODMethod
		if err := rows.Scan(&m.Method, &m.In, &m.Out); err != nil {
			return rep, fmt.Errorf("scan eod method: %w", err)
		}
		rep.Methods = append(rep.Methods, m)
	}
	if err := rows.Err(); err != nil {
		return rep, err
	}

	// Tips by payment method (ut-docs#1007), held OUT of revenue: a
	// separate query on the same join/date-range shape as the EODMethod
	// query above, but summing payments.tip_amount instead of the
	// tendered `amount` column — tip_amount is a distinct payments column
	// (migration 019_payment_tip_amount.sql; tip_recipient, not summed
	// here, came later in migration 061 per ADR-0061), never folded into
	// a sale's total (sale.total/tax_total come from sale_lines, not
	// payments), so this can only ever ADD a Tips entry, never change
	// Gross/Net/TaxNet — note it does NOT mean tips are absent from
	// EODMethod.In, which is the full tendered amount and already
	// includes any tip (see the EODTip doc comment above). p.tip_amount
	// > 0 keeps a method with no tipped payments this period out of the
	// slice entirely, matching the EODMethod query's own "one row per
	// method actually seen" convention. No sale_type restriction: today
	// every tipped payment is a completeTender 'sale' row (the refund
	// path never sets TipAmount), so this is equivalent to EODMethod's
	// own split in practice; if a returned sale ever carries its own
	// tipped payment, this query would add it rather than net it the way
	// EODMethod.Out does — revisit then, not speculatively now.
	tipRows, err := r.db.QueryContext(ctx, `
SELECT p.method_id, COUNT(*), COALESCE(SUM(p.tip_amount), 0)
FROM payments p
JOIN sales s ON s.id = p.sale_id
WHERE p.tip_amount > 0 AND s.status = 'completed' AND date(s.created_at, 'localtime') BETWEEN date(?) AND date(?)
GROUP BY p.method_id ORDER BY p.method_id`, from, to)
	if err != nil {
		return rep, fmt.Errorf("eod tips: %w", err)
	}
	defer tipRows.Close()
	for tipRows.Next() {
		var tp EODTip
		if err := tipRows.Scan(&tp.Method, &tp.Count, &tp.Amount); err != nil {
			return rep, fmt.Errorf("scan eod tip: %w", err)
		}
		rep.Tips = append(rep.Tips, tp)
	}
	if err := tipRows.Err(); err != nil {
		return rep, err
	}

	// Per-VAT-rate breakdown (ut-docs#1003): NOT computed here. rep.TaxBands
	// is filled by internal/pages' attachEODTaxBands (eod_tax_bands.go),
	// which feeds SalesForTaxBands (below — the same local-calendar-day
	// window as this function) through the shared per-sale banding in
	// internal/pos (pos.VATBandsForSale). A pure SQL aggregation over
	// sale_lines was tried first and silently dropped two sale-level
	// amounts that have no sale_lines row — the service charge's tax
	// (ADR-0061) and the whole-sale discount — so the band sums broke the
	// Z-report's own identities on any sale carrying either. The correct
	// math needs internal/pos, which this package cannot import
	// (internal/pos already imports internal/data), so the computation
	// lives one layer up.

	// Department and per-till breakdowns are single-day only for now, both
	// gated on the same from==to check — DepartmentsForDay is a day-scoped
	// helper (generalizing it to a range is out of this cycle's scope), and
	// tills is kept consistent with it rather than silently populating one
	// breakdown but not the other on a range report (2026-08-02 review
	// finding: an asymmetric partial breakdown reads as "no data" for
	// whichever one is empty, indistinguishable from a genuine zero).
	if from == to {
		if depts, err := r.DepartmentsForDay(ctx, from); err == nil {
			rep.Departments = depts
		}
		// Cash-drawer reconciliation (ut-docs#1006) — single-day only, like
		// the breakdowns around it (summing counted-vs-calculated across a
		// multi-day range would be misleading). Best-effort on the same
		// swallow-the-error pattern as Departments: a reconciliation query
		// failure must not sink the whole Z-report (day-close still
		// completes; the section is simply absent, same as a day with no
		// closed shift).
		if rc, rcErr := r.CashReconciliationForLocalDay(ctx, from); rcErr == nil && rc != nil {
			// CashSales comes from the report's own payment-method
			// breakdown (net cash: sales in minus refunds out), so the two
			// figures on one report can never disagree.
			for _, m := range rep.Methods {
				if m.Method == "cash" {
					rc.CashSales = m.In - m.Out
					break
				}
			}
			// Hold cash tips out of CashSales (ut-docs#1046), same as #1007
			// already holds every method's tips out of revenue — rep.Tips
			// is populated above from the same tip_amount query, keyed by
			// method_id, so this is the report's own cash-tip figure, not a
			// separate lookup that could disagree with it.
			for _, tp := range rep.Tips {
				if tp.Method == "cash" {
					rc.TipsHeldOut = tp.Amount
					rc.CashSales -= tp.Amount
					break
				}
			}
			rep.CashReconciliation = rc
		}
	}

	// Per-till (register) breakdown — only meaningful with >1 till, so it's
	// left empty for single-register shops (and, per the comment above, for
	// any multi-day range).
	var tillRows *sql.Rows
	if from == to {
		tillRows, err = r.db.QueryContext(ctx, `
SELECT s.till_id, COALESCE(t.name, ''), COUNT(*), COALESCE(SUM(s.total), 0)
FROM sales s
LEFT JOIN tills t ON t.id = s.till_id
WHERE s.status = 'completed' AND s.sale_type = 'sale' AND date(s.created_at, 'localtime') = date(?)
GROUP BY s.till_id ORDER BY 4 DESC`, from)
	} else {
		return rep, nil
	}
	if err != nil {
		return rep, fmt.Errorf("eod tills: %w", err)
	}
	defer tillRows.Close()
	var tills []TillSales
	for tillRows.Next() {
		var ts TillSales
		if err := tillRows.Scan(&ts.TillID, &ts.Name, &ts.Count, &ts.Revenue); err != nil {
			return rep, fmt.Errorf("scan eod till: %w", err)
		}
		tills = append(tills, ts)
	}
	if err := tillRows.Err(); err != nil {
		return rep, err
	}
	if len(tills) > 1 {
		rep.Tills = tills
	}
	return rep, nil
}

// dateRangeSummaryInstant is dateRangeSummary's close-to-close sibling
// (ADR-0066 Decision 2, ut-docs#1140), the aggregation body behind the
// "eod" kind's archived/printed/Z-numbered report once its window becomes
// [previous close, this close). Same aggregation shape, but every
// date(..., 'localtime') BETWEEN date(?) AND date(?) fragment becomes the
// half-open datetime(...) INSTANT compare — see instantWindow's doc comment
// for the form and the zero-`from` (till's first-ever close, Decision 3)
// unbounded case. Deliberately a genuinely parallel query body, NOT a
// refactor of dateRangeSummary into shared code: that function stays
// calendar-day for EndOfDay/EndOfDayRange, and the ADR is explicit that a
// wrapper formatting the instants back into date strings would silently
// re-introduce calendar-day bucketing.
//
// Two deliberate differences from dateRangeSummary beyond the comparison
// form:
//   - The cancellations query still windows on COALESCE(voided_at,
//     created_at) — a sale completed one day and voided the next belongs,
//     as a Storno, to the close in which it was VOIDED. Only the comparison
//     form changes; the column choice is an existing decision the ADR does
//     not revisit.
//   - Departments, cash reconciliation and the per-till breakdown are
//     ALWAYS computed — no from==to gate. That gate exists in
//     dateRangeSummary only because it is shared with EndOfDayRange's
//     multi-day ranges; the "eod" kind has exactly one instant window per
//     close, the moral equivalent of the single-day path, so the gate has
//     nothing to guard here. (Tills still only populate with >1 register,
//     same as the single-day path.)
//
// TaxBands/MethodTaxBands are NOT computed here (same layering as
// dateRangeSummary — the banding math lives in internal/pos, one layer up);
// the "eod" generation path must feed them from SalesForTaxBandsInstant
// directly, never through the rep.Day == "" fallback to the date-string
// SalesForTaxBands (ADR-0066 Decision 6: date(...) parses an RFC3339
// timestamp without error, so that fallback would silently degrade to
// calendar-day banding).
func (r *POSRepo) dateRangeSummaryInstant(ctx context.Context, from, to time.Time) (EODReport, error) {
	rep := EODReport{GeneratedAt: time.Now().UTC().Format(time.RFC3339)}
	win, args := instantWindow("created_at", from, to)
	err := r.db.QueryRowContext(ctx, `
SELECT
  COALESCE(SUM(CASE WHEN sale_type = 'sale'   THEN 1 END), 0),
  COALESCE(SUM(CASE WHEN sale_type = 'sale'   THEN total END), 0),
  COALESCE(SUM(CASE WHEN sale_type = 'return' THEN 1 END), 0),
  COALESCE(SUM(CASE WHEN sale_type = 'return' THEN total END), 0),
  COALESCE(SUM(CASE WHEN sale_type = 'sale' THEN tax_total ELSE -tax_total END), 0),
  COALESCE(MIN(receipt_no), ''), COALESCE(MAX(receipt_no), '')
FROM sales
WHERE status = 'completed' AND `+win, args...).
		Scan(&rep.SalesCount, &rep.Gross, &rep.RefundCount, &rep.RefundTotal,
			&rep.TaxNet, &rep.FirstReceipt, &rep.LastReceipt)
	if err != nil {
		return rep, fmt.Errorf("eod instant totals: %w", err)
	}
	rep.Net = rep.Gross - rep.RefundTotal

	// Cancellations (ut-docs#1012) — same separate 'voided' scan as
	// dateRangeSummary (see its inline comment and CancelCount's doc
	// comment on EODReport), windowed on COALESCE(voided_at, created_at)
	// per the note above.
	vwin, vargs := instantWindow("COALESCE(voided_at, created_at)", from, to)
	err = r.db.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(SUM(total), 0)
FROM sales
WHERE status = 'voided' AND `+vwin, vargs...).Scan(&rep.CancelCount, &rep.CancelTotal)
	if err != nil {
		return rep, fmt.Errorf("eod instant cancellations: %w", err)
	}

	// Voucher flows (ut-docs#1008) — the instant sibling lives in
	// voucher_repo.go, called out explicitly by ADR-0066 precisely because
	// it is easy to miss next to the fragments inline in this function.
	vouchers, err := r.VouchersIssuedRedeemedForInstantWindow(ctx, from, to)
	if err != nil {
		return rep, fmt.Errorf("eod instant vouchers: %w", err)
	}
	rep.VouchersIssuedCount = vouchers.IssuedCount
	rep.VouchersIssued = vouchers.IssuedMinor
	rep.VouchersRedeemedCount = vouchers.RedeemedCount
	rep.VouchersRedeemed = vouchers.RedeemedMinor

	mwin, margs := instantWindow("s.created_at", from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT p.method_id,
  COALESCE(SUM(CASE WHEN s.sale_type = 'sale'   THEN p.amount - p.change_given END), 0),
  COALESCE(SUM(CASE WHEN s.sale_type = 'return' THEN p.amount - p.change_given END), 0)
FROM payments p
JOIN sales s ON s.id = p.sale_id
WHERE s.status = 'completed' AND `+mwin+`
GROUP BY p.method_id ORDER BY 2 DESC`, margs...)
	if err != nil {
		return rep, fmt.Errorf("eod instant methods: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m EODMethod
		if err := rows.Scan(&m.Method, &m.In, &m.Out); err != nil {
			return rep, fmt.Errorf("scan eod instant method: %w", err)
		}
		rep.Methods = append(rep.Methods, m)
	}
	if err := rows.Err(); err != nil {
		return rep, err
	}

	// Tips by payment method (ut-docs#1007), held OUT of revenue — same
	// query shape and conventions as dateRangeSummary's tips query (see its
	// inline comment), instant-windowed.
	tipRows, err := r.db.QueryContext(ctx, `
SELECT p.method_id, COUNT(*), COALESCE(SUM(p.tip_amount), 0)
FROM payments p
JOIN sales s ON s.id = p.sale_id
WHERE p.tip_amount > 0 AND s.status = 'completed' AND `+mwin+`
GROUP BY p.method_id ORDER BY p.method_id`, margs...)
	if err != nil {
		return rep, fmt.Errorf("eod instant tips: %w", err)
	}
	defer tipRows.Close()
	for tipRows.Next() {
		var tp EODTip
		if err := tipRows.Scan(&tp.Method, &tp.Count, &tp.Amount); err != nil {
			return rep, fmt.Errorf("scan eod instant tip: %w", err)
		}
		rep.Tips = append(rep.Tips, tp)
	}
	if err := tipRows.Err(); err != nil {
		return rep, err
	}

	// Departments and cash reconciliation: always computed (see the doc
	// comment above), best-effort on the same swallow-the-error pattern as
	// dateRangeSummary — a breakdown query failure must not sink the whole
	// Z-report (day-close still completes; the section is simply absent).
	if depts, err := r.DepartmentsForInstantWindow(ctx, from, to); err == nil {
		rep.Departments = depts
	}
	if rc, rcErr := r.CashReconciliationForInstantWindow(ctx, from, to); rcErr == nil && rc != nil {
		// CashSales comes from the report's own payment-method breakdown
		// (net cash: sales in minus refunds out), so the two figures on one
		// report can never disagree.
		for _, m := range rep.Methods {
			if m.Method == "cash" {
				rc.CashSales = m.In - m.Out
				break
			}
		}
		// Hold cash tips out of CashSales (ut-docs#1046), from the report's
		// own Tips figure — same as dateRangeSummary.
		for _, tp := range rep.Tips {
			if tp.Method == "cash" {
				rc.TipsHeldOut = tp.Amount
				rc.CashSales -= tp.Amount
				break
			}
		}
		rep.CashReconciliation = rc
	}

	// Per-till (register) breakdown — always computed for the "eod" kind
	// (see the doc comment above), still only populated with >1 till, same
	// as the single-day path.
	tillRows, err := r.db.QueryContext(ctx, `
SELECT s.till_id, COALESCE(t.name, ''), COUNT(*), COALESCE(SUM(s.total), 0)
FROM sales s
LEFT JOIN tills t ON t.id = s.till_id
WHERE s.status = 'completed' AND s.sale_type = 'sale' AND `+mwin+`
GROUP BY s.till_id ORDER BY 4 DESC`, margs...)
	if err != nil {
		return rep, fmt.Errorf("eod instant tills: %w", err)
	}
	defer tillRows.Close()
	var tills []TillSales
	for tillRows.Next() {
		var ts TillSales
		if err := tillRows.Scan(&ts.TillID, &ts.Name, &ts.Count, &ts.Revenue); err != nil {
			return rep, fmt.Errorf("scan eod instant till: %w", err)
		}
		tills = append(tills, ts)
	}
	if err := tillRows.Err(); err != nil {
		return rep, err
	}
	if len(tills) > 1 {
		rep.Tills = tills
	}
	return rep, nil
}

// EODTaxBandLine is one sale line's input to the day-close VAT banding
// (ut-docs#1003): the recorded rate, tax and tax-inclusive line total —
// the same three figures data.SaleDetailLine carries for the invoice's
// VAT table (LineTotal is sale_lines.total_after_tax).
type EODTaxBandLine struct {
	RateBP    int
	TaxAmount int64
	LineTotal int64
}

// EODTaxBandSale is one completed sale in the day-close window with
// exactly the header fields the per-sale VAT banding needs: the pricing-
// mode inference (pos.InferTaxInclusive) reads Subtotal/DiscountTotal/
// TaxTotal/Total/ServiceCharge/VoucherIssueTotal, the banding itself prorates DiscountTotal
// and apportions ServiceCharge at ServiceChargeTaxBasisBP, and SaleType
// decides the sign ('return' subtracts).
type EODTaxBandSale struct {
	ID                      string
	SaleType                string
	Subtotal                int64
	DiscountTotal           int64
	TaxTotal                int64
	Total                   int64
	ServiceCharge           int64
	ServiceChargeTaxBasisBP int
	// VoucherIssueTotal (ut-docs#1008 review F1, migration 069): read
	// solely so pos.InferTaxInclusive can balance its identity for a sale
	// that also issued vouchers — the banding itself never places voucher
	// face value in any band (a 0% liability, not a taxable supply).
	VoucherIssueTotal int64
	Lines             []EODTaxBandLine
	// Payments (ut-docs#1004): the sale's tendered revenue per method, for
	// the method x VAT-rate cross-tab's apportionment. Ordered by method_id
	// within the sale (the query's ORDER BY), which the apportionment
	// relies on for a stable "last payment takes the remainder" rule.
	Payments []EODTaxBandPayment
}

// EODTaxBandPayment is one payment's tendered REVENUE share for a sale
// (ut-docs#1004): amount minus change given minus tip — tips are
// deliberately excluded here (they carry no VAT rate; see
// eod_method_tax_bands.go's doc comment for why this makes
// MethodTaxBand's column totals reconcile to EODMethod.In minus that
// method's EODTip amount, not to EODMethod.In directly, whenever a
// method carries tips).
type EODTaxBandPayment struct {
	Method string
	Amount int64
}

// SalesForTaxBands loads every completed sale (and return) in the SAME
// local-calendar-day window dateRangeSummary aggregates — date(created_at,
// 'localtime') BETWEEN date(from) AND date(to), ut-docs#869 — with the
// per-line figures the day-close VAT banding needs. The caller
// (internal/pages' attachEODTaxBands) runs each sale through the shared
// pos.VATBandsForSale; the math cannot live here because internal/data
// cannot import internal/pos (see dateRangeSummary's inline note).
//
// Zero-value "note" lines (total_before_tax = total_after_tax = 0,
// arbitrary tax_rate_bp) are excluded at the query so they can't invent a
// spurious band — same exclusion the previous SQL-aggregate banding had; a
// real 0%-rate line has nonzero money and keeps its band. Three fixed
// queries regardless of sale count (no N+1); lines and payments are
// grouped per sale in Go.
func (r *POSRepo) SalesForTaxBands(ctx context.Context, from, to string) ([]EODTaxBandSale, error) {
	saleRows, err := r.db.QueryContext(ctx, `
SELECT id, sale_type, subtotal, discount_total, tax_total, total,
       service_charge_amount, service_charge_tax_basis_bp, voucher_issue_total
FROM sales
WHERE status = 'completed' AND date(created_at, 'localtime') BETWEEN date(?) AND date(?)
ORDER BY created_at, id`, from, to)
	if err != nil {
		return nil, fmt.Errorf("eod band sales: %w", err)
	}
	defer saleRows.Close()
	var out []EODTaxBandSale
	idx := map[string]int{}
	for saleRows.Next() {
		var s EODTaxBandSale
		if err := saleRows.Scan(&s.ID, &s.SaleType, &s.Subtotal, &s.DiscountTotal,
			&s.TaxTotal, &s.Total, &s.ServiceCharge, &s.ServiceChargeTaxBasisBP, &s.VoucherIssueTotal); err != nil {
			return nil, fmt.Errorf("scan eod band sale: %w", err)
		}
		idx[s.ID] = len(out)
		out = append(out, s)
	}
	if err := saleRows.Err(); err != nil {
		return nil, err
	}

	lineRows, err := r.db.QueryContext(ctx, `
SELECT sl.sale_id, COALESCE(sl.tax_rate_bp, 0), sl.tax_amount, sl.total_after_tax
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
WHERE s.status = 'completed' AND date(s.created_at, 'localtime') BETWEEN date(?) AND date(?)
  AND (sl.total_before_tax != 0 OR sl.total_after_tax != 0)
ORDER BY sl.sale_id, sl.line_no`, from, to)
	if err != nil {
		return nil, fmt.Errorf("eod band lines: %w", err)
	}
	defer lineRows.Close()
	for lineRows.Next() {
		var saleID string
		var l EODTaxBandLine
		if err := lineRows.Scan(&saleID, &l.RateBP, &l.TaxAmount, &l.LineTotal); err != nil {
			return nil, fmt.Errorf("scan eod band line: %w", err)
		}
		if i, ok := idx[saleID]; ok {
			out[i].Lines = append(out[i].Lines, l)
		}
	}
	if err := lineRows.Err(); err != nil {
		return nil, err
	}

	// Payments per sale (ut-docs#1004): tendered REVENUE only — change
	// given comes back off, and tip_amount (which InsertPayment folds into
	// `amount`, see the EODTip doc comment) is subtracted because a tip
	// carries no VAT rate and must not be apportioned into any band. The
	// ORDER BY method_id within each sale gives the apportionment a stable
	// payment order (its "last payment takes the remainder" rule).
	payRows, err := r.db.QueryContext(ctx, `
SELECT p.sale_id, p.method_id, COALESCE(SUM(p.amount - p.change_given - p.tip_amount), 0)
FROM payments p
JOIN sales s ON s.id = p.sale_id
WHERE s.status = 'completed' AND date(s.created_at, 'localtime') BETWEEN date(?) AND date(?)
GROUP BY p.sale_id, p.method_id ORDER BY p.sale_id, p.method_id`, from, to)
	if err != nil {
		return nil, fmt.Errorf("eod band payments: %w", err)
	}
	defer payRows.Close()
	for payRows.Next() {
		var saleID string
		var p EODTaxBandPayment
		if err := payRows.Scan(&saleID, &p.Method, &p.Amount); err != nil {
			return nil, fmt.Errorf("scan eod band payment: %w", err)
		}
		if i, ok := idx[saleID]; ok {
			out[i].Payments = append(out[i].Payments, p)
		}
	}
	return out, payRows.Err()
}

// SalesForTaxBandsInstant is SalesForTaxBands' close-to-close sibling
// (ADR-0066 Decision 2, ut-docs#1140): the same three fixed queries (sales
// header, non-zero lines, payments; no N+1, grouped per sale in Go) over a
// half-open [from, to) INSTANT window — see instantWindow's doc comment for
// the comparison form and the zero-`from` unbounded case. The "eod" kind's
// generation path must call this DIRECTLY with the time.Time window, never
// the rep.Day == "" fallback into the date-string SalesForTaxBands: SQLite's
// date(...) parses an RFC3339 timestamp without error, so that route would
// silently degrade to calendar-day banding on a close-to-close report
// (ADR-0066 Decision 6). Same zero-value "note" line exclusion as
// SalesForTaxBands so a rate-carrying zero-money line can't invent a
// spurious band.
func (r *POSRepo) SalesForTaxBandsInstant(ctx context.Context, from, to time.Time) ([]EODTaxBandSale, error) {
	win, args := instantWindow("created_at", from, to)
	saleRows, err := r.db.QueryContext(ctx, `
SELECT id, sale_type, subtotal, discount_total, tax_total, total,
       service_charge_amount, service_charge_tax_basis_bp, voucher_issue_total
FROM sales
WHERE status = 'completed' AND `+win+`
ORDER BY created_at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("eod instant band sales: %w", err)
	}
	defer saleRows.Close()
	var out []EODTaxBandSale
	idx := map[string]int{}
	for saleRows.Next() {
		var s EODTaxBandSale
		if err := saleRows.Scan(&s.ID, &s.SaleType, &s.Subtotal, &s.DiscountTotal,
			&s.TaxTotal, &s.Total, &s.ServiceCharge, &s.ServiceChargeTaxBasisBP, &s.VoucherIssueTotal); err != nil {
			return nil, fmt.Errorf("scan eod instant band sale: %w", err)
		}
		idx[s.ID] = len(out)
		out = append(out, s)
	}
	if err := saleRows.Err(); err != nil {
		return nil, err
	}

	swin, sargs := instantWindow("s.created_at", from, to)
	lineRows, err := r.db.QueryContext(ctx, `
SELECT sl.sale_id, COALESCE(sl.tax_rate_bp, 0), sl.tax_amount, sl.total_after_tax
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
WHERE s.status = 'completed' AND `+swin+`
  AND (sl.total_before_tax != 0 OR sl.total_after_tax != 0)
ORDER BY sl.sale_id, sl.line_no`, sargs...)
	if err != nil {
		return nil, fmt.Errorf("eod instant band lines: %w", err)
	}
	defer lineRows.Close()
	for lineRows.Next() {
		var saleID string
		var l EODTaxBandLine
		if err := lineRows.Scan(&saleID, &l.RateBP, &l.TaxAmount, &l.LineTotal); err != nil {
			return nil, fmt.Errorf("scan eod instant band line: %w", err)
		}
		if i, ok := idx[saleID]; ok {
			out[i].Lines = append(out[i].Lines, l)
		}
	}
	if err := lineRows.Err(); err != nil {
		return nil, err
	}

	// Payments per sale (ut-docs#1004): tendered REVENUE only — same change
	// and tip exclusions and the same stable method_id ordering as
	// SalesForTaxBands (see its inline comment).
	payRows, err := r.db.QueryContext(ctx, `
SELECT p.sale_id, p.method_id, COALESCE(SUM(p.amount - p.change_given - p.tip_amount), 0)
FROM payments p
JOIN sales s ON s.id = p.sale_id
WHERE s.status = 'completed' AND `+swin+`
GROUP BY p.sale_id, p.method_id ORDER BY p.sale_id, p.method_id`, sargs...)
	if err != nil {
		return nil, fmt.Errorf("eod instant band payments: %w", err)
	}
	defer payRows.Close()
	for payRows.Next() {
		var saleID string
		var p EODTaxBandPayment
		if err := payRows.Scan(&saleID, &p.Method, &p.Amount); err != nil {
			return nil, fmt.Errorf("scan eod instant band payment: %w", err)
		}
		if i, ok := idx[saleID]; ok {
			out[i].Payments = append(out[i].Payments, p)
		}
	}
	return out, payRows.Err()
}

// ArchiveReport stores a generated report; kind+period is unique so the
// scheduled job is idempotent. Returns false when it already existed.
//
// Each stored row also gets a sequential, gapless Z-number chained to its
// predecessor (ut-docs#1080): z_number = MAX(z_number)+1 within the kind,
// computed and inserted in ONE atomic INSERT...SELECT (same gapless-sequence
// idiom as InvoiceRepo.Create). Aggregates without GROUP BY always yield
// exactly one row, so on a till's very first close (zero matching rows)
// MAX(z_number) is NULL: COALESCE gives z_number=1 and prev_z_number/
// prev_closed_at stay NULL -- "no previous close" is stored as NULL, never a
// fake 0. The prev_closed_at subquery filters z_number IS NOT NULL so a
// pre-migration legacy row (never numbered) can't be picked up as the
// "previous close" while prev_z_number stays NULL.
//
// "Gapless" describes allocation: no number is ever skipped or handed out
// twice while the rows exist. Age-based retention still applies on top --
// PruneReportArchiveOlderThan (ADR-0040 §2) deletes old rows, so the
// surviving sequence can start above 1, and in the one pathological case
// where it removes every numbered row of a kind (a till dormant for the
// whole 10-year window) the next close restarts at 1.
//
// A duplicate (kind, period) is absorbed by DO NOTHING before any row is
// written, so it consumes no number and reports created=false, not an
// error. The 3-attempt retry (same shape as InvoiceRepo.Create) covers the
// one error class worth retrying: a lost race on the
// ux_report_archive_kind_znumber UNIQUE index, i.e. two closes that both
// computed the same MAX(z_number)+1. That race is defence in depth, not an
// observed failure -- ut-docs#1080's review measured it on the DSN this
// repo actually opens (internal/db.Open: WAL, busy_timeout 5000) and got
// zero lost races out of 60 concurrent single-attempt inserts, because an
// autocommit INSERT...SELECT takes SQLite's write lock before it evaluates
// the SELECT, so MAX() is read under that lock and concurrent closes
// serialise instead of racing. The loop is kept so the invariant survives
// a later change to that shape (an explicit deferred transaction, another
// driver). Errors of any other class -- I/O, busy-timeout expiry, a
// cancelled ctx, a schema mismatch -- are not masked by it: every attempt
// fails identically and the original error is returned wrapped after the
// third.
//
// closedAt (ADR-0066 Decisions 4 and 5, ut-docs#1140) is the close instant
// the caller's whole close is keyed on. Zero value = legacy behavior,
// byte-for-byte: created_at takes the schema default (datetime('now')) and
// no guard beyond (kind, period) uniqueness applies. Non-zero, it is
// written INTO created_at (closedAt.UTC() in archiveTimestampFmt, the same
// text form the schema default emits) so the NEXT close's `from` — read
// back via LatestArchivedAt — is byte-identical to this close's `to`;
// letting SQLite stamp a second, independent datetime('now') would leave a
// sub-second gap or overlap between consecutive close windows (the ADR's
// clock-skew fix, load-bearing for gaplessness).
//
// Non-zero closedAt on kind='eod' additionally arms the atomic double-close
// guard: once period is a close INSTANT, two closes seconds apart no longer
// collide on (kind, period), so that uniqueness stops serialising anything
// and a lost race would burn a real, gapless Z-number on a near-empty
// duplicate Z-Bon. The replacement predicate — no kind='eod' row already
// exists whose created_at falls on the same LOCAL calendar day as closedAt
// — is folded into this SAME autocommit INSERT...SELECT (the write-lock
// property above is exactly what makes it race-free; a separate pre-check
// would be the TOCTOU the ADR calls out), and a hit behaves like a (kind,
// period) conflict: created=false, no number consumed, no error. It rides
// in a HAVING clause, NOT the WHERE: an aggregate SELECT with no GROUP BY
// always yields exactly one row even when WHERE filters out every input row
// (the same property the first-close z_number=1 case above relies on), so
// a false predicate in the WHERE would not suppress the insert at all —
// HAVING is evaluated against the single aggregate row and can actually
// eliminate it. The guard never applies to other kinds (the `? != 'eod'`
// arm) or to a zero closedAt (legacy statement, no HAVING).
func (r *POSRepo) ArchiveReport(ctx context.Context, kind, period string, content []byte, firstReceipt, lastReceipt string, closedAt time.Time) (bool, error) {
	id := uuid.NewString()
	query := `
INSERT INTO report_archive (id, kind, period, content_json, z_number, prev_z_number, prev_closed_at, first_receipt, last_receipt)
SELECT ?, ?, ?, ?,
  COALESCE(MAX(z_number), 0) + 1,
  MAX(z_number),
  (SELECT created_at FROM report_archive
     WHERE kind = ? AND z_number IS NOT NULL
     ORDER BY z_number DESC LIMIT 1),
  ?, ?
FROM report_archive WHERE kind = ?
ON CONFLICT (kind, period) DO NOTHING`
	args := []any{id, kind, period, string(content), kind, firstReceipt, lastReceipt, kind}
	if !closedAt.IsZero() {
		closedAtStr := closedAt.UTC().Format(archiveTimestampFmt)
		query = `
INSERT INTO report_archive (id, kind, period, content_json, z_number, prev_z_number, prev_closed_at, first_receipt, last_receipt, created_at)
SELECT ?, ?, ?, ?,
  COALESCE(MAX(z_number), 0) + 1,
  MAX(z_number),
  (SELECT created_at FROM report_archive
     WHERE kind = ? AND z_number IS NOT NULL
     ORDER BY z_number DESC LIMIT 1),
  ?, ?, ?
FROM report_archive WHERE kind = ?
HAVING (? != 'eod' OR NOT EXISTS (
  SELECT 1 FROM report_archive ra2
  WHERE ra2.kind = 'eod'
    AND date(ra2.created_at, 'localtime') = date(?, 'localtime')))
ON CONFLICT (kind, period) DO NOTHING`
		args = []any{id, kind, period, string(content), kind, firstReceipt, lastReceipt,
			closedAtStr, kind, kind, closedAtStr}
	}
	var lastErr error
	for range 3 {
		res, err := r.db.ExecContext(ctx, query, args...)
		if err == nil {
			n, _ := res.RowsAffected()
			return n > 0, nil
		}
		lastErr = err
	}
	return false, fmt.Errorf("archive report: %w", lastErr)
}

// ArchivedReportRow lists an archived report for the Reports page and the
// retention export (ADR-0040 §7). JSON tags are snake_case per this repo's
// API convention -- the export handler is the one consumer that actually
// marshals this to JSON; the Reports page reads the Go fields directly.
//
// ZNumber is 0 for a pre-migration legacy row that never got a number (real
// numbers start at 1, so 0 is unambiguous "no number"). PrevZNumber/
// PrevClosedAt are pointers because NULL ("no previous close" -- legacy rows
// and a till's first close) is a distinct fact from any real value and must
// not be coerced to a fake zero.
type ArchivedReportRow struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Period       string  `json:"period"`
	Content      string  `json:"content_json"`
	CreatedAt    string  `json:"created_at"`
	ZNumber      int64   `json:"z_number"`
	PrevZNumber  *int64  `json:"prev_z_number"`
	PrevClosedAt *string `json:"prev_closed_at"`
	FirstReceipt string  `json:"first_receipt"`
	LastReceipt  string  `json:"last_receipt"`
}

// scanArchivedReport scans the shared SELECT column list of
// ListArchivedReports / ArchivedReportsInRange, mapping the nullable
// columns to their Go representations (see ArchivedReportRow's doc).
func scanArchivedReport(rows *sql.Rows) (ArchivedReportRow, error) {
	var a ArchivedReportRow
	var zNum, prevZ sql.NullInt64
	var prevClosed, firstR, lastR sql.NullString
	if err := rows.Scan(&a.ID, &a.Kind, &a.Period, &a.Content, &a.CreatedAt,
		&zNum, &prevZ, &prevClosed, &firstR, &lastR); err != nil {
		return a, fmt.Errorf("scan report: %w", err)
	}
	a.CreatedAt = formatArchiveTimestamp(a.CreatedAt)
	a.ZNumber = zNum.Int64 // NULL (legacy row) reads as 0; real numbers start at 1
	if prevZ.Valid {
		a.PrevZNumber = &prevZ.Int64
	}
	if prevClosed.Valid {
		// Same SQLite datetime('now') format as created_at, so the same
		// ISO-8601 normalization applies (CLAUDE.md date-format rule).
		s := formatArchiveTimestamp(prevClosed.String)
		a.PrevClosedAt = &s
	}
	a.FirstReceipt = firstR.String
	a.LastReceipt = lastR.String
	return a, nil
}

// archiveTimestampFmt is the one text form report_archive.created_at ever
// holds: the schema default datetime('now') emits it (space-separated,
// implicitly UTC), and ArchiveReport's explicit closedAt write formats to
// the SAME layout and timezone convention deliberately (ADR-0066,
// ut-docs#1140) so every reader — formatArchiveTimestamp, LatestArchivedAt,
// the double-close guard's date(..., 'localtime') — parses one form, never
// two. Deliberately equal to reportWindowFmt, not just coincidentally the
// same literal (review finding, ut-docs#1140): ArchiveReport's closedAt
// write and windowArgs' bound-param rendering must stay byte-identical, or
// the "next close's from is byte-identical to this close's to" gaplessness
// guarantee (ADR-0066 Decision 5) silently stops holding.
const archiveTimestampFmt = reportWindowFmt

// formatArchiveTimestamp converts report_archive.created_at
// (archiveTimestampFmt, implicitly UTC) to ISO-8601 (CLAUDE.md's API format
// rule) for both the Reports page and the export. Falls back to the raw
// value on an unexpected format rather than blanking it -- a slightly-off
// timestamp is better than a silently dropped one. (LatestArchivedAt, which
// feeds correctness-critical window boundaries rather than display, makes
// the opposite call and errors instead.)
func formatArchiveTimestamp(raw string) string {
	t, err := time.Parse(archiveTimestampFmt, raw)
	if err != nil {
		return raw
	}
	return t.UTC().Format(time.RFC3339)
}

// GetArchivedReport returns the single archived report for (kind, period),
// via the report_archive `UNIQUE(kind, period)` index — for a caller that
// already knows both (ut-docs#1323: the EOD reprint handler previously
// called ListArchivedReports(ctx, 100) and linear-scanned up to 100 full
// report blobs, including their large content_json, just to find this one
// row). false, nil means no such report exists.
func (r *POSRepo) GetArchivedReport(ctx context.Context, kind, period string) (ArchivedReportRow, bool, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, kind, period, content_json, created_at,
       z_number, prev_z_number, prev_closed_at, first_receipt, last_receipt
FROM report_archive WHERE kind = ? AND period = ?`, kind, period)
	if err != nil {
		return ArchivedReportRow{}, false, fmt.Errorf("get archived report: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		return ArchivedReportRow{}, false, rows.Err()
	}
	a, err := scanArchivedReport(rows)
	if err != nil {
		return ArchivedReportRow{}, false, err
	}
	return a, true, nil
}

// ListArchivedReports returns recent archived reports, newest first.
func (r *POSRepo) ListArchivedReports(ctx context.Context, limit int) ([]ArchivedReportRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, kind, period, content_json, created_at,
       z_number, prev_z_number, prev_closed_at, first_receipt, last_receipt
FROM report_archive ORDER BY period DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list reports: %w", err)
	}
	defer rows.Close()
	var out []ArchivedReportRow
	for rows.Next() {
		a, err := scanArchivedReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LatestArchivedAt returns the greatest created_at among kind's archived
// rows — for the "eod" kind, the previous close instant: the next close's
// window `from` and eodDue's cheap "already ran today" pre-check (ADR-0066
// Decision 5, ut-docs#1140). MAX(created_at), deliberately NOT ORDER BY
// period: once "eod" periods become RFC3339 close instants, period mixes
// calendar-date and RFC3339 forms across the cutover and no longer orders
// chronologically within a day; created_at stays a single text form
// (archiveTimestampFmt) whose text MAX is its chronological MAX. nil, nil
// when no rows exist — the till's first-ever close, which the caller runs
// with an unbounded lower bound (Decision 3).
//
// The stored value is UTC-naive text, parsed with time.Parse — never
// time.ParseInLocation(..., time.Local), which would silently reproduce
// ADR-0057's bug class on any non-UTC host (CI's TZ=UTC can't catch that
// mistake; the regression test overrides time.Local for exactly this
// reason). Unlike the display-only formatArchiveTimestamp, a parse failure
// here is an ERROR: this value becomes a fiscal window boundary, and a
// silent fallback would corrupt the close window rather than merely render
// an odd string.
func (r *POSRepo) LatestArchivedAt(ctx context.Context, kind string) (*time.Time, error) {
	var raw sql.NullString
	if err := r.db.QueryRowContext(ctx,
		`SELECT MAX(created_at) FROM report_archive WHERE kind = ?`, kind).Scan(&raw); err != nil {
		return nil, fmt.Errorf("latest archived at: %w", err)
	}
	if !raw.Valid {
		return nil, nil
	}
	ts, err := time.Parse(archiveTimestampFmt, raw.String)
	if err != nil {
		return nil, fmt.Errorf("latest archived at: parse created_at %q: %w", raw.String, err)
	}
	return &ts, nil
}

// HasArchivedReport reports whether kind+period was already generated.
func (r *POSRepo) HasArchivedReport(ctx context.Context, kind, period string) (bool, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM report_archive WHERE kind = ? AND period = ?`,
		kind, period).Scan(&n); err != nil {
		return false, fmt.Errorf("has report: %w", err)
	}
	return n > 0, nil
}

// PruneReportArchiveOlderThan deletes report_archive rows whose period is
// strictly before cutoff (ADR-0040 §2, till-mode age-based retention).
// period is stored "YYYY-MM-DD" (see generateEOD), which sorts and
// range-compares correctly as plain text, so cutoff is the same format and
// no date parsing happens here. A single DELETE statement, never
// read-then-delete, so a prune can't race an in-flight archive write.
// Returns the number of rows removed.
func (r *POSRepo) PruneReportArchiveOlderThan(ctx context.Context, cutoff string) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM report_archive WHERE period < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune report archive: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// ReportArchiveCoverage summarizes how far back the shop's report archive
// goes, for the settings page's "records held from X to Y" display
// (ADR-0040 §7).
type ReportArchiveCoverage struct {
	Earliest string
	Latest   string
	Count    int
}

// ReportArchiveCoverage returns the earliest/latest archived period and the
// total row count. MIN/MAX are NULL on an empty table, which would fail a
// plain string Scan -- sql.NullString handles that, and the zero-value
// Coverage{} (empty strings, Count 0) is what the caller renders as "no
// archived reports yet".
func (r *POSRepo) ReportArchiveCoverage(ctx context.Context) (ReportArchiveCoverage, error) {
	var cov ReportArchiveCoverage
	var earliest, latest sql.NullString
	if err := r.db.QueryRowContext(ctx,
		`SELECT MIN(period), MAX(period), COUNT(*) FROM report_archive`,
	).Scan(&earliest, &latest, &cov.Count); err != nil {
		return cov, fmt.Errorf("report archive coverage: %w", err)
	}
	cov.Earliest = earliest.String
	cov.Latest = latest.String
	return cov, nil
}

// ArchivedReportsInRange returns archived reports with period in [from, to]
// (inclusive), oldest first -- the bounded fetch behind the settings-page
// export (ADR-0040 §7). Reuses ArchivedReportRow; the caller is responsible
// for bounding the [from, to] span before calling this (no row-count/date-
// span limit is enforced here, same division of responsibility as
// data_api.go's export handler).
func (r *POSRepo) ArchivedReportsInRange(ctx context.Context, from, to string) ([]ArchivedReportRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, kind, period, content_json, created_at,
       z_number, prev_z_number, prev_closed_at, first_receipt, last_receipt
FROM report_archive WHERE period BETWEEN ? AND ? ORDER BY period`, from, to)
	if err != nil {
		return nil, fmt.Errorf("archived reports in range: %w", err)
	}
	defer rows.Close()
	// []ArchivedReportRow{}, not var-declared nil: this slice is JSON-
	// encoded directly by the export handler, and an empty (not nil) slice
	// marshals to "[]", not "null" -- a real, previously-shipped bug this
	// repo's review process caught before merge (ADR-0040 card 1 review).
	out := []ArchivedReportRow{}
	for rows.Next() {
		a, err := scanArchivedReport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SaleExists reports whether a sale id is already recorded (sync idempotency).
func (r *POSRepo) SaleExists(ctx context.Context, saleID string) (bool, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sales WHERE id = ?`, saleID).Scan(&n); err != nil {
		return false, fmt.Errorf("sale exists: %w", err)
	}
	return n > 0, nil
}

// SetSaleProvenance stamps a journaled-in sale with its source till and
// original timestamp (CompleteSale wrote "now").
func (r *POSRepo) SetSaleProvenance(ctx context.Context, saleID, tillID, createdAt string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE sales SET till_id = ?, created_at = ? WHERE id = ?`, tillID, createdAt, saleID)
	if err != nil {
		return fmt.Errorf("set provenance: %w", err)
	}
	return nil
}

// LocalSalesSince lists this till's OWN completed sales after the cursor
// (created_at), oldest first — the replica's push queue. Journaled-in
// sales (till_id != ”) are excluded.
func (r *POSRepo) LocalSalesSince(ctx context.Context, cursor string, limit int) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT receipt_no FROM sales
WHERE status = 'completed' AND till_id = '' AND created_at > ?
ORDER BY created_at LIMIT ?`, cursor, limit)
	if err != nil {
		return nil, fmt.Errorf("local sales since: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var rn string
		if err := rows.Scan(&rn); err != nil {
			return nil, fmt.Errorf("scan local sale: %w", err)
		}
		out = append(out, rn)
	}
	return out, rows.Err()
}

// CountLocalSalesSince is the replica's push-queue depth (D4 status chip).
func (r *POSRepo) CountLocalSalesSince(ctx context.Context, cursor string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM sales
WHERE status = 'completed' AND till_id = '' AND created_at > ?`, cursor).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count local sales: %w", err)
	}
	return n, nil
}

// OriginalSaleIDFor returns the linked original sale id for a return.
func (r *POSRepo) OriginalSaleIDFor(ctx context.Context, returnSaleID string) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx,
		`SELECT original_sale_id FROM sale_links WHERE sale_id = ?`, returnSaleID).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("original sale id: %w", err)
	}
	return id, nil
}

// RefundLineKey identifies a refundable line across the original sale and
// its return sales: same item/variant at the same unit price.
//
// orderType (ut-docs#1181, ADR-0073 Decision 6) is part of the key: the
// same product sold once dine-in and once takeaway has the SAME gross unit
// price and DIFFERENT tax rates, so a price-only key would let both units
// be refunded against the higher-rate row and reclaim VAT that was never
// collected at that rate. "" (dine-in) keeps the pre-ADR-0073 key shape
// byte-identical for every uniform sale.
func RefundLineKey(itemID, variantID string, unitPrice int64, orderType string) string {
	k := itemID + "|" + variantID + "|" + strconv.FormatInt(unitPrice, 10)
	if orderType != "" {
		k += "|" + orderType
	}
	return k
}

// ReturnedQuantities sums, per line key, what previous returns linked to
// the original sale already gave back — the double-refund guard's input.
func (r *POSRepo) ReturnedQuantities(ctx context.Context, originalSaleID string) (map[string]float64, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT COALESCE(l.item_id, ''), COALESCE(l.variant_id, ''), l.unit_price, COALESCE(l.order_type, ''), SUM(l.quantity)
FROM sale_lines l
JOIN sale_links k ON k.sale_id = l.sale_id
JOIN sales s ON s.id = l.sale_id AND s.sale_type = 'return' AND s.status = 'completed'
WHERE k.original_sale_id = ?
GROUP BY l.item_id, l.variant_id, l.unit_price, l.order_type`, originalSaleID)
	if err != nil {
		return nil, fmt.Errorf("returned quantities: %w", err)
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var itemID, variantID, orderType string
		var unitPrice int64
		var qty float64
		if err := rows.Scan(&itemID, &variantID, &unitPrice, &orderType, &qty); err != nil {
			return nil, fmt.Errorf("scan returned qty: %w", err)
		}
		out[RefundLineKey(itemID, variantID, unitPrice, orderType)] = qty
	}
	return out, rows.Err()
}

// OriginalReceiptFor resolves the original sale's receipt number for a
// return sale (refund receipts reference it), and the reverse direction
// lists returns made against a sale.
func (r *POSRepo) OriginalReceiptFor(ctx context.Context, returnSaleID string) (string, bool, error) {
	var receipt string
	err := r.db.QueryRowContext(ctx, `
SELECT s.receipt_no FROM sale_links k JOIN sales s ON s.id = k.original_sale_id
WHERE k.sale_id = ?`, returnSaleID).Scan(&receipt)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("original receipt: %w", err)
	}
	return receipt, true, nil
}

// ReturnReceiptsFor lists receipt numbers of returns linked to a sale.
func (r *POSRepo) ReturnReceiptsFor(ctx context.Context, originalSaleID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT s.receipt_no FROM sale_links k JOIN sales s ON s.id = k.sale_id
WHERE k.original_sale_id = ? ORDER BY s.created_at`, originalSaleID)
	if err != nil {
		return nil, fmt.Errorf("return receipts: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var rn string
		if err := rows.Scan(&rn); err != nil {
			return nil, fmt.Errorf("scan return receipt: %w", err)
		}
		out = append(out, rn)
	}
	return out, rows.Err()
}

// ReceiptExists reports whether a receipt number belongs to a sale — the
// scan handler's fallback for scan-to-refund.
func (r *POSRepo) ReceiptExists(ctx context.Context, receiptNo string) (bool, error) {
	var n int
	if err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sales WHERE receipt_no = ?`, receiptNo).Scan(&n); err != nil {
		return false, fmt.Errorf("receipt exists: %w", err)
	}
	return n > 0, nil
}

// AuditActionCount aggregates till activity for "Ask your till" — counts
// only, never payloads, so no customer data can reach the model.
type AuditActionCount struct {
	ActorID    string `json:"actor_id"`
	EntityType string `json:"entity_type"`
	Action     string `json:"action"`
	Count      int    `json:"count"`
}

// AuditActionSummary counts audit actions per actor over the last N days
// (voids, no-sales, logins, overrides — the shrinkage-relevant signals).
func (r *POSRepo) AuditActionSummary(ctx context.Context, days, limit int) ([]AuditActionCount, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT COALESCE(actor_id, ''), entity_type, action, COUNT(*)
FROM audit_log
WHERE created_at >= datetime('now', ?)
GROUP BY actor_id, entity_type, action
ORDER BY COUNT(*) DESC LIMIT ?`, fmt.Sprintf("-%d days", days), limit)
	if err != nil {
		return nil, fmt.Errorf("audit summary: %w", err)
	}
	defer rows.Close()
	var out []AuditActionCount
	for rows.Next() {
		var a AuditActionCount
		if err := rows.Scan(&a.ActorID, &a.EntityType, &a.Action, &a.Count); err != nil {
			return nil, fmt.Errorf("scan audit summary: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ShiftSummary is a row on the shifts page (current or historical).
type ShiftSummary struct {
	ID          string
	RegisterID  string
	CashierID   string
	OpenedAt    string
	ClosedAt    string
	OpeningCash int64
	ClosingCash int64
	Expected    int64
	Note        string
	Open        bool
	Variance    int64 // counted - expected (closed shifts only)
}

// CurrentOpenShift returns the open shift (any register), if one exists.
func (r *POSRepo) CurrentOpenShift(ctx context.Context) (ShiftSummary, bool, error) {
	var s ShiftSummary
	var closedAt, note sql.NullString
	var closing, expected sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
SELECT id, register_id, cashier_id, opened_at, closed_at, opening_cash, closing_cash, expected_cash, note
FROM shifts WHERE closed_at IS NULL ORDER BY opened_at DESC LIMIT 1`).Scan(
		&s.ID, &s.RegisterID, &s.CashierID, &s.OpenedAt, &closedAt, &s.OpeningCash, &closing, &expected, &note)
	if err == sql.ErrNoRows {
		return ShiftSummary{}, false, nil
	}
	if err != nil {
		return ShiftSummary{}, false, fmt.Errorf("current open shift: %w", err)
	}
	s.Open = true
	s.ClosedAt = closedAt.String
	s.ClosingCash = closing.Int64
	s.Expected = expected.Int64
	s.Note = note.String
	return s, true, nil
}

// CurrentOpenShiftForRegister returns the open shift for ONE register, if it
// has one — the register-scoped resolution ut-docs#268 requires for write
// paths, instead of CurrentOpenShift's "most recent across any register"
// heuristic. pos.OpenShift guards against opening a second shift for the
// same register, but that guard is a non-transactional read-then-write
// (FindOpenShiftForRegister runs before the insert's own transaction), and
// there is no unique index enforcing it at the DB level (independent
// review finding, ut-docs#268 round 2) — so two shifts open for one
// register, however unlikely, isn't impossible. ORDER BY + LIMIT 1 mirrors
// the CurrentOpenShift sibling so that if it ever happens, this resolves
// to the same (newest) shift a manager looking at CurrentOpenShift's own
// display would see, rather than an arbitrary row.
func (r *POSRepo) CurrentOpenShiftForRegister(ctx context.Context, registerID string) (ShiftSummary, bool, error) {
	var s ShiftSummary
	var closedAt, note sql.NullString
	var closing, expected sql.NullInt64
	err := r.db.QueryRowContext(ctx, `
SELECT id, register_id, cashier_id, opened_at, closed_at, opening_cash, closing_cash, expected_cash, note
FROM shifts WHERE register_id = ? AND closed_at IS NULL ORDER BY opened_at DESC LIMIT 1`, registerID).Scan(
		&s.ID, &s.RegisterID, &s.CashierID, &s.OpenedAt, &closedAt, &s.OpeningCash, &closing, &expected, &note)
	if err == sql.ErrNoRows {
		return ShiftSummary{}, false, nil
	}
	if err != nil {
		return ShiftSummary{}, false, fmt.Errorf("current open shift for register: %w", err)
	}
	s.Open = true
	s.ClosedAt = closedAt.String
	s.ClosingCash = closing.Int64
	s.Expected = expected.Int64
	s.Note = note.String
	return s, true, nil
}

// ListRecentShifts returns the latest shifts, newest first.
func (r *POSRepo) ListRecentShifts(ctx context.Context, limit int) ([]ShiftSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, register_id, cashier_id, opened_at, closed_at, opening_cash, closing_cash, expected_cash, note
FROM shifts ORDER BY opened_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list shifts: %w", err)
	}
	defer rows.Close()
	var out []ShiftSummary
	for rows.Next() {
		var s ShiftSummary
		var closedAt, note sql.NullString
		var closing, expected sql.NullInt64
		if err := rows.Scan(&s.ID, &s.RegisterID, &s.CashierID, &s.OpenedAt, &closedAt, &s.OpeningCash, &closing, &expected, &note); err != nil {
			return nil, fmt.Errorf("scan shift: %w", err)
		}
		s.Open = !closedAt.Valid
		s.ClosedAt = closedAt.String
		s.ClosingCash = closing.Int64
		s.Expected = expected.Int64
		s.Note = note.String
		if !s.Open {
			s.Variance = s.ClosingCash - s.Expected
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate shifts: %w", err)
	}
	return out, nil
}

// Register is a till/checkout station.
type Register struct {
	ID   string
	Name string
}

// ListRegisters returns active registers for the shift-open picker.
func (r *POSRepo) ListRegisters(ctx context.Context) ([]Register, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name FROM registers WHERE is_active = 1 ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list registers: %w", err)
	}
	defer rows.Close()
	var out []Register
	for rows.Next() {
		var reg Register
		if err := rows.Scan(&reg.ID, &reg.Name); err != nil {
			return nil, fmt.Errorf("scan register: %w", err)
		}
		out = append(out, reg)
	}
	return out, rows.Err()
}

// CreateRegister adds a new, active register (a till/checkout station).
// The schema's UNIQUE constraint on name rejects duplicates. locationID is
// optional (nil leaves the register unassigned to any stock location).
func (r *POSRepo) CreateRegister(ctx context.Context, name string, locationID *string) (string, error) {
	id := uuid.NewString()
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO registers (id, name, location_id, is_active) VALUES (?, ?, ?, 1)`, id, name, locationID); err != nil {
		return "", fmt.Errorf("create register: %w", err)
	}
	return id, nil
}

// CreateRegisterForEnrolment auto-provisions a register for a joining till
// (ut-docs#894): the primary's /api/sync/enroll handler calls this so the new
// register is already part of the snapshot the joining till downloads, instead
// of a manual Settings → Registers step after the join. registers.name is
// UNIQUE, and the till's name is chosen by whoever enrols it — a collision
// (e.g. two tills both named "till") must NOT fail the enrolment, so on a
// UNIQUE violation this retries with "<baseName> (2)", "<baseName> (3)", …
// up to a bounded number of attempts. Returns the id and the (possibly
// suffixed) name actually used.
func (r *POSRepo) CreateRegisterForEnrolment(ctx context.Context, baseName string) (string, string, error) {
	const maxAttempts = 50
	name := baseName
	for i := 1; i <= maxAttempts; i++ {
		if i > 1 {
			name = fmt.Sprintf("%s (%d)", baseName, i)
		}
		id, err := r.CreateRegister(ctx, name, nil)
		if err == nil {
			return id, name, nil
		}
		if !isUniqueViolation(err) {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("create register for enrolment: %d name candidates for %q all taken", maxAttempts, baseName)
}

// RenameRegister changes a register's display name; the id (and every
// shift/sale row keyed by it) is unaffected.
func (r *POSRepo) RenameRegister(ctx context.Context, id, newName string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE registers SET name = ? WHERE id = ?`, newName, id)
	if err != nil {
		return fmt.Errorf("rename register: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("rename register: %s not found", id)
	}
	return nil
}

// SetRegisterLocation changes a register's assigned stock location (nil
// clears it back to unassigned) -- ut-docs#895: a register's location was
// previously fixed at creation time, so a mis-assignment had no fix short of
// recreating the register. Existing shift/sale history stays keyed by the
// register's id and is unaffected by a location change.
func (r *POSRepo) SetRegisterLocation(ctx context.Context, id string, locationID *string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE registers SET location_id = ? WHERE id = ?`, locationID, id)
	if err != nil {
		return fmt.Errorf("set register location: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set register location: %s not found", id)
	}
	return nil
}

// SetRegisterActive soft-disables/re-enables a register, mirroring
// SetStockLocationActive's pattern. Unlike a stock location, a register
// with shift/sale history is still allowed to be deactivated (retiring a
// till keeps its history) -- callers guard only the last-active-register
// case, not RegisterInUse.
func (r *POSRepo) SetRegisterActive(ctx context.Context, id string, active bool) error {
	v := 0
	if active {
		v = 1
	}
	res, err := r.db.ExecContext(ctx, `UPDATE registers SET is_active = ? WHERE id = ?`, v, id)
	if err != nil {
		return fmt.Errorf("set register active: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set register active: %s not found", id)
	}
	return nil
}

// RegisterInUse reports whether any shift or sale still references this
// register. Informational only -- the registers admin page does not block
// deactivation on it (unlike locations' guard), since a retired till should
// still be deactivatable while keeping its history.
func (r *POSRepo) RegisterInUse(ctx context.Context, id string) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx, `
SELECT 1 WHERE EXISTS (SELECT 1 FROM shifts WHERE register_id = ?)
   OR EXISTS (SELECT 1 FROM sales WHERE register_id = ?)`,
		id, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check register in use: %w", err)
	}
	return exists == 1, nil
}

// CountActiveRegisters counts active registers -- guards "cannot deactivate
// the last active register" (pos.OpenShift/ResolveTillRegisterID depend on
// at least one active register existing), mirroring
// CountActiveStockLocations' last-location guard.
func (r *POSRepo) CountActiveRegisters(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM registers WHERE is_active = 1`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active registers: %w", err)
	}
	return n, nil
}

// RegisterAdmin is a register as the registers admin page needs it
// (includes the soft-disable state and location assignment the plain
// picker list doesn't).
type RegisterAdmin struct {
	ID         string
	Name       string
	LocationID *string
	IsActive   bool
}

// ListRegistersForAdmin returns every register (active and inactive),
// ordered by name, for the registers management page. ListRegisters itself
// stays active-only for the shift-open/Settings pickers, untouched.
func (r *POSRepo) ListRegistersForAdmin(ctx context.Context) ([]RegisterAdmin, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, location_id, is_active FROM registers ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list registers for admin: %w", err)
	}
	defer rows.Close()
	var out []RegisterAdmin
	for rows.Next() {
		var reg RegisterAdmin
		var locationID sql.NullString
		var active int
		if err := rows.Scan(&reg.ID, &reg.Name, &locationID, &active); err != nil {
			return nil, fmt.Errorf("scan register admin: %w", err)
		}
		if locationID.Valid {
			v := locationID.String
			reg.LocationID = &v
		}
		reg.IsActive = active == 1
		out = append(out, reg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate registers for admin: %w", err)
	}
	return out, nil
}

// StockLocation is a named place stock lives (shop floor, back room, …).
type StockLocation struct {
	ID   string
	Name string
}

// ListStockLocations returns all stock locations for pickers.
func (r *POSRepo) ListStockLocations(ctx context.Context) ([]StockLocation, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name FROM stock_locations ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list stock locations: %w", err)
	}
	defer rows.Close()
	var out []StockLocation
	for rows.Next() {
		var l StockLocation
		if err := rows.Scan(&l.ID, &l.Name); err != nil {
			return nil, fmt.Errorf("scan stock location: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stock locations: %w", err)
	}
	return out, nil
}

// ListStockLevels returns current stock per item/location for the inventory page.
func (r *POSRepo) ListStockLevels(ctx context.Context) ([]LowStockItem, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT i.id, i.name, COALESCE(i.sku, ''), inv.location_id, COALESCE(sl.name, ''),
       COALESCE(inv.quantity, 0), COALESCE(i.reorder_level, 0), COALESCE(i.lead_time_days, 0)
FROM inventory inv
JOIN items i ON i.id = inv.item_id
LEFT JOIN stock_locations sl ON sl.id = inv.location_id
WHERE i.is_active = 1
ORDER BY i.name, sl.name`)
	if err != nil {
		return nil, fmt.Errorf("query stock levels: %w", err)
	}
	defer rows.Close()
	var items []LowStockItem
	for rows.Next() {
		var item LowStockItem
		if err := rows.Scan(&item.ItemID, &item.Name, &item.SKU, &item.LocationID, &item.LocationName, &item.CurrentQty, &item.ReorderLevel, &item.LeadTimeDays); err != nil {
			return nil, fmt.Errorf("scan stock level: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stock levels: %w", err)
	}
	return items, nil
}

// ItemDailySellRates returns each item's average units sold per day over
// [from, to) (completed sales minus returns). Items with no movement are
// absent. Drives the inventory page's "days of stock left" prediction. The
// divisor is the window's own span (to - from in days), so a caller passing
// a calendar period (e.g. a 31-day month) gets a rate scaled to that
// period's real length rather than a hardcoded count. A non-positive span
// (to <= from) has no meaningful daily rate, so it returns an empty map
// rather than dividing by zero or a negative number.
func (r *POSRepo) ItemDailySellRates(ctx context.Context, from, to time.Time) (map[string]float64, error) {
	days := to.Sub(from).Hours() / 24
	if days <= 0 {
		return map[string]float64{}, nil
	}
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT COALESCE(NULLIF(sl.item_id, ''), v.item_id) AS iid,
       SUM(CASE WHEN s.sale_type = 'return' THEN -sl.quantity ELSE sl.quantity END)
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
LEFT JOIN item_variants v ON v.id = sl.variant_id
WHERE s.status = 'completed'
  AND COALESCE(NULLIF(sl.item_id, ''), v.item_id) IS NOT NULL
  AND datetime(s.created_at) >= datetime(?) AND datetime(s.created_at) < datetime(?)
GROUP BY iid`, fromStr, toStr)
	if err != nil {
		return nil, fmt.Errorf("query sell rates: %w", err)
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var itemID string
		var qty float64
		if err := rows.Scan(&itemID, &qty); err != nil {
			return nil, fmt.Errorf("scan sell rate: %w", err)
		}
		if qty > 0 {
			out[itemID] = qty / days
		}
	}
	return out, rows.Err()
}

// NextReceiptNo returns the next available receipt number. On a synced
// replica (ADR-0011) the till's prefix (settings sync.receipt_prefix,
// e.g. "T2-") namespaces the sequence so tills never collide; the counter
// is the max within this till's own prefix.
func (r *POSRepo) NextReceiptNo(ctx context.Context, tx *sql.Tx) (string, error) {
	exec := r.exec(tx)
	var prefix string
	_ = exec.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = 'sync.receipt_prefix'`).Scan(&prefix)
	var maxVal sql.NullInt64
	if err := exec.QueryRowContext(ctx, `
SELECT COALESCE(MAX(CAST(substr(receipt_no, ?) AS INTEGER)), 0)
FROM sales WHERE receipt_no LIKE ? || '%'`,
		len(prefix)+1, prefix).Scan(&maxVal); err != nil {
		return "", fmt.Errorf("next receipt no: %w", err)
	}
	next := maxVal.Int64 + 1
	if next < 1 {
		next = 1
	}
	return fmt.Sprintf("%s%09d", prefix, next), nil
}

// CurrentQty returns quantity and whether a matching inventory row existed.
func (r *POSRepo) CurrentQty(ctx context.Context, tx *sql.Tx, locationID, itemID, variantID string) (float64, bool, error) {
	exec := r.exec(tx)
	var qty float64
	err := exec.QueryRowContext(ctx, `
SELECT COALESCE(quantity, 0)
FROM inventory
WHERE location_id = ?
  AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL))
`, locationID, nullIfEmpty(itemID), nullIfEmpty(variantID)).Scan(&qty)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read inventory: %w", err)
	}
	return qty, true, nil
}

// ----------------------------------------------------------------------
// Batched sale-completion writes (ut-docs#1318). CompleteSale used to issue
// ~5 statements PER BASKET LINE inside its transaction; the methods below
// replace those per-line loops with chunked multi-row statements. The
// single-row siblings (CurrentQty, InsertSaleLine, InsertSaleLineModifiers,
// InsertSaleDiscount, RecordStockMovement) stay untouched — they have other
// live call sites (sync replay, inventory API, catalog import).
// ----------------------------------------------------------------------

// maxBatchParams caps bound parameters per multi-row statement, with safe
// headroom under SQLite's historic 999-variable-per-statement default.
const maxBatchParams = 800

// batchChunkSize is how many rows fit in one multi-row statement given the
// per-row column count.
func batchChunkSize(columnsPerRow int) int {
	return maxBatchParams / columnsPerRow
}

// StockKey identifies one inventory aggregation row: a location plus exactly
// one of item/variant (the same identity shape the inventory and
// stock_movements CHECK constraints enforce).
type StockKey struct {
	LocationID string
	ItemID     string
	VariantID  string
}

// stockKeyPredicate is CurrentQty's own WHERE shape for one key; keys are
// OR-joined rather than expressed as a row-value IN (VALUES ...) list, which
// SQLite does not reliably support for this NULL-asymmetric predicate.
const stockKeyPredicate = `(location_id = ? AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL)))`

// CurrentQtyBatch is CurrentQty for many keys in one SELECT (chunked if the
// distinct-key list is very large). A key with no matching inventory row is
// simply ABSENT from the returned map — the caller treats missing as 0, the
// same meaning as CurrentQty's found=false. Duplicate input keys are fine.
func (r *POSRepo) CurrentQtyBatch(ctx context.Context, tx *sql.Tx, keys []StockKey) (map[StockKey]float64, error) {
	out := make(map[StockKey]float64, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	// Dedupe, preserving first-seen order.
	seen := make(map[StockKey]bool, len(keys))
	distinct := make([]StockKey, 0, len(keys))
	for _, k := range keys {
		if !seen[k] {
			seen[k] = true
			distinct = append(distinct, k)
		}
	}
	exec := r.exec(tx)
	chunk := batchChunkSize(3)
	for start := 0; start < len(distinct); start += chunk {
		end := start + chunk
		if end > len(distinct) {
			end = len(distinct)
		}
		part := distinct[start:end]
		preds := make([]string, 0, len(part))
		args := make([]any, 0, len(part)*3)
		for _, k := range part {
			preds = append(preds, stockKeyPredicate)
			args = append(args, k.LocationID, nullIfEmpty(k.ItemID), nullIfEmpty(k.VariantID))
		}
		rows, err := exec.QueryContext(ctx,
			`SELECT location_id, item_id, variant_id, COALESCE(quantity, 0) FROM inventory WHERE `+strings.Join(preds, " OR "), args...)
		if err != nil {
			return nil, fmt.Errorf("read inventory batch: %w", err)
		}
		for rows.Next() {
			var loc string
			var itemID, variantID sql.NullString
			var qty float64
			if err := rows.Scan(&loc, &itemID, &variantID, &qty); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan inventory batch: %w", err)
			}
			k := StockKey{LocationID: loc, ItemID: itemID.String, VariantID: variantID.String}
			// First row per key wins — same as CurrentQty's QueryRow on a
			// (pathological) duplicated inventory row.
			if _, ok := out[k]; !ok {
				out[k] = qty
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read inventory batch: %w", err)
		}
		rows.Close()
	}
	return out, nil
}

// SaleLineRow mirrors InsertSaleLine's parameters, with the row ID generated
// by the caller (uuid.NewString(), same as today — never DB-generated).
type SaleLineRow struct {
	ID             string
	SaleID         string
	LineNo         int
	ItemID         string
	VariantID      string
	Name           string
	SKU            string
	Barcode        string
	Qty            float64
	UnitPrice      int64
	LineDiscount   int64
	TaxRateBP      int
	TaxAmount      int64
	TotalBeforeTax int64
	TotalAfterTax  int64
	// OrderType (ut-docs#1181, ADR-0073): "" (dine-in) or "takeaway" — the
	// line's own mode, already normalized by pos.CompleteSale.
	OrderType string
}

// InsertSaleLinesBatch writes many sale_lines rows via chunked multi-row
// INSERTs. No-op for an empty batch.
func (r *POSRepo) InsertSaleLinesBatch(ctx context.Context, tx *sql.Tx, rows []SaleLineRow) error {
	if len(rows) == 0 {
		return nil
	}
	const cols = 16
	exec := r.exec(tx)
	chunk := batchChunkSize(cols)
	for start := 0; start < len(rows); start += chunk {
		end := start + chunk
		if end > len(rows) {
			end = len(rows)
		}
		part := rows[start:end]
		placeholders := make([]string, 0, len(part))
		args := make([]any, 0, len(part)*cols)
		for _, row := range part {
			placeholders = append(placeholders, "(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
			args = append(args, row.ID, row.SaleID, row.LineNo, nullIfEmpty(row.ItemID), nullIfEmpty(row.VariantID),
				row.Name, row.SKU, row.Barcode, row.Qty, row.UnitPrice, row.LineDiscount,
				row.TaxRateBP, row.TaxAmount, row.TotalBeforeTax, row.TotalAfterTax, row.OrderType)
		}
		if _, err := exec.ExecContext(ctx, `
INSERT INTO sale_lines (id, sale_id, line_no, item_id, variant_id, name_snapshot, sku_snapshot, barcode_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax, order_type)
VALUES `+strings.Join(placeholders, ", "), args...); err != nil {
			return fmt.Errorf("insert sale lines batch: %w", err)
		}
	}
	return nil
}

// SaleLineModifierRow mirrors InsertSaleLineModifiers' per-modifier insert,
// with the row ID generated by the caller.
type SaleLineModifierRow struct {
	ID              string
	SaleLineID      string
	GroupID         string
	OptionID        string
	GroupName       string
	OptionName      string
	PriceDeltaMinor int64
}

// InsertSaleLineModifiersBatch writes many sale_line_modifiers rows via
// chunked multi-row INSERTs. No-op for an empty batch (the common
// zero-modifier sale). Must run AFTER the referenced sale_lines rows exist.
func (r *POSRepo) InsertSaleLineModifiersBatch(ctx context.Context, tx *sql.Tx, rows []SaleLineModifierRow) error {
	if len(rows) == 0 {
		return nil
	}
	const cols = 7
	exec := r.exec(tx)
	chunk := batchChunkSize(cols)
	for start := 0; start < len(rows); start += chunk {
		end := start + chunk
		if end > len(rows) {
			end = len(rows)
		}
		part := rows[start:end]
		placeholders := make([]string, 0, len(part))
		args := make([]any, 0, len(part)*cols)
		for _, row := range part {
			placeholders = append(placeholders, "(?, ?, ?, ?, ?, ?, ?)")
			args = append(args, row.ID, row.SaleLineID, nullableString(row.GroupID), nullableString(row.OptionID),
				row.GroupName, row.OptionName, row.PriceDeltaMinor)
		}
		if _, err := exec.ExecContext(ctx, `
INSERT INTO sale_line_modifiers (id, sale_line_id, group_id, option_id, group_name_snapshot, option_name_snapshot, price_delta_minor)
VALUES `+strings.Join(placeholders, ", "), args...); err != nil {
			return fmt.Errorf("insert sale line modifiers batch: %w", err)
		}
	}
	return nil
}

// SaleDiscountRow mirrors InsertSaleDiscount's parameters, with the row ID
// generated by the caller. LineID empty = a sale-level discount (NULL).
type SaleDiscountRow struct {
	ID     string
	SaleID string
	LineID string
	Type   string
	Value  int64
	Amount int64
	Reason string
}

// InsertSaleDiscountsBatch writes many sale_discounts rows via chunked
// multi-row INSERTs. No-op for an empty batch. Rows with a LineID must run
// AFTER the referenced sale_lines rows exist.
func (r *POSRepo) InsertSaleDiscountsBatch(ctx context.Context, tx *sql.Tx, rows []SaleDiscountRow) error {
	if len(rows) == 0 {
		return nil
	}
	const cols = 7
	exec := r.exec(tx)
	chunk := batchChunkSize(cols)
	for start := 0; start < len(rows); start += chunk {
		end := start + chunk
		if end > len(rows) {
			end = len(rows)
		}
		part := rows[start:end]
		placeholders := make([]string, 0, len(part))
		args := make([]any, 0, len(part)*cols)
		for _, row := range part {
			placeholders = append(placeholders, "(?, ?, ?, ?, ?, ?, ?)")
			args = append(args, row.ID, row.SaleID, nullIfEmpty(row.LineID), row.Type, row.Value, row.Amount, row.Reason)
		}
		if _, err := exec.ExecContext(ctx, `
INSERT INTO sale_discounts (id, sale_id, line_id, type, value, amount, reason)
VALUES `+strings.Join(placeholders, ", "), args...); err != nil {
			return fmt.Errorf("insert sale discounts batch: %w", err)
		}
	}
	return nil
}

// RecordStockMovementsBatch is RecordStockMovement for many movements at
// once, on the caller's tx (no savepoints — CompleteSale's surrounding
// db.WithTx owns atomicity):
//
//   - one chunked multi-row INSERT into stock_movements (one row per input,
//     with the same missing-cost_price-column retry the single-row method
//     has);
//   - the net quantity delta AGGREGATED per StockKey across the batch (a
//     basket can carry the same item on two lines), then applied with ONE
//     prepared inventory UPDATE executed per distinct key;
//   - keys with no existing inventory row get one chunked multi-row INSERT;
//   - one audit_log row PER MOVEMENT (not per aggregated key), same payload
//     shape as RecordStockMovement.
//
// existing is CurrentQtyBatch's result for these keys on the SAME tx: keys
// absent from a non-nil map skip the UPDATE probe and insert directly. A nil
// map means "unknown" — every key is probed with the UPDATE and inserted
// only when no row was affected (full RecordStockMovement semantics).
// Returned movement IDs are in input order.
func (r *POSRepo) RecordStockMovementsBatch(ctx context.Context, tx *sql.Tx, ins []StockMovementInput, existing map[StockKey]float64) ([]string, error) {
	if len(ins) == 0 {
		return nil, nil
	}
	if tx == nil {
		return nil, errors.New("transaction required")
	}
	for i, in := range ins {
		switch {
		case in.LocationID == "":
			return nil, fmt.Errorf("stock movement %d: locationID required", i+1)
		case in.ItemID == "" && in.VariantID == "":
			return nil, fmt.Errorf("stock movement %d: itemID or variantID required", i+1)
		case in.ItemID != "" && in.VariantID != "":
			return nil, fmt.Errorf("stock movement %d: cannot specify both itemID and variantID", i+1)
		case in.Type == "":
			return nil, fmt.Errorf("stock movement %d: type required", i+1)
		case in.Quantity == 0:
			return nil, fmt.Errorf("stock movement %d: quantity must be non-zero", i+1)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	movementIDs := make([]string, len(ins))
	for i := range ins {
		movementIDs[i] = uuid.NewString()
	}

	// (b) stock_movements, chunked; whole-batch retry without cost_price on
	// the same column-missing error the single-row method detects. The
	// schema error is deterministic, so it can only strike the FIRST chunk —
	// a later-chunk cost_price error is returned as-is rather than risking
	// re-inserting already-landed chunks.
	var insertMovements func(withCostPrice bool) error
	insertMovements = func(withCostPrice bool) error {
		cols := 9
		colList := `id, item_id, variant_id, location_id, sale_line_id, type, quantity, cost_price, created_at`
		rowPH := "(?, ?, ?, ?, ?, ?, ?, ?, ?)"
		if !withCostPrice {
			cols = 8
			colList = `id, item_id, variant_id, location_id, sale_line_id, type, quantity, created_at`
			rowPH = "(?, ?, ?, ?, ?, ?, ?, ?)"
		}
		chunk := batchChunkSize(cols)
		for start := 0; start < len(ins); start += chunk {
			end := start + chunk
			if end > len(ins) {
				end = len(ins)
			}
			placeholders := make([]string, 0, end-start)
			args := make([]any, 0, (end-start)*cols)
			for i := start; i < end; i++ {
				in := ins[i]
				placeholders = append(placeholders, rowPH)
				args = append(args, movementIDs[i], nullIfEmpty(in.ItemID), nullIfEmpty(in.VariantID), in.LocationID,
					nullIfEmpty(in.SaleLineID), in.Type, in.Quantity)
				if withCostPrice {
					args = append(args, nullInt64(in.CostPrice))
				}
				args = append(args, now)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO stock_movements (`+colList+`) VALUES `+strings.Join(placeholders, ", "), args...); err != nil {
				if withCostPrice && start == 0 && strings.Contains(err.Error(), "cost_price") {
					return insertMovements(false)
				}
				return fmt.Errorf("insert stock movements batch: %w", err)
			}
		}
		return nil
	}
	if err := insertMovements(true); err != nil {
		return nil, err
	}

	// (c) aggregate the net delta per key, in first-seen order.
	agg := make(map[StockKey]float64, len(ins))
	order := make([]StockKey, 0, len(ins))
	for _, in := range ins {
		k := StockKey{LocationID: in.LocationID, ItemID: in.ItemID, VariantID: in.VariantID}
		if _, ok := agg[k]; !ok {
			order = append(order, k)
		}
		agg[k] += in.Quantity
	}

	// (d) split into has-row / needs-row using the caller's CurrentQtyBatch
	// knowledge — no fresh existence-check query here.
	updateKeys := make([]StockKey, 0, len(order))
	insertKeys := make([]StockKey, 0)
	for _, k := range order {
		if existing == nil {
			updateKeys = append(updateKeys, k) // unknown: probe via UPDATE
			continue
		}
		if _, ok := existing[k]; ok {
			updateKeys = append(updateKeys, k)
		} else {
			insertKeys = append(insertKeys, k)
		}
	}

	// (e) one prepared UPDATE, executed once per distinct existing key.
	if len(updateKeys) > 0 {
		stmt, err := tx.PrepareContext(ctx, `
UPDATE inventory
SET quantity = quantity + ?, updated_at = ?
WHERE location_id = ?
  AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL))
`)
		if err != nil {
			return nil, fmt.Errorf("prepare inventory update: %w", err)
		}
		defer stmt.Close()
		for _, k := range updateKeys {
			res, err := stmt.ExecContext(ctx, agg[k], now, k.LocationID, nullIfEmpty(k.ItemID), nullIfEmpty(k.VariantID))
			if err != nil {
				return nil, fmt.Errorf("update inventory: %w", err)
			}
			// aff==0 means the map's knowledge was wrong (or nil): fall
			// through to the insert list, same as RecordStockMovement's
			// own insert-on-zero-affected branch.
			if aff, _ := res.RowsAffected(); aff == 0 {
				insertKeys = append(insertKeys, k)
			}
		}
	}

	// (f) new inventory rows for keys with no existing row, chunked.
	if len(insertKeys) > 0 {
		const cols = 6
		chunk := batchChunkSize(cols)
		for start := 0; start < len(insertKeys); start += chunk {
			end := start + chunk
			if end > len(insertKeys) {
				end = len(insertKeys)
			}
			placeholders := make([]string, 0, end-start)
			args := make([]any, 0, (end-start)*cols)
			for _, k := range insertKeys[start:end] {
				placeholders = append(placeholders, "(?, ?, ?, ?, ?, ?)")
				args = append(args, uuid.NewString(), nullIfEmpty(k.ItemID), nullIfEmpty(k.VariantID), k.LocationID, agg[k], now)
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at)
VALUES `+strings.Join(placeholders, ", "), args...); err != nil {
				return nil, fmt.Errorf("insert inventory batch: %w", err)
			}
		}
	}

	// (g) one audit row per movement, chunked — the audit trail stays
	// per-movement, exactly as N RecordStockMovement calls would leave it.
	{
		const cols = 6
		chunk := batchChunkSize(cols)
		for start := 0; start < len(ins); start += chunk {
			end := start + chunk
			if end > len(ins) {
				end = len(ins)
			}
			placeholders := make([]string, 0, end-start)
			args := make([]any, 0, (end-start)*cols)
			for i := start; i < end; i++ {
				in := ins[i]
				payloadJSON, _ := json.Marshal(map[string]any{
					"type":     in.Type,
					"quantity": in.Quantity,
					"reason":   in.Reason,
				})
				placeholders = append(placeholders, "(?, ?, 'inventory', ?, ?, ?, ?)")
				args = append(args, uuid.NewString(), nullIfEmpty(in.ActorID), movementIDs[i], in.Type, string(payloadJSON), now)
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, data_json, created_at)
VALUES `+strings.Join(placeholders, ", "), args...); err != nil {
				return nil, fmt.Errorf("insert audit batch: %w", err)
			}
		}
	}

	return movementIDs, nil
}

// InsertSaleParams is InsertSale's argument struct (ut-docs#976). InsertSale
// had grown to ~25 positional arguments, one sale-column addition at a time;
// at that arity two adjacent same-typed arguments (two money amounts, two
// basis-point rates) transposed at a call site compiles cleanly and fails
// silently -- named fields turn that into a compile-time mismatch instead.
// Field order mirrors InsertSale's OLD positional-parameter order exactly,
// deliberately (not pos.SaleInput's shape, which has a different field
// order/set) -- that's what makes the positional-to-named translation at
// each call site mechanically auditable. Pure refactor -- no field here
// changes meaning from InsertSale's old positional parameter of the same
// name.
type InsertSaleParams struct {
	SaleID        string
	ReceiptNo     string
	SaleType      string
	RegisterID    string
	CashierID     string
	CustomerID    string
	Currency      string
	Subtotal      int64
	DiscountTotal int64
	TaxTotal      int64
	Total         int64
	// ServiceCharge is the computed till-set service-charge amount for this
	// sale (distinct from a payment's tip_amount) -- it already participates
	// in Total, so it is stored separately only so it can be broken out on
	// the receipt/journal.
	ServiceCharge int64
	// ServiceChargeTaxBasisBP (ADR-0061 Decision 4) is the flat rate the
	// charge's tax was computed at, or 0 for the apportioned fail-closed
	// default. It is persisted rather than recomputed so a replayed/synced
	// sale rebuilds the SAME totals CompleteSale originally stored -- see
	// migration 062.
	ServiceChargeTaxBasisBP int
	// VoucherIssueTotal (ut-docs#1008 review F1, migration 069) is the
	// summed face value of the vouchers issued in this sale — included in
	// Total but in neither Subtotal nor TaxTotal (a 0% liability), stored
	// on the header so pos.InferTaxInclusive can balance its identity from
	// the sale row alone, same shape as ServiceCharge above.
	VoucherIssueTotal int64
	Note              string
	CreatedAt         string
	TenderType        string
	OrderType         string
	TableID           string
	Offline           bool
	SyncStatus        string
	SyncAttempts      int
	SyncNextAttemptAt string
	SyncLastError     string
}

// validateRequired checks the InsertSaleParams fields that must never be
// left at their Go zero value -- every one of these binds directly into a
// NOT NULL column with no nullIfEmpty() indirection, so an omitted field
// would otherwise write a silent empty string past the schema's own
// DEFAULT (SQL defaults only apply when a column is omitted from the
// INSERT, never when it's explicitly bound to ""). Everything else
// (RegisterID, CashierID, CustomerID, TableID, Note, OrderType, the
// sync-retry fields, ServiceCharge/ServiceChargeTaxBasisBP) is legitimately
// optional and existing tests rely on omitting them -- do not add to this
// list without checking those.
func (p InsertSaleParams) validateRequired() error {
	for _, f := range []struct {
		name  string
		empty bool
	}{
		{"SaleID", p.SaleID == ""},
		{"ReceiptNo", p.ReceiptNo == ""},
		{"SaleType", p.SaleType == ""},
		{"Currency", p.Currency == ""},
		{"CreatedAt", p.CreatedAt == ""},
		{"SyncStatus", p.SyncStatus == ""},
		{"TenderType", p.TenderType == ""},
	} {
		if f.empty {
			return fmt.Errorf("insert sale: %s is required", f.name)
		}
	}
	return nil
}

// InsertSale writes the sale header row. See InsertSaleParams for field docs.
func (r *POSRepo) InsertSale(ctx context.Context, tx *sql.Tx, p InsertSaleParams) error {
	if err := p.validateRequired(); err != nil {
		return err
	}
	offlineVal := 0
	if p.Offline {
		offlineVal = 1
	}
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO sales (id, receipt_no, status, sale_type, tender_type, order_type, table_id, offline, sync_status, sync_attempts, sync_next_attempt_at, sync_last_error, register_id, cashier_id, customer_id, currency, subtotal, discount_total, tax_total, total, service_charge_amount, service_charge_tax_basis_bp, voucher_issue_total, rounding, note, created_at, completed_at)
VALUES (?, ?, 'completed', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)
`, p.SaleID, p.ReceiptNo, p.SaleType, p.TenderType, p.OrderType, nullIfEmpty(p.TableID), offlineVal, p.SyncStatus, p.SyncAttempts, nullIfEmpty(p.SyncNextAttemptAt), nullIfEmpty(p.SyncLastError), nullIfEmpty(p.RegisterID), nullIfEmpty(p.CashierID), nullIfEmpty(p.CustomerID), p.Currency, p.Subtotal, p.DiscountTotal, p.TaxTotal, p.Total, p.ServiceCharge, p.ServiceChargeTaxBasisBP, p.VoucherIssueTotal, nullIfEmpty(p.Note), p.CreatedAt, p.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert sale: %w", err)
	}
	return nil
}

// InsertSaleDiscount writes a sale_discounts row.
func (r *POSRepo) InsertSaleDiscount(ctx context.Context, tx *sql.Tx, id, saleID, lineID, discountType string, value, amount int64, reason string) error {
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO sale_discounts (id, sale_id, line_id, type, value, amount, reason)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, id, saleID, nullIfEmpty(lineID), discountType, value, amount, reason)
	if err != nil {
		return fmt.Errorf("insert sale discount: %w", err)
	}
	return nil
}

// SaleCharge is one row of a sale's itemized additive statutory charge list
// (ADR-0062, ut-docs#963) — the child rows sales.service_charge_amount /
// service_charge_tax_basis_bp are derived FROM (sum, first-item basis) once
// a sale has 2+ charges, never the other way around. Amount is already-
// computed and persisted verbatim, same "never recomputed on replay"
// reasoning as every other money field this layer stores (ADR-0061
// Decision 4). There is no Seq field here: order is the slice's own order —
// InsertSaleCharges numbers rows by position, GetSaleDetail reads them back
// `ORDER BY seq`, so the Go type never needs to carry the DB's own ordering
// column.
type SaleCharge struct {
	Key        string `json:"key"`
	Label      string `json:"label,omitempty"`
	Amount     int64  `json:"amount"`
	TaxBasisBP int    `json:"tax_basis_bp,omitempty"`
	Base       string `json:"base,omitempty"`
}

// InsertSaleCharges writes charges as sale_charges rows for saleID, in the
// same transaction as InsertSale — a sibling method rather than a wider
// InsertSale signature (ut-docs#976 flags InsertSale's existing 25
// positional arguments as already risky; this ADR deliberately does not add
// a 26th). Deliberately a no-op for an empty/nil list: a sale with no
// itemized charges (nothing has adopted charge.policy.ask's new Charges
// field yet, per step 2/3 of this ADR) simply gets no sale_charges rows,
// same as it does today.
func (r *POSRepo) InsertSaleCharges(ctx context.Context, tx *sql.Tx, saleID string, charges []SaleCharge) error {
	for i, c := range charges {
		if _, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO sale_charges (sale_id, seq, key, label, amount_minor, tax_basis_bp, base)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, saleID, i, c.Key, c.Label, c.Amount, c.TaxBasisBP, coalesceChargeBase(c.Base)); err != nil {
			return fmt.Errorf("insert sale charge %d: %w", i, err)
		}
	}
	return nil
}

// coalesceChargeBase defaults an empty Base to "net_lines" — the column's
// own DEFAULT only applies when the value is SQL NULL, not an empty string,
// and the Go zero value for SaleCharge.Base is "".
func coalesceChargeBase(base string) string {
	if base == "" {
		return "net_lines"
	}
	return base
}

// InsertSaleLine writes a sale_lines row.
func (r *POSRepo) InsertSaleLine(ctx context.Context, tx *sql.Tx, lineID, saleID string, lineNo int, itemID, variantID, name, sku, barcode string, qty float64, unitPrice, lineDiscount int64, taxRateBP int, taxAmount, totalBeforeTax, totalAfterTax int64) error {
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO sale_lines (id, sale_id, line_no, item_id, variant_id, name_snapshot, sku_snapshot, barcode_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, lineID, saleID, lineNo, nullIfEmpty(itemID), nullIfEmpty(variantID), name, sku, barcode, qty, unitPrice, lineDiscount, taxRateBP, taxAmount, totalBeforeTax, totalAfterTax)
	if err != nil {
		return fmt.Errorf("insert sale line: %w", err)
	}
	return nil
}

// CardPresentFields is optional, provider-agnostic reconciliation metadata
// a locally-attached card terminal (e.g. a future ZVT integration,
// ut-docs#515) supplies on a payment (ut-docs#543). All empty for every
// payment method today (cash, Stripe, SumUp, QR-pay, demo). MaskedPAN must
// never be a full card number -- masking is enforced at the caller's
// boundary (pos.CompleteSale), not here.
type CardPresentFields struct {
	MaskedPAN  string
	AuthCode   string
	TerminalID string
	TraceID    string
}

// InsertPayment writes a payment row. tipAmount is gratuity metadata
// (docs/germany-pos-parity-backlog.md tip-flow gap) -- it rides alongside
// amount but is never subtracted/added when deriving what the payment
// applies toward the sale total; see pos.PaymentInput.TipAmount.
// tipRecipient (ADR-0061 Decision 3) records whose money the tip is
// ("employee"/"business") as decided at capture time -- the caller
// (pos.CompleteSale) validates and defaults it, this layer stores it as-is.
// voucherID (ut-docs#1053, migration 072) records WHICH tracked voucher a
// 'voucher'-method payment redeemed -- empty for every other payment. The
// caller (pos.CompleteSale) validates it; this layer stores it as-is, a
// soft reference with no FK (see migration 072's header).
func (r *POSRepo) InsertPayment(ctx context.Context, tx *sql.Tx, paymentID, saleID, methodID string, amount int64, currency, reference string, changeGiven int64, tipAmount int64, tipRecipient string, voucherID string, paidAt string, cardPresent CardPresentFields) error {
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO payments (id, sale_id, method_id, amount, currency, reference, change_given, tip_amount, tip_recipient, voucher_id, masked_pan, auth_code, terminal_id, trace_id, paid_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, paymentID, saleID, methodID, amount, currency, nullIfEmpty(reference), changeGiven, tipAmount, tipRecipient, nullIfEmpty(voucherID),
		nullIfEmpty(cardPresent.MaskedPAN), nullIfEmpty(cardPresent.AuthCode), nullIfEmpty(cardPresent.TerminalID), nullIfEmpty(cardPresent.TraceID), paidAt)
	if err != nil {
		return fmt.Errorf("insert payment: %w", err)
	}
	return nil
}

// InsertSaleLink writes a sale link row.
func (r *POSRepo) InsertSaleLink(ctx context.Context, tx *sql.Tx, id, saleID, originalSaleID, reason string) error {
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO sale_links (id, sale_id, original_sale_id, reason)
VALUES (?, ?, ?, ?)
`, id, saleID, originalSaleID, reason)
	if err != nil {
		return fmt.Errorf("insert sale link: %w", err)
	}
	return nil
}

// UpsertInventory adjusts inventory quantities.
func (r *POSRepo) UpsertInventory(ctx context.Context, tx *sql.Tx, locationID, itemID, variantID string, qty float64, updatedAt string) error {
	exec := r.exec(tx)
	res, err := exec.ExecContext(ctx, `
UPDATE inventory
SET quantity = quantity + ?, updated_at = ?
WHERE location_id = ?
  AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL))
`, qty, updatedAt, locationID, nullIfEmpty(itemID), nullIfEmpty(variantID))
	if err != nil {
		return fmt.Errorf("update inventory: %w", err)
	}
	aff, _ := res.RowsAffected()
	if aff == 0 {
		if _, err := exec.ExecContext(ctx, `
INSERT INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
`, uuid.NewString(), nullIfEmpty(itemID), nullIfEmpty(variantID), locationID, qty, updatedAt); err != nil {
			return fmt.Errorf("insert inventory: %w", err)
		}
	}
	return nil
}

// InsertAudit writes an audit_log entry (id optional). blocked_actor_id is
// always NULL — the ordinary, non-elevated path. See InsertAuditElevated
// for the manager-override-elevation variant (ut-docs#557).
func (r *POSRepo) InsertAudit(ctx context.Context, tx *sql.Tx, actorID, entityType, entityID, action string, payload any, createdAt string, id string) error {
	return r.insertAudit(ctx, tx, actorID, "", entityType, entityID, action, payload, createdAt, id)
}

// InsertAuditElevated writes an audit_log entry recording BOTH the actor who
// performed the action (actorID — the approving user once elevation
// succeeded) and the originally-blocked session user (blockedActorID),
// dual attribution for manager-override elevation (ut-docs#557,
// internal/pages/elevation.go's checkOrElevate). blockedActorID must be
// non-empty; pass InsertAudit instead for the ordinary, non-elevated case.
func (r *POSRepo) InsertAuditElevated(ctx context.Context, tx *sql.Tx, actorID, blockedActorID, entityType, entityID, action string, payload any, createdAt, id string) error {
	return r.insertAudit(ctx, tx, actorID, blockedActorID, entityType, entityID, action, payload, createdAt, id)
}

func (r *POSRepo) insertAudit(ctx context.Context, tx *sql.Tx, actorID, blockedActorID, entityType, entityID, action string, payload any, createdAt string, id string) error {
	if id == "" {
		id = uuid.NewString()
	}
	data, _ := json.Marshal(payload)
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO audit_log (id, actor_id, blocked_actor_id, entity_type, entity_id, action, data_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, id, nullIfEmpty(actorID), nullIfEmpty(blockedActorID), entityType, entityID, action, string(data), createdAt)
	if err != nil {
		return fmt.Errorf("insert audit_log: %w", err)
	}
	return nil
}

// HasAuditEntry reports whether an audit_log row exists for the given
// entity/action pair — used by the receipt-printing path (ADR-0048) to
// derive a sale's unsigned-override marker from the sale's own authoritative
// audit row rather than re-reading current fiscal settings, since printing
// (especially a reprint) can happen well after the override window that was
// active when the sale itself completed.
func (r *POSRepo) HasAuditEntry(ctx context.Context, entityType, entityID, action string) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx, `
SELECT 1 FROM audit_log
WHERE entity_type = ? AND entity_id = ? AND action = ?
LIMIT 1
`, entityType, entityID, action).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check audit_log entry: %w", err)
	}
	return true, nil
}

// LatestLocalSaleID returns the id of the most recently created sale rung up
// on THIS till, or ok=false when this till has none yet — used by the
// fiscalisation status chip (ut-docs#685) to find "the till's own
// last-signing outcome" without a new fiscal.status.ask extension point
// (none exists; ADR-0044 registers only fiscal.sign.ask).
//
// An empty till_id is the same "this till's own sales" filter
// LocalSalesSince uses (ADR-0011 D3: a journaled-in replica sale is stamped with its source
// till by SetSaleProvenance, along with the ORIGIN's created_at — so on a
// primary the newest row overall is routinely a REPLICA's sale). Without
// this filter the chip would decide the local till's health from a foreign
// sale that, by construction, can never carry a local
// unsigned_fiscal_signing marker (sync_sales.go's applyJournal replays a
// sale through CompleteSale, never through completeTender's fiscal.sign.ask
// hook) — a silent false green on exactly the condition the chip exists to
// surface. `rowid` breaks a same-second created_at tie by real insertion
// order, so the answer is stable across the chip's 30s polls instead of
// flickering between two equally-timestamped sales.
func (r *POSRepo) LatestLocalSaleID(ctx context.Context) (string, bool, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `
SELECT id FROM sales WHERE till_id = '' ORDER BY created_at DESC, rowid DESC LIMIT 1
`).Scan(&id)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("latest local sale id: %w", err)
	}
	return id, true, nil
}

// CountUnresolvedAuditActionsSince counts distinct entities of entityType
// carrying at least one of actions as an audit_log row at or after since,
// excluding any entity that also carries a "fiscal_signing_resolved" row
// (the historical resolved-marker shape a pre-ADR-0056 build could still
// have written — see saleFiscalSigningGapKind in
// internal/pages/fiscal_sign_hook.go, whose read-side logic this mirrors at
// the aggregate level). Generic by design — entityType/actions are caller
// params, not hardcoded — so any future "how many X have an unresolved Y"
// chip can reuse it instead of a fiscal-specific query.
func (r *POSRepo) CountUnresolvedAuditActionsSince(ctx context.Context, entityType string, actions []string, since time.Time) (int, error) {
	if len(actions) == 0 {
		return 0, nil
	}
	placeholders := strings.Repeat("?,", len(actions))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(actions)+3)
	args = append(args, entityType)
	for _, a := range actions {
		args = append(args, a)
	}
	args = append(args, since.UTC().Format(time.RFC3339), entityType)
	query := fmt.Sprintf(`
SELECT COUNT(DISTINCT al.entity_id) FROM audit_log al
WHERE al.entity_type = ? AND al.action IN (%s) AND al.created_at >= ?
  AND NOT EXISTS (
    SELECT 1 FROM audit_log r
    WHERE r.entity_type = ? AND r.entity_id = al.entity_id AND r.action = 'fiscal_signing_resolved'
  )
`, placeholders)
	var n int
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count unresolved audit actions: %w", err)
	}
	return n, nil
}

// AuditEntry is one row for the audit-trail browse/filter page. ActorName
// is resolved via a LEFT JOIN on users — plugin-originated entries
// (internal/data/plugin_repo.go's InsertAudit/InsertAuditRaw) always write
// actor_id NULL, so ActorName is empty for those, not a display bug.
type AuditEntry struct {
	ID         string
	ActorID    string
	ActorName  string
	EntityType string
	EntityID   string
	Action     string
	DataJSON   string
	CreatedAt  string
	// BlockedActorID is the originally-blocked session user's id, set only
	// on an elevated entry (InsertAuditElevated, ut-docs#557) — empty for
	// every ordinary entry, including every row written before migration
	// 049 added the column.
	BlockedActorID string
}

// AuditFilters narrows ListAudit; zero values mean "no filter" on that field.
type AuditFilters struct {
	EntityType string
	ActorID    string
	Action     string // substring match
	Since      string // inclusive, ISO-8601/RFC3339 or a bare YYYY-MM-DD date
	Until      string // inclusive — a bare date means end of that day, see endOfDayIfBareDate
	Limit      int
	Offset     int
}

// endOfDayIfBareDate turns a bare "YYYY-MM-DD" into that day's last instant
// so an inclusive Until filter actually includes the chosen day. created_at
// is always a full RFC3339 timestamp ("2026-01-01T10:00:00Z"), and
// lexicographic comparison means the bare date alone sorts BEFORE every
// timestamp on that same day — "2026-01-01" <= "2026-01-01T10:00:00Z" is
// false — so an unmodified date-only Until would silently exclude the
// entire end day. Anything that isn't exactly 10 chars (already a full
// timestamp, or some other format) passes through unchanged.
func endOfDayIfBareDate(v string) string {
	if len(v) == 10 {
		return v + "T23:59:59Z"
	}
	return v
}

// buildAuditWhere builds the shared WHERE clause + args for AuditFilters,
// used by both ListAudit and ListAuditForExport so the bare-date Until fix
// (endOfDayIfBareDate) can't drift between a paginated-browse code path and
// a bulk-export one — see docs/code-reviews/2026-07-24-audit-trail-page.md
// for the bug this already caused once.
func buildAuditWhere(f AuditFilters) (string, []any) {
	var where []string
	var args []any
	if f.EntityType != "" {
		where = append(where, "a.entity_type = ?")
		args = append(args, f.EntityType)
	}
	if f.ActorID != "" {
		where = append(where, "a.actor_id = ?")
		args = append(args, f.ActorID)
	}
	if f.Action != "" {
		where = append(where, "a.action LIKE ?")
		args = append(args, "%"+f.Action+"%")
	}
	if f.Since != "" {
		where = append(where, "a.created_at >= ?")
		args = append(args, f.Since)
	}
	if f.Until != "" {
		where = append(where, "a.created_at <= ?")
		args = append(args, endOfDayIfBareDate(f.Until))
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	return clause, args
}

const auditExportCeiling = 10000

// ListAudit returns audit_log rows newest-first, matching all supplied
// filters (AND). Manager-gated at the handler — this reads system-wide
// history, not scoped to the caller.
func (r *POSRepo) ListAudit(ctx context.Context, f AuditFilters) ([]AuditEntry, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	whereClause, args := buildAuditWhere(f)
	query := `
SELECT a.id, COALESCE(a.actor_id, ''), COALESCE(u.display_name, ''),
       a.entity_type, a.entity_id, a.action, COALESCE(a.data_json, ''), a.created_at,
       COALESCE(a.blocked_actor_id, '')
FROM audit_log a
LEFT JOIN users u ON u.id = a.actor_id` + whereClause + `
ORDER BY a.created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, f.Offset)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit_log: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.ActorID, &e.ActorName, &e.EntityType, &e.EntityID, &e.Action, &e.DataJSON, &e.CreatedAt, &e.BlockedActorID); err != nil {
			return nil, fmt.Errorf("scan audit_log: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list audit_log: %w", err)
	}
	return out, nil
}

// ListAuditForExport returns every AuditEntry matching f (no pagination —
// f.Limit/f.Offset are ignored), up to a hard ceiling so a pathological
// filter can't exhaust memory reading the live SQLite file. If more than
// ceiling rows match, the result is truncated to ceiling and truncated=true.
func (r *POSRepo) ListAuditForExport(ctx context.Context, f AuditFilters) ([]AuditEntry, bool, error) {
	return r.listAuditForExportWithCeiling(ctx, f, auditExportCeiling)
}

func (r *POSRepo) listAuditForExportWithCeiling(ctx context.Context, f AuditFilters, ceiling int) ([]AuditEntry, bool, error) {
	whereClause, args := buildAuditWhere(f)
	query := `
SELECT a.id, COALESCE(a.actor_id, ''), COALESCE(u.display_name, ''),
       a.entity_type, a.entity_id, a.action, COALESCE(a.data_json, ''), a.created_at,
       COALESCE(a.blocked_actor_id, '')
FROM audit_log a
LEFT JOIN users u ON u.id = a.actor_id` + whereClause + `
ORDER BY a.created_at DESC LIMIT ?`
	args = append(args, ceiling+1)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list audit_log for export: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.ActorID, &e.ActorName, &e.EntityType, &e.EntityID, &e.Action, &e.DataJSON, &e.CreatedAt, &e.BlockedActorID); err != nil {
			return nil, false, fmt.Errorf("scan audit_log: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list audit_log for export: %w", err)
	}
	truncated := len(out) > ceiling
	if truncated {
		out = out[:ceiling]
	}
	return out, truncated, nil
}

// DistinctAuditEntityTypes powers the entity-type filter dropdown.
func (r *POSRepo) DistinctAuditEntityTypes(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT entity_type FROM audit_log ORDER BY entity_type`)
	if err != nil {
		return nil, fmt.Errorf("distinct audit entity types: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan audit entity type: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ResetTransactionHistory moved to reset_archive_repo.go (ADR-0042): a reset
// now archives into the *_archive tables instead of deleting, alongside
// ListResetBatches and RestoreResetBatch.

// CustomerSummary is a row in the customer picker for data management.
type CustomerSummary struct {
	ID    string
	Name  string
	Phone string
	Email string
}

// SearchCustomers finds customers by name/phone/email for the erasure picker.
func (r *POSRepo) SearchCustomers(ctx context.Context, q string, limit int) ([]CustomerSummary, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	like := "%" + strings.TrimSpace(q) + "%"
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, COALESCE(phone,''), COALESCE(email,'')
FROM customers
WHERE name LIKE ? OR phone LIKE ? OR email LIKE ?
ORDER BY name LIMIT ?`, like, like, like, limit)
	if err != nil {
		return nil, fmt.Errorf("search customers: %w", err)
	}
	defer rows.Close()
	var out []CustomerSummary
	for rows.Next() {
		var c CustomerSummary
		if err := rows.Scan(&c.ID, &c.Name, &c.Phone, &c.Email); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// EraseCustomer removes a customer's personal data (GDPR right to erasure):
// it unlinks the customer from sales (live AND archived) and promotions
// (keeping the sales, which are financial records, but anonymous) and
// deletes the customer row. Audited. Returns false if no such customer.
//
// The sales_archive unlink (ut-docs#640, independent review) closes the
// same gap ErrArchiveReferencesRemoved documents for CleanupObsoleteItems
// and the demo-data removal script: right after a reset-transactions run,
// this customer's sale sits in sales_archive, invisible to a LIVE-only
// unlink — deleting the customer row without also anonymising that
// archived row would leave it dangling, and a later RestoreResetBatch
// would then hit a live FK it can no longer satisfy
// (sales.customer_id -> customers). An earlier version of this fix
// refused the erasure outright instead — rejected on further review: any
// batch that can trigger the refusal necessarily has sales_count > 0, so
// DeleteResetBatch's retention window (10 years by default) AND
// RestoreResetBatch's "till has traded since" refusal (true after the
// shop's very next sale) together make "restore or purge it first" an
// almost always impossible instruction — a GDPR Article 17 erasure
// request would sit unfulfillable for years. Anonymising the archived
// copy the same way the live copy already is has no such trap: archive
// tables carry no FK to live tables (migration 040's own header), nothing
// else in this codebase reads sales_archive.customer_id, and it matches
// this function's own existing contract ("keeping the sales... but
// anonymous") instead of inventing a different rule for the archived half.
func (r *POSRepo) EraseCustomer(ctx context.Context, id, actorID string) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, q := range []string{
		`UPDATE sales SET customer_id = NULL WHERE customer_id = ?`,
		`UPDATE sales_archive SET customer_id = NULL WHERE customer_id = ?`,
		`UPDATE promotions SET customer_id = NULL WHERE customer_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return false, fmt.Errorf("erase customer unlink: %w", err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM customers WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("erase customer: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return false, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := r.InsertAudit(ctx, tx, actorID, "customer", id, "customer_erased", nil, now, ""); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// obsoleteItemsWhere selects items that are safe to permanently delete during
// catalog cleanup: they are already deactivated (is_active = 0) AND have no
// financial or stock history whatsoever — neither the item nor any of its
// variants appears in sale_lines or stock_movements, LIVE OR ARCHIVED.
// Anything ever sold or moved is KEPT (deactivated at most) so audit/tax
// history stays intact.
//
// The *_archive clauses (ut-docs#640) close a gap found in independent
// review of ut-docs#187 (see ErrArchiveReferencesRemoved's doc comment,
// reset_archive_repo.go): right after a reset-transactions run, the live
// sale_lines/stock_movements tables are empty — the real references sit in
// sale_lines_archive/stock_movements_archive instead, invisible to the
// clauses above on their own. Without this, catalog cleanup could delete an
// item a still-restorable archive batch depends on, and a later
// RestoreResetBatch would then hit a live FK it can no longer satisfy.
// item_variants itself is never archived (reset only clears transactional
// tables), so a variant_id recorded in an archive row still resolves
// against the live item_variants table exactly like the live clauses above.
const obsoleteItemsWhere = `
is_active = 0
AND id NOT IN (SELECT item_id FROM sale_lines WHERE item_id IS NOT NULL)
AND id NOT IN (SELECT item_id FROM stock_movements WHERE item_id IS NOT NULL)
AND id NOT IN (SELECT v.item_id FROM item_variants v
              WHERE v.id IN (SELECT variant_id FROM sale_lines WHERE variant_id IS NOT NULL))
AND id NOT IN (SELECT v.item_id FROM item_variants v
              WHERE v.id IN (SELECT variant_id FROM stock_movements WHERE variant_id IS NOT NULL))
AND id NOT IN (SELECT item_id FROM sale_lines_archive WHERE item_id IS NOT NULL)
AND id NOT IN (SELECT item_id FROM stock_movements_archive WHERE item_id IS NOT NULL)
AND id NOT IN (SELECT v.item_id FROM item_variants v
              WHERE v.id IN (SELECT variant_id FROM sale_lines_archive WHERE variant_id IS NOT NULL))
AND id NOT IN (SELECT v.item_id FROM item_variants v
              WHERE v.id IN (SELECT variant_id FROM stock_movements_archive WHERE variant_id IS NOT NULL))`

// ObsoleteItem is a row in the catalog-cleanup preview.
type ObsoleteItem struct {
	ID   string
	SKU  string
	Name string
}

// ListObsoleteItems returns the inactive, never-sold items that CleanupObsoleteItems
// would remove, so the manager can preview before confirming.
func (r *POSRepo) ListObsoleteItems(ctx context.Context, limit int) ([]ObsoleteItem, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, COALESCE(sku,''), name FROM items
WHERE `+obsoleteItemsWhere+`
ORDER BY name LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list obsolete items: %w", err)
	}
	defer rows.Close()
	var out []ObsoleteItem
	for rows.Next() {
		var it ObsoleteItem
		if err := rows.Scan(&it.ID, &it.SKU, &it.Name); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// CleanupObsoleteItems permanently deletes inactive, never-sold items and their
// operational children (inventory levels, price history; barcodes/images/variants
// cascade). Items with any sale or stock history are left untouched. Audited.
// Returns the number of items removed.
func (r *POSRepo) CleanupObsoleteItems(ctx context.Context, actorID string) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// The set of item ids we're about to delete, and the variants under them.
	itemSet := `SELECT id FROM items WHERE ` + obsoleteItemsWhere
	variantSet := `SELECT id FROM item_variants WHERE item_id IN (` + itemSet + `)`

	// Operational children without ON DELETE CASCADE must go first.
	for _, s := range []string{
		`DELETE FROM inventory     WHERE item_id IN (` + itemSet + `) OR variant_id IN (` + variantSet + `)`,
		`DELETE FROM price_history WHERE item_id IN (` + itemSet + `) OR variant_id IN (` + variantSet + `)`,
	} {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return 0, fmt.Errorf("cleanup children: %w", err)
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM items WHERE `+obsoleteItemsWhere)
	if err != nil {
		return 0, fmt.Errorf("cleanup items: %w", err)
	}
	count, _ := res.RowsAffected()
	if count == 0 {
		return 0, nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := r.InsertAudit(ctx, tx, actorID, "system", "catalog", "catalog_cleanup",
		map[string]any{"items_deleted": count}, now, ""); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}

// UpdateSaleStatus updates sale status (and optionally voided_at when voided).
// ErrSaleNotFound reports a status update against a sale id that doesn't
// exist — callers distinguish it (404) from validation failures (400).
var ErrSaleNotFound = errors.New("sale not found")

func (r *POSRepo) UpdateSaleStatus(ctx context.Context, tx *sql.Tx, saleID, status string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := r.exec(tx).ExecContext(ctx, `
UPDATE sales
SET status = ?, voided_at = CASE WHEN ? = 'voided' THEN ? ELSE voided_at END
WHERE id = ?
`, status, status, now, saleID)
	if err != nil {
		return fmt.Errorf("update sale status: %w", err)
	}
	if n, rerr := res.RowsAffected(); rerr == nil && n == 0 {
		return fmt.Errorf("update sale status: %w: %s", ErrSaleNotFound, saleID)
	}
	return nil
}

// RecordPaymentFailure logs a recoverable payment failure attempt for later retry/audit.
func (r *POSRepo) RecordPaymentFailure(ctx context.Context, failure PaymentFailure) (string, error) {
	saleID := failure.SaleID
	if saleID == "" {
		saleID = uuid.NewString()
	}
	payload := map[string]any{
		"reason":   failure.Reason,
		"payments": failure.Payments,
		"lines":    failure.Lines,
		"total":    failure.Total,
		"currency": failure.Currency,
		"ts":       time.Now().UTC().Format(time.RFC3339),
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := r.InsertAudit(ctx, nil, failure.ActorID, "sale", saleID, "payment_failed", payload, now, ""); err != nil {
		return "", err
	}
	return saleID, nil
}

// PaymentFailure captures failed payment data for audit.
type PaymentFailure struct {
	SaleID   string
	ActorID  string
	Reason   string
	Payments []any
	Lines    []any
	Total    int64
	Currency string
}

// FindOpenShiftForRegister returns an open shift id for the register, if any.
func (r *POSRepo) FindOpenShiftForRegister(ctx context.Context, tx *sql.Tx, registerID string) (string, error) {
	var existingID string
	err := r.exec(tx).QueryRowContext(ctx, `
SELECT id FROM shifts 
WHERE register_id = ? AND closed_at IS NULL
LIMIT 1`, registerID).Scan(&existingID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("check existing shift: %w", err)
	}
	return existingID, nil
}

// InsertShift inserts a shift row.
func (r *POSRepo) InsertShift(ctx context.Context, tx *sql.Tx, shiftID, registerID, cashierID string, openingCash int64, openedAt string) error {
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO shifts (id, register_id, cashier_id, opened_at, opening_cash)
VALUES (?, ?, ?, ?, ?)
`, shiftID, registerID, cashierID, openedAt, openingCash)
	if err != nil {
		return fmt.Errorf("insert shift: %w", err)
	}
	return nil
}

// LoadShiftForClose fetches shift info required to close.
func (r *POSRepo) LoadShiftForClose(ctx context.Context, tx *sql.Tx, shiftID string) (string, string, int64, error) {
	var registerID, cashierID string
	var openingCash int64
	err := r.exec(tx).QueryRowContext(ctx, `
SELECT register_id, cashier_id, opening_cash
FROM shifts 
WHERE id = ? AND closed_at IS NULL
`, shiftID).Scan(&registerID, &cashierID, &openingCash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", 0, errors.New("shift not found or already closed")
		}
		return "", "", 0, fmt.Errorf("query shift: %w", err)
	}
	return registerID, cashierID, openingCash, nil
}

// UpdateShiftClose sets closing details. newFloat is the drawer's
// carry-forward after close (counted closing cash minus any skim recorded
// with the close — ut-docs#1006); countProtocol is the optional
// denomination-count JSON blob, stored NULL when empty.
func (r *POSRepo) UpdateShiftClose(ctx context.Context, tx *sql.Tx, shiftID string, closingCash, expectedCash, newFloat int64, note, countProtocol string, closedAt string) error {
	_, err := r.exec(tx).ExecContext(ctx, `
UPDATE shifts
SET closed_at = ?, closing_cash = ?, expected_cash = ?, new_float = ?, note = ?, count_protocol = ?
WHERE id = ?
`, closedAt, closingCash, expectedCash, newFloat, nullIfEmpty(note), nullIfEmpty(countProtocol), shiftID)
	if err != nil {
		return fmt.Errorf("update shift: %w", err)
	}
	return nil
}

// LastClosedShiftCarryForward returns the cash the most recent closed shift
// on a register left in the drawer — its new_float when recorded, else its
// closing_cash (a shift closed before ut-docs#1006, or by older code, has
// no new_float; the full counted amount stayed in the drawer then). ok is
// false when the register has no closed shift yet. This is what an omitted
// opening_cash on shift-open defaults to, so the float carries forward
// instead of being re-typed.
func (r *POSRepo) LastClosedShiftCarryForward(ctx context.Context, registerID string) (int64, bool, error) {
	var carry int64
	err := r.db.QueryRowContext(ctx, `
SELECT COALESCE(new_float, closing_cash, 0)
FROM shifts
WHERE register_id = ? AND closed_at IS NOT NULL
ORDER BY closed_at DESC
LIMIT 1`, registerID).Scan(&carry)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("last closed shift carry-forward: %w", err)
	}
	return carry, true, nil
}

// ShiftOpenExists checks if shift is open.
func (r *POSRepo) ShiftOpenExists(ctx context.Context, tx *sql.Tx, shiftID string) (bool, error) {
	var exists int
	err := r.exec(tx).QueryRowContext(ctx, `
SELECT 1 FROM shifts WHERE id = ? AND closed_at IS NULL
`, shiftID).Scan(&exists)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("query shift: %w", err)
	}
	return true, nil
}

// SumCashPaymentsForShift sums cash payments in shift window for a register.
func (r *POSRepo) SumCashPaymentsForShift(ctx context.Context, registerID string, openedAt, closedAt sql.NullString) (int64, error) {
	timeFilter := `s.completed_at >= ?`
	args := []any{registerID, openedAt.String}
	if closedAt.Valid {
		timeFilter += ` AND s.completed_at <= ?`
		args = append(args, closedAt.String)
	}
	query := fmt.Sprintf(`
SELECT COALESCE(SUM(p.amount - p.change_given), 0)
FROM payments p
JOIN sales s ON s.id = p.sale_id
JOIN payment_methods pm ON pm.id = p.method_id
WHERE pm.type = 'cash'
  AND s.status = 'completed'
  AND s.register_id = ?
  AND %s
`, timeFilter)
	var cashPayments int64
	if err := r.db.QueryRowContext(ctx, query, args...).Scan(&cashPayments); err != nil {
		return 0, fmt.Errorf("sum cash payments: %w", err)
	}
	return cashPayments, nil
}

// SumShiftAdjustments returns total adjustments for a shift.
func (r *POSRepo) SumShiftAdjustments(ctx context.Context, shiftID string) (int64, error) {
	var adjustments int64
	if err := r.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(
	CAST(json_extract(data_json, '$.amount') AS INTEGER)
), 0)
FROM audit_log
WHERE entity_type = 'shift'
  AND entity_id = ?
  AND action = 'cash_adjustment'
`, shiftID).Scan(&adjustments); err != nil {
		return 0, fmt.Errorf("sum adjustments: %w", err)
	}
	return adjustments, nil
}

// ComputeExpectedCash calculates expected cash for a shift.
func (r *POSRepo) ComputeExpectedCash(ctx context.Context, shiftID string, openingCash int64) (int64, error) {
	if shiftID == "" {
		return 0, errors.New("shift_id required")
	}
	var openedAt, closedAt sql.NullString
	var registerID string
	if err := r.db.QueryRowContext(ctx, `
SELECT register_id, opened_at, closed_at
FROM shifts
WHERE id = ?
`, shiftID).Scan(&registerID, &openedAt, &closedAt); err != nil {
		return 0, fmt.Errorf("query shift: %w", err)
	}
	cashPayments, err := r.SumCashPaymentsForShift(ctx, registerID, openedAt, closedAt)
	if err != nil {
		return 0, err
	}
	adjustments, err := r.SumShiftAdjustments(ctx, shiftID)
	if err != nil {
		return 0, err
	}
	return openingCash + cashPayments + adjustments, nil
}

// LookupCustomer resolves a customer by id/loyalty/phone.
func (r *POSRepo) LookupCustomer(ctx context.Context, code string) (string, string, bool) {
	c := strings.TrimSpace(code)
	if c == "" {
		return "", "", false
	}
	row := r.db.QueryRowContext(ctx, `
SELECT id, name FROM customers
WHERE lower(id) = lower(?) OR lower(loyalty_no) = lower(?) OR phone = ?
LIMIT 1
`, c, c, c)
	var id, name string
	if err := row.Scan(&id, &name); err != nil {
		return "", "", false
	}
	return id, name, true
}

// FindActivePromo returns promo type/value if active and optionally targeted to the given customer.
func (r *POSRepo) FindActivePromo(ctx context.Context, customerID string, code string) (string, int64, bool) {
	row := r.db.QueryRowContext(ctx, `
SELECT type, value
FROM promotions
WHERE code = ?
  AND is_active = 1
  AND (customer_id IS NULL OR customer_id = ?)
  AND (starts_at IS NULL OR datetime(starts_at) <= CURRENT_TIMESTAMP)
  AND (ends_at IS NULL OR datetime(ends_at) >= CURRENT_TIMESTAMP)
LIMIT 1
`, strings.TrimSpace(code), nullIfEmpty(customerID))
	var pType string
	var value int64
	if err := row.Scan(&pType, &value); err != nil {
		return "", 0, false
	}
	if value <= 0 {
		return "", 0, false
	}
	if pType == "" {
		pType = "amount"
	}
	return pType, value, true
}

// isUniqueViolation reports whether err is a SQLite UNIQUE constraint
// failure. Same string-matching approach as isForeignKeyViolation in
// reset_archive_repo.go -- modernc.org/sqlite (this project's driver)
// doesn't export a typed error the way mattn/go-sqlite3 does.
func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// ErrPromotionCodeExists is returned by CreatePromotion when the code is
// already taken (promotions.code is the PRIMARY KEY) -- callers should
// present this distinctly (e.g. "?err=code_exists") rather than a generic
// failure.
var ErrPromotionCodeExists = errors.New("promotion code already exists")

// PromotionInput is the editable shape of a promo code -- everything except
// the code itself (the PRIMARY KEY, immutable once created). Value is a raw
// int64 at rest: minor currency units when Type is "amount", basis points
// when Type is "percent" (1% = 100) -- matching FindActivePromo's own
// interpretation in pos_api.go. Money-boundary conversion for the "amount"
// case happens at the UI-form layer (promotions_page.go), not here.
type PromotionInput struct {
	Type        string
	Value       int64
	Description string
	StartsAt    string
	EndsAt      string
	CustomerID  string
}

// CreatePromotion adds a new, active promo code. Real merchant-created rows
// are never sample data -- is_sample_data (migration 038) is left at its
// column default (0) here, deliberately untouched.
func (r *POSRepo) CreatePromotion(ctx context.Context, code string, in PromotionInput) error {
	_, err := r.db.ExecContext(ctx, `
INSERT INTO promotions (code, type, value, description, starts_at, ends_at, customer_id, is_active)
VALUES (?, ?, ?, ?, ?, ?, ?, 1)`,
		strings.TrimSpace(code), in.Type, in.Value, nullIfEmpty(in.Description),
		nullIfEmpty(in.StartsAt), nullIfEmpty(in.EndsAt), nullIfEmpty(in.CustomerID))
	if err != nil {
		if isUniqueViolation(err) {
			return ErrPromotionCodeExists
		}
		return fmt.Errorf("create promotion: %w", err)
	}
	return nil
}

// UpdatePromotion edits an existing promotion's type/value/description/
// dates/customer target. The code (PRIMARY KEY) is the promo's identity and
// is never rewritten here.
func (r *POSRepo) UpdatePromotion(ctx context.Context, code string, in PromotionInput) error {
	res, err := r.db.ExecContext(ctx, `
UPDATE promotions SET type = ?, value = ?, description = ?, starts_at = ?, ends_at = ?, customer_id = ?
WHERE code = ?`,
		in.Type, in.Value, nullIfEmpty(in.Description), nullIfEmpty(in.StartsAt),
		nullIfEmpty(in.EndsAt), nullIfEmpty(in.CustomerID), strings.TrimSpace(code))
	if err != nil {
		return fmt.Errorf("update promotion: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("update promotion: %s not found", code)
	}
	return nil
}

// SetPromotionActive soft-deactivates/reactivates a promo code -- mirrors
// SetStockLocationActive/SetUserActive's pattern. The row is never hard-
// deleted so redemption history is preserved; a deactivated code simply
// stops matching FindActivePromo's `is_active = 1` filter.
func (r *POSRepo) SetPromotionActive(ctx context.Context, code string, active bool) error {
	v := 0
	if active {
		v = 1
	}
	res, err := r.db.ExecContext(ctx, `UPDATE promotions SET is_active = ? WHERE code = ?`, v, strings.TrimSpace(code))
	if err != nil {
		return fmt.Errorf("set promotion active: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set promotion active: %s not found", code)
	}
	return nil
}

// PromotionAdmin is a promo code as the promotions management page needs
// it: every row (active and inactive), unlike FindActivePromo's active-
// only, in-window, checkout-time lookup.
type PromotionAdmin struct {
	Code        string
	Type        string
	Value       int64
	Description string
	StartsAt    string
	EndsAt      string
	CustomerID  string
	IsActive    bool
}

// ListPromotionsForAdmin returns every promo code (active and inactive),
// active-first then alphabetical, for the promotions management page.
// FindActivePromo's own query is separate and untouched by this addition.
func (r *POSRepo) ListPromotionsForAdmin(ctx context.Context) ([]PromotionAdmin, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT code, type, value, COALESCE(description,''), COALESCE(starts_at,''), COALESCE(ends_at,''), COALESCE(customer_id,''), is_active
FROM promotions
ORDER BY is_active DESC, code ASC`)
	if err != nil {
		return nil, fmt.Errorf("list promotions for admin: %w", err)
	}
	defer rows.Close()
	var out []PromotionAdmin
	for rows.Next() {
		var p PromotionAdmin
		var active int
		if err := rows.Scan(&p.Code, &p.Type, &p.Value, &p.Description, &p.StartsAt, &p.EndsAt, &p.CustomerID, &active); err != nil {
			return nil, fmt.Errorf("scan promotion admin: %w", err)
		}
		p.IsActive = active == 1
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate promotions for admin: %w", err)
	}
	return out, nil
}

// EnsureStockLocation returns an existing location id or creates a default one.
func (r *POSRepo) EnsureStockLocation(ctx context.Context) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM stock_locations WHERE name = 'Main' OR id = 'loc_main' ORDER BY id LIMIT 1`).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = "loc_main"
	if _, err := r.db.ExecContext(ctx, `INSERT INTO stock_locations(id, name) VALUES(?,?)`, id, "Main"); err != nil {
		return "", err
	}
	return id, nil
}

// CreateStockLocation adds a new, active stock location. The schema's
// UNIQUE constraint on name rejects duplicates.
func (r *POSRepo) CreateStockLocation(ctx context.Context, name string) (string, error) {
	id := uuid.NewString()
	if _, err := r.db.ExecContext(ctx,
		`INSERT INTO stock_locations (id, name, is_active) VALUES (?, ?, 1)`, id, name); err != nil {
		return "", fmt.Errorf("create stock location: %w", err)
	}
	return id, nil
}

// RenameStockLocation changes a location's display name; the id (and every
// inventory/movement row keyed by it) is unaffected.
func (r *POSRepo) RenameStockLocation(ctx context.Context, id, newName string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE stock_locations SET name = ? WHERE id = ?`, newName, id)
	if err != nil {
		return fmt.Errorf("rename stock location: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("rename stock location: %s not found", id)
	}
	return nil
}

// SetStockLocationActive soft-disables/re-enables a location, mirroring
// SetUserActive's pattern.
func (r *POSRepo) SetStockLocationActive(ctx context.Context, id string, active bool) error {
	v := 0
	if active {
		v = 1
	}
	res, err := r.db.ExecContext(ctx, `UPDATE stock_locations SET is_active = ? WHERE id = ?`, v, id)
	if err != nil {
		return fmt.Errorf("set stock location active: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set stock location active: %s not found", id)
	}
	return nil
}

// StockLocationInUse reports whether any inventory, stock movement, or
// register still references this location — deactivating it would silently
// orphan that history.
func (r *POSRepo) StockLocationInUse(ctx context.Context, id string) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx, `
SELECT 1 WHERE EXISTS (SELECT 1 FROM inventory WHERE location_id = ?)
   OR EXISTS (SELECT 1 FROM stock_movements WHERE location_id = ?)
   OR EXISTS (SELECT 1 FROM registers WHERE location_id = ?)`,
		id, id, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check stock location in use: %w", err)
	}
	return exists == 1, nil
}

// ListActiveStockLocations returns only active locations, for pickers that
// must not offer a deactivated location as a destination (stock receive/
// adjust/return). ListStockLocations itself stays unfiltered — other
// existing callers may rely on seeing every location regardless of state.
func (r *POSRepo) ListActiveStockLocations(ctx context.Context) ([]StockLocation, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name FROM stock_locations WHERE is_active = 1 ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list active stock locations: %w", err)
	}
	defer rows.Close()
	var out []StockLocation
	for rows.Next() {
		var l StockLocation
		if err := rows.Scan(&l.ID, &l.Name); err != nil {
			return nil, fmt.Errorf("scan active stock location: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active stock locations: %w", err)
	}
	return out, nil
}

// CountActiveStockLocations counts active locations — guards "cannot
// deactivate the last location" (mirrors CountOtherActiveAdminsWithPIN's
// last-admin guard in internal/data/auth_repo.go).
func (r *POSRepo) CountActiveStockLocations(ctx context.Context) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM stock_locations WHERE is_active = 1`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active stock locations: %w", err)
	}
	return n, nil
}

// StockLocationAdmin is a stock location as the locations admin page needs
// it (includes the soft-disable state the plain picker list doesn't).
type StockLocationAdmin struct {
	ID       string
	Name     string
	IsActive bool
}

// ListStockLocationsForAdmin returns every location (active and inactive)
// for the locations management page. ListStockLocations stays unfiltered
// and IsActive-agnostic for existing pickers/callers.
func (r *POSRepo) ListStockLocationsForAdmin(ctx context.Context) ([]StockLocationAdmin, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name, is_active FROM stock_locations ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list stock locations for admin: %w", err)
	}
	defer rows.Close()
	var out []StockLocationAdmin
	for rows.Next() {
		var l StockLocationAdmin
		var active int
		if err := rows.Scan(&l.ID, &l.Name, &active); err != nil {
			return nil, fmt.Errorf("scan stock location admin: %w", err)
		}
		l.IsActive = active == 1
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stock locations for admin: %w", err)
	}
	return out, nil
}

// EnsurePaymentMethod upserts a minimal payment method to satisfy FK.
func (r *POSRepo) EnsurePaymentMethod(ctx context.Context, id string) error {
	var exists int
	if err := r.db.QueryRowContext(ctx, `SELECT 1 FROM payment_methods WHERE id = ? AND is_active = 1`, id).Scan(&exists); err == nil {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES (?, ?, 'cash', 1)`, id, id)
	return err
}

// EnsureRegister returns an existing register or creates a default one.
func (r *POSRepo) EnsureRegister(ctx context.Context) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM registers WHERE is_active = 1 ORDER BY id LIMIT 1`).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = "reg-default"
	if _, err := r.db.ExecContext(ctx, `INSERT INTO registers (id, name, is_active) VALUES (?, ?, 1)`, id, "Default Register"); err != nil {
		return "", err
	}
	return id, nil
}

// EnsureUser returns a default cashier user if none exists.
func (r *POSRepo) EnsureUser(ctx context.Context) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT id FROM users WHERE is_active = 1 ORDER BY id LIMIT 1`).Scan(&id)
	if err == nil && id != "" {
		return id, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = "cashier-default"
	if _, err := r.db.ExecContext(ctx, `INSERT INTO users (id, username, display_name, role, is_active) VALUES (?, ?, ?, 'cashier', 1)`, id, "cashier", "Default Cashier"); err != nil {
		return "", err
	}
	return id, nil
}

// SaleTotals returns receipt_no, subtotal, tax_total, total for a sale.
func (r *POSRepo) SaleTotals(ctx context.Context, saleID string) (string, int64, int64, int64, error) {
	var receiptNo string
	var dbSubtotal, dbTax, dbTotal int64
	err := r.db.QueryRowContext(ctx, `SELECT receipt_no, subtotal, tax_total, total FROM sales WHERE id = ?`, saleID).
		Scan(&receiptNo, &dbSubtotal, &dbTax, &dbTotal)
	return receiptNo, dbSubtotal, dbTax, dbTotal, err
}

// SaleCompletedAt returns the completed_at timestamp for a sale.
func (r *POSRepo) SaleCompletedAt(ctx context.Context, saleID string) (time.Time, bool, error) {
	var completedNS sql.NullString
	err := r.db.QueryRowContext(ctx, `SELECT completed_at FROM sales WHERE id = ?`, saleID).Scan(&completedNS)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("sale completed_at: %w", err)
	}
	completed := completedNS.String
	if strings.TrimSpace(completed) == "" {
		return time.Time{}, false, nil
	}
	ts, err := time.Parse(time.RFC3339, completed)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse completed_at: %w", err)
	}
	return ts, true, nil
}

// SaleDetail is everything the journal shows when a sale is opened. Also
// the LAN sync wire payload (internal/pages/sync_sales.go's journalSale) --
// the json tags below are load-bearing for universal-till/CLAUDE.md's
// snake_case rule on that surface, not just documentation (ut-docs#262).
type SaleDetail struct {
	ID         string `json:"id"`
	ReceiptNo  string `json:"receipt_no"`
	Status     string `json:"status"`
	SaleType   string `json:"sale_type"`
	TenderType string `json:"tender_type"`
	OrderType  string `json:"order_type"`
	// TableID/TableLabel (ut-docs#820, ADR-0054) are the dining table this
	// sale was served at -- both empty when none was assigned. TableLabel
	// is resolved via a join against `tables` in GetSaleDetail so callers
	// (the receipt/kitchen-ticket render paths) never need a lookup of
	// their own; it reflects the table's CURRENT label, same "snapshot vs.
	// live" trade-off order_type's own plain string avoids by not needing
	// one -- a renamed table changes how past receipts display it, which
	// is an acceptable, deliberately simple choice for a v1.
	TableID       string `json:"table_id,omitempty"`
	TableLabel    string `json:"table_label,omitempty"`
	Offline       bool   `json:"offline"`
	SyncStatus    string `json:"sync_status"`
	Currency      string `json:"currency"`
	Subtotal      int64  `json:"subtotal"`
	DiscountTotal int64  `json:"discount_total"`
	TaxTotal      int64  `json:"tax_total"`
	Total         int64  `json:"total"`
	ServiceCharge int64  `json:"service_charge"`
	// ServiceChargeTaxBasisBP (ADR-0061 Decision 4) is the flat rate the
	// service charge's tax was computed at when the sale was tendered, or 0
	// for the apportioned fail-closed default. It rides the LAN-sync journal
	// so a replay reproduces the ORIGINAL totals rather than re-deriving them
	// against the primary's own policy; omitempty keeps the wire additive --
	// a pre-ADR-0061 peer simply lacks the key, which reads as 0, exactly the
	// behaviour that peer had.
	ServiceChargeTaxBasisBP int `json:"service_charge_tax_basis_bp,omitempty"`
	// VoucherIssueTotal (ut-docs#1008 review F1, migration 069): the summed
	// face value of vouchers issued in this sale — in Total, in neither
	// Subtotal nor TaxTotal. saleIsTaxInclusive/pos.InferTaxInclusive read
	// it to balance the pricing-mode identity; omitempty keeps the LAN-sync
	// journal wire additive, same convention as the field above (a
	// pre-migration-068 peer's journal simply lacks the key, which reads as
	// 0 — correct, since such a peer cannot issue tracked vouchers yet).
	VoucherIssueTotal int64               `json:"voucher_issue_total,omitempty"`
	CreatedAt         string              `json:"created_at"`
	CashierID         string              `json:"cashier_id"`
	Lines             []SaleDetailLine    `json:"lines"`
	Payments          []SaleDetailPayment `json:"payments"`
	// Charges (ADR-0062, ut-docs#963/#984) is the itemized additive
	// statutory charge list read from sale_charges, in seq order. Empty for
	// every sale until step 2/3 of that ADR starts writing sale_charges rows
	// -- ServiceCharge/ServiceChargeTaxBasisBP above stay the source of
	// truth for a sale with zero or one charge. omitempty + the "charges"
	// JSON key are ADR-0062 Decision 7's LAN-sync journal shape: a
	// pre-ADR-0062 peer's journal simply lacks the key, which applyJournal
	// reads as "fall back to the scalar reconstruction" (step 3, ut-docs#986)
	// -- not a new code path, today's applyJournal logic kept as that
	// fallback branch.
	Charges []SaleCharge `json:"charges,omitempty"`
	// VoucherIssues (ut-docs#1053) are the multi-purpose vouchers ISSUED in
	// this sale, read from voucher_transactions (type='issue') joined to
	// vouchers on the soft sale_id reference (migration 068's header — no FK
	// to traverse). They ride the LAN-sync journal so applyJournal can
	// reconstruct pos.SaleInput.VoucherIssues on the primary; omitempty
	// keeps the wire additive, same convention as Charges above (a
	// pre-1.3.0 peer's journal simply lacks the key, which is exactly what
	// a voucher-free sale already looks like).
	VoucherIssues []SaleDetailVoucherIssue `json:"voucher_issues,omitempty"`
}

// SaleDetailVoucherIssue is one voucher issued in the sale (ut-docs#1053):
// the stable voucher id, the optional holder label, and the face value in
// minor units (= the issue transaction's amount = the voucher's
// original_amount and opening balance).
type SaleDetailVoucherIssue struct {
	VoucherID   string `json:"voucher_id"`
	HolderLabel string `json:"holder_label,omitempty"`
	Amount      int64  `json:"amount"`
}

type SaleDetailLine struct {
	Name         string   `json:"name"`
	SKU          string   `json:"sku"`
	ItemID       string   `json:"item_id"`
	VariantID    string   `json:"variant_id"`
	TaxRateBP    int      `json:"tax_rate_bp"`
	Qty          float64  `json:"qty"`
	UnitPrice    int64    `json:"unit_price"`
	LineDiscount int64    `json:"line_discount"`
	TaxAmount    int64    `json:"tax_amount"`
	LineTotal    int64    `json:"line_total"`
	Modifiers    []string `json:"modifiers"` // chosen customization option names (ADR-0020), e.g. "Extra shot"
	// OrderType (ut-docs#1181, ADR-0073): this line's own consumption mode,
	// "" (dine-in) or "takeaway" — never "mixed", which is a header-only
	// summary. omitempty keeps the LAN-sync journal wire additive (contract
	// 1.5.0): a pre-1.5.0 peer simply lacks the key, and applyJournal
	// treats an absent line value under a "takeaway" header as takeaway
	// (the only meaning it could have had on that peer).
	OrderType string `json:"order_type,omitempty"`
}

type SaleDetailPayment struct {
	Method      string `json:"method"`
	Amount      int64  `json:"amount"`
	ChangeGiven int64  `json:"change_given"`
	TipAmount   int64  `json:"tip_amount"`
	// TipRecipient (ADR-0061 Decision 3): "employee" or "business" -- whose
	// money the tip was recorded as at capture time. omitempty keeps the
	// journal wire additive: a pre-ADR-0061 peer's payload simply lacks the
	// key and pos.CompleteSale re-defaults it to employee on replay.
	TipRecipient string `json:"tip_recipient,omitempty"`
	Reference    string `json:"reference"`
	// Card-present reconciliation fields (ut-docs#543) -- empty for every
	// payment method that doesn't supply them. See CardPresentFields.
	MaskedPAN  string `json:"masked_pan,omitempty"`
	AuthCode   string `json:"auth_code,omitempty"`
	TerminalID string `json:"terminal_id,omitempty"`
	TraceID    string `json:"trace_id,omitempty"`
	// VoucherID (ut-docs#1053, migration 072): the tracked voucher this
	// 'voucher'-method payment redeemed -- empty for every other payment.
	// omitempty keeps the LAN-sync journal wire additive, same convention
	// as TipRecipient/the card-present fields above: a pre-1.3.0 peer's
	// payload simply lacks the key, which is an untracked voucher payment,
	// exactly the behaviour that peer had.
	VoucherID string `json:"voucher_id,omitempty"`
	PaidAt    string `json:"paid_at"`
}

// GetSaleDetailByID is GetSaleDetail keyed on the sale id (invoices store
// sale ids, not receipt numbers).
func (r *POSRepo) GetSaleDetailByID(ctx context.Context, saleID string) (SaleDetail, bool, error) {
	var receiptNo string
	err := r.db.QueryRowContext(ctx,
		`SELECT receipt_no FROM sales WHERE id = ?`, saleID).Scan(&receiptNo)
	if err == sql.ErrNoRows {
		return SaleDetail{}, false, nil
	}
	if err != nil {
		return SaleDetail{}, false, fmt.Errorf("sale by id: %w", err)
	}
	return r.GetSaleDetail(ctx, receiptNo)
}

// GetSaleDetail loads a sale with its lines and payments by receipt number.
func (r *POSRepo) GetSaleDetail(ctx context.Context, receiptNo string) (SaleDetail, bool, error) {
	var d SaleDetail
	err := r.db.QueryRowContext(ctx, `
SELECT s.id, s.receipt_no, s.status, s.sale_type, s.tender_type, s.order_type, s.offline, s.sync_status,
       s.currency, s.subtotal, s.discount_total, s.tax_total, s.total, s.service_charge_amount,
       s.service_charge_tax_basis_bp, s.voucher_issue_total, s.created_at,
       COALESCE(s.cashier_id, ''), COALESCE(s.table_id, ''), COALESCE(t.label, '')
FROM sales s LEFT JOIN tables t ON t.id = s.table_id
WHERE s.receipt_no = ?`, receiptNo).Scan(
		&d.ID, &d.ReceiptNo, &d.Status, &d.SaleType, &d.TenderType, &d.OrderType, &d.Offline,
		&d.SyncStatus, &d.Currency, &d.Subtotal, &d.DiscountTotal, &d.TaxTotal,
		&d.Total, &d.ServiceCharge, &d.ServiceChargeTaxBasisBP, &d.VoucherIssueTotal, &d.CreatedAt, &d.CashierID, &d.TableID, &d.TableLabel)
	if err == sql.ErrNoRows {
		return SaleDetail{}, false, nil
	}
	if err != nil {
		return SaleDetail{}, false, fmt.Errorf("get sale detail: %w", err)
	}

	lineRows, err := r.db.QueryContext(ctx, `
SELECT id, name_snapshot, COALESCE(sku_snapshot, ''), COALESCE(item_id, ''),
       COALESCE(variant_id, ''), COALESCE(tax_rate_bp, 0), quantity, unit_price,
       line_discount, tax_amount, total_after_tax, COALESCE(order_type, '')
FROM sale_lines WHERE sale_id = ? ORDER BY line_no`, d.ID)
	if err != nil {
		return SaleDetail{}, false, fmt.Errorf("get sale lines: %w", err)
	}
	var lineIDs []string
	for lineRows.Next() {
		var id string
		var l SaleDetailLine
		if err := lineRows.Scan(&id, &l.Name, &l.SKU, &l.ItemID, &l.VariantID, &l.TaxRateBP, &l.Qty, &l.UnitPrice, &l.LineDiscount, &l.TaxAmount, &l.LineTotal, &l.OrderType); err != nil {
			lineRows.Close()
			return SaleDetail{}, false, fmt.Errorf("scan sale line: %w", err)
		}
		lineIDs = append(lineIDs, id)
		d.Lines = append(d.Lines, l)
	}
	if err := lineRows.Err(); err != nil {
		lineRows.Close()
		return SaleDetail{}, false, fmt.Errorf("iterate sale lines: %w", err)
	}
	lineRows.Close()

	if len(lineIDs) > 0 {
		modRows, err := r.db.QueryContext(ctx, `
SELECT slm.sale_line_id, slm.option_name_snapshot
FROM sale_line_modifiers slm
JOIN sale_lines sl ON sl.id = slm.sale_line_id
WHERE sl.sale_id = ?
ORDER BY sl.line_no, slm.rowid`, d.ID)
		if err != nil {
			return SaleDetail{}, false, fmt.Errorf("get sale line modifiers: %w", err)
		}
		byLine := map[string][]string{}
		for modRows.Next() {
			var lineID, optName string
			if err := modRows.Scan(&lineID, &optName); err != nil {
				modRows.Close()
				return SaleDetail{}, false, fmt.Errorf("scan sale line modifier: %w", err)
			}
			byLine[lineID] = append(byLine[lineID], optName)
		}
		if err := modRows.Err(); err != nil {
			modRows.Close()
			return SaleDetail{}, false, fmt.Errorf("iterate sale line modifiers: %w", err)
		}
		modRows.Close()
		for i, id := range lineIDs {
			d.Lines[i].Modifiers = byLine[id]
		}
	}

	payRows, err := r.db.QueryContext(ctx, `
SELECT method_id, amount, change_given, tip_amount, COALESCE(tip_recipient, 'employee'), COALESCE(reference, ''),
       COALESCE(masked_pan, ''), COALESCE(auth_code, ''), COALESCE(terminal_id, ''), COALESCE(trace_id, ''),
       COALESCE(voucher_id, ''), paid_at
FROM payments WHERE sale_id = ? ORDER BY paid_at`, d.ID)
	if err != nil {
		return SaleDetail{}, false, fmt.Errorf("get sale payments: %w", err)
	}
	defer payRows.Close()
	for payRows.Next() {
		var p SaleDetailPayment
		if err := payRows.Scan(&p.Method, &p.Amount, &p.ChangeGiven, &p.TipAmount, &p.TipRecipient, &p.Reference,
			&p.MaskedPAN, &p.AuthCode, &p.TerminalID, &p.TraceID, &p.VoucherID, &p.PaidAt); err != nil {
			return SaleDetail{}, false, fmt.Errorf("scan sale payment: %w", err)
		}
		d.Payments = append(d.Payments, p)
	}
	if err := payRows.Err(); err != nil {
		return SaleDetail{}, false, fmt.Errorf("iterate sale payments: %w", err)
	}

	// Vouchers issued in this sale (ut-docs#1053): voucher_transactions'
	// sale_id is a deliberately FK-less soft reference (migration 068's
	// header), so this is a plain WHERE sale_id = ? join, not an FK
	// traversal. The issue transaction's amount IS the face value; the
	// holder label lives on the vouchers row.
	viRows, err := r.db.QueryContext(ctx, `
SELECT vt.voucher_id, COALESCE(v.holder_label, ''), vt.amount
FROM voucher_transactions vt
JOIN vouchers v ON v.id = vt.voucher_id
WHERE vt.sale_id = ? AND vt.type = 'issue'
ORDER BY vt.rowid`, d.ID)
	if err != nil {
		return SaleDetail{}, false, fmt.Errorf("get sale voucher issues: %w", err)
	}
	defer viRows.Close()
	for viRows.Next() {
		var vi SaleDetailVoucherIssue
		if err := viRows.Scan(&vi.VoucherID, &vi.HolderLabel, &vi.Amount); err != nil {
			return SaleDetail{}, false, fmt.Errorf("scan sale voucher issue: %w", err)
		}
		d.VoucherIssues = append(d.VoucherIssues, vi)
	}
	if err := viRows.Err(); err != nil {
		return SaleDetail{}, false, fmt.Errorf("iterate sale voucher issues: %w", err)
	}

	chargeRows, err := r.db.QueryContext(ctx, `
SELECT key, label, amount_minor, tax_basis_bp, base
FROM sale_charges WHERE sale_id = ? ORDER BY seq`, d.ID)
	if err != nil {
		return SaleDetail{}, false, fmt.Errorf("get sale charges: %w", err)
	}
	defer chargeRows.Close()
	for chargeRows.Next() {
		var c SaleCharge
		if err := chargeRows.Scan(&c.Key, &c.Label, &c.Amount, &c.TaxBasisBP, &c.Base); err != nil {
			return SaleDetail{}, false, fmt.Errorf("scan sale charge: %w", err)
		}
		d.Charges = append(d.Charges, c)
	}
	if err := chargeRows.Err(); err != nil {
		return SaleDetail{}, false, fmt.Errorf("iterate sale charges: %w", err)
	}
	return d, true, nil
}

func (r *POSRepo) ListRecentSales(ctx context.Context, limit int) ([]SaleJournalEntry, error) {
	entries, _, err := r.ListSalesJournal(ctx, SalesJournalFilter{AllTills: true, Limit: limit})
	return entries, err
}

// ListSalesJournal is the general sales-journal read: ListRecentSales (every
// till, no day filter) is the AllTills=true special case of this, kept as a
// thin wrapper so callers that only ever wanted "all tills, recent N" don't
// need to change. ut-docs#550: lets one till show every till's sales for
// end-of-day review, filterable by till and by day — a plain local-DB query,
// no primary/replica special-casing needed (ADR-0011: only the primary's
// local sales table ever accumulates other tills' journaled sales, so a
// replica's own local rows are all this query can ever find there).
//
// The bool return is ut-docs#774: true when more rows exist for this filter
// than limit — detected by asking for one extra row rather than paying for a
// separate COUNT(*). Day, when set, matches the shop's LOCAL calendar day
// (date(s.created_at, 'localtime'), same convention DayTotal already uses)
// rather than the raw stored UTC date, since Day comes from a browser date
// picker in the operator's own local time.
func (r *POSRepo) ListSalesJournal(ctx context.Context, f SalesJournalFilter) ([]SaleJournalEntry, bool, error) {
	limit := f.Limit
	if limit <= 0 {
		limit = 5
	}
	query := `
SELECT s.receipt_no, s.total, s.tender_type, s.sync_status, s.created_at, s.till_id, COALESCE(t.name, '') AS till_name
FROM sales s
LEFT JOIN tills t ON t.id = s.till_id
WHERE 1=1
`
	args := []any{}
	if !f.AllTills {
		query += ` AND s.till_id = ?`
		args = append(args, f.TillID)
	}
	if f.Day != "" {
		query += ` AND date(s.created_at, 'localtime') = date(?)`
		args = append(args, f.Day)
	}
	query += ` ORDER BY s.created_at DESC LIMIT ?`
	args = append(args, limit+1)

	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list sales journal: %w", err)
	}
	defer rows.Close()
	var out []SaleJournalEntry
	for rows.Next() {
		var entry SaleJournalEntry
		if err := rows.Scan(&entry.ReceiptNo, &entry.Total, &entry.TenderType, &entry.SyncStatus, &entry.CreatedAt, &entry.TillID, &entry.TillName); err != nil {
			return nil, false, fmt.Errorf("scan sales journal: %w", err)
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list sales journal: %w", err)
	}
	truncated := len(out) > limit
	if truncated {
		out = out[:limit]
	}
	return out, truncated, nil
}

func (r *POSRepo) ListQueuedSales(ctx context.Context, limit int, asOf string) ([]QueuedSale, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
SELECT id, receipt_no, sync_attempts, sync_next_attempt_at, sync_last_error, total, tender_type
FROM sales
WHERE sync_status = 'queued'
  AND (sync_next_attempt_at IS NULL OR sync_next_attempt_at <= ?)
ORDER BY created_at ASC
LIMIT ?
`
	rows, err := r.db.QueryContext(ctx, query, asOf, limit)
	if err != nil {
		return nil, fmt.Errorf("list queued sales: %w", err)
	}
	defer rows.Close()
	var out []QueuedSale
	for rows.Next() {
		var entry QueuedSale
		if err := rows.Scan(&entry.ID, &entry.ReceiptNo, &entry.SyncAttempts, &entry.SyncNextAttemptAt, &entry.SyncLastError, &entry.Total, &entry.TenderType); err != nil {
			return nil, fmt.Errorf("scan queued sales: %w", err)
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list queued sales: %w", err)
	}
	return out, nil
}

func (r *POSRepo) BumpSaleSyncAttempt(ctx context.Context, tx *sql.Tx, saleID, nextAttemptAt, lastError string) error {
	_, err := r.exec(tx).ExecContext(ctx, `
UPDATE sales
SET sync_attempts = sync_attempts + 1,
    sync_next_attempt_at = ?,
    sync_last_error = ?
WHERE id = ?
`, nullIfEmpty(nextAttemptAt), nullIfEmpty(lastError), saleID)
	if err != nil {
		return fmt.Errorf("bump sale sync attempt: %w", err)
	}
	return nil
}

func (r *POSRepo) ListActivePluginVersions(ctx context.Context, tx *sql.Tx) ([]PluginVersionRow, error) {
	rows, err := r.exec(tx).QueryContext(ctx, `
SELECT id, version
FROM plugins
WHERE is_active = 1
ORDER BY id
`)
	if err != nil {
		return nil, fmt.Errorf("list active plugins: %w", err)
	}
	defer rows.Close()
	var out []PluginVersionRow
	for rows.Next() {
		var row PluginVersionRow
		if err := rows.Scan(&row.ID, &row.Version); err != nil {
			return nil, fmt.Errorf("scan active plugins: %w", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list active plugins: %w", err)
	}
	return out, nil
}

// PaymentMethod is an active tender method offered on the Pay tab.
type PaymentMethod struct {
	ID       string
	Name     string
	PluginID string // empty for built-ins
}

// ListActivePaymentMethods returns active methods for the tender UI,
// built-ins first (sort_order), then plugin-provided ones.
func (r *POSRepo) ListActivePaymentMethods(ctx context.Context) ([]PaymentMethod, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, COALESCE(plugin_id, '')
FROM payment_methods WHERE is_active = 1 ORDER BY sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("list payment methods: %w", err)
	}
	defer rows.Close()
	var out []PaymentMethod
	for rows.Next() {
		var m PaymentMethod
		if err := rows.Scan(&m.ID, &m.Name, &m.PluginID); err != nil {
			return nil, fmt.Errorf("scan payment method: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListActiveNonCashPaymentMethods returns active tender methods excluding
// type='cash', for surfaces with no cash drawer (self-order kiosk, ADR-0020
// v1: card/contactless only).
func (r *POSRepo) ListActiveNonCashPaymentMethods(ctx context.Context) ([]PaymentMethod, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, COALESCE(plugin_id, '')
FROM payment_methods WHERE is_active = 1 AND type != 'cash' ORDER BY sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("list non-cash payment methods: %w", err)
	}
	defer rows.Close()
	var out []PaymentMethod
	for rows.Next() {
		var m PaymentMethod
		if err := rows.Scan(&m.ID, &m.Name, &m.PluginID); err != nil {
			return nil, fmt.Errorf("scan payment method: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListPaymentMethodIDs returns active payment method ids ordered by id.
func (r *POSRepo) ListPaymentMethodIDs(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM payment_methods WHERE is_active = 1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := make(map[string]struct{})
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			continue
		}
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// SearchItemsForShortcuts finds active items and primary barcodes to add as shortcuts.
func (r *POSRepo) SearchItemsForShortcuts(ctx context.Context, q string, offset, limit int) ([]ShortcutSearchResult, error) {
	like := "%" + strings.TrimSpace(q) + "%"
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT i.id,
       i.name,
       COALESCE((
         SELECT ib.barcode
         FROM item_barcodes ib
         WHERE ib.item_id = i.id
         ORDER BY ib.is_primary DESC
         LIMIT 1
       ), '') AS barcode,
       COALESCE(i.sku, ''),
       COALESCE(img.path, '')
FROM items i
LEFT JOIN item_images img ON img.item_id = i.id AND img.role = 'thumbnail'
WHERE i.is_active = 1 AND (
	  i.name LIKE ?
	  OR i.sku LIKE ?
	  OR EXISTS (SELECT 1 FROM item_barcodes ib2 WHERE ib2.item_id = i.id AND ib2.barcode LIKE ?)
)
ORDER BY i.name
LIMIT ? OFFSET ?
`, like, like, like, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []ShortcutSearchResult
	for rows.Next() {
		var rres ShortcutSearchResult
		if err := rows.Scan(&rres.ItemID, &rres.Name, &rres.Barcode, &rres.SKU, &rres.Image); err != nil {
			return nil, err
		}
		res = append(res, rres)
	}
	return res, rows.Err()
}

// ResolveShortcutLine returns a priced line for a barcode/lookup used by shortcuts/buttons.
func (r *POSRepo) ResolveShortcutLine(ctx context.Context, code string) (ShortcutLine, bool) {
	line, _, ok := r.ResolveShortcutLineDecoded(ctx, code)
	return line, ok
}

// enabledBarcodeSymbologies loads the shop's enabled symbology ids for the
// scan path. A scan must never fail on a settings read (ADR-0003
// offline-first) — the accessor already returns the ADR-0059 §2 default set
// alongside any error, which reproduces pre-registry behaviour exactly, so
// the error is deliberately swallowed here.
func (r *POSRepo) enabledBarcodeSymbologies(ctx context.Context) []string {
	ids, _ := r.settings.EnabledBarcodeSymbologies(ctx)
	return ids
}

// ResolveScanLine is the ADR-0059 §3 scan-resolution entry point: it
// matches code against the shop-enabled symbologies via the shared registry
// matcher (the same one AddBarcode's inference uses — the specificity
// ordering is defined once), then looks up the decoded LookupKey — NOT the
// raw scan — through the variant-barcode -> item-barcode chain. LookupKey ==
// code for plain symbologies, so those resolve exactly as before; for the
// two embedded-data symbologies it is the zeroed template AddBarcode stores,
// which is what lets EVERY label of a scale item hit the same catalog row.
// The returned Decoded tells the caller whether the code carried an embedded
// weight/price. ok is false when no enabled symbology matches (an unmatched
// code is a named non-match, mirroring AddBarcode — only reachable once the
// shop disabled the default catch-alls) or when nothing in the catalog owns
// the LookupKey.
func (r *POSRepo) ResolveScanLine(ctx context.Context, code string, enabledIDs []string) (ShortcutLine, barcode.Decoded, bool) {
	dec, ok := barcode.Default().Match(enabledIDs, code)
	if !ok {
		return ShortcutLine{}, barcode.Decoded{}, false
	}
	// variant barcode
	if row, ok := r.resolveVariant(ctx, dec.LookupKey); ok {
		price := r.resolvePrice(ctx, "", row.VariantID, row.Price)
		if row.Variant != "" {
			row.ItemName = row.ItemName + " - " + row.Variant
		}
		return r.toShortcutLine(code, price, row), dec, true
	}
	// item barcode
	if row, ok := r.resolveItem(ctx, dec.LookupKey); ok {
		price := r.resolvePrice(ctx, row.ItemID, "", row.Price)
		return r.toShortcutLine(code, price, row), dec, true
	}
	// Raw-code fallback (ut-docs#934 review finding F2): for an
	// embedded-data match, dec.LookupKey (the zeroed template) differs
	// from the raw scanned code. A shop that enables a scale symbology
	// after already cataloguing plain, full-digit EAN-13 barcodes in that
	// prefix range (2x/02) — never re-entered using the zeroed
	// convention — must keep resolving those existing rows; the
	// zeroed-key tier above still gets first refusal, so this fallback
	// never shadows a genuine scale-label row (that already matched
	// above and returned).
	if dec.LookupKey != code {
		if row, ok := r.resolveVariant(ctx, code); ok {
			price := r.resolvePrice(ctx, "", row.VariantID, row.Price)
			if row.Variant != "" {
				row.ItemName = row.ItemName + " - " + row.Variant
			}
			return r.toShortcutLine(code, price, row), barcode.Decoded{}, true
		}
		if row, ok := r.resolveItem(ctx, code); ok {
			price := r.resolvePrice(ctx, row.ItemID, "", row.Price)
			return r.toShortcutLine(code, price, row), barcode.Decoded{}, true
		}
	}
	return ShortcutLine{}, dec, false
}

// ResolveShortcutLineDecoded is ResolveShortcutLine plus the barcode decode
// (ut-docs#934): the variant/item tiers go through ResolveScanLine against
// the shop's enabled symbologies; the shortcut-button, exact-SKU and
// name-LIKE tiers stay on the raw code, unchanged (shortcut_buttons lookup
// is out of ADR-0059's scope per its Non-goals, and SKU/name search is not
// a barcode symbology). dec is only meaningful when the match came from the
// barcode tiers — it is zero-valued for shortcut/SKU/name matches.
func (r *POSRepo) ResolveShortcutLineDecoded(ctx context.Context, code string) (ShortcutLine, barcode.Decoded, bool) {
	// variant barcode -> item barcode, via the symbology registry
	if line, dec, ok := r.ResolveScanLine(ctx, code, r.enabledBarcodeSymbologies(ctx)); ok {
		return line, dec, true
	}
	// shortcut barcode
	if row, ok := r.resolveShortcut(ctx, code); ok {
		price := r.resolvePrice(ctx, row.ItemID, row.VariantID, row.Price)
		if row.Label.Valid && row.Label.String != "" {
			row.ItemName = row.Label.String
		}
		return r.toShortcutLine(code, price, row), barcode.Decoded{}, true
	}

	q := strings.TrimSpace(code)
	if q == "" {
		return ShortcutLine{}, barcode.Decoded{}, false
	}
	// SKU exact
	if row, ok := r.resolveSKU(ctx, q); ok {
		price := r.resolveRowPrice(ctx, row)
		if row.Variant != "" {
			row.ItemName = row.ItemName + " - " + row.Variant
		}
		return r.toShortcutLine(row.SKU, price, row), barcode.Decoded{}, true
	}
	// Name like
	if row, ok := r.resolveNameLike(ctx, "%"+q+"%"); ok {
		price := r.resolveRowPrice(ctx, row)
		if row.Variant != "" {
			row.ItemName = row.ItemName + " - " + row.Variant
		} else if row.ItemName == "" {
			row.ItemName = q
		}
		return r.toShortcutLine(q, price, row), barcode.Decoded{}, true
	}
	return ShortcutLine{}, barcode.Decoded{}, false
}

// resolveRowPrice prices a resolved item/variant row. ResolveCurrentPrice
// rejects a row with both ItemID and VariantID set (item_id/variant_id are
// mutually exclusive everywhere a price_history row is looked up), so a
// variant match must be priced by its VariantID alone, dropping ItemID —
// which is otherwise still needed on the row for tax-code/display purposes.
func (r *POSRepo) resolveRowPrice(ctx context.Context, row shortcutPriceRow) int64 {
	if row.VariantID != "" {
		return r.resolvePrice(ctx, "", row.VariantID, row.Price)
	}
	return r.resolvePrice(ctx, row.ItemID, "", row.Price)
}

// ResolveCurrentPrice returns the active price (minor units) for an item or variant.
func (r *POSRepo) ResolveCurrentPrice(ctx context.Context, itemID, variantID string) (int64, error) {
	if (itemID == "" && variantID == "") || (itemID != "" && variantID != "") {
		return 0, errors.New("resolve price requires exactly one of itemID or variantID")
	}

	if variantID != "" {
		if price, ok, err := r.lookupPriceHistory(ctx, "variant_id", variantID); err != nil {
			return 0, err
		} else if ok {
			return price, nil
		}
		var price int64
		if err := r.db.QueryRowContext(ctx, `SELECT price FROM item_variants WHERE id = ? AND is_active = 1`, variantID).Scan(&price); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return 0, fmt.Errorf("variant not found or inactive: %s", variantID)
			}
			return 0, fmt.Errorf("load variant price: %w", err)
		}
		return price, nil
	}

	if price, ok, err := r.lookupPriceHistory(ctx, "item_id", itemID); err != nil {
		return 0, err
	} else if ok {
		return price, nil
	}
	var price int64
	if err := r.db.QueryRowContext(ctx, `SELECT base_price FROM items WHERE id = ? AND is_active = 1`, itemID).Scan(&price); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("item not found or inactive: %s", itemID)
		}
		return 0, fmt.Errorf("load item price: %w", err)
	}
	return price, nil
}

func (r *POSRepo) lookupPriceHistory(ctx context.Context, column, id string) (int64, bool, error) {
	query := fmt.Sprintf(`
SELECT price
FROM price_history
WHERE %s = ?
  AND datetime(starts_at) <= CURRENT_TIMESTAMP
  AND (ends_at IS NULL OR datetime(ends_at) > CURRENT_TIMESTAMP)
ORDER BY datetime(starts_at) DESC
LIMIT 1
`, column)
	var price int64
	if err := r.db.QueryRowContext(ctx, query, id).Scan(&price); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("lookup price_history: %w", err)
	}
	return price, true, nil
}

type shortcutPriceRow struct {
	ItemID    string
	ItemName  string
	VariantID string
	Variant   string
	SKU       string
	Price     int64
	Image     sql.NullString
	TaxRateBP sql.NullInt64
	TaxCodeID sql.NullString
	IsWeighed sql.NullInt64
	Label     sql.NullString
}

func (r *POSRepo) resolveVariant(ctx context.Context, code string) (shortcutPriceRow, bool) {
	row := r.db.QueryRowContext(ctx, `
SELECT i.id, i.name, v.id, v.name, v.price, i.is_weighed,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0), i.tax_code_id
FROM variant_barcodes vb
JOIN item_variants v ON v.id = vb.variant_id
JOIN items i ON i.id = v.item_id
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE vb.barcode = ?
  AND i.is_active = 1 AND v.is_active = 1
LIMIT 1
`, code)
	var res shortcutPriceRow
	if err := row.Scan(&res.ItemID, &res.ItemName, &res.VariantID, &res.Variant, &res.Price, &res.IsWeighed, &res.Image, &res.TaxRateBP, &res.TaxCodeID); err != nil {
		return shortcutPriceRow{}, false
	}
	return res, true
}

func (r *POSRepo) resolveItem(ctx context.Context, code string) (shortcutPriceRow, bool) {
	row := r.db.QueryRowContext(ctx, `
SELECT i.id, i.name, i.base_price, i.is_weighed,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0), i.tax_code_id
FROM item_barcodes ib
JOIN items i ON i.id = ib.item_id
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE ib.barcode = ?
  AND i.is_active = 1
LIMIT 1
`, code)
	var res shortcutPriceRow
	if err := row.Scan(&res.ItemID, &res.ItemName, &res.Price, &res.IsWeighed, &res.Image, &res.TaxRateBP, &res.TaxCodeID); err != nil {
		return shortcutPriceRow{}, false
	}
	return res, true
}

func (r *POSRepo) resolveShortcut(ctx context.Context, code string) (shortcutPriceRow, bool) {
	row := r.db.QueryRowContext(ctx, `
SELECT sb.item_id, sb.label, i.base_price, i.is_weighed,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0), i.tax_code_id
FROM shortcut_buttons sb
JOIN items i ON i.id = sb.item_id
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE sb.barcode = ?
  AND i.is_active = 1
LIMIT 1
`, code)
	var res shortcutPriceRow
	if err := row.Scan(&res.ItemID, &res.Label, &res.Price, &res.IsWeighed, &res.Image, &res.TaxRateBP, &res.TaxCodeID); err != nil {
		return shortcutPriceRow{}, false
	}
	res.ItemName = res.Label.String
	return res, true
}

func (r *POSRepo) resolveSKU(ctx context.Context, sku string) (shortcutPriceRow, bool) {
	row := r.db.QueryRowContext(ctx, `
SELECT i.id, i.sku, i.name, i.base_price, i.is_weighed,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0), i.tax_code_id
FROM items i
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE i.is_active = 1 AND i.sku = ?
LIMIT 1
`, sku)
	var res shortcutPriceRow
	if err := row.Scan(&res.ItemID, &res.SKU, &res.ItemName, &res.Price, &res.IsWeighed, &res.Image, &res.TaxRateBP, &res.TaxCodeID); err == nil {
		return res, true
	}
	return r.resolveVariantSKU(ctx, sku)
}

// resolveVariantSKU is resolveSKU's fallback for a variant's own SKU (not
// the parent item's) — items.sku never matches it, so without this a
// variant could not be found by exact-SKU search at all.
func (r *POSRepo) resolveVariantSKU(ctx context.Context, sku string) (shortcutPriceRow, bool) {
	row := r.db.QueryRowContext(ctx, `
SELECT i.id, i.name, v.id, v.name, v.price, i.is_weighed,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0), i.tax_code_id
FROM item_variants v
JOIN items i ON i.id = v.item_id
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE v.is_active = 1 AND i.is_active = 1 AND v.sku = ?
LIMIT 1
`, sku)
	var res shortcutPriceRow
	res.SKU = sku
	if err := row.Scan(&res.ItemID, &res.ItemName, &res.VariantID, &res.Variant, &res.Price, &res.IsWeighed, &res.Image, &res.TaxRateBP, &res.TaxCodeID); err != nil {
		return shortcutPriceRow{}, false
	}
	return res, true
}

func (r *POSRepo) resolveNameLike(ctx context.Context, like string) (shortcutPriceRow, bool) {
	row := r.db.QueryRowContext(ctx, `
SELECT i.id, i.name, i.base_price, i.is_weighed,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0), i.tax_code_id
FROM items i
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE i.is_active = 1 AND i.name LIKE ?
ORDER BY i.name
LIMIT 1
`, like)
	var res shortcutPriceRow
	if err := row.Scan(&res.ItemID, &res.ItemName, &res.Price, &res.IsWeighed, &res.Image, &res.TaxRateBP, &res.TaxCodeID); err == nil {
		return res, true
	}
	return r.resolveVariantNameLike(ctx, like)
}

// resolveVariantNameLike is resolveNameLike's fallback for a variant's own
// name (not the parent item's) — items.name never matches it, so without
// this a variant could not be found by name search at all.
func (r *POSRepo) resolveVariantNameLike(ctx context.Context, like string) (shortcutPriceRow, bool) {
	row := r.db.QueryRowContext(ctx, `
SELECT i.id, i.name, v.id, v.name, v.price, i.is_weighed,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0), i.tax_code_id
FROM item_variants v
JOIN items i ON i.id = v.item_id
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE v.is_active = 1 AND i.is_active = 1 AND v.name LIKE ?
ORDER BY v.name
LIMIT 1
`, like)
	var res shortcutPriceRow
	if err := row.Scan(&res.ItemID, &res.ItemName, &res.VariantID, &res.Variant, &res.Price, &res.IsWeighed, &res.Image, &res.TaxRateBP, &res.TaxCodeID); err != nil {
		return shortcutPriceRow{}, false
	}
	return res, true
}

func (r *POSRepo) resolvePrice(ctx context.Context, itemID, variantID string, fallback int64) int64 {
	price, err := r.ResolveCurrentPrice(ctx, itemID, variantID)
	if err != nil {
		return fallback
	}
	return price
}

func (r *POSRepo) toShortcutLine(code string, price int64, row shortcutPriceRow) ShortcutLine {
	line := ShortcutLine{
		SKU:        code,
		Name:       row.ItemName,
		ItemID:     row.ItemID,
		VariantID:  row.VariantID,
		Price:      price,
		TaxRateBP:  int(row.TaxRateBP.Int64),
		TaxCodeID:  row.TaxCodeID.String,
		IsWeighed:  row.IsWeighed.Int64 == 1,
		HasVariant: row.VariantID != "",
	}
	if row.Image.Valid {
		line.ImageURL = row.Image.String
	}
	if row.Label.Valid && row.Label.String != "" {
		line.Label = row.Label.String
	}
	return line
}

// AppendPriceHistoryItem ends the current open price (if any) and appends a new price_history row for an item.
func (r *POSRepo) AppendPriceHistoryItem(ctx context.Context, itemID string, price int64, startsAt time.Time) error {
	if itemID == "" {
		return errors.New("itemID required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE price_history SET ends_at = ? WHERE item_id = ? AND ends_at IS NULL`, startsAt.Format(time.RFC3339), itemID); err != nil {
		return fmt.Errorf("close previous price: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO price_history(id, item_id, price, starts_at) VALUES(?,?,?,?)`, uuid.New().String(), itemID, price, startsAt.Format(time.RFC3339)); err != nil {
		return fmt.Errorf("insert price_history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit price update: %w", err)
	}
	return nil
}

// AppendPriceHistoryVariant ends the current open price (if any) and appends a new price_history row for a variant.
func (r *POSRepo) AppendPriceHistoryVariant(ctx context.Context, variantID string, price int64, startsAt time.Time) error {
	if variantID == "" {
		return errors.New("variantID required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE price_history SET ends_at = ? WHERE variant_id = ? AND ends_at IS NULL`, startsAt.Format(time.RFC3339), variantID); err != nil {
		return fmt.Errorf("close previous price: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO price_history(id, variant_id, price, starts_at) VALUES(?,?,?,?)`, uuid.New().String(), variantID, price, startsAt.Format(time.RFC3339)); err != nil {
		return fmt.Errorf("insert price_history: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit price update: %w", err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt64(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func valueOrDefault(val, fallback string) string {
	if strings.TrimSpace(val) != "" {
		return val
	}
	return fallback
}

type dbExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (r *POSRepo) exec(tx *sql.Tx) dbExecutor {
	if tx != nil {
		return tx
	}
	return r.db
}
