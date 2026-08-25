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

// TestCloseShift_SkimComputesNewFloat covers the ut-docs#1006 close-time
// skim-to-safe: new_float = counted closing cash minus skim, the skim lands
// as a second cash_adjustment audit row (type "skim", negative amount), and
// expected_cash stays untouched by the skim — variance is checked against
// the count BEFORE the skim is applied.
func TestCloseShift_SkimComputesNewFloat(t *testing.T) {
	ctx := context.Background()
	db := testShiftDB(t)
	defer db.Close()

	seedShiftData(t, db)

	shiftID, err := OpenShift(ctx, db, ShiftInput{
		RegisterID:  "reg1",
		CashierID:   "user1",
		OpeningCash: 10000, // £100.00 float
	})
	if err != nil {
		t.Fatalf("OpenShift failed: %v", err)
	}

	// £411.10 cash takings during the shift.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.ExecContext(ctx, `
INSERT INTO sales (id, receipt_no, status, sale_type, register_id, cashier_id, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES ('sale1', 'RCP-001', 'completed', 'sale', 'reg1', 'user1', 'GBP', 41110, 0, 0, 41110, ?, ?)
`, now, now); err != nil {
		t.Fatalf("insert sale: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO payments (id, sale_id, method_id, amount, currency, change_given, paid_at)
VALUES ('pay1', 'sale1', 'cash', 41110, 'GBP', 0, ?)
`, now); err != nil {
		t.Fatalf("insert payment: %v", err)
	}

	// Close counting £511.10, skimming £411.10 back to the safe.
	err = CloseShift(ctx, db, ShiftCloseInput{
		ShiftID:       shiftID,
		ClosingCash:   51110,
		Skim:          41110,
		SkimReason:    "evening skim",
		CountProtocol: `{"5000":10,"100":11,"10":1}`,
	})
	if err != nil {
		t.Fatalf("CloseShift failed: %v", err)
	}

	var closingCash, expectedCash, newFloat int64
	var countProtocol sql.NullString
	if err := db.QueryRowContext(ctx, `
SELECT closing_cash, expected_cash, new_float, count_protocol
FROM shifts WHERE id = ?`, shiftID).Scan(&closingCash, &expectedCash, &newFloat, &countProtocol); err != nil {
		t.Fatalf("query shift: %v", err)
	}
	if expectedCash != 51110 {
		t.Errorf("expected_cash must ignore the skim: want 51110, got %d", expectedCash)
	}
	if newFloat != 10000 {
		t.Errorf("new_float = closing - skim: want 10000, got %d", newFloat)
	}
	if countProtocol.String != `{"5000":10,"100":11,"10":1}` {
		t.Errorf("count_protocol not persisted, got %q", countProtocol.String)
	}

	// The skim is recorded through the same cash_adjustment audit shape
	// RecordCashAdjustment writes, so SumShiftAdjustments-style queries
	// treat it identically.
	var typ string
	var amount int64
	var reason string
	if err := db.QueryRowContext(ctx, `
SELECT json_extract(data_json, '$.type'),
       CAST(json_extract(data_json, '$.amount') AS INTEGER),
       json_extract(data_json, '$.reason')
FROM audit_log
WHERE entity_type = 'shift' AND entity_id = ? AND action = 'cash_adjustment'
`, shiftID).Scan(&typ, &amount, &reason); err != nil {
		t.Fatalf("query skim audit row: %v", err)
	}
	if typ != "skim" {
		t.Errorf("audit type: want skim, got %q", typ)
	}
	if amount != -41110 {
		t.Errorf("audit amount: want -41110 (cash leaving the drawer), got %d", amount)
	}
	if reason != "evening skim" {
		t.Errorf("audit reason: want 'evening skim', got %q", reason)
	}
}

