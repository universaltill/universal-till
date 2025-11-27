package pos

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/db"
)

// SaleInput captures the data needed to persist a sale (or return).
type SaleInput struct {
	SaleType               string // sale|return
	RegisterID             string
	CashierID              string
	CustomerID             string
	Currency               string
	TaxInclusive           bool
	SaleDiscount           int64 // fixed discount (minor units) applied to whole sale
	Lines                  []SaleLineInput
	Payments               []PaymentInput
	OriginalSaleID         string // for returns; creates sale_links entry when set
	Note                   string
	ReceiptNo              string
	ActorID                string
	AllowNegativeInventory bool
}

type SaleLineInput struct {
	ItemID             string
	VariantID          string
	SKU                string
	Barcode            string
	Name               string
	Qty                float64 // REAL; supports weighed items
	UnitPrice          int64   // minor units, before discount
	TaxRateBasisPoints int
	LineDiscount       int64  // fixed minor units
	LocationID         string // stock movement location
}

type PaymentInput struct {
	MethodID    string
	Amount      int64
	Currency    string
	Reference   string
	ChangeGiven int64
}

// CompleteSale persists a sale (or return) with lines, payments, stock movements, discounts, and optional sale link.
// It enforces payment coverage, FK constraints, and uses a single transaction for integrity.
func CompleteSale(ctx context.Context, sqlDB *sql.DB, in SaleInput) (string, error) {
	if len(in.Lines) == 0 {
		return "", errors.New("sale requires at least one line")
	}
	if len(in.Payments) == 0 {
		return "", errors.New("sale requires at least one payment")
	}
	if in.SaleType == "" {
		in.SaleType = "sale"
	}
	if in.Currency == "" {
		in.Currency = "GBP"
	}
	if in.ReceiptNo == "" {
		in.ReceiptNo = generateReceiptNo()
	}

	// Compute totals
	var subtotal int64
	var taxTotal int64
	for _, l := range in.Lines {
		if err := validateLine(l); err != nil {
			return "", err
		}
		lineBase := AmountForQuantity(l.UnitPrice, l.Qty)
		if l.LineDiscount < 0 || l.LineDiscount > lineBase {
			return "", fmt.Errorf("invalid line discount for item %s", l.ItemID)
		}
		lineNet := lineBase - l.LineDiscount
		lineTax, _ := ComputeTaxBasisPoints(lineNet, l.TaxRateBasisPoints, in.TaxInclusive)
		subtotal += lineNet
		taxTotal += lineTax
	}
	total := subtotal - in.SaleDiscount
	if !in.TaxInclusive {
		total += taxTotal
	}
	if total < 0 {
		total = 0
	}

	var paymentsSum int64
	for _, p := range in.Payments {
		paymentsSum += p.Amount
	}
	if paymentsSum < total {
		return "", fmt.Errorf("payments (%d) do not cover total (%d)", paymentsSum, total)
	}

	saleID := uuid.NewString()

	err := db.WithTx(ctx, sqlDB, func(tx *sql.Tx) error {
		if !in.AllowNegativeInventory {
			for _, l := range in.Lines {
				cur, found, err := currentQty(ctx, tx, l.LocationID, l.ItemID, l.VariantID)
				if err != nil {
					return err
				}
				if !found {
					cur = 0
				}
				qtyDelta := l.Qty
				if in.SaleType == "sale" {
					qtyDelta = -qtyDelta
				}
				if cur+qtyDelta < 0 {
					return fmt.Errorf("insufficient stock for item %s at location %s (have %.2f, need %.2f)", valueOrDefault(l.ItemID, l.VariantID), l.LocationID, cur, l.Qty)
				}
			}
		}

		if _, err := tx.ExecContext(ctx, `
INSERT INTO sales (id, receipt_no, status, sale_type, register_id, cashier_id, customer_id, currency, subtotal, discount_total, tax_total, total, rounding, note, created_at)
VALUES (?, ?, 'completed', ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
`, saleID, in.ReceiptNo, in.SaleType, nullIfEmpty(in.RegisterID), nullIfEmpty(in.CashierID), nullIfEmpty(in.CustomerID), in.Currency, subtotal, in.SaleDiscount, taxTotal, total, nullIfEmpty(in.Note), time.Now().UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("insert sale: %w", err)
		}

		var saleDiscountID string
		if in.SaleDiscount > 0 {
			saleDiscountID = uuid.NewString()
			if _, err := tx.ExecContext(ctx, `
INSERT INTO sale_discounts (id, sale_id, line_id, type, value, amount, reason)
VALUES (?, ?, NULL, 'fixed', ?, ?, 'sale_discount')
`, saleDiscountID, saleID, in.SaleDiscount, in.SaleDiscount); err != nil {
				return fmt.Errorf("insert sale discount: %w", err)
			}
		}

		for i, l := range in.Lines {
			lineID := uuid.NewString()
			lineBase := AmountForQuantity(l.UnitPrice, l.Qty)
			lineNet := lineBase - l.LineDiscount
			lineTax, _ := ComputeTaxBasisPoints(lineNet, l.TaxRateBasisPoints, in.TaxInclusive)
			totalBeforeTax := lineNet
			totalAfterTax := lineNet + lineTax

			if _, err := tx.ExecContext(ctx, `
INSERT INTO sale_lines (id, sale_id, line_no, item_id, variant_id, name_snapshot, sku_snapshot, barcode_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, lineID, saleID, i+1, nullIfEmpty(l.ItemID), nullIfEmpty(l.VariantID), l.Name, l.SKU, l.Barcode, l.Qty, l.UnitPrice, l.LineDiscount, l.TaxRateBasisPoints, lineTax, totalBeforeTax, totalAfterTax); err != nil {
				return fmt.Errorf("insert sale line: %w", err)
			}

			if l.LineDiscount > 0 {
				if _, err := tx.ExecContext(ctx, `
INSERT INTO sale_discounts (id, sale_id, line_id, type, value, amount, reason)
VALUES (?, ?, ?, 'fixed', ?, ?, 'line_discount')
`, uuid.NewString(), saleID, lineID, l.LineDiscount, l.LineDiscount); err != nil {
					return fmt.Errorf("insert line discount: %w", err)
				}
			}

			// Stock movement: negative for sale, positive for return
			qty := l.Qty
			if in.SaleType == "sale" {
				qty = -qty
			}
			if in.SaleType == "return" {
				// keep positive
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO stock_movements (id, item_id, variant_id, location_id, sale_line_id, type, quantity, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, uuid.NewString(), nullIfEmpty(l.ItemID), nullIfEmpty(l.VariantID), l.LocationID, lineID, in.SaleType, qty, time.Now().UTC().Format(time.RFC3339)); err != nil {
				return fmt.Errorf("insert stock movement: %w", err)
			}

			// inventory upsert with null-safe match
			res, err := tx.ExecContext(ctx, `
UPDATE inventory
SET quantity = quantity + ?, updated_at = ?
WHERE location_id = ?
  AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL))
`, qty, time.Now().UTC().Format(time.RFC3339), l.LocationID, nullString(l.ItemID), nullString(l.VariantID))
			if err != nil {
				return fmt.Errorf("update inventory: %w", err)
			}
			aff, _ := res.RowsAffected()
			if aff == 0 {
				if _, err := tx.ExecContext(ctx, `
INSERT INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
`, uuid.NewString(), nullIfEmpty(l.ItemID), nullIfEmpty(l.VariantID), l.LocationID, qty, time.Now().UTC().Format(time.RFC3339)); err != nil {
					return fmt.Errorf("insert inventory: %w", err)
				}
			}
		}

		for _, p := range in.Payments {
			if p.Amount <= 0 {
				return fmt.Errorf("payment amount must be > 0")
			}
			if _, err := tx.ExecContext(ctx, `
INSERT INTO payments (id, sale_id, method_id, amount, currency, reference, change_given, paid_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, uuid.NewString(), saleID, p.MethodID, p.Amount, valueOrDefault(p.Currency, in.Currency), nullIfEmpty(p.Reference), p.ChangeGiven, time.Now().UTC().Format(time.RFC3339)); err != nil {
				return fmt.Errorf("insert payment: %w", err)
			}
		}

		if in.SaleType == "return" && in.OriginalSaleID != "" {
			if _, err := tx.ExecContext(ctx, `
INSERT INTO sale_links (id, sale_id, original_sale_id, reason)
VALUES (?, ?, ?, 'return')
`, uuid.NewString(), saleID, in.OriginalSaleID); err != nil {
				return fmt.Errorf("insert sale link: %w", err)
			}
		}

		if err := insertAudit(ctx, tx, in.ActorID, "sale", saleID, auditAction(in.SaleType), in.Note, subtotal, taxTotal, total); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return "", err
	}
	return saleID, nil
}

