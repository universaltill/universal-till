package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pos"
)

// Smoke test for offline sale flow using actual POS service APIs
// This validates the complete end-to-end sale flow without network dependencies
//
// Usage:
//   go run ./scripts/smoke-offline-sale/main.go [db_path]
//   go run ./scripts/smoke-offline-sale/main.go ./data/test-smoke.db
//
// Exit codes:
//   0 - Success (sale completed within threshold)
//   1 - Setup/initialization failure
//   2 - Sale completion failure
//   3 - Performance threshold exceeded (warning, not blocking)

const (
	// Performance threshold for sale completion (from SC-001)
	saleCompletionThresholdMS = 5000

	// Default database path
	defaultDBPath = "./data/smoke-offline-sale.db"
)

func main() {
	dbPath := defaultDBPath
	if len(os.Args) > 1 {
		dbPath = os.Args[1]
	}

	ctx := context.Background()
	fmt.Printf("=== Offline Sale Smoke Test ===\n")
	fmt.Printf("Database: %s\n", dbPath)
	fmt.Printf("Performance threshold: %dms\n\n", saleCompletionThresholdMS)

	// Open and migrate database
	fmt.Println("Step 1/5: Opening database and running migrations...")
	database, err := db.Open(dbPath)
	if err != nil {
		fatal("Failed to open database: %v", err)
	}
	defer database.Close()

	// Seed test data
	fmt.Println("Step 2/5: Seeding test data...")
	if err := seedTestData(database.DB); err != nil {
		fatal("Failed to seed test data: %v", err)
	}

	// Create sale input
	fmt.Println("Step 3/5: Preparing sale input...")
	saleInput := pos.SaleInput{
		SaleType:     "sale",
		RegisterID:   "reg-smoke",
		CashierID:    "cashier-smoke",
		Currency:     "GBP",
		TaxInclusive: false,
		Lines: []pos.SaleLineInput{
			{
				ItemID:             "item-smoke-001",
				SKU:                "SMOKE-001",
				Name:               "Smoke Test Item",
				Qty:                2,
				UnitPrice:          500,  // £5.00
				TaxRateBasisPoints: 2000, // 20%
				LineDiscount:       0,
				LocationID:         "loc-smoke",
			},
		},
		Payments: []pos.PaymentInput{
			{
				MethodID: "cash",
				Amount:   1200, // £12.00 (covers £10.00 subtotal + £2.00 tax)
				Currency: "GBP",
			},
		},
		AllowNegativeInventory: false,
	}

	// Execute sale
	fmt.Println("Step 4/5: Completing sale transaction...")
	startTime := time.Now()

	saleID, err := pos.CompleteSale(ctx, database.DB, saleInput)

	duration := time.Since(startTime)
	durationMS := duration.Milliseconds()

	if err != nil {
		fatal("Sale completion failed: %v", err)
	}

	fmt.Printf("✓ Sale completed in %dms (ID: %s)\n", durationMS, saleID)

	// Validate results
	fmt.Println("Step 5/5: Validating sale data...")
	if err := validateSale(database.DB, saleID); err != nil {
		fatal("Sale validation failed: %v", err)
	}

	// Check performance threshold
	fmt.Println()
	if durationMS > saleCompletionThresholdMS {
		fmt.Printf("⚠️  WARNING: Performance threshold exceeded!\n")
		fmt.Printf("   Sale took %dms, threshold is %dms\n", durationMS, saleCompletionThresholdMS)
		fmt.Printf("   This may indicate performance degradation on constrained hardware.\n")
		os.Exit(3) // Warning exit code
	} else {
		fmt.Printf("✓ Performance OK: %dms <= %dms threshold\n", durationMS, saleCompletionThresholdMS)
	}

	fmt.Println("\n=== Smoke Test PASSED ===")
}

