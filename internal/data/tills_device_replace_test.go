package data

import (
	"context"
	"testing"
	"time"
)

// ut-docs#7098: ListTills carries the device id; BackfillDeviceID only fills an
// empty one and never moves a claimed id (first claim wins).
func TestTillsRepo_ListTillsCarriesDeviceIDAndBackfillFillsOnlyEmpty(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "tills-backfill.db")
	repo := NewTillsRepo(d.DB)
	a, _ := repo.InsertTill(ctx, "Phone", "h1")
	b, _ := repo.InsertTill(ctx, "Tablet", "h2")
	if err := repo.BackfillDeviceID(ctx, a, "dev-A"); err != nil {
		t.Fatal(err)
	}
	// already filled: a second, different id never replaces it
	if err := repo.BackfillDeviceID(ctx, a, "dev-X"); err != nil {
		t.Fatal(err)
	}
	// dev-A is claimed by another row: b gets nothing
	if err := repo.BackfillDeviceID(ctx, b, "dev-A"); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	list, _ := repo.ListTills(ctx)
	for _, r := range list {
		got[r.ID] = r.DeviceID
	}
	if got[a] != "dev-A" || got[b] != "" {
		t.Fatalf("device ids = %v, want a=dev-A b=empty", got)
	}
}

// An approved replace takes the device id over (unlike SetDeviceID).
func TestTillsRepo_ReplaceEnrolmentDeviceMovesTheClaim(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "tills-replace-dev.db")
	repo := NewTillsRepo(d.DB)
	a, _ := repo.InsertTill(ctx, "Phone", "h1")
	_ = repo.SetDeviceID(ctx, a, "dev-old")
	old, err := repo.ReplaceEnrolmentDevice(ctx, a, "dev-new")
	if err != nil || old != "dev-old" {
		t.Fatalf("old=%q err=%v, want dev-old", old, err)
	}
	row, ok, _ := repo.TillByDeviceID(ctx, "dev-new")
	if !ok || row.ID != a {
		t.Fatalf("dev-new maps to %+v ok=%v, want %s", row, ok, a)
	}
	// another row already holds the new id: refused, nothing changes
	b, _ := repo.InsertTill(ctx, "Tablet", "h2")
	_ = repo.SetDeviceID(ctx, b, "dev-B")
	if _, err := repo.ReplaceEnrolmentDevice(ctx, a, "dev-B"); err == nil {
		t.Fatal("taking a device id held by another till must fail")
	}
}

// The manager's replace choice is bound to the approval token.
func TestPairingRepo_ReplaceChoiceBoundToToken(t *testing.T) {
	ctx := context.Background()
	d := openMigratedDB(t, "pairing-replace.db")
	pr := NewPairingRepo(d.DB)
	idA, _ := pr.CreatePendingRequestForDevice(ctx, "phone-till2", "c", TillRoleAdditional, "", "dev-C", time.Hour)
	idB, _ := pr.CreatePendingRequestForDevice(ctx, "other", "c", TillRoleAdditional, "", "dev-D", time.Hour)
	if err := pr.ApproveWithReplace(ctx, idA, "tokA", "", "till-old", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := pr.ApproveWithReplace(ctx, idB, "tokB", "", "", time.Hour); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := pr.ReplaceTillForToken(ctx, "tokA"); !ok || got != "till-old" {
		t.Fatalf("tokA replace = %q ok=%v, want till-old", got, ok)
	}
	if got, ok, _ := pr.ReplaceTillForToken(ctx, "tokB"); !ok || got != "" {
		t.Fatalf("tokB replace = %q ok=%v, want empty", got, ok)
	}
	if _, ok, _ := pr.ReplaceTillForToken(ctx, "nope"); ok {
		t.Fatal("unknown token must not resolve")
	}
}
