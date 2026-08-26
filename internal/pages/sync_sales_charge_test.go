package pages

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ADR-0061 Decision 4: a replayed/synced sale reproduces its ORIGINAL
// persisted amounts exactly — replay never re-asks charge.policy.ask and
// never recomputes a different total than what was originally stored,
// regardless of what a plugin installed on the primary would answer today.
// The journal carries the original service-charge AMOUNT; the replay's
// deterministic fail-closed apportionment (per-line rates) reproduces the
// original tax/total bit-for-bit.
func TestApplyJournal_ServiceChargeReplayNeverReAsksChargePolicy(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()

	// A plugin on the primary that would answer charge.policy.ask with a
	// policy that CONTRADICTS the original sale (charge forbidden, flat 7%
	// basis) — if replay consulted the hook, the persisted totals would
	// differ and the counter would move.
	seedChargePolicyPlugin(t, dp.Db)
	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	asks := 0
	bus.SetEventMode("charge.policy.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"charge.policy.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			asks++
			return json.RawMessage(`{"service_charge_permitted":false,"service_charge_tax_basis_bp":700}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	// The original sale, as the replica tendered it: 100 @20% exclusive,
	// service charge 10, taxed at the sale's own blended rate (20% -> 2)
	// per ADR-0061's fail-closed default: tax 22, total 132. The journal
	// carries these ORIGINAL persisted amounts.
	j := seedJournalSale("remote-sale-sc", "T2-R777-SC", "sale", "", "itm1", 1, 100)
	j.Sale.Lines[0].TaxRateBP = 2000
	j.Sale.Lines[0].TaxAmount = 20
	j.Sale.ServiceCharge = 10
	j.Sale.TaxTotal = 22
	j.Sale.Total = 132
	j.Sale.Payments = []data.SaleDetailPayment{
		{Method: "cash", Amount: 132, TipAmount: 25, TipRecipient: "business"},
	}

	applied, _, err := applyJournal(ctx, dp, "till-1", j)
	if err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("expected the journal to apply")
	}

	if asks != 0 {
		t.Fatalf("replay must NEVER re-ask charge.policy.ask (ADR-0061 Decision 4), but the plugin was asked %d time(s)", asks)
	}

	var total, taxTotal, serviceCharge int64
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT total, tax_total, service_charge_amount FROM sales WHERE id = 'remote-sale-sc'`).
		Scan(&total, &taxTotal, &serviceCharge); err != nil {
		t.Fatalf("read replayed sale: %v", err)
	}
	if serviceCharge != 10 {
		t.Fatalf("replay must keep the ORIGINAL charge amount 10 (never recompute/suppress against today's policy), got %d", serviceCharge)
	}
	if taxTotal != 22 || total != 132 {
		t.Fatalf("replay recomputed different totals than originally stored: tax_total=%d total=%d, want 22/132", taxTotal, total)
	}

	// The tip recipient recorded at capture time survives the replay too
	// (ADR-0061 Decision 3) — never re-derived from the primary's policy.
	var recipient string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT tip_recipient FROM payments WHERE sale_id = 'remote-sale-sc'`).Scan(&recipient); err != nil {
		t.Fatalf("read replayed payment: %v", err)
	}
	if recipient != "business" {
		t.Fatalf("tip_recipient dropped by journal replay: got %q, want 'business'", recipient)
	}
}

// A pre-ADR-0061 peer's journal has no tip_recipient key at all — the
// replay must default it to 'employee', not fail or store empty.
func TestApplyJournal_MissingTipRecipientDefaultsToEmployee(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()

	j := seedJournalSale("remote-sale-oldtip", "T2-R778-OT", "sale", "", "itm1", 1, 100)
	j.Sale.Payments = []data.SaleDetailPayment{{Method: "cash", Amount: 100, TipAmount: 15}}
	applied, _, err := applyJournal(ctx, dp, "till-1", j)
	if err != nil || !applied {
		t.Fatalf("apply: applied=%v err=%v", applied, err)
	}
	var recipient string
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT tip_recipient FROM payments WHERE sale_id = 'remote-sale-oldtip'`).Scan(&recipient); err != nil {
		t.Fatalf("read replayed payment: %v", err)
	}
	if recipient != "employee" {
		t.Fatalf("want the employee default for a recipient-less journal payment, got %q", recipient)
	}
}

