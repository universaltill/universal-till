package db

import (
	"path/filepath"
	"testing"
)

// TestFiscalTSEOverridePermissionGrantedToOwnerRolesOnly guards migration
// 046 against the exact regression ADR-0048 Decision 3 calls out by name:
// the fiscal_tse_override action must be seeded to admin/super_admin ONLY,
// deliberately breaking from the manager-inclusive pattern every earlier
// permission migration (039/042/043/044/045) follows. A typo re-adding
// 'manager' here would silently hand a manager's PIN the ability to grant
// an owner-only override — this test runs the real migration runner
// (Open applies every migration on a fresh DB) rather than mirroring the
// expected grant set by hand, so a bad migration file actually fails it.
func TestFiscalTSEOverridePermissionGrantedToOwnerRolesOnly(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "fiscal-permission.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	rows, err := d.DB.Query(`SELECT role, granted FROM role_permissions WHERE action = 'fiscal_tse_override' ORDER BY role`)
	if err != nil {
		t.Fatalf("query role_permissions: %v", err)
	}
	defer rows.Close()

	got := map[string]int{}
	for rows.Next() {
		var role string
		var granted int
		if err := rows.Scan(&role, &granted); err != nil {
			t.Fatalf("scan role_permissions row: %v", err)
		}
		got[role] = granted
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate role_permissions: %v", err)
	}

	want := map[string]int{"admin": 1, "super_admin": 1}
	if len(got) != len(want) {
		t.Fatalf("fiscal_tse_override granted to roles %v, want exactly %v", got, want)
	}
	for role, granted := range want {
		if got[role] != granted {
			t.Fatalf("fiscal_tse_override for role %q = %d, want %d", role, got[role], granted)
		}
	}
	if granted, ok := got["manager"]; ok {
		t.Fatalf("fiscal_tse_override must NOT be granted to manager (ADR-0048 Decision 3), but role_permissions has manager granted=%d", granted)
	}
}
