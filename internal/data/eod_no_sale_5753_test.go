package data

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#5753: the EOD/Z report counts "No sale" drawer opens
// (no_sale_events) of every till, informational only. Every instant is
// anchored on the host's local noon (time.Now()), never a fixed date.

func nsSeedUsers(t *testing.T, d *db.DB) {
	t.Helper()
	mustExec(t, d, `INSERT INTO users (id, username, display_name, role) VALUES ('ns-a','alice','Alice','cashier')`)
	mustExec(t, d, `INSERT INTO users (id, username, display_name, role) VALUES ('ns-b','bob','Bob','cashier')`)
}

func nsEvent(t *testing.T, repo *POSRepo, id string, at time.Time, actor, till string) {
	t.Helper()
	ctx := context.Background()
	e := NoSaleEvent{ID: id, CreatedAt: b8At(at), ActorID: actor, TillID: till}
	if till != "" {
		if _, err := repo.ApplyJournaledNoSaleEvent(ctx, e); err != nil {
			t.Fatalf("journaled no-sale %s: %v", id, err)
		}
		return
	}
	if _, err := repo.InsertNoSaleEvent(ctx, nil, e); err != nil {
		t.Fatalf("insert no-sale %s: %v", id, err)
	}
}

func nsSeedSale(t *testing.T, d *db.DB, at time.Time) {
	t.Helper()
	b8Sale(t, d, "ns-sale", b8At(at), "completed", "sale", 10, 110)
	mustExec(t, d, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES ('ns-pay','ns-sale','cash',110,'GBP',0,?)`, b8At(at))
}

func TestEndOfDay_NoSaleCountAndPerCashier(t *testing.T) {
	ctx := context.Background()
	at := iwAnchor()
	day := ""

	run := func(name string, withEvents bool) EODReport {
		d := b8OpenDB(t, name)
		repo := NewPOSRepo(d.DB)
		nsSeedUsers(t, d)
		nsSeedSale(t, d, at)
		if withEvents {
			nsEvent(t, repo, "e1", at.Add(time.Minute), "ns-a", "")
			nsEvent(t, repo, "e2", at.Add(2*time.Minute), "ns-a", "")
			mustExec(t, d, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-r', 'Replica', 'h')`)
			nsEvent(t, repo, "e3", at.Add(3*time.Minute), "ns-b", "till-r")
		}
		day = b8ExpectedDay(t, d, at, 0, 0)
		rep, err := repo.EndOfDay(ctx, day)
		if err != nil {
			t.Fatalf("EndOfDay: %v", err)
		}
		return rep
	}
	control := run("ns-ctl.db", false)
	rep := run("ns-evt.db", true)

	if rep.NoSaleCount != 3 {
		t.Fatalf("NoSaleCount = %d, want 3", rep.NoSaleCount)
	}
	want := []NoSaleOperator{{CashierID: "ns-a", DisplayName: "Alice", Count: 2}, {CashierID: "ns-b", DisplayName: "Bob", Count: 1}}
	if !reflect.DeepEqual(rep.NoSaleOperators, want) {
		t.Fatalf("NoSaleOperators = %+v, want %+v", rep.NoSaleOperators, want)
	}
	rep.NoSaleCount, rep.NoSaleOperators = 0, nil
	if !reflect.DeepEqual(rep, control) {
		t.Fatalf("no-sale events changed another report field:\n got %+v\nwant %+v", rep, control)
	}
}

