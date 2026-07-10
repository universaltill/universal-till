package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/catalogtypes"
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
	SKU        string
	Name       string
	ItemID     string
	VariantID  string
	Price      int64
	TaxRateBP  int
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
	ItemID       string
	Name         string
	SKU          string
	LocationID   string
	LocationName string
	CurrentQty   float64
	ReorderLevel int
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
	inv.location_id,
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
		query += ` AND inv.location_id = ?`
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

type DailySales struct {
	Day      string
	Count    int
	Total    int64
	TaxTotal int64
}

type TopItem struct {
	Name    string
	Qty     float64
	Revenue int64
}

type MethodTotal struct {
	Method string
	Count  int
	Amount int64
}

// SalesByDay aggregates completed sales per day for the last N days.
func (r *POSRepo) SalesByDay(ctx context.Context, days int) ([]DailySales, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT date(created_at) AS day, COUNT(*), COALESCE(SUM(total), 0), COALESCE(SUM(tax_total), 0)
FROM sales
WHERE status = 'completed' AND created_at >= datetime('now', ?)
GROUP BY day ORDER BY day DESC`, fmt.Sprintf("-%d days", days))
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

// TopItems returns the best sellers by revenue for the last N days.
func (r *POSRepo) TopItems(ctx context.Context, days, limit int) ([]TopItem, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT sl.name_snapshot, SUM(sl.quantity), COALESCE(SUM(sl.total_after_tax), 0) AS revenue
FROM sale_lines sl
JOIN sales s ON s.id = sl.sale_id
WHERE s.status = 'completed' AND s.created_at >= datetime('now', ?)
GROUP BY sl.name_snapshot ORDER BY revenue DESC LIMIT ?`, fmt.Sprintf("-%d days", days), limit)
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

// PaymentBreakdown sums applied payments per method for the last N days.
func (r *POSRepo) PaymentBreakdown(ctx context.Context, days int) ([]MethodTotal, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT p.method_id, COUNT(*), COALESCE(SUM(p.amount - p.change_given), 0) AS applied
FROM payments p
JOIN sales s ON s.id = p.sale_id
WHERE s.status = 'completed' AND s.created_at >= datetime('now', ?)
GROUP BY p.method_id ORDER BY applied DESC`, fmt.Sprintf("-%d days", days))
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
       COALESCE(inv.quantity, 0), COALESCE(i.reorder_level, 0)
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
		if err := rows.Scan(&item.ItemID, &item.Name, &item.SKU, &item.LocationID, &item.LocationName, &item.CurrentQty, &item.ReorderLevel); err != nil {
			return nil, fmt.Errorf("scan stock level: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate stock levels: %w", err)
	}
	return items, nil
}

// NextReceiptNo returns the next available receipt number.
func (r *POSRepo) NextReceiptNo(ctx context.Context, tx *sql.Tx) (string, error) {
	exec := r.exec(tx)
	var maxVal sql.NullInt64
	if err := exec.QueryRowContext(ctx, `SELECT COALESCE(MAX(CAST(receipt_no AS INTEGER)), 0) FROM sales`).Scan(&maxVal); err != nil {
		return "", fmt.Errorf("next receipt no: %w", err)
	}
	next := maxVal.Int64 + 1
	if next < 1 {
		next = 1
	}
	return fmt.Sprintf("%09d", next), nil
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

// InsertSale writes the sale header row.
func (r *POSRepo) InsertSale(ctx context.Context, tx *sql.Tx, saleID, receiptNo, saleType, registerID, cashierID, customerID, currency string, subtotal, discountTotal, taxTotal, total int64, note, createdAt, tenderType string, offline bool, syncStatus string, syncAttempts int, syncNextAttemptAt, syncLastError string) error {
	offlineVal := 0
	if offline {
		offlineVal = 1
	}
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO sales (id, receipt_no, status, sale_type, tender_type, offline, sync_status, sync_attempts, sync_next_attempt_at, sync_last_error, register_id, cashier_id, customer_id, currency, subtotal, discount_total, tax_total, total, rounding, note, created_at, completed_at)
VALUES (?, ?, 'completed', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)
`, saleID, receiptNo, saleType, tenderType, offlineVal, syncStatus, syncAttempts, nullIfEmpty(syncNextAttemptAt), nullIfEmpty(syncLastError), nullIfEmpty(registerID), nullIfEmpty(cashierID), nullIfEmpty(customerID), currency, subtotal, discountTotal, taxTotal, total, nullIfEmpty(note), createdAt, createdAt)
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

