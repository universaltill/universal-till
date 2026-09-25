package data

import (
	"context"
	"testing"
)

// Manage-shop catalog contract §3.2: categories.icon and
// categories.sell_screen_hidden (migration 041) must reach a satellite till
// through the ADR-0011 admin bundle, so a category hidden or given an icon
// on the main till looks the same on every till of the shop.
func TestAdminDumpApplyRoundTrip_CategoryIconAndHidden(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	mustExec(t, primary, `INSERT INTO categories (id, name, icon, sell_screen_hidden) VALUES ('cat-ic', 'Coffee', 'lucide:coffee', 1)`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("DumpAdmin: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("ApplyAdmin: %v", err)
	}
	var icon string
	var hidden int
	if err := replica.DB.QueryRow(`SELECT COALESCE(icon, ''), sell_screen_hidden FROM categories WHERE id = 'cat-ic'`).Scan(&icon, &hidden); err != nil {
		t.Fatalf("replica read: %v", err)
	}
	if icon != "lucide:coffee" || hidden != 1 {
		t.Fatalf("replica icon=%q hidden=%d, want the primary's lucide:coffee / 1", icon, hidden)
	}
}
