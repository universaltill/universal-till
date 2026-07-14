package pos

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	_ "modernc.org/sqlite"
)

func setupSaleDB(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), fmt.Sprintf("pos_%d.db", time.Now().UnixNano()))
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		t.Fatalf("set busy_timeout: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("enable fks: %v", err)
	}
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE stock_locations (id TEXT PRIMARY KEY, name TEXT);`,
		`CREATE TABLE items (id TEXT PRIMARY KEY, sku TEXT, name TEXT, base_price INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE item_variants (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, price INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE sales (id TEXT PRIMARY KEY, receipt_no TEXT NOT NULL UNIQUE, status TEXT NOT NULL, sale_type TEXT NOT NULL, tender_type TEXT NOT NULL DEFAULT 'unknown', offline INTEGER NOT NULL DEFAULT 0, sync_status TEXT NOT NULL DEFAULT 'queued', sync_attempts INTEGER NOT NULL DEFAULT 0, sync_next_attempt_at TEXT, sync_last_error TEXT, register_id TEXT, cashier_id TEXT, customer_id TEXT, currency TEXT NOT NULL, subtotal INTEGER NOT NULL, discount_total INTEGER NOT NULL, tax_total INTEGER NOT NULL, total INTEGER NOT NULL, rounding INTEGER NOT NULL DEFAULT 0, note TEXT, created_at TEXT NOT NULL, completed_at TEXT, voided_at TEXT);`,
		`CREATE TABLE sale_lines (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, line_no INTEGER NOT NULL, item_id TEXT, variant_id TEXT, name_snapshot TEXT NOT NULL, sku_snapshot TEXT, barcode_snapshot TEXT, quantity REAL NOT NULL, unit_price INTEGER NOT NULL, line_discount INTEGER NOT NULL DEFAULT 0, tax_rate_bp INTEGER NOT NULL, tax_amount INTEGER NOT NULL, total_before_tax INTEGER NOT NULL, total_after_tax INTEGER NOT NULL, FOREIGN KEY (sale_id) REFERENCES sales(id));`,
		`CREATE TABLE sale_discounts (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, line_id TEXT, type TEXT NOT NULL, value INTEGER NOT NULL, amount INTEGER NOT NULL, reason TEXT);`,
		`CREATE TABLE payments (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, method_id TEXT NOT NULL, amount INTEGER NOT NULL, currency TEXT NOT NULL, reference TEXT, change_given INTEGER NOT NULL DEFAULT 0, paid_at TEXT NOT NULL, FOREIGN KEY (sale_id) REFERENCES sales(id));`,
		`CREATE TABLE sale_links (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, original_sale_id TEXT NOT NULL, reason TEXT);`,
		`CREATE TABLE stock_movements (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, sale_line_id TEXT, type TEXT NOT NULL, quantity REAL NOT NULL, created_at TEXT NOT NULL);`,
		`CREATE TABLE inventory (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, quantity REAL NOT NULL, updated_at TEXT NOT NULL, UNIQUE(item_id, variant_id, location_id));`,
		`CREATE TABLE payment_methods (id TEXT PRIMARY KEY, name TEXT, type TEXT, is_active INTEGER DEFAULT 1);`,
		`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL, data_json TEXT, created_at TEXT NOT NULL);`,
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, is_active INTEGER DEFAULT 1);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	return db
}

func TestCompleteSale_SucceedsAndWritesRows(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType:     "sale",
		RegisterID:   "reg1",
		CashierID:    "user1",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{
			{
				ItemID:             "itm1",
				SKU:                "SKU1",
				Name:               "Apple",
				Qty:                2,
				UnitPrice:          500,
				TaxRateBasisPoints: 2000,
				LineDiscount:       0,
				LocationID:         "loc1",
			},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 1200, Currency: "GBP"},
		},
		AllowNegativeInventory: false,
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale error: %v", err)
	}
	if saleID == "" {
		t.Fatalf("expected saleID")
	}

	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 sale, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM sale_lines`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 sale line, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM payments`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 payment, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM stock_movements`).Scan(&count)
	if count != 1 {
		t.Fatalf("expected 1 stock movement, got %d", count)
	}
	var qty float64
	_ = db.QueryRow(`SELECT quantity FROM inventory WHERE item_id='itm1' AND location_id='loc1'`).Scan(&qty)
	if qty != 3 {
		t.Fatalf("expected inventory 3, got %v", qty)
	}
}

