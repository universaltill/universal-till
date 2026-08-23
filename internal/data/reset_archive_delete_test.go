package data_test

// DeleteResetBatch (ut-docs#661): the ADR-0042 §3 permanent-purge path,
// gated on the shop's active country's archive_min_days retention window
// (ut-docs#659). These tests pin the three things a UI can't be trusted to
// enforce on its own: the window itself, the boundary either side of it,
// and that a batch with no trading history at all isn't held hostage by a
// window meant to protect trading history.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// setBatchCreatedAt backdates a reset_batches row directly -- reset always
// stamps "now", so boundary tests need to move the clock on the row rather
// than on the machine.
func setBatchCreatedAt(t *testing.T, x func(q string, args ...any), batchID string, at time.Time) {
	t.Helper()
	x(`UPDATE reset_batches SET created_at = ? WHERE id = ?`, at.UTC().Format(time.RFC3339), batchID)
}

func TestDeleteResetBatch_NoTradingHistory_DeletesRegardlessOfWindow(t *testing.T) {
	d, x, count := resetTestDB(t, "purge_no_history.db")
	// No sale seeded at all -- reset still archives whatever else is live
	// (a shift here) and always writes a reset_batches header, with
	// sales_count = 0.
	x(`INSERT INTO registers (id, name) VALUES ('r1','Front Till')`)
	x(`INSERT INTO users (id, username, display_name, role) VALUES ('u1','cashier1','Cashier One','cashier')`)
	x(`INSERT INTO shifts (id, register_id, cashier_id, opening_cash) VALUES ('sh1','r1','u1',5000)`)

	repo := data.NewPOSRepo(d.DB)
	n, batchID, err := repo.ResetTransactionHistory(context.Background(), "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if n != 0 {
		t.Fatalf("sales archived = %d, want 0", n)
	}

	// created_at is "now" -- well inside any real retention window -- and
	// the purge must still succeed because there is no trading history to
	// protect.
	if err := repo.DeleteResetBatch(context.Background(), batchID, ""); err != nil {
		t.Fatalf("delete batch with zero sales must not be gated by retention: %v", err)
	}
	if c := count("reset_batches"); c != 0 {
		t.Fatalf("reset_batches should be empty after purge, got %d", c)
	}
	if c := count("shifts_archive"); c != 0 {
		t.Fatalf("shifts_archive should be empty after purge, got %d", c)
	}
}

