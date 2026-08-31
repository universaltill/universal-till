package data

import (
	"context"
	"testing"
	"time"
)

// Customer order tracking tokens (ut-docs#527). Harness matches
// order_status_repo_test.go: real SQLite in a TempDir, real migrations, no
// os.Chdir.

// EnsureOrderTrackingToken must mint a hex token, persist it on the sale,
// and hand back the SAME token on every later call for that receipt — the
// confirmation screen may re-render, and a second QR pointing at a second
// URL for the same order would be a bug.
func TestEnsureOrderTrackingToken_PersistsAndIsIdempotent(t *testing.T) {
	d := openOrderStatusDB(t, "tracking_token_idempotent.db")
	seedOrderStatusSale(t, d, "sale-1", "R-0001")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	tok, err := repo.EnsureOrderTrackingToken(ctx, "R-0001")
	if err != nil {
		t.Fatalf("EnsureOrderTrackingToken: %v", err)
	}
	// 16 bytes of crypto/rand, hex-encoded — same convention as the sync
	// enrolment tokens (sync_api.go enrolTokens.issue).
	if len(tok) != 32 {
		t.Fatalf("token %q: want 32 hex chars (16 random bytes)", tok)
	}
	for _, c := range tok {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Fatalf("token %q is not lowercase hex", tok)
		}
	}

	var stored string
	if err := d.DB.QueryRow(`SELECT tracking_token FROM sales WHERE receipt_no='R-0001'`).Scan(&stored); err != nil {
		t.Fatalf("read back tracking_token: %v", err)
	}
	if stored != tok {
		t.Fatalf("persisted token %q != returned token %q", stored, tok)
	}

	again, err := repo.EnsureOrderTrackingToken(ctx, "R-0001")
	if err != nil {
		t.Fatalf("second EnsureOrderTrackingToken: %v", err)
	}
	if again != tok {
		t.Fatalf("second call minted a NEW token %q, want the existing %q", again, tok)
	}
}

func TestEnsureOrderTrackingToken_DistinctPerReceipt(t *testing.T) {
	d := openOrderStatusDB(t, "tracking_token_distinct.db")
	seedOrderStatusSale(t, d, "sale-1", "R-0001")
	seedOrderStatusSale(t, d, "sale-2", "R-0002")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	tok1, err := repo.EnsureOrderTrackingToken(ctx, "R-0001")
	if err != nil {
		t.Fatalf("EnsureOrderTrackingToken R-0001: %v", err)
	}
	tok2, err := repo.EnsureOrderTrackingToken(ctx, "R-0002")
	if err != nil {
		t.Fatalf("EnsureOrderTrackingToken R-0002: %v", err)
	}
	if tok1 == tok2 {
		t.Fatalf("two receipts share the token %q — must be unique per sale", tok1)
	}
}

// A receipt that does not exist is an error (unlike lookup-by-token below):
// checkout only calls this immediately after committing the sale, so a
// missing row means the caller is wired wrong, not a customer typo.
func TestEnsureOrderTrackingToken_UnknownReceiptErrors(t *testing.T) {
	d := openOrderStatusDB(t, "tracking_token_unknown.db")
	repo := NewPOSRepo(d.DB)
	if _, err := repo.EnsureOrderTrackingToken(context.Background(), "R-NOPE"); err == nil {
		t.Fatal("want an error for an unknown receipt, got nil")
	}
}