func TestCompleteSale_RejectsUnderpayment(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)

	in := SaleInput{
		SaleType:               "sale",
		Currency:               "GBP",
		AllowNegativeInventory: false,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 500, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 100, Currency: "GBP"},
		},
	}

	if _, err := CompleteSale(ctx, db, in); err == nil {
		t.Fatalf("expected underpayment to fail")
	}
}

func TestCompleteSale_OfflineSyncFlagsAndAuditPlugins(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)
	_, _ = db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('p1','Test Plugin','1.2.3',1)`)

	in := SaleInput{
		SaleType:     "sale",
		RegisterID:   "reg1",
		CashierID:    "user1",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{
			{
				ItemID:             "itm1",
				SKU:                "SKU1",
				Name:               "Apple",
				Qty:                1,
				UnitPrice:          500,
				TaxRateBasisPoints: 2000,
				LineDiscount:       0,
				LocationID:         "loc1",
			},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 600, Currency: "GBP"},
		},
		AllowNegativeInventory: false,
		Offline:                true,
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale error: %v", err)
	}

	var offline int
	var syncStatus string
	var syncAttempts int
	var tenderType string
	if err := db.QueryRow(`SELECT offline, sync_status, sync_attempts, tender_type FROM sales WHERE id = ?`, saleID).Scan(&offline, &syncStatus, &syncAttempts, &tenderType); err != nil {
		t.Fatalf("read sale flags: %v", err)
	}
	if offline != 1 {
		t.Fatalf("expected offline=1, got %d", offline)
	}
	if syncStatus != "queued" {
		t.Fatalf("expected sync_status queued, got %s", syncStatus)
	}
	if syncAttempts != 0 {
		t.Fatalf("expected sync_attempts 0, got %d", syncAttempts)
	}
	if tenderType != "cash" {
		t.Fatalf("expected tender_type cash, got %s", tenderType)
	}

	var dataJSON string
	if err := db.QueryRow(`SELECT data_json FROM audit_log WHERE entity_id = ? LIMIT 1`, saleID).Scan(&dataJSON); err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(dataJSON), &payload); err != nil {
		t.Fatalf("decode audit log: %v", err)
	}
	pluginsRaw, ok := payload["plugins"].(map[string]any)
	if !ok {
		t.Fatalf("expected plugins in audit payload")
	}
	if pluginsRaw["p1"] != "1.2.3" {
		t.Fatalf("expected plugin version in audit payload, got %v", pluginsRaw["p1"])
	}
}

func TestCompleteSale_InclusiveTaxNoDoubleCount(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 120, 1)`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)

	in := SaleInput{
		SaleType:     "sale",
		Currency:     "GBP",
		TaxInclusive: true,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 120, TaxRateBasisPoints: 2000, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 120, Currency: "GBP"},
		},
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale error: %v", err)
	}
	var storedTotal int64
	var taxTotal int64
	if err := db.QueryRow(`SELECT total, tax_total FROM sales WHERE id = ?`, saleID).Scan(&storedTotal, &taxTotal); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if storedTotal != 120 {
		t.Fatalf("expected total 120 (inclusive), got %d", storedTotal)
	}
	if taxTotal <= 0 {
		t.Fatalf("expected tax_total > 0 for inclusive tax, got %d", taxTotal)
	}
	// The LINE must agree with the header: with inclusive pricing the
	// after-tax total IS the ticket price; before-tax is net of the tax.
	// (Regression: after-tax used to get the tax added ON TOP → 140.)
	var before, after, lineTax int64
	if err := db.QueryRow(`SELECT total_before_tax, total_after_tax, tax_amount FROM sale_lines WHERE sale_id = ?`, saleID).
		Scan(&before, &after, &lineTax); err != nil {
		t.Fatalf("read line: %v", err)
	}
	if after != 120 {
		t.Fatalf("inclusive line total_after_tax = %d, want 120 (the ticket price)", after)
	}
	if before != 120-lineTax {
		t.Fatalf("inclusive line total_before_tax = %d, want %d", before, 120-lineTax)
	}
}

