package data

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

func openKioskCounterOrdersDB(t *testing.T, name string) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), name))
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
			{Name: "Flat White", Qty: "2", Modifiers: []string{"Oat milk"}},
			{Name: "Croissant", Qty: "1"},
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
	if len(got.Lines) != 2 || got.Lines[0].Name != "Flat White" || got.Lines[0].Qty != "2" ||
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

	first, err := repo.Create(ctx, KioskCounterOrder{Lines: []KioskCounterOrderLine{{Name: "Tea", Qty: "1"}}})
	if err != nil {
		t.Fatalf("Create first: %v", err)
	}
	second, err := repo.Create(ctx, KioskCounterOrder{Lines: []KioskCounterOrderLine{{Name: "Tea", Qty: "1"}}})
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if first.DisplayNo == second.DisplayNo {
		t.Fatalf("two counter orders got the same display_no %q", first.DisplayNo)
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