// UpdateSaleStatus updates sale.status and writes audit_log. Status expected: open|parked|voided|refunded.
func UpdateSaleStatus(ctx context.Context, sqlDB *sql.DB, saleID, status, actorID, reason string) error {
	if saleID == "" {
		return errors.New("saleID required")
	}
	if status == "" {
		return errors.New("status required")
	}
	switch status {
	case "open", "parked", "voided", "refunded", "completed":
	default:
		return fmt.Errorf("invalid status: %s", status)
	}
	err := db.WithTx(ctx, sqlDB, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
UPDATE sales
SET status = ?, voided_at = CASE WHEN ? = 'voided' THEN ? ELSE voided_at END
WHERE id = ?
`, status, status, time.Now().UTC().Format(time.RFC3339), saleID); err != nil {
			return fmt.Errorf("update sale status: %w", err)
		}
		if err := insertAudit(ctx, tx, actorID, "sale", saleID, status, reason, 0, 0, 0); err != nil {
			return err
		}
		return nil
	})
	return err
}

func validateLine(l SaleLineInput) error {
	if l.ItemID == "" && l.VariantID == "" {
		return errors.New("line requires item_id or variant_id")
	}
	if l.ItemID != "" && l.VariantID != "" {
		return errors.New("line cannot have both item_id and variant_id")
	}
	if l.Qty <= 0 {
		return errors.New("quantity must be > 0")
	}
	if l.UnitPrice < 0 {
		return errors.New("unit price must be >= 0")
	}
	if l.LocationID == "" {
		return errors.New("location_id is required")
	}
	return nil
}

func generateReceiptNo() string {
	id := uuid.NewString()
	if len(id) > 8 {
		return id[len(id)-8:]
	}
	return id
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func valueOrDefault(val, def string) string {
	if val != "" {
		return val
	}
	return def
}

func currentQty(ctx context.Context, tx *sql.Tx, locationID, itemID, variantID string) (float64, bool, error) {
	var qty float64
	err := tx.QueryRowContext(ctx, `
SELECT COALESCE(quantity, 0)
FROM inventory
WHERE location_id = ?
  AND ((item_id = ? AND variant_id IS NULL) OR (variant_id = ? AND item_id IS NULL))
`, locationID, nullString(itemID), nullString(variantID)).Scan(&qty)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("read inventory: %w", err)
	}
	return qty, true, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func insertAudit(ctx context.Context, tx *sql.Tx, actorID, entityType, entityID, action, reason string, subtotal, taxTotal, total int64) error {
	payload := map[string]any{
		"subtotal": subtotal,
		"taxTotal": taxTotal,
		"total":    total,
		"action":   action,
		"reason":   reason,
		"ts":       time.Now().UTC().Format(time.RFC3339),
	}
	data, _ := json.Marshal(payload)
	_, err := tx.ExecContext(ctx, `
INSERT INTO audit_log (id, actor_id, entity_type, entity_id, action, data_json, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, uuid.NewString(), nullIfEmpty(actorID), entityType, entityID, action, string(data), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("insert audit_log: %w", err)
	}
	return nil
}

func auditAction(saleType string) string {
	if saleType == "return" {
		return "refund"
	}
	return "complete"
}
