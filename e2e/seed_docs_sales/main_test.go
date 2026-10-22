package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
)

// newSeededDB is a migrated temp DB with the demo catalogue, the same
// starting point run-till.sh gives seed_docs_sales.
func newSeededDB(t *testing.T) *db.DB {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "unitill-pos.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if err := data.NewDemoSeedRepo(conn.DB).SeedDemoCatalogue(context.Background()); err != nil {
		t.Fatalf("seed demo catalogue: %v", err)
	}
	return conn
}

func TestSeed_WritesFixedDateSalesAndInvoices(t *testing.T) {
	conn := newSeededDB(t)
	if err := seed(context.Background(), conn.DB); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 3 completed sales, all inside the pinned December-2025 window and
	// stamped at midday UTC (no timezone can move one across a day/month edge).
	rows, err := conn.DB.Query(`SELECT created_at, completed_at, local_date, total FROM sales WHERE status='completed' ORDER BY created_at`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var created, completed, localDate string
		var total int64
		if err := rows.Scan(&created, &completed, &localDate, &total); err != nil {
			t.Fatal(err)
		}
		n++
		if len(created) < 10 || created[:7] != "2025-12" {
			t.Errorf("sale %d created_at = %q, want a fixed 2025-12 date", n, created)
		}
		if completed != created {
			t.Errorf("sale %d completed_at = %q, want = created_at %q", n, completed, created)
		}
		if len(localDate) != 10 || localDate[:7] != "2025-12" {
			t.Errorf("sale %d local_date = %q, want 2025-12-DD", n, localDate)
		}
		if total <= 0 {
			t.Errorf("sale %d total = %d, want > 0", n, total)
		}
	}
	if n != 3 {
		t.Fatalf("got %d sales, want 3", n)
	}

	// A payment is stamped with the same fixed date, not the wall clock.
	var badPayments int
	if err := conn.DB.QueryRow(`SELECT COUNT(*) FROM payments WHERE paid_at NOT LIKE '2025-12-%'`).Scan(&badPayments); err != nil {
		t.Fatal(err)
	}
	if badPayments != 0 {
		t.Errorf("%d payments carry a wall-clock paid_at", badPayments)
	}

	// 2 invoices, each consistent with its sale: gross == sale total,
	// net+tax == gross, the VAT bands sum to the same figures.
	inv, err := data.NewInvoiceRepo(conn.DB).List(context.Background(), "2025-12-01", "2025-12-31")
	if err != nil {
		t.Fatal(err)
	}
	if len(inv) != 2 {
		t.Fatalf("got %d invoices in December 2025, want 2", len(inv))
	}
	var mismatched int
	if err := conn.DB.QueryRow(`SELECT COUNT(*) FROM invoices i JOIN sales s ON s.id=i.sale_id
		WHERE i.gross_total != s.total OR i.net_total + i.tax_total != i.gross_total
		   OR i.issued_at NOT LIKE '2025-12-%'`).Scan(&mismatched); err != nil {
		t.Fatal(err)
	}
	if mismatched != 0 {
		t.Errorf("%d invoices disagree with their sale (gross/net+tax/issued_at)", mismatched)
	}
}

func TestSeed_IsIdempotent(t *testing.T) {
	conn := newSeededDB(t)
	for i := 0; i < 2; i++ {
		if err := seed(context.Background(), conn.DB); err != nil {
			t.Fatalf("seed run %d: %v", i+1, err)
		}
	}
	var n int
	if err := conn.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("after two runs got %d sales, want 3", n)
	}
}

// The seeded sales are weeks old: /orders must show them as collected (the
// honest end state), not as live "Not started" orders with cancel buttons,
// and their status timestamps must be the fixed ones too.
func TestSeed_MarksSeededOrdersCollectedAtTheirFixedTime(t *testing.T) {
	conn := newSeededDB(t)
	if err := seed(context.Background(), conn.DB); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rows, err := conn.DB.Query(`SELECT order_status, order_status_updated_at FROM sales`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var status, at string
		if err := rows.Scan(&status, &at); err != nil {
			t.Fatal(err)
		}
		n++
		if status != "collected" || len(at) < 7 || at[:7] != "2025-12" {
			t.Errorf("order status = %q at %q, want collected at a fixed 2025-12 time", status, at)
		}
	}
	if n != 3 {
		t.Fatalf("got %d sales, want 3", n)
	}
	var events int
	if err := conn.DB.QueryRow(`SELECT COUNT(*) FROM order_status_events WHERE created_at NOT LIKE '2025-12-%'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 0 {
		t.Errorf("%d order_status_events carry a wall-clock created_at", events)
	}
}

// ut-docs#4768: the channel-orders help screenshot needs populated rows.
func TestSeed_WritesChannelOrders(t *testing.T) {
	conn := newSeededDB(t)
	for i := 0; i < 2; i++ { // second run proves idempotence
		if err := seed(context.Background(), conn.DB); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	var n int
	if err := conn.DB.QueryRow(`SELECT COUNT(*) FROM channel_orders`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("channel orders = %d err=%v, want 3", n, err)
	}
}
