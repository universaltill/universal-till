package db

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// setSetting writes one settings row on d.
func setSetting(t *testing.T, d *sql.DB, key, value string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

// getSetting reads one settings row; ok=false when the row is absent.
func getSetting(t *testing.T, d *sql.DB, key string) (string, bool) {
	t.Helper()
	var v string
	err := d.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	return v, true
}

// deadTillBackup builds a backup file as another device (the dead till)
// would have written it: its own cloud identity and a sale marker. Returns
// the backup's file name inside dbPath's backups folder.
func deadTillBackup(t *testing.T, dbPath string) string {
	t.Helper()
	other := filepath.Join(t.TempDir(), "unitill-pos.db")
	d, err := Open(other)
	if err != nil {
		t.Fatalf("open dead till db: %v", err)
	}
	for k, v := range map[string]string{
		"marketplace.device_id":         "till-dead-device",
		"marketplace.token":             "dead-till-secret-token-5069",
		"marketplace.token_kind":        "device",
		"marketplace.device_registered": "till-dead-device",
		"marketplace.store_id":          "store-of-dead-till",
		"marketplace.merchant_id":       "merchant-of-dead-till",
		"marketplace.enrolled_at":       "2026-01-01T00:00:00Z",
		"marketplace.paired_at":         "2026-01-01T00:00:00Z",
		"till_identity.cloud_id":        "cloud-id-dead",
		"entitlement.plan":              "shop",
		"cloudsync.snapshot_hash":       "dead-hash",
		"cloudsync.catalog.cursor":      "cursor-of-the-backup-rows",
		"marketplace.terms_version":     "dead-terms",
		"cloud.link_tier":               "realtime",
		"cloudsync.unpaid_checkin.date": "2026-01-01",
		"diagnostics.session_id":        "dead-support-session",
		"marker.sales":                  "from-the-backup",
		"store.name":                    "Backup shop name",
		"sales_mirror.epoch":            "epoch-of-the-backup",
	} {
		setSetting(t, d.DB, k, v)
	}
	snap, err := Snapshot(d.DB, other)
	if err != nil {
		t.Fatalf("snapshot dead till: %v", err)
	}
	d.Close()
	dir, err := BackupDir(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Base(snap)
	raw, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

// ut-docs#5069: a backup taken on another device (the dead till) must not
// bring back that device's cloud identity. The running till keeps its own
// device id, token and store link; the backup's sales and settings come back.
func TestApplyPendingRestoreKeepsThisTillsCloudIdentity(t *testing.T) {
	path := testDBPath(t)
	live, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"marketplace.device_id":         "till-new-device",
		"marketplace.token":             "new-till-token",
		"marketplace.device_registered": "till-new-device",
		"marketplace.store_id":          "store-of-new-till",
		"marketplace.merchant_id":       "merchant-of-new-till",
		"marketplace.paired_at":         "2026-10-26T09:00:00Z",
		"entitlement.plan":              "local",
		"marker.sales":                  "live-before-restore",
	} {
		setSetting(t, live.DB, k, v)
	}
	live.Close()

	name := deadTillBackup(t, path)
	if err := StageRestore(path, name); err != nil {
		t.Fatalf("stage: %v", err)
	}
	applied, err := ApplyPendingRestore(path)
	if err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}

	restored := openTest(t, path)
	want := map[string]string{
		"marketplace.device_id":         "till-new-device",
		"marketplace.token":             "new-till-token",
		"marketplace.device_registered": "till-new-device",
		"marketplace.store_id":          "store-of-new-till",
		"marketplace.merchant_id":       "merchant-of-new-till",
		"marketplace.paired_at":         "2026-10-26T09:00:00Z",
		"entitlement.plan":              "local",
		// The backup's own data does come back.
		"marker.sales": "from-the-backup",
		"store.name":   "Backup shop name",
		// The pull state describes the restored rows: the backup's.
		"cloudsync.catalog.cursor": "cursor-of-the-backup-rows",
	}
	for k, v := range want {
		if got, _ := getSetting(t, restored.DB, k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// The dead till's identity rows the live till never had are gone too.
	for _, k := range []string{"marketplace.token_kind", "marketplace.enrolled_at", "till_identity.cloud_id", "cloudsync.snapshot_hash",
		"marketplace.terms_version", "cloud.link_tier", "cloudsync.unpaid_checkin.date", "diagnostics.session_id"} {
		if v, ok := getSetting(t, restored.DB, k); ok {
			t.Errorf("%s = %q survived the restore; it was the dead till's", k, v)
		}
	}
	restored.Close()
	// secure_delete: the dead till's token is not left in a free page.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("dead-till-secret-token-5069")) {
		t.Error("the dead till's token bytes are still in the restored file")
	}
}

// A fresh device that never paired restores a dead till's backup: it gets
// no cloud identity at all (it mints and pairs its own), never the dead
// till's credential.
func TestApplyPendingRestoreOnUnpairedTillDropsBackupIdentity(t *testing.T) {
	path := testDBPath(t)
	live, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	live.Close()
	name := deadTillBackup(t, path)
	if err := StageRestore(path, name); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyPendingRestore(path); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	restored := openTest(t, path)
	for _, k := range []string{"marketplace.device_id", "marketplace.token", "marketplace.store_id", "marketplace.merchant_id", "till_identity.cloud_id", "entitlement.plan"} {
		if v, ok := getSetting(t, restored.DB, k); ok {
			t.Errorf("%s = %q came from the dead till's backup", k, v)
		}
	}
	if got, _ := getSetting(t, restored.DB, "marker.sales"); got != "from-the-backup" {
		t.Errorf("backup data not restored: %q", got)
	}
}

// No live DB at all (the data folder only holds the staged file): the
// restore still applies, without the backup's identity.
func TestApplyPendingRestoreWithoutLiveDBDropsBackupIdentity(t *testing.T) {
	path := testDBPath(t)
	name := deadTillBackup(t, path)
	if err := StageRestore(path, name); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyPendingRestore(path); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	restored := openTest(t, path)
	if v, ok := getSetting(t, restored.DB, "marketplace.token"); ok {
		t.Errorf("token %q came from the dead till's backup", v)
	}
	if got, _ := getSetting(t, restored.DB, "marker.sales"); got != "from-the-backup" {
		t.Errorf("backup data not restored: %q", got)
	}
}

// A LAN join stages the main till's snapshot through the same pending file
// plus a replica identity; ApplyReplicaIdentity owns that case and the
// shop's store link must travel with the snapshot, so the restore leaves it.
func TestApplyPendingRestoreLeavesLANJoinSnapshotToReplicaIdentity(t *testing.T) {
	path := testDBPath(t)
	live, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	setSetting(t, live.DB, "marketplace.store_id", "standalone-store")
	live.Close()
	name := deadTillBackup(t, path)
	if err := StageRestore(path, name); err != nil {
		t.Fatal(err)
	}
	if err := StageReplicaIdentity(path, ReplicaIdentity{PrimaryURL: "https://main.local", TillID: "t2"}); err != nil {
		t.Fatal(err)
	}
	if applied, err := ApplyPendingRestore(path); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	restored := openTest(t, path)
	if got, _ := getSetting(t, restored.DB, "marketplace.store_id"); got != "store-of-dead-till" {
		t.Errorf("join snapshot store_id = %q, want the main till's", got)
	}
}

// A staged file that is not a till database cannot be sanitised: it is
// withdrawn and the live database stays, so boot never swaps in a file that
// may still carry another till's credential. Settings → Backups is told
// (RestoreRefused) until the next restore is staged.
func TestApplyPendingRestoreRefusesUnreadableFile(t *testing.T) {
	path := testDBPath(t)
	live, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	setSetting(t, live.DB, "marker.sales", "live")
	live.Close()
	if err := StageRestoreFromReader(path, bytes.NewReader([]byte("not a database at all"))); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyPendingRestore(path)
	if applied {
		t.Fatal("an unreadable staged file was applied")
	}
	if err != nil {
		t.Fatalf("a refused restore must not stop boot: %v", err)
	}
	if PendingRestore(path) {
		t.Error("the refused file is still staged; every boot would retry it")
	}
	if !RestoreRefused(path) {
		t.Error("the refusal is not recorded for Settings → Backups")
	}
	d := openTest(t, path)
	if got, _ := getSetting(t, d.DB, "marker.sales"); got != "live" {
		t.Errorf("live data lost: %q", got)
	}
	snap, err := Snapshot(d.DB, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := StageRestore(path, filepath.Base(snap)); err != nil {
		t.Fatal(err)
	}
	if RestoreRefused(path) {
		t.Error("staging a new restore must clear the refusal notice")
	}
}

// The live DB is corrupt — the case a restore is the cure for, normally
// with this till's own backup. The restore applies and the backup's own
// identity is kept, so the recovered till stays paired.
func TestApplyPendingRestoreOverUnreadableLiveDBKeepsBackupIdentity(t *testing.T) {
	path := testDBPath(t)
	name := deadTillBackup(t, path)
	if err := os.WriteFile(path, []byte("this live database is damaged beyond reading"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StageRestore(path, name); err != nil {
		t.Fatal(err)
	}
	applied, err := ApplyPendingRestore(path)
	if err != nil || !applied {
		t.Fatalf("a corrupt live DB must not block the restore: %v %v", applied, err)
	}
	restored := openTest(t, path)
	if got, _ := getSetting(t, restored.DB, "marketplace.token"); got != "dead-till-secret-token-5069" {
		t.Errorf("token = %q, want the backup's own (same-till recovery)", got)
	}
	if got, _ := getSetting(t, restored.DB, "marker.sales"); got != "from-the-backup" {
		t.Errorf("backup data not restored: %q", got)
	}
	if v, ok := getSetting(t, restored.DB, "cloudsync.snapshot_hash"); ok {
		t.Errorf("push hash %q kept; the restored data must be pushed again", v)
	}
	// ut-docs#5074: the restored change log's seqs may go backwards, so the
	// sales copy's epoch is dropped and re-minted; every copy re-syncs.
	if v, ok := getSetting(t, restored.DB, "sales_mirror.epoch"); ok {
		t.Errorf("sales copy epoch %q kept across a restore", v)
	}
}