// TestCloseShift_NoSkimStillRecordsNewFloat: with no skim the new float is
// simply the counted cash, and no cash_adjustment audit row is written.
func TestCloseShift_NoSkimStillRecordsNewFloat(t *testing.T) {
	ctx := context.Background()
	db := testShiftDB(t)
	defer db.Close()

	seedShiftData(t, db)

	shiftID, err := OpenShift(ctx, db, ShiftInput{
		RegisterID: "reg1", CashierID: "user1", OpeningCash: 10000,
	})
	if err != nil {
		t.Fatalf("OpenShift failed: %v", err)
	}
	if err := CloseShift(ctx, db, ShiftCloseInput{ShiftID: shiftID, ClosingCash: 10000}); err != nil {
		t.Fatalf("CloseShift failed: %v", err)
	}
	var newFloat int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(new_float, -1) FROM shifts WHERE id = ?`, shiftID).Scan(&newFloat); err != nil {
		t.Fatalf("query shift: %v", err)
	}
	if newFloat != 10000 {
		t.Errorf("new_float without skim: want counted 10000, got %d", newFloat)
	}
	var n int
	if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM audit_log
WHERE entity_type = 'shift' AND entity_id = ? AND action = 'cash_adjustment'`, shiftID).Scan(&n); err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if n != 0 {
		t.Errorf("no skim must write no cash_adjustment row, got %d", n)
	}
}

// TestCloseShift_SkimValidation: a skim can't be negative or exceed the
// counted drawer, and a malformed count protocol is rejected — all with no
// partial write (the shift stays open).
func TestCloseShift_SkimValidation(t *testing.T) {
	ctx := context.Background()
	db := testShiftDB(t)
	defer db.Close()

	seedShiftData(t, db)

	shiftID, err := OpenShift(ctx, db, ShiftInput{
		RegisterID: "reg1", CashierID: "user1", OpeningCash: 10000,
	})
	if err != nil {
		t.Fatalf("OpenShift failed: %v", err)
	}

	cases := []struct {
		name string
		in   ShiftCloseInput
	}{
		{"negative skim", ShiftCloseInput{ShiftID: shiftID, ClosingCash: 10000, Skim: -100}},
		{"skim exceeds counted cash", ShiftCloseInput{ShiftID: shiftID, ClosingCash: 10000, Skim: 10001}},
		{"malformed count protocol", ShiftCloseInput{ShiftID: shiftID, ClosingCash: 10000, CountProtocol: `{"100":`}},
	}
	for _, c := range cases {
		if err := CloseShift(ctx, db, c.in); err == nil {
			t.Errorf("%s: expected error, got nil", c.name)
		}
		var closedAt sql.NullString
		if err := db.QueryRowContext(ctx, `SELECT closed_at FROM shifts WHERE id = ?`, shiftID).Scan(&closedAt); err != nil {
			t.Fatalf("%s: query shift: %v", c.name, err)
		}
		if closedAt.Valid {
			t.Fatalf("%s: rejected close must not close the shift", c.name)
		}
	}
}

// TestRecordCashAdjustment_SkimType: "skim" joins payout|adjustment as a
// valid mid-shift adjustment type (no TSE/fiscal gate here — that is
// ut-docs#998's open question, deliberately not decided by this change).
func TestRecordCashAdjustment_SkimType(t *testing.T) {
	ctx := context.Background()
	db := testShiftDB(t)
	defer db.Close()

	seedShiftData(t, db)

	shiftID, err := OpenShift(ctx, db, ShiftInput{
		RegisterID: "reg1", CashierID: "user1", OpeningCash: 10000,
	})
	if err != nil {
		t.Fatalf("OpenShift failed: %v", err)
	}
	adjID, err := RecordCashAdjustment(ctx, db, CashAdjustmentInput{
		ShiftID: shiftID,
		Type:    "skim",
		Amount:  -5000,
		Reason:  "midday skim",
		ActorID: "user1",
	})
	if err != nil {
		t.Fatalf("RecordCashAdjustment(skim) failed: %v", err)
	}
	var typ string
	if err := db.QueryRowContext(ctx, `
SELECT json_extract(data_json, '$.type') FROM audit_log WHERE id = ?`, adjID).Scan(&typ); err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if typ != "skim" {
		t.Errorf("expected type skim, got %q", typ)
	}
	// Still rejects an unknown type.
	if _, err := RecordCashAdjustment(ctx, db, CashAdjustmentInput{
		ShiftID: shiftID, Type: "withdrawal", Amount: -100, Reason: "x", ActorID: "user1",
	}); err == nil {
		t.Error("expected error for unknown adjustment type")
	}
}