// ADR-0061 Decision 4, the non-default half (reviewer finding, 2026-08-24):
// a sale tendered while a country plugin answered a FLAT
// service_charge_tax_basis_bp must replay to exactly the totals it was stored
// with. The basis is persisted (migration 062) and rides the journal, because
// computeSaleTotals re-derives the charge's tax from it on replay: without it
// the primary re-derived the APPORTIONED tax instead, and when that landed
// above the original the replay was rejected outright as underpayment -- so
// the sale could never replicate at all.
func TestApplyJournal_ServiceChargeFlatBasisReplaysExactly(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()

	// As the replica tendered it: 100 @20% exclusive (line tax 20), charge
	// 10 taxed at the plugin's flat 7% = 1 -> tax 21, total 131. The
	// apportioned default would have made it 2 -> tax 22, total 132.
	j := seedJournalSale("remote-sale-flat", "T2-R779-FB", "sale", "", "itm1", 1, 100)
	j.Sale.Lines[0].TaxRateBP = 2000
	j.Sale.Lines[0].TaxAmount = 20
	j.Sale.ServiceCharge = 10
	j.Sale.ServiceChargeTaxBasisBP = 700
	j.Sale.TaxTotal = 21
	j.Sale.Total = 131
	j.Sale.Payments = []data.SaleDetailPayment{{Method: "cash", Amount: 131}}

	applied, _, err := applyJournal(ctx, dp, "till-1", j)
	if err != nil {
		t.Fatalf("replay rejected a sale the replica already completed: %v", err)
	}
	if !applied {
		t.Fatal("expected the journal to apply")
	}
	var total, taxTotal, basis int64
	if err := dp.Db.QueryRowContext(ctx,
		`SELECT total, tax_total, service_charge_tax_basis_bp FROM sales WHERE id = 'remote-sale-flat'`).
		Scan(&total, &taxTotal, &basis); err != nil {
		t.Fatalf("read replayed sale: %v", err)
	}
	if taxTotal != 21 || total != 131 {
		t.Fatalf("replay re-derived the charge tax against the primary's own default instead of the original basis: tax_total=%d total=%d, want 21/131", taxTotal, total)
	}
	if basis != 700 {
		t.Fatalf("the original tax basis must persist on the replayed sale too (so IT can be re-journaled onward), got %d", basis)
	}
}

// The basis survives a full local round-trip: tendered -> persisted ->
// GetSaleDetail -> buildJournal, which is how it reaches a peer at all.
func TestBuildJournal_CarriesServiceChargeTaxBasis(t *testing.T) {
	_, dp := newSyncSalesTestDeps(t)
	ctx := context.Background()

	j := seedJournalSale("remote-sale-rt", "T2-R780-RT", "sale", "", "itm1", 1, 100)
	j.Sale.Lines[0].TaxRateBP = 2000
	j.Sale.Lines[0].TaxAmount = 20
	j.Sale.ServiceCharge = 10
	j.Sale.ServiceChargeTaxBasisBP = 700
	j.Sale.Payments = []data.SaleDetailPayment{{Method: "cash", Amount: 131}}
	if _, _, err := applyJournal(ctx, dp, "till-1", j); err != nil {
		t.Fatalf("seed via replay: %v", err)
	}

	out, found, err := buildJournal(ctx, data.NewPOSRepo(dp.Db), "T2-R780-RT")
	if err != nil || !found {
		t.Fatalf("buildJournal: found=%v err=%v", found, err)
	}
	if out.Sale.ServiceChargeTaxBasisBP != 700 {
		t.Fatalf("buildJournal dropped the charge tax basis: got %d, want 700", out.Sale.ServiceChargeTaxBasisBP)
	}
}