func TestDeleteResetBatch_WithinRetentionWindow_Refused(t *testing.T) {
	d, x, count := resetTestDB(t, "purge_within_window.db")
	seedFullSale(t, x)

	repo := data.NewPOSRepo(d.DB)
	_, batchID, err := repo.ResetTransactionHistory(context.Background(), "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	// No country configured -- must fall back to the global floor
	// (GlobalArchiveMinDays), not "no restriction". created_at is "now", so
	// the batch is fully inside any positive window.
	err = repo.DeleteResetBatch(context.Background(), batchID, "")
	var within *data.ArchiveWithinRetentionWindowError
	if !errors.As(err, &within) {
		t.Fatalf("delete within window: got %v, want *ArchiveWithinRetentionWindowError", err)
	}
	if !within.RetainedUntil.After(time.Now().UTC()) {
		t.Fatalf("RetainedUntil = %v, want a time in the future", within.RetainedUntil)
	}
	if c := count("reset_batches"); c != 1 {
		t.Fatalf("reset_batches must keep the refused batch, got %d", c)
	}
	if c := count("sales_archive"); c != 2 {
		t.Fatalf("sales_archive must be untouched by a refused purge, got %d", c)
	}
}

func TestDeleteResetBatch_OutsideRetentionWindow_Deletes(t *testing.T) {
	d, x, count := resetTestDB(t, "purge_outside_window.db")
	seedFullSale(t, x)

	repo := data.NewPOSRepo(d.DB)
	_, batchID, err := repo.ResetTransactionHistory(context.Background(), "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	setBatchCreatedAt(t, x, batchID, time.Now().AddDate(0, 0, -int(data.GlobalArchiveMinDays)-1))

	if err := repo.DeleteResetBatch(context.Background(), batchID, ""); err != nil {
		t.Fatalf("delete outside window: %v", err)
	}
	if c := count("reset_batches"); c != 0 {
		t.Fatalf("reset_batches should be empty after purge, got %d", c)
	}
	for _, tbl := range resetTables {
		if c := count(tbl + "_archive"); c != 0 {
			t.Fatalf("%s_archive should be empty after purge, got %d", tbl, c)
		}
	}
}

func TestDeleteResetBatch_RetainedUntilDateIsPurgeableFromMidnight(t *testing.T) {
	d, x, count := resetTestDB(t, "purge_midnight.db")
	// GB is seeded builtin by migration 041; override its retention to a
	// small, easy-to-straddle value directly (same pattern as
	// TestDeleteResetBatch_BoundaryEitherSideOfCountryWindow).
	x(`UPDATE country_settings SET archive_min_days = 5 WHERE code = 'GB'`)
	x(`INSERT INTO settings (key, value) VALUES ('store.country', 'GB')`)
	seedFullSale(t, x)

	repo := data.NewPOSRepo(d.DB)
	_, batchID, err := repo.ResetTransactionHistory(context.Background(), "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}

	// Archive it exactly 5 days before today's date, but at a time-of-day a
	// couple of seconds *later* than right now -- so the retained-until
	// date is today, yet we're attempting the purge before the original
	// archive's time-of-day is reached. ut-docs#699: the named (date-only)
	// retained-until must already be purgeable at this moment, not only
	// from that time-of-day onward.
	//
	// Clamp (never wrap) the +2s offset to stay within "now"'s own
	// calendar day: a plain time.Time.Add that crosses midnight would move
	// the wrapped H/M/S *behind* the real now's time-of-day, which
	// (independent review, ut-docs#699) would make even the pre-fix buggy
	// arithmetic pass -- silently losing this test's regression-catching
	// power in the last ~2 seconds of any UTC day.
	now := time.Now().UTC()
	laterHour, laterMin, laterSec := now.Hour(), now.Minute(), now.Second()+2
	if laterSec > 59 {
		laterSec, laterMin = laterSec-60, laterMin+1
	}
	if laterMin > 59 {
		laterMin, laterHour = laterMin-60, laterHour+1
	}
	if laterHour > 23 {
		laterHour, laterMin, laterSec = 23, 59, 59
	}
	shiftedDate := now.AddDate(0, 0, -5)
	archivedAt := time.Date(shiftedDate.Year(), shiftedDate.Month(), shiftedDate.Day(),
		laterHour, laterMin, laterSec, 0, time.UTC)
	setBatchCreatedAt(t, x, batchID, archivedAt)

	if err := repo.DeleteResetBatch(context.Background(), batchID, ""); err != nil {
		t.Fatalf("purge on the retained-until date itself, before the original time-of-day: got %v, want success", err)
	}
	if c := count("reset_batches"); c != 0 {
		t.Fatalf("reset_batches should be empty after purge, got %d", c)
	}
}

func TestDeleteResetBatch_BoundaryEitherSideOfCountryWindow(t *testing.T) {
	d, x, count := resetTestDB(t, "purge_boundary.db")
	// GB is seeded builtin by migration 041; override its retention to a
	// small, easy-to-straddle value directly (bypassing the repo's Upsert
	// floor, same as other tests in this package seed rows directly) so the
	// boundary itself, not the 10-year floor, is what the test exercises.
	x(`UPDATE country_settings SET archive_min_days = 5 WHERE code = 'GB'`)
	x(`INSERT INTO settings (key, value) VALUES ('store.country', 'GB')`)
	seedFullSale(t, x)

	repo := data.NewPOSRepo(d.DB)
	_, batchID, err := repo.ResetTransactionHistory(context.Background(), "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}

	// Archived 4 days ago: 1 day still inside the 5-day window -- refused.
	setBatchCreatedAt(t, x, batchID, time.Now().AddDate(0, 0, -4))
	var within *data.ArchiveWithinRetentionWindowError
	if err := repo.DeleteResetBatch(context.Background(), batchID, ""); !errors.As(err, &within) {
		t.Fatalf("delete 1 day inside window: got %v, want *ArchiveWithinRetentionWindowError", err)
	}
	if c := count("reset_batches"); c != 1 {
		t.Fatalf("reset_batches must survive a refused purge, got %d", c)
	}

	// Archived 6 days ago: 1 day past the 5-day window -- deletes.
	setBatchCreatedAt(t, x, batchID, time.Now().AddDate(0, 0, -6))
	if err := repo.DeleteResetBatch(context.Background(), batchID, ""); err != nil {
		t.Fatalf("delete 1 day past window: %v", err)
	}
	if c := count("reset_batches"); c != 0 {
		t.Fatalf("reset_batches should be empty after purge, got %d", c)
	}
}

func TestDeleteResetBatch_UnknownBatch(t *testing.T) {
	d, _, _ := resetTestDB(t, "purge_unknown.db")
	repo := data.NewPOSRepo(d.DB)
	err := repo.DeleteResetBatch(context.Background(), "does-not-exist", "")
	if !errors.Is(err, data.ErrResetBatchNotFound) {
		t.Fatalf("delete unknown batch: got %v, want ErrResetBatchNotFound", err)
	}
}

func TestDeleteResetBatch_UnknownCountryFallsBackToGlobalFloor(t *testing.T) {
	d, x, count := resetTestDB(t, "purge_unknown_country.db")
	// A country code with no country_settings row at all (never onboarded,
	// or a custom code that was since deleted) must fall back to the global
	// floor, same as "no country configured" -- never "no restriction".
	x(`INSERT INTO settings (key, value) VALUES ('store.country', 'ZZ')`)
	seedFullSale(t, x)

	repo := data.NewPOSRepo(d.DB)
	_, batchID, err := repo.ResetTransactionHistory(context.Background(), "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	var within *data.ArchiveWithinRetentionWindowError
	if err := repo.DeleteResetBatch(context.Background(), batchID, ""); !errors.As(err, &within) {
		t.Fatalf("delete with unknown country code: got %v, want *ArchiveWithinRetentionWindowError", err)
	}
	if c := count("reset_batches"); c != 1 {
		t.Fatalf("reset_batches must survive a refused purge, got %d", c)
	}
}

// ut-docs#698: ListResetBatches must compute the same purge eligibility
// DeleteResetBatch itself gates on, read-only, so the Settings → Data UI can
// show it BEFORE an operator goes through the confirm-and-fail motion. These
// tests pin the same three cases the DeleteResetBatch tests above pin for the
// enforcement path, plus the same #699 midnight boundary — the two paths
// must never disagree.

func TestListResetBatches_ZeroSalesBatch_AlwaysPurgeable(t *testing.T) {
	d, x, _ := resetTestDB(t, "list_no_history.db")
	x(`INSERT INTO registers (id, name) VALUES ('r1','Front Till')`)
	x(`INSERT INTO users (id, username, display_name, role) VALUES ('u1','cashier1','Cashier One','cashier')`)
	x(`INSERT INTO shifts (id, register_id, cashier_id, opening_cash) VALUES ('sh1','r1','u1',5000)`)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	_, batchID, err := repo.ResetTransactionHistory(ctx, "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}

	batches, err := repo.ListResetBatches(ctx)
	if err != nil {
		t.Fatalf("list batches: %v", err)
	}
	if len(batches) != 1 || batches[0].ID != batchID {
		t.Fatalf("ListResetBatches = %+v, want one batch %s", batches, batchID)
	}
	if !batches[0].Purgeable {
		t.Fatalf("zero-sales batch must always be Purgeable, got %+v", batches[0])
	}
	if !batches[0].RetainedUntil.IsZero() {
		t.Fatalf("zero-sales batch has no retention gate, want zero RetainedUntil, got %v", batches[0].RetainedUntil)
	}
}

func TestListResetBatches_WithinRetentionWindow_NotPurgeable(t *testing.T) {
	d, x, _ := resetTestDB(t, "list_within_window.db")
	seedFullSale(t, x)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	// created_at is "now" -- fully inside the global floor window.
	_, batchID, err := repo.ResetTransactionHistory(ctx, "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}

	batches, err := repo.ListResetBatches(ctx)
	if err != nil {
		t.Fatalf("list batches: %v", err)
	}
	if len(batches) != 1 || batches[0].ID != batchID {
		t.Fatalf("ListResetBatches = %+v, want one batch %s", batches, batchID)
	}
	if batches[0].Purgeable {
		t.Fatalf("batch within its retention window must not be Purgeable, got %+v", batches[0])
	}
	if !batches[0].RetainedUntil.After(time.Now().UTC()) {
		t.Fatalf("RetainedUntil = %v, want a time in the future", batches[0].RetainedUntil)
	}
}

func TestListResetBatches_OutsideRetentionWindow_Purgeable(t *testing.T) {
	d, x, _ := resetTestDB(t, "list_outside_window.db")
	seedFullSale(t, x)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	_, batchID, err := repo.ResetTransactionHistory(ctx, "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	setBatchCreatedAt(t, x, batchID, time.Now().AddDate(0, 0, -int(data.GlobalArchiveMinDays)-1))

	batches, err := repo.ListResetBatches(ctx)
	if err != nil {
		t.Fatalf("list batches: %v", err)
	}
	if len(batches) != 1 || batches[0].ID != batchID {
		t.Fatalf("ListResetBatches = %+v, want one batch %s", batches, batchID)
	}
	if !batches[0].Purgeable {
		t.Fatalf("batch past its retention window must be Purgeable, got %+v", batches[0])
	}
	if batches[0].RetainedUntil.IsZero() {
		t.Fatalf("a gated batch must still report the date it became purgeable, got zero RetainedUntil")
	}
}

// Mirrors TestDeleteResetBatch_RetainedUntilDateIsPurgeableFromMidnight
// (ut-docs#699): the read path must land on the exact same purgeable-from-
// midnight semantics as the enforcement path, not a subtly different one.
func TestListResetBatches_BoundaryMatchesDeleteResetBatchFromMidnight(t *testing.T) {
	d, x, _ := resetTestDB(t, "list_midnight.db")
	x(`UPDATE country_settings SET archive_min_days = 5 WHERE code = 'GB'`)
	x(`INSERT INTO settings (key, value) VALUES ('store.country', 'GB')`)
	seedFullSale(t, x)

	repo := data.NewPOSRepo(d.DB)
	ctx := context.Background()
	_, batchID, err := repo.ResetTransactionHistory(ctx, "")
	if err != nil {
		t.Fatalf("reset: %v", err)
	}

	// Same clamped "5 days ago, a couple seconds later in the day than now"
	// shape as the DeleteResetBatch #699 test -- the retained-until DATE is
	// today, even though the archive's own time-of-day hasn't been reached
	// yet.
	now := time.Now().UTC()
	laterHour, laterMin, laterSec := now.Hour(), now.Minute(), now.Second()+2
	if laterSec > 59 {
		laterSec, laterMin = laterSec-60, laterMin+1
	}
	if laterMin > 59 {
		laterMin, laterHour = laterMin-60, laterHour+1
	}
	if laterHour > 23 {
		laterHour, laterMin, laterSec = 23, 59, 59
	}
	shiftedDate := now.AddDate(0, 0, -5)
	archivedAt := time.Date(shiftedDate.Year(), shiftedDate.Month(), shiftedDate.Day(),
		laterHour, laterMin, laterSec, 0, time.UTC)
	setBatchCreatedAt(t, x, batchID, archivedAt)

	batches, err := repo.ListResetBatches(ctx)
	if err != nil {
		t.Fatalf("list batches: %v", err)
	}
	if len(batches) != 1 || batches[0].ID != batchID {
		t.Fatalf("ListResetBatches = %+v, want one batch %s", batches, batchID)
	}
	if !batches[0].Purgeable {
		t.Fatalf("retained-until date reached (even before the archive's own time-of-day) must be Purgeable, got %+v", batches[0])
	}

	// And DeleteResetBatch itself must agree -- the two paths must never
	// disagree on the same batch.
	if err := repo.DeleteResetBatch(ctx, batchID, ""); err != nil {
		t.Fatalf("DeleteResetBatch disagreed with ListResetBatches' Purgeable=true: %v", err)
	}
}