// ListLiveTrackedOrders feeds the cloud relay push (ADR-0070): every sale
// holding a tracking token, filtered through the caller's visibility rule
// (a callback, per order_status_repo.go's header — internal/pos imports
// internal/data, so the rule can't be imported here directly). Tokenless
// sales never appear; the callback decides liveness; ordering is stable so
// the push's hash gate doesn't see phantom changes. This test passes a nil
// terminalStatuses throughout — the pre-#1321 unbounded query — so it's
// purely exercising the callback wiring; TestListLiveTrackedOrders_
// PrunesOldTerminalRowsInSQL below covers the terminalStatuses/
// terminalCutoff SQL-side prune itself.
func TestListLiveTrackedOrders(t *testing.T) {
	d := openOrderStatusDB(t, "tracking_list_live.db")
	seedOrderStatusSale(t, d, "sale-1", "R-0001")
	seedOrderStatusSale(t, d, "sale-2", "R-0002")
	seedOrderStatusSale(t, d, "sale-3", "R-0003") // never gets a token
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	tok1, err := repo.EnsureOrderTrackingToken(ctx, "R-0001")
	if err != nil {
		t.Fatalf("token R-0001: %v", err)
	}
	tok2, err := repo.EnsureOrderTrackingToken(ctx, "R-0002")
	if err != nil {
		t.Fatalf("token R-0002: %v", err)
	}
	if applied, _, err := repo.ApplyOrderStatus(ctx, "R-0001", "preparing", "u-alice", "2026-08-28T10:00:00Z",
		func(string) bool { return true }); err != nil || !applied {
		t.Fatalf("seed status R-0001: applied=%v err=%v", applied, err)
	}
	if applied, _, err := repo.ApplyOrderStatus(ctx, "R-0002", "collected", "u-alice", "2026-08-28T10:05:00Z",
		func(string) bool { return true }); err != nil || !applied {
		t.Fatalf("seed status R-0002: applied=%v err=%v", applied, err)
	}

	// Callback sees exactly the tokened sales' status views.
	var seen []TrackedOrder
	all, err := repo.ListLiveTrackedOrders(ctx, nil, time.Time{}, func(o TrackedOrder) bool {
		seen = append(seen, o)
		return true
	})
	if err != nil {
		t.Fatalf("ListLiveTrackedOrders: %v", err)
	}
	if len(all) != 2 || len(seen) != 2 {
		t.Fatalf("got %d rows (%d callback calls), want 2 — tokenless R-0003 must not appear: %+v", len(all), len(seen), all)
	}
	byToken := map[string]LiveTrackedOrder{}
	for _, o := range all {
		byToken[o.Token] = o
	}
	if o := byToken[tok1]; o.ReceiptNo != "R-0001" || o.Status != "preparing" || o.StatusUpdatedAt != "2026-08-28T10:00:00Z" {
		t.Fatalf("R-0001 row = %+v", o)
	}
	if o := byToken[tok2]; o.ReceiptNo != "R-0002" || o.Status != "collected" {
		t.Fatalf("R-0002 row = %+v", o)
	}

	// The callback's verdict is honored: filtering out terminal statuses
	// drops R-0002 from the result.
	live, err := repo.ListLiveTrackedOrders(ctx, nil, time.Time{}, func(o TrackedOrder) bool {
		return o.Status != "collected" && o.Status != "cancelled"
	})
	if err != nil {
		t.Fatalf("ListLiveTrackedOrders filtered: %v", err)
	}
	if len(live) != 1 || live[0].Token != tok1 {
		t.Fatalf("filtered rows = %+v, want only R-0001's token", live)
	}

	// Nothing visible → empty, no error (the push's delete signal).
	none, err := repo.ListLiveTrackedOrders(ctx, nil, time.Time{}, func(TrackedOrder) bool { return false })
	if err != nil {
		t.Fatalf("ListLiveTrackedOrders none: %v", err)
	}
	if len(none) != 0 {
		t.Fatalf("want no rows, got %+v", none)
	}
}

