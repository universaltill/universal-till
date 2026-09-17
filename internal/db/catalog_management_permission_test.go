package db

import (
	"path/filepath"
	"testing"
)

// TestCatalogManagementPermissionGrantedToManagerAdminSuperAdminOnly guards
// the catalog_management seed row added by ut-docs#2312, the same way
// TestTaxCodeManagementPermissionGrantedToManagerAdminSuperAdminOnly guards
// tax_code_management (057) and TestFiscalTSEOverridePermissionGranted-
// OwnerRolesOnly guards 046: this runs the REAL migration runner (Open
// applies every migration/seed on a fresh DB) rather than mirroring the
// expected grant set by hand in some fixture, so a bad seed (wrong role
// list, a typo, a dropped row) actually fails this test rather than only
// ever being checked against a hand-seeded test fixture that could drift
// from what a real till's migration run actually produces.
//
// ut-docs#2312: before this card, /designer, POST /api/buttons/{add,remove,
// reorder,move}, /ui/pos/tile-sheet and every mutating /api/catalog/* route
// carried NO permission check at all -- reachable and mutable by every
// signed-in operator, cashiers included. catalog_management closes that
// gap; this test pins its seeded grant to manager/admin/super_admin only,
// so an existing till's manager/admin keeps working with no behaviour
// change and a cashier is newly denied.
func TestCatalogManagementPermissionGrantedToManagerAdminSuperAdminOnly(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "catalog-management-permission.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	rows, err := d.DB.Query(`SELECT role, granted FROM role_permissions WHERE action = 'catalog_management' ORDER BY role`)
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
		t.Fatalf("catalog_management granted to roles %v, want exactly %v", got, want)
	}
	for role, granted := range want {
		if got[role] != granted {
			t.Fatalf("catalog_management for role %q = %d, want %d", role, got[role], granted)
		}
	}
	if granted, ok := got["cashier"]; ok {
		t.Fatalf("catalog_management must NOT be granted to cashier, but role_permissions has cashier granted=%d", granted)
	}

	// The action itself must also be registered in permission_actions (the
	// FK role_permissions.action references) -- a missing row here would
	// make AuthRepo.Can's own query simply find nothing and fail closed,
	// masking a real seeding bug as "cashier correctly denied" for every
	// role, not just cashier.
	var actionCount int
	if err := d.DB.QueryRow(`SELECT count(*) FROM permission_actions WHERE action = 'catalog_management'`).Scan(&actionCount); err != nil {
		t.Fatalf("query permission_actions: %v", err)
	}
	if actionCount != 1 {
		t.Fatalf("permission_actions has %d row(s) for catalog_management, want exactly 1", actionCount)
	}
}
