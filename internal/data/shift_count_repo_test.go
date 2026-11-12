package data

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// ut-docs#1043: ShiftCount reads one shift's stored count protocol. Instants
// derive from the clock (fixed-date guard).

func TestShiftCount(t *testing.T) {
	f := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	t0 := shiftWindowBase()
	at := func(d time.Duration) string { return b8At(t0.Add(d)) }
	mustExec(t, f.d, `INSERT INTO shifts(id, register_id, cashier_id, opened_at, closed_at, opening_cash, closing_cash, expected_cash, count_protocol) VALUES('S1','reg1','user1',?,?,500,10310,10000,'{"5000":2,"100":3,"10":1}')`, at(-3*time.Hour), at(-time.Hour))
	mustExec(t, f.d, `INSERT INTO shifts(id, register_id, cashier_id, opened_at, closed_at, opening_cash, closing_cash, expected_cash) VALUES('S2','reg1','user1',?,?,500,700,700)`, at(-6*time.Hour), at(-5*time.Hour))
	mustExec(t, f.d, `INSERT INTO shifts(id, register_id, cashier_id, opened_at, opening_cash) VALUES('S3','reg1','user1',?,500)`, at(-30*time.Minute))

	got, err := f.repo.ShiftCount(ctx, "S1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ShiftID != "S1" || got.OpenedAt != at(-3*time.Hour) || got.ClosedAt != at(-time.Hour) ||
		got.Counted.Minor() != 10310 || got.Protocol != `{"5000":2,"100":3,"10":1}` {
		t.Fatalf("S1 = %+v", got)
	}

	got, err = f.repo.ShiftCount(ctx, "S2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Protocol != "" || got.Counted.Minor() != 700 {
		t.Fatalf("S2 (NULL protocol) = %+v", got)
	}

	if _, err := f.repo.ShiftCount(ctx, "S3"); !errors.Is(err, ErrShiftNotClosed) {
		t.Fatalf("open shift err = %v, want ErrShiftNotClosed", err)
	}
	if _, err := f.repo.ShiftCount(ctx, "nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown shift err = %v, want sql.ErrNoRows", err)
	}
}
