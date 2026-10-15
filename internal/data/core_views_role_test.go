package data

import (
	"context"
	"testing"
)

// ADR-0153 Decision 1, "The operator's role still applies to reads"
// (ut-docs#4143): every data class names the role permission an ai.agent
// plugin's operator needs for a view of that class. A cashier (sale-only)
// gets no sales, staff or till-health views; customers has no rule yet
// (no view exists), so it is refused to everyone (fail closed).
func TestAIReadRolePermission_EveryClassDecided_4143(t *testing.T) {
	want := map[string]struct {
		perm string
		ok   bool
	}{
		DataClassShop:       {"", true},
		DataClassCatalog:    {"", true},
		DataClassStock:      {"", true},
		DataClassSales:      {"reports", true},
		DataClassStaff:      {"user_management", true},
		DataClassTillHealth: {"audit", true},
		DataClassCustomers:  {"", false},
		"":                  {"", false},
		"unknown":           {"", false},
	}
	for _, c := range append([]string{DataClassShop}, AIDataClasses...) {
		if _, listed := want[c]; !listed {
			t.Errorf("data class %q has no expectation here: decide its role rule", c)
		}
	}
	for class, w := range want {
		perm, ok := AIReadRolePermission(class)
		if perm != w.perm || ok != w.ok {
			t.Errorf("AIReadRolePermission(%q) = %q, %v; want %q, %v", class, perm, ok, w.perm, w.ok)
		}
	}
}

// The seeded roles: a cashier holds none of the gating permissions, a
// manager all of them.
func TestAIReadRolePermission_SeededRoles_4143(t *testing.T) {
	ctx := context.Background()
	repo, _ := newAuthTestRepo(t)
	for _, class := range []string{DataClassSales, DataClassStaff, DataClassTillHealth} {
		perm, _ := AIReadRolePermission(class)
		for role, want := range map[string]bool{"cashier": false, "manager": true, "admin": true} {
			got, err := repo.HasPermission(ctx, role, perm)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("%s holds %s (class %s) = %v, want %v", role, perm, class, got, want)
			}
		}
	}
}