func TestCompleteSale_ExclusiveLineTotalsUnchanged(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 100, 1)`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)

	in := SaleInput{
		SaleType: "sale", Currency: "GBP", TaxInclusive: false,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 100, TaxRateBasisPoints: 2000, LocationID: "loc1"},
		},
		Payments: []PaymentInput{{MethodID: "cash", Amount: 120, Currency: "GBP"}},
	}
	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("CompleteSale: %v", err)
	}
	var before, after int64
	if err := db.QueryRow(`SELECT total_before_tax, total_after_tax FROM sale_lines WHERE sale_id = ?`, saleID).
		Scan(&before, &after); err != nil {
		t.Fatalf("read line: %v", err)
	}
	if before != 100 || after != 120 {
		t.Fatalf("exclusive line totals = %d/%d, want 100/120", before, after)
	}
}

func TestCompleteSale_RollsBackOnPaymentFailure(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	// create trigger to force payment insert failure
	_, _ = db.Exec(`CREATE TRIGGER payments_fail BEFORE INSERT ON payments WHEN NEW.reference = 'FAIL' BEGIN SELECT RAISE(ABORT, 'payment failure'); END;`)

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType:     "sale",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 500, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 500, Currency: "GBP", Reference: "FAIL"},
		},
	}

	if _, err := CompleteSale(ctx, db, in); err == nil {
		t.Fatalf("expected payment failure to bubble")
	}

	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&count)
	if count != 0 {
		t.Fatalf("expected no sales persisted, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM sale_lines`).Scan(&count)
	if count != 0 {
		t.Fatalf("expected no sale_lines, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM payments`).Scan(&count)
	if count != 0 {
		t.Fatalf("expected no payments, got %d", count)
	}
	_ = db.QueryRow(`SELECT COUNT(*) FROM stock_movements`).Scan(&count)
	if count != 0 {
		t.Fatalf("expected no stock_movements, got %d", count)
	}
}

func TestUpdateSaleStatus_Void(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 120, 1)`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)

	saleID, err := CompleteSale(ctx, db, SaleInput{
		SaleType:     "sale",
		Currency:     "GBP",
		TaxInclusive: true,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 120, TaxRateBasisPoints: 2000, LocationID: "loc1"},
		},
		Payments: []PaymentInput{{MethodID: "cash", Amount: 120}},
	})
	if err != nil {
		t.Fatalf("complete sale: %v", err)
	}

	if err := UpdateSaleStatus(ctx, db, saleID, "voided", "actor1", "test-void"); err != nil {
		t.Fatalf("update status: %v", err)
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM sales WHERE id=?`, saleID).Scan(&status)
	if status != "voided" {
		t.Fatalf("expected voided, got %s", status)
	}
}

func TestCompleteSale_AllowsChangeAcrossPayments(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',20,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('card','Card','card',1)`)

	in := SaleInput{
		SaleType: "sale",
		Currency: "GBP",
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 2, UnitPrice: 500, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 600},
			{MethodID: "card", Amount: 500, ChangeGiven: 100},
		},
	}

	saleID, err := CompleteSale(ctx, db, in)
	if err != nil {
		t.Fatalf("complete sale: %v", err)
	}
	var change int64
	if err := db.QueryRow(`SELECT change_given FROM payments WHERE sale_id=? AND method_id='card'`, saleID).Scan(&change); err != nil {
		t.Fatalf("read payment: %v", err)
	}
	if change != 100 {
		t.Fatalf("expected change 100, got %d", change)
	}
}

func TestCompleteSale_RejectsInvalidChange(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	in := SaleInput{
		SaleType: "sale",
		Currency: "GBP",
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 500, TaxRateBasisPoints: 0, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 500, ChangeGiven: 600},
		},
	}

	if _, err := CompleteSale(ctx, db, in); err == nil {
		t.Fatalf("expected change validation error")
	}
}

