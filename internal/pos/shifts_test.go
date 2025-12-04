package pos

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestOpenShift(t *testing.T) {
	ctx := context.Background()
	db := testShiftDB(t)
	defer db.Close()

	seedShiftData(t, db)

	shiftID, err := OpenShift(ctx, db, ShiftInput{
		RegisterID:  "reg1",
		CashierID:   "user1",
		OpeningCash: 10000, // £100.00
	})
	if err != nil {
		t.Fatalf("OpenShift failed: %v", err)
	}
	if shiftID == "" {
		t.Fatal("expected shift ID")
	}

	// Verify shift record
	var registerID, cashierID string
	var openingCash int64
	var closedAt sql.NullString
	err = db.QueryRowContext(ctx, `
SELECT register_id, cashier_id, opening_cash, closed_at
FROM shifts
WHERE id = ?
`, shiftID).Scan(&registerID, &cashierID, &openingCash, &closedAt)
	if err != nil {
		t.Fatalf("query shift: %v", err)
	}
	if registerID != "reg1" {
		t.Errorf("expected register reg1, got %s", registerID)
	}
	if cashierID != "user1" {
		t.Errorf("expected cashier user1, got %s", cashierID)
	}
	if openingCash != 10000 {
		t.Errorf("expected opening cash 10000, got %d", openingCash)
	}
	if closedAt.Valid {
		t.Error("expected closed_at to be NULL")
	}

	// Verify audit log
	var auditCount int
	err = db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM audit_log
WHERE entity_type = 'shift' AND entity_id = ? AND action = 'open'
`, shiftID).Scan(&auditCount)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("expected 1 audit entry, got %d", auditCount)
	}
}

func TestOpenShift_DuplicateRegister(t *testing.T) {
	ctx := context.Background()
	db := testShiftDB(t)
	defer db.Close()

	seedShiftData(t, db)

	// Open first shift
	_, err := OpenShift(ctx, db, ShiftInput{
		RegisterID:  "reg1",
		CashierID:   "user1",
		OpeningCash: 10000,
	})
	if err != nil {
		t.Fatalf("first OpenShift failed: %v", err)
	}

	// Try to open another shift on same register
	_, err = OpenShift(ctx, db, ShiftInput{
		RegisterID:  "reg1",
		CashierID:   "user2",
		OpeningCash: 5000,
	})
	if err == nil {
		t.Fatal("expected error for duplicate open shift")
	}
}

func TestCloseShift(t *testing.T) {
	ctx := context.Background()
	db := testShiftDB(t)
	defer db.Close()

	seedShiftData(t, db)

	// Open shift
	shiftID, err := OpenShift(ctx, db, ShiftInput{
		RegisterID:  "reg1",
		CashierID:   "user1",
		OpeningCash: 10000,
	})
	if err != nil {
		t.Fatalf("OpenShift failed: %v", err)
	}

	// Add a completed sale with cash payment
	now := time.Now().UTC().Format(time.RFC3339)
	saleID := "sale1"
	_, err = db.ExecContext(ctx, `
INSERT INTO sales (id, receipt_no, status, sale_type, register_id, cashier_id, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES (?, 'RCP-001', 'completed', 'sale', 'reg1', 'user1', 'GBP', 100, 0, 20, 120, ?, ?)
`, saleID, now, now)
	if err != nil {
		t.Fatalf("insert sale: %v", err)
	}

	_, err = db.ExecContext(ctx, `
INSERT INTO payments (id, sale_id, method_id, amount, currency, change_given, paid_at)
VALUES ('pay1', ?, 'cash', 200, 'GBP', 80, ?)
`, saleID, now)
	if err != nil {
		t.Fatalf("insert payment: %v", err)
	}

	// Close shift
	err = CloseShift(ctx, db, ShiftCloseInput{
		ShiftID:     shiftID,
		ClosingCash: 10120, // opening 10000 + net cash 120
		Note:        "end of day",
	})
	if err != nil {
		t.Fatalf("CloseShift failed: %v", err)
	}

	// Verify shift closed
	var closingCash, expectedCash int64
	var closedAt, note sql.NullString
	err = db.QueryRowContext(ctx, `
SELECT closed_at, closing_cash, expected_cash, note
FROM shifts
WHERE id = ?
`, shiftID).Scan(&closedAt, &closingCash, &expectedCash, &note)
	if err != nil {
		t.Fatalf("query shift: %v", err)
	}
	if !closedAt.Valid {
		t.Error("expected closed_at to be set")
	}
	if closingCash != 10120 {
		t.Errorf("expected closing cash 10120, got %d", closingCash)
	}
	// Expected: opening 10000 + cash payment 120 (200 - 80 change)
	if expectedCash != 10120 {
		t.Errorf("expected cash 10120, got %d", expectedCash)
	}
	if note.String != "end of day" {
		t.Errorf("expected note 'end of day', got %s", note.String)
	}

	// Verify audit log
	var auditCount int
	err = db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM audit_log
WHERE entity_type = 'shift' AND entity_id = ? AND action = 'close'
`, shiftID).Scan(&auditCount)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if auditCount != 1 {
		t.Errorf("expected 1 close audit entry, got %d", auditCount)
	}
}

