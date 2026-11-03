package data

import (
	"context"
	"testing"
	"time"
)

// ut-docs#5127 UT-1b: a till with sales or closes never re-pairs as a new
// device behind its history's back, and a moved till re-sends its daily
// rollups to the new shop.

func TestHasTradingHistory(t *testing.T) {
	ctx := context.Background()
	t.Run("empty", func(t *testing.T) {
		d := b8OpenDB(t, "history-empty.db")
		if has, err := NewPOSRepo(d.DB).HasTradingHistory(ctx); err != nil || has {
			t.Fatalf("HasTradingHistory = %v, %v; want false", has, err)
		}
	})
	t.Run("a sale", func(t *testing.T) {
		d := b8OpenDB(t, "history-sale.db")
		b8Sale(t, d, "s1", b8At(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)), "completed", "sale", 0, 100)
		if has, err := NewPOSRepo(d.DB).HasTradingHistory(ctx); err != nil || !has {
			t.Fatalf("HasTradingHistory = %v, %v; want true", has, err)
		}
	})
	t.Run("a close only", func(t *testing.T) {
		d := b8OpenDB(t, "history-close.db")
		mustExec(t, d, `INSERT INTO report_archive (id, kind, period, content_json, created_at) VALUES ('z1', 'Z', '2026-10-01', '{}', '2026-10-01T23:00:00Z')`)
		if has, err := NewPOSRepo(d.DB).HasTradingHistory(ctx); err != nil || !has {
			t.Fatalf("HasTradingHistory = %v, %v; want true", has, err)
		}
	})
}

func TestResetSalesAggregateUploads(t *testing.T) {
	d := b8OpenDB(t, "uploads-reset.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	at := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	for _, day := range []string{"2026-09-23", "2026-09-24"} {
		if err := repo.RecordSalesAggregateUpload(ctx, day, "reg-A", "h", "", at); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.ResetSalesAggregateUploads(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	for _, day := range []string{"2026-09-23", "2026-09-24"} {
		if _, ok, _ := repo.SalesAggregateUploadHash(ctx, day, "reg-A"); ok {
			t.Fatalf("%s survived the reset", day)
		}
	}
}
