package db

import (
	"path/filepath"
	"testing"
)

// TestWorkerAllocationPermissionGrantedToManagerAdminSuperAdminOnly guards
// migration 066 (ut-docs#964) the same way
// TestTaxCodeManagementPermissionGrantedToManagerAdminSuperAdminOnly guards
// 057: this runs the REAL migration runner (Open applies every migration on
// a fresh DB) rather than mirroring the expected grant set by hand in some
// fixture, so a bad migration file (wrong role list, a typo, a dropped WHERE
// clause) actually fails this test.
func TestWorkerAllocationPermissionGrantedToManagerAdminSuperAdminOnly(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "worker-allocation-permission.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	rows, err := d.DB.Query(`SELECT role, granted FROM role_permissions WHERE action = 'worker_allocation' ORDER BY role`)
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

	want := map[string]int{"manager": 1, "admin": 1, "super_admin": 1}
	if len(got) != len(want) {
		t.Fatalf("worker_allocation granted to roles %v, want exactly %v", got, want)
	}
	for role, granted := range want {
		if got[role] != granted {
			t.Fatalf("worker_allocation for role %q = %d, want %d", role, got[role], granted)
		}
	}
	if granted, ok := got["cashier"]; ok {
		t.Fatalf("worker_allocation must NOT be granted to cashier, but role_permissions has cashier granted=%d", granted)
	}
}