// seedTestData creates minimal test data required for a sale
func seedTestData(sqlDB *sql.DB) error {
	now := time.Now().UTC().Format(time.RFC3339)

	// Begin transaction for atomic setup
	tx, err := sqlDB.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Create location
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO stock_locations (id, name)
		VALUES (?, ?)
	`, "loc-smoke", "Smoke Test Location"); err != nil {
		return fmt.Errorf("insert location: %w", err)
	}

	// Create register
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO registers (id, name, location_id, is_active)
		VALUES (?, ?, ?, ?)
	`, "reg-smoke", "Smoke Test Register", "loc-smoke", 1); err != nil {
		return fmt.Errorf("insert register: %w", err)
	}

	// Create user/cashier
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO users (id, username, display_name, role, is_active)
		VALUES (?, ?, ?, ?, ?)
	`, "cashier-smoke", "smoke_cashier", "Smoke Cashier", "cashier", 1); err != nil {
		return fmt.Errorf("insert user: %w", err)
	}

	// Create payment method
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO payment_methods (id, name, type, is_active)
		VALUES (?, ?, ?, ?)
	`, "cash", "Cash", "cash", 1); err != nil {
		return fmt.Errorf("insert payment method: %w", err)
	}

	// Create item
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO items (id, sku, name, base_price, is_active)
		VALUES (?, ?, ?, ?, ?)
	`, "item-smoke-001", "SMOKE-001", "Smoke Test Item", 500, 1); err != nil {
		return fmt.Errorf("insert item: %w", err)
	}

	// Create inventory with sufficient quantity
	if _, err := tx.Exec(`
		INSERT OR IGNORE INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at)
		VALUES (?, ?, NULL, ?, ?, ?)
	`, "inv-smoke-001", "item-smoke-001", "loc-smoke", 1000, now); err != nil {
		return fmt.Errorf("insert inventory: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// validateSale checks that the sale was properly recorded
func validateSale(sqlDB *sql.DB, saleID string) error {
	// Check sale exists and is completed
	var status, receiptNo string
	var total int
	err := sqlDB.QueryRow(`
		SELECT status, receipt_no, total
		FROM sales
		WHERE id = ?
	`, saleID).Scan(&status, &receiptNo, &total)
	if err != nil {
		return fmt.Errorf("query sale: %w", err)
	}

	if status != "completed" {
		return fmt.Errorf("expected status 'completed', got '%s'", status)
	}

	if receiptNo == "" {
		return fmt.Errorf("receipt_no is empty")
	}

	if total != 1200 {
		return fmt.Errorf("expected total 1200, got %d", total)
	}

	fmt.Printf("  ✓ Sale status: %s\n", status)
	fmt.Printf("  ✓ Receipt: %s\n", receiptNo)
	fmt.Printf("  ✓ Total: £%.2f\n", float64(total)/100)

	// Check sale line exists
	var lineCount int
	err = sqlDB.QueryRow(`
		SELECT COUNT(*)
		FROM sale_lines
		WHERE sale_id = ?
	`, saleID).Scan(&lineCount)
	if err != nil {
		return fmt.Errorf("query sale lines: %w", err)
	}

	if lineCount != 1 {
		return fmt.Errorf("expected 1 sale line, got %d", lineCount)
	}

	fmt.Printf("  ✓ Sale lines: %d\n", lineCount)

	// Check payment exists
	var paymentCount int
	err = sqlDB.QueryRow(`
		SELECT COUNT(*)
		FROM payments
		WHERE sale_id = ?
	`, saleID).Scan(&paymentCount)
	if err != nil {
		return fmt.Errorf("query payments: %w", err)
	}

	if paymentCount != 1 {
		return fmt.Errorf("expected 1 payment, got %d", paymentCount)
	}

	fmt.Printf("  ✓ Payments: %d\n", paymentCount)

	// Check stock movement was created
	var movementCount int
	err = sqlDB.QueryRow(`
		SELECT COUNT(*)
		FROM stock_movements
		WHERE type = 'sale' AND item_id = ?
	`, "item-smoke-001").Scan(&movementCount)
	if err != nil {
		return fmt.Errorf("query stock movements: %w", err)
	}

	if movementCount < 1 {
		return fmt.Errorf("expected at least 1 stock movement, got %d", movementCount)
	}

	fmt.Printf("  ✓ Stock movements: %d\n", movementCount)

	// Check inventory was updated
	var newQty float64
	err = sqlDB.QueryRow(`
		SELECT quantity
		FROM inventory
		WHERE item_id = ? AND location_id = ?
	`, "item-smoke-001", "loc-smoke").Scan(&newQty)
	if err != nil {
		return fmt.Errorf("query inventory: %w", err)
	}

	if newQty != 998 { // Started with 1000, sold 2
		return fmt.Errorf("expected inventory 998, got %v", newQty)
	}

	fmt.Printf("  ✓ Inventory updated: %.0f remaining\n", newQty)

	return nil
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "\n❌ ERROR: "+format+"\n", args...)
	os.Exit(2)
}
