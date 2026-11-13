package data

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

// ut-docs#5755: a shift's no-sale drawer opens. Every instant is anchored on
// iwAnchor() (the host's local noon), never a fixed date.

func TestNoSaleForShift_WindowRegisterAndBoundaries(t *testing.T) {
	ctx := context.Background()
	d := b8OpenDB(t, "shift-ns.db")
	repo := NewPOSRepo(d.DB)
	nsSeedUsers(t, d)
	mustExec(t, d, `INSERT INTO registers(id,name,is_active) VALUES('reg-a','A',1)`)
	mustExec(t, d, `INSERT INTO registers(id,name,is_active) VALUES('reg-b','B',1)`)

	t0 := iwAnchor().UTC()
	legacy := t0.Format("2006-01-02 15:04:05")
	// S1: legacy-format opened_at on purpose (a text compare would misorder it).
	mustExec(t, d, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,closed_at,opening_cash) VALUES('S1','reg-a','ns-a',?,?,0)`,
		legacy, b8At(t0.Add(60*time.Minute)))
	mustExec(t, d, `INSERT INTO shifts(id,register_id,cashier_id,opened_at,opening_cash) VALUES('S2','reg-a','ns-a',?,0)`,
		b8At(t0.Add(2*time.Hour)))

	evs := []struct {
		id, reg string
		at      time.Time
		actor   string
	}{
		{"before", "reg-a", t0.Add(-time.Second), "ns-a"},
		{"at-open", "reg-a", t0, "ns-a"},
		{"mid-b", "reg-a", t0.Add(10 * time.Minute), "ns-b"},
		{"no-actor", "reg-a", t0.Add(20 * time.Minute), ""},
		{"at-close", "reg-a", t0.Add(60 * time.Minute), "ns-b"},
		{"after", "reg-a", t0.Add(60*time.Minute + time.Second), "ns-a"},
		{"other-reg", "reg-b", t0.Add(30 * time.Minute), "ns-a"},
		{"no-reg", "", t0.Add(30 * time.Minute), "ns-a"},
		{"s2-ev", "reg-a", t0.Add(3 * time.Hour), "ns-a"},
	}
	for _, e := range evs {
		if _, err := repo.InsertNoSaleEvent(ctx, nil, NoSaleEvent{ID: e.id, CreatedAt: b8At(e.at), RegisterID: e.reg, ActorID: e.actor}); err != nil {
			t.Fatalf("insert %s: %v", e.id, err)
		}
	}

	total, ops, err := repo.NoSaleForShift(ctx, "S1")
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 {
		t.Fatalf("S1 total = %d, want 4", total)
	}
	want := []NoSaleOperator{{"ns-b", "Bob", 2}, {"", "", 1}, {"ns-a", "Alice", 1}}
	if !reflect.DeepEqual(ops, want) {
		t.Fatalf("S1 ops = %+v, want %+v", ops, want)
	}

	total, ops, err = repo.NoSaleForShift(ctx, "S2")
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || !reflect.DeepEqual(ops, []NoSaleOperator{{"ns-a", "Alice", 1}}) {
		t.Fatalf("S2 = %d %+v", total, ops)
	}

	total, ops, err = repo.NoSaleForShift(ctx, "nope")
	if err != nil || total != 0 || ops != nil {
		t.Fatalf("unknown shift = %d %+v %v", total, ops, err)
	}
}

func TestNoSaleForShift_QueryErrorIsReturned(t *testing.T) {
	d := b8OpenDB(t, "shift-ns-err.db")
	repo := NewPOSRepo(d.DB)
	mustExec(t, d, `DROP TABLE no_sale_events`)
	_, _, err := repo.NoSaleForShift(context.Background(), "S1")
	if err == nil || !strings.Contains(err.Error(), "no-sale for shift") {
		t.Fatalf("want wrapped error, got %v", err)
	}
}
