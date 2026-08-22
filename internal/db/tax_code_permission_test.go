package db

import (
	"path/filepath"
	"testing"
)

// TestTaxCodeManagementPermissionGrantedToManagerAdminSuperAdminOnly guards
// migration 057 (ut-docs#259) the same way TestFiscalTSEOverridePermission-
// GrantedToOwnerRolesOnly guards 046: this runs the REAL migration runner
// (Open applies every migration on a fresh DB) rather than mirroring the
// expected grant set by hand in some fixture, so a bad migration file (wrong
// role list, a typo, a dropped WHERE clause) actually fails this test.
//
// This exists because internal/pages/tax_codes_page_test.go's role-gating
// test (TestTaxCodesPage_RealSessionGatesByRole) only proves canPerform
// reads whatever role_permissions rows already exist -- its DB fixture
// (internal/pages/ui_smoke_test.go's seedForPages) hand-seeds
// tax_code_management for manager/admin/super_admin independently of
// migration 057's own SQL, so that test alone would still pass even if 057
// were deleted, or granted the action to the wrong roles entirely (verified
// live while testing this card: inverting 057 to grant only 'cashier'
// left every subtest of TestTaxCodesPage_RealSessionGatesByRole green).
func TestTaxCodeManagementPermissionGrantedToManagerAdminSuperAdminOnly(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "tax-code-permission.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	rows, err := d.DB.Query(`SELECT role, granted FROM role_permissions WHERE action = 'tax_code_management' ORDER BY role`)
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
		t.Fatalf("tax_code_management granted to roles %v, want exactly %v", got, want)
	}
	for role, granted := range want {
		if got[role] != granted {
			t.Fatalf("tax_code_management for role %q = %d, want %d", role, got[role], granted)
		}
	}
	if granted, ok := got["cashier"]; ok {
		t.Fatalf("tax_code_management must NOT be granted to cashier, but role_permissions has cashier granted=%d", granted)
	}
}