// InsertPayment writes a payment row.
func (r *POSRepo) InsertPayment(ctx context.Context, tx *sql.Tx, paymentID, saleID, methodID string, amount int64, currency, reference string, changeGiven int64, paidAt string) error {
	_, err := r.exec(tx).ExecContext(ctx, `
INSERT INTO payments (id, sale_id, method_id, amount, currency, reference, change_given, paid_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, paymentID, saleID, methodID, amount, currency, nullIfEmpty(reference), changeGiven, paidAt)
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

// UpdateSaleStatus updates sale status (and optionally voided_at when voided).
func (r *POSRepo) UpdateSaleStatus(ctx context.Context, tx *sql.Tx, saleID, status string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := r.exec(tx).ExecContext(ctx, `
UPDATE sales
SET status = ?, voided_at = CASE WHEN ? = 'voided' THEN ? ELSE voided_at END
WHERE id = ?
`, status, status, now, saleID)
	if err != nil {
		return fmt.Errorf("update sale status: %w", err)
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
	var completed string
	err := r.db.QueryRowContext(ctx, `SELECT completed_at FROM sales WHERE id = ?`, saleID).Scan(&completed)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("sale completed_at: %w", err)
	}
	if strings.TrimSpace(completed) == "" {
		return time.Time{}, false, nil
	}
	ts, err := time.Parse(time.RFC3339, completed)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("parse completed_at: %w", err)
	}
	return ts, true, nil
}

// SaleDetail is everything the journal shows when a sale is opened.
type SaleDetail struct {
	ID            string
	ReceiptNo     string
	Status        string
	SaleType      string
	TenderType    string
	Offline       bool
	SyncStatus    string
	Currency      string
	Subtotal      int64
	DiscountTotal int64
	TaxTotal      int64
	Total         int64
	CreatedAt     string
	Lines         []SaleDetailLine
	Payments      []SaleDetailPayment
}

type SaleDetailLine struct {
	Name         string
	SKU          string
	Qty          float64
	UnitPrice    int64
	LineDiscount int64
	TaxAmount    int64
	LineTotal    int64
}

type SaleDetailPayment struct {
	Method      string
	Amount      int64
	ChangeGiven int64
	Reference   string
	PaidAt      string
}

// GetSaleDetail loads a sale with its lines and payments by receipt number.
func (r *POSRepo) GetSaleDetail(ctx context.Context, receiptNo string) (SaleDetail, bool, error) {
	var d SaleDetail
	err := r.db.QueryRowContext(ctx, `
SELECT id, receipt_no, status, sale_type, tender_type, offline, sync_status,
       currency, subtotal, discount_total, tax_total, total, created_at
FROM sales WHERE receipt_no = ?`, receiptNo).Scan(
		&d.ID, &d.ReceiptNo, &d.Status, &d.SaleType, &d.TenderType, &d.Offline,
		&d.SyncStatus, &d.Currency, &d.Subtotal, &d.DiscountTotal, &d.TaxTotal,
		&d.Total, &d.CreatedAt)
	if err == sql.ErrNoRows {
		return SaleDetail{}, false, nil
	}
	if err != nil {
		return SaleDetail{}, false, fmt.Errorf("get sale detail: %w", err)
	}

	lineRows, err := r.db.QueryContext(ctx, `
SELECT name_snapshot, COALESCE(sku_snapshot, ''), quantity, unit_price,
       line_discount, tax_amount, total_after_tax
FROM sale_lines WHERE sale_id = ? ORDER BY line_no`, d.ID)
	if err != nil {
		return SaleDetail{}, false, fmt.Errorf("get sale lines: %w", err)
	}
	defer lineRows.Close()
	for lineRows.Next() {
		var l SaleDetailLine
		if err := lineRows.Scan(&l.Name, &l.SKU, &l.Qty, &l.UnitPrice, &l.LineDiscount, &l.TaxAmount, &l.LineTotal); err != nil {
			return SaleDetail{}, false, fmt.Errorf("scan sale line: %w", err)
		}
		d.Lines = append(d.Lines, l)
	}
	if err := lineRows.Err(); err != nil {
		return SaleDetail{}, false, fmt.Errorf("iterate sale lines: %w", err)
	}

	payRows, err := r.db.QueryContext(ctx, `
SELECT method_id, amount, change_given, COALESCE(reference, ''), paid_at
FROM payments WHERE sale_id = ? ORDER BY paid_at`, d.ID)
	if err != nil {
		return SaleDetail{}, false, fmt.Errorf("get sale payments: %w", err)
	}
	defer payRows.Close()
	for payRows.Next() {
		var p SaleDetailPayment
		if err := payRows.Scan(&p.Method, &p.Amount, &p.ChangeGiven, &p.Reference, &p.PaidAt); err != nil {
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
       (
         SELECT ib.barcode
         FROM item_barcodes ib
         WHERE ib.item_id = i.id
         ORDER BY ib.is_primary DESC
         LIMIT 1
       ) AS barcode,
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
		name := row.ItemName
		if row.Variant != "" {
			name = name + " - " + row.Variant
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
		price := r.resolvePrice(ctx, row.ItemID, row.VariantID, row.Price)
		return r.toShortcutLine(row.SKU, price, row), true
	}
	// Name like
	if row, ok := r.resolveNameLike(ctx, "%"+q+"%"); ok {
		price := r.resolvePrice(ctx, row.ItemID, row.VariantID, row.Price)
		if row.ItemName == "" {
			row.ItemName = q
		}
		return r.toShortcutLine(q, price, row), true
	}
	return ShortcutLine{}, false
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
  AND (ends_at IS NULL OR ends_at > CURRENT_TIMESTAMP)
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
	IsWeighed sql.NullInt64
	Label     sql.NullString
}

func (r *POSRepo) resolveVariant(ctx context.Context, code string) (shortcutPriceRow, bool) {
	row := r.db.QueryRowContext(ctx, `
SELECT i.id, i.name, v.id, v.name, v.price, i.is_weighed,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0)
FROM variant_barcodes vb
JOIN item_variants v ON v.id = vb.variant_id
JOIN items i ON i.id = v.item_id
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE vb.barcode = ?
  AND i.is_active = 1 AND v.is_active = 1
LIMIT 1
`, code)
	var res shortcutPriceRow
	if err := row.Scan(&res.ItemID, &res.ItemName, &res.VariantID, &res.Variant, &res.Price, &res.IsWeighed, &res.Image, &res.TaxRateBP); err != nil {
		return shortcutPriceRow{}, false
	}
	return res, true
}

func (r *POSRepo) resolveItem(ctx context.Context, code string) (shortcutPriceRow, bool) {
	row := r.db.QueryRowContext(ctx, `
SELECT i.id, i.name, i.base_price, i.is_weighed,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0)
FROM item_barcodes ib
JOIN items i ON i.id = ib.item_id
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE ib.barcode = ?
  AND i.is_active = 1
