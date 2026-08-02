package discovery

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/universaltill/universal-till/internal/data"
)

// openTestSettings mirrors internal/data/settings_repo_test.go's minimal
// in-memory setup — this package only ever touches the settings table via
// *data.SettingsRepo's own Get/Set, never raw SQL, so a real schema for the
// rest of the DB is unnecessary here.
func openTestSettings(t *testing.T) *data.SettingsRepo {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatalf("create settings: %v", err)
	}
	return data.NewSettingsRepo(db)
}

func TestTillID_CreatesAndPersistsOnFirstCall(t *testing.T) {
	settings := openTestSettings(t)
	ctx := context.Background()

	id, err := TillID(ctx, settings)
	if err != nil {
		t.Fatalf("TillID: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty generated till id")
	}

	stored, ok, err := settings.Get(ctx, TillIDSettingKey)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !ok || stored != id {
		t.Fatalf("expected the generated id %q persisted under %q, got %q (ok=%v)", id, TillIDSettingKey, stored, ok)
	}
}

func TestTillID_IsStableAcrossCalls(t *testing.T) {
	settings := openTestSettings(t)
	ctx := context.Background()

	first, err := TillID(ctx, settings)
	if err != nil {
		t.Fatalf("TillID (first): %v", err)
	}
	second, err := TillID(ctx, settings)
	if err != nil {
		t.Fatalf("TillID (second): %v", err)
	}
	if first != second {
		t.Fatalf("expected the same id on every call (get-or-create), got %q then %q", first, second)
	}
}

func TestTillID_ReturnsExistingValueWithoutOverwriting(t *testing.T) {
	settings := openTestSettings(t)
	ctx := context.Background()

	const preSeeded = "till-already-here"
	if err := settings.Set(ctx, TillIDSettingKey, preSeeded); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, err := TillID(ctx, settings)
	if err != nil {
		t.Fatalf("TillID: %v", err)
	}
	if got != preSeeded {
		t.Fatalf("expected the pre-existing id %q preserved, got %q", preSeeded, got)
	}
}

func TestStoreNameOrDefault_FallsBackWhenUnset(t *testing.T) {
	settings := openTestSettings(t)
	ctx := context.Background()

	if got := storeNameOrDefault(ctx, settings); got != "this shop" {
		t.Fatalf(`expected fallback "this shop", got %q`, got)
	}

	if err := settings.Set(ctx, "store.name", "Task Runner"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := storeNameOrDefault(ctx, settings); got != "Task Runner" {
		t.Fatalf("expected the configured store name, got %q", got)
	}
}
