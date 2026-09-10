package data

import (
	"context"
	"fmt"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#1368: GET /api/sync/admin used to run DumpAdmin's full 34-table
// scan + marshal + hash on EVERY replica poll (~30s each, per till) before
// the ?have= fingerprint check could short-circuit anything — the check
// only ever saved network transfer, never the server-side work. Migration
// 022 adds a one-row sync_admin_version generation counter bumped by
// AFTER INSERT/UPDATE/DELETE triggers on every adminTables entry, and
// DumpAdmin caches the last bundle keyed on that generation, so an
// unchanged poll costs one single-row SELECT.

func syncAdminGeneration(t *testing.T, d *db.DB) int64 {
	t.Helper()
	var g int64
	if err := d.QueryRow(`SELECT generation FROM sync_admin_version WHERE id = 1`).Scan(&g); err != nil {
		t.Fatalf("read sync_admin_version: %v", err)
	}
	return g
}

// Every table in adminTables carries all three triggers — a table added to
// the slice without a matching migration would silently serve stale
// bundles forever (the cache would never see a bump for it).
func TestSyncAdminVersion_EveryAdminTableHasTriggers(t *testing.T) {
	d := openMigratedDB(t, "triggers.db")
	var seeded int
	if err := d.QueryRow(`SELECT COUNT(*) FROM sync_admin_version WHERE id = 1`).Scan(&seeded); err != nil {
		t.Fatalf("sync_admin_version missing: %v", err)
	}
	if seeded != 1 {
		t.Fatalf("sync_admin_version seed row count = %d, want 1", seeded)
	}
	for _, at := range adminTables {
		for _, ev := range []string{"ins", "upd", "del"} {
			name := fmt.Sprintf("trg_sync_admin_version_%s_%s", at.name, ev)
			var n int
			if err := d.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = ? AND tbl_name = ?`, name, at.name).Scan(&n); err != nil {
				t.Fatalf("read sqlite_master: %v", err)
			}
			if n != 1 {
				t.Errorf("adminTables entry %q has no %s trigger %q — add it to a migration (see 022_sync_admin_version.sql)", at.name, ev, name)
			}
		}
	}
}

// Insert/update/delete on a representative sample — a plain table
// (tax_codes), a hasIsActive catalog table (items), and tills — each bump
// the generation exactly once per statement.
func TestSyncAdminVersion_TriggersBumpGeneration(t *testing.T) {
	d := openMigratedDB(t, "bump.db")
	steps := []struct{ name, sql string }{
		{"tax_codes insert", `INSERT INTO tax_codes (id, name, rate_basis_points) VALUES ('tx1', 'Standard', 2000)`},
		{"tax_codes update", `UPDATE tax_codes SET rate_basis_points = 1900 WHERE id = 'tx1'`},
		{"tax_codes delete", `DELETE FROM tax_codes WHERE id = 'tx1'`},
		{"items insert", `INSERT INTO items (id, sku, name, base_price) VALUES ('itm1', 'COLA', 'Cola Can', 120)`},
		{"items update", `UPDATE items SET is_active = 0 WHERE id = 'itm1'`},
		{"items delete", `DELETE FROM items WHERE id = 'itm1'`},
		{"tills insert", `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-a', 'Replica 1', 'hash-a')`},
		{"tills update name", `UPDATE tills SET name = 'Replica One' WHERE id = 'till-a'`},
		{"tills update enrolled_at", `UPDATE tills SET enrolled_at = '2026-01-01 00:00:00' WHERE id = 'till-a'`},
		{"tills delete", `DELETE FROM tills WHERE id = 'till-a'`},
	}
	prev := syncAdminGeneration(t, d)
	for _, s := range steps {
		mustExec(t, d, s.sql)
		got := syncAdminGeneration(t, d)
		if got != prev+1 {
			t.Fatalf("%s: generation %d -> %d, want %d", s.name, prev, got, prev+1)
		}
		prev = got
	}
}

// The critical regression: TillByBearerHash touches tills.last_seen_at on
// EVERY authenticated sync call, this endpoint included. A naive AFTER
// UPDATE ON tills trigger would bump the generation on every poll and the
// cache would never hit — the "cheap check" would always say "changed".
// last_seen_at and bearer_hash are both redactCols (never in the dump), so
// a change to either must NOT move the generation; name/enrolled_at must.
func TestSyncAdminVersion_TillAuthTouchDoesNotBump(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "touch.db")
	mustExec(t, d, `INSERT INTO tills (id, name, bearer_hash) VALUES ('till-a', 'Replica 1', 'hash-a')`)
	base := syncAdminGeneration(t, d)

	// Exactly what TillsRepo.TillByBearerHash does, then the real thing.
	mustExec(t, d, `UPDATE tills SET last_seen_at = '2026-09-10 12:00:00' WHERE id = 'till-a'`)
	if got := syncAdminGeneration(t, d); got != base {
		t.Fatalf("last_seen_at touch bumped generation %d -> %d (tills UPDATE trigger is not gated on name/enrolled_at)", base, got)
	}
	for range 3 {
		if _, ok, err := NewTillsRepo(d.DB).TillByBearerHash(ctx, "hash-a"); err != nil || !ok {
			t.Fatalf("auth: ok=%v err=%v", ok, err)
		}
	}
	if got := syncAdminGeneration(t, d); got != base {
		t.Fatalf("TillByBearerHash bumped generation %d -> %d", base, got)
	}
	// bearer_hash is redacted from the dump too — a rotated secret must not
	// force a rescan either.
	mustExec(t, d, `UPDATE tills SET bearer_hash = 'hash-b' WHERE id = 'till-a'`)
	if got := syncAdminGeneration(t, d); got != base {
		t.Fatalf("bearer_hash change bumped generation %d -> %d", base, got)
	}

	mustExec(t, d, `UPDATE tills SET name = 'Replica One' WHERE id = 'till-a'`)
	if got := syncAdminGeneration(t, d); got != base+1 {
		t.Fatalf("name change: generation %d -> %d, want %d", base, got, base+1)
	}
	// A no-op UPDATE (same name written back) is not a change either.
	mustExec(t, d, `UPDATE tills SET name = 'Replica One', last_seen_at = '2026-09-10 12:01:00' WHERE id = 'till-a'`)
	if got := syncAdminGeneration(t, d); got != base+1 {
		t.Fatalf("no-op name write bumped generation to %d, want %d", got, base+1)
	}
}

// Repo-level: an unchanged generation serves the cached bundle; any
// trigger-driven bump invalidates it. The middle step proves the cache is
// actually consulted (not just that two scans agree): a row written under
// a hand-rewound generation is invisible until the generation moves.
func TestAdminDumpCache_ServesUntilGenerationMoves(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "cache.db")
	mustExec(t, d, `INSERT INTO categories (id, name) VALUES ('cat1', 'Drinks')`)
	repo := NewSyncAdminRepo(d.DB)

	first, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	second, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("re-dump: %v", err)
	}
	if first.Fingerprint() != second.Fingerprint() {
		t.Fatalf("fingerprint moved with no writes: %s -> %s", first.Fingerprint(), second.Fingerprint())
	}
	if len(second.Tables["categories"]) != 1 {
		t.Fatalf("cached bundle lost content: %d categories", len(second.Tables["categories"]))
	}

	// Cache-hit proof: write a row, then rewind the counter to what the
	// cache saw. DumpAdmin must return the OLD bundle — it never touched
	// the categories table. (sync_admin_version itself has no trigger, so
	// the rewind is invisible to the mechanism.)
	gen := syncAdminGeneration(t, d)
	mustExec(t, d, `INSERT INTO categories (id, name) VALUES ('cat2', 'Snacks')`)
	mustExec(t, d, `UPDATE sync_admin_version SET generation = ? WHERE id = 1`, gen)
	stale, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump (rewound): %v", err)
	}
	if n := len(stale.Tables["categories"]); n != 1 {
		t.Fatalf("DumpAdmin rescanned on an unchanged generation: %d categories, want the cached 1", n)
	}

	// Now let the real trigger bump it: the new state is served.
	mustExec(t, d, `UPDATE categories SET name = 'Cold Drinks' WHERE id = 'cat1'`)
	fresh, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump (bumped): %v", err)
	}
	if fresh.Fingerprint() == first.Fingerprint() {
		t.Fatal("fingerprint did not move after a real catalog change")
	}
	if n := len(fresh.Tables["categories"]); n != 2 {
		t.Fatalf("fresh bundle has %d categories, want 2", n)
	}
	if got := fmt.Sprint(fresh.Tables["categories"][0]["name"]); got != "Cold Drinks" {
		t.Fatalf("fresh bundle name = %q, want Cold Drinks", got)
	}

	// A second repo on the same DB starts cold but converges on the same
	// content (the counter lives in the DB, the cache per process).
	other, err := NewSyncAdminRepo(d.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump (other repo): %v", err)
	}
	if other.Fingerprint() != fresh.Fingerprint() {
		t.Fatalf("second repo fingerprint %s != %s", other.Fingerprint(), fresh.Fingerprint())
	}
}

