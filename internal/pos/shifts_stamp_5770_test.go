package pos

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#5770 x #5831 B1: a shift open/close must take its clock stamp AFTER it
// holds the write lock. A stamp read before the lock but committed after a Z's
// bound would belong to no Z window.
func TestShiftStampsTakenUnderWriteLock_5770(t *testing.T) {
	ctx := context.Background()
	d, err := db.Open(testsupport.MigratedDBFile(t, "shift-stamp-5770.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	execSQL(t, d.DB, `INSERT INTO registers (id, name, is_active) VALUES ('reg1', 'Main Register', 1)`)
	execSQL(t, d.DB, `INSERT INTO users (id, username, display_name, pin_hash, role) VALUES ('user1', 'cashier1', 'Cashier One', '', 'cashier')`)
	repo := data.NewPOSRepo(d.DB)

	var opened OpenedShift
	var closed ClosedShift
	for _, step := range []struct {
		name string
		run  func() error
		got  func() string
	}{
		{"open", func() (e error) {
			opened, e = OpenShift(ctx, d.DB, ShiftInput{RegisterID: "reg1", CashierID: "user1", OpeningCash: 1000})
			return
		}, func() string { return opened.OpenedAt }},
		{"close", func() (e error) {
			closed, e = CloseShift(ctx, d.DB, ShiftCloseInput{ShiftID: opened.ShiftID, ClosingCash: 1000})
			return
		}, func() string { return closed.ClosedAt }},
	} {
		release, err := repo.HoldWriteLock(ctx)
		if err != nil {
			t.Fatal(err)
		}
		errc := make(chan error, 1)
		go func() { errc <- step.run() }()
		time.Sleep(1200 * time.Millisecond)
		released := time.Now()
		release()
		if err := <-errc; err != nil {
			t.Fatalf("%s behind the held lock: %v", step.name, err)
		}
		at, err := time.Parse(time.RFC3339, step.got())
		if err != nil {
			t.Fatalf("%s stamp %q: %v", step.name, step.got(), err)
		}
		if at.Before(released.Truncate(time.Second)) {
			t.Fatalf("shift %s stamped %s, before the lock was released at %s: it would fall in no Z", step.name, step.got(), released.UTC().Format(time.RFC3339))
		}
	}
}
