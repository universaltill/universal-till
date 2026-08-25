package app

// Tests for the ut-docs#1040 install-time provisioner: on a fresh .deb
// install on a Pi running a DESKTOP OS, postinstall.sh runs
// `unitill-pos provision-desktop-kiosk-defaults` (as the pos service user,
// against the real service DB) to seed WindowMode=kiosk +
// LaunchOnStartup=true and record a system-actor audit entry, so the
// decision is visible to the owner on /audit — not just in installer
// stdout. TDD-first per the pipeline's standing convention: these were
// written (and watched fail: "undefined: provisionDesktopKioskDefaults")
// before provision.go existed.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func openProvisionTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "unitill-pos.db"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func auditRows(t *testing.T, database *db.DB, action string) []string {
	t.Helper()
	rows, err := database.Query(
		`SELECT COALESCE(actor_id, ''), COALESCE(data_json, '') FROM audit_log WHERE action = ?`, action)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	defer rows.Close()
	var payloads []string
	for rows.Next() {
		var actor, payload string
		if err := rows.Scan(&actor, &payload); err != nil {
			t.Fatalf("scan audit_log: %v", err)
		}
		if actor != "system" {
			t.Errorf("audit actor = %q, want %q (an install-time action has no session user)", actor, "system")
		}
		payloads = append(payloads, payload)
	}
	return payloads
}

func TestProvisionDesktopKioskDefaults_SeedsSettingsAndAudits(t *testing.T) {
	database := openProvisionTestDB(t)
	ctx := context.Background()

	did, err := provisionDesktopKioskDefaults(ctx, database.DB, "deb-postinstall", true)
	if err != nil {
		t.Fatalf("provisionDesktopKioskDefaults: %v", err)
	}
	if !did {
		t.Fatal("first run reported nothing to do on a fresh DB")
	}

	settingsRepo := data.NewSettingsRepo(database.DB)
	if v, ok, _ := settingsRepo.Get(ctx, common.KeyWindowMode); !ok || v != "kiosk" {
		t.Errorf("%s = %q (present=%v), want %q", common.KeyWindowMode, v, ok, "kiosk")
	}
	if v, ok, _ := settingsRepo.Get(ctx, common.KeyLaunchOnStartup); !ok || v != "true" {
		t.Errorf("%s = %q (present=%v), want %q", common.KeyLaunchOnStartup, v, ok, "true")
	}
	if _, ok, _ := settingsRepo.Get(ctx, keyDesktopKioskOverlayProvisioned); !ok {
		t.Errorf("completion marker %s not written", keyDesktopKioskOverlayProvisioned)
	}

	payloads := auditRows(t, database, auditActionDesktopKioskOverlayProvisioned)
	if len(payloads) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", len(payloads))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloads[0]), &payload); err != nil {
		t.Fatalf("audit payload is not JSON: %v (%q)", err, payloads[0])
	}
	if payload["trigger"] != "deb-postinstall" {
		t.Errorf("audit payload trigger = %v, want %q — the entry must record what triggered the provisioning", payload["trigger"], "deb-postinstall")
	}
	if payload["autostart_staged"] != true {
		t.Errorf("audit payload autostart_staged = %v, want true", payload["autostart_staged"])
	}
	if payload["window_mode"] != "kiosk" {
		t.Errorf("audit payload window_mode = %v, want %q", payload["window_mode"], "kiosk")
	}
}

func TestProvisionDesktopKioskDefaults_IsIdempotent(t *testing.T) {
	database := openProvisionTestDB(t)
	ctx := context.Background()

	if _, err := provisionDesktopKioskDefaults(ctx, database.DB, "deb-postinstall", true); err != nil {
		t.Fatalf("first run: %v", err)
	}

	// An owner later changes their mind in Settings…
	settingsRepo := data.NewSettingsRepo(database.DB)
	if err := settingsRepo.Set(ctx, common.KeyWindowMode, "normal"); err != nil {
		t.Fatalf("simulate owner change: %v", err)
	}

	// …and a re-run (postinstall is idempotent by contract, same as the
	// headless path's --auto) must neither clobber that choice nor write a
	// second audit row.
	did, err := provisionDesktopKioskDefaults(ctx, database.DB, "deb-postinstall", true)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if did {
		t.Error("second run reported work done — must be a no-op once the marker exists")
	}
	if v, _, _ := settingsRepo.Get(ctx, common.KeyWindowMode); v != "normal" {
		t.Errorf("re-run clobbered the owner's window mode: got %q, want %q", v, "normal")
	}
	if n := len(auditRows(t, database, auditActionDesktopKioskOverlayProvisioned)); n != 1 {
		t.Errorf("audit rows after re-run = %d, want exactly 1", n)
	}
}

func TestProvisionDesktopKioskDefaults_RecordsAutostartNotStaged(t *testing.T) {
	// postinstall can fail to stage the autostart entry (e.g. the desktop
	// shell binary can't exec because the WebKit libs were skipped with
	// --no-install-recommends) — the audit entry must say so honestly.
	database := openProvisionTestDB(t)
	if _, err := provisionDesktopKioskDefaults(context.Background(), database.DB, "deb-postinstall", false); err != nil {
		t.Fatalf("provisionDesktopKioskDefaults: %v", err)
	}
	payloads := auditRows(t, database, auditActionDesktopKioskOverlayProvisioned)
	if len(payloads) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(payloads))
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloads[0]), &payload); err != nil {
		t.Fatalf("audit payload is not JSON: %v", err)
	}
	if payload["autostart_staged"] != false {
		t.Errorf("audit payload autostart_staged = %v, want false", payload["autostart_staged"])
	}
}
