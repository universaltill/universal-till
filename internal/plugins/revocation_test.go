package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func revocationFeedServer(t *testing.T, entries []RevocationEntry) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/revocations" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(RevocationFeed{Revocations: entries, UpdatedAt: time.Now().UTC()})
	}))
}

func TestSyncRevocationsDisablesInstalledPlugin(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	seedInstalledPlugin(t, db, "com.test.revoked", "Bad Plugin", "1.0.0", "none", true)
	seedInstalledPlugin(t, db, "com.test.fine", "Fine Plugin", "1.0.0", "none", true)

	srv := revocationFeedServer(t, []RevocationEntry{
		{PluginID: "com.test.revoked", DeveloperID: "dev-1", Reason: "malware", RevokedAt: time.Now().UTC()},
		{PluginID: "com.test.notinstalled", Reason: "x"}, // not installed → counted, no-op
	})
	defer srv.Close()

	rc := NewRevocationChecker(db, srv.URL, nil)
	n, err := rc.SyncRevocations(ctx)
	if err != nil {
		t.Fatalf("SyncRevocations: %v", err)
	}
	if n != 2 {
		t.Fatalf("processed = %d, want 2", n)
	}

	var active int
	var state string
	if err := db.QueryRow(`SELECT is_active, install_state FROM plugins WHERE id = 'com.test.revoked'`).Scan(&active, &state); err != nil {
		t.Fatalf("query: %v", err)
	}
	if active != 0 || state != "revoked" {
		t.Fatalf("revoked plugin state: active=%d install_state=%q", active, state)
	}
	// The innocent plugin is untouched.
	if err := db.QueryRow(`SELECT is_active FROM plugins WHERE id = 'com.test.fine'`).Scan(&active); err != nil {
		t.Fatalf("query: %v", err)
	}
	if active != 1 {
		t.Fatalf("unrevoked plugin was disabled")
	}

	// The disable is audit-logged.
	var auditCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'disable_revoked' AND entity_id = 'com.test.revoked'`).Scan(&auditCount); err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("audit rows = %d, want 1", auditCount)
	}

	// Second sync: plugin already disabled → still processed without error.
	if _, err := rc.SyncRevocations(ctx); err != nil {
		t.Fatalf("second sync: %v", err)
	}

	// GetRevokedPlugins surfaces it.
	revoked, err := rc.GetRevokedPlugins(ctx)
	if err != nil {
		t.Fatalf("GetRevokedPlugins: %v", err)
	}
	if len(revoked) != 1 || revoked[0].PluginID != "com.test.revoked" {
		t.Fatalf("revoked list = %+v", revoked)
	}
}

func TestSyncRevocationsErrorPaths(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()

	// Non-200 from the marketplace.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if _, err := NewRevocationChecker(db, bad.URL, nil).SyncRevocations(ctx); err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("500 not surfaced: %v", err)
	}

	// Malformed feed body.
	garbage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("{not json"))
	}))
	defer garbage.Close()
	if _, err := NewRevocationChecker(db, garbage.URL, nil).SyncRevocations(ctx); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Fatalf("bad json not surfaced: %v", err)
	}

	// Unreachable marketplace (closed port) — fails, doesn't hang.
	if _, err := NewRevocationChecker(db, "http://127.0.0.1:1", nil).SyncRevocations(ctx); err == nil || !strings.Contains(err.Error(), "fetch revocations") {
		t.Fatalf("unreachable not surfaced: %v", err)
	}
}
