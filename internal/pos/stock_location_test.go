package pos

import (
	"context"
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

// testStockLocationDB is a minimal schema for register→stock-location
// resolution: registers (with the location_id FK column) plus
// stock_locations (column-identical to 001_init.sql, same drift rule as
// testRegisterIdentityDB). internal/db cannot be imported here (replica.go
// already imports this package), so the real migrations are out of reach.
func testStockLocationDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, stmt := range []string{
		`CREATE TABLE stock_locations (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, is_active INTEGER NOT NULL DEFAULT 1, address_street TEXT, address_postcode TEXT, address_city TEXT);`,
		`CREATE TABLE registers (id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, location_id TEXT, is_active INTEGER NOT NULL DEFAULT 1, FOREIGN KEY (location_id) REFERENCES stock_locations (id));`,
		`INSERT INTO stock_locations(id, name, is_active) VALUES('loc_main', 'Main', 1)`,
		`INSERT INTO stock_locations(id, name, is_active) VALUES('loc_wh', 'Warehouse', 1)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	return db
}

// No register named at all: exactly today's EnsureStockLocation answer —
// the single hardcoded Main location. This is the regression guard for
// every shop that never touches Settings → Registers (ut-docs#2067).
func TestResolveStockLocationID_EmptyRegisterFallsBackToMain(t *testing.T) {
	ctx := context.Background()
	db := testStockLocationDB(t)

	got, err := ResolveStockLocationID(ctx, db, "")
	if err != nil {
		t.Fatalf("ResolveStockLocationID: %v", err)
	}
	if got != "loc_main" {
		t.Fatalf("expected the Main fallback loc_main, got %q", got)
	}
}

// A register that exists but has no location assigned: Main, unchanged.
func TestResolveStockLocationID_RegisterWithoutLocationFallsBackToMain(t *testing.T) {
	ctx := context.Background()
	db := testStockLocationDB(t)
	if _, err := db.Exec(`INSERT INTO registers(id, name, location_id) VALUES('reg1', 'Front Till', NULL)`); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveStockLocationID(ctx, db, "reg1")
	if err != nil {
		t.Fatalf("ResolveStockLocationID: %v", err)
	}
	if got != "loc_main" {
		t.Fatalf("expected the Main fallback for an unassigned register, got %q", got)
	}
}

// A register pinned to an active non-Main location resolves to THAT
// location — the whole point of the card.
func TestResolveStockLocationID_RegisterWithActiveLocationWins(t *testing.T) {
	ctx := context.Background()
	db := testStockLocationDB(t)
	if _, err := db.Exec(`INSERT INTO registers(id, name, location_id) VALUES('reg-wh', 'Warehouse Till', 'loc_wh')`); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveStockLocationID(ctx, db, "reg-wh")
	if err != nil {
		t.Fatalf("ResolveStockLocationID: %v", err)
	}
	if got != "loc_wh" {
		t.Fatalf("expected the register's own location loc_wh, got %q", got)
	}
}

// The register's location has since been deactivated: never the stale
// location — fall back to Main exactly as if nothing were assigned.
func TestResolveStockLocationID_DeactivatedLocationFallsBackToMain(t *testing.T) {
	ctx := context.Background()
	db := testStockLocationDB(t)
	for _, stmt := range []string{
		`INSERT INTO registers(id, name, location_id) VALUES('reg-wh', 'Warehouse Till', 'loc_wh')`,
		`UPDATE stock_locations SET is_active = 0 WHERE id = 'loc_wh'`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}

	got, err := ResolveStockLocationID(ctx, db, "reg-wh")
	if err != nil {
		t.Fatalf("ResolveStockLocationID: %v", err)
	}
	if got != "loc_main" {
		t.Fatalf("expected the Main fallback for a retired location, got %q (stale)", got)
	}
}

// A register id the shop has never heard of (a synced sale from a peer
// whose register never reached this till, say): Main, no error.
func TestResolveStockLocationID_UnknownRegisterFallsBackToMain(t *testing.T) {
	ctx := context.Background()
	db := testStockLocationDB(t)

	got, err := ResolveStockLocationID(ctx, db, "ghost")
	if err != nil {
		t.Fatalf("ResolveStockLocationID: %v", err)
	}
	if got != "loc_main" {
		t.Fatalf("expected the Main fallback for an unknown register, got %q", got)
	}
}
