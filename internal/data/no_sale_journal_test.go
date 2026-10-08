package data

import (
	"context"
	"slices"
	"testing"
	"time"
)

// ut-docs#3562: a replica journals its own no-sale drawer opens to the main
// till (LAN sync D3, POST /api/sync/no-sales), so the main till's ADR-0111
// rollup counts opens done on every till, not just its own.

func TestLocalNoSaleEventsSince_OwnRowsOrderedByCompositeCursor(t *testing.T) {
	d := b8OpenDB(t, "no-sale-local-since.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	ins := func(id, at, till string) {
		t.Helper()
		if _, err := repo.InsertNoSaleEvent(ctx, nil, NoSaleEvent{ID: id, CreatedAt: at, TillID: till, RegisterID: "reg-A"}); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	// Two opens in the SAME second (ids b < c), one later, one earlier, and
	// a journaled row from another till that must never be re-pushed.
	ins("ns-c", "2026-09-01T10:00:00Z", "")
	ins("ns-b", "2026-09-01T10:00:00Z", "")
	ins("ns-d", "2026-09-01T11:00:00Z", "")
	ins("ns-a", "2026-09-01T09:00:00Z", "")
	ins("ns-remote", "2026-09-01T10:30:00Z", "till-other")

	ids := func(evs []NoSaleEvent) []string {
		out := make([]string, 0, len(evs))
		for _, e := range evs {
			out = append(out, e.ID)
		}
		return out
	}
	all, err := repo.LocalNoSaleEventsSince(ctx, "", "", 50)
	if err != nil {
		t.Fatalf("LocalNoSaleEventsSince: %v", err)
	}
	if got, want := ids(all), []string{"ns-a", "ns-b", "ns-c", "ns-d"}; !slices.Equal(got, want) {
		t.Fatalf("from the start = %v, want %v (own rows only, ordered by (created_at, id))", got, want)
	}
	if all[1].RegisterID != "reg-A" || all[1].CreatedAt != "2026-09-01T10:00:00Z" {
		t.Fatalf("row fields not read back: %+v", all[1])
	}

	// Cursor at the FIRST of the two same-second rows: the second must not
	// be lost (a created_at-only cursor would skip it).
	next, err := repo.LocalNoSaleEventsSince(ctx, "2026-09-01T10:00:00Z", "ns-b", 50)
	if err != nil {
		t.Fatalf("LocalNoSaleEventsSince(cursor): %v", err)
	}
	if got, want := ids(next), []string{"ns-c", "ns-d"}; !slices.Equal(got, want) {
		t.Fatalf("after (10:00, ns-b) = %v, want %v", got, want)
	}

	limited, err := repo.LocalNoSaleEventsSince(ctx, "", "", 2)
	if err != nil {
		t.Fatalf("LocalNoSaleEventsSince(limit): %v", err)
	}
	if got, want := ids(limited), []string{"ns-a", "ns-b"}; !slices.Equal(got, want) {
		t.Fatalf("limit 2 = %v, want %v", got, want)
	}
}

func TestApplyJournaledNoSaleEvent_IdempotentAndStoresTill(t *testing.T) {
	d := b8OpenDB(t, "no-sale-apply-journal.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.Local)
	e := NoSaleEvent{
		ID: "ns-remote-1", CreatedAt: b8At(at), RegisterID: "reg-R", TillID: "till-7",
		ActorID: "u1", ApproverID: "m1", Reason: "float",
	}

	applied, err := repo.ApplyJournaledNoSaleEvent(ctx, e)
	if err != nil || !applied {
		t.Fatalf("first apply = (%v, %v), want (true, nil)", applied, err)
	}
	applied, err = repo.ApplyJournaledNoSaleEvent(ctx, e)
	if err != nil || applied {
		t.Fatalf("second apply = (%v, %v), want (false, nil) — idempotent by id", applied, err)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM no_sale_events WHERE id = 'ns-remote-1'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d (%v), want 1", n, err)
	}
	var till, localDate, reg, actor, approver, reason string
	if err := d.DB.QueryRow(`SELECT till_id, local_date, register_id, actor_id, approver_id, reason FROM no_sale_events WHERE id = 'ns-remote-1'`).
		Scan(&till, &localDate, &reg, &actor, &approver, &reason); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if till != "till-7" || reg != "reg-R" || actor != "u1" || approver != "m1" || reason != "float" {
		t.Fatalf("stored = till %q reg %q actor %q approver %q reason %q", till, reg, actor, approver, reason)
	}
	if want := b8ExpectedDay(t, d, at, 0, 0); localDate != want {
		t.Fatalf("local_date = %q, want %q (InsertNoSaleEvent's rule)", localDate, want)
	}

	// A journaled row is not this till's own: never re-pushed.
	own, err := repo.LocalNoSaleEventsSince(ctx, "", "", 50)
	if err != nil || len(own) != 0 {
		t.Fatalf("LocalNoSaleEventsSince after a journaled apply = %+v (%v), want none", own, err)
	}

	for name, bad := range map[string]NoSaleEvent{
		"no id":         {CreatedAt: e.CreatedAt, TillID: "till-7"},
		"no created_at": {ID: "x1", TillID: "till-7"},
		"no till":       {ID: "x2", CreatedAt: e.CreatedAt},
	} {
		if _, err := repo.ApplyJournaledNoSaleEvent(ctx, bad); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