// ut-docs#1321: terminalStatuses + terminalCutoff let SQL prune old terminal
// rows before they ever reach Go — but ONLY terminal ones. A non-terminal
// row must reach the callback (and this test's own visible-tracking
// callback) no matter how old, exactly matching pos.OrderTrackingVisible's
// "preparing is live regardless of timestamp" rule (order_tracking_test.go)
// — this is the SQL-side prune's own correctness proof: it must never
// disagree with what the callback would have decided.
func TestListLiveTrackedOrders_PrunesOldTerminalRowsInSQL(t *testing.T) {
	d := openOrderStatusDB(t, "tracking_list_live_prune.db")
	seedOrderStatusSale(t, d, "sale-1", "R-OLD-PREPARING")
	seedOrderStatusSale(t, d, "sale-2", "R-OLD-COLLECTED")
	seedOrderStatusSale(t, d, "sale-3", "R-RECENT-COLLECTED")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	for _, r := range []string{"R-OLD-PREPARING", "R-OLD-COLLECTED", "R-RECENT-COLLECTED"} {
		if _, err := repo.EnsureOrderTrackingToken(ctx, r); err != nil {
			t.Fatalf("token %s: %v", r, err)
		}
	}
	// A "preparing" order from 2020 — ancient by any cutoff, but non-terminal
	// so it must never be pruned by terminalCutoff.
	if applied, _, err := repo.ApplyOrderStatus(ctx, "R-OLD-PREPARING", "preparing", "u-alice",
		"2020-01-01T00:00:00Z", func(string) bool { return true }); err != nil || !applied {
		t.Fatalf("seed R-OLD-PREPARING: applied=%v err=%v", applied, err)
	}
	// A "collected" order from 2020 — terminal AND outside the cutoff below:
	// SQL must prune this one before the callback ever sees it.
	if applied, _, err := repo.ApplyOrderStatus(ctx, "R-OLD-COLLECTED", "collected", "u-alice",
		"2020-01-01T00:00:00Z", func(string) bool { return true }); err != nil || !applied {
		t.Fatalf("seed R-OLD-COLLECTED: applied=%v err=%v", applied, err)
	}
	// A "collected" order updated just now — terminal, but inside the
	// cutoff, so it must still reach the callback.
	recentAt := time.Now().UTC().Format(time.RFC3339)
	if applied, _, err := repo.ApplyOrderStatus(ctx, "R-RECENT-COLLECTED", "collected", "u-alice",
		recentAt, func(string) bool { return true }); err != nil || !applied {
		t.Fatalf("seed R-RECENT-COLLECTED: applied=%v err=%v", applied, err)
	}

	var seenReceipts []string
	cutoff := time.Now().UTC().Add(-2 * time.Hour) // mirrors pos.OrderTrackingExpiry
	all, err := repo.ListLiveTrackedOrders(ctx, []string{"collected", "cancelled"}, cutoff,
		func(o TrackedOrder) bool {
			seenReceipts = append(seenReceipts, o.ReceiptNo)
			return true // callback would accept everything it's shown
		})
	if err != nil {
		t.Fatalf("ListLiveTrackedOrders: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d rows, want 2 (R-OLD-PREPARING never bounded, R-OLD-COLLECTED SQL-pruned, R-RECENT-COLLECTED inside cutoff): %+v", len(all), all)
	}
	got := map[string]bool{}
	for _, o := range all {
		got[o.ReceiptNo] = true
	}
	if !got["R-OLD-PREPARING"] {
		t.Fatalf("R-OLD-PREPARING (non-terminal, ancient) must never be SQL-pruned — rows: %+v", all)
	}
	if !got["R-RECENT-COLLECTED"] {
		t.Fatalf("R-RECENT-COLLECTED (terminal, inside cutoff) must survive the prune — rows: %+v", all)
	}
	if got["R-OLD-COLLECTED"] {
		t.Fatalf("R-OLD-COLLECTED (terminal, outside cutoff) must be SQL-pruned before reaching the callback — rows: %+v", all)
	}
	if len(seenReceipts) != 2 {
		t.Fatalf("callback ran %d times, want 2 — the pruned row must never reach it: %v", len(seenReceipts), seenReceipts)
	}

	// Empty terminalStatuses fails OPEN (pre-#1321 unbounded query, not a
	// silently-empty result) — a caller with no terminal set yet still gets
	// every tokened row, cutoff ignored.
	unbounded, err := repo.ListLiveTrackedOrders(ctx, nil, cutoff, func(TrackedOrder) bool { return true })
	if err != nil {
		t.Fatalf("ListLiveTrackedOrders unbounded: %v", err)
	}
	if len(unbounded) != 3 {
		t.Fatalf("got %d rows with nil terminalStatuses, want all 3 (fails open): %+v", len(unbounded), unbounded)
	}
}

// Lookup by a valid token returns status-only order data; an unknown or
// empty token returns found=false with NO error — a guessed token must get
// the exact same not-found response as a malformed one, never a
// distinguishable failure (anonymous, unauthenticated surface).
func TestLookupOrderByTrackingToken(t *testing.T) {
	d := openOrderStatusDB(t, "tracking_token_lookup.db")
	seedOrderStatusSale(t, d, "sale-1", "R-0001")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	tok, err := repo.EnsureOrderTrackingToken(ctx, "R-0001")
	if err != nil {
		t.Fatalf("EnsureOrderTrackingToken: %v", err)
	}
	if applied, _, err := repo.ApplyOrderStatus(ctx, "R-0001", "preparing", "u-alice", "2026-08-23T10:00:00Z",
		func(string) bool { return true }); err != nil || !applied {
		t.Fatalf("seed status: applied=%v err=%v", applied, err)
	}

	o, found, err := repo.LookupOrderByTrackingToken(ctx, tok)
	if err != nil {
		t.Fatalf("LookupOrderByTrackingToken: %v", err)
	}
	if !found {
		t.Fatal("want found=true for a valid token")
	}
	if o.ReceiptNo != "R-0001" || o.Status != "preparing" || o.StatusUpdatedAt != "2026-08-23T10:00:00Z" || o.CreatedAt == "" {
		t.Fatalf("tracked order = %+v, want R-0001/preparing/@10:00 with a created_at", o)
	}

	for _, bad := range []string{"", "deadbeefdeadbeefdeadbeefdeadbeef", "not-a-token"} {
		o, found, err := repo.LookupOrderByTrackingToken(ctx, bad)
		if err != nil {
			t.Fatalf("token %q: want no error, got %v", bad, err)
		}
		if found {
			t.Fatalf("token %q: want found=false, got %+v", bad, o)
		}
	}
}
