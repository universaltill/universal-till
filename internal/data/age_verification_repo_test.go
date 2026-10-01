package data

import (
	"context"
	"database/sql"
	"testing"
)

// seedAgeVerificationSale inserts the minimal items + sales rows that
// age_verifications' FOREIGN KEYs (056_age_verifications.sql, no cascade)
// require.
func seedAgeVerificationSale(t *testing.T, dbx *posTestDB, itemID, saleID string) {
	t.Helper()
	if _, err := dbx.d.DB.Exec(`INSERT INTO items(id,sku,name,base_price,is_active,age_restricted) VALUES(?,?,?,100,1,1)`, itemID, itemID+"-SKU", "Cider"); err != nil {
		t.Fatalf("seed item %s: %v", itemID, err)
	}
	if _, err := dbx.d.DB.Exec(`INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at,completed_at) VALUES(?,?, 'completed','sale','GBP',100,0,20,120,datetime('now'),datetime('now'))`, saleID, "R-"+saleID); err != nil {
		t.Fatalf("seed sale %s: %v", saleID, err)
	}
}

func TestInsertAgeVerification_RecordsSnapshotRow(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	seedAgeVerificationSale(t, dbx, "itm-cider", "sale-1")

	if err := dbx.repo.InsertAgeVerification(ctx, nil, "sale-1", "itm-cider", "Cider 500ml", "accepted", "user1", "2026-10-01T10:00:00Z", "av1"); err != nil {
		t.Fatalf("InsertAgeVerification: %v", err)
	}
	var saleID, itemID, itemName, outcome, cashierID, createdAt string
	if err := dbx.d.DB.QueryRow(`SELECT sale_id, item_id, item_name, outcome, cashier_id, created_at FROM age_verifications WHERE id = 'av1'`).
		Scan(&saleID, &itemID, &itemName, &outcome, &cashierID, &createdAt); err != nil {
		t.Fatalf("scan row: %v", err)
	}
	if saleID != "sale-1" || itemID != "itm-cider" || itemName != "Cider 500ml" || outcome != "accepted" || cashierID != "user1" || createdAt != "2026-10-01T10:00:00Z" {
		t.Fatalf("unexpected row: sale=%s item=%s name=%s outcome=%s cashier=%s at=%s", saleID, itemID, itemName, outcome, cashierID, createdAt)
	}
}

// The schema itself refuses an outcome outside the fixed vocabulary — the
// CHECK constraint is the last line of defence behind
// pos.ValidAgeVerificationOutcome.
func TestInsertAgeVerification_CheckConstraintRejectsUnknownOutcome(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	seedAgeVerificationSale(t, dbx, "itm-cider", "sale-1")
	if err := dbx.repo.InsertAgeVerification(ctx, nil, "sale-1", "itm-cider", "Cider", "maybe", "user1", "2026-10-01T10:00:00Z", ""); err == nil {
		t.Fatal("an outcome outside accepted|refused must be refused by the schema")
	}
}

// Empty ids store NULL (nullIfEmpty), and an empty id mints a uuid — same
// conventions as InsertShrinkageEvent.
func TestInsertAgeVerification_EmptyOptionalIDsStoreNull(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	seedAgeVerificationSale(t, dbx, "itm-cider", "sale-1")
	if err := dbx.repo.InsertAgeVerification(ctx, nil, "sale-1", "", "Cider", "refused", "", "2026-10-01T10:00:00Z", ""); err != nil {
		t.Fatalf("InsertAgeVerification: %v", err)
	}
	var id string
	var itemID, cashierID sql.NullString
	if err := dbx.d.DB.QueryRow(`SELECT id, item_id, cashier_id FROM age_verifications`).Scan(&id, &itemID, &cashierID); err != nil {
		t.Fatal(err)
	}
	if id == "" || itemID.Valid || cashierID.Valid {
		t.Fatalf("want generated id and NULL item/cashier, got id=%q item=%v cashier=%v", id, itemID, cashierID)
	}
}

func TestAgeRestrictedItemIDs(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	for _, s := range []string{
		`INSERT INTO items(id,sku,name,base_price,is_active,age_restricted) VALUES('itm-beer','B','Beer',300,1,1)`,
		`INSERT INTO items(id,sku,name,base_price,is_active,age_restricted) VALUES('itm-bread','BR','Bread',150,1,0)`,
	} {
		if _, err := dbx.d.DB.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	got, err := dbx.repo.AgeRestrictedItemIDs(ctx, []string{"itm-beer", "itm-bread", "itm-missing", "", "itm-beer"})
	if err != nil {
		t.Fatalf("AgeRestrictedItemIDs: %v", err)
	}
	if len(got) != 1 || !got["itm-beer"] {
		t.Fatalf("want only itm-beer restricted, got %v", got)
	}
	empty, err := dbx.repo.AgeRestrictedItemIDs(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("no ids: want empty map and no error, got %v, %v", empty, err)
	}
}
