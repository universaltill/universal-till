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

// ut-docs#6795: attempts a restart (or New sale / park) left behind.

func seedAttempt(t *testing.T, r *PaymentAttemptRepo, key, provider string, deadline time.Time) {
	t.Helper()
	a := baseAttempt(key)
	a.Provider = provider
	a.Deadline = deadline
	if _, err := r.Begin(context.Background(), a); err != nil {
		t.Fatal(err)
	}
}

func TestMarkPendingUnknownAtBoot_OnlyPendingChanges(t *testing.T) {
	db := newAttemptTestDB(t, ":memory:")
	r := NewPaymentAttemptRepo(db)
	ctx := context.Background()
	future := time.Now().Add(time.Minute)
	seedAttempt(t, r, "pend", "stripe", future)
	seedAttempt(t, r, "unk", "stripe", future)
	seedAttempt(t, r, "ok", "stripe", future)
	seedAttempt(t, r, "dec", "stripe", future)
	if err := r.Mark(ctx, "unk", AttemptUnknown, AttemptUpdate{Reason: "processing"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Mark(ctx, "ok", AttemptApproved, AttemptUpdate{}); err != nil {
		t.Fatal(err)
	}
	if err := r.Mark(ctx, "dec", AttemptDeclined, AttemptUpdate{}); err != nil {
		t.Fatal(err)
	}
	n, err := r.MarkPendingUnknownAtBoot(ctx, time.Now())
	if err != nil || n != 1 {
		t.Fatalf("MarkPendingUnknownAtBoot = %d, %v; want 1", n, err)
	}
	want := map[string][2]string{
		"pend": {AttemptUnknown, AttemptReasonTillRestarted},
		"unk":  {AttemptUnknown, "processing"},
		"ok":   {AttemptApproved, ""},
		"dec":  {AttemptDeclined, ""},
	}
	for k, w := range want {
		a, _, _ := r.Get(ctx, k)
		if a.State != w[0] || a.Reason != w[1] {
			t.Errorf("%s = %s/%q, want %s/%q", k, a.State, a.Reason, w[0], w[1])
		}
	}
}

func TestPaymentAttempt_NeedsAttention(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, c := range []struct {
		name string
		a    PaymentAttempt
		want bool
	}{
		{"unknown", PaymentAttempt{State: AttemptUnknown, Deadline: now.Add(time.Hour)}, true},
		{"pending past deadline and grace", PaymentAttempt{State: AttemptPending, Deadline: now.Add(-AttemptSettleGrace - time.Second)}, true},
		{"pending at deadline plus grace", PaymentAttempt{State: AttemptPending, Deadline: now.Add(-AttemptSettleGrace)}, true},
		// Review R3: just past its deadline the collect's own request may still
		// be confirming with the broker; not an orphan yet.
		{"pending at deadline", PaymentAttempt{State: AttemptPending, Deadline: now}, false},
		{"pending within settle grace", PaymentAttempt{State: AttemptPending, Deadline: now.Add(-time.Second)}, false},
		{"pending sub-second future", PaymentAttempt{State: AttemptPending, Deadline: now.Add(500 * time.Millisecond)}, false},
		{"pending future", PaymentAttempt{State: AttemptPending, Deadline: now.Add(time.Minute)}, false},
		{"approved", PaymentAttempt{State: AttemptApproved, Deadline: now.Add(-time.Hour)}, false},
		{"declined", PaymentAttempt{State: AttemptDeclined, Deadline: now.Add(-time.Hour)}, false},
		{"cancelled", PaymentAttempt{State: AttemptCancelled, Deadline: now.Add(-time.Hour)}, false},
		{"resolved", PaymentAttempt{State: AttemptResolvedNotCharged, Deadline: now.Add(-time.Hour)}, false},
	} {
		if got := c.a.NeedsAttention(now); got != c.want {
			t.Errorf("%s: NeedsAttention = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestListNeedingAttention_FiltersAndOrders(t *testing.T) {
	db := newAttemptTestDB(t, ":memory:")
	r := NewPaymentAttemptRepo(db)
	ctx := context.Background()
	now := time.Now()
	seedAttempt(t, r, "a-unk", "stripe", now.Add(time.Hour))
	time.Sleep(2 * time.Millisecond)
	past := now.Add(-AttemptSettleGrace - time.Second)
	seedAttempt(t, r, "b-pend-past", "stripe", past)
	time.Sleep(2 * time.Millisecond)
	seedAttempt(t, r, "c-pend-future", "stripe", now.Add(time.Minute))
	time.Sleep(2 * time.Millisecond)
	seedAttempt(t, r, "d-other", "other", past)
	time.Sleep(2 * time.Millisecond)
	seedAttempt(t, r, "e-ok", "stripe", past)
	if err := r.Mark(ctx, "a-unk", AttemptUnknown, AttemptUpdate{Reason: "processing"}); err != nil {
		t.Fatal(err)
	}
	if err := r.Mark(ctx, "e-ok", AttemptApproved, AttemptUpdate{}); err != nil {
		t.Fatal(err)
	}
	keys := func(p string, limit int) []string {
		rows, err := r.ListNeedingAttention(ctx, now, p, limit)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, a := range rows {
			out = append(out, a.Key)
		}
		return out
	}
	if got := keys("stripe", 0); len(got) != 2 || got[0] != "a-unk" || got[1] != "b-pend-past" {
		t.Errorf("stripe = %v", got)
	}
	if got := keys("", 0); len(got) != 3 || got[2] != "d-other" {
		t.Errorf("all = %v", got)
	}
	if got := keys("other", 0); len(got) != 1 || got[0] != "d-other" {
		t.Errorf("other = %v", got)
	}
	if got := keys("", 1); len(got) != 1 || got[0] != "a-unk" {
		t.Errorf("limit 1 = %v", got)
	}
}

// ut-docs#7028: stamp an approved attempt that never became a sale.
func TestPaymentAttempt_MarkChargedNoSale_7028(t *testing.T) {
	r := NewPaymentAttemptRepo(newAttemptTestDB(t, ":memory:"))
	ctx := context.Background()
	for _, k := range []string{"a1", "a2", "p", "u", "d"} {
		if _, err := r.Begin(ctx, baseAttempt(k)); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range []string{"a1", "a2"} {
		if err := r.Mark(ctx, k, AttemptApproved, AttemptUpdate{PIID: "pi_1"}); err != nil {
			t.Fatal(err)
		}
	}
	// a2 was approved under a manager override.
	if _, err := r.db.Exec(`UPDATE payment_attempts SET reason='manager_override' WHERE idempotency_key='a2'`); err != nil {
		t.Fatal(err)
	}
	_ = r.Mark(ctx, "u", AttemptUnknown, AttemptUpdate{})
	_ = r.Mark(ctx, "d", AttemptDeclined, AttemptUpdate{})
	for k, wantPrior := range map[string]string{"a1": "", "a2": "manager_override"} {
		changed, prior, err := r.MarkChargedNoSale(ctx, k)
		if err != nil || !changed || prior.Reason != wantPrior || prior.State != AttemptApproved {
			t.Fatalf("%s: changed=%v prior=%+v err=%v", k, changed, prior, err)
		}
		got, _, _ := r.Get(ctx, k)
		if got.State != AttemptApproved || got.Reason != AttemptReasonChargedNoSale || got.PIID != "pi_1" {
			t.Fatalf("%s after stamp = %+v", k, got)
		}
		if changed, _, err := r.MarkChargedNoSale(ctx, k); err != nil || changed {
			t.Fatalf("%s second call: changed=%v err=%v, want no-op", k, changed, err)
		}
	}
	for _, k := range []string{"p", "u", "d"} {
		if _, _, err := r.MarkChargedNoSale(ctx, k); !errors.Is(err, ErrAttemptSettled) {
			t.Fatalf("%s: %v, want ErrAttemptSettled", k, err)
		}
	}
	if _, _, err := r.MarkChargedNoSale(ctx, "none"); !errors.Is(err, ErrAttemptNotFound) {
		t.Fatalf("absent: %v, want ErrAttemptNotFound", err)
	}
}