func TestComputeExpectedCash(t *testing.T) {
	ctx := context.Background()
	db := testShiftDB(t)
	defer db.Close()

	seedShiftData(t, db)

	// Open shift
	shiftID, err := OpenShift(ctx, db, ShiftInput{
		RegisterID:  "reg1",
		CashierID:   "user1",
		OpeningCash: 10000,
	})
	if err != nil {
		t.Fatalf("OpenShift failed: %v", err)
	}

	// Add cash payment
	now := time.Now().UTC().Format(time.RFC3339)
	saleID := "sale1"
	_, err = db.ExecContext(ctx, `
INSERT INTO sales (id, receipt_no, status, sale_type, register_id, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES (?, 'RCP-001', 'completed', 'sale', 'reg1', 'GBP', 100, 0, 20, 120, ?, ?)
`, saleID, now, now)
	if err != nil {
		t.Fatalf("insert sale: %v", err)
	}

	_, err = db.ExecContext(ctx, `
INSERT INTO payments (id, sale_id, method_id, amount, currency, change_given, paid_at)
VALUES ('pay1', ?, 'cash', 150, 'GBP', 30, ?)
`, saleID, now)
	if err != nil {
		t.Fatalf("insert payment: %v", err)
	}

	// Add payout
	_, err = RecordCashAdjustment(ctx, db, CashAdjustmentInput{
		ShiftID: shiftID,
		Type:    "payout",
		Amount:  -5000, // £50 payout
		Reason:  "supplier payment",
		ActorID: "user1",
	})
	if err != nil {
		t.Fatalf("RecordCashAdjustment failed: %v", err)
	}

	// Compute expected cash
	expectedCash, err := ComputeExpectedCash(ctx, db, shiftID, 10000)
	if err != nil {
		t.Fatalf("ComputeExpectedCash failed: %v", err)
	}

	// Expected: 10000 (opening) + 120 (cash payment net) - 5000 (payout) = 5120
	want := int64(5120)
	if expectedCash != want {
		t.Errorf("expected cash %d, got %d", want, expectedCash)
	}
}

func TestRecordCashAdjustment(t *testing.T) {
	ctx := context.Background()
	db := testShiftDB(t)
	defer db.Close()

	seedShiftData(t, db)

	// Open shift
	shiftID, err := OpenShift(ctx, db, ShiftInput{
		RegisterID:  "reg1",
		CashierID:   "user1",
		OpeningCash: 10000,
	})
	if err != nil {
		t.Fatalf("OpenShift failed: %v", err)
	}

	// Record payout
	adjustmentID, err := RecordCashAdjustment(ctx, db, CashAdjustmentInput{
		ShiftID: shiftID,
		Type:    "payout",
		Amount:  -2000,
		Reason:  "petty cash",
		ActorID: "user1",
	})
	if err != nil {
		t.Fatalf("RecordCashAdjustment failed: %v", err)
	}
	if adjustmentID == "" {
		t.Fatal("expected adjustment ID")
	}

	// Verify audit log
	var action string
	var dataJSON string
	err = db.QueryRowContext(ctx, `
SELECT action, data_json FROM audit_log WHERE id = ?
`, adjustmentID).Scan(&action, &dataJSON)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if action != "cash_adjustment" {
		t.Errorf("expected action 'cash_adjustment', got %s", action)
	}
	if dataJSON == "" {
		t.Error("expected non-empty data_json")
	}
}

