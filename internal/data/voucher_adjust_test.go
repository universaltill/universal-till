package data

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// Manager balance correction (ut-docs#4253): AdjustVoucherBalance against
// the real migrated schema (074 admits type 'adjustment').

func adjIn(id, voucherID string, expected, newBal int64, reason string) VoucherAdjustment {
	return VoucherAdjustment{
		ID: id, VoucherID: voucherID, ExpectedBalanceMinor: expected, NewBalanceMinor: newBal,
		Reason: reason, ActorUserID: "user1", Now: time.Now().UTC().Format(time.RFC3339),
	}
}

func adjSeedUser(t *testing.T, repo *POSRepo) {
	t.Helper()
	if _, err := repo.db.Exec(`INSERT OR IGNORE INTO users (id, username, display_name, role) VALUES ('user1', 'user1', 'Manager', 'manager')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func adjRows(t *testing.T, repo *POSRepo, voucherID string) int {
	t.Helper()
	var n int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = ? AND type = 'adjustment'`, voucherID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAdjustVoucherBalance_Refusals(t *testing.T) {
	d := b8OpenDB(t, "voucher-adjust-refusals.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	adjSeedUser(t, repo)

	vSeedVoucher(t, ctx, repo, "GS-OK", 1000)
	vSeedVoucher(t, ctx, repo, "GS-VOID", 1000)
	if _, err := d.DB.Exec(`UPDATE vouchers SET status = 'void', balance = 0 WHERE id = 'GS-VOID'`); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateVoucher(ctx, nil, Voucher{
		ID: "GS-SP", OriginalAmountMinor: 1000, BalanceMinor: 1000, Currency: "EUR",
		Purpose: VoucherPurposeSingle, CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		in   VoucherAdjustment
		want error
	}{
		{"not found", adjIn("a1", "GS-NOPE", 1000, 500, "typo"), ErrVoucherNotFound},
		{"void", adjIn("a2", "GS-VOID", 0, 500, "typo"), ErrVoucherNotActive},
		{"single purpose", adjIn("a3", "GS-SP", 1000, 500, "typo"), ErrVoucherNotMultiPurpose},
		{"negative", adjIn("a4", "GS-OK", 1000, -1, "typo"), ErrVoucherAdjustmentInvalid},
		{"above ceiling", adjIn("a5", "GS-OK", 1000, maxItemPriceMinor+1, "typo"), ErrVoucherAdjustmentInvalid},
		{"empty reason", adjIn("a6", "GS-OK", 1000, 500, "   "), ErrVoucherAdjustmentReason},
		{"too long reason", adjIn("a7", "GS-OK", 1000, 500, strings.Repeat("é", 201)), ErrVoucherAdjustmentReason},
		{"no change", adjIn("a8", "GS-OK", 1000, 1000, "typo"), ErrVoucherAdjustmentNoChange},
		{"balance changed", adjIn("a9", "GS-OK", 900, 500, "typo"), ErrVoucherBalanceChanged},
		{"missing id", adjIn("", "GS-OK", 1000, 500, "typo"), ErrVoucherAdjustmentInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := repo.AdjustVoucherBalance(ctx, nil, tc.in)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	v, _ := repo.GetVoucherBalance(ctx, nil, "GS-OK")
	if v.BalanceMinor != 1000 || v.Status != "active" {
		t.Fatalf("refusals changed GS-OK: %+v", v)
	}
	for _, id := range []string{"GS-OK", "GS-VOID", "GS-SP"} {
		if n := adjRows(t, repo, id); n != 0 {
			t.Fatalf("refusal wrote %d adjustment rows for %s", n, id)
		}
	}
	// Exactly 200 runes is fine.
	if _, _, err := repo.AdjustVoucherBalance(ctx, nil, adjIn("a10", "GS-OK", 1000, 999, strings.Repeat("é", 200))); err != nil {
		t.Fatalf("200-rune reason: %v", err)
	}
	// The ceiling itself is fine.
	if _, _, err := repo.AdjustVoucherBalance(ctx, nil, adjIn("a11", "GS-OK", 999, maxItemPriceMinor, "top up")); err != nil {
		t.Fatalf("ceiling balance: %v", err)
	}
}

func TestAdjustVoucherBalance_UpDownZeroAndReopen(t *testing.T) {
	d := b8OpenDB(t, "voucher-adjust-success.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	adjSeedUser(t, repo)
	vSeedVoucher(t, ctx, repo, "GS-1", 1000)

	// Down.
	before, after, err := repo.AdjustVoucherBalance(ctx, nil, adjIn("adj-1", "GS-1", 1000, 400, "card damaged, re-issued"))
	if err != nil {
		t.Fatalf("down: %v", err)
	}
	if before.BalanceMinor != 1000 || after.BalanceMinor != 400 || after.Status != "active" {
		t.Fatalf("down: before %+v after %+v", before, after)
	}
	// To zero -> redeemed ("Used up").
	_, after, err = repo.AdjustVoucherBalance(ctx, nil, adjIn("adj-2", "GS-1", 400, 0, "customer spent it elsewhere"))
	if err != nil {
		t.Fatalf("to zero: %v", err)
	}
	if after.BalanceMinor != 0 || after.Status != "redeemed" {
		t.Fatalf("to zero: %+v, want 0/redeemed", after)
	}
	// Up from a redeemed voucher re-opens it.
	_, after, err = repo.AdjustVoucherBalance(ctx, nil, adjIn("adj-3", "GS-1", 0, 2500, "goodwill top-up"))
	if err != nil {
		t.Fatalf("up from redeemed: %v", err)
	}
	if after.BalanceMinor != 2500 || after.Status != "active" {
		t.Fatalf("up: %+v, want 2500/active", after)
	}
	// original_amount never moves.
	if after.OriginalAmountMinor != 1000 {
		t.Fatalf("original_amount = %d, want 1000", after.OriginalAmountMinor)
	}

	// Signed rows with reason and actor, NULL sale.
	rows, err := d.DB.Query(`SELECT id, amount, sale_id IS NULL, actor_user_id, reason FROM voucher_transactions WHERE voucher_id = 'GS-1' AND type = 'adjustment' ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		id       string
		amount   int64
		nullSale bool
		actor    string
		reason   string
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.amount, &r.nullSale, &r.actor, &r.reason); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	rows.Close()
	want := []row{
		{"adj-1", -600, true, "user1", "card damaged, re-issued"},
		{"adj-2", -400, true, "user1", "customer spent it elsewhere"},
		{"adj-3", 2500, true, "user1", "goodwill top-up"},
	}
	if len(got) != len(want) {
		t.Fatalf("adjustment rows = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}

	// History carries the reason and actor.
	hist, err := repo.VoucherTransactions(ctx, "GS-1")
	if err != nil {
		t.Fatal(err)
	}
	var adj []VoucherHistoryRow
	for _, h := range hist {
		if h.Type == "adjustment" {
			adj = append(adj, h)
		}
	}
	if len(adj) != 3 || adj[0].Reason != "card damaged, re-issued" || adj[0].AmountMinor != -600 || adj[0].ActorUserID != "user1" {
		t.Fatalf("history adjustments = %+v", adj)
	}
}

func TestAdjustVoucherBalance_TrimsReason(t *testing.T) {
	d := b8OpenDB(t, "voucher-adjust-trim.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	adjSeedUser(t, repo)
	vSeedVoucher(t, ctx, repo, "GS-T", 1000)
	if _, _, err := repo.AdjustVoucherBalance(ctx, nil, adjIn("adj-t", "GS-T", 1000, 900, "  typo at issue  ")); err != nil {
		t.Fatal(err)
	}
	var reason string
	if err := d.DB.QueryRow(`SELECT reason FROM voucher_transactions WHERE id = 'adj-t'`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "typo at issue" {
		t.Fatalf("reason = %q, want trimmed", reason)
	}
}

func TestAdjustVoucherBalance_IdempotentRetry(t *testing.T) {
	d := b8OpenDB(t, "voucher-adjust-idem.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	adjSeedUser(t, repo)
	vSeedVoucher(t, ctx, repo, "GS-I", 1000)

	in := adjIn("adj-same", "GS-I", 1000, 700, "typo")
	b1, a1, err := repo.AdjustVoucherBalance(ctx, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	// The same call again (a lost response, a double-submit): no second
	// application, same answer — even though ExpectedBalance is now stale.
	b2, a2, err := repo.AdjustVoucherBalance(ctx, nil, in)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if b2.BalanceMinor != b1.BalanceMinor || a2.BalanceMinor != a1.BalanceMinor || a2.BalanceMinor != 700 {
		t.Fatalf("retry answered before %d after %d, want %d/%d", b2.BalanceMinor, a2.BalanceMinor, b1.BalanceMinor, a1.BalanceMinor)
	}
	if n := adjRows(t, repo, "GS-I"); n != 1 {
		t.Fatalf("adjustment rows after retry = %d, want 1", n)
	}
	var audits int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'voucher' AND entity_id = 'GS-I' AND action = 'adjust'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("audit rows after retry = %d, want 1", audits)
	}
	// The same id on a different voucher is a collision, not a retry.
	vSeedVoucher(t, ctx, repo, "GS-J", 1000)
	if _, _, err := repo.AdjustVoucherBalance(ctx, nil, adjIn("adj-same", "GS-J", 1000, 700, "typo")); !errors.Is(err, ErrVoucherAdjustmentInvalid) {
		t.Fatalf("id reused on another voucher: err = %v, want ErrVoucherAdjustmentInvalid", err)
	}
}

func TestAdjustVoucherBalance_WritesAuditRow(t *testing.T) {
	d := b8OpenDB(t, "voucher-adjust-audit.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	adjSeedUser(t, repo)
	vSeedVoucher(t, ctx, repo, "GS-A", 1000)
	if _, _, err := repo.AdjustVoucherBalance(ctx, nil, adjIn("adj-a", "GS-A", 1000, 1250, "wrong amount keyed")); err != nil {
		t.Fatal(err)
	}
	var actor, payload string
	if err := d.DB.QueryRow(`SELECT COALESCE(actor_id, ''), data_json FROM audit_log WHERE entity_type = 'voucher' AND entity_id = 'GS-A' AND action = 'adjust'`).Scan(&actor, &payload); err != nil {
		t.Fatalf("audit row: %v", err)
	}
	if actor != "user1" {
		t.Fatalf("audit actor = %q", actor)
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatal(err)
	}
	if p["old"] != float64(1000) || p["new"] != float64(1250) || p["delta"] != float64(250) || p["reason"] != "wrong amount keyed" || p["adjustment_id"] != "adj-a" {
		t.Fatalf("audit payload = %v", p)
	}

	// An actor this till has no users row for (a replica's user on the
	// primary) still records the correction: the audit row carries the id
	// in its payload and a NULL actor_id rather than failing the FK.
	if _, _, err := repo.AdjustVoucherBalance(ctx, nil, VoucherAdjustment{
		ID: "adj-b", VoucherID: "GS-A", ExpectedBalanceMinor: 1250, NewBalanceMinor: 1000,
		Reason: "undo", ActorUserID: "remote-user", Now: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("unknown actor: %v", err)
	}
	if err := d.DB.QueryRow(`SELECT COALESCE(actor_id, ''), data_json FROM audit_log WHERE entity_id = 'GS-A' AND action = 'adjust' AND data_json LIKE '%adj-b%'`).Scan(&actor, &payload); err != nil {
		t.Fatalf("audit row for unknown actor: %v", err)
	}
	if actor != "" || !strings.Contains(payload, `"actor":"remote-user"`) {
		t.Fatalf("unknown actor audit = %q / %s", actor, payload)
	}
}

// Day-end: an adjustment is its own signed bucket — never Issued, Imported
// (despite its NULL sale_id) or Redeemed — in both window functions.
func TestVoucherRangeSummary_AdjustmentBucket(t *testing.T) {
	d := b8OpenDB(t, "voucher-adjust-range.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	adjSeedUser(t, repo)

	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	vSeedVoucher(t, ctx, repo, "GS-R1", 1000)
	vSeedVoucher(t, ctx, repo, "GS-R2", 1000)
	at := b8At(today)
	for _, in := range []VoucherAdjustment{
		{ID: "r-1", VoucherID: "GS-R1", ExpectedBalanceMinor: 1000, NewBalanceMinor: 400, Reason: "x", ActorUserID: "user1", Now: at},
		{ID: "r-2", VoucherID: "GS-R2", ExpectedBalanceMinor: 1000, NewBalanceMinor: 1200, Reason: "y", ActorUserID: "user1", Now: at},
	} {
		if _, _, err := repo.AdjustVoucherBalance(ctx, nil, in); err != nil {
			t.Fatal(err)
		}
	}

	check := func(label string, s VoucherRangeSummary) {
		t.Helper()
		if s.AdjustedCount != 2 || s.AdjustedMinor != -400 {
			t.Fatalf("%s adjusted = %d/%d, want 2/-400", label, s.AdjustedCount, s.AdjustedMinor)
		}
		if s.IssuedCount != 0 || s.ImportedCount != 0 || s.RedeemedCount != 0 {
			t.Fatalf("%s: an adjustment leaked into another bucket: %+v", label, s)
		}
	}
	day := b8ExpectedDay(t, d, today, 0, 0)
	s, err := repo.VouchersIssuedRedeemedForRange(ctx, day, day)
	if err != nil {
		t.Fatal(err)
	}
	check("range", s)
	s, err = repo.VouchersIssuedRedeemedForInstantWindow(ctx, today.Add(-time.Hour), today.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	check("instant", s)

	rep, err := repo.EndOfDay(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if rep.VouchersAdjustedCount != 2 || rep.VouchersAdjusted != -400 {
		t.Fatalf("EOD adjusted = %d/%d, want 2/-400", rep.VouchersAdjustedCount, rep.VouchersAdjusted)
	}
	if rep.Gross != 0 || rep.Net != 0 {
		t.Fatalf("an adjustment moved revenue: gross %d net %d", rep.Gross, rep.Net)
	}
}

// AdjustVoucherBalance inside the caller's transaction: nothing lands until
// the caller commits, and a rollback leaves no trace.
func TestAdjustVoucherBalance_CallerTxRollback(t *testing.T) {
	d := b8OpenDB(t, "voucher-adjust-tx.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	adjSeedUser(t, repo)
	vSeedVoucher(t, ctx, repo, "GS-X", 1000)
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.AdjustVoucherBalance(ctx, tx, adjIn("adj-x", "GS-X", 1000, 10, "x")); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	v, _ := repo.GetVoucherBalance(ctx, nil, "GS-X")
	if v.BalanceMinor != 1000 || adjRows(t, repo, "GS-X") != 0 {
		t.Fatalf("rolled-back adjustment left balance %d / rows %d", v.BalanceMinor, adjRows(t, repo, "GS-X"))
	}
}

// MAJOR-1 (review of ut-docs#4253): a replayed adjustment id must carry the
// SAME request — the same delta (new − expected) and reason — or it is
// refused, never answered with success as if a different correction had
// been applied.
func TestAdjustVoucherBalance_ReplayWithDifferentRequestRefused(t *testing.T) {
	d := b8OpenDB(t, "voucher-adjust-replay-mismatch.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	adjSeedUser(t, repo)
	vSeedVoucher(t, ctx, repo, "GS-R", 1000)

	if _, _, err := repo.AdjustVoucherBalance(ctx, nil, adjIn("adj-r", "GS-R", 1000, 700, "typo")); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]VoucherAdjustment{
		"different new balance":      adjIn("adj-r", "GS-R", 1000, 500, "typo"),
		"different expected balance": adjIn("adj-r", "GS-R", 900, 700, "typo"),
		"different reason":           adjIn("adj-r", "GS-R", 1000, 700, "something else"),
	} {
		if _, _, err := repo.AdjustVoucherBalance(ctx, nil, in); !errors.Is(err, ErrVoucherAdjustmentInvalid) {
			t.Fatalf("%s: err = %v, want ErrVoucherAdjustmentInvalid", name, err)
		}
	}
	// A matching replay whose reason differs only by surrounding space is
	// the same request (the reason is trimmed before it is stored).
	if _, _, err := repo.AdjustVoucherBalance(ctx, nil, adjIn("adj-r", "GS-R", 1000, 700, "  typo ")); err != nil {
		t.Fatalf("matching replay (untrimmed reason): %v", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-R"); v.BalanceMinor != 700 {
		t.Fatalf("balance = %d, want 700 (no replay may apply anything)", v.BalanceMinor)
	}
	if n := adjRows(t, repo, "GS-R"); n != 1 {
		t.Fatalf("adjustment rows = %d, want 1", n)
	}
}

// A matching replay after the balance has moved on (a later redemption)
// still answers the adjustment's OWN before/after, not the live balance.
func TestAdjustVoucherBalance_ReplayAfterRedemptionReportsOwnResult(t *testing.T) {
	d := b8OpenDB(t, "voucher-adjust-replay-moved.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	adjSeedUser(t, repo)
	vSeedVoucher(t, ctx, repo, "GS-M", 1000)

	in := adjIn("adj-m", "GS-M", 1000, 700, "typo")
	if _, _, err := repo.AdjustVoucherBalance(ctx, nil, in); err != nil {
		t.Fatal(err)
	}
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-M", 200, false); err != nil {
		t.Fatal(err)
	}
	before, after, err := repo.AdjustVoucherBalance(ctx, nil, in)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if before.BalanceMinor != 1000 || after.BalanceMinor != 700 || after.Status != "active" || after.ID != "GS-M" {
		t.Fatalf("replay answered before %d after %+v, want 1000 → 700 active", before.BalanceMinor, after)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-M"); v.BalanceMinor != 500 {
		t.Fatalf("live balance = %d, want 500 (the replay must not touch it)", v.BalanceMinor)
	}
}

// MINOR-1: MirrorVoucherAdjustment sets a replica's local row to the main
// till's result with no range/purpose/status checks of its own (the main
// till already decided), records the delta against the LOCAL prior
// balance, skips the ledger row when nothing moves, and is idempotent on
// the adjustment id.
func TestMirrorVoucherAdjustment(t *testing.T) {
	d := b8OpenDB(t, "voucher-adjust-mirror.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	adjSeedUser(t, repo)
	now := time.Now().UTC().Format(time.RFC3339)

	mirror := func(voucherID, adjID string, newBal int64, reason string) error {
		tx, err := d.DB.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback() }()
		if err := repo.MirrorVoucherAdjustment(ctx, tx, voucherID, adjID, newBal, reason, "someone-elsewhere", now); err != nil {
			return err
		}
		return tx.Commit()
	}
	rowAmount := func(adjID string) (int64, bool) {
		var amt int64
		err := d.DB.QueryRow(`SELECT amount FROM voucher_transactions WHERE id = ? AND type = 'adjustment'`, adjID).Scan(&amt)
		return amt, err == nil
	}

	// A stale local row (800) lands on the primary's 600; delta −200.
	vSeedVoucher(t, ctx, repo, "GS-A", 1000)
	if _, err := d.DB.Exec(`UPDATE vouchers SET balance = 800 WHERE id = 'GS-A'`); err != nil {
		t.Fatal(err)
	}
	if err := mirror("GS-A", "m-1", 600, "top-up"); err != nil {
		t.Fatalf("mirror: %v", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-A"); v.BalanceMinor != 600 || v.Status != "active" {
		t.Fatalf("GS-A = %+v, want 600 active", v)
	}
	if amt, ok := rowAmount("m-1"); !ok || amt != -200 {
		t.Fatalf("m-1 amount = %d (%v), want -200", amt, ok)
	}
	// Idempotent on the id: a second mirror changes nothing.
	if _, err := d.DB.Exec(`UPDATE vouchers SET balance = 550 WHERE id = 'GS-A'`); err != nil { // a later local redemption
		t.Fatal(err)
	}
	if err := mirror("GS-A", "m-1", 600, "top-up"); err != nil {
		t.Fatalf("repeat mirror: %v", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-A"); v.BalanceMinor != 550 {
		t.Fatalf("repeat mirror moved the balance to %d", v.BalanceMinor)
	}

	// Already at the target: no ledger row.
	vSeedVoucher(t, ctx, repo, "GS-EQ", 1000)
	if err := mirror("GS-EQ", "m-2", 1000, "nothing"); err != nil {
		t.Fatalf("equal mirror: %v", err)
	}
	if _, ok := rowAmount("m-2"); ok {
		t.Fatal("a mirror with no balance change wrote a ledger row")
	}

	// To zero: status follows (redeemed).
	if err := mirror("GS-EQ", "m-3", 0, "spent"); err != nil {
		t.Fatal(err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-EQ"); v.BalanceMinor != 0 || v.Status != "redeemed" {
		t.Fatalf("GS-EQ = %+v, want 0 redeemed", v)
	}

	// No local checks: a stale local void / single-purpose row and a result
	// above this till's own ceiling are still mirrored.
	vSeedVoucher(t, ctx, repo, "GS-V", 1000)
	if _, err := d.DB.Exec(`UPDATE vouchers SET status = 'void', purpose = 'single_purpose' WHERE id = 'GS-V'`); err != nil {
		t.Fatal(err)
	}
	if err := mirror("GS-V", "m-4", maxItemPriceMinor+1, "primary knows best"); err != nil {
		t.Fatalf("mirror onto stale void/single-purpose row: %v", err)
	}
	if v, _ := repo.GetVoucherBalance(ctx, nil, "GS-V"); v.BalanceMinor != maxItemPriceMinor+1 || v.Status != "active" {
		t.Fatalf("GS-V = %+v, want the primary's result, active", v)
	}

	// Unknown voucher locally → ErrVoucherNotFound (the caller seeds first).
	if err := mirror("GS-NOPE", "m-5", 10, "x"); !errors.Is(err, ErrVoucherNotFound) {
		t.Fatalf("unknown voucher: err = %v, want ErrVoucherNotFound", err)
	}
}
