package data

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

// regDevFixture: registers reg-default (self), r1 (dev-A), r2 (dev-B), plus a user.
func regDevFixture(t *testing.T) (*POSRepo, func(q string, a ...any)) {
	t.Helper()
	d, repo := openRegTestDB(t)
	x := func(q string, a ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, a...); err != nil {
			t.Fatalf("%.50s: %v", q, err)
		}
	}
	x(`INSERT INTO registers (id, name, is_active) VALUES ('reg-default','Main',1),('r1','phone',1),('r2','tablet',1)`)
	x(`INSERT INTO settings (key, value) VALUES ('sync.till_register_id','reg-default')`)
	x(`INSERT INTO users (id, username, display_name, role) VALUES ('u1','cashier1','Cashier One','cashier')`)
	return repo, x
}

func TestOneDeviceOneRegister(t *testing.T) {
	repo, x := regDevFixture(t)
	ctx := context.Background()
	if err := repo.LinkRegisterDevice(ctx, nil, "r1", "dev-A", "tA", RegisterLinkEnrol); err != nil {
		t.Fatal(err)
	}
	if err := repo.LinkRegisterDevice(ctx, nil, "r2", "dev-A", "tB", RegisterLinkEnrol); !errors.Is(err, ErrDeviceHasRegister) {
		t.Fatalf("second register for dev-A: err = %v, want ErrDeviceHasRegister", err)
	}
	var n int
	if err := repo.db.QueryRow(`SELECT COUNT(*) FROM register_devices WHERE device_id='dev-A'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows for dev-A = %d (%v), want 1", n, err)
	}
	_ = x
	got, ok, err := repo.RegisterByDevice(ctx, "dev-A")
	if err != nil || !ok || got.RegisterID != "r1" {
		t.Fatalf("RegisterByDevice = %+v %v %v, want r1", got, ok, err)
	}
	if _, ok, _ := repo.RegisterByDevice(ctx, ""); ok {
		t.Fatal("an empty device id must never match")
	}
}

func TestLinkRegisterDeviceKeepsOldDeviceAsSupersededBy(t *testing.T) {
	repo, _ := regDevFixture(t)
	ctx := context.Background()
	_ = repo.LinkRegisterDevice(ctx, nil, "r1", "dev-old", "tA", RegisterLinkEnrol)
	if err := repo.LinkRegisterDevice(ctx, nil, "r1", "dev-new", "tA", RegisterLinkEnrol); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := repo.RegisterByDevice(ctx, "dev-new")
	if !ok || got.SupersededBy != "dev-old" || got.RegisterID != "r1" {
		t.Fatalf("link = %+v, want r1 with superseded_by dev-old", got)
	}
}

func TestRegisterByTillFallsBackToEnrolmentAudit(t *testing.T) {
	repo, x := regDevFixture(t)
	x(`INSERT INTO audit_log (id, entity_type, entity_id, action, data_json) VALUES ('a','till','tX','till_enrolled','{"register_id":"r2"}')`)
	got, ok, err := repo.RegisterByTill(context.Background(), "tX")
	if err != nil || !ok || got.RegisterID != "r2" {
		t.Fatalf("RegisterByTill = %+v %v %v, want r2 from audit", got, ok, err)
	}
}

func TestRetireRegisterKeepsSales(t *testing.T) {
	repo, x := regDevFixture(t)
	ctx := context.Background()
	for _, id := range []string{"s1", "s2", "s3", "s4", "s5"} {
		x(`INSERT INTO sales (id, receipt_no, subtotal, total, register_id, sync_status) VALUES (?,?,0,0,'r1','synced')`, id, "R"+id)
	}
	x(`INSERT INTO shifts (id, register_id, cashier_id, opened_at, closed_at) VALUES ('sh1','r1','u1','2026-01-01T09:00:00Z','2026-01-01T17:00:00Z')`)
	res, err := repo.RetireRegister(ctx, "r1", RetiredManual, "", RetireOpts{ActorID: "u1"})
	if err != nil || res.AlreadyRetired {
		t.Fatalf("retire: %+v %v", res, err)
	}
	var active, sales, shifts, audits int
	var reason string
	_ = repo.db.QueryRow(`SELECT is_active FROM registers WHERE id='r1'`).Scan(&active)
	_ = repo.db.QueryRow(`SELECT COUNT(*) FROM sales WHERE register_id='r1'`).Scan(&sales)
	_ = repo.db.QueryRow(`SELECT COUNT(*) FROM shifts WHERE register_id='r1'`).Scan(&shifts)
	_ = repo.db.QueryRow(`SELECT retired_reason FROM register_devices WHERE register_id='r1'`).Scan(&reason)
	_ = repo.db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='register_retired' AND entity_id='r1'`).Scan(&audits)
	if active != 0 || sales != 5 || shifts != 1 || reason != "manual" || audits != 1 {
		t.Fatalf("active=%d sales=%d shifts=%d reason=%q audits=%d; want 0 5 1 manual 1", active, sales, shifts, reason, audits)
	}
	// a second retire is a no-op, not a second audit row
	if res, err := repo.RetireRegister(ctx, "r1", RetiredManual, "", RetireOpts{}); err != nil || !res.AlreadyRetired {
		t.Fatalf("second retire: %+v %v", res, err)
	}
}

