package data

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	_ "modernc.org/sqlite"
)

// newAuditTestDB creates a minimal in-memory schema with just the tables
// ListAudit/InsertAudit need — matching this package's existing convention
// of a small hand-rolled schema per test file rather than the full migration
// set (see e.g. pos_repo_search_test.go's testsupport.NewCatalogTestDB).
func newAuditTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`CREATE TABLE users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, display_name TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'cashier', pin_hash TEXT, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT NOT NULL, entity_id TEXT NOT NULL, action TEXT NOT NULL, data_json TEXT, created_at TEXT NOT NULL DEFAULT (datetime('now')), blocked_actor_id TEXT, FOREIGN KEY (actor_id) REFERENCES users (id));`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	return db
}

func TestPOSRepo_ListAudit_FiltersAndOrdersNewestFirst(t *testing.T) {
	db := newAuditTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	if _, err := db.Exec(`INSERT INTO users(id, username, display_name, role) VALUES ('u1','alice','Alice','manager')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	if err := repo.InsertAudit(ctx, nil, "u1", "plugin", "com.x.faq", "install", map[string]any{"version": "1.0.0"}, "2026-01-01T10:00:00Z", ""); err != nil {
		t.Fatalf("insert audit 1: %v", err)
	}
	if err := repo.InsertAudit(ctx, nil, "", "sale", "R-1", "void", nil, "2026-01-01T11:00:00Z", ""); err != nil {
		t.Fatalf("insert audit 2: %v", err)
	}
	if err := repo.InsertAudit(ctx, nil, "u1", "plugin", "com.x.faq", "uninstall", nil, "2026-01-01T12:00:00Z", ""); err != nil {
		t.Fatalf("insert audit 3: %v", err)
	}

	// No filter: newest first, all 3.
	all, err := repo.ListAudit(ctx, AuditFilters{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(all))
	}
	if all[0].Action != "uninstall" || all[2].Action != "install" {
		t.Fatalf("expected newest-first ordering, got %+v", all)
	}
	if all[0].ActorName != "Alice" {
		t.Fatalf("expected actor name resolved via join, got %q", all[0].ActorName)
	}
	if all[1].ActorName != "" {
		t.Fatalf("expected empty actor name for a NULL actor_id (plugin-originated entries), got %q", all[1].ActorName)
	}

	// entity_type filter.
	plugins, err := repo.ListAudit(ctx, AuditFilters{EntityType: "plugin"})
	if err != nil {
		t.Fatalf("ListAudit entity_type: %v", err)
	}
	if len(plugins) != 2 {
		t.Fatalf("expected 2 plugin entries, got %d", len(plugins))
	}

	// actor_id filter.
	byActor, err := repo.ListAudit(ctx, AuditFilters{ActorID: "u1"})
	if err != nil {
		t.Fatalf("ListAudit actor_id: %v", err)
	}
	if len(byActor) != 2 {
		t.Fatalf("expected 2 entries for actor u1, got %d", len(byActor))
	}

	// action substring filter.
	byAction, err := repo.ListAudit(ctx, AuditFilters{Action: "install"})
	if err != nil {
		t.Fatalf("ListAudit action: %v", err)
	}
	if len(byAction) != 2 { // "install" and "uninstall" both contain "install"
		t.Fatalf("expected 2 entries matching 'install', got %d: %+v", len(byAction), byAction)
	}

	// date range filter.
	byRange, err := repo.ListAudit(ctx, AuditFilters{Since: "2026-01-01T11:00:00Z", Until: "2026-01-01T11:30:00Z"})
	if err != nil {
		t.Fatalf("ListAudit range: %v", err)
	}
	if len(byRange) != 1 || byRange[0].Action != "void" {
		t.Fatalf("expected exactly the 11:00 sale-void entry, got %+v", byRange)
	}

	// A bare "YYYY-MM-DD" Until (what <input type="date"> actually submits)
	// must include the whole day, not exclude it — created_at is a full
	// RFC3339 timestamp, so an unmodified date-only Until compares as
	// LESS than every timestamp on that same day.
	untilBareDate, err := repo.ListAudit(ctx, AuditFilters{Until: "2026-01-01"})
	if err != nil {
		t.Fatalf("ListAudit bare-date until: %v", err)
	}
	if len(untilBareDate) != 3 {
		t.Fatalf("expected all 3 entries (all created on 2026-01-01) with Until=2026-01-01, got %d: %+v", len(untilBareDate), untilBareDate)
	}

	// pagination.
	paged, err := repo.ListAudit(ctx, AuditFilters{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("ListAudit paged: %v", err)
	}
	if len(paged) != 1 || paged[0].Action != "void" {
		t.Fatalf("expected the middle entry with limit=1 offset=1, got %+v", paged)
	}
}

func TestPOSRepo_ListAuditForExport_ReturnsAllMatchingRowsUnbounded(t *testing.T) {
	db := newAuditTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	// ListAudit's default page is 50 rows; insert more than that and confirm
	// export returns everything, not just one page.
	for i := 0; i < 60; i++ {
		ts := fmt.Sprintf("2026-01-01T%02d:%02d:00Z", i/60, i%60)
		if err := repo.InsertAudit(ctx, nil, "", "sale", fmt.Sprintf("R-%d", i), "void", nil, ts, ""); err != nil {
			t.Fatalf("seed entry %d: %v", i, err)
		}
	}

	paged, err := repo.ListAudit(ctx, AuditFilters{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(paged) != 50 {
		t.Fatalf("expected ListAudit's default page to cap at 50, got %d", len(paged))
	}

	exported, truncated, err := repo.ListAuditForExport(ctx, AuditFilters{})
	if err != nil {
		t.Fatalf("ListAuditForExport: %v", err)
	}
	if truncated {
		t.Fatal("expected truncated=false well under the export ceiling")
	}
	if len(exported) != 60 {
		t.Fatalf("expected all 60 entries from export, got %d", len(exported))
	}
}

func TestPOSRepo_ListAuditForExport_BareUntilDateIncludesWholeDay(t *testing.T) {
	db := newAuditTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	if err := repo.InsertAudit(ctx, nil, "", "sale", "R-1", "void", nil, "2026-01-01T23:59:00Z", ""); err != nil {
		t.Fatalf("seed: %v", err)
	}

	exported, _, err := repo.ListAuditForExport(ctx, AuditFilters{Until: "2026-01-01"})
	if err != nil {
		t.Fatalf("ListAuditForExport: %v", err)
	}
	if len(exported) != 1 {
		t.Fatalf("expected the bare-date Until=2026-01-01 to include the whole day, got %d entries", len(exported))
	}
}

func TestPOSRepo_ListAuditForExport_TruncatesAtCeilingAndSignalsIt(t *testing.T) {
	db := newAuditTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		ts := fmt.Sprintf("2026-01-01T00:%02d:00Z", i)
		if err := repo.InsertAudit(ctx, nil, "", "sale", fmt.Sprintf("R-%d", i), "void", nil, ts, ""); err != nil {
			t.Fatalf("seed entry %d: %v", i, err)
		}
	}

	exported, truncated, err := repo.listAuditForExportWithCeiling(ctx, AuditFilters{}, 3)
	if err != nil {
		t.Fatalf("listAuditForExportWithCeiling: %v", err)
	}
	if !truncated {
		t.Fatal("expected truncated=true when matches exceed the ceiling")
	}
	if len(exported) != 3 {
		t.Fatalf("expected exactly ceiling (3) rows, got %d", len(exported))
	}
}

func TestPOSRepo_ListAuditForExport_FiltersMatchListAudit(t *testing.T) {
	db := newAuditTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	if _, err := db.Exec(`INSERT INTO users(id, username, display_name, role) VALUES ('u1','alice','Alice','manager')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := repo.InsertAudit(ctx, nil, "u1", "plugin", "com.x.faq", "install", nil, "2026-01-01T10:00:00Z", ""); err != nil {
		t.Fatalf("seed 1: %v", err)
	}
	if err := repo.InsertAudit(ctx, nil, "", "sale", "R-1", "void", nil, "2026-01-01T11:00:00Z", ""); err != nil {
		t.Fatalf("seed 2: %v", err)
	}

	exported, _, err := repo.ListAuditForExport(ctx, AuditFilters{EntityType: "plugin"})
	if err != nil {
		t.Fatalf("ListAuditForExport: %v", err)
	}
	if len(exported) != 1 || exported[0].Action != "install" {
		t.Fatalf("expected entity_type=plugin to narrow to the install entry, got %+v", exported)
	}
}

// TestPOSRepo_InsertAudit_LeavesBlockedActorIDEmpty pins InsertAudit's
// unchanged behavior after migration 049 (ut-docs#557) added the column:
// the ordinary, non-elevated path always leaves blocked_actor_id NULL.
func TestPOSRepo_InsertAudit_LeavesBlockedActorIDEmpty(t *testing.T) {
	db := newAuditTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	if _, err := db.Exec(`INSERT INTO users(id, username, display_name, role) VALUES ('u1','alice','Alice','manager')`); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := repo.InsertAudit(ctx, nil, "u1", "sale", "R-1", "refund", nil, "2026-01-01T10:00:00Z", ""); err != nil {
		t.Fatalf("InsertAudit: %v", err)
	}

	entries, err := repo.ListAudit(ctx, AuditFilters{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].BlockedActorID != "" {
		t.Fatalf("BlockedActorID = %q, want empty for a non-elevated InsertAudit entry", entries[0].BlockedActorID)
	}
}

// TestPOSRepo_InsertAuditElevated_PersistsBlockedActorID proves the
// dual-attribution write (ut-docs#557): actor_id is the approver who
// actually performed the action, blocked_actor_id is the originally-blocked
// session user — both survive round-trip through ListAudit AND
// ListAuditForExport (both threaded through the same buildAuditWhere/Scan
// column list).
func TestPOSRepo_InsertAuditElevated_PersistsBlockedActorID(t *testing.T) {
	db := newAuditTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	if _, err := db.Exec(`INSERT INTO users(id, username, display_name, role) VALUES ('approver1','mgr','Manager','manager')`); err != nil {
		t.Fatalf("seed approver: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO users(id, username, display_name, role) VALUES ('blocked1','cash','Cashier','cashier')`); err != nil {
		t.Fatalf("seed blocked user: %v", err)
	}

	if err := repo.InsertAuditElevated(ctx, nil, "approver1", "blocked1", "settings", "theme", "settings_changed",
		map[string]any{"key": "theme"}, "2026-01-01T10:00:00Z", ""); err != nil {
		t.Fatalf("InsertAuditElevated: %v", err)
	}

	entries, err := repo.ListAudit(ctx, AuditFilters{})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].ActorID != "approver1" {
		t.Fatalf("ActorID = %q, want approver1 (the approver actually acted)", entries[0].ActorID)
	}
	if entries[0].BlockedActorID != "blocked1" {
		t.Fatalf("BlockedActorID = %q, want blocked1 (the originally-blocked user)", entries[0].BlockedActorID)
	}

	exported, _, err := repo.ListAuditForExport(ctx, AuditFilters{})
	if err != nil {
		t.Fatalf("ListAuditForExport: %v", err)
	}
	if len(exported) != 1 || exported[0].BlockedActorID != "blocked1" {
		t.Fatalf("expected ListAuditForExport to also carry BlockedActorID, got %+v", exported)
	}
}

func TestPOSRepo_DistinctAuditEntityTypes(t *testing.T) {
	db := newAuditTestDB(t)
	repo := NewPOSRepo(db)
	ctx := context.Background()

	if err := repo.InsertAudit(ctx, nil, "", "plugin", "p1", "install", nil, "2026-01-01T10:00:00Z", ""); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.InsertAudit(ctx, nil, "", "sale", "s1", "void", nil, "2026-01-01T10:00:00Z", ""); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := repo.InsertAudit(ctx, nil, "", "plugin", "p2", "uninstall", nil, "2026-01-01T10:00:00Z", ""); err != nil {
		t.Fatalf("insert: %v", err)
	}

	types, err := repo.DistinctAuditEntityTypes(ctx)
	if err != nil {
		t.Fatalf("DistinctAuditEntityTypes: %v", err)
	}
	if len(types) != 2 {
		t.Fatalf("expected 2 distinct types (plugin, sale), got %+v", types)
	}
}
