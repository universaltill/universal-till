package data

import (
	"context"
	"strings"
	"testing"
)

// ut-docs#3253: a GDPR erasure must leave no till holding the customer's
// name or contact data — not the primary's parked baskets, not a LAN
// replica whose own sales still reference the customer row.

const erasedName = "Test Erased Person"

func heldPayloadWithCustomer(cid string) string {
	return `{"lines":[{"item_id":"i1","qty":1,"unit_price":1250}],"customer_id":"` + cid +
		`","customer_name":"` + erasedName + `","total":1250}`
}

func TestEraseCustomer_StripsCustomerFromParkedBaskets(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "erase-held.db")
	mustExec(t, d, `INSERT INTO customers (id, name, phone) VALUES ('c1', ?, '555')`, erasedName)
	// Parked under the customer's name (the hold label fallback), then archived by a reset.
	mustExec(t, d, `INSERT INTO held_sales (id, label, total_minor, line_count, payload, created_at, updated_at)
		VALUES ('h-old', ?, 1250, 1, ?, '2026-09-01 10:00:00', '2026-09-01 10:00:00')`, erasedName, heldPayloadWithCustomer("c1"))
	repo := NewPOSRepo(d.DB)
	if _, _, err := repo.ResetTransactionHistory(ctx, "", ""); err != nil {
		t.Fatalf("reset: %v", err)
	}
	mustExec(t, d, `INSERT INTO held_sales (id, label, total_minor, line_count, payload, created_at, updated_at)
		VALUES ('h-live', ?, 1250, 1, ?, '2026-09-02 10:00:00', '2026-09-02 10:00:00')`, erasedName, heldPayloadWithCustomer("c1"))
	// A typed label and another customer's basket are left alone.
	mustExec(t, d, `INSERT INTO held_sales (id, label, total_minor, line_count, payload, created_at, updated_at)
		VALUES ('h-other', 'Table 4', 1250, 1, '{"lines":[],"customer_id":"c2","customer_name":"Someone Else","total":1250}', '2026-09-02 10:00:00', '2026-09-02 10:00:00')`)

	if ok, err := repo.EraseCustomer(ctx, "c1", "", ""); err != nil || !ok {
		t.Fatalf("erase: ok=%v err=%v", ok, err)
	}

	for _, tbl := range []string{"held_sales", "held_sales_archive"} {
		rows, err := d.DB.Query(`SELECT id, label, payload FROM ` + tbl + ` WHERE id IN ('h-old','h-live')`)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			n++
			var id, label, payload string
			if err := rows.Scan(&id, &label, &payload); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(label+payload, erasedName) || strings.Contains(payload, `"c1"`) {
				t.Errorf("%s %s still holds the erased customer: label=%q payload=%s", tbl, id, label, payload)
			}
			if !strings.Contains(payload, `"unit_price":1250`) || !strings.Contains(payload, `"total":1250`) {
				t.Errorf("%s %s basket contents changed: %s", tbl, id, payload)
			}
		}
		rows.Close()
		if n != 1 {
			t.Fatalf("%s: want 1 parked basket for c1, got %d", tbl, n)
		}
	}
	var label, payload, updated string
	if err := d.DB.QueryRow(`SELECT label, payload, updated_at FROM held_sales WHERE id='h-other'`).Scan(&label, &payload, &updated); err != nil {
		t.Fatal(err)
	}
	if label != "Table 4" || !strings.Contains(payload, "Someone Else") || updated != "2026-09-02 10:00:00" {
		t.Errorf("another customer's basket was touched: label=%q payload=%s updated_at=%s", label, payload, updated)
	}
	if err := d.DB.QueryRow(`SELECT updated_at FROM held_sales WHERE id='h-live'`).Scan(&updated); err != nil {
		t.Fatal(err)
	}
	if err := d.DB.QueryRow(`SELECT label FROM held_sales WHERE id='h-live'`).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if want := heldSaleClockLabel("2026-09-02 10:00:00"); label != want || want == "" {
		t.Errorf("a label that was the customer's name falls back to the park's clock time: got %q, want %q", label, want)
	}
	if updated == "2026-09-02 10:00:00" {
		t.Errorf("scrubbed live basket must move updated_at so an older replica write-through loses (ADR-0093)")
	}
}