func TestRetireRefusesSelf(t *testing.T) {
	repo, _ := regDevFixture(t)
	if _, err := repo.RetireRegister(context.Background(), "reg-default", RetiredManual, "", RetireOpts{}); !errors.Is(err, ErrRetireSelf) {
		t.Fatalf("err = %v, want ErrRetireSelf", err)
	}
	var active int
	_ = repo.db.QueryRow(`SELECT is_active FROM registers WHERE id='reg-default'`).Scan(&active)
	if active != 1 {
		t.Fatal("self register must stay active")
	}
}

func TestRetireRefusesLastActive(t *testing.T) {
	repo, x := regDevFixture(t)
	x(`UPDATE registers SET is_active = 0 WHERE id IN ('reg-default','r2')`)
	if _, err := repo.RetireRegister(context.Background(), "r1", RetiredManual, "", RetireOpts{}); !errors.Is(err, ErrRetireLast) {
		t.Fatalf("err = %v, want ErrRetireLast", err)
	}
}

func TestRetireRefusesOpenShiftAndUnsentSales(t *testing.T) {
	repo, x := regDevFixture(t)
	ctx := context.Background()
	x(`INSERT INTO shifts (id, register_id, cashier_id, opened_at) VALUES ('open','r1','u1','2026-01-01T09:00:00Z')`)
	if _, err := repo.RetireRegister(ctx, "r1", RetiredManual, "", RetireOpts{}); !errors.Is(err, ErrRetireOpenShift) {
		t.Fatalf("open shift: err = %v, want ErrRetireOpenShift", err)
	}
	x(`INSERT INTO sales (id, receipt_no, subtotal, total, register_id, sync_status) VALUES ('q','Rq',0,0,'r2','queued')`)
	if _, err := repo.RetireRegister(ctx, "r2", RetiredManual, "", RetireOpts{}); !errors.Is(err, ErrRetireUnsent) {
		t.Fatalf("unsent: err = %v, want ErrRetireUnsent", err)
	}
	var n int
	_ = repo.db.QueryRow(`SELECT COUNT(*) FROM registers WHERE is_active = 1`).Scan(&n)
	if n != 3 {
		t.Fatalf("active registers = %d, want 3 (nothing retired)", n)
	}
}

func TestReactivateClearsRetiredColumns(t *testing.T) {
	repo, _ := regDevFixture(t)
	ctx := context.Background()
	if _, err := repo.RetireRegister(ctx, "r1", RetiredOldEntry, "", RetireOpts{}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReactivateRegister(ctx, "r1", ""); err != nil {
		t.Fatal(err)
	}
	var reason string
	var at *string
	var active int
	_ = repo.db.QueryRow(`SELECT retired_reason, retired_at FROM register_devices WHERE register_id='r1'`).Scan(&reason, &at)
	_ = repo.db.QueryRow(`SELECT is_active FROM registers WHERE id='r1'`).Scan(&active)
	if reason != "" || at != nil || active != 1 {
		t.Fatalf("reason=%q retired_at=%v active=%d, want \"\" nil 1", reason, at, active)
	}
	// invariant: no active register carries a retired stamp
	var bad int
	_ = repo.db.QueryRow(`SELECT COUNT(*) FROM register_devices d JOIN registers r ON r.id=d.register_id WHERE r.is_active=1 AND d.retired_at IS NOT NULL`).Scan(&bad)
	if bad != 0 {
		t.Fatalf("%d active registers still marked retired", bad)
	}
}

func TestRegisterLastSaleCountsUnsent(t *testing.T) {
	repo, x := regDevFixture(t)
	x(`INSERT INTO sales (id, receipt_no, subtotal, total, register_id, sync_status, created_at) VALUES ('a','Ra',0,0,'r1','synced','2026-01-01T10:00:00Z'),('b','Rb',0,0,'r1','queued','2026-01-02T10:00:00Z')`)
	n, last, unsent, err := repo.RegisterLastSale(context.Background(), "r1")
	if err != nil || n != 2 || unsent != 1 || last.Format("2006-01-02") != "2026-01-02" {
		t.Fatalf("count=%d last=%v unsent=%d err=%v; want 2 2026-01-02 1", n, last, unsent, err)
	}
}

// ut-docs#7098: retire is soft. No new code may rewrite or delete sales or shifts.
func TestNoNewCodeWritesToSales(t *testing.T) {
	for _, f := range []string{"register_devices_repo.go", "../registeraudit/audit.go", "../pages/registers_ghosts.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			continue // a file of a later commit
		}
		s := strings.ToUpper(string(b))
		for _, bad := range []string{"UPDATE SALES", "DELETE FROM SALES", "DELETE FROM SHIFTS", "UPDATE SHIFTS", "DELETE FROM NO_SALE_EVENTS"} {
			if strings.Contains(s, bad) {
				t.Fatalf("%s contains %q", f, bad)
			}
		}
	}
}