// A per-till settings write (filtered OUT of the dump by DumpAdmin) is an
// accepted false positive: it forces one rescan but the content — and so
// the ?have= fingerprint — stays identical. Pins that the false positive
// is harmless, not that it doesn't happen.
func TestAdminDumpCache_PerTillSettingRescanKeepsFingerprint(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "pertill.db")
	repo := NewSyncAdminRepo(d.DB)
	before, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("dump: %v", err)
	}
	mustExec(t, d, `INSERT INTO settings (key, value) VALUES ('printer.host', '10.0.0.9')`)
	after, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("re-dump: %v", err)
	}
	if before.Fingerprint() != after.Fingerprint() {
		t.Fatalf("per-till setting leaked into the bundle: %s -> %s", before.Fingerprint(), after.Fingerprint())
	}
}

// Defensive: a database with no sync_admin_version row (shouldn't happen —
// migrations run in order — but a hand-edited DB must not break sync)
// degrades to always-miss rather than erroring the whole sync path.
func TestAdminDumpCache_MissingVersionRowDegradesToRescan(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "norow.db")
	mustExec(t, d, `DELETE FROM sync_admin_version`)
	repo := NewSyncAdminRepo(d.DB)
	if _, err := repo.DumpAdmin(ctx); err != nil {
		t.Fatalf("dump without version row: %v", err)
	}
	mustExec(t, d, `INSERT INTO categories (id, name) VALUES ('cat1', 'Drinks')`)
	b, err := repo.DumpAdmin(ctx)
	if err != nil {
		t.Fatalf("re-dump without version row: %v", err)
	}
	if len(b.Tables["categories"]) != 1 {
		t.Fatalf("without a version row DumpAdmin must always rescan; got %d categories", len(b.Tables["categories"]))
	}
}
