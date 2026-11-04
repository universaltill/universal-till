package data_test

// CheckRestorable / CheckPurgeable (ut-docs#5445): the read-only preflight of
// RestoreResetBatch / DeleteResetBatch, so the handler can answer 404/409
// synchronously before it starts the background job. The same predicates run
// again inside the job's transaction (authoritative).

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

func TestCheckRestorable_UnknownBatch(t *testing.T) {
	d, _, _ := resetTestDB(t, "preflight_restore_unknown.db")
	err := data.NewPOSRepo(d.DB).CheckRestorable(context.Background(), "nope")
	if !errors.Is(err, data.ErrResetBatchNotFound) {
		t.Fatalf("got %v, want ErrResetBatchNotFound", err)
	}
}

func TestCheckRestorable_TradedSinceReset(t *testing.T) {
	d, x, _ := resetTestDB(t, "preflight_restore_traded.db")
	seedFullSale(t, x)
	repo := data.NewPOSRepo(d.DB)
	_, batchID, err := repo.ResetTransactionHistory(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CheckRestorable(context.Background(), batchID); err != nil {
		t.Fatalf("fresh batch must be restorable, got %v", err)
	}
	x(`INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('post1','R9','completed','sale','GBP',50,0,0,50,datetime('now'))`)
	if err := repo.CheckRestorable(context.Background(), batchID); !errors.Is(err, data.ErrShopHasTradedSinceReset) {
		t.Fatalf("got %v, want ErrShopHasTradedSinceReset", err)
	}
}

func TestCheckPurgeable_UnknownAndWithinWindow(t *testing.T) {
	d, x, count := resetTestDB(t, "preflight_purge.db")
	seedFullSale(t, x)
	repo := data.NewPOSRepo(d.DB)
	if err := repo.CheckPurgeable(context.Background(), "nope"); !errors.Is(err, data.ErrResetBatchNotFound) {
		t.Fatalf("got %v, want ErrResetBatchNotFound", err)
	}
	_, batchID, err := repo.ResetTransactionHistory(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	var within *data.ArchiveWithinRetentionWindowError
	if err := repo.CheckPurgeable(context.Background(), batchID); !errors.As(err, &within) {
		t.Fatalf("got %v, want *ArchiveWithinRetentionWindowError", err)
	}
	if c := count("reset_batches"); c != 1 {
		t.Fatalf("preflight must not mutate; reset_batches=%d", c)
	}
}
