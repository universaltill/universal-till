package data

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// Regression tests for ut-docs#1323 (performance audit section G): four
// install-time conflict-check methods and one receipt-path lookup used to
// issue one query per candidate instead of a single batched query. These
// assert both (a) behavior is unchanged and (b) the query count no longer
// grows with the candidate count, using the openCountingConn harness
// export_repo_querycount_test.go already established in this package.

func newBatchTestDB(t *testing.T) (*db.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "batch.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d, path
}

// --- SettingsRepo.GetByPrefix -------------------------------------------

func TestSettingsRepo_GetByPrefix_ConstantQueryCount(t *testing.T) {
	d, path := newBatchTestDB(t)
	ctx := context.Background()
	repo := NewSettingsRepo(d.DB)

	for i := 0; i < 20; i++ {
		id := string(rune('a' + i))
		if err := repo.Set(ctx, "payments.fee."+id, `{"bp":100,"fixed":0}`); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	counter := new(int64)
	countingRepo := NewSettingsRepo(openCountingConn(t, path, counter))
	got, err := countingRepo.GetByPrefix(ctx, "payments.fee.")
	if err != nil {
		t.Fatalf("GetByPrefix: %v", err)
	}
	if len(got) != 20 {
		t.Fatalf("expected 20 keys, got %d: %+v", len(got), got)
	}
	if n := *counter; n != 1 {
		t.Fatalf("expected exactly 1 SELECT for 20 keys under one prefix, got %d", n)
	}
}

// --- POSRepo.GetArchivedReport -------------------------------------------

func TestGetArchivedReport_MatchesListScanAndSkipsUnrelatedRows(t *testing.T) {
	d, _ := newBatchTestDB(t)
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	for i := 0; i < 50; i++ {
		period := fmt.Sprintf("period-%03d", i)
		mustExec(t, d, `INSERT INTO report_archive (id, kind, period, content_json, created_at)
VALUES (?, 'eod', ?, '{"net":100}', datetime('now'))`, fmt.Sprintf("ra-%d", i), period)
	}
	mustExec(t, d, `INSERT INTO report_archive (id, kind, period, content_json, created_at)
VALUES ('ra-target', 'eod', '2026-08-15-target', '{"net":4200}', datetime('now'))`)
	// A different kind sharing the same period must never match.
	mustExec(t, d, `INSERT INTO report_archive (id, kind, period, content_json, created_at)
VALUES ('ra-wrongkind', 'zreport', '2026-08-15-target', '{"net":9999}', datetime('now'))`)

	got, ok, err := repo.GetArchivedReport(ctx, "eod", "2026-08-15-target")
	if err != nil {
		t.Fatalf("GetArchivedReport: %v", err)
	}
	if !ok {
		t.Fatal("expected the seeded report to be found")
	}
	if got.Content != `{"net":4200}` {
		t.Fatalf("unexpected content: %q", got.Content)
	}

	if _, ok, err := repo.GetArchivedReport(ctx, "eod", "no-such-period"); err != nil || ok {
		t.Fatalf("expected not-found for an absent period, got ok=%v err=%v", ok, err)
	}
}

func TestGetArchivedReport_ConstantQueryCount(t *testing.T) {
	d, path := newBatchTestDB(t)
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		mustExec(t, d, `INSERT INTO report_archive (id, kind, period, content_json, created_at)
VALUES (?, 'eod', ?, '{"net":100}', datetime('now'))`, fmt.Sprintf("ra-%d", i), fmt.Sprintf("period-%03d", i))
	}
	mustExec(t, d, `INSERT INTO report_archive (id, kind, period, content_json, created_at)
VALUES ('ra-target', 'eod', '2026-08-15-target', '{"net":4200}', datetime('now'))`)

	counter := new(int64)
	countingRepo := NewPOSRepo(openCountingConn(t, path, counter))
	_, ok, err := countingRepo.GetArchivedReport(ctx, "eod", "2026-08-15-target")
	if err != nil {
		t.Fatalf("GetArchivedReport: %v", err)
	}
	if !ok {
		t.Fatal("expected the seeded report to be found")
	}
	// The old shape (ListArchivedReports(ctx, 100) + linear scan) issued 1
	// SELECT that returned every one of the 51 rows; this direct indexed
	// lookup must issue exactly 1 SELECT too — the fix isn't about the
	// SELECT *count* here (both are 1) but the *rows scanned*, which this
	// counting harness (SELECT-statement counting) can't observe. Assert
	// what it can: still exactly one query, not a new N+1.
	if n := *counter; n != 1 {
		t.Fatalf("expected exactly 1 SELECT, got %d", n)
	}
}

