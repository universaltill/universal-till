package pos

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	_ "modernc.org/sqlite"
)

const (
	// Sale completion thresholds (warn/fail) on target hardware (e.g., Raspberry Pi 4/8GB or equivalent mini PC).
	defaultSaleWarnThresholdMS = 4000
	defaultSaleFailThresholdMS = 5000 // also used as legacy UT_BENCHMARK_THRESHOLD_MS fallback

	// Micro-interaction thresholds (warn/fail) for local actions like lookup/cart add.
	defaultMicroWarnThresholdMS = 150
	defaultMicroFailThresholdMS = 200
)

// getBenchmarkThreshold returns the configured sale fail threshold in milliseconds.
// Deprecated: prefer saleThresholds().
func getBenchmarkThreshold() int {
	_, fail := saleThresholds()
	return fail
}

func parsePositive(key string, def int) int {
	if val := os.Getenv(key); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			return parsed
		}
	}
	return def
}

func saleThresholds() (warn, fail int) {
	warn = parsePositive("UT_BENCHMARK_SALE_WARN_MS", defaultSaleWarnThresholdMS)
	fail = parsePositive("UT_BENCHMARK_SALE_FAIL_MS", defaultSaleFailThresholdMS)

	// Legacy fail override for backward compatibility.
	if val := os.Getenv("UT_BENCHMARK_THRESHOLD_MS"); val != "" {
		if parsed, err := strconv.Atoi(val); err == nil && parsed > 0 {
			fail = parsed
		}
	}
	if warn > fail {
		warn = fail
	}
	return
}

func microInteractionThresholds() (warn, fail int) {
	warn = parsePositive("UT_BENCHMARK_INTERACT_WARN_MS", defaultMicroWarnThresholdMS)
	fail = parsePositive("UT_BENCHMARK_INTERACT_FAIL_MS", defaultMicroFailThresholdMS)
	if warn > fail {
		warn = fail
	}
	return
}

