package app

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#5897: boot repair of a discovery id inherited from the former main
// till of a till promoted before #5893.

func TestRepairInheritedDiscoveryIDOnPrimary_MirrorSignalFromSalesMirrorState(t *testing.T) {
	sqlDB := openBackfillBootDB(t)
	ctx := context.Background()
	repo := data.NewSettingsRepo(sqlDB)
	if err := repo.Set(ctx, data.LANDiscoveryTillIDSettingsKey, "old-main"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`INSERT INTO mirror_state (key, value) VALUES ('main_till_id', 'old-main')`); err != nil {
		t.Fatal(err)
	}
	lg := &recordingLog{}

	repairInheritedDiscoveryIDOnPrimary(ctx, sqlDB, lg)

	got, _, _ := repo.Get(ctx, data.LANDiscoveryTillIDSettingsKey)
	if got == "old-main" || got == "" {
		t.Fatalf("id not re-minted: %q", got)
	}
	if len(lg.errors) != 0 || len(lg.infos) != 1 || !strings.Contains(lg.infos[0], "old-main") || !strings.Contains(lg.infos[0], got) {
		t.Fatalf("logs: infos=%v errors=%v", lg.infos, lg.errors)
	}
	// Second boot: nothing to do, nothing logged.
	repairInheritedDiscoveryIDOnPrimary(ctx, sqlDB, lg)
	if again, _, _ := repo.Get(ctx, data.LANDiscoveryTillIDSettingsKey); again != got || len(lg.infos) != 1 {
		t.Fatal("second boot re-minted or logged")
	}
}

func TestRepairInheritedDiscoveryIDOnPrimary_DeferredIsLogged(t *testing.T) {
	sqlDB := openBackfillBootDB(t)
	ctx := context.Background()
	repo := data.NewSettingsRepo(sqlDB)
	_ = repo.Set(ctx, data.LANDiscoveryTillIDSettingsKey, "old-main")
	_ = repo.Set(ctx, data.TillIdentityCloudIDSettingsKey, "c")
	if _, err := sqlDB.Exec(`INSERT INTO tills (id, name, bearer_hash) VALUES ('t1', 'T', 'h')`); err != nil {
		t.Fatal(err)
	}
	lg := &recordingLog{}
	repairInheritedDiscoveryIDOnPrimary(ctx, sqlDB, lg)
	if got, _, _ := repo.Get(ctx, data.LANDiscoveryTillIDSettingsKey); got != "old-main" {
		t.Fatalf("id changed: %q", got)
	}
	if len(lg.infos) != 1 || !strings.Contains(lg.infos[0], "joined_tills") || !strings.Contains(lg.infos[0], "next boot") {
		t.Fatalf("infos=%v", lg.infos)
	}
}

func TestRepairInheritedDiscoveryIDOnPrimary_ErrorIsLoggedNotFatal(t *testing.T) {
	sqlDB := openBackfillBootDB(t)
	ctx := context.Background()
	repo := data.NewSettingsRepo(sqlDB)
	_ = repo.Set(ctx, data.LANDiscoveryTillIDSettingsKey, "old-main")
	_ = repo.Set(ctx, data.TillIdentityCloudIDSettingsKey, "c")
	if _, err := sqlDB.Exec(`CREATE TRIGGER fail_audit BEFORE INSERT ON audit_log BEGIN SELECT RAISE(ABORT, 'x'); END`); err != nil {
		t.Fatal(err)
	}
	lg := &recordingLog{}
	repairInheritedDiscoveryIDOnPrimary(ctx, sqlDB, lg)
	if len(lg.errors) != 1 {
		t.Fatalf("errors=%v", lg.errors)
	}
	if got, _, _ := repo.Get(ctx, data.LANDiscoveryTillIDSettingsKey); got != "old-main" {
		t.Fatalf("id changed on failure: %q", got)
	}
}
