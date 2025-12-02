package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/db"
)

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(2)
}

func main() {
	path := "./data/unitill-pos.db"
	if len(os.Args) > 1 {
		path = os.Args[1]
	}

	fmt.Printf("Using DB: %s\n", path)
	d, err := db.Open(path)
	if err != nil {
		fatalf("open db: %v", err)
	}
	defer d.Close()

	// create sample data
	tx, err := d.Begin()
	if err != nil {
		fatalf("begin: %v", err)
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	// minimal rows: tax code, location, register, user, payment method, item, price, inventory
	exec := func(q string, args ...interface{}) {
		if _, err := tx.Exec(q, args...); err != nil {
			tx.Rollback()
			fatalf("exec failed: %v -- %s", err, q)
		}
	}

	// ensure a tax code exists and get its id (handle existing rows with same name)
	if _, err := tx.Exec(`INSERT OR IGNORE INTO tax_codes (id,name,rate_basis_points,is_active) VALUES (?,?,?,?)`, "tax-standard", "Standard VAT", 2000, 1); err != nil {
		tx.Rollback()
		fatalf("ensure tax code insert: %v", err)
	}
	var taxCodeID string
	row := tx.QueryRow(`SELECT id FROM tax_codes WHERE name = ? LIMIT 1`, "Standard VAT")
	if err := row.Scan(&taxCodeID); err != nil {
		tx.Rollback()
		fatalf("select tax code id: %v", err)
	}

	exec(`INSERT OR IGNORE INTO stock_locations (id,name) VALUES (?,?)`, "loc-main", "Main Location")
	exec(`INSERT OR IGNORE INTO registers (id,name,location_id,is_active) VALUES (?,?,?,?)`, "reg-1", "Front Till", "loc-main", 1)
	exec(`INSERT OR IGNORE INTO users (id,username,display_name,role,is_active) VALUES (?,?,?,?,?)`, "user-1", "cashier", "Cashier", "cashier", 1)
	exec(`INSERT OR IGNORE INTO payment_methods (id,name,type,is_active) VALUES (?,?,?,?)`, "cash", "Cash", "cash", 1)

	// Resolve actual IDs in case rows already existed with different IDs
	var (
		locationID      string
		registerID      string
		userID          string
		paymentMethodID string
	)
	row = tx.QueryRow(`SELECT id FROM stock_locations WHERE name = ? LIMIT 1`, "Main Location")
	if err := row.Scan(&locationID); err != nil {
		tx.Rollback()
		fatalf("lookup location id: %v", err)
	}
	row = tx.QueryRow(`SELECT id FROM registers WHERE name = ? LIMIT 1`, "Front Till")
	if err := row.Scan(&registerID); err != nil {
		tx.Rollback()
		fatalf("lookup register id: %v", err)
	}
	row = tx.QueryRow(`SELECT id FROM users WHERE username = ? LIMIT 1`, "cashier")
	if err := row.Scan(&userID); err != nil {
		tx.Rollback()
		fatalf("lookup user id: %v", err)
	}
	row = tx.QueryRow(`SELECT id FROM payment_methods WHERE name = ? LIMIT 1`, "Cash")
	if err := row.Scan(&paymentMethodID); err != nil {
		tx.Rollback()
		fatalf("lookup payment method id: %v", err)
	}

	itemID := uuid.New().String()
	exec(`INSERT OR IGNORE INTO items (id,sku,name,base_price,tax_code_id,is_active,is_weighed,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?)`, itemID, "PLU001", "Test Item", 1000, taxCodeID, 1, 0, now, now)
	// Resolve actual item ID in case an item with the SKU already existed
	row = tx.QueryRow(`SELECT id FROM items WHERE sku = ? LIMIT 1`, "PLU001")
	if err := row.Scan(&itemID); err != nil {
		tx.Rollback()
		fatalf("lookup item id: %v", err)
	}
	priceID := uuid.New().String()
	exec(`INSERT OR IGNORE INTO price_history (id,item_id,price,starts_at) VALUES (?,?,?,?)`, priceID, itemID, 1000, now)
	invID := uuid.New().String()
	exec(`INSERT OR IGNORE INTO inventory (id,item_id,location_id,quantity,updated_at) VALUES (?,?,?,?,?)`, invID, itemID, locationID, 10, now)

	if err := tx.Commit(); err != nil {
		fatalf("commit setup: %v", err)
	}

	// perform sale (transactional): create sale, sale_line, payment, stock_movement, and update inventory
	saleID := uuid.New().String()
	saleLineID := uuid.New().String()
	paymentID := uuid.New().String()
	stockMoveID := uuid.New().String()
	receiptNo := fmt.Sprintf("R-%d", time.Now().Unix())

	tx2, err := d.Begin()
	if err != nil {
		fatalf("begin sale tx: %v", err)
	}

	// sale totals: subtotal=1000, tax 200, total 1200
	if _, err := tx2.Exec(`INSERT INTO sales (id,receipt_no,status,sale_type,register_id,cashier_id,subtotal,discount_total,tax_total,total,rounding,created_at,completed_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, saleID, receiptNo, "completed", "sale", registerID, userID, 1000, 0, 200, 1200, 0, now, now); err != nil {
		tx2.Rollback()
		fatalf("insert sale: %v", err)
	}

	if _, err := tx2.Exec(`INSERT INTO sale_lines (id,sale_id,line_no,item_id,name_snapshot,sku_snapshot,quantity,unit_price,tax_rate_bp,tax_amount,total_before_tax,total_after_tax) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, saleLineID, saleID, 1, itemID, "Test Item", "PLU001", 1.0, 1000, 2000, 200, 1000, 1200); err != nil {
		tx2.Rollback()
		fatalf("insert sale_line: %v", err)
	}

	if _, err := tx2.Exec(`INSERT INTO payments (id,sale_id,method_id,amount,currency,reference,change_given,paid_at) VALUES (?,?,?,?,?,?,?,?)`, paymentID, saleID, paymentMethodID, 1200, "GBP", "", 0, now); err != nil {
		tx2.Rollback()
		fatalf("insert payment: %v", err)
	}

	if _, err := tx2.Exec(`INSERT INTO stock_movements (id,item_id,location_id,sale_line_id,type,quantity,cost_price,created_at) VALUES (?,?,?,?,?,?,?,?)`, stockMoveID, itemID, locationID, saleLineID, "sale", -1.0, nil, now); err != nil {
		tx2.Rollback()
		fatalf("insert stock_movement: %v", err)
	}

	// update inventory aggregate: decrement quantity
	if _, err := tx2.Exec(`UPDATE inventory SET quantity = quantity - 1, updated_at = ? WHERE item_id = ? AND location_id = ?`, time.Now().UTC().Format("2006-01-02 15:04:05"), itemID, locationID); err != nil {
		tx2.Rollback()
		fatalf("update inventory: %v", err)
	}

	if err := tx2.Commit(); err != nil {
		fatalf("commit sale tx: %v", err)
	}

	// validate
	validate(d.DB, saleID, saleLineID, paymentID, stockMoveID, itemID)

	fmt.Println("Smoke quickstart completed successfully")
}

func validate(sqlDB *sql.DB, saleID, saleLineID, paymentID, stockMoveID, itemID string) {
	var cnt int
	row := sqlDB.QueryRow(`SELECT COUNT(*) FROM sales WHERE id = ?`, saleID)
	if err := row.Scan(&cnt); err != nil {
		log.Fatalf("validate sales count: %v", err)
	}
	if cnt != 1 {
		log.Fatalf("expected sales count 1, got %d", cnt)
	}

	row = sqlDB.QueryRow(`SELECT COUNT(*) FROM sale_lines WHERE id = ?`, saleLineID)
	if err := row.Scan(&cnt); err != nil {
		log.Fatalf("validate sale_lines count: %v", err)
	}
	if cnt != 1 {
		log.Fatalf("expected sale_lines count 1, got %d", cnt)
	}

	row = sqlDB.QueryRow(`SELECT COUNT(*) FROM payments WHERE id = ?`, paymentID)
	if err := row.Scan(&cnt); err != nil {
		log.Fatalf("validate payments count: %v", err)
	}
	if cnt != 1 {
		log.Fatalf("expected payments count 1, got %d", cnt)
	}

	row = sqlDB.QueryRow(`SELECT COUNT(*) FROM stock_movements WHERE id = ?`, stockMoveID)
	if err := row.Scan(&cnt); err != nil {
		log.Fatalf("validate stock_movements count: %v", err)
	}
	if cnt != 1 {
		log.Fatalf("expected stock_movements count 1, got %d", cnt)
	}

	// check inventory quantity now 9
	var qty float64
	row = sqlDB.QueryRow(`SELECT quantity FROM inventory WHERE item_id = ? AND location_id = ?`, itemID, "loc-main")
	if err := row.Scan(&qty); err != nil {
		log.Fatalf("validate inventory qty: %v", err)
	}
	if qty != 9 {
		log.Fatalf("expected inventory qty 9 after sale, got %v", qty)
	}
}
