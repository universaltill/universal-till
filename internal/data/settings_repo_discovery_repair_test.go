package data

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#5897: a till promoted before #5893 still holds its former main
// till's id in lan_discovery.till_id; RepairInheritedDiscoveryID re-mints it
// once, only while no till holds a credential for this till.

func newDiscoveryRepairDB(t *testing.T) (*sql.DB, *SettingsRepo) {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, "discovery_repair.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d.DB, NewSettingsRepo(d.DB)
}

func setS(t *testing.T, r *SettingsRepo, k, v string) {
	t.Helper()
	if err := r.Set(context.Background(), k, v); err != nil {
		t.Fatal(err)
	}
}

func getS(t *testing.T, r *SettingsRepo, k string) string {
	t.Helper()
	v, _, err := r.Get(context.Background(), k)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func countRepairAudits(t *testing.T, d *sql.DB) int {
	t.Helper()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'discovery_id_repaired'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRepairInheritedDiscoveryID_CloudIDNoMarker_Remints(t *testing.T) {
	d, r := newDiscoveryRepairDB(t)
	ctx := context.Background()
	setS(t, r, LANDiscoveryTillIDSettingsKey, "old-main")
	setS(t, r, TillIdentityCloudIDSettingsKey, "cloud-c")

	res, err := r.RepairInheritedDiscoveryID(ctx, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Repaired || res.Signal != "cloud_id" || res.OldID != "old-main" {
		t.Fatalf("result = %+v", res)
	}
	got := getS(t, r, LANDiscoveryTillIDSettingsKey)
	if got == "old-main" {
		t.Fatal("id not re-minted")
	}
	if _, err := uuid.Parse(got); err != nil {
		t.Fatalf("new id not a uuid: %q", got)
	}
	if res.NewID != got || getS(t, r, TillIdentityOwnDiscoveryIDSettingsKey) != got {
		t.Fatalf("marker/new id mismatch: %+v", res)
	}
	if getS(t, r, TillIdentityCloudIDSettingsKey) != "cloud-c" {
		t.Fatal("cloud id changed")
	}
	if n := countRepairAudits(t, d); n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
}

func TestRepairInheritedDiscoveryID_MirrorMatch_PinsCloudID(t *testing.T) {
	d, r := newDiscoveryRepairDB(t)
	ctx := context.Background()
	setS(t, r, LANDiscoveryTillIDSettingsKey, "old-main")

	res, err := r.RepairInheritedDiscoveryID(ctx, " old-main ", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Repaired || res.Signal != "mirror" {
		t.Fatalf("result = %+v", res)
	}
	if getS(t, r, LANDiscoveryTillIDSettingsKey) == "old-main" {
		t.Fatal("id not re-minted")
	}
	if getS(t, r, TillIdentityCloudIDSettingsKey) != "old-main" {
		t.Fatal("cloud id not pinned to the pre-repair id")
	}
	if getS(t, r, TillIdentityOwnDiscoveryIDSettingsKey) == "" {
		t.Fatal("marker missing")
	}
	if countRepairAudits(t, d) != 1 {
		t.Fatal("want one audit row")
	}
}

func TestRepairInheritedDiscoveryID_NeverJoined_Untouched(t *testing.T) {
	d, r := newDiscoveryRepairDB(t)
	ctx := context.Background()
	setS(t, r, LANDiscoveryTillIDSettingsKey, "mine")
	for i := 0; i < 2; i++ {
		res, err := r.RepairInheritedDiscoveryID(ctx, "someone-else", time.Now())
		if err != nil || res.Repaired || res.Deferred != "" {
			t.Fatalf("run %d: %+v %v", i, res, err)
		}
	}
	if getS(t, r, LANDiscoveryTillIDSettingsKey) != "mine" || countRepairAudits(t, d) != 0 {
		t.Fatal("never-joined till was touched")
	}
}

func TestRepairInheritedDiscoveryID_NoStoredID_Untouched(t *testing.T) {
	_, r := newDiscoveryRepairDB(t)
	setS(t, r, TillIdentityCloudIDSettingsKey, "c")
	res, err := r.RepairInheritedDiscoveryID(context.Background(), "", time.Now())
	if err != nil || res.Repaired {
		t.Fatalf("%+v %v", res, err)
	}
	if getS(t, r, LANDiscoveryTillIDSettingsKey) != "" {
		t.Fatal("minted an id for a till that had none")
	}
}

func TestRepairInheritedDiscoveryID_SecondRun_NoRemint(t *testing.T) {
	d, r := newDiscoveryRepairDB(t)
	ctx := context.Background()
	setS(t, r, LANDiscoveryTillIDSettingsKey, "old-main")
	setS(t, r, TillIdentityCloudIDSettingsKey, "cloud-c")
	if _, err := r.RepairInheritedDiscoveryID(ctx, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	first := getS(t, r, LANDiscoveryTillIDSettingsKey)
	res, err := r.RepairInheritedDiscoveryID(ctx, "", time.Now())
	if err != nil || res.Repaired {
		t.Fatalf("%+v %v", res, err)
	}
	if getS(t, r, LANDiscoveryTillIDSettingsKey) != first || countRepairAudits(t, d) != 1 {
		t.Fatal("re-minted on second run")
	}
}

func TestSettingsRepo_ClearReplicaIdentityWritesOwnDiscoveryIDMarker(t *testing.T) {
	_, r := newDiscoveryRepairDB(t)
	setS(t, r, LANDiscoveryTillIDSettingsKey, "main-id")
	setS(t, r, "sync.till_id", "cloud-c")
	setS(t, r, "sync.primary_url", "http://p")
	if err := r.ClearReplicaIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := getS(t, r, LANDiscoveryTillIDSettingsKey)
	if id == "main-id" || id == "" || getS(t, r, TillIdentityOwnDiscoveryIDSettingsKey) != id {
		t.Fatalf("marker %q vs id %q", getS(t, r, TillIdentityOwnDiscoveryIDSettingsKey), id)
	}
}

func TestRepairInheritedDiscoveryID_AfterPromote_NoRemint(t *testing.T) {
	d, r := newDiscoveryRepairDB(t)
	setS(t, r, LANDiscoveryTillIDSettingsKey, "main-id")
	setS(t, r, "sync.till_id", "cloud-c")
	setS(t, r, "sync.primary_url", "http://p")
	if err := r.ClearReplicaIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := getS(t, r, LANDiscoveryTillIDSettingsKey)
	res, err := r.RepairInheritedDiscoveryID(context.Background(), "", time.Now())
	if err != nil || res.Repaired {
		t.Fatalf("%+v %v", res, err)
	}
	if getS(t, r, LANDiscoveryTillIDSettingsKey) != id || countRepairAudits(t, d) != 0 {
		t.Fatal("promoted-on-this-build till was re-minted")
	}
}

func TestRepairInheritedDiscoveryID_Replica_Skipped(t *testing.T) {
	_, r := newDiscoveryRepairDB(t)
	setS(t, r, LANDiscoveryTillIDSettingsKey, "main-id")
	setS(t, r, TillIdentityCloudIDSettingsKey, "c")
	setS(t, r, "sync.primary_url", "http://p")
	res, err := r.RepairInheritedDiscoveryID(context.Background(), "main-id", time.Now())
	if err != nil || res.Repaired || res.Deferred != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if getS(t, r, LANDiscoveryTillIDSettingsKey) != "main-id" {
		t.Fatal("replica id changed")
	}
}

func TestRepairInheritedDiscoveryID_TillHoldsCredential_Deferred(t *testing.T) {
	for _, col := range []string{"bearer_hash", "bearer_prev_hash"} {
		t.Run(col, func(t *testing.T) {
			d, r := newDiscoveryRepairDB(t)
			setS(t, r, LANDiscoveryTillIDSettingsKey, "old-main")
			setS(t, r, TillIdentityCloudIDSettingsKey, "c")
			if _, err := d.Exec(`INSERT INTO tills (id, name, ` + col + `) VALUES ('t1', 'Till 2', 'h')`); err != nil {
				t.Fatal(err)
			}
			res, err := r.RepairInheritedDiscoveryID(context.Background(), "", time.Now())
			if err != nil || res.Repaired || res.Deferred != "joined_tills" {
				t.Fatalf("%+v %v", res, err)
			}
			if getS(t, r, LANDiscoveryTillIDSettingsKey) != "old-main" || countRepairAudits(t, d) != 0 {
				t.Fatal("deferred repair wrote something")
			}
		})
	}
}

func TestRepairInheritedDiscoveryID_InheritedRosterRows_DoNotDefer(t *testing.T) {
	d, r := newDiscoveryRepairDB(t)
	setS(t, r, LANDiscoveryTillIDSettingsKey, "old-main")
	setS(t, r, TillIdentityCloudIDSettingsKey, "c")
	if _, err := d.Exec(`INSERT INTO tills (id, name) VALUES ('t1', 'Inherited')`); err != nil {
		t.Fatal(err)
	}
	res, err := r.RepairInheritedDiscoveryID(context.Background(), "", time.Now())
	if err != nil || !res.Repaired {
		t.Fatalf("%+v %v", res, err)
	}
}

func seedPairing(t *testing.T, d *sql.DB, status string, expires time.Time) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO pending_pairings (id, device_name, commitment, requested_at, expires_at, status) VALUES (?, 'd', 'c', ?, ?, ?)`,
		uuid.NewString(), time.Now().UTC().Format(time.RFC3339), expires.UTC().Format(time.RFC3339), status); err != nil {
		t.Fatal(err)
	}
}

func TestRepairInheritedDiscoveryID_LivePairing_Deferred(t *testing.T) {
	for _, st := range []string{"pending", "approved"} {
		d, r := newDiscoveryRepairDB(t)
		setS(t, r, LANDiscoveryTillIDSettingsKey, "old-main")
		setS(t, r, TillIdentityCloudIDSettingsKey, "c")
		seedPairing(t, d, st, time.Now().Add(5*time.Minute))
		res, err := r.RepairInheritedDiscoveryID(context.Background(), "", time.Now())
		if err != nil || res.Repaired || res.Deferred != "pairing_in_flight" {
			t.Fatalf("%s: %+v %v", st, res, err)
		}
		if getS(t, r, LANDiscoveryTillIDSettingsKey) != "old-main" {
			t.Fatal("deferred repair changed id")
		}
	}
}

func TestRepairInheritedDiscoveryID_ExpiredPairing_DoesNotDefer(t *testing.T) {
	d, r := newDiscoveryRepairDB(t)
	setS(t, r, LANDiscoveryTillIDSettingsKey, "old-main")
	setS(t, r, TillIdentityCloudIDSettingsKey, "c")
	seedPairing(t, d, "pending", time.Now().Add(-time.Hour))
	res, err := r.RepairInheritedDiscoveryID(context.Background(), "", time.Now())
	if err != nil || !res.Repaired {
		t.Fatalf("%+v %v", res, err)
	}
}

func TestRepairInheritedDiscoveryID_WriteFails_RollsBack(t *testing.T) {
	d, r := newDiscoveryRepairDB(t)
	ctx := context.Background()
	setS(t, r, LANDiscoveryTillIDSettingsKey, "old-main")
	if _, err := d.Exec(`CREATE TRIGGER fail_audit BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'x'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RepairInheritedDiscoveryID(ctx, "old-main", time.Now()); err == nil {
		t.Fatal("want error")
	}
	if getS(t, r, LANDiscoveryTillIDSettingsKey) != "old-main" ||
		getS(t, r, TillIdentityOwnDiscoveryIDSettingsKey) != "" ||
		getS(t, r, TillIdentityCloudIDSettingsKey) != "" {
		t.Fatal("partial write survived")
	}
	if _, err := d.Exec(`DROP TRIGGER fail_audit`); err != nil {
		t.Fatal(err)
	}
	res, err := r.RepairInheritedDiscoveryID(ctx, "old-main", time.Now())
	if err != nil || !res.Repaired {
		t.Fatalf("retry: %+v %v", res, err)
	}
}

// A restore keeps till_identity.* live but takes lan_discovery.till_id from
// the backup: a pre-repair backup brings the former main till's id back while
// the marker stays set. The recorded inherited id re-arms the repair.
func TestRepairInheritedDiscoveryID_RestoredPreRepairBackup_RemintsAgain(t *testing.T) {
	d, r := newDiscoveryRepairDB(t)
	ctx := context.Background()
	setS(t, r, LANDiscoveryTillIDSettingsKey, "old-main")
	setS(t, r, TillIdentityCloudIDSettingsKey, "cloud-c")
	first, err := r.RepairInheritedDiscoveryID(ctx, "", time.Now())
	if err != nil || !first.Repaired {
		t.Fatalf("first: %+v %v", first, err)
	}
	if getS(t, r, TillIdentityInheritedDiscoveryIDSettingsKey) != "old-main" {
		t.Fatal("inherited id not recorded")
	}
	setS(t, r, LANDiscoveryTillIDSettingsKey, "old-main") // the restore

	res, err := r.RepairInheritedDiscoveryID(ctx, "", time.Now())
	if err != nil || !res.Repaired || res.Signal != "restored" {
		t.Fatalf("after restore: %+v %v", res, err)
	}
	if got := getS(t, r, LANDiscoveryTillIDSettingsKey); got == "old-main" || got == first.NewID {
		t.Fatalf("id after restore repair = %q", got)
	}
	if getS(t, r, TillIdentityCloudIDSettingsKey) != "cloud-c" || countRepairAudits(t, d) != 2 {
		t.Fatal("cloud id changed or audit rows wrong")
	}
}

// A different till's own id restored over a repaired till (ut-docs#5173
// keeps a restored main till's id) is not touched.
func TestRepairInheritedDiscoveryID_RestoredOtherOwnID_Untouched(t *testing.T) {
	_, r := newDiscoveryRepairDB(t)
	ctx := context.Background()
	setS(t, r, LANDiscoveryTillIDSettingsKey, "old-main")
	setS(t, r, TillIdentityCloudIDSettingsKey, "cloud-c")
	if res, err := r.RepairInheritedDiscoveryID(ctx, "", time.Now()); err != nil || !res.Repaired {
		t.Fatalf("%+v %v", res, err)
	}
	setS(t, r, LANDiscoveryTillIDSettingsKey, "other-till-own")
	res, err := r.RepairInheritedDiscoveryID(ctx, "", time.Now())
	if err != nil || res.Repaired || res.Deferred != "" {
		t.Fatalf("%+v %v", res, err)
	}
	if getS(t, r, LANDiscoveryTillIDSettingsKey) != "other-till-own" {
		t.Fatal("restored own id was re-minted")
	}
}