LIMIT 1
`, code)
	var res shortcutPriceRow
	if err := row.Scan(&res.ItemID, &res.ItemName, &res.Price, &res.IsWeighed, &res.Image, &res.TaxRateBP); err != nil {
		return shortcutPriceRow{}, false
	}
	return res, true
}

func (r *POSRepo) resolveShortcut(ctx context.Context, code string) (shortcutPriceRow, bool) {
	row := r.db.QueryRowContext(ctx, `
SELECT sb.item_id, sb.label, i.base_price, i.is_weighed,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0)
FROM shortcut_buttons sb
JOIN items i ON i.id = sb.item_id
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE sb.barcode = ?
  AND i.is_active = 1
LIMIT 1
`, code)
	var res shortcutPriceRow
	if err := row.Scan(&res.ItemID, &res.Label, &res.Price, &res.IsWeighed, &res.Image, &res.TaxRateBP); err != nil {
		return shortcutPriceRow{}, false
	}
	res.ItemName = res.Label.String
	return res, true
}

func (r *POSRepo) resolveSKU(ctx context.Context, sku string) (shortcutPriceRow, bool) {
	row := r.db.QueryRowContext(ctx, `
SELECT i.id, i.sku, i.name, i.base_price, i.is_weighed,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0)
FROM items i
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE i.is_active = 1 AND i.sku = ?
LIMIT 1
`, sku)
	var res shortcutPriceRow
	if err := row.Scan(&res.ItemID, &res.SKU, &res.ItemName, &res.Price, &res.IsWeighed, &res.Image, &res.TaxRateBP); err != nil {
		return shortcutPriceRow{}, false
	}
	return res, true
}

func (r *POSRepo) resolveNameLike(ctx context.Context, like string) (shortcutPriceRow, bool) {
	row := r.db.QueryRowContext(ctx, `
SELECT i.id, i.name, i.base_price, i.is_weighed,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0)
FROM items i
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE i.is_active = 1 AND i.name LIKE ?
ORDER BY i.name
LIMIT 1
`, like)
	var res shortcutPriceRow
	if err := row.Scan(&res.ItemID, &res.ItemName, &res.Price, &res.IsWeighed, &res.Image, &res.TaxRateBP); err != nil {
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
