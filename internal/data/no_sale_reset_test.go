package data_test

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#2558: no_sale_events is transactional history, so "Clear
// transaction history" (ADR-0042) moves it into no_sale_events_archive
// (migration 064) like shrinkage_events (ut-docs#3452), and restore brings
// it back unchanged. Without this, a training-time "No sale" would survive
// go-live in the cloud rollup's no_sale_count.
func TestResetThenRestoreRoundTrip_NoSaleEvents(t *testing.T) {
	d, x, count := resetTestDB(t, "restore_no_sale_events.db")
	seedFullSale(t, x)
	x(`INSERT INTO no_sale_events (id, created_at, local_date, register_id, till_id, actor_id, approver_id, reason)
	   VALUES ('ns1','2026-01-01T00:00:00Z','2026-01-01','r1',NULL,'u1','m1','float'),
	          ('ns2','2026-01-01T00:00:01Z','2026-01-01',NULL,'replica-1',NULL,NULL,NULL)`)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	_, batchID, err := repo.ResetTransactionHistory(ctx, "", "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if c := count("no_sale_events"); c != 0 {
		t.Fatalf("no_sale_events not cleared by reset: %d row(s)", c)
	}
	if c := count("no_sale_events_archive"); c != 2 {
		t.Fatalf("no_sale_events_archive holds %d row(s), want 2", c)
	}

	// A no-sale recorded after the reset blocks restore (never a merge).
	x(`INSERT INTO no_sale_events (id, created_at, local_date) VALUES ('ns-after','2026-02-01T00:00:00Z','2026-02-01')`)
	if _, err := repo.RestoreResetBatch(ctx, batchID, "", ""); !errors.Is(err, data.ErrShopHasTradedSinceReset) {
		t.Fatalf("restore with a post-reset no-sale = %v, want ErrShopHasTradedSinceReset", err)
	}
	x(`DELETE FROM no_sale_events WHERE id = 'ns-after'`)

	if _, err := repo.RestoreResetBatch(ctx, batchID, "", ""); err != nil {
		t.Fatalf("restore: %v", err)
	}
	var got string
	if err := d.DB.QueryRow(`SELECT group_concat(id || '|' || created_at || '|' || local_date || '|' || COALESCE(register_id,'-') || '|' ||
	  COALESCE(till_id,'-') || '|' || COALESCE(actor_id,'-') || '|' || COALESCE(approver_id,'-') || '|' || COALESCE(reason,'-'), ';')
	  FROM (SELECT * FROM no_sale_events ORDER BY id)`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	const want = "ns1|2026-01-01T00:00:00Z|2026-01-01|r1|-|u1|m1|float;ns2|2026-01-01T00:00:01Z|2026-01-01|-|replica-1|-|-|-"
	if got != want {
		t.Fatalf("restored no_sale_events = %q, want %q", got, want)
	}
	if c := count("no_sale_events_archive"); c != 0 {
		t.Fatalf("no_sale_events_archive should be empty after restore, got %d", c)
	}
}