func TestEndOfDayInstant_NoSaleCountAndPerCashier(t *testing.T) {
	ctx := context.Background()
	at := iwAnchor()
	from, to := at.Add(-time.Hour), at.Add(time.Hour)

	run := func(name string, withEvents bool) EODReport {
		d := b8OpenDB(t, name)
		repo := NewPOSRepo(d.DB)
		nsSeedUsers(t, d)
		nsSeedSale(t, d, at)
		if withEvents {
			nsEvent(t, repo, "e1", at.Add(time.Minute), "ns-a", "")
			nsEvent(t, repo, "e2", at.Add(2*time.Minute), "ns-a", "")
			mustExec(t, d, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-r', 'Replica', 'h')`)
			nsEvent(t, repo, "e3", at.Add(3*time.Minute), "ns-b", "till-r")
		}
		rep, err := repo.EndOfDayInstant(ctx, from, to)
		if err != nil {
			t.Fatalf("EndOfDayInstant: %v", err)
		}
		return rep
	}
	control := run("nsi-ctl.db", false)
	rep := run("nsi-evt.db", true)
	if rep.NoSaleCount != 3 {
		t.Fatalf("NoSaleCount = %d, want 3", rep.NoSaleCount)
	}
	want := []NoSaleOperator{{CashierID: "ns-a", DisplayName: "Alice", Count: 2}, {CashierID: "ns-b", DisplayName: "Bob", Count: 1}}
	if !reflect.DeepEqual(rep.NoSaleOperators, want) {
		t.Fatalf("NoSaleOperators = %+v, want %+v", rep.NoSaleOperators, want)
	}
	rep.NoSaleCount, rep.NoSaleOperators = 0, nil
	if !reflect.DeepEqual(rep, control) {
		t.Fatalf("no-sale events changed another report field:\n got %+v\nwant %+v", rep, control)
	}
}

func TestEndOfDayRange_NoSaleTotalOnly(t *testing.T) {
	d := b8OpenDB(t, "ns-range.db")
	repo := NewPOSRepo(d.DB)
	nsSeedUsers(t, d)
	d1 := iwAnchor().AddDate(0, 0, -1)
	d2 := iwAnchor()
	nsEvent(t, repo, "r1", d1, "ns-a", "")
	nsEvent(t, repo, "r2", d2, "ns-b", "")
	nsEvent(t, repo, "r3", d2.Add(time.Minute), "ns-b", "")
	rep, err := repo.EndOfDayRange(context.Background(), b8ExpectedDay(t, d, d1, 0, 0), b8ExpectedDay(t, d, d2, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if rep.NoSaleCount != 3 {
		t.Fatalf("NoSaleCount = %d, want 3", rep.NoSaleCount)
	}
	if rep.NoSaleOperators != nil {
		t.Fatalf("range report must carry the total only, got %+v", rep.NoSaleOperators)
	}
}

func TestNoSale_InstantWindowHalfOpen(t *testing.T) {
	d := b8OpenDB(t, "ns-half.db")
	repo := NewPOSRepo(d.DB)
	from := iwAnchor()
	to := from.Add(time.Hour)
	nsEvent(t, repo, "h0", from.Add(-time.Second), "ns-a", "") // out
	nsEvent(t, repo, "h1", from, "ns-a", "")                   // in
	nsEvent(t, repo, "h2", to.Add(-time.Second), "ns-a", "")   // in
	nsEvent(t, repo, "h3", to, "ns-a", "")                     // out
	ctx := context.Background()
	n, err := repo.NoSaleCountForInstantWindow(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("count = %d, want 2 (window is [from,to))", n)
	}
	ops, err := repo.NoSaleOperatorsForInstantWindow(ctx, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Count != 2 {
		t.Fatalf("operators = %+v, want one row with count 2", ops)
	}
}

func TestNoSale_FirstCloseZeroFromIncludesHistory(t *testing.T) {
	d := b8OpenDB(t, "ns-first.db")
	repo := NewPOSRepo(d.DB)
	to := iwAnchor().Add(time.Hour)
	nsEvent(t, repo, "f-old", time.Date(2020, 6, 1, 12, 0, 0, 0, time.UTC), "ns-a", "")
	nsEvent(t, repo, "f-new", iwAnchor(), "ns-a", "")
	nsEvent(t, repo, "f-out", to, "ns-a", "")
	n, err := repo.NoSaleCountForInstantWindow(context.Background(), time.Time{}, to)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("count = %d, want 2 (old event included, event at `to` excluded)", n)
	}
}

func TestNoSale_LocalDayBoundary(t *testing.T) {
	d := b8OpenDB(t, "ns-localday.db")
	repo := NewPOSRepo(d.DB)
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	late := today.Add(-time.Minute) // 23:59 yesterday, local
	early := today                  // 00:00 today, local
	nsEvent(t, repo, "l1", late, "ns-a", "")
	nsEvent(t, repo, "l2", early, "ns-a", "")
	nsEvent(t, repo, "l3", early.Add(time.Minute), "ns-a", "")
	ctx := context.Background()
	prev, err := repo.EndOfDay(ctx, b8ExpectedDay(t, d, late, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	cur, err := repo.EndOfDay(ctx, b8ExpectedDay(t, d, early, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if prev.NoSaleCount != 1 || cur.NoSaleCount != 2 {
		t.Fatalf("prev=%d cur=%d, want 1 and 2", prev.NoSaleCount, cur.NoSaleCount)
	}
}

func TestNoSaleOperators_UnattributedAndUnknownActor(t *testing.T) {
	d := b8OpenDB(t, "ns-unattr.db")
	repo := NewPOSRepo(d.DB)
	at := iwAnchor()
	nsEvent(t, repo, "u1", at, "", "")
	nsEvent(t, repo, "u2", at.Add(time.Minute), "ghost-id", "")
	nsEvent(t, repo, "u3", at.Add(2*time.Minute), "ghost-id", "")
	ops, err := repo.NoSaleOperatorsForDay(context.Background(), b8ExpectedDay(t, d, at, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := []NoSaleOperator{{CashierID: "ghost-id", DisplayName: "ghost-id", Count: 2}, {CashierID: "", DisplayName: "", Count: 1}}
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("ops = %+v, want %+v", ops, want)
	}
}

func TestNoSaleOperators_DeterministicOrder(t *testing.T) {
	d := b8OpenDB(t, "ns-order.db")
	repo := NewPOSRepo(d.DB)
	nsSeedUsers(t, d)
	at := iwAnchor()
	// Tie on count 1: name order Alice < Bob < Zed regardless of insert order.
	mustExec(t, d, `INSERT INTO users (id, username, display_name, role) VALUES ('ns-z','zed','Zed','cashier')`)
	nsEvent(t, repo, "o1", at, "ns-z", "")
	nsEvent(t, repo, "o2", at.Add(time.Minute), "ns-b", "")
	nsEvent(t, repo, "o3", at.Add(2*time.Minute), "ns-a", "")
	nsEvent(t, repo, "o4", at.Add(3*time.Minute), "ns-z", "")
	ops, err := repo.NoSaleOperatorsForDay(context.Background(), b8ExpectedDay(t, d, at, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, o := range ops {
		got = append(got, o.DisplayName)
	}
	if strings.Join(got, ",") != "Zed,Alice,Bob" {
		t.Fatalf("order = %v, want Zed,Alice,Bob (count desc, then name)", got)
	}
}

func TestEndOfDay_NoSaleOmittedFromJSONWhenZero(t *testing.T) {
	d := b8OpenDB(t, "ns-json.db")
	repo := NewPOSRepo(d.DB)
	rep, err := repo.EndOfDay(context.Background(), b8ExpectedDay(t, d, iwAnchor(), 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rep)
	if strings.Contains(string(b), "no_sale_") {
		t.Fatalf("zero no-sale fields must be omitted from JSON: %s", b)
	}
}

// The window is local_date, not the UTC date of created_at: an open stamped
// 23:30 UTC yesterday that the shop's clock put on today (a shop east of
// UTC) counts on today's report, whatever the host's own TZ.
func TestNoSale_DateWindowUsesLocalDateNotUTCDate(t *testing.T) {
	d := b8OpenDB(t, "ns-localdate.db")
	repo := NewPOSRepo(d.DB)
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	yesterday := today.AddDate(0, 0, -1)
	mustExec(t, d, `INSERT INTO no_sale_events (id, created_at, local_date, actor_id) VALUES ('ld1', ?, ?, 'ns-a')`,
		b8At(today.Add(-30*time.Minute)), today.Format("2006-01-02"))
	ctx := context.Background()
	cur, err := repo.EndOfDay(ctx, today.Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	prev, err := repo.EndOfDay(ctx, yesterday.Format("2006-01-02"))
	if err != nil {
		t.Fatal(err)
	}
	if cur.NoSaleCount != 1 || prev.NoSaleCount != 0 {
		t.Fatalf("today=%d yesterday=%d, want 1 and 0 (window is local_date)", cur.NoSaleCount, prev.NoSaleCount)
	}
	if len(cur.NoSaleOperators) != 1 || len(prev.NoSaleOperators) != 0 {
		t.Fatalf("operators today=%+v yesterday=%+v", cur.NoSaleOperators, prev.NoSaleOperators)
	}
}

// A failing no-sale read never fails the close: the report still
// generates, with no no-sale figures (best-effort, like Operators).
func TestEndOfDay_NoSaleFailureNeverFailsClose(t *testing.T) {
	d := b8OpenDB(t, "ns-broken.db")
	repo := NewPOSRepo(d.DB)
	at := iwAnchor()
	nsSeedSale(t, d, at)
	mustExec(t, d, `ALTER TABLE no_sale_events RENAME TO no_sale_events_gone`)
	ctx := context.Background()
	rep, err := repo.EndOfDay(ctx, b8ExpectedDay(t, d, at, 0, 0))
	if err != nil {
		t.Fatalf("EndOfDay must not fail on a no-sale read error: %v", err)
	}
	if rep.SalesCount != 1 || rep.NoSaleCount != 0 || rep.NoSaleOperators != nil {
		t.Fatalf("date path: sales=%d nosale=%d ops=%+v", rep.SalesCount, rep.NoSaleCount, rep.NoSaleOperators)
	}
	irep, err := repo.EndOfDayInstant(ctx, at.Add(-time.Hour), at.Add(time.Hour))
	if err != nil {
		t.Fatalf("EndOfDayInstant must not fail on a no-sale read error: %v", err)
	}
	if irep.SalesCount != 1 || irep.NoSaleCount != 0 || irep.NoSaleOperators != nil {
		t.Fatalf("instant path: sales=%d nosale=%d ops=%+v", irep.SalesCount, irep.NoSaleCount, irep.NoSaleOperators)
	}
}
