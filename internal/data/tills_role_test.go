package data

import (
	"context"
	"testing"
	"time"
)

// ut-docs#2781 (migration 067): every till has a role — additional (the
// default, what every till enrolled before 067 is) or satellite — and a
// pending pair request carries the role it asked for.

func TestTillsRole_DefaultsToAdditional(t *testing.T) {
	d := openMigratedDB(t, "tills-role-default.db")
	// A row written the pre-067 way, naming no role.
	mustExec(t, d, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-a', 'Till 2', 'hash-a')`)
	var role string
	if err := d.QueryRow(`SELECT role FROM tills WHERE id = 'till-a'`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != TillRoleAdditional {
		t.Fatalf("default role = %q, want %q", role, TillRoleAdditional)
	}
	mustExec(t, d, `INSERT INTO pending_pairings (id, device_name, commitment, requested_at, expires_at) VALUES ('p1', 'Kiosk', 'c', '2026-01-01T00:00:00Z', '2099-01-01T00:00:00Z')`)
	if err := d.QueryRow(`SELECT requested_role FROM pending_pairings WHERE id = 'p1'`).Scan(&role); err != nil {
		t.Fatal(err)
	}
	if role != TillRoleAdditional {
		t.Fatalf("default requested_role = %q, want %q", role, TillRoleAdditional)
	}
}

func TestTillsRole_SatelliteRoundTrips(t *testing.T) {
	d := openMigratedDB(t, "tills-role-satellite.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	id, err := repo.InsertTillWithRole(ctx, "Kiosk", "hash-k", TillRoleSatellite)
	if err != nil {
		t.Fatal(err)
	}
	if role, ok, err := repo.RoleByID(ctx, id); err != nil || !ok || role != TillRoleSatellite {
		t.Fatalf("RoleByID = %q ok=%v err=%v, want satellite", role, ok, err)
	}
	list, err := repo.ListTills(ctx)
	if err != nil || len(list) != 1 || list[0].Role != TillRoleSatellite {
		t.Fatalf("ListTills = %+v err=%v, want one satellite", list, err)
	}
	got, ok, err := repo.TillByBearerHash(ctx, "hash-k")
	if err != nil || !ok || got.Role != TillRoleSatellite {
		t.Fatalf("TillByBearerHash = %+v ok=%v err=%v, want satellite", got, ok, err)
	}
}

func TestTillsRole_CheckConstraintRejectsBogusValue(t *testing.T) {
	d := openMigratedDB(t, "tills-role-check.db")
	if _, err := d.Exec(`INSERT INTO tills (id, name, bearer_hash, role) VALUES ('till-b', 'Till 3', 'hash-b', 'replica')`); err == nil {
		t.Fatal("tills.role accepted 'replica'; the CHECK must allow only additional/satellite")
	}
	if _, err := d.Exec(`INSERT INTO pending_pairings (id, device_name, commitment, requested_at, expires_at, requested_role) VALUES ('p2', 'X', 'c', '2026-01-01T00:00:00Z', '2099-01-01T00:00:00Z', 'main')`); err == nil {
		t.Fatal("pending_pairings.requested_role accepted 'main'")
	}
	mustExec(t, d, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-c', 'Till 4', 'hash-c')`)
	if _, err := d.Exec(`UPDATE tills SET role = 'kiosk' WHERE id = 'till-c'`); err == nil {
		t.Fatal("an UPDATE to a bogus role must be refused too")
	}
	if _, err := NewTillsRepo(d.DB).InsertTillWithRole(context.Background(), "Till 5", "hash-5", "bogus"); err == nil {
		t.Fatal("InsertTillWithRole must refuse a bogus role")
	}
}

// SetRole reports the old role, and a real change — only a real change —
// bumps sync_admin_version, so joined tills pull the new role while a
// repeated write costs nothing (migration 067's trigger; 023's tills UPDATE
// trigger watches only name/enrolled_at).
func TestTillsRepo_SetRoleBumpsAdminGenerationOnlyOnChange(t *testing.T) {
	d := openMigratedDB(t, "tills-role-set.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	id, err := repo.InsertTill(ctx, "Till 2", "hash-2")
	if err != nil {
		t.Fatal(err)
	}
	base := syncAdminGeneration(t, d)
	old, ok, err := repo.SetRole(ctx, id, TillRoleSatellite)
	if err != nil || !ok || old != TillRoleAdditional {
		t.Fatalf("SetRole = old %q ok=%v err=%v", old, ok, err)
	}
	if got := syncAdminGeneration(t, d); got != base+1 {
		t.Fatalf("role change: generation %d -> %d, want %d", base, got, base+1)
	}
	if old, _, _ := repo.SetRole(ctx, id, TillRoleSatellite); old != TillRoleSatellite {
		t.Fatalf("second SetRole old = %q, want satellite", old)
	}
	if got := syncAdminGeneration(t, d); got != base+1 {
		t.Fatalf("same-role write bumped generation to %d, want %d", got, base+1)
	}
	if _, ok, err := repo.SetRole(ctx, "nope", TillRoleSatellite); err != nil || ok {
		t.Fatalf("unknown id: ok=%v err=%v, want not-found", ok, err)
	}
	if _, _, err := repo.SetRole(ctx, id, "bogus"); err == nil {
		t.Fatal("SetRole must refuse a bogus role")
	}
}

// The approve-to-pair queue carries the requested role, approval can
// override it, and the approved token resolves to the final role.
func TestPairingRepo_RoleFlowsFromRequestThroughApproval(t *testing.T) {
	d := openMigratedDB(t, "pairing-role.db")
	ctx := context.Background()
	repo := NewPairingRepo(d.DB)
	id, err := repo.CreatePendingRequestWithRole(ctx, "Kiosk", "c1", TillRoleSatellite, "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	list, err := repo.ListPending(ctx)
	if err != nil || len(list) != 1 || list[0].RequestedRole != TillRoleSatellite {
		t.Fatalf("ListPending = %+v err=%v, want requested satellite", list, err)
	}
	if role, ok, _ := repo.RoleForToken(ctx, "tok-1"); ok {
		t.Fatalf("an unapproved row must not resolve a token, got %q", role)
	}
	if err := repo.ApproveWithRole(ctx, id, "tok-1", TillRoleAdditional, time.Minute); err != nil {
		t.Fatal(err)
	}
	if role, ok, err := repo.RoleForToken(ctx, "tok-1"); err != nil || !ok || role != TillRoleAdditional {
		t.Fatalf("RoleForToken = %q ok=%v err=%v, want the manager's additional", role, ok, err)
	}

	// A blank role at approval keeps the requested one.
	id2, err := repo.CreatePendingRequestWithRole(ctx, "Order station", "c2", TillRoleSatellite, "", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.Approve(ctx, id2, "tok-2", time.Minute); err != nil {
		t.Fatal(err)
	}
	if role, ok, _ := repo.RoleForToken(ctx, "tok-2"); !ok || role != TillRoleSatellite {
		t.Fatalf("RoleForToken after plain Approve = %q ok=%v, want satellite", role, ok)
	}
	if _, err := repo.CreatePendingRequestWithRole(ctx, "X", "c3", "bogus", "", time.Minute); err == nil {
		t.Fatal("a bogus requested role must be refused")
	}
}
