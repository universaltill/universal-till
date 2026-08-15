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
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/logging"
)

// POSRepo centralizes DB access for POS handlers.
type POSRepo struct {
	db *sql.DB
}

var posObs = newRepoObservability("pos")

func NewPOSRepo(db *sql.DB) *POSRepo {
	return &POSRepo{db: db}
}

// ShortcutSearchResult is used for shortcut item selection.
type ShortcutSearchResult struct {
	ItemID  string
	Name    string
	Barcode string
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
}

type SaleJournalEntry struct {
	ReceiptNo  string
	Total      int64
	TenderType string
	SyncStatus string
	CreatedAt  string
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
	ActorID    string
	Reason     string
	ItemID     string
	VariantID  string
	LocationID string
	QtyBefore  float64
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
SELECT i.id, i.sku, i.name, i.description, i.category_id, i.brand_id, i.unit, i.base_price, i.tax_code_id, i.is_active, i.is_weighed
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
	if err = r.db.QueryRowContext(ctx, `SELECT id, item_id, sku, name, price, cost_price, is_active FROM item_variants WHERE id = ?`, variantID).
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
SELECT id, item_id, variant_id, name_snapshot, sku_snapshot, barcode_snapshot, quantity, unit_price, tax_rate_bp
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
		)
		if err := rows.Scan(&id, &itemID, &variantID, &name, &sku, &barcode, &qty, &unitPrice, &taxRateBP); err != nil {
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
// EOD Z-report). day is "YYYY-MM-DD".
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
WHERE s.status = 'completed' AND s.sale_type = 'sale' AND date(s.created_at) = date(?)
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
// SlowItems/busyBuckets); TaxSummary is the fiscal view and nets them.
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
// summary an owner (or their accountant) needs per return period.
type TaxBand struct {
	RateBP int   // basis points (2000 = 20%)
	Net    int64 // taxable amount before tax, minor units
	Tax    int64 // tax collected, minor units
}

// TaxSummary groups completed sales' lines by tax rate over [from, to).
// Returns reduce the figures (sale_type='return' lines subtract).
func (r *POSRepo) TaxSummary(ctx context.Context, from, to time.Time) ([]TaxBand, error) {
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT sl.tax_rate_bp,
       COALESCE(SUM(CASE WHEN s.sale_type = 'return' THEN -sl.total_before_tax ELSE sl.total_before_tax END), 0),
       COALESCE(SUM(CASE WHEN s.sale_type = 'return' THEN -sl.tax_amount ELSE sl.tax_amount END), 0)
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
WHERE s.status = 'completed'
  AND datetime(s.created_at) >= datetime(?) AND datetime(s.created_at) < datetime(?)
GROUP BY sl.tax_rate_bp ORDER BY sl.tax_rate_bp DESC`, fromStr, toStr)
	if err != nil {
		return nil, fmt.Errorf("tax summary: %w", err)
	}
	defer rows.Close()
	var out []TaxBand
	for rows.Next() {
		var b TaxBand
		if err := rows.Scan(&b.RateBP, &b.Net, &b.Tax); err != nil {
			return nil, fmt.Errorf("scan tax band: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
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
// offset days back from today (1 = yesterday).
func (r *POSRepo) DayTotal(ctx context.Context, daysAgo int) (int64, int, error) {
	var total int64
	var count int
	err := r.db.QueryRowContext(ctx, `
SELECT COALESCE(SUM(total), 0), COUNT(*) FROM sales
WHERE status = 'completed' AND sale_type = 'sale'
  AND date(created_at, 'localtime') = date('now', 'localtime', ?)`,
		fmt.Sprintf("-%d days", daysAgo)).Scan(&total, &count)
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

// SalesByWeekday buckets completed sales by local weekday over the last N
// days — "which days are busiest" for staffing decisions.
func (r *POSRepo) SalesByWeekday(ctx context.Context, from, to time.Time) ([]BusySlot, error) {
	return r.busyBuckets(ctx, from, to, `CAST(strftime('%w', s.created_at, 'localtime') AS INTEGER)`)
}

// SalesByHour buckets completed sales by local hour of day over [from, to).
func (r *POSRepo) SalesByHour(ctx context.Context, from, to time.Time) ([]BusySlot, error) {
	return r.busyBuckets(ctx, from, to, `CAST(strftime('%H', s.created_at, 'localtime') AS INTEGER)`)
}

func (r *POSRepo) busyBuckets(ctx context.Context, from, to time.Time, bucketExpr string) ([]BusySlot, error) {
	fromStr, toStr := windowArgs(from, to)
	rows, err := r.db.QueryContext(ctx, `
SELECT `+bucketExpr+` AS slot, COUNT(*), COALESCE(SUM(s.total), 0)
FROM sales s
WHERE s.status = 'completed' AND s.sale_type = 'sale'
  AND datetime(s.created_at) >= datetime(?) AND datetime(s.created_at) < datetime(?)
GROUP BY slot ORDER BY slot`, fromStr, toStr)
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

// EODMethod is one payment method's day: taken in on sales, paid out on
// refunds (minor units).
type EODMethod struct {
	Method string `json:"method"`
	In     int64  `json:"in"`
	Out    int64  `json:"out"`
}

// EODReport is the classic Z-report for one business day, or — when From/To
// are set instead of Day — a date-ranged summary spanning multiple days.
type EODReport struct {
	Day          string      `json:"day,omitempty"`
	From         string      `json:"from,omitempty"`
	To           string      `json:"to,omitempty"`
	SalesCount   int         `json:"sales_count"`
	Gross        int64       `json:"gross"`
	RefundCount  int         `json:"refund_count"`
	RefundTotal  int64       `json:"refund_total"`
	Net          int64       `json:"net"`
	TaxNet       int64       `json:"tax_net"`
	Methods      []EODMethod `json:"methods"`
	Departments  []DeptSales `json:"departments"` // per-department sales (E1b)
	Tills        []TillSales `json:"tills"`       // per-register sales (multi-till)
	FirstReceipt string      `json:"first_receipt"`
	LastReceipt  string      `json:"last_receipt"`
	GeneratedAt  string      `json:"generated_at"`
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
// EndOfDayRange. date(created_at) BETWEEN ? AND ? is equivalent to
// date(created_at) = ? when from == to, so EndOfDay's behavior (and its
// existing tests) are unaffected by sharing this with the range query.
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
WHERE status = 'completed' AND date(created_at) BETWEEN ? AND ?`,
		from, to).Scan(&rep.SalesCount, &rep.Gross, &rep.RefundCount, &rep.RefundTotal,
		&rep.TaxNet, &rep.FirstReceipt, &rep.LastReceipt)
	if err != nil {
		return rep, fmt.Errorf("eod totals: %w", err)
	}
	rep.Net = rep.Gross - rep.RefundTotal

	rows, err := r.db.QueryContext(ctx, `
SELECT p.method_id,
  COALESCE(SUM(CASE WHEN s.sale_type = 'sale'   THEN p.amount - p.change_given END), 0),
  COALESCE(SUM(CASE WHEN s.sale_type = 'return' THEN p.amount - p.change_given END), 0)
FROM payments p
JOIN sales s ON s.id = p.sale_id
WHERE s.status = 'completed' AND date(s.created_at) BETWEEN ? AND ?
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
WHERE s.status = 'completed' AND s.sale_type = 'sale' AND date(s.created_at) = ?
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

// ArchiveReport stores a generated report; kind+period is unique so the
// scheduled job is idempotent. Returns false when it already existed.
func (r *POSRepo) ArchiveReport(ctx context.Context, kind, period string, content []byte) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
INSERT INTO report_archive (id, kind, period, content_json)
VALUES (?, ?, ?, ?) ON CONFLICT (kind, period) DO NOTHING`,
		uuid.NewString(), kind, period, string(content))
	if err != nil {
		return false, fmt.Errorf("archive report: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ArchivedReportRow lists an archived report for the Reports page and the
// retention export (ADR-0040 §7). JSON tags are snake_case per this repo's
// API convention -- the export handler is the one consumer that actually
// marshals this to JSON; the Reports page reads the Go fields directly.
type ArchivedReportRow struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Period    string `json:"period"`
	Content   string `json:"content_json"`
	CreatedAt string `json:"created_at"`
}

// formatArchiveTimestamp converts report_archive.created_at (SQLite
// `datetime('now')`, "YYYY-MM-DD HH:MM:SS", implicitly UTC) to ISO-8601
// (CLAUDE.md's API format rule) for both the Reports page and the export.
// Falls back to the raw value on an unexpected format rather than blanking
// it -- a slightly-off timestamp is better than a silently dropped one.
func formatArchiveTimestamp(raw string) string {
	t, err := time.Parse("2006-01-02 15:04:05", raw)
	if err != nil {
		return raw
	}
	return t.UTC().Format(time.RFC3339)
}

// ListArchivedReports returns recent archived reports, newest first.
func (r *POSRepo) ListArchivedReports(ctx context.Context, limit int) ([]ArchivedReportRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, kind, period, content_json, created_at
FROM report_archive ORDER BY period DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list reports: %w", err)
	}
	defer rows.Close()
	var out []ArchivedReportRow
	for rows.Next() {
		var a ArchivedReportRow
		if err := rows.Scan(&a.ID, &a.Kind, &a.Period, &a.Content, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan report: %w", err)
		}
		a.CreatedAt = formatArchiveTimestamp(a.CreatedAt)
		out = append(out, a)
	}
	return out, rows.Err()
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
SELECT id, kind, period, content_json, created_at
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
		var a ArchivedReportRow
		if err := rows.Scan(&a.ID, &a.Kind, &a.Period, &a.Content, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan report: %w", err)
		}
		a.CreatedAt = formatArchiveTimestamp(a.CreatedAt)
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
func RefundLineKey(itemID, variantID string, unitPrice int64) string {
	return itemID + "|" + variantID + "|" + strconv.FormatInt(unitPrice, 10)
}

// ReturnedQuantities sums, per line key, what previous returns linked to
// the original sale already gave back — the double-refund guard's input.
func (r *POSRepo) ReturnedQuantities(ctx context.Context, originalSaleID string) (map[string]float64, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT COALESCE(l.item_id, ''), COALESCE(l.variant_id, ''), l.unit_price, SUM(l.quantity)
FROM sale_lines l
JOIN sale_links k ON k.sale_id = l.sale_id
JOIN sales s ON s.id = l.sale_id AND s.sale_type = 'return' AND s.status = 'completed'
WHERE k.original_sale_id = ?
GROUP BY l.item_id, l.variant_id, l.unit_price`, originalSaleID)
	if err != nil {
		return nil, fmt.Errorf("returned quantities: %w", err)
	}
	defer rows.Close()
	out := map[string]float64{}
	for rows.Next() {
		var itemID, variantID string
		var unitPrice int64
		var qty float64
		if err := rows.Scan(&itemID, &variantID, &unitPrice, &qty); err != nil {
			return nil, fmt.Errorf("scan returned qty: %w", err)
		}
		out[RefundLineKey(itemID, variantID, unitPrice)] = qty
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

// InsertSale writes the sale header row. serviceCharge is the computed
// till-set service-charge amount for this sale (distinct from a payment's
// tip_amount) -- it already participates in total, so it is stored
// separately only so it can be broken out on the receipt/journal.
func (r *POSRepo) InsertSale(ctx context.Context, tx *sql.Tx, saleID, receiptNo, saleType, registerID, cashierID, customerID, currency string, subtotal, discountTotal, taxTotal, total, serviceCharge int64, note, createdAt, tenderType, orderType string, offline bool, syncStatus string, syncAttempts int, syncNextAttemptAt, syncLastError string) error {
	offlineVal := 0
	if offline {
		offlineVal = 1
	}
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO sales (id, receipt_no, status, sale_type, tender_type, order_type, offline, sync_status, sync_attempts, sync_next_attempt_at, sync_last_error, register_id, cashier_id, customer_id, currency, subtotal, discount_total, tax_total, total, service_charge_amount, rounding, note, created_at, completed_at)
VALUES (?, ?, 'completed', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)
`, saleID, receiptNo, saleType, tenderType, orderType, offlineVal, syncStatus, syncAttempts, nullIfEmpty(syncNextAttemptAt), nullIfEmpty(syncLastError), nullIfEmpty(registerID), nullIfEmpty(cashierID), nullIfEmpty(customerID), currency, subtotal, discountTotal, taxTotal, total, serviceCharge, nullIfEmpty(note), createdAt, createdAt)
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

// InsertPayment writes a payment row. tipAmount is gratuity metadata
// (docs/germany-pos-parity-backlog.md tip-flow gap) -- it rides alongside
// amount but is never subtracted/added when deriving what the payment
// applies toward the sale total; see pos.PaymentInput.TipAmount.
func (r *POSRepo) InsertPayment(ctx context.Context, tx *sql.Tx, paymentID, saleID, methodID string, amount int64, currency, reference string, changeGiven int64, tipAmount int64, paidAt string) error {
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO payments (id, sale_id, method_id, amount, currency, reference, change_given, tip_amount, paid_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
`, paymentID, saleID, methodID, amount, currency, nullIfEmpty(reference), changeGiven, tipAmount, paidAt)
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

// InsertAudit writes an audit_log entry (id optional).
func (r *POSRepo) InsertAudit(ctx context.Context, tx *sql.Tx, actorID, entityType, entityID, action string, payload any, createdAt string, id string) error {
	if id == "" {
		id = uuid.NewString()
	}
	data, _ := json.Marshal(payload)
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, data_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, id, nullIfEmpty(actorID), entityType, entityID, action, string(data), createdAt)
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
       a.entity_type, a.entity_id, a.action, COALESCE(a.data_json, ''), a.created_at
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
		if err := rows.Scan(&e.ID, &e.ActorID, &e.ActorName, &e.EntityType, &e.EntityID, &e.Action, &e.DataJSON, &e.CreatedAt); err != nil {
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
       a.entity_type, a.entity_id, a.action, COALESCE(a.data_json, ''), a.created_at
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
		if err := rows.Scan(&e.ID, &e.ActorID, &e.ActorName, &e.EntityType, &e.EntityID, &e.Action, &e.DataJSON, &e.CreatedAt); err != nil {
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
// it unlinks the customer from sales and promotions (keeping the sales, which
// are financial records, but anonymous) and deletes the customer row. Audited.
// Returns false if no such customer.
func (r *POSRepo) EraseCustomer(ctx context.Context, id, actorID string) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	for _, q := range []string{
		`UPDATE sales SET customer_id = NULL WHERE customer_id = ?`,
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
// variants appears in sale_lines or stock_movements. Anything ever sold or
// moved is KEPT (deactivated at most) so audit/tax history stays intact.
const obsoleteItemsWhere = `
is_active = 0
AND id NOT IN (SELECT item_id FROM sale_lines WHERE item_id IS NOT NULL)
AND id NOT IN (SELECT item_id FROM stock_movements WHERE item_id IS NOT NULL)
AND id NOT IN (SELECT v.item_id FROM item_variants v
              WHERE v.id IN (SELECT variant_id FROM sale_lines WHERE variant_id IS NOT NULL))
AND id NOT IN (SELECT v.item_id FROM item_variants v
              WHERE v.id IN (SELECT variant_id FROM stock_movements WHERE variant_id IS NOT NULL))`

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

// UpdateShiftClose sets closing details.
func (r *POSRepo) UpdateShiftClose(ctx context.Context, tx *sql.Tx, shiftID string, closingCash, expectedCash int64, note string, closedAt string) error {
	_, err := r.exec(tx).ExecContext(ctx, `
UPDATE shifts
SET closed_at = ?, closing_cash = ?, expected_cash = ?, note = ?
WHERE id = ?
`, closedAt, closingCash, expectedCash, nullIfEmpty(note), shiftID)
	if err != nil {
		return fmt.Errorf("update shift: %w", err)
	}
	return nil
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
	ID            string              `json:"id"`
	ReceiptNo     string              `json:"receipt_no"`
	Status        string              `json:"status"`
	SaleType      string              `json:"sale_type"`
	TenderType    string              `json:"tender_type"`
	OrderType     string              `json:"order_type"`
	Offline       bool                `json:"offline"`
	SyncStatus    string              `json:"sync_status"`
	Currency      string              `json:"currency"`
	Subtotal      int64               `json:"subtotal"`
	DiscountTotal int64               `json:"discount_total"`
	TaxTotal      int64               `json:"tax_total"`
	Total         int64               `json:"total"`
	ServiceCharge int64               `json:"service_charge"`
	CreatedAt     string              `json:"created_at"`
	CashierID     string              `json:"cashier_id"`
	Lines         []SaleDetailLine    `json:"lines"`
	Payments      []SaleDetailPayment `json:"payments"`
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
}

type SaleDetailPayment struct {
	Method      string `json:"method"`
	Amount      int64  `json:"amount"`
	ChangeGiven int64  `json:"change_given"`
	TipAmount   int64  `json:"tip_amount"`
	Reference   string `json:"reference"`
	PaidAt      string `json:"paid_at"`
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
SELECT id, receipt_no, status, sale_type, tender_type, order_type, offline, sync_status,
       currency, subtotal, discount_total, tax_total, total, service_charge_amount, created_at,
       COALESCE(cashier_id, '')
FROM sales WHERE receipt_no = ?`, receiptNo).Scan(
		&d.ID, &d.ReceiptNo, &d.Status, &d.SaleType, &d.TenderType, &d.OrderType, &d.Offline,
		&d.SyncStatus, &d.Currency, &d.Subtotal, &d.DiscountTotal, &d.TaxTotal,
		&d.Total, &d.ServiceCharge, &d.CreatedAt, &d.CashierID)
	if err == sql.ErrNoRows {
		return SaleDetail{}, false, nil
	}
	if err != nil {
		return SaleDetail{}, false, fmt.Errorf("get sale detail: %w", err)
	}

	lineRows, err := r.db.QueryContext(ctx, `
SELECT id, name_snapshot, COALESCE(sku_snapshot, ''), COALESCE(item_id, ''),
       COALESCE(variant_id, ''), COALESCE(tax_rate_bp, 0), quantity, unit_price,
       line_discount, tax_amount, total_after_tax
FROM sale_lines WHERE sale_id = ? ORDER BY line_no`, d.ID)
	if err != nil {
		return SaleDetail{}, false, fmt.Errorf("get sale lines: %w", err)
	}
	var lineIDs []string
	for lineRows.Next() {
		var id string
		var l SaleDetailLine
		if err := lineRows.Scan(&id, &l.Name, &l.SKU, &l.ItemID, &l.VariantID, &l.TaxRateBP, &l.Qty, &l.UnitPrice, &l.LineDiscount, &l.TaxAmount, &l.LineTotal); err != nil {
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
SELECT method_id, amount, change_given, tip_amount, COALESCE(reference, ''), paid_at
FROM payments WHERE sale_id = ? ORDER BY paid_at`, d.ID)
	if err != nil {
		return SaleDetail{}, false, fmt.Errorf("get sale payments: %w", err)
	}
	defer payRows.Close()
	for payRows.Next() {
		var p SaleDetailPayment
		if err := payRows.Scan(&p.Method, &p.Amount, &p.ChangeGiven, &p.TipAmount, &p.Reference, &p.PaidAt); err != nil {
			return SaleDetail{}, false, fmt.Errorf("scan sale payment: %w", err)
		}
		d.Payments = append(d.Payments, p)
	}
	if err := payRows.Err(); err != nil {
		return SaleDetail{}, false, fmt.Errorf("iterate sale payments: %w", err)
	}
	return d, true, nil
}

func (r *POSRepo) ListRecentSales(ctx context.Context, limit int) ([]SaleJournalEntry, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT receipt_no, total, tender_type, sync_status, created_at
FROM sales
ORDER BY created_at DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent sales: %w", err)
	}
	defer rows.Close()
	var out []SaleJournalEntry
	for rows.Next() {
		var entry SaleJournalEntry
		if err := rows.Scan(&entry.ReceiptNo, &entry.Total, &entry.TenderType, &entry.SyncStatus, &entry.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan recent sales: %w", err)
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list recent sales: %w", err)
	}
	return out, nil
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
		if err := rows.Scan(&rres.ItemID, &rres.Name, &rres.Barcode, &rres.Image); err != nil {
			return nil, err
		}
		res = append(res, rres)
	}
	return res, rows.Err()
}

// ResolveShortcutLine returns a priced line for a barcode/lookup used by shortcuts/buttons.
func (r *POSRepo) ResolveShortcutLine(ctx context.Context, code string) (ShortcutLine, bool) {
	// variant barcode
	if row, ok := r.resolveVariant(ctx, code); ok {
		price := r.resolvePrice(ctx, "", row.VariantID, row.Price)
		if row.Variant != "" {
			row.ItemName = row.ItemName + " - " + row.Variant
		}
		return r.toShortcutLine(code, price, row), true
	}
	// item barcode
	if row, ok := r.resolveItem(ctx, code); ok {
		price := r.resolvePrice(ctx, row.ItemID, "", row.Price)
		return r.toShortcutLine(code, price, row), true
	}
	// shortcut barcode
	if row, ok := r.resolveShortcut(ctx, code); ok {
		price := r.resolvePrice(ctx, row.ItemID, row.VariantID, row.Price)
		if row.Label.Valid && row.Label.String != "" {
			row.ItemName = row.Label.String
		}
		return r.toShortcutLine(code, price, row), true
	}

	q := strings.TrimSpace(code)
	if q == "" {
		return ShortcutLine{}, false
	}
	// SKU exact
	if row, ok := r.resolveSKU(ctx, q); ok {
		price := r.resolveRowPrice(ctx, row)
		if row.Variant != "" {
			row.ItemName = row.ItemName + " - " + row.Variant
		}
		return r.toShortcutLine(row.SKU, price, row), true
	}
	// Name like
	if row, ok := r.resolveNameLike(ctx, "%"+q+"%"); ok {
		price := r.resolveRowPrice(ctx, row)
		if row.Variant != "" {
			row.ItemName = row.ItemName + " - " + row.Variant
		} else if row.ItemName == "" {
			row.ItemName = q
		}
		return r.toShortcutLine(q, price, row), true
	}
	return ShortcutLine{}, false
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