// setupBenchmarkDB creates a temporary SQLite database for benchmarking.
func setupBenchmarkDB(tb testing.TB) *sql.DB {
	tb.Helper()
	path := filepath.Join(tb.TempDir(), fmt.Sprintf("bench_%d.db", time.Now().UnixNano()))
	db, err := sql.Open("sqlite", path)
	if err != nil {
		tb.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		tb.Fatalf("set busy_timeout: %v", err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		tb.Fatalf("enable fks: %v", err)
	}

	// Create schema
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE stock_locations (id TEXT PRIMARY KEY, name TEXT);`,
		`CREATE TABLE items (id TEXT PRIMARY KEY, sku TEXT, name TEXT, description TEXT, category_id TEXT, brand_id TEXT, unit TEXT, base_price INTEGER NOT NULL, tax_code_id TEXT, is_active INTEGER NOT NULL DEFAULT 1, is_weighed INTEGER NOT NULL DEFAULT 0);`,
		`CREATE TABLE item_barcodes (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, barcode TEXT NOT NULL);`,
		`CREATE TABLE item_variants (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, price INTEGER NOT NULL, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE sales (id TEXT PRIMARY KEY, receipt_no TEXT NOT NULL UNIQUE, status TEXT NOT NULL, sale_type TEXT NOT NULL, tender_type TEXT NOT NULL DEFAULT 'unknown', order_type TEXT NOT NULL DEFAULT '', table_id TEXT, offline INTEGER NOT NULL DEFAULT 0, sync_status TEXT NOT NULL DEFAULT 'queued', sync_attempts INTEGER NOT NULL DEFAULT 0, sync_next_attempt_at TEXT, sync_last_error TEXT, register_id TEXT, cashier_id TEXT, customer_id TEXT, currency TEXT NOT NULL, subtotal INTEGER NOT NULL, discount_total INTEGER NOT NULL, tax_total INTEGER NOT NULL, total INTEGER NOT NULL, service_charge_amount INTEGER NOT NULL DEFAULT 0, service_charge_tax_basis_bp INTEGER NOT NULL DEFAULT 0, voucher_issue_total INTEGER NOT NULL DEFAULT 0, rounding INTEGER NOT NULL DEFAULT 0, note TEXT, created_at TEXT NOT NULL, completed_at TEXT, voided_at TEXT);`,
		`CREATE TABLE sale_lines (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, line_no INTEGER NOT NULL, item_id TEXT, variant_id TEXT, name_snapshot TEXT NOT NULL, sku_snapshot TEXT, barcode_snapshot TEXT, quantity REAL NOT NULL, unit_price INTEGER NOT NULL, line_discount INTEGER NOT NULL DEFAULT 0, tax_rate_bp INTEGER NOT NULL, tax_amount INTEGER NOT NULL, total_before_tax INTEGER NOT NULL, total_after_tax INTEGER NOT NULL, FOREIGN KEY (sale_id) REFERENCES sales(id));`,
		`CREATE TABLE sale_discounts (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, line_id TEXT, type TEXT NOT NULL, value INTEGER NOT NULL, amount INTEGER NOT NULL, reason TEXT);`,
		`CREATE TABLE payments (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, method_id TEXT NOT NULL, amount INTEGER NOT NULL, currency TEXT NOT NULL, reference TEXT, change_given INTEGER NOT NULL DEFAULT 0, tip_amount INTEGER NOT NULL DEFAULT 0, tip_recipient TEXT NOT NULL DEFAULT 'employee', masked_pan TEXT, auth_code TEXT, terminal_id TEXT, trace_id TEXT, paid_at TEXT NOT NULL, FOREIGN KEY (sale_id) REFERENCES sales(id));`,
		`CREATE TABLE sale_links (id TEXT PRIMARY KEY, sale_id TEXT NOT NULL, original_sale_id TEXT NOT NULL, reason TEXT);`,
		`CREATE TABLE stock_movements (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, sale_line_id TEXT, type TEXT NOT NULL, quantity REAL NOT NULL, created_at TEXT NOT NULL);`,
		`CREATE TABLE inventory (id TEXT PRIMARY KEY, item_id TEXT, variant_id TEXT, location_id TEXT NOT NULL, quantity REAL NOT NULL, updated_at TEXT NOT NULL, UNIQUE(item_id, variant_id, location_id));`,
		`CREATE TABLE payment_methods (id TEXT PRIMARY KEY, name TEXT, type TEXT, is_active INTEGER DEFAULT 1);`,
		`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL, data_json TEXT, created_at TEXT NOT NULL, blocked_actor_id TEXT);`,
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, author TEXT, is_active INTEGER DEFAULT 1);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			tb.Fatalf("setup stmt failed: %v", err)
		}
	}

	// Seed test data
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main Store')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, description, category_id, brand_id, unit, base_price, tax_code_id, is_active, is_weighed) VALUES('itm1','SKU001','Test Item','Benchmark item',NULL,NULL,'unit',500,NULL,1,0)`)
	_, _ = db.Exec(`INSERT INTO item_barcodes(id, item_id, barcode) VALUES('barcode1','itm1','SKU001')`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',10000,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)

	return db
}

// BenchmarkCompleteSale benchmarks the end-to-end sale completion flow
// This validates the SC-001 requirement: sale completion <5s on target hardware
//
// Run with: go test -bench=BenchmarkCompleteSale -benchtime=10x ./internal/pos
// Set custom threshold: UT_BENCHMARK_THRESHOLD_MS=3000 go test -bench=BenchmarkCompleteSale ./internal/pos
func BenchmarkCompleteSale(b *testing.B) {
	ctx := context.Background()
	db := setupBenchmarkDB(b)
	defer db.Close()

	// Prepare sale input
	saleInput := SaleInput{
		SaleType:     "sale",
		RegisterID:   "reg1",
		CashierID:    "cashier1",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{
			{
				ItemID:             "itm1",
				SKU:                "SKU001",
				Name:               "Test Item",
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

	threshold := getBenchmarkThreshold()

	b.ResetTimer()
	var totalDuration time.Duration

	for i := 0; i < b.N; i++ {
		start := time.Now()
		_, err := CompleteSale(ctx, db, saleInput)
		duration := time.Since(start)
		totalDuration += duration

		if err != nil {
			b.Fatalf("CompleteSale failed: %v", err)
		}

		// Log individual iteration if it exceeds threshold
		if duration.Milliseconds() > int64(threshold) {
			b.Logf("WARNING: Iteration %d took %v (threshold: %dms)", i+1, duration, threshold)
		}
	}

	b.StopTimer()

	// Calculate average duration
	avgDuration := totalDuration / time.Duration(b.N)
	avgMS := avgDuration.Milliseconds()

	b.ReportMetric(float64(avgMS), "ms/op")

	// Report threshold check
	if avgMS > int64(threshold) {
		b.Logf("⚠️  THRESHOLD EXCEEDED: Average %dms > %dms threshold", avgMS, threshold)
		b.Logf("Performance target: Sale completion should be <%dms on target hardware", threshold)
	} else {
		b.Logf("✓ PASSED: Average %dms <= %dms threshold", avgMS, threshold)
	}
}

// BenchmarkCompleteSaleMultiLine benchmarks sale with multiple line items
// This validates performance with more complex transactions
func BenchmarkCompleteSaleMultiLine(b *testing.B) {
	ctx := context.Background()
	db := setupBenchmarkDB(b)
	defer db.Close()

	// Add more items
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, description, category_id, brand_id, unit, base_price, tax_code_id, is_active, is_weighed) VALUES('itm2','SKU002','Item 2','Item 2 desc',NULL,NULL,'unit',1000,NULL,1,0)`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, description, category_id, brand_id, unit, base_price, tax_code_id, is_active, is_weighed) VALUES('itm3','SKU003','Item 3','Item 3 desc',NULL,NULL,'unit',1500,NULL,1,0)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv2','itm2',NULL,'loc1',10000,datetime('now'))`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv3','itm3',NULL,'loc1',10000,datetime('now'))`)

	// Total calculation: (2*500) + (1*1000) + (3*1500) = 1000 + 1000 + 4500 = 6500
	// Tax at 20%: 6500 * 0.20 = 1300
	// Total with tax: 6500 + 1300 = 7800
	saleInput := SaleInput{
		SaleType:     "sale",
		RegisterID:   "reg1",
		CashierID:    "cashier1",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{
			{ItemID: "itm1", SKU: "SKU001", Name: "Item 1", Qty: 2, UnitPrice: 500, TaxRateBasisPoints: 2000, LocationID: "loc1"},
			{ItemID: "itm2", SKU: "SKU002", Name: "Item 2", Qty: 1, UnitPrice: 1000, TaxRateBasisPoints: 2000, LocationID: "loc1"},
			{ItemID: "itm3", SKU: "SKU003", Name: "Item 3", Qty: 3, UnitPrice: 1500, TaxRateBasisPoints: 2000, LocationID: "loc1"},
		},
		Payments: []PaymentInput{
			{MethodID: "cash", Amount: 7800, Currency: "GBP"},
		},
		AllowNegativeInventory: false,
	}

	threshold := getBenchmarkThreshold()

	b.ResetTimer()
	var totalDuration time.Duration

	for i := 0; i < b.N; i++ {
		start := time.Now()
		_, err := CompleteSale(ctx, db, saleInput)
		duration := time.Since(start)
		totalDuration += duration

		if err != nil {
			b.Fatalf("CompleteSale failed: %v", err)
		}
	}

	b.StopTimer()

	avgDuration := totalDuration / time.Duration(b.N)
	avgMS := avgDuration.Milliseconds()

	b.ReportMetric(float64(avgMS), "ms/op")

	if avgMS > int64(threshold) {
		b.Logf("⚠️  THRESHOLD EXCEEDED: Average %dms > %dms threshold (multi-line)", avgMS, threshold)
	} else {
		b.Logf("✓ PASSED: Average %dms <= %dms threshold (multi-line)", avgMS, threshold)
	}
}

// TestBenchmarkThresholdConfiguration tests that threshold can be configured
func TestBenchmarkThresholdConfiguration(t *testing.T) {
	// Test default
	os.Unsetenv("UT_BENCHMARK_THRESHOLD_MS")
	os.Unsetenv("UT_BENCHMARK_SALE_FAIL_MS")
	if threshold := getBenchmarkThreshold(); threshold != defaultSaleFailThresholdMS {
		t.Errorf("expected default threshold %d, got %d", defaultSaleFailThresholdMS, threshold)
	}

	// Test custom value
	os.Setenv("UT_BENCHMARK_THRESHOLD_MS", "3000")
	defer os.Unsetenv("UT_BENCHMARK_THRESHOLD_MS")
	if threshold := getBenchmarkThreshold(); threshold != 3000 {
		t.Errorf("expected custom threshold 3000, got %d", threshold)
	}

	// Test invalid value falls back to default
	os.Setenv("UT_BENCHMARK_THRESHOLD_MS", "invalid")
	if threshold := getBenchmarkThreshold(); threshold != defaultSaleFailThresholdMS {
		t.Errorf("expected default threshold on invalid input, got %d", threshold)
	}
}

// TestSalePerformanceThresholds runs a short sale flow and enforces warn/fail thresholds for CI.
func TestSalePerformanceThresholds(t *testing.T) {
	ctx := context.Background()
	db := setupBenchmarkDB(t)
	defer db.Close()

	saleInput := SaleInput{
		SaleType:     "sale",
		RegisterID:   "reg1",
		CashierID:    "cashier1",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []SaleLineInput{
			{
				ItemID:             "itm1",
				SKU:                "SKU001",
				Name:               "Test Item",
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

	warn, fail := saleThresholds()
	const iterations = 3
	var totalDuration time.Duration

	for i := 0; i < iterations; i++ {
		start := time.Now()
		if _, err := CompleteSale(ctx, db, saleInput); err != nil {
			t.Fatalf("CompleteSale failed: %v", err)
		}
		totalDuration += time.Since(start)
	}

	avg := totalDuration / iterations
	avgMS := avg.Milliseconds()

	if avgMS > int64(fail) {
		t.Fatalf("sale performance: average %dms exceeds fail threshold %dms (warn %dms)", avgMS, fail, warn)
	}
	if avgMS > int64(warn) {
		t.Logf("⚠️ sale performance warning: average %dms exceeds warn threshold %dms (fail %dms)", avgMS, warn, fail)
	}
}

// TestMicroInteractionLatency validates local interaction latency (e.g., item lookup/cart add).
func TestMicroInteractionLatency(t *testing.T) {
	ctx := context.Background()
	db := setupBenchmarkDB(t)
	defer db.Close()

	warn, fail := microInteractionThresholds()
	searcher := NewCatalogSearcher(data.NewPOSRepo(db))
	resolver := &staticResolver{
		lines: map[string]BasketLine{
			"SKU001": {
				SKU:        "SKU001",
				Name:       "Test Item",
				PriceCents: 500,
				Qty:        1,
				ItemID:     "itm1",
				TaxRateBP:  2000,
			},
		},
	}
	svc := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, resolver)

	const iterations = 25
	var totalDuration time.Duration

	for i := 0; i < iterations; i++ {
		start := time.Now()
		results, err := searcher.SearchActiveItems(ctx, "SKU001", 0, 5)
		if err != nil {
			t.Fatalf("lookup failed: %v", err)
		}
		if len(results) == 0 {
			t.Fatalf("lookup returned no results")
		}
		if _, err := svc.Scan("SKU001"); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		totalDuration += time.Since(start)
		svc.Reset()
	}

	avg := totalDuration / iterations
	avgMS := avg.Milliseconds()

	if avgMS > int64(fail) {
		t.Fatalf("micro interaction performance: average %dms exceeds fail threshold %dms (warn %dms)", avgMS, fail, warn)
	}
	if avgMS > int64(warn) {
		t.Logf("⚠️ micro interaction performance warning: average %dms exceeds warn threshold %dms (fail %dms)", avgMS, warn, fail)
	}
}

// TestScanTotalsLatency validates scan+totals update latency on low-end baseline.
func TestScanTotalsLatency(t *testing.T) {
	warn, fail := microInteractionThresholds()
	resolver := &staticResolver{
		lines: map[string]BasketLine{
			"SKU001": {
				SKU:        "SKU001",
				Name:       "Test Item",
				PriceCents: 500,
				Qty:        1,
				ItemID:     "itm1",
				TaxRateBP:  2000,
			},
		},
	}
	svc := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, resolver)

	const iterations = 50
	var totalDuration time.Duration

	for i := 0; i < iterations; i++ {
		start := time.Now()
		if _, err := svc.Scan("SKU001"); err != nil {
			t.Fatalf("scan failed: %v", err)
		}
		_ = svc.Basket()
		totalDuration += time.Since(start)
		svc.Reset()
	}

	avg := totalDuration / iterations
	avgMS := avg.Milliseconds()

	if testing.Short() {
		t.Skip("skipping scan perf guard in -short")
	}
	if avgMS > int64(fail) {
		t.Fatalf("scan+totals performance: average %dms exceeds fail threshold %dms (warn %dms)", avgMS, fail, warn)
	}
	if avgMS > int64(warn) {
		t.Logf("⚠️ scan+totals performance warning: average %dms exceeds warn threshold %dms (fail %dms)", avgMS, warn, fail)
	}
}

type staticResolver struct {
	lines map[string]BasketLine
}

func (s *staticResolver) Resolve(code string) (BasketLine, bool) {
	line, ok := s.lines[code]
	return line, ok
}
