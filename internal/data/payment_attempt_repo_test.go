package data

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ut-docs#6455 (ADR-0198 §2.3, §6.8): core's own record of a card-present
// attempt, written BEFORE the tap and surviving a restart.

func newAttemptTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	mig, err := os.ReadFile("../db/migrations/111_payment_attempts.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(string(mig)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return db
}

func baseAttempt(key string) PaymentAttempt {
	return PaymentAttempt{Key: key, Provider: "stripe", AmountMinor: 1250, Currency: "gbp", TenderRef: "tender-1", Deadline: time.Now().Add(2 * time.Minute)}
}

func TestPaymentAttempt_WrittenPendingBeforeTheTap(t *testing.T) {
	db := newAttemptTestDB(t, ":memory:")
	r := NewPaymentAttemptRepo(db)
	ctx := context.Background()
	got, err := r.Begin(ctx, baseAttempt("k1"))
	if err != nil {
		t.Fatal(err)
	}
	if got.State != AttemptPending || got.AmountMinor != 1250 || got.Currency != "gbp" || got.Provider != "stripe" {
		t.Fatalf("Begin = %+v", got)
	}
	read, ok, err := r.Get(ctx, "k1")
	if err != nil || !ok || read.State != AttemptPending || read.TenderRef != "tender-1" {
		t.Fatalf("Get = %+v %v %v", read, ok, err)
	}
	if _, ok, _ := r.Get(ctx, "nope"); ok {
		t.Fatal("Get found a row that was never written")
	}
}

func TestPaymentAttempt_SurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "till.db")
	db := newAttemptTestDB(t, path)
	if _, err := NewPaymentAttemptRepo(db).Begin(context.Background(), baseAttempt("k1")); err != nil {
		t.Fatal(err)
	}
	if err := NewPaymentAttemptRepo(db).Mark(context.Background(), "k1", AttemptUnknown, AttemptUpdate{Reason: "timeout"}); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db2, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db2.Close() })
	got, ok, err := NewPaymentAttemptRepo(db2).Get(context.Background(), "k1")
	if err != nil || !ok || got.State != AttemptUnknown || got.Reason != "timeout" {
		t.Fatalf("after reopen = %+v %v %v", got, ok, err)
	}
}

func TestPaymentAttempt_BeginValidatesAndIsIdempotent(t *testing.T) {
	r := NewPaymentAttemptRepo(newAttemptTestDB(t, ":memory:"))
	ctx := context.Background()
	for _, bad := range []PaymentAttempt{
		{Key: "", Provider: "stripe", AmountMinor: 1, Currency: "gbp"},
		{Key: "k", Provider: "", AmountMinor: 1, Currency: "gbp"},
		{Key: "k", Provider: "stripe", AmountMinor: 0, Currency: "gbp"},
		{Key: "k", Provider: "stripe", AmountMinor: -4, Currency: "gbp"},
		{Key: "k", Provider: "stripe", AmountMinor: 1, Currency: "pounds"},
	} {
		if _, err := r.Begin(ctx, bad); !errors.Is(err, ErrAttemptInvalid) {
			t.Fatalf("Begin(%+v) err = %v, want ErrAttemptInvalid", bad, err)
		}
	}
	a, err := r.Begin(ctx, baseAttempt("k1"))
	if err != nil {
		t.Fatal(err)
	}
	// The same in-flight attempt retried: the same row, still pending.
	b, err := r.Begin(ctx, baseAttempt("k1"))
	if err != nil || b.State != AttemptPending || !b.CreatedAt.Equal(a.CreatedAt) {
		t.Fatalf("retry = %+v %v", b, err)
	}
	// The key is bound to one amount: another amount under it is refused.
	other := baseAttempt("k1")
	other.AmountMinor = 1251
	if _, err := r.Begin(ctx, other); !errors.Is(err, ErrAttemptMismatch) {
		t.Fatalf("different amount, same key: err = %v, want ErrAttemptMismatch", err)
	}
	other = baseAttempt("k1")
	other.Currency = "eur"
	if _, err := r.Begin(ctx, other); !errors.Is(err, ErrAttemptMismatch) {
		t.Fatalf("different currency, same key: err = %v, want ErrAttemptMismatch", err)
	}
}