func TestAdminApply_ErasedCustomerPinnedBySatelliteSaleBecomesAnonymousShell(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")

	mustExec(t, primary, `INSERT INTO customers (id, name, phone, email, address, loyalty_no, notes, phone_e164)
		VALUES ('c1', ?, '555-0100', 'erased@example.com', '1 Test Street', 'L-77', 'likes extra cheese', '5550100')`, erasedName)
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// The replica sold to the customer and parked a basket for them.
	mustExec(t, replica, `INSERT INTO sales (id, receipt_no, subtotal, total, customer_id) VALUES ('s1', 'R-1', 100, 100, 'c1')`)
	mustExec(t, replica, `INSERT INTO held_sales (id, label, total_minor, line_count, payload, created_at, updated_at)
		VALUES ('h1', ?, 1250, 1, ?, '2026-09-02 10:00:00', '2026-09-02 10:00:00')`, erasedName, heldPayloadWithCustomer("c1"))

	if ok, err := NewPOSRepo(primary.DB).EraseCustomer(ctx, "c1", "", ""); err != nil || !ok {
		t.Fatalf("erase on primary: ok=%v err=%v", ok, err)
	}
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("second dump: %v", err)
	}
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle2)); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	// A later pull must leave the shell alone: no version bump, so open
	// sale screens don't refresh on every pull (ut-docs#2875).
	adminBefore, sellBefore := syncAdminGeneration(t, replica), sellScreenGeneration(t, replica)
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle2)); err != nil {
		t.Fatalf("third apply: %v", err)
	}
	if got := syncAdminGeneration(t, replica); got != adminBefore {
		t.Errorf("re-applying the same bundle moved the admin generation %d -> %d", adminBefore, got)
	}
	if got := sellScreenGeneration(t, replica); got != sellBefore {
		t.Errorf("re-applying the same bundle moved the sell-screen generation %d -> %d", sellBefore, got)
	}

	var name string
	var phone, email, address, loyalty, notes, phoneE164 *string
	if err := replica.DB.QueryRow(`SELECT name, phone, email, address, loyalty_no, notes, phone_e164 FROM customers WHERE id='c1'`).
		Scan(&name, &phone, &email, &address, &loyalty, &notes, &phoneE164); err != nil {
		t.Fatalf("the shell row must stay for the replica's own sale's FK: %v", err)
	}
	if name != "" || phone != nil || email != nil || address != nil || loyalty != nil || notes != nil || phoneE164 != nil {
		t.Errorf("replica kept personal data after the primary's erasure: name=%q phone=%v email=%v address=%v loyalty_no=%v notes=%v phone_e164=%v",
			name, phone, email, address, loyalty, notes, phoneE164)
	}
	var label, payload string
	if err := replica.DB.QueryRow(`SELECT label, payload FROM held_sales WHERE id='h1'`).Scan(&label, &payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(label+payload, erasedName) || strings.Contains(payload, `"c1"`) {
		t.Errorf("replica's parked basket still holds the erased customer: label=%q payload=%s", label, payload)
	}
	// The shell is invisible to the customer readers.
	repo := NewPOSRepo(replica.DB)
	if found, err := repo.SearchCustomers(ctx, "", 50); err != nil || len(found) != 0 {
		t.Errorf("erasure picker lists the anonymous shell: err=%v found=%+v", err, found)
	}
	if _, _, ok := repo.LookupCustomer(ctx, "c1"); ok {
		t.Errorf("LookupCustomer resolves the anonymous shell")
	}
}

// The hard-delete branch: no replica sale pins the customer, so the row goes,
// and the replica's parked basket for them is stripped all the same.
func TestAdminApply_ErasedCustomerWithoutSatelliteHistoryStripsParkedBasket(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	mustExec(t, primary, `INSERT INTO customers (id, name) VALUES ('c1', ?)`, erasedName)
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatal(err)
	}
	mustExec(t, replica, `INSERT INTO held_sales (id, label, total_minor, line_count, payload, created_at, updated_at)
		VALUES ('h1', ?, 1250, 1, ?, '2026-09-02 10:00:00', '2026-09-02 10:00:00')`, erasedName, heldPayloadWithCustomer("c1"))
	if ok, err := NewPOSRepo(primary.DB).EraseCustomer(ctx, "c1", "", ""); err != nil || !ok {
		t.Fatalf("erase: ok=%v err=%v", ok, err)
	}
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle2)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := replica.DB.QueryRow(`SELECT count(*) FROM customers WHERE id='c1'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("customer row should be hard-deleted: n=%d err=%v", n, err)
	}
	var label, payload, updated string
	if err := replica.DB.QueryRow(`SELECT label, payload, updated_at FROM held_sales WHERE id='h1'`).Scan(&label, &payload, &updated); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(label+payload, erasedName) || strings.Contains(payload, `"c1"`) {
		t.Errorf("replica's parked basket still holds the erased customer: label=%q payload=%s", label, payload)
	}
	if updated != "2026-09-02 10:00:00" {
		t.Errorf("the replica-side strip must not move updated_at (ReconcileWithPrimary compares it): got %s", updated)
	}
}
