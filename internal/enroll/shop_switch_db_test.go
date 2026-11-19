package enroll

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#6672 (guard for the closed ut-docs#5049): switching a running till
// to another shop only writes settings rows on the open handle. It never
// replaces, deletes or re-creates the database file under the live server,
// and a failed switch leaves the tables intact. A real, migrated database
// file backs the settings here (not a fake KV), and a db.FileWatch taken
// before the switch must still see the same file afterwards.

// shopSwitchDB opens a real migrated DB with the data-dir lock and a
// FileWatch, like app.Run does, and returns a settings store on it.
func shopSwitchDB(t *testing.T) (*sql.DB, string, *db.FileWatch, *data.SettingsRepo) {
	t.Helper()
	path := testsupport.MigratedDBFile(t, "unitill-pos.db")
	lock, err := db.AcquireDataDirLock(path)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })
	dbh, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = dbh.Close() })
	watch, err := db.NewFileWatch(path)
	if err != nil {
		t.Fatalf("NewFileWatch: %v", err)
	}
	return dbh.DB, path, watch, data.NewSettingsRepo(dbh.DB)
}

// bootDBTill is bootForPair with the settings in the real database.
func bootDBTill(t *testing.T, repo *data.SettingsRepo, endpoint string) *config.Config {
	t.Helper()
	resetState()
	t.Cleanup(resetState)
	ctx := context.Background()
	for k, v := range map[string]string{
		keyDeviceID:         pairOldDev,
		keyDeviceRegistered: pairOldDev,
		keyToken:            pairOldToken,
		keyStoreID:          pairOldStore,
		keyMerchantID:       pairOldStore,
		keyEnrolledAt:       "2026-01-01T00:00:00Z",
		keyPublicKey:        pinnedKey,
		"till.name":         "Front counter",
	} {
		if err := repo.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	initForTest(t, cfg, repo)
	cfg.Marketplace.EndpointURL = endpoint + "/api"
	return cfg
}

// assertDBIntact is the invariant: same file (inode) as at boot, and the
// schema is there for the existing pool and for a brand-new connection.
func assertDBIntact(t *testing.T, sqlDB *sql.DB, path string, watch *db.FileWatch) {
	t.Helper()
	if err := watch.Check(); err != nil {
		t.Fatalf("the database or lock file was swapped by the shop switch: %v", err)
	}
	ctx := context.Background()
	// A fresh connection is what hit "no such table" in #5049.
	sqlDB.SetMaxIdleConns(0)
	for _, tbl := range []string{"settings", "users"} {
		var n int
		if err := sqlDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+tbl).Scan(&n); err != nil {
			t.Fatalf("pool query on %s: %v", tbl, err)
		}
	}
	raw, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=rw")
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, tbl := range []string{"settings", "users"} {
		var n int
		if err := raw.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+tbl).Scan(&n); err != nil {
			t.Fatalf("new connection query on %s: %v", tbl, err)
		}
	}
}

func TestShopSwitchPair_KeepsTheDatabaseFile(t *testing.T) {
	cloud := newFakePairCloud(t)
	sqlDB, path, watch, repo := shopSwitchDB(t)
	cfg := bootDBTill(t, repo, cloud.srv.URL)
	ctx := context.Background()

	if _, err := Pair(ctx, cfg, repo, pairCode); err != nil {
		t.Fatalf("Pair: %v", err)
	}
	if got, _, _ := repo.Get(ctx, keyStoreID); got != pairStore {
		t.Fatalf("store id = %q, want %q", got, pairStore)
	}
	if got, _, _ := repo.Get(ctx, keyToken); got != pairToken {
		t.Fatal("the new credential was not persisted")
	}
	assertDBIntact(t, sqlDB, path, watch)
	// The same handle keeps serving after the switch (no restart needed).
	if _, _, err := repo.Get(ctx, keyStoreID); err != nil {
		t.Fatalf("settings read after the switch: %v", err)
	}
}

