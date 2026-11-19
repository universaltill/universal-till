package plugins

import (
	"context"
	"errors"
	"testing"
)

func TestRollback_RefusesRevokedTarget(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	const id = "com.test.rbrev"
	seedInstalledPlugin(t, db, id, "RB", "2.0.0", "none", true)
	writeVersionDir(t, base, id, "1.0.0", true)
	writeVersionDir(t, base, id, "2.0.0", true)
	seedCatalogRow(t, db, id, "RB", "1.0.0", "")
	recordRevoked(t, db, id, "1.0.0")

	err := rm.Rollback(ctx, id, "1.0.0", "tester")
	if !errors.Is(err, ErrRevokedRelease) {
		t.Fatalf("want ErrRevokedRelease, got %v", err)
	}
	var version string
	var active int
	if err := db.QueryRow(`SELECT version, is_active FROM plugins WHERE id = ?`, id).Scan(&version, &active); err != nil {
		t.Fatal(err)
	}
	if version != "2.0.0" || active != 1 {
		t.Fatalf("row changed: version=%s active=%d", version, active)
	}
}
