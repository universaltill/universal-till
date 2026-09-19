package data

import (
	"context"
	"database/sql"
	"testing"

	"github.com/universaltill/universal-till/internal/money"
)

// seedShrinkageItem inserts the minimal items row shrinkage_events.item_id's
// FOREIGN KEY (no cascade, 035_shrinkage_events.sql) requires.
func seedShrinkageItem(t *testing.T, dbx *posTestDB, id, sku, name string) {
	t.Helper()
	if _, err := dbx.d.DB.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES(?,?,?,100,1)`, id, sku, name); err != nil {
		t.Fatalf("seed item %s: %v", id, err)
	}
}

// TestInsertShrinkageEvent_DirectPermission_NoApprover covers the "session
// user already held void_comp_waste" path (internal/pages/pos_api.go's
// `elev.Outcome == allowed` branch): approver_id must be left empty/NULL,
// never defaulted to the actor.
func TestInsertShrinkageEvent_DirectPermission_NoApprover(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	seedShrinkageItem(t, dbx, "itm1", "SKU1", "Apple")

	if err := dbx.repo.InsertShrinkageEvent(ctx, nil,
		"void", "itm1", "Apple", "SKU1", 2,
		money.FromMinor(150), money.FromMinor(300),
		"user1", "", "", "", "reg1",
		"2026-09-19T10:00:00Z", "evt1"); err != nil {
		t.Fatalf("InsertShrinkageEvent: %v", err)
	}

	var reasonCategory, itemName, actorID string
	var approverID, note sql.NullString
	var unitPriceMinor, extendedValueMinor int64
	var qty float64
	row := dbx.d.DB.QueryRow(`SELECT reason_category, item_name, actor_id, approver_id, note, unit_price_minor, extended_value_minor, quantity FROM shrinkage_events WHERE id = 'evt1'`)
	if err := row.Scan(&reasonCategory, &itemName, &actorID, &approverID, &note, &unitPriceMinor, &extendedValueMinor, &qty); err != nil {
		t.Fatalf("scan shrinkage_events row: %v", err)
	}
	if reasonCategory != "void" || itemName != "Apple" || actorID != "user1" {
		t.Fatalf("unexpected row: reason=%s item=%s actor=%s", reasonCategory, itemName, actorID)
	}
	if approverID.Valid {
		t.Fatalf("approver_id must be NULL for a direct-permission removal, got %q", approverID.String)
	}
	if unitPriceMinor != 150 || extendedValueMinor != 300 || qty != 2 {
		t.Fatalf("unexpected money/qty: unit=%d extended=%d qty=%v", unitPriceMinor, extendedValueMinor, qty)
	}
}

// TestInsertShrinkageEvent_Elevated covers the manager-PIN-elevated path:
// approver_id set, distinct from actor_id (the originally-blocked session
// user) — same dual-attribution shape InsertAuditElevated already uses.
func TestInsertShrinkageEvent_Elevated(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	seedShrinkageItem(t, dbx, "itm1", "SKU1", "Apple")

	if err := dbx.repo.InsertShrinkageEvent(ctx, nil,
		"waste", "itm1", "Apple", "SKU1", 1,
		money.FromMinor(150), money.FromMinor(150),
		"user1", "user2", "dropped on floor", "takeaway", "reg1",
		"2026-09-19T10:05:00Z", "evt2"); err != nil {
		t.Fatalf("InsertShrinkageEvent: %v", err)
	}

	var actorID, approverID, note, orderType string
	row := dbx.d.DB.QueryRow(`SELECT actor_id, approver_id, note, order_type FROM shrinkage_events WHERE id = 'evt2'`)
	if err := row.Scan(&actorID, &approverID, &note, &orderType); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if actorID != "user1" || approverID != "user2" || note != "dropped on floor" || orderType != "takeaway" {
		t.Fatalf("unexpected dual-attribution row: actor=%s approver=%s note=%s orderType=%s", actorID, approverID, note, orderType)
	}
}

// TestInsertShrinkageEvent_GeneratesID mirrors insertAudit's own "empty id
// generates a fresh uuid" contract.
func TestInsertShrinkageEvent_GeneratesID(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	seedShrinkageItem(t, dbx, "itm1", "SKU1", "Apple")

	if err := dbx.repo.InsertShrinkageEvent(ctx, nil,
		"comp", "itm1", "Apple", "SKU1", 1,
		money.FromMinor(150), money.FromMinor(150),
		"user1", "", "", "", "reg1",
		"2026-09-19T10:10:00Z", ""); err != nil {
		t.Fatalf("InsertShrinkageEvent: %v", err)
	}
	var count int
	if err := dbx.d.DB.QueryRow(`SELECT COUNT(*) FROM shrinkage_events WHERE reason_category = 'comp'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one comp row, got %d", count)
	}
	var id string
	if err := dbx.d.DB.QueryRow(`SELECT id FROM shrinkage_events WHERE reason_category = 'comp'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected a generated non-empty id")
	}
}

// seedShrinkageEvent is the minimal fixture ShrinkageByReason/
// ShrinkageTopItems tests need to insert a row directly (bypassing
// InsertShrinkageEvent so a createdAt outside "now" can be set for window
// testing, same reason seedLifecycleSale bypasses InsertSale).
func seedShrinkageEvent(t *testing.T, dbx *posTestDB, id, reason, itemName string, qty float64, unitPriceMinor, extendedMinor int64, createdAt string) {
	t.Helper()
	if _, err := dbx.d.DB.Exec(`INSERT INTO shrinkage_events (id, reason_category, item_name, quantity, unit_price_minor, extended_value_minor, actor_id, order_type, created_at)
VALUES (?, ?, ?, ?, ?, ?, 'user1', '', ?)`, id, reason, itemName, qty, unitPriceMinor, extendedMinor, createdAt); err != nil {
		t.Fatalf("seed shrinkage event %s: %v", id, err)
	}
}

func TestShrinkageByReason_GroupsAndTotals(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	seedShrinkageEvent(t, dbx, "e1", "void", "Apple", 2, 150, 300, relDays(-1))
	seedShrinkageEvent(t, dbx, "e2", "void", "Banana", 1, 100, 100, relDays(-1))
	seedShrinkageEvent(t, dbx, "e3", "waste", "Apple", 1, 150, 150, relDays(-2))
	// Outside the 7-day window — must not be counted.
	seedShrinkageEvent(t, dbx, "e4", "comp", "Apple", 1, 150, 150, relDays(-30))

	totals, err := dbx.repo.ShrinkageByReason(ctx, winFrom(7), winTo())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]ShrinkageReasonTotal{}
	for _, tt := range totals {
		got[tt.ReasonCategory] = tt
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 reason categories in window, got %+v", totals)
	}
	if got["void"].Count != 2 || got["void"].Total != 400 {
		t.Fatalf("unexpected void total: %+v", got["void"])
	}
	if got["waste"].Count != 1 || got["waste"].Total != 150 {
		t.Fatalf("unexpected waste total: %+v", got["waste"])
	}
	if _, ok := got["comp"]; ok {
		t.Fatalf("comp event outside window must not be counted: %+v", totals)
	}
}

func TestShrinkageByReason_EmptyWindowReturnsEmptyNotError(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	totals, err := dbx.repo.ShrinkageByReason(ctx, winFrom(7), winTo())
	if err != nil {
		t.Fatalf("expected no error on an empty window, got %v", err)
	}
	if len(totals) != 0 {
		t.Fatalf("expected zero rows, got %+v", totals)
	}
}

func TestShrinkageTopItems_OrdersByValueDescending(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	seedShrinkageEvent(t, dbx, "e1", "void", "Apple", 2, 150, 300, relDays(-1))
	seedShrinkageEvent(t, dbx, "e2", "waste", "Apple", 1, 150, 150, relDays(-1))
	seedShrinkageEvent(t, dbx, "e3", "comp", "Banana", 5, 100, 500, relDays(-1))
	seedShrinkageEvent(t, dbx, "e4", "void", "Cherry", 1, 50, 50, relDays(-30)) // outside window

	items, err := dbx.repo.ShrinkageTopItems(ctx, winFrom(7), winTo(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items in window, got %+v", items)
	}
	if items[0].Name != "Banana" || items[0].Revenue != 500 {
		t.Fatalf("expected Banana (500) first, got %+v", items[0])
	}
	if items[1].Name != "Apple" || items[1].Revenue != 450 || items[1].Qty != 3 {
		t.Fatalf("expected Apple (450, qty 3) second, got %+v", items[1])
	}
}