func TestPaymentAttempt_StateMachine(t *testing.T) {
	r := NewPaymentAttemptRepo(newAttemptTestDB(t, ":memory:"))
	ctx := context.Background()
	begin := func(k string) {
		t.Helper()
		if _, err := r.Begin(ctx, baseAttempt(k)); err != nil {
			t.Fatal(err)
		}
	}

	// pending -> approved records the PI and the brand/last4 from the broker.
	begin("a")
	if err := r.Mark(ctx, "a", AttemptApproved, AttemptUpdate{PIID: "pi_1", CardBrand: "visa", Last4: "4242"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := r.Get(ctx, "a")
	if got.State != AttemptApproved || got.PIID != "pi_1" || got.CardBrand != "visa" || got.Last4 != "4242" {
		t.Fatalf("approved = %+v", got)
	}
	// settled -> anything is refused, including a second approve.
	for _, to := range []string{AttemptApproved, AttemptDeclined, AttemptCancelled, AttemptUnknown, AttemptResolvedNotCharged} {
		if err := r.Mark(ctx, "a", to, AttemptUpdate{}); !errors.Is(err, ErrAttemptSettled) {
			t.Fatalf("approved -> %s: err = %v, want ErrAttemptSettled", to, err)
		}
	}
	// pending is entered by Begin only.
	if err := r.Mark(ctx, "a", AttemptPending, AttemptUpdate{}); !errors.Is(err, ErrAttemptInvalid) {
		t.Fatalf("Mark(pending): err = %v, want ErrAttemptInvalid", err)
	}
	// an unknown outcome may still be settled, either way.
	begin("u")
	if err := r.Mark(ctx, "u", AttemptUnknown, AttemptUpdate{Reason: "timeout"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Mark(ctx, "u", AttemptResolvedNotCharged, AttemptUpdate{Reason: "manager"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Mark(ctx, "u", AttemptApproved, AttemptUpdate{}); !errors.Is(err, ErrAttemptSettled) {
		t.Fatalf("resolved_not_charged -> approved: err = %v", err)
	}
	begin("v")
	_ = r.Mark(ctx, "v", AttemptUnknown, AttemptUpdate{})
	if err := r.Mark(ctx, "v", AttemptApproved, AttemptUpdate{PIID: "pi_2"}); err != nil {
		t.Fatalf("unknown -> approved: %v", err)
	}
	// not a state, and not a row
	if err := r.Mark(ctx, "v", "paid", AttemptUpdate{}); !errors.Is(err, ErrAttemptInvalid) {
		t.Fatalf("bad state: err = %v", err)
	}
	if err := r.Mark(ctx, "missing", AttemptDeclined, AttemptUpdate{}); !errors.Is(err, ErrAttemptNotFound) {
		t.Fatalf("missing row: err = %v", err)
	}
}

// A declined/cancelled attempt may be tried again with the same key and
// amount (same tender retried); an approved one never.
func TestPaymentAttempt_RetryAfterDefiniteFailureOnly(t *testing.T) {
	r := NewPaymentAttemptRepo(newAttemptTestDB(t, ":memory:"))
	ctx := context.Background()
	for _, end := range []string{AttemptDeclined, AttemptCancelled, AttemptResolvedNotCharged} {
		k := "k-" + end
		if _, err := r.Begin(ctx, baseAttempt(k)); err != nil {
			t.Fatal(err)
		}
		if err := r.Mark(ctx, k, end, AttemptUpdate{Reason: "r"}); err != nil {
			t.Fatal(err)
		}
		got, err := r.Begin(ctx, baseAttempt(k))
		if err != nil || got.State != AttemptPending || got.Reason != "" {
			t.Fatalf("retry after %s = %+v %v", end, got, err)
		}
	}
	if _, err := r.Begin(ctx, baseAttempt("done")); err != nil {
		t.Fatal(err)
	}
	if err := r.Mark(ctx, "done", AttemptApproved, AttemptUpdate{PIID: "pi_1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Begin(ctx, baseAttempt("done")); !errors.Is(err, ErrAttemptSettled) {
		t.Fatalf("Begin over an approved attempt: err = %v, want ErrAttemptSettled", err)
	}
}

func TestPaymentAttempt_PruneKeepsPendingAndUnknown(t *testing.T) {
	db := newAttemptTestDB(t, ":memory:")
	r := NewPaymentAttemptRepo(db)
	ctx := context.Background()
	for _, k := range []string{"pend", "unk", "appr", "decl", "recent-appr"} {
		if _, err := r.Begin(ctx, baseAttempt(k)); err != nil {
			t.Fatal(err)
		}
	}
	_ = r.Mark(ctx, "unk", AttemptUnknown, AttemptUpdate{})
	_ = r.Mark(ctx, "appr", AttemptApproved, AttemptUpdate{PIID: "pi"})
	_ = r.Mark(ctx, "decl", AttemptDeclined, AttemptUpdate{})
	_ = r.Mark(ctx, "recent-appr", AttemptApproved, AttemptUpdate{PIID: "pi2"})
	old := time.Now().UTC().Add(-91 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE payment_attempts SET updated_at = ? WHERE idempotency_key IN ('pend','unk','appr','decl')`, old); err != nil {
		t.Fatal(err)
	}
	n, err := r.PruneSettled(ctx, time.Now())
	if err != nil || n != 2 {
		t.Fatalf("PruneSettled = %d, %v; want 2 (appr, decl)", n, err)
	}
	for k, want := range map[string]bool{"pend": true, "unk": true, "appr": false, "decl": false, "recent-appr": true} {
		if _, ok, _ := r.Get(ctx, k); ok != want {
			t.Errorf("after prune, %q present = %v, want %v", k, ok, want)
		}
	}
	// Begin prunes too, so the table is bounded by the sale rate with no job.
	if _, err := db.Exec(`UPDATE payment_attempts SET updated_at = ? WHERE idempotency_key = 'recent-appr'`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Begin(ctx, baseAttempt("fresh")); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := r.Get(ctx, "recent-appr"); ok {
		t.Fatal("Begin did not prune a settled row past retention")
	}
}

func TestPaymentAttempt_NoCardDataColumns(t *testing.T) {
	db := newAttemptTestDB(t, ":memory:")
	rows, err := db.Query(`SELECT name FROM pragma_table_info('payment_attempts')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		switch n {
		case "pan", "card_number", "cvv", "cvc", "track", "pin_block", "expiry":
			t.Fatalf("column %q breaks ADR-0127", n)
		}
	}
}
