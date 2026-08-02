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

// TestRoleCheckFromSettings_* covers the ACTUAL role-check logic wired into
// production (internal/app.Run passes this to discovery.NewAdvertiser) —
// not just Advertiser's tick logic against a synthetic injected bool
// (that's advertiser_test.go's job). Before this, the "empty
// sync.primary_url means primary" rule was duplicated as an inline closure
// in app.go with zero test coverage of its own: nothing would fail if that
// closure's condition were accidentally inverted. This exercises the real
// settings-backed rule directly, against a real *data.SettingsRepo, the
// same way app.go wires it.
func TestRoleCheckFromSettings_TrueWhenPrimaryURLUnset(t *testing.T) {
	settings := openTestSettings(t)
	check := RoleCheckFromSettings(settings)

	if !check(context.Background()) {
		t.Fatal("expected primary role (true) when sync.primary_url is unset")
	}
}

func TestRoleCheckFromSettings_FalseWhenPrimaryURLSet(t *testing.T) {
	settings := openTestSettings(t)
	ctx := context.Background()
	if err := settings.Set(ctx, "sync.primary_url", "https://primary.example"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	check := RoleCheckFromSettings(settings)

	if check(ctx) {
		t.Fatal("expected replica role (false) when sync.primary_url is set")
	}
}

func TestRoleCheckFromSettings_TrueWhenPrimaryURLWhitespaceOnly(t *testing.T) {
	settings := openTestSettings(t)
	ctx := context.Background()
	if err := settings.Set(ctx, "sync.primary_url", "   "); err != nil {
		t.Fatalf("Set: %v", err)
	}
	check := RoleCheckFromSettings(settings)

	if !check(ctx) {
		t.Fatal("expected primary role (true) when sync.primary_url is whitespace-only")
	}
}