// TestLastClosedShiftNewFloat covers the opening-float carry-forward
// lookup: none without a closed shift; new_float when recorded; falling
// back to closing_cash for a shift closed before this feature existed.
func TestLastClosedShiftNewFloat(t *testing.T) {
	ctx := context.Background()
	db := testShiftDB(t)
	defer db.Close()

	seedShiftData(t, db)

	// No closed shift yet.
	if _, ok, err := LastClosedShiftNewFloat(ctx, db, "reg1"); err != nil || ok {
		t.Fatalf("expected no carry-forward yet, got ok=%v err=%v", ok, err)
	}

	// A shift closed with a skim: carry-forward is its new_float.
	shiftID, err := OpenShift(ctx, db, ShiftInput{RegisterID: "reg1", CashierID: "user1", OpeningCash: 10000})
	if err != nil {
		t.Fatalf("OpenShift failed: %v", err)
	}
	if err := CloseShift(ctx, db, ShiftCloseInput{ShiftID: shiftID, ClosingCash: 51110, Skim: 41110}); err != nil {
		t.Fatalf("CloseShift failed: %v", err)
	}
	cf, ok, err := LastClosedShiftNewFloat(ctx, db, "reg1")
	if err != nil || !ok {
		t.Fatalf("expected carry-forward, got ok=%v err=%v", ok, err)
	}
	if cf.Minor() != 10000 {
		t.Errorf("carry-forward: want 10000, got %d", cf.Minor())
	}

	// A pre-feature closed shift (new_float NULL) falls back to its
	// closing_cash — simulate by clearing the column, and make it the most
	// recent close.
	if _, err := db.ExecContext(ctx, `UPDATE shifts SET new_float = NULL, closed_at = ? WHERE id = ?`,
		time.Now().UTC().Add(time.Hour).Format(time.RFC3339), shiftID); err != nil {
		t.Fatalf("simulate pre-feature close: %v", err)
	}
	cf, ok, err = LastClosedShiftNewFloat(ctx, db, "reg1")
	if err != nil || !ok {
		t.Fatalf("expected fallback carry-forward, got ok=%v err=%v", ok, err)
	}
	if cf.Minor() != 51110 {
		t.Errorf("fallback carry-forward: want closing_cash 51110, got %d", cf.Minor())
	}

	// Another register is unaffected.
	if _, ok, err := LastClosedShiftNewFloat(ctx, db, "reg2"); err != nil || ok {
		t.Fatalf("expected no carry-forward for reg2, got ok=%v err=%v", ok, err)
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
  new_float INTEGER,
  count_protocol TEXT,
  FOREIGN KEY (register_id) REFERENCES registers (id),
  FOREIGN KEY (cashier_id) REFERENCES users (id)
);

CREATE TABLE sales (
  id TEXT PRIMARY KEY,
  receipt_no TEXT NOT NULL UNIQUE,
  status TEXT NOT NULL,
  sale_type TEXT NOT NULL,
  tender_type TEXT NOT NULL DEFAULT 'unknown',
  offline INTEGER NOT NULL DEFAULT 0,
  sync_status TEXT NOT NULL DEFAULT 'queued',
  sync_attempts INTEGER NOT NULL DEFAULT 0,
  sync_next_attempt_at TEXT,
  sync_last_error TEXT,
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
  tip_amount INTEGER NOT NULL DEFAULT 0,
  tip_recipient TEXT NOT NULL DEFAULT 'employee',
  masked_pan TEXT,
  auth_code TEXT,
  terminal_id TEXT,
  trace_id TEXT,
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
  created_at TEXT NOT NULL,
  blocked_actor_id TEXT
);
`
}