func TestRecordCashAdjustment_ClosedShift(t *testing.T) {
	ctx := context.Background()
	db := testShiftDB(t)
	defer db.Close()

	seedShiftData(t, db)

	// Open and close shift
	shiftID, _ := OpenShift(ctx, db, ShiftInput{
		RegisterID:  "reg1",
		CashierID:   "user1",
		OpeningCash: 10000,
	})
	_ = CloseShift(ctx, db, ShiftCloseInput{
		ShiftID:     shiftID,
		ClosingCash: 10000,
	})

	// Try to record adjustment on closed shift
	_, err := RecordCashAdjustment(ctx, db, CashAdjustmentInput{
		ShiftID: shiftID,
		Type:    "payout",
		Amount:  -1000,
		Reason:  "test",
		ActorID: "user1",
	})
	if err == nil {
		t.Fatal("expected error for closed shift")
	}
}

// testShiftDB creates an in-memory SQLite database for shift testing
func testShiftDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	execSQL(t, db, shiftSchema())
	return db
}

func seedShiftData(t *testing.T, db *sql.DB) {
	t.Helper()
	execSQL(t, db, `INSERT INTO registers (id, name, is_active) VALUES ('reg1', 'Main Register', 1)`)
	execSQL(t, db, `INSERT INTO registers (id, name, is_active) VALUES ('reg2', 'Second Register', 1)`)
	execSQL(t, db, `INSERT INTO users (id, username, pin_hash, role, created_at) VALUES ('user1', 'cashier1', '', 'cashier', datetime('now'))`)
	execSQL(t, db, `INSERT INTO users (id, username, pin_hash, role, created_at) VALUES ('user2', 'cashier2', '', 'cashier', datetime('now'))`)
	execSQL(t, db, `INSERT INTO payment_methods (id, name, type, is_active) VALUES ('cash', 'Cash', 'cash', 1)`)
	execSQL(t, db, `INSERT INTO payment_methods (id, name, type, is_active) VALUES ('card', 'Card', 'card', 1)`)
}

// shiftSchema returns the minimal schema needed for shift tests
func shiftSchema() string {
	return `
CREATE TABLE registers (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  is_active INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE users (
  id TEXT PRIMARY KEY,
  username TEXT UNIQUE NOT NULL,
  pin_hash TEXT NOT NULL,
  role TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE payment_methods (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  is_active INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE shifts (
  id TEXT PRIMARY KEY,
  register_id TEXT NOT NULL,
  cashier_id TEXT NOT NULL,
  opened_at TEXT NOT NULL DEFAULT (datetime('now')),
  closed_at TEXT,
  opening_cash INTEGER NOT NULL DEFAULT 0,
  closing_cash INTEGER,
  expected_cash INTEGER,
  note TEXT,
  FOREIGN KEY (register_id) REFERENCES registers (id),
  FOREIGN KEY (cashier_id) REFERENCES users (id)
);

CREATE TABLE sales (
  id TEXT PRIMARY KEY,
  receipt_no TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL,
  sale_type TEXT NOT NULL,
  register_id TEXT,
  cashier_id TEXT,
  customer_id TEXT,
  currency TEXT NOT NULL,
  subtotal INTEGER NOT NULL,
  discount_total INTEGER NOT NULL,
  tax_total INTEGER NOT NULL,
  total INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  completed_at TEXT,
  voided_at TEXT
);

CREATE TABLE payments (
  id TEXT PRIMARY KEY,
  sale_id TEXT NOT NULL,
  method_id TEXT NOT NULL,
  amount INTEGER NOT NULL,
  currency TEXT NOT NULL DEFAULT 'GBP',
  reference TEXT,
  change_given INTEGER NOT NULL DEFAULT 0,
  paid_at TEXT NOT NULL DEFAULT (datetime('now')),
  FOREIGN KEY (sale_id) REFERENCES sales (id),
  FOREIGN KEY (method_id) REFERENCES payment_methods (id)
);

CREATE TABLE audit_log (
  id TEXT PRIMARY KEY,
  actor_id TEXT,
  entity_type TEXT NOT NULL,
  entity_id TEXT NOT NULL,
  action TEXT NOT NULL,
  data_json TEXT,
  created_at TEXT NOT NULL
);
`
}
