package pos

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/money"
)

// SaleInput captures the data needed to persist a sale (or return).
type SaleInput struct {
	SaleType               string // sale|return
	SaleID                 string
	RegisterID             string
	CashierID              string
	CustomerID             string
	Currency               string
	TaxInclusive           bool
	SaleDiscount           money.Money // fixed discount (minor units) applied to whole sale
	Lines                  []SaleLineInput
	Payments               []PaymentInput
	OriginalSaleID         string // for returns; creates sale_links entry when set
	Note                   string
	ReceiptNo              string
	ActorID                string
	AllowNegativeInventory bool
	Offline                bool
}

type SaleLineInput struct {
	ItemID             string
	VariantID          string
	SKU                string
	Barcode            string
	Name               string
	Qty                float64     // REAL; supports weighed items
	UnitPrice          money.Money // minor units, before discount
	TaxRateBasisPoints int
	LineDiscount       money.Money // fixed minor units
	LocationID         string      // stock movement location
}

type PaymentInput struct {
	MethodID    string
	Amount      money.Money
	Currency    string
	Reference   string
	ChangeGiven money.Money
}

const receiptRetryLimit = 5

var errReceiptConflictRetry = errors.New("receipt_conflict_retry")

const (
	syncStatusQueued = "queued"
	syncStatusSynced = "synced"
)

var receiptAllocator = func(ctx context.Context, tx *sql.Tx, repo *data.POSRepo) (string, error) {
	return repo.NextReceiptNo(ctx, tx)
}

func computeSaleTotals(in SaleInput) (subtotal, taxTotal, total money.Money, err error) {
	for _, l := range in.Lines {
		if err := validateLine(l); err != nil {
			return 0, 0, 0, err
		}
		lineBase := AmountForQuantity(l.UnitPrice, l.Qty)
		if l.LineDiscount.IsNegative() || l.LineDiscount > lineBase {
			return 0, 0, 0, fmt.Errorf("invalid line discount for item %s", l.ItemID)
		}
		lineNet := lineBase.Sub(l.LineDiscount)
		lineTax, _ := ComputeTaxBasisPoints(lineNet, l.TaxRateBasisPoints, in.TaxInclusive)
		subtotal = subtotal.Add(lineNet)
		taxTotal = taxTotal.Add(lineTax)
	}
	total = subtotal.Sub(in.SaleDiscount)
	if !in.TaxInclusive {
		total = total.Add(taxTotal)
	}
	if total.IsNegative() {
		total = 0
	}
	return subtotal, taxTotal, total, nil
}

func netPayments(payments []PaymentInput) (money.Money, error) {
	var sum money.Money
	if len(payments) == 0 {
		return 0, errors.New("sale requires at least one payment")
	}
	for i, p := range payments {
		if p.MethodID == "" {
			return 0, fmt.Errorf("payment %d missing method", i+1)
		}
		if !p.Amount.IsPositive() {
			return 0, fmt.Errorf("payment %d amount must be > 0", i+1)
		}
		if p.ChangeGiven.IsNegative() {
			return 0, fmt.Errorf("payment %d change must be >= 0", i+1)
		}
		if p.ChangeGiven > p.Amount {
			return 0, fmt.Errorf("payment %d change cannot exceed amount", i+1)
		}
		sum = sum.Add(p.Amount.Sub(p.ChangeGiven))
	}
	return sum, nil
}

func deriveTenderType(payments []PaymentInput) string {
	if len(payments) == 0 {
		return "unknown"
	}
	method := strings.ToLower(strings.TrimSpace(payments[0].MethodID))
	if method == "" {
		method = "unknown"
	}
	for i := 1; i < len(payments); i++ {
		next := strings.ToLower(strings.TrimSpace(payments[i].MethodID))
		if next != method {
			return "split"
		}
	}
	return method
}

func isReceiptConflictErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "receipt_no") && strings.Contains(strings.ToLower(msg), "unique")
}

