package pos

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestAggregateInventory_ItemID(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	defer db.Close()

	// Insert item and inventory record
	execSQL(t, db, `INSERT INTO items (id, name, category, created_at) VALUES ('item1', 'Widget', 'goods', datetime('now'))`)
	execSQL(t, db, `INSERT INTO locations (id, name, type, created_at) VALUES ('loc1', 'Main Store', 'store', datetime('now'))`)
	execSQL(t, db, `INSERT INTO inventory (id, item_id, location_id, quantity, updated_at) VALUES ('inv1', 'item1', 'loc1', 100, datetime('now'))`)

	qty, err := AggregateInventory(ctx, db, "loc1", "item1", "")
	if err != nil {
		t.Fatalf("AggregateInventory failed: %v", err)
	}
	if qty != 100 {
		t.Errorf("expected 100, got %.2f", qty)
	}
}

func TestAggregateInventory_NoRecord(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	defer db.Close()

	execSQL(t, db, `INSERT INTO locations (id, name, type, created_at) VALUES ('loc1', 'Main Store', 'store', datetime('now'))`)

	qty, err := AggregateInventory(ctx, db, "loc1", "nonexistent", "")
	if err != nil {
		t.Fatalf("AggregateInventory failed: %v", err)
	}
	if qty != 0 {
		t.Errorf("expected 0 for missing item, got %.2f", qty)
	}
}

func TestAggregateInventory_BothItemAndVariantError(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	defer db.Close()

	_, err := AggregateInventory(ctx, db, "loc1", "item1", "variant1")
	if err == nil {
		t.Fatal("expected error for both itemID and variantID")
	}
	want := "cannot specify both itemID and variantID"
	if err.Error() != want {
		t.Errorf("expected error %q, got %q", want, err.Error())
	}
}

