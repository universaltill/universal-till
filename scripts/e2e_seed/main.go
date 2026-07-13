package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/db"
)

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func main() {
	path := os.Getenv("UT_DB_PATH")
	if path == "" {
		if len(os.Args) > 1 {
			path = os.Args[1]
		} else {
			path = "./data/unitill-pos.db"
		}
	}

	fmt.Printf("Seeding E2E DB: %s\n", path)
	// data/ is gitignored — a fresh checkout (CI) has no parent directory and
	// SQLite reports the missing dir as the misleading "out of memory (14)".
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fatalf("create db dir: %v", err)
		}
	}
	conn, err := db.Open(path)
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer conn.Close()

	// Minimal seed data for UI flows: tax code, location, register, user, payment, item, price, inventory.
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	tx, err := conn.Begin()
	if err != nil {
		fatalf("begin: %v", err)
	}

	exec := func(q string, args ...interface{}) {
		if _, err := tx.Exec(q, args...); err != nil {
			_ = tx.Rollback()
			fatalf("exec failed: %v -- %s", err, q)
		}
	}

	if _, err := tx.Exec(`INSERT OR IGNORE INTO tax_codes (id,name,rate_basis_points,is_active) VALUES (?,?,?,?)`, "tax-standard", "Standard VAT", 2000, 1); err != nil {
		_ = tx.Rollback()
		fatalf("ensure tax code insert: %v", err)
	}

	exec(`INSERT OR IGNORE INTO stock_locations (id,name) VALUES (?,?)`, "loc-main", "Main Location")
	exec(`INSERT OR IGNORE INTO registers (id,name,location_id,is_active) VALUES (?,?,?,?)`, "reg-1", "Front Till", "loc-main", 1)
	exec(`INSERT OR IGNORE INTO users (id,username,display_name,role,is_active) VALUES (?,?,?,?,?)`, "user-1", "cashier", "Cashier", "cashier", 1)
	exec(`INSERT OR IGNORE INTO payment_methods (id,name,type,is_active) VALUES (?,?,?,?)`, "cash", "Cash", "cash", 1)

	var taxCodeID string
	row := tx.QueryRow(`SELECT id FROM tax_codes WHERE name = ? LIMIT 1`, "Standard VAT")
	if err := row.Scan(&taxCodeID); err != nil {
		_ = tx.Rollback()
		fatalf("select tax code id: %v", err)
	}

	itemID := uuid.New().String()
	exec(`INSERT OR IGNORE INTO items (id,sku,name,base_price,tax_code_id,is_active,is_weighed,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`, itemID, "PLU001", "Test Item", 1000, taxCodeID, 1, 0, now, now)
	row = tx.QueryRow(`SELECT id FROM items WHERE sku = ? LIMIT 1`, "PLU001")
	if err := row.Scan(&itemID); err != nil {
		_ = tx.Rollback()
		fatalf("lookup item id: %v", err)
	}

	priceID := uuid.New().String()
	exec(`INSERT OR IGNORE INTO price_history (id,item_id,price,starts_at) VALUES (?,?,?,?)`, priceID, itemID, 1000, now)
	invID := uuid.New().String()
	exec(`INSERT OR IGNORE INTO inventory (id,item_id,location_id,quantity,updated_at) VALUES (?,?,?,?,?)`, invID, itemID, "loc-main", 10, now)

	if err := tx.Commit(); err != nil {
		fatalf("commit setup: %v", err)
	}

	fmt.Println("E2E seed completed successfully")
}