// --- GetPluginVersionsAt ----------------------------------------------

func TestGetPluginVersionsAt_MatchesSingularSemantics(t *testing.T) {
	d, _ := newBatchTestDB(t)
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	seedCatalogEntry(t, d, "com.example.a", "1.0.0")
	seedCatalogEntry(t, d, "com.example.b", "2.0.0")
	if err := repo.InstallPlugin(ctx, nil, "com.example.a"); err != nil {
		t.Fatal(err)
	}
	if err := repo.InstallPlugin(ctx, nil, "com.example.b"); err != nil {
		t.Fatal(err)
	}

	at := time.Now().Add(time.Hour)
	got, err := repo.GetPluginVersionsAt(ctx, []string{"com.example.a", "com.example.b", "com.example.missing"}, at)
	if err != nil {
		t.Fatalf("GetPluginVersionsAt: %v", err)
	}
	if got["com.example.a"] != "1.0.0" || got["com.example.b"] != "2.0.0" {
		t.Fatalf("unexpected versions: %+v", got)
	}
	if _, ok := got["com.example.missing"]; ok {
		t.Fatalf("unknown plugin id must be absent from the result, got %+v", got)
	}

	// Before either install: neither id has a row yet, matching
	// GetPluginVersionAt's own "not found" contract at that point in time.
	before := time.Now().Add(-24 * time.Hour)
	got, err = repo.GetPluginVersionsAt(ctx, []string{"com.example.a", "com.example.b"}, before)
	if err != nil {
		t.Fatalf("GetPluginVersionsAt (before): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no versions active before install time, got %+v", got)
	}

	if got, err := repo.GetPluginVersionsAt(ctx, nil, at); err != nil || len(got) != 0 {
		t.Fatalf("empty ids should short-circuit to an empty map, got %+v err=%v", got, err)
	}
}

