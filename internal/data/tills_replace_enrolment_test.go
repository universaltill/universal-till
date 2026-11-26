package data

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ut-docs#7057: re-pairing a till whose name is already enrolled replaces
// the enrolment: same till id and receipt prefix, a new bearer, the old
// bearer refused, a fresh TLS pin.
func TestReplaceEnrolment_KeepsIDAndPrefixSwapsBearer(t *testing.T) {
	d := openMigratedDB(t, "tills-replace.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	id, _, prefix, err := repo.EnrolTillPrefix(ctx, "Bar tablet", "old-hash", TillRoleAdditional, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkTLSPinned(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceEnrolment(ctx, id, "new-hash", TillRoleSatellite, time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("ReplaceEnrolment: %v", err)
	}
	got, ok, err := repo.TillByBearerHashAt(ctx, "new-hash", time.Now())
	if err != nil || !ok || got.ID != id || got.Role != TillRoleSatellite {
		t.Fatalf("new bearer resolves to %+v ok=%v err=%v, want till %s as satellite", got, ok, err, id)
	}
	if _, ok, _ := repo.TillByBearerHashAt(ctx, "old-hash", time.Now()); ok {
		t.Fatal("the old bearer still works after the enrolment was replaced")
	}
	var gotPrefix string
	if err := d.DB.QueryRow(`SELECT receipt_prefix FROM tills WHERE id = ?`, id).Scan(&gotPrefix); err != nil || gotPrefix != prefix {
		t.Fatalf("receipt prefix = %q (err %v), want it kept as %q", gotPrefix, err, prefix)
	}
	if all, _ := repo.AllTillsTLSPinned(ctx); all {
		t.Fatal("the TLS pin of the replaced enrolment must be cleared, the new device pins afresh")
	}
	list, _ := repo.ListTills(ctx)
	if len(list) != 1 {
		t.Fatalf("tills after replace = %d, want 1 (no duplicate row)", len(list))
	}
}

func TestReplaceEnrolment_UnknownTillIsAnError(t *testing.T) {
	d := openMigratedDB(t, "tills-replace-missing.db")
	if err := NewTillsRepo(d.DB).ReplaceEnrolment(context.Background(), "nope", "h", TillRoleAdditional, time.Now()); err == nil {
		t.Fatal("replacing an unknown till must fail, not succeed silently")
	}
}

func TestTillByName_IsCaseAndSpaceInsensitive(t *testing.T) {
	d := openMigratedDB(t, "tills-byname.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	id, _, _, _ := repo.EnrolTillPrefix(ctx, "Café 2", "h", TillRoleAdditional, "")
	got, ok, err := repo.TillByName(ctx, "  CAFÉ 2 ")
	if err != nil || !ok || got.ID != id {
		t.Fatalf("TillByName = %+v ok=%v err=%v, want till %s", got, ok, err, id)
	}
	if _, ok, _ := repo.TillByName(ctx, "Other"); ok {
		t.Fatal("unexpected match for a different name")
	}
}

func TestTillRow_SeenWithin(t *testing.T) {
	now := time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC) // fixed-date:ok pure arithmetic on its argument
	recent := TillRow{LastSeenAt: now.Add(-time.Minute).Format(time.RFC3339)}
	old := TillRow{LastSeenAt: now.Add(-time.Hour).Format(time.RFC3339)}
	if !recent.SeenWithin(now, 5*time.Minute) || old.SeenWithin(now, 5*time.Minute) || (TillRow{}).SeenWithin(now, time.Hour) {
		t.Fatal("SeenWithin: 1 minute ago is within 5 minutes; 1 hour ago and never are not")
	}
}

// A till seen since the cut-off is live, not gone: the replace is refused
// inside the write itself, so a device that came back between the caller's
// look and the update is never taken over (review of ut-docs#7057).
func TestReplaceEnrolment_RefusesATillSeenSinceTheCutoff(t *testing.T) {
	d := openMigratedDB(t, "tills-replace-live.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	id, _, _, err := repo.EnrolTillPrefix(ctx, "Bar tablet", "old-hash", TillRoleAdditional, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.DB.Exec(`UPDATE tills SET last_seen_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339), id); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReplaceEnrolment(ctx, id, "new-hash", TillRoleAdditional, time.Now().Add(-time.Minute)); !errors.Is(err, ErrTillStillLive) {
		t.Fatalf("err = %v, want ErrTillStillLive", err)
	}
	if _, ok, _ := repo.TillByBearerHashAt(ctx, "old-hash", time.Now()); !ok {
		t.Fatal("the live till's own bearer must keep working")
	}
}
