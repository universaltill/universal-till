package data

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#2558: a "No sale" drawer open is recorded in no_sale_events
// (migration 064) and rolls up into the cloud sales aggregate's
// no_sale_count / by_cashier.no_sale_opens under the same (business date,
// till) key as the sales rung up on that device.

func TestInsertNoSaleEvent_StoresRowWithLocalDate(t *testing.T) {
	d := b8OpenDB(t, "no-sale-insert.db")
	repo := NewPOSRepo(d.DB)
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local)

	id, err := repo.InsertNoSaleEvent(context.Background(), nil, NoSaleEvent{
		CreatedAt: b8At(at), RegisterID: "reg-A", ActorID: "u1", ApproverID: "m1", Reason: "change for the float",
	})
	if err != nil {
		t.Fatalf("InsertNoSaleEvent: %v", err)
	}
	if id == "" {
		t.Fatal("InsertNoSaleEvent returned an empty id")
	}
	var localDate, reg, actor, approver, reason string
	var till any
	if err := d.DB.QueryRow(`SELECT local_date, COALESCE(register_id,''), till_id, COALESCE(actor_id,''), COALESCE(approver_id,''), COALESCE(reason,'')
FROM no_sale_events WHERE id = ?`, id).Scan(&localDate, &reg, &till, &actor, &approver, &reason); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if want := b8ExpectedDay(t, d, at, 0, 0); localDate != want {
		t.Fatalf("local_date = %q, want %q (the sales.local_date rule)", localDate, want)
	}
	if reg != "reg-A" || till != nil || actor != "u1" || approver != "m1" || reason != "change for the float" {
		t.Fatalf("row = reg %q till %v actor %q approver %q reason %q", reg, till, actor, approver, reason)
	}

	// Optional columns stay NULL when empty (approver only set on elevation).
	id2, err := repo.InsertNoSaleEvent(context.Background(), nil, NoSaleEvent{CreatedAt: b8At(at), ActorID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	var approverNull, regNull, reasonNull any
	if err := d.DB.QueryRow(`SELECT approver_id, register_id, reason FROM no_sale_events WHERE id = ?`, id2).Scan(&approverNull, &regNull, &reasonNull); err != nil {
		t.Fatal(err)
	}
	if approverNull != nil || regNull != nil || reasonNull != nil {
		t.Fatalf("empty optionals must be NULL, got approver %v register %v reason %v", approverNull, regNull, reasonNull)
	}
}

func TestInsertNoSaleEvent_RequiresCreatedAt(t *testing.T) {
	d := b8OpenDB(t, "no-sale-insert-required.db")
	if _, err := NewPOSRepo(d.DB).InsertNoSaleEvent(context.Background(), nil, NoSaleEvent{ActorID: "u1"}); err == nil {
		t.Fatal("InsertNoSaleEvent without CreatedAt must fail")
	}
}

// noSaleAt inserts a no-sale event through the repo method under test.
func noSaleAt(t *testing.T, repo *POSRepo, at time.Time, tillID, registerID, actorID string) {
	t.Helper()
	if _, err := repo.InsertNoSaleEvent(context.Background(), nil, NoSaleEvent{
		CreatedAt: b8At(at), TillID: tillID, RegisterID: registerID, ActorID: actorID,
	}); err != nil {
		t.Fatalf("InsertNoSaleEvent: %v", err)
	}
}

func TestNoSaleOpensForTill_ScopedByDayAndTill(t *testing.T) {
	f := seedSalesAggregateFixture(t)
	repo := NewPOSRepo(f.d.DB)
	ctx := context.Background()

	noSaleAt(t, repo, f.today, "", "reg-A", "u1")
	noSaleAt(t, repo, f.today.Add(time.Hour), "", "reg-A", "u1")
	noSaleAt(t, repo, f.today.Add(2*time.Hour), "", "reg-A", "u2")
	noSaleAt(t, repo, f.today, "", "reg-B", "u1")        // other till
	noSaleAt(t, repo, f.yesterday, "", "reg-A", "u1")    // other day
	noSaleAt(t, repo, f.today, "replica-1", "reg-A", "") // till_id wins over register_id
	noSaleAt(t, repo, f.today, "", "", "u3")             // neither → selfTill

	got, err := repo.NoSaleOpensForTill(ctx, f.day, "reg-A", "self-till")
	if err != nil {
		t.Fatalf("NoSaleOpensForTill: %v", err)
	}
	want := NoSaleOpens{Total: 3, ByActor: map[string]int{"u1": 2, "u2": 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reg-A today = %+v, want %+v", got, want)
	}
	for _, tc := range []struct {
		day, till string
		want      NoSaleOpens
	}{
		{f.day, "reg-B", NoSaleOpens{Total: 1, ByActor: map[string]int{"u1": 1}}},
		{f.prevDay, "reg-A", NoSaleOpens{Total: 1, ByActor: map[string]int{"u1": 1}}},
		{f.day, "replica-1", NoSaleOpens{Total: 1, ByActor: map[string]int{"": 1}}},
		{f.day, "self-till", NoSaleOpens{Total: 1, ByActor: map[string]int{"u3": 1}}},
		{f.prevDay, "reg-B", NoSaleOpens{Total: 0, ByActor: map[string]int{}}},
	} {
		got, err := repo.NoSaleOpensForTill(ctx, tc.day, tc.till, "self-till")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s/%s = %+v, want %+v", tc.day, tc.till, got, tc.want)
		}
	}
}

func TestSalesAggregateForTill_NoSaleCounts(t *testing.T) {
	f := seedSalesAggregateFixture(t)
	repo := NewPOSRepo(f.d.DB)
	ctx := context.Background()
	mustExec(t, f.d, `INSERT INTO users (id, username, display_name) VALUES ('u3', 'carol', 'Carol Example')`)

	noSaleAt(t, repo, f.today, "", "reg-A", "u1")
	noSaleAt(t, repo, f.today.Add(time.Hour), "", "reg-A", "u1")
	// u3 rang no sale on reg-A today, only opened the drawer.
	noSaleAt(t, repo, f.today.Add(2*time.Hour), "", "reg-A", "u3")
	noSaleAt(t, repo, f.today, "", "reg-B", "u2") // other till: not counted on reg-A

	agg, err := repo.SalesAggregateForTill(ctx, f.day, "reg-A", "self-till", false)
	if err != nil {
		t.Fatalf("SalesAggregateForTill: %v", err)
	}
	if agg.NoSaleCount != 3 {
		t.Fatalf("NoSaleCount = %d, want 3", agg.NoSaleCount)
	}
	if agg.Cashiers != nil {
		t.Fatalf("cashier breakdown must not be read when off: %+v", agg.Cashiers)
	}

	agg, err = repo.SalesAggregateForTill(ctx, f.day, "reg-A", "self-till", true)
	if err != nil {
		t.Fatal(err)
	}
	wantCashiers := []SalesAggregateCashier{
		{StaffID: "u1", Net: money.FromMinor(1725), Count: 2, ItemQty: 3, Refunds: 1, NoSaleOpens: 2},
		{StaffID: "u2", Net: money.FromMinor(1071), Count: 1, ItemQty: 1, Voids: 1, Discounts: 1},
		{StaffID: "u3", NoSaleOpens: 1},
	}
	if !reflect.DeepEqual(agg.Cashiers, wantCashiers) {
		t.Fatalf("cashiers = %+v\nwant %+v", agg.Cashiers, wantCashiers)
	}
}

// A (day, till) whose only activity is a no-sale still gets a rollup: the
// drawer opened, and the cloud must hear about it.
func TestSalesAggregateKeys_IncludesNoSaleOnlyDayAndTill(t *testing.T) {
	d := b8OpenDB(t, "no-sale-keys.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local)
	day := b8ExpectedDay(t, d, at, 0, 0)
	noSaleAt(t, repo, at, "", "", "u1")
	noSaleAt(t, repo, at, "", "reg-Z", "u1")
	noSaleAt(t, repo, at.AddDate(0, 0, -30), "", "reg-Z", "u1") // outside the window

	keys, err := repo.SalesAggregateKeys(ctx, day, day, "self-till")
	if err != nil {
		t.Fatalf("SalesAggregateKeys: %v", err)
	}
	want := []SalesAggregateKey{{BusinessDate: day, TillID: "reg-Z"}, {BusinessDate: day, TillID: "self-till"}}
	if !reflect.DeepEqual(keys, want) {
		t.Fatalf("keys = %+v, want %+v", keys, want)
	}
	agg, err := repo.SalesAggregateForTill(ctx, day, "self-till", "self-till", true)
	if err != nil {
		t.Fatal(err)
	}
	if agg.NoSaleCount != 1 || !reflect.DeepEqual(agg.Cashiers, []SalesAggregateCashier{{StaffID: "u1", NoSaleOpens: 1}}) {
		t.Fatalf("no-sale-only rollup = count %d cashiers %+v", agg.NoSaleCount, agg.Cashiers)
	}
}
