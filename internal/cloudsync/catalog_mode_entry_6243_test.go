package cloudsync

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#6243: caching a check-in block that moves the till from till mode
// to cloud/adopting marks the linked icons the cloud never received, from
// both the 200 and the 402 plan_required paths.

const entryBlock = `{"plan":"shop","subscription_status":"active","catalog_master":"cloud"}`

func seedEntryTill(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO settings (key, value) VALUES ('entitlement.catalog_master', 'till')`,
		`INSERT INTO items (id, sku, name, base_price, icon) VALUES ('i1', 'S-i1', 'N', 100, 'x')`,
		`INSERT INTO cloud_links (entity, cloud_id, local_id, version, updated_at, base) VALUES ('item', 'cl-i1', 'i1', 1, 'x', '{"icon":""}')`,
		`DELETE FROM cloud_outbox`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func openItemMarks(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM cloud_outbox WHERE entity = 'item' AND local_id = 'i1' AND sealed_at IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The 200 path: cacheEntitlement is what the check-in feeds the block to.
// (A whole Tick would upload the mark in the same tick; that upload and the
// pull keeping the icon are pinned in catalogpull's mode-entry test.)
func TestSyncEntitlement_EnteringCloudMarksIcons_6243(t *testing.T) {
	d := openMigratedDB(t, "ent_enter_cloud.db")
	seedEntryTill(t, d.DB)
	cacheEntitlement(context.Background(), data.NewSettingsRepo(d.DB), json.RawMessage(entryBlock), time.Now())
	if n := openItemMarks(t, d.DB); n != 1 {
		t.Fatalf("open item marks = %d, want 1", n)
	}
	cacheEntitlement(context.Background(), data.NewSettingsRepo(d.DB), json.RawMessage(entryBlock), time.Now())
	if n := openItemMarks(t, d.DB); n != 1 {
		t.Fatalf("open item marks after a repeat = %d, want still 1", n)
	}
}

func TestPlanRequired_EnteringCloudMarksIcons_6243(t *testing.T) {
	d := openMigratedDB(t, "ent_enter_402.db")
	seedEntryTill(t, d.DB)
	err := &statusError{Path: "/v1/stores/sync", StatusCode: http.StatusPaymentRequired, RetryAfter: time.Hour,
		Entitlement: json.RawMessage(entryBlock)}
	cachePlanRequired(context.Background(), data.NewSettingsRepo(d.DB), err)
	if n := openItemMarks(t, d.DB); n != 1 {
		t.Fatalf("open item marks = %d, want 1", n)
	}
}

// A failing mark step rolls the cache back and raises a problem (not just a
// log line); the next successful cache write resolves it.
func TestSyncEntitlement_MarkFailureRaisesProblem_6243(t *testing.T) {
	d := openMigratedDB(t, "ent_mark_fail.db")
	seedEntryTill(t, d.DB)
	logging.ResolveProblems(problemKeyCatalogModeMarks)
	t.Cleanup(func() { logging.ResolveProblems(problemKeyCatalogModeMarks) })
	if _, err := d.DB.Exec(`CREATE TRIGGER t6243p BEFORE INSERT ON cloud_outbox BEGIN SELECT RAISE(ABORT, 'boom'); END`); err != nil {
		t.Fatal(err)
	}
	cacheEntitlement(context.Background(), data.NewSettingsRepo(d.DB), json.RawMessage(entryBlock), time.Now())
	if !logging.HasOpenProblem(problemKeyCatalogModeMarks) {
		t.Fatal("no open problem after a failed mode-entry marking")
	}
	if _, err := d.DB.Exec(`DROP TRIGGER t6243p`); err != nil {
		t.Fatal(err)
	}
	cacheEntitlement(context.Background(), data.NewSettingsRepo(d.DB), json.RawMessage(entryBlock), time.Now())
	if logging.HasOpenProblem(problemKeyCatalogModeMarks) {
		t.Fatal("problem still open after the retry succeeded")
	}
}
