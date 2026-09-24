package data

import (
	"context"
	"testing"
)

// ut-docs#2500: categories.image_path rides the existing admin bundle
// (categories is an adminTable dumped whole-row), so a category image set
// on the primary reaches a satellite with no sync code of its own — the
// built-in icon then renders there; an uploaded file does not travel (D2),
// which the sell-screen resolver handles (internal/ui).
func TestAdminDumpApplyRoundTrip_CategoryImagePath(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	mustExec(t, primary, `INSERT INTO categories (id, name, image_path) VALUES ('cat-img', 'Coffee', '/public/assets/category-icons/coffee.svg')`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("DumpAdmin: %v", err)
	}
	if err := NewSyncAdminRepo(replica.DB).ApplyAdmin(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("ApplyAdmin: %v", err)
	}
	var got string
	if err := replica.DB.QueryRow(`SELECT COALESCE(image_path, '') FROM categories WHERE id = 'cat-img'`).Scan(&got); err != nil {
		t.Fatalf("replica read: %v", err)
	}
	if got != "/public/assets/category-icons/coffee.svg" {
		t.Fatalf("replica image_path = %q, want the primary's built-in icon path", got)
	}
}