func TestGetPluginVersionsAt_ConstantQueryCount(t *testing.T) {
	d, path := newBatchTestDB(t)
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	ids := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		id := "com.example.plugin" + string(rune('a'+i))
		seedCatalogEntry(t, d, id, "1.0.0")
		if err := repo.InstallPlugin(ctx, nil, id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	at := time.Now().Add(time.Hour)

	counter := new(int64)
	countingRepo := NewPluginRepo(openCountingConn(t, path, counter))
	got, err := countingRepo.GetPluginVersionsAt(ctx, ids, at)
	if err != nil {
		t.Fatalf("GetPluginVersionsAt: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("expected %d versions, got %d: %+v", len(ids), len(got), got)
	}
	if n := *counter; n != 1 {
		t.Fatalf("expected exactly 1 batched SELECT for %d ids, got %d", len(ids), n)
	}
}

// --- FindPaymentKeyConflicts / FindPaymentNameConflicts ----------------

func TestFindPaymentKeyConflicts_BatchedBehaviorAndQueryCount(t *testing.T) {
	d, path := newBatchTestDB(t)
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	// "cash" is a seeded built-in (no owner) — conflict with Owner == "".
	pgPlugin(t, d, "com.other.pay", 1)
	pgEntry(t, d, "pe-other", "com.other.pay", "otherkey", "Other Pay")
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	// A key with no payment_methods row at all, but an active OTHER
	// plugin's entry claims it (sync hasn't necessarily run for it) —
	// exercises the plugin_entries fallback path directly.
	pgPlugin(t, d, "com.entryonly.pay", 1)
	pgEntry(t, d, "pe-entryonly", "com.entryonly.pay", "entryonlykey", "Entry Only")

	keys := []string{"cash", "otherkey", "entryonlykey", "com.mine.freekey"}
	counter := new(int64)
	countingRepo := NewPluginRepo(openCountingConn(t, path, counter))
	conflicts, err := countingRepo.FindPaymentKeyConflicts(ctx, nil, "com.mine.pay", keys)
	if err != nil {
		t.Fatalf("FindPaymentKeyConflicts: %v", err)
	}
	if n := *counter; n != 2 {
		t.Fatalf("expected exactly 2 batched SELECTs (payment_methods + plugin_entries fallback) for %d keys, got %d", len(keys), n)
	}
	if len(conflicts) != 3 {
		t.Fatalf("expected 3 conflicts (cash, otherkey, entryonlykey), got %d: %+v", len(conflicts), conflicts)
	}
	// Order must match input key order — validatePaymentEntryKeys reports
	// conflicts[0] verbatim in its error message.
	if conflicts[0].Key != "cash" || conflicts[0].Owner != "" {
		t.Fatalf("expected cash first with no owner, got %+v", conflicts[0])
	}
	if conflicts[1].Key != "otherkey" || conflicts[1].Owner != "com.other.pay" || !conflicts[1].OwnerInstalled {
		t.Fatalf("expected otherkey owned by com.other.pay, got %+v", conflicts[1])
	}
	if conflicts[2].Key != "entryonlykey" || conflicts[2].Owner != "com.entryonly.pay" {
		t.Fatalf("expected entryonlykey owned by com.entryonly.pay via the entry fallback, got %+v", conflicts[2])
	}

	// The plugin's own already-synced key must never conflict.
	self, err := repo.FindPaymentKeyConflicts(ctx, nil, "com.other.pay", []string{"otherkey"})
	if err != nil {
		t.Fatalf("FindPaymentKeyConflicts (own key): %v", err)
	}
	if len(self) != 0 {
		t.Fatalf("plugin's own existing key must not self-conflict, got %+v", self)
	}

	if got, err := repo.FindPaymentKeyConflicts(ctx, nil, "com.mine.pay", nil); err != nil || len(got) != 0 {
		t.Fatalf("empty keys should short-circuit, got %+v err=%v", got, err)
	}
}

func TestFindPaymentNameConflicts_BatchedBehaviorAndQueryCount(t *testing.T) {
	d, path := newBatchTestDB(t)
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	pgPlugin(t, d, "com.other.pay", 1)
	pgEntry(t, d, "pe-other", "com.other.pay", "otherkey", "Other Pay")
	if err := repo.SyncPluginPaymentMethods(ctx); err != nil {
		t.Fatalf("sync: %v", err)
	}
	pgPlugin(t, d, "com.entryonly.pay", 1)
	pgEntry(t, d, "pe-entryonly", "com.entryonly.pay", "entryonlykey", "Entry Only Label")

	names := []string{"Cash", "Other Pay", "Entry Only Label", "Totally Free Label"}
	counter := new(int64)
	countingRepo := NewPluginRepo(openCountingConn(t, path, counter))
	conflicts, err := countingRepo.FindPaymentNameConflicts(ctx, nil, "com.mine.pay", names)
	if err != nil {
		t.Fatalf("FindPaymentNameConflicts: %v", err)
	}
	if n := *counter; n != 2 {
		t.Fatalf("expected exactly 2 batched SELECTs for %d names, got %d", len(names), n)
	}
	if len(conflicts) != 3 {
		t.Fatalf("expected 3 conflicts (Cash, Other Pay, Entry Only Label), got %d: %+v", len(conflicts), conflicts)
	}
	if conflicts[0].Key != "Cash" || conflicts[0].Owner != "" {
		t.Fatalf("expected Cash first with no owner, got %+v", conflicts[0])
	}
	if conflicts[1].Key != "Other Pay" || conflicts[1].Owner != "com.other.pay" {
		t.Fatalf("expected 'Other Pay' owned by com.other.pay, got %+v", conflicts[1])
	}
	if conflicts[2].Key != "Entry Only Label" || conflicts[2].Owner != "com.entryonly.pay" {
		t.Fatalf("expected 'Entry Only Label' owned by com.entryonly.pay via the entry fallback, got %+v", conflicts[2])
	}

	self, err := repo.FindPaymentNameConflicts(ctx, nil, "com.other.pay", []string{"Other Pay"})
	if err != nil {
		t.Fatalf("FindPaymentNameConflicts (own label): %v", err)
	}
	if len(self) != 0 {
		t.Fatalf("plugin's own existing label must not self-conflict, got %+v", self)
	}
}

// --- FindPageKeyConflicts / FindPageRouteConflicts ----------------------

func TestFindPageKeyConflicts_BatchedBehaviorAndQueryCount(t *testing.T) {
	d, path := newBatchTestDB(t)
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	pgPlugin(t, d, "com.owner.page", 1)
	mustExec(t, d, `INSERT INTO plugin_entries (id, plugin_id, type, key, label, route, sort_order, is_active, created_at, updated_at)
VALUES ('pe-page', 'com.owner.page', 'page', 'takenkey', 'Taken Page', '/plugin/taken', 1, 1, '2026-07-30T00:00:00Z', '2026-07-30T00:00:00Z')`)

	keys := []string{"takenkey", "freekey"}
	counter := new(int64)
	countingRepo := NewPluginRepo(openCountingConn(t, path, counter))
	conflicts, err := countingRepo.FindPageKeyConflicts(ctx, nil, "com.mine.page", keys)
	if err != nil {
		t.Fatalf("FindPageKeyConflicts: %v", err)
	}
	if n := *counter; n != 1 {
		t.Fatalf("expected exactly 1 batched SELECT for %d keys, got %d", len(keys), n)
	}
	if len(conflicts) != 1 || conflicts[0].Key != "takenkey" || conflicts[0].Owner != "com.owner.page" {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	self, err := repo.FindPageKeyConflicts(ctx, nil, "com.owner.page", []string{"takenkey"})
	if err != nil {
		t.Fatalf("FindPageKeyConflicts (own key): %v", err)
	}
	if len(self) != 0 {
		t.Fatalf("plugin's own existing page key must not self-conflict, got %+v", self)
	}
}

func TestFindPageRouteConflicts_BatchedBehaviorAndQueryCount(t *testing.T) {
	d, path := newBatchTestDB(t)
	ctx := context.Background()
	repo := NewPluginRepo(d.DB)

	pgPlugin(t, d, "com.owner.page", 1)
	mustExec(t, d, `INSERT INTO plugin_entries (id, plugin_id, type, key, label, route, sort_order, is_active, created_at, updated_at)
VALUES ('pe-page', 'com.owner.page', 'page', 'ownkey', 'Owner Page', '/plugin/taken', 1, 1, '2026-07-30T00:00:00Z', '2026-07-30T00:00:00Z')`)

	routes := []string{"/plugin/taken", "/plugin/free"}
	counter := new(int64)
	countingRepo := NewPluginRepo(openCountingConn(t, path, counter))
	conflicts, err := countingRepo.FindPageRouteConflicts(ctx, nil, "com.mine.page", routes)
	if err != nil {
		t.Fatalf("FindPageRouteConflicts: %v", err)
	}
	if n := *counter; n != 1 {
		t.Fatalf("expected exactly 1 batched SELECT for %d routes, got %d", len(routes), n)
	}
	if len(conflicts) != 1 || conflicts[0].Route != "/plugin/taken" || conflicts[0].Owner != "com.owner.page" {
		t.Fatalf("unexpected conflicts: %+v", conflicts)
	}

	self, err := repo.FindPageRouteConflicts(ctx, nil, "com.owner.page", []string{"/plugin/taken"})
	if err != nil {
		t.Fatalf("FindPageRouteConflicts (own route): %v", err)
	}
	if len(self) != 0 {
		t.Fatalf("plugin's own existing route must not self-conflict, got %+v", self)
	}
}
