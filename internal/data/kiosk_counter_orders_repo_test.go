package data

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func openKioskCounterOrdersDB(t *testing.T, name string) *db.DB {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// Create must generate a "C-"-prefixed display_no distinct from any sale
// receipt/display-no sequence, and the order must then show up via
// ListOpen; MarkCollected must remove it from that same list.
func TestKioskCounterOrdersRepo_CreateListMarkCollected(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_orders.db")
	ctx := context.Background()
	repo := NewKioskCounterOrdersRepo(d.DB)

	created, err := repo.Create(ctx, KioskCounterOrder{
		OrderType: "takeaway",
		Lines: []KioskCounterOrderLine{
			{Name: "Flat White", Qty: 2, Modifiers: []string{"Oat milk"}},
			{Name: "Croissant", Qty: 1},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID == "" {
		t.Fatal("Create did not assign an id")
	}
	if !strings.HasPrefix(created.DisplayNo, "C-") {
		t.Fatalf("DisplayNo %q must be C--prefixed, never confusable with a sale display number", created.DisplayNo)
	}
	if created.Status != KioskCounterOrderStatusOpen {
		t.Fatalf("new order status = %q, want %q", created.Status, KioskCounterOrderStatusOpen)
	}

	open, err := repo.ListOpen(ctx)
	if err != nil {
		t.Fatalf("ListOpen: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("ListOpen: want 1 open order, got %d", len(open))
	}
	got := open[0]
	if got.ID != created.ID || got.DisplayNo != created.DisplayNo || got.OrderType != "takeaway" {
		t.Fatalf("ListOpen row mismatch: %+v", got)
	}
	if len(got.Lines) != 2 || got.Lines[0].Name != "Flat White" || got.Lines[0].Qty != 2 ||
		len(got.Lines[0].Modifiers) != 1 || got.Lines[0].Modifiers[0] != "Oat milk" {
		t.Fatalf("ListOpen lines round-trip mismatch: %+v", got.Lines)
	}

	if err := repo.MarkCollected(ctx, created.ID); err != nil {
		t.Fatalf("MarkCollected: %v", err)
	}
	open, err = repo.ListOpen(ctx)
	if err != nil {
		t.Fatalf("ListOpen after collect: %v", err)
	}
	if len(open) != 0 {
		t.Fatalf("ListOpen after MarkCollected: want 0, got %d: %+v", len(open), open)
	}

	var status, collectedAt string
	if err := d.DB.QueryRow(`SELECT status, collected_at FROM kiosk_counter_orders WHERE id = ?`, created.ID).Scan(&status, &collectedAt); err != nil {
		t.Fatalf("read back collected row: %v", err)
	}
	if status != KioskCounterOrderStatusCollected || collectedAt == "" {
		t.Fatalf("collected row: status=%q collected_at=%q", status, collectedAt)
	}
}

// Two Create calls must never hand back the same display_no.
func TestKioskCounterOrdersRepo_DisplayNoIncrementsAcrossOrders(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_orders_seq.db")
	ctx := context.Background()
	repo := NewKioskCounterOrdersRepo(d.DB)

	first, err := repo.Create(ctx, KioskCounterOrder{Lines: []KioskCounterOrderLine{{Name: "Tea", Qty: 1}}})
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, err := repo.Create(ctx, KioskCounterOrder{Lines: []KioskCounterOrderLine{{Name: "Tea", Qty: 1}}})
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if first.DisplayNo == second.DisplayNo {
		t.Fatalf("two counter orders got the same display_no %q", first.DisplayNo)
	}
}

// A second MarkCollected on an ALREADY-collected order must not move
// collected_at. The staff board polls every 15s, so two tills can both be
// showing the same still-open row; the second tap must not rewrite when
// the customer actually collected their order (review finding, ut-docs#582).
func TestKioskCounterOrdersRepo_MarkCollectedTwiceKeepsFirstCollectedAt(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_orders_recollect.db")
	ctx := context.Background()
	repo := NewKioskCounterOrdersRepo(d.DB)

	created, err := repo.Create(ctx, KioskCounterOrder{Lines: []KioskCounterOrderLine{{Name: "Tea", Qty: 1}}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.MarkCollected(ctx, created.ID); err != nil {
		t.Fatalf("MarkCollected: %v", err)
	}
	var first string
	if err := d.DB.QueryRow(`SELECT collected_at FROM kiosk_counter_orders WHERE id = ?`, created.ID).Scan(&first); err != nil {
		t.Fatal(err)
	}

	// Backdate it so a re-stamp would be unmistakable rather than landing
	// on the same RFC3339 second as the first call.
	past := "2020-01-01T00:00:00Z"
	if _, err := d.DB.Exec(`UPDATE kiosk_counter_orders SET collected_at = ? WHERE id = ?`, past, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkCollected(ctx, created.ID); err != nil {
		t.Fatalf("second MarkCollected: %v", err)
	}
	var second string
	if err := d.DB.QueryRow(`SELECT collected_at FROM kiosk_counter_orders WHERE id = ?`, created.ID).Scan(&second); err != nil {
		t.Fatal(err)
	}
	if second != past {
		t.Fatalf("second MarkCollected rewrote collected_at: %q -> %q (first call recorded %q)", past, second, first)
	}
}

// ut-docs#2221 review finding F1: Qty moved from a pre-formatted string to
// a raw float64 (so print and on-screen destinations can format it
// independently) — but a row an already-open counter order left behind
// across the upgrade still has "Qty":"2" (or, for a weighed line under a
// comma-decimal kiosk locale like de/tr, "Qty":"1,5") in lines_json.
// ListOpen must still read it back, not fail the whole query the instant
// one legacy row is in the open set.
func TestKioskCounterOrdersRepo_ListOpenToleratesLegacyStringQty(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_orders_legacy_qty.db")
	ctx := context.Background()

	// Bypass the repo's own Create (which only ever writes the current
	// shape) and insert exactly what the pre-#2221 code wrote: Qty as a
	// JSON string, including a comma-decimal weighed quantity.
	legacyLinesJSON := `[{"Name":"Flat White","Qty":"2","Modifiers":["Oat milk"]},{"Name":"Ham (weighed)","Qty":"1,5","Modifiers":null}]`
	if _, err := d.DB.ExecContext(ctx, `
INSERT INTO kiosk_counter_orders (id, display_no, order_type, lines_json, status, created_at)
VALUES ('legacy-1', 'C-1', 'takeaway', ?, ?, '2026-09-10T00:00:00Z')`,
		legacyLinesJSON, KioskCounterOrderStatusOpen); err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	repo := NewKioskCounterOrdersRepo(d.DB)
	open, err := repo.ListOpen(ctx)
	if err != nil {
		t.Fatalf("ListOpen must tolerate a legacy string-Qty row, got: %v", err)
	}
	if len(open) != 1 || len(open[0].Lines) != 2 {
		t.Fatalf("ListOpen result = %+v", open)
	}
	if open[0].Lines[0].Qty != 2 {
		t.Fatalf("legacy plain-integer qty: got %v, want 2", open[0].Lines[0].Qty)
	}
	if open[0].Lines[1].Qty != 1.5 {
		t.Fatalf("legacy comma-decimal qty: got %v, want 1.5", open[0].Lines[1].Qty)
	}
}

// MarkCollected on an unknown id must not error — same silent-no-op
// convention as a re-tapped "mark collected" button.
func TestKioskCounterOrdersRepo_MarkCollectedUnknownIDIsNoop(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_orders_unknown.db")
	repo := NewKioskCounterOrdersRepo(d.DB)
	if err := repo.MarkCollected(context.Background(), "does-not-exist"); err != nil {
		t.Fatalf("MarkCollected on unknown id: %v", err)
	}
}

// ut-docs#815: a counter order created from a table-QR self-order session
// (internal/pages/self_order_shop.go) carries the table it came from.
// Create must persist TableID, and ListOpen — reading it back for the
// staff board — must resolve TableLabel via the same LEFT JOIN tables
// pattern GetSaleDetail already uses for a sale's own TableLabel
// (ut-docs#820), never a raw id the board would have to look up itself.
func TestKioskCounterOrdersRepo_CreateAndListOpenResolveTableLabel(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_orders_table.db")
	ctx := context.Background()
	posRepo := NewPOSRepo(d.DB)
	tableID, err := posRepo.CreateTable(ctx, "T5", "Terrace", 4, "rect", 100, 100)
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	repo := NewKioskCounterOrdersRepo(d.DB)
	created, err := repo.Create(ctx, KioskCounterOrder{
		OrderType:  "",
		TableID:    tableID,
		TableLabel: "T5", // as the checkout handler already knows it, not read back here
		Lines:      []KioskCounterOrderLine{{Name: "Flat White", Qty: 1}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.TableID != tableID {
		t.Fatalf("Create result TableID = %q, want %q", created.TableID, tableID)
	}

	var gotTableID string
	if err := d.DB.QueryRow(`SELECT COALESCE(table_id, '') FROM kiosk_counter_orders WHERE id = ?`, created.ID).Scan(&gotTableID); err != nil {
		t.Fatal(err)
	}
	if gotTableID != tableID {
		t.Fatalf("stored table_id = %q, want %q", gotTableID, tableID)
	}

	open, err := repo.ListOpen(ctx)
	if err != nil {
		t.Fatalf("ListOpen: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("ListOpen: want 1, got %d", len(open))
	}
	if open[0].TableID != tableID {
		t.Fatalf("ListOpen TableID = %q, want %q", open[0].TableID, tableID)
	}
	if open[0].TableLabel != "T5" {
		t.Fatalf("ListOpen TableLabel = %q, want %q (resolved via join, not the write-time value)", open[0].TableLabel, "T5")
	}
}

// A plain counter order with no table (the existing #582 kiosk-till flow)
// must round-trip with empty TableID/TableLabel — no regression from this
// card's join.
func TestKioskCounterOrdersRepo_ListOpenNoTableIsEmpty(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_orders_no_table.db")
	ctx := context.Background()
	repo := NewKioskCounterOrdersRepo(d.DB)
	if _, err := repo.Create(ctx, KioskCounterOrder{Lines: []KioskCounterOrderLine{{Name: "Tea", Qty: 1}}}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	open, err := repo.ListOpen(ctx)
	if err != nil {
		t.Fatalf("ListOpen: %v", err)
	}
	if len(open) != 1 {
		t.Fatalf("ListOpen: want 1, got %d", len(open))
	}
	if open[0].TableID != "" || open[0].TableLabel != "" {
		t.Fatalf("expected no table on a plain counter order, got TableID=%q TableLabel=%q", open[0].TableID, open[0].TableLabel)
	}
}

// ut-docs#2703: a pay-at-counter order is now parked as a held sale; its
// kiosk_counter_orders row only records the "C-" number and what was
// ordered. It shares the one C- sequence with legacy rows, never shows on
// the legacy staff board, and can never be "collected" without payment.
func TestKioskCounterOrdersRepo_HeldOrderSharesSequenceButIsNotOpen(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_orders_held.db")
	ctx := context.Background()
	repo := NewKioskCounterOrdersRepo(d.DB)

	legacy, err := repo.Create(ctx, KioskCounterOrder{Lines: []KioskCounterOrderLine{{Name: "Tea", Qty: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	held, err := repo.Create(ctx, KioskCounterOrder{ID: "hold-abc", Status: KioskCounterOrderStatusHeld, Lines: []KioskCounterOrderLine{{Name: "Flat White", Qty: 1}}})
	if err != nil {
		t.Fatalf("Create(held): %v", err)
	}
	if legacy.DisplayNo != "C-1" || held.DisplayNo != "C-2" {
		t.Fatalf("display numbers = %q, %q; want C-1, C-2 from one sequence", legacy.DisplayNo, held.DisplayNo)
	}
	if held.ID != "hold-abc" || held.Status != KioskCounterOrderStatusHeld {
		t.Fatalf("Create(held) returned %+v", held)
	}
	open, err := repo.ListOpen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].ID != legacy.ID {
		t.Fatalf("ListOpen = %+v, want only the legacy open row", open)
	}
	if err := repo.MarkCollected(ctx, held.ID); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := d.DB.QueryRow(`SELECT status FROM kiosk_counter_orders WHERE id = ?`, held.ID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != KioskCounterOrderStatusHeld {
		t.Fatalf("MarkCollected moved a held (unpaid) order to %q", status)
	}
}

// ut-docs#2703 review F2: every till mints its own C- sequence from its own
// kiosk_counter_orders table, and a held counter order is pushed to the
// main till -- so two kiosks on two tills both minted "C-1" onto the main's
// Open orders and into sales.display_no. The till's sync.receipt_prefix
// namespaces the sequence exactly as NextDisplayNo does; no prefix keeps
// the plain "C-n".
func TestKioskCounterOrdersRepo_DisplayNoCarriesTillPrefix(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_orders_prefix.db")
	ctx := context.Background()
	repo := NewKioskCounterOrdersRepo(d.DB)
	line := []KioskCounterOrderLine{{Name: "Tea", Qty: 1}}

	plain, err := repo.Create(ctx, KioskCounterOrder{Lines: line})
	if err != nil {
		t.Fatal(err)
	}
	if plain.DisplayNo != "C-1" {
		t.Fatalf("no prefix configured: display_no = %q, want C-1", plain.DisplayNo)
	}

	if _, err := d.DB.Exec(`INSERT INTO settings (key, value) VALUES ('sync.receipt_prefix', 'T2-')`); err != nil {
		t.Fatal(err)
	}
	// A counter number from another till (pushed to this one) must not
	// bleed into this till's max either.
	if _, err := d.DB.Exec(`INSERT INTO kiosk_counter_orders (id, display_no, order_type, lines_json, status, created_at) VALUES ('other', 'C-T9-40', '', '[]', 'held', '2026-09-25T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	first, err := repo.Create(ctx, KioskCounterOrder{Lines: line})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.Create(ctx, KioskCounterOrder{Status: KioskCounterOrderStatusHeld, Lines: line})
	if err != nil {
		t.Fatal(err)
	}
	if first.DisplayNo != "C-T2-1" || second.DisplayNo != "C-T2-2" {
		t.Fatalf("prefixed display numbers = %q, %q; want C-T2-1, C-T2-2", first.DisplayNo, second.DisplayNo)
	}
}

// ...and the sale display-number sequence still ignores every C-number a
// paid counter order put into sales.display_no, prefixed or not.
func TestPOSRepo_NextDisplayNo_IgnoresCounterOrderNumbers(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_orders_nextdisplay.db")
	ctx := context.Background()
	pos := NewPOSRepo(d.DB)
	for i, dn := range []string{"3", "C-9", "C-T2-50", "C-T2-7"} {
		if _, err := d.DB.Exec(`INSERT INTO sales (id, receipt_no, display_no, status, sale_type, currency, subtotal, total, created_at)
VALUES (?, ?, ?, 'completed', 'sale', 'GBP', 100, 100, '2026-09-25T10:00:00Z')`, fmt.Sprintf("s%d", i), fmt.Sprintf("R%d", i), dn); err != nil {
			t.Fatal(err)
		}
	}
	got, err := pos.NextDisplayNo(ctx, nil)
	if err != nil || got != "4" {
		t.Fatalf("NextDisplayNo(no prefix) = (%q,%v), want (4,nil): C-numbers must not count", got, err)
	}
	if _, err := d.DB.Exec(`INSERT INTO settings (key, value) VALUES ('sync.receipt_prefix', 'T2-')`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`INSERT INTO sales (id, receipt_no, display_no, status, sale_type, currency, subtotal, total, created_at)
VALUES ('sp', 'T2-1', 'T2-5', 'completed', 'sale', 'GBP', 100, 100, '2026-09-25T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	got, err = pos.NextDisplayNo(ctx, nil)
	if err != nil || got != "T2-6" {
		t.Fatalf("NextDisplayNo(T2-) = (%q,%v), want (T2-6,nil): C-T2- numbers must not count", got, err)
	}
}

// ut-docs#2714: when parking a pay-at-counter order as a held sale fails,
// the checkout deletes the "held" counter row it just created, so no
// invisible orphan is left behind (and a retry reuses the same C-number).
// DeleteHeld must only ever remove a "held" row -- never an open or a
// collected one -- and an unknown id is a silent no-op.
func TestKioskCounterOrdersRepo_DeleteHeldOnlyDeletesHeldRows(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_orders_delete_held.db")
	ctx := context.Background()
	repo := NewKioskCounterOrdersRepo(d.DB)
	line := []KioskCounterOrderLine{{Name: "Tea", Qty: 1}}

	open, err := repo.Create(ctx, KioskCounterOrder{Lines: line})
	if err != nil {
		t.Fatal(err)
	}
	held, err := repo.Create(ctx, KioskCounterOrder{Status: KioskCounterOrderStatusHeld, Lines: line})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteHeld(ctx, open.ID); err != nil {
		t.Fatalf("DeleteHeld(open): %v", err)
	}
	if err := repo.DeleteHeld(ctx, held.ID); err != nil {
		t.Fatalf("DeleteHeld(held): %v", err)
	}
	if err := repo.DeleteHeld(ctx, "no-such-id"); err != nil {
		t.Fatalf("DeleteHeld(unknown): %v", err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM kiosk_counter_orders WHERE id = ?`, held.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("held row still present after DeleteHeld")
	}
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM kiosk_counter_orders WHERE id = ?`, open.ID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("DeleteHeld removed an OPEN row")
	}
	// The deleted number is free again: the next order reuses it.
	next, err := repo.Create(ctx, KioskCounterOrder{Status: KioskCounterOrderStatusHeld, Lines: line})
	if err != nil {
		t.Fatal(err)
	}
	if next.DisplayNo != held.DisplayNo {
		t.Fatalf("next display_no = %q, want the freed %q", next.DisplayNo, held.DisplayNo)
	}
}

// ut-docs#2714: "held" rows are only the C-number record of an order whose
// payable object is a held sale; once they are older than
// counterOrderHeldRetention they are pruned by Create (same tx, after the
// insert, so the new row already holds the max and the sequence never goes
// down). Open and collected rows are never touched.
func TestKioskCounterOrdersRepo_CreatePrunesStaleHeldRows(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_orders_prune.db")
	ctx := context.Background()
	repo := NewKioskCounterOrdersRepo(d.DB)
	old := time.Now().UTC().Add(-91 * 24 * time.Hour).Format(time.RFC3339)
	recent := time.Now().UTC().Add(-89 * 24 * time.Hour).Format(time.RFC3339)
	for _, row := range []struct{ id, no, status, at string }{
		{"stale-held", "C-50", KioskCounterOrderStatusHeld, old},
		{"recent-held", "C-10", KioskCounterOrderStatusHeld, recent},
		{"old-open", "C-11", KioskCounterOrderStatusOpen, old},
		{"old-collected", "C-12", KioskCounterOrderStatusCollected, old},
	} {
		if _, err := d.DB.Exec(`INSERT INTO kiosk_counter_orders (id, display_no, order_type, lines_json, status, created_at) VALUES (?, ?, '', '[]', ?, ?)`,
			row.id, row.no, row.status, row.at); err != nil {
			t.Fatal(err)
		}
	}
	created, err := repo.Create(ctx, KioskCounterOrder{Status: KioskCounterOrderStatusHeld, Lines: []KioskCounterOrderLine{{Name: "Tea", Qty: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if created.DisplayNo != "C-51" {
		t.Fatalf("display_no = %q, want C-51 (the stale row still counts toward MAX before it is pruned)", created.DisplayNo)
	}
	rows, err := d.DB.Query(`SELECT id FROM kiosk_counter_orders ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	want := []string{created.ID, "old-collected", "old-open", "recent-held"}
	sort.Strings(ids)
	sort.Strings(want)
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("rows after Create = %v, want %v (only the stale held row pruned)", ids, want)
	}
	// ...and the sequence never goes down afterwards.
	again, err := repo.Create(ctx, KioskCounterOrder{Lines: []KioskCounterOrderLine{{Name: "Tea", Qty: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if again.DisplayNo != "C-52" {
		t.Fatalf("display_no after prune = %q, want C-52", again.DisplayNo)
	}
}

// ut-docs#2714: a replica (sync.primary_url set) with no
// sync.receipt_prefix used to mint the same plain "C-<n>" as the main till,
// and both numbers end up on the main's Open orders. Such a till now
// derives its namespace from sync.till_id; the main till (no primary_url)
// keeps "C-<n>", and a replica with neither prefix nor till id stays plain.
func TestKioskCounterOrdersRepo_ReplicaWithoutPrefixDerivesOneFromTillID(t *testing.T) {
	line := []KioskCounterOrderLine{{Name: "Tea", Qty: 1}}
	cases := []struct {
		name     string
		settings map[string]string
		want     string
	}{
		{"main till keeps C-n", map[string]string{"sync.till_id": "7b2e-91ff"}, "C-1"},
		{"replica derives from till id", map[string]string{"sync.primary_url": "http://main:8080", "sync.till_id": "7b-2e_91ffab12"}, "C-7B2E91-1"},
		{"replica short till id", map[string]string{"sync.primary_url": "http://main:8080", "sync.till_id": "t2"}, "C-T2-1"},
		{"replica without till id stays plain", map[string]string{"sync.primary_url": "http://main:8080"}, "C-1"},
		{"explicit prefix wins", map[string]string{"sync.primary_url": "http://main:8080", "sync.till_id": "abcdef", "sync.receipt_prefix": "K1-"}, "C-K1-1"},
		{"blank-space prefix counts as blank", map[string]string{"sync.primary_url": "http://main:8080", "sync.till_id": "abcdef", "sync.receipt_prefix": "  "}, "C-ABCDEF-1"},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := openKioskCounterOrdersDB(t, fmt.Sprintf("counter_orders_replica_%d.db", i))
			for k, v := range c.settings {
				if _, err := d.DB.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)`, k, v); err != nil {
					t.Fatal(err)
				}
			}
			got, err := NewKioskCounterOrdersRepo(d.DB).Create(context.Background(), KioskCounterOrder{Lines: line})
			if err != nil {
				t.Fatal(err)
			}
			if got.DisplayNo != c.want {
				t.Fatalf("display_no = %q, want %q", got.DisplayNo, c.want)
			}
		})
	}
}

// ut-docs#2714: concurrent Create calls on one real, migrated database never
// hand out the same C-number twice (the write-locked tx serialises
// read-MAX + insert; migration 044's unique index makes it a hard rule).
func TestKioskCounterOrdersRepo_ConcurrentCreateNeverDuplicates(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_orders_concurrent.db")
	repo := NewKioskCounterOrdersRepo(d.DB)
	const workers, each = 8, 10
	var wg sync.WaitGroup
	errs := make(chan error, workers*each)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if _, err := repo.Create(context.Background(), KioskCounterOrder{Status: KioskCounterOrderStatusHeld, Lines: []KioskCounterOrderLine{{Name: "Tea", Qty: 1}}}); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Create: %v", err)
	}
	var total, distinct int
	if err := d.DB.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT display_no) FROM kiosk_counter_orders`).Scan(&total, &distinct); err != nil {
		t.Fatal(err)
	}
	if total != workers*each || distinct != total {
		t.Fatalf("rows = %d, distinct display_no = %d; want %d unique numbers", total, distinct, workers*each)
	}
}