func TestRecordStockMovement_Receive(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	defer db.Close()

	execSQL(t, db, `INSERT INTO items (id, name, category, created_at) VALUES ('item1', 'Widget', 'goods', datetime('now'))`)
	execSQL(t, db, `INSERT INTO locations (id, name, type, created_at) VALUES ('loc1', 'Main Store', 'store', datetime('now'))`)
	execSQL(t, db, `INSERT INTO users (id, username, pin_hash, role, created_at) VALUES ('user1', 'manager', '', 'manager', datetime('now'))`)

	movementID, err := RecordStockMovement(ctx, db, StockMovementInput{
		ItemID:     "item1",
		LocationID: "loc1",
		Type:       "receive",
		Quantity:   50,
		CostPrice:  100,
		Reason:     "initial stock",
		ActorID:    "user1",
	})
	if err != nil {
		t.Fatalf("RecordStockMovement failed: %v", err)
	}
	if movementID == "" {
		t.Fatal("expected movementID")
	}

	// Verify movement record
	var qty float64
	err = db.QueryRowContext(ctx, `SELECT quantity FROM stock_movements WHERE id = ?`, movementID).Scan(&qty)
	if err != nil {
		t.Fatalf("query stock_movements: %v", err)
	}
	if qty != 50 {
		t.Errorf("expected qty 50, got %.2f", qty)
	}

	// Verify inventory aggregate
	invQty, err := AggregateInventory(ctx, db, "loc1", "item1", "")
	if err != nil {
		t.Fatalf("AggregateInventory failed: %v", err)
	}
	if invQty != 50 {
		t.Errorf("expected inventory 50, got %.2f", invQty)
	}

	// Verify audit log
	var auditCount int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM audit_log WHERE entity_id = ? AND action = 'receive'`, movementID).Scan(&auditCount)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("expected 1 audit log entry, got %d", auditCount)
	}
}

func TestRecordStockMovement_Adjust(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	defer db.Close()

	execSQL(t, db, `INSERT INTO items (id, name, category, created_at) VALUES ('item1', 'Widget', 'goods', datetime('now'))`)
	execSQL(t, db, `INSERT INTO locations (id, name, type, created_at) VALUES ('loc1', 'Main Store', 'store', datetime('now'))`)
	execSQL(t, db, `INSERT INTO inventory (id, item_id, location_id, quantity, updated_at) VALUES ('inv1', 'item1', 'loc1', 100, datetime('now'))`)
	execSQL(t, db, `INSERT INTO users (id, username, pin_hash, role, created_at) VALUES ('user1', 'manager', '', 'manager', datetime('now'))`)

	// Adjust down by 10
	_, err := RecordStockMovement(ctx, db, StockMovementInput{
		ItemID:     "item1",
		LocationID: "loc1",
		Type:       "adjust",
		Quantity:   -10,
		Reason:     "damage",
		ActorID:    "user1",
	})
	if err != nil {
		t.Fatalf("RecordStockMovement failed: %v", err)
	}

	invQty, err := AggregateInventory(ctx, db, "loc1", "item1", "")
	if err != nil {
		t.Fatalf("AggregateInventory failed: %v", err)
	}
	if invQty != 90 {
		t.Errorf("expected inventory 90, got %.2f", invQty)
	}
}

func TestCheckNegativeInventory_Sufficient(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	defer db.Close()

	execSQL(t, db, `INSERT INTO items (id, name, category, created_at) VALUES ('item1', 'Widget', 'goods', datetime('now'))`)
	execSQL(t, db, `INSERT INTO locations (id, name, type, created_at) VALUES ('loc1', 'Main Store', 'store', datetime('now'))`)
	execSQL(t, db, `INSERT INTO inventory (id, item_id, location_id, quantity, updated_at) VALUES ('inv1', 'item1', 'loc1', 100, datetime('now'))`)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	err = CheckNegativeInventory(ctx, tx, "loc1", "item1", "", 50)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestCheckNegativeInventory_Insufficient(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	defer db.Close()

	execSQL(t, db, `INSERT INTO items (id, name, category, created_at) VALUES ('item1', 'Widget', 'goods', datetime('now'))`)
	execSQL(t, db, `INSERT INTO locations (id, name, type, created_at) VALUES ('loc1', 'Main Store', 'store', datetime('now'))`)
	execSQL(t, db, `INSERT INTO inventory (id, item_id, location_id, quantity, updated_at) VALUES ('inv1', 'item1', 'loc1', 10, datetime('now'))`)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	err = CheckNegativeInventory(ctx, tx, "loc1", "item1", "", 50)
	if err == nil {
		t.Fatal("expected error for insufficient stock")
	}
}

func TestRecordNegativeInventoryOverride(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	defer db.Close()

	execSQL(t, db, `INSERT INTO users (id, username, pin_hash, role, created_at) VALUES ('mgr1', 'manager', '', 'manager', datetime('now'))`)

	overrideID, err := RecordNegativeInventoryOverride(ctx, db, OverrideNegativeInventory{
		ActorID:    "mgr1",
		Reason:     "customer emergency order",
		ItemID:     "item1",
		LocationID: "loc1",
		QtyBefore:  5,
	})
	if err != nil {
		t.Fatalf("RecordNegativeInventoryOverride failed: %v", err)
	}
	if overrideID == "" {
		t.Fatal("expected overrideID")
	}

	var action string
	err = db.QueryRowContext(ctx, `SELECT action FROM audit_log WHERE id = ?`, overrideID).Scan(&action)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if action != "negative_inventory_override" {
		t.Errorf("expected action 'negative_inventory_override', got %q", action)
	}
}

func TestRecordNegativeInventoryOverride_MissingReason(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	defer db.Close()

	execSQL(t, db, `INSERT INTO users (id, username, pin_hash, role, created_at) VALUES ('mgr1', 'manager', '', 'manager', datetime('now'))`)

	_, err := RecordNegativeInventoryOverride(ctx, db, OverrideNegativeInventory{
		ActorID:    "mgr1",
		Reason:     "", // Missing reason
		ItemID:     "item1",
		LocationID: "loc1",
		QtyBefore:  5,
	})
	if err == nil {
		t.Fatal("expected error for missing reason")
	}
	want := "reason required"
	if err.Error() != want {
		t.Errorf("expected error %q, got %q", want, err.Error())
	}
}

func TestRecordNegativeInventoryOverride_MissingActorID(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	defer db.Close()

	_, err := RecordNegativeInventoryOverride(ctx, db, OverrideNegativeInventory{
		ActorID:    "", // Missing actor
		Reason:     "test",
		ItemID:     "item1",
		LocationID: "loc1",
		QtyBefore:  5,
	})
	if err == nil {
		t.Fatal("expected error for missing actorID")
	}
	want := "actorID required"
	if err.Error() != want {
		t.Errorf("expected error %q, got %q", want, err.Error())
	}
}

func TestCheckNegativeInventory_ZeroRequest(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	defer db.Close()

	execSQL(t, db, `INSERT INTO items (id, name, category, created_at) VALUES ('item1', 'Widget', 'goods', datetime('now'))`)
	execSQL(t, db, `INSERT INTO locations (id, name, type, created_at) VALUES ('loc1', 'Main Store', 'store', datetime('now'))`)
	execSQL(t, db, `INSERT INTO inventory (id, item_id, location_id, quantity, updated_at) VALUES ('inv1', 'item1', 'loc1', 10, datetime('now'))`)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// Zero quantity should not error
	err = CheckNegativeInventory(ctx, tx, "loc1", "item1", "", 0)
	if err != nil {
		t.Errorf("expected no error for zero request, got %v", err)
	}
}

func TestCheckNegativeInventory_NegativeRequest(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	defer db.Close()

	execSQL(t, db, `INSERT INTO items (id, name, category, created_at) VALUES ('item1', 'Widget', 'goods', datetime('now'))`)
	execSQL(t, db, `INSERT INTO locations (id, name, type, created_at) VALUES ('loc1', 'Main Store', 'store', datetime('now'))`)
	execSQL(t, db, `INSERT INTO inventory (id, item_id, location_id, quantity, updated_at) VALUES ('inv1', 'item1', 'loc1', 10, datetime('now'))`)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// Negative quantity should not error (no validation needed)
	err = CheckNegativeInventory(ctx, tx, "loc1", "item1", "", -5)
	if err != nil {
		t.Errorf("expected no error for negative request, got %v", err)
	}
}

func TestRecordStockMovement_ZeroQuantityError(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	defer db.Close()

	execSQL(t, db, `INSERT INTO items (id, name, category, created_at) VALUES ('item1', 'Widget', 'goods', datetime('now'))`)
	execSQL(t, db, `INSERT INTO locations (id, name, type, created_at) VALUES ('loc1', 'Main Store', 'store', datetime('now'))`)
	execSQL(t, db, `INSERT INTO users (id, username, pin_hash, role, created_at) VALUES ('user1', 'manager', '', 'manager', datetime('now'))`)

	_, err := RecordStockMovement(ctx, db, StockMovementInput{
		ItemID:     "item1",
		LocationID: "loc1",
		Type:       "receive",
		Quantity:   0, // Zero quantity
		ActorID:    "user1",
	})
	if err == nil {
		t.Fatal("expected error for zero quantity")
	}
	want := "quantity must be non-zero"
	if err.Error() != want {
		t.Errorf("expected error %q, got %q", want, err.Error())
	}
}

func TestGetLowStockItems(t *testing.T) {
	ctx := context.Background()
	db := testDB(t)
	defer db.Close()

	execSQL(t, db, `INSERT INTO items (id, name, category, reorder_level, created_at) VALUES ('item1', 'Low Stock Item', 'goods', 50, datetime('now'))`)
	execSQL(t, db, `INSERT INTO items (id, name, category, reorder_level, created_at) VALUES ('item2', 'OK Stock Item', 'goods', 10, datetime('now'))`)
	execSQL(t, db, `INSERT INTO items (id, name, category, reorder_level, created_at) VALUES ('item3', 'No Reorder', 'goods', 0, datetime('now'))`)
	execSQL(t, db, `INSERT INTO locations (id, name, type, created_at) VALUES ('loc1', 'Main Store', 'store', datetime('now'))`)
	execSQL(t, db, `INSERT INTO inventory (id, item_id, location_id, quantity, updated_at) VALUES ('inv1', 'item1', 'loc1', 10, datetime('now'))`)
	execSQL(t, db, `INSERT INTO inventory (id, item_id, location_id, quantity, updated_at) VALUES ('inv2', 'item2', 'loc1', 20, datetime('now'))`)
	execSQL(t, db, `INSERT INTO inventory (id, item_id, location_id, quantity, updated_at) VALUES ('inv3', 'item3', 'loc1', 5, datetime('now'))`)

	items, err := GetLowStockItems(ctx, db, "")
	if err != nil {
		t.Fatalf("GetLowStockItems failed: %v", err)
	}

	// Should only return item1 (qty 10 < reorder_level 50)
	if len(items) != 1 {
		t.Fatalf("expected 1 low stock item, got %d", len(items))
	}
	if items[0].Name != "Low Stock Item" {
		t.Errorf("expected 'Low Stock Item', got %q", items[0].Name)
	}
	if items[0].CurrentQty != 10 {
		t.Errorf("expected qty 10, got %.2f", items[0].CurrentQty)
	}
	if items[0].ReorderLevel != 50 {
		t.Errorf("expected reorder level 50, got %d", items[0].ReorderLevel)
	}
}

// testDB creates an in-memory SQLite database for testing
func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	// Run migrations to create schema
	execSQL(t, db, schema())
	return db
}

func execSQL(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatalf("exec: %v\nquery: %s", err, query)
	}
}

// schema returns the minimal schema needed for inventory tests
func schema() string {
	return `
CREATE TABLE items (
  id TEXT PRIMARY KEY,
  sku TEXT UNIQUE,
  name TEXT NOT NULL,
  category TEXT NOT NULL,
  reorder_level INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);

CREATE TABLE locations (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE stock_locations (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL
);

CREATE TABLE inventory (
  id TEXT PRIMARY KEY,
  item_id TEXT,
  variant_id TEXT,
  location_id TEXT NOT NULL,
  quantity REAL NOT NULL DEFAULT 0,
  updated_at TEXT NOT NULL,
  FOREIGN KEY (item_id) REFERENCES items(id),
  FOREIGN KEY (location_id) REFERENCES locations(id)
);

CREATE TABLE stock_movements (
  id TEXT PRIMARY KEY,
  item_id TEXT,
  variant_id TEXT,
  location_id TEXT NOT NULL,
  sale_line_id TEXT,
  type TEXT NOT NULL,
  quantity REAL NOT NULL,
  cost_price INTEGER,
  created_at TEXT NOT NULL,
  FOREIGN KEY (item_id) REFERENCES items(id),
  FOREIGN KEY (location_id) REFERENCES locations(id)
);

CREATE TABLE audit_log (
  id TEXT PRIMARY KEY,
  actor_id TEXT,
  entity_type TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  action TEXT NOT NULL,
  data_json TEXT,
  created_at TEXT NOT NULL,
  blocked_actor_id TEXT
);

CREATE TABLE users (
  id TEXT PRIMARY KEY,
  username TEXT UNIQUE NOT NULL,
  pin_hash TEXT NOT NULL,
  role TEXT NOT NULL,
  created_at TEXT NOT NULL
);
`
}