func TestShopSwitchPair_FailedKeepsTheDatabaseFile(t *testing.T) {
	cloud := newFakePairCloud(t)
	cloud.set(func(c *fakePairCloud) { c.status = http.StatusInternalServerError; c.errOut = "internal" })
	sqlDB, path, watch, repo := shopSwitchDB(t)
	cfg := bootDBTill(t, repo, cloud.srv.URL)

	if _, err := Pair(context.Background(), cfg, repo, pairCode); err == nil {
		t.Fatal("want the pair to fail")
	}
	if cloud.calls.Load() == 0 {
		t.Fatal("the cloud was never asked: the failure path was not exercised")
	}
	// The identity is cleared before the call by design (ADR-0116 D5), but
	// the tables stay.
	assertDBIntact(t, sqlDB, path, watch)
}

func TestShopSwitchMove_KeepsTheDatabaseFile(t *testing.T) {
	cloud := newFakeMoveCloud(t)
	sqlDB, path, watch, repo := shopSwitchDB(t)
	cfg := bootDBTill(t, repo, cloud.srv.URL)
	ctx := context.Background()
	if _, err := Pair(ctx, cfg, repo, pairCode); err == nil {
		t.Fatal("want a pending move")
	}
	// The real upload-ledger reset, as pages wires it.
	var reset atomic.Int32
	pos := data.NewPOSRepo(sqlDB)
	SetMoveHooks(MoveHooks{ResetUploadLedger: func(c context.Context) error {
		reset.Add(1)
		return pos.ResetSalesAggregateUploads(c)
	}})
	t.Cleanup(func() { SetMoveHooks(MoveHooks{}) })

	res, err := DecideMove(ctx, cfg, repo, moveID, MoveApprove)
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	if res.StoreID != moveTarget || reset.Load() != 1 {
		t.Fatalf("result = %+v, ledger resets = %d", res, reset.Load())
	}
	if got, _, _ := repo.Get(ctx, keyStoreID); got != moveTarget {
		t.Fatalf("store id = %q, want %q", got, moveTarget)
	}
	assertDBIntact(t, sqlDB, path, watch)
}

// A refused move (ut-docs#6672 AC 3): nothing is wiped first, so the old
// identity, the upload ledger and the tables all stay.
func TestShopSwitchMove_FailedKeepsTheDatabaseFile(t *testing.T) {
	cloud := newFakeMoveCloud(t)
	sqlDB, path, watch, repo := shopSwitchDB(t)
	cfg := bootDBTill(t, repo, cloud.srv.URL)
	ctx := context.Background()
	if _, err := Pair(ctx, cfg, repo, pairCode); err == nil {
		t.Fatal("want a pending move")
	}
	var reset atomic.Int32
	SetMoveHooks(MoveHooks{ResetUploadLedger: func(context.Context) error { reset.Add(1); return nil }})
	t.Cleanup(func() { SetMoveHooks(MoveHooks{}) })
	cloud.set(func(c *fakeMoveCloud) { c.approvalCode, c.approvalErr = http.StatusGone, "move_expired" })

	_, err := DecideMove(ctx, cfg, repo, moveID, MoveApprove)
	var ref *MoveRefusedError
	if !errors.As(err, &ref) || cloud.approvalCalls.Load() == 0 {
		t.Fatalf("err = %v, approval calls = %d: want a refusal from the cloud", err, cloud.approvalCalls.Load())
	}
	if got, _, _ := repo.Get(ctx, keyStoreID); got != pairOldStore || reset.Load() != 0 {
		t.Fatalf("store id = %q (want %q), ledger resets = %d: a refused move wiped something", got, pairOldStore, reset.Load())
	}
	if got, _, _ := repo.Get(ctx, keyToken); got != pairOldToken {
		t.Fatal("a refused move cleared the credential")
	}
	assertDBIntact(t, sqlDB, path, watch)
}

// The guard itself must be able to fail: a deleted file is reported, not
// served from.
func TestShopSwitchGuard_DetectsADeletedFile(t *testing.T) {
	_, path, watch, _ := shopSwitchDB(t)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := watch.Check(); err == nil || !strings.Contains(err.Error(), "deleted") {
		t.Fatalf("Check = %v, want a deleted-file error", err)
	}
}