func TestRecordPaymentFailure_PersistsAuditLog(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	failureID, err := RecordPaymentFailure(ctx, db, PaymentFailure{
		Reason:   "gateway timeout",
		Payments: []PaymentInput{{MethodID: "card", Amount: 1000}},
		Lines:    []SaleLineInput{{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 1000}},
		Total:    1000,
		Currency: "GBP",
	})
	if err != nil {
		t.Fatalf("record failure: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_id = ? AND action = 'payment_failed'`, failureID).Scan(&count); err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 failure audit, got %d", count)
	}
}

func TestReceiptNoGenerator_Concurrency(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',100,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	const workers = 6
	var wg sync.WaitGroup
	receipts := make(chan string, workers)
	errs := make(chan error, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			saleID, err := CompleteSale(ctx, db, SaleInput{
				SaleType: "sale",
				Currency: "GBP",
				Lines:    []SaleLineInput{{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 500, TaxRateBasisPoints: 0, LocationID: "loc1"}},
				Payments: []PaymentInput{{MethodID: "cash", Amount: 500}},
			})
			if err != nil {
				errs <- err
				return
			}
			var receipt string
			if err := db.QueryRow(`SELECT receipt_no FROM sales WHERE id = ?`, saleID).Scan(&receipt); err != nil {
				errs <- err
				return
			}
			receipts <- receipt
		}()
	}
	wg.Wait()
	close(receipts)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent sale error: %v", err)
		}
	}
	var receiptVals []int
	for rcpt := range receipts {
		val, err := strconv.Atoi(rcpt)
		if err != nil {
			t.Fatalf("invalid receipt %s: %v", rcpt, err)
		}
		receiptVals = append(receiptVals, val)
	}
	if len(receiptVals) != workers {
		t.Fatalf("expected %d receipts, got %d", workers, len(receiptVals))
	}
	sort.Ints(receiptVals)
	for i := 1; i < len(receiptVals); i++ {
		if receiptVals[i] <= receiptVals[i-1] {
			t.Fatalf("receipts not strictly increasing: %v", receiptVals)
		}
	}
}

func TestCompleteSale_RetriesAfterReceiptConflict(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()

	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 500, 1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',10,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, rounding, created_at)
VALUES ('existing', '000000001', 'completed', 'sale', 'GBP', 500, 0, 0, 500, 0, ?)
`, now); err != nil {
		t.Fatalf("insert existing sale: %v", err)
	}

	origAllocator := receiptAllocator
	t.Cleanup(func() { receiptAllocator = origAllocator })
	var mu sync.Mutex
	allocations := map[*sql.Tx]string{}
	seq := []string{"000000001", "000000002"}
	receiptAllocator = func(ctx context.Context, tx *sql.Tx, repo *data.POSRepo) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if val, ok := allocations[tx]; ok {
			return val, nil
		}
		if len(seq) == 0 {
			return repo.NextReceiptNo(ctx, tx)
		}
		val := seq[0]
		seq = seq[1:]
		allocations[tx] = val
		return val, nil
	}

	if _, err := CompleteSale(ctx, db, SaleInput{
		SaleType: "sale",
		Currency: "GBP",
		Lines:    []SaleLineInput{{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 500, TaxRateBasisPoints: 0, LocationID: "loc1"}},
		Payments: []PaymentInput{{MethodID: "cash", Amount: 500}},
	}); err != nil {
		t.Fatalf("complete sale: %v", err)
	}

	rows, err := db.Query(`SELECT receipt_no FROM sales ORDER BY receipt_no`)
	if err != nil {
		t.Fatalf("query receipts: %v", err)
	}
	defer rows.Close()
	var receipts []string
	for rows.Next() {
		var rcpt string
		if err := rows.Scan(&rcpt); err != nil {
			t.Fatalf("scan receipt: %v", err)
		}
		receipts = append(receipts, rcpt)
	}
	if len(receipts) != 2 {
		t.Fatalf("expected 2 receipts, got %d", len(receipts))
	}
	if receipts[0] != "000000001" || receipts[1] != "000000002" {
		t.Fatalf("unexpected receipts: %v", receipts)
	}
}
