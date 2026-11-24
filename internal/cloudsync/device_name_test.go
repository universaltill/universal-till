package cloudsync

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
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

// ut-docs#2802: the heartbeat reports sync.till_id when set (tier 1 of
// discovery.ReportedTillID, ut-docs#3307).
func TestBuildSyncRequestReportsTillID(t *testing.T) {
	db := testDB(t)
	settings := data.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settings.Set(ctx, "sync.till_id", "till-abc"); err != nil {
		t.Fatal(err)
	}
	req := buildSyncRequest(ctx, testCfg(""), settings, Hooks{})
	if got := req.devices[0]["till_id"]; got != "till-abc" {
		t.Fatalf("till_id = %v, want till-abc", got)
	}
}

// ut-docs#3307: a main till with no sync.till_id reports its own LAN id. It
// is minted only through discovery.TillID (lan_discovery.till_id), the one
// source of truth — the value reported is exactly the stored LAN id, and a
// second heartbeat reports the same id.
func TestBuildSyncRequestUnsetTillIDReportsLANID(t *testing.T) {
	for name, seed := range map[string]string{"unset": "", "whitespace": "  "} {
		t.Run(name, func(t *testing.T) {
			db := testDB(t)
			settings := data.NewSettingsRepo(db)
			ctx := context.Background()
			if seed != "" {
				if err := settings.Set(ctx, "sync.till_id", seed); err != nil {
					t.Fatal(err)
				}
			}
			req := buildSyncRequest(ctx, testCfg(""), settings, Hooks{})
			got, _ := req.devices[0]["till_id"].(string)
			if got == "" {
				t.Fatalf("till_id missing: %v", req.devices[0])
			}
			lan, ok, _ := settings.Get(ctx, discovery.TillIDSettingKey)
			if !ok || lan != got {
				t.Fatalf("till_id = %q, want the stored %s %q (ok=%v)", got, discovery.TillIDSettingKey, lan, ok)
			}
			again := buildSyncRequest(ctx, testCfg(""), settings, Hooks{})
			if again.devices[0]["till_id"] != got {
				t.Fatalf("till_id not stable: %q then %v", got, again.devices[0]["till_id"])
			}
		})
	}
}

// ut-docs#3307: a till promoted back to main after being joined keeps
// reporting the cloud id it was known by, and mints no fresh LAN id.
func TestBuildSyncRequestReportsKeptCloudID(t *testing.T) {
	db := testDB(t)
	settings := data.NewSettingsRepo(db)
	ctx := context.Background()
	if err := settings.Set(ctx, data.TillIdentityCloudIDSettingsKey, "kept-id"); err != nil {
		t.Fatal(err)
	}
	req := buildSyncRequest(ctx, testCfg(""), settings, Hooks{})
	if got := req.devices[0]["till_id"]; got != "kept-id" {
		t.Fatalf("till_id = %v, want kept-id", got)
	}
	if v, minted, _ := settings.Get(ctx, discovery.TillIDSettingKey); minted {
		t.Fatalf("heartbeat minted a LAN till id %q despite a kept cloud id", v)
	}
}

// ADR-0198 Amendment 2026-11-23 go-live precondition (ut-cloud till_role):
// a joined till reports its own role in its devices[] entry; a main or
// standalone till (and an invalid value) omits it.
func TestBuildSyncRequestReportsTheJoinedTillRole(t *testing.T) {
	for _, tc := range []struct {
		name     string
		settings map[string]string
		want     string // "" = key absent
	}{
		{"additional", map[string]string{"sync.primary_url": "http://main.example", "sync.till_role": "additional"}, "additional"},
		{"satellite", map[string]string{"sync.primary_url": "http://main.example", "sync.till_role": "satellite"}, "satellite"},
		{"kiosk display on a joined till", map[string]string{"sync.primary_url": "http://main.example", "sync.till_role": "additional", "display.mode": "self_order"}, "kiosk"},
		{"main till", map[string]string{}, ""},
		{"stale role on a main till", map[string]string{"sync.till_role": "satellite"}, ""},
		{"invalid role", map[string]string{"sync.primary_url": "http://main.example", "sync.till_role": "admin"}, ""},
		{"joined, role unknown", map[string]string{"sync.primary_url": "http://main.example"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testDB(t)
			settings := data.NewSettingsRepo(db)
			for k, v := range tc.settings {
				if err := settings.Set(context.Background(), k, v); err != nil {
					t.Fatal(err)
				}
			}
			req := buildSyncRequest(context.Background(), testCfg(""), settings, Hooks{})
			got, has := req.devices[0]["till_role"]
			if tc.want == "" && has {
				t.Fatalf("till_role = %v, want absent", got)
			}
			if tc.want != "" && got != tc.want {
				t.Fatalf("till_role = %v, want %q", got, tc.want)
			}
		})
	}
}
