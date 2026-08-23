package data

import (
	"context"
	"testing"
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
