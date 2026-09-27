package cloudsync

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3019: the heartbeat reports the name the owner typed (till.name)
// on a main till, and a replica's own sync.till_name (enroll.DeviceName).
func TestBuildSyncRequestDeviceNameMainTillUsesTillName(t *testing.T) {
	db := testDB(t)
	settings := data.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settings.Set(ctx, "till.name", "Front Counter"); err != nil {
		t.Fatalf("seed till.name: %v", err)
	}
	cfg := testCfg("")

	req := buildSyncRequest(ctx, cfg, settings, Hooks{})
	if got := req.devices[0]["name"]; got != "Front Counter" {
		t.Fatalf("device name = %v, want %q", got, "Front Counter")
	}
}

func TestBuildSyncRequestDeviceNameReplicaUsesSyncTillName(t *testing.T) {
	db := testDB(t)
	settings := data.NewSettingsRepo(db)
	ctx := context.Background()
	for k, v := range map[string]string{
		"sync.primary_url": "https://main.example.lan",
		"till.name":        "Main Till's Name",
		"sync.till_name":   "Back Office",
	} {
		if err := settings.Set(ctx, k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
	cfg := testCfg("")

	req := buildSyncRequest(ctx, cfg, settings, Hooks{})
	if got := req.devices[0]["name"]; got != "Back Office" {
		t.Fatalf("device name = %v, want the replica's own sync.till_name %q (not till.name)", got, "Back Office")
	}
}

// A replica with no sync.till_name of its own must still default to "Till"
// — never fall back to till.name, which is the main till's name once copied
// there by the admin sync.
func TestBuildSyncRequestDeviceNameReplicaWithNoOwnNameDefaultsToTill(t *testing.T) {
	db := testDB(t)
	settings := data.NewSettingsRepo(db)
	ctx := context.Background()
	for k, v := range map[string]string{
		"sync.primary_url": "https://main.example.lan",
		"till.name":        "Main Till's Name",
	} {
		if err := settings.Set(ctx, k, v); err != nil {
			t.Fatalf("seed %s: %v", k, err)
		}
	}
	cfg := testCfg("")

	req := buildSyncRequest(ctx, cfg, settings, Hooks{})
	if got := req.devices[0]["name"]; got != "Till" {
		t.Fatalf("device name = %v, want the \"Till\" default (never the main till's till.name)", got)
	}
}