// CompleteSale persists a sale (or return) with lines, payments, stock movements, discounts, and optional sale link.
// It enforces payment coverage, FK constraints, and uses a single transaction for integrity.
func CompleteSale(ctx context.Context, sqlDB *sql.DB, in SaleInput) (string, error) {
	repo := data.NewPOSRepo(sqlDB)
	if len(in.Lines) == 0 {
		return "", errors.New("sale requires at least one line")
	}
	if in.SaleType == "" {
		in.SaleType = "sale"
	}
	if in.Currency == "" {
		in.Currency = "GBP"
	}
	subtotal, taxTotal, total, err := computeSaleTotals(in)
	if err != nil {
		return "", err
	}
	netPaid, err := netPayments(in.Payments)
	if err != nil {
		return "", err
	}
	if netPaid < total {
		return "", fmt.Errorf("payments (%d) do not cover total (%d)", netPaid, total)
	}
	saleID := in.SaleID
	if saleID == "" {
		saleID = uuid.NewString()
	}

	providedReceipt := in.ReceiptNo
	tenderType := deriveTenderType(in.Payments)

	for attempt := 0; attempt < receiptRetryLimit; attempt++ {
		in.ReceiptNo = providedReceipt

		err = db.WithTx(ctx, sqlDB, func(tx *sql.Tx) error {
			if !in.AllowNegativeInventory {
				for _, l := range in.Lines {
					cur, found, err := repo.CurrentQty(ctx, tx, l.LocationID, l.ItemID, l.VariantID)
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

			receiptNo := in.ReceiptNo
			now := time.Now().UTC().Format(time.RFC3339)
			syncStatus := syncStatusSynced
			syncNextAttemptAt := ""
			if in.Offline {
				syncStatus = syncStatusQueued
				syncNextAttemptAt = now
			}
			if receiptNo == "" {
				var err error
				receiptNo, err = receiptAllocator(ctx, tx, repo)
				if err != nil {
					return err
				}
			}
			if err := repo.InsertSale(ctx, tx, saleID, receiptNo, in.SaleType, in.RegisterID, in.CashierID, in.CustomerID, in.Currency, subtotal.Minor(), in.SaleDiscount.Minor(), taxTotal.Minor(), total.Minor(), in.Note, now, tenderType, in.Offline, syncStatus, 0, syncNextAttemptAt, ""); err != nil {
				if in.ReceiptNo == "" && isReceiptConflictErr(err) {
					return errReceiptConflictRetry
				}
				return err
			}
			in.ReceiptNo = receiptNo

			var saleDiscountID string
			if in.SaleDiscount.IsPositive() {
				saleDiscountID = uuid.NewString()
				if err := repo.InsertSaleDiscount(ctx, tx, saleDiscountID, saleID, "", "fixed", in.SaleDiscount.Minor(), in.SaleDiscount.Minor(), "sale_discount"); err != nil {
					return err
				}
			}

			for i, l := range in.Lines {
				lineID := uuid.NewString()
				lineBase := AmountForQuantity(l.UnitPrice, l.Qty)
				lineNet := lineBase.Sub(l.LineDiscount)
				lineTax, _ := ComputeTaxBasisPoints(lineNet, l.TaxRateBasisPoints, in.TaxInclusive)
				totalBeforeTax := lineNet
				totalAfterTax := lineNet.Add(lineTax)

				if err := repo.InsertSaleLine(ctx, tx, lineID, saleID, i+1, l.ItemID, l.VariantID, l.Name, l.SKU, l.Barcode, l.Qty, l.UnitPrice.Minor(), l.LineDiscount.Minor(), l.TaxRateBasisPoints, lineTax.Minor(), totalBeforeTax.Minor(), totalAfterTax.Minor()); err != nil {
					return err
				}

				if l.LineDiscount.IsPositive() {
					if err := repo.InsertSaleDiscount(ctx, tx, uuid.NewString(), saleID, lineID, "fixed", l.LineDiscount.Minor(), l.LineDiscount.Minor(), "line_discount"); err != nil {
						return err
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
				if _, err := repo.RecordStockMovement(ctx, tx, data.StockMovementInput{
					ItemID:     l.ItemID,
					VariantID:  l.VariantID,
					LocationID: l.LocationID,
					SaleLineID: lineID,
					Type:       in.SaleType,
					Quantity:   qty,
					ActorID:    in.ActorID,
				}); err != nil {
					return err
				}
			}

			for _, p := range in.Payments {
				if err := repo.InsertPayment(ctx, tx, uuid.NewString(), saleID, p.MethodID, p.Amount.Minor(), valueOrDefault(p.Currency, in.Currency), p.Reference, p.ChangeGiven.Minor(), time.Now().UTC().Format(time.RFC3339)); err != nil {
					return err
				}
			}

			if in.SaleType == "return" && in.OriginalSaleID != "" {
				if err := repo.InsertSaleLink(ctx, tx, uuid.NewString(), saleID, in.OriginalSaleID, "return"); err != nil {
					return err
				}
			}

			pluginVersions, err := repo.ListActivePluginVersions(ctx, tx)
			if err != nil {
				return err
			}
			plugins := make(map[string]string, len(pluginVersions))
			for _, p := range pluginVersions {
				plugins[p.ID] = p.Version
			}

			if err := repo.InsertAudit(ctx, tx, in.ActorID, "sale", saleID, auditAction(in.SaleType), map[string]any{
				"subtotal": subtotal,
				"taxTotal": taxTotal,
				"total":    total,
				"action":   auditAction(in.SaleType),
				"reason":   in.Note,
				"offline":  in.Offline,
				"tender":   tenderType,
				"sync":     syncStatus,
				"plugins":  plugins,
				"ts":       time.Now().UTC().Format(time.RFC3339),
			}, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
				return err
			}

			return nil
		})
		if err == nil {
			return saleID, nil
		}
		if errors.Is(err, errReceiptConflictRetry) {
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("insert sale: unable to allocate receipt number")
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
	repo := data.NewPOSRepo(sqlDB)
	err := db.WithTx(ctx, sqlDB, func(tx *sql.Tx) error {
		if err := repo.UpdateSaleStatus(ctx, tx, saleID, status); err != nil {
			return err
		}
		if err := repo.InsertAudit(ctx, tx, actorID, "sale", saleID, status, map[string]any{
			"reason":   reason,
			"status":   status,
			"ts":       time.Now().UTC().Format(time.RFC3339),
			"subtotal": 0,
			"taxTotal": 0,
			"total":    0,
		}, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
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
	// numeric-ish receipt no derived from timestamp for readability
	n := time.Now().UnixNano() % 1000000000
	return fmt.Sprintf("%09d", n)
}

func valueOrDefault(val, def string) string {
	if val != "" {
		return val
	}
	return def
}

func auditAction(saleType string) string {
	if saleType == "return" {
		return "refund"
	}
	return "complete"
}

type PaymentFailure struct {
	SaleID   string
	ActorID  string
	Reason   string
	Payments []PaymentInput
	Lines    []SaleLineInput
	Total    int64
	Currency string
}

// RecordPaymentFailure logs a recoverable payment failure attempt for later retry/audit.
func RecordPaymentFailure(ctx context.Context, sqlDB *sql.DB, failure PaymentFailure) (string, error) {
	repo := data.NewPOSRepo(sqlDB)
	return repo.RecordPaymentFailure(ctx, data.PaymentFailure{
		SaleID:   failure.SaleID,
		ActorID:  failure.ActorID,
		Reason:   failure.Reason,
		Payments: toAnySlice(failure.Payments),
		Lines:    toAnySlice(failure.Lines),
		Total:    failure.Total,
		Currency: failure.Currency,
	})
}

func toAnySlice[T any](in []T) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
