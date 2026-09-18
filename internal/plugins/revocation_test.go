package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRevocationEntryDecodesUtCloudWireFormat is the narrowest possible
// regression test for ut-docs#2380: no HTTP, no DB — just prove the JSON
// tags decode ut-cloud's actual field names, not the till-side snake_case
// this struct declared before the fix (which decoded every real feed
// entry's PluginID as "").
func TestRevocationEntryDecodesUtCloudWireFormat(t *testing.T) {
	var feed RevocationFeed
	body := []byte(`{"revocations":[{"pluginId":"com.acme.rogue","version":"2.1.0","action":"delete","reason":"malware"}],"latestVersion":"7"}`)
	if err := json.Unmarshal(body, &feed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(feed.Revocations) != 1 {
		t.Fatalf("revocations = %d, want 1", len(feed.Revocations))
	}
	got := feed.Revocations[0]
	want := RevocationEntry{PluginID: "com.acme.rogue", Version: "2.1.0", Action: "delete", Reason: "malware"}
	if got != want {
		t.Fatalf("decoded = %+v, want %+v", got, want)
	}
	if feed.LatestVersion != "7" {
		t.Fatalf("LatestVersion = %q, want %q", feed.LatestVersion, "7")
	}
}

// revocationFeedServer serves the LITERAL body given, not a
// json.Marshal(RevocationFeed{...}) round-trip through this package's own
// struct. That distinction is load-bearing (ut-docs#2380): a mock that
// encodes via the till's own struct tags would silently agree with
// whatever the till's decoder expects, even if the till's decoder is
// wrong — exactly how the pre-fix version of this test passed while
// production revocation enforcement was a complete no-op (ut-cloud's real
// GET /v1/revocations serves protojson-cased keys — pluginId/action/
// reason/version, no developer_id or revoked_at at all — never the
// snake_case this struct's tags used to declare). Every caller below
// writes ut-cloud's actual wire shape by hand so this test would have
// failed red against the bug this fix addresses.
func revocationFeedServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/revocations" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

func TestSyncRevocationsDisablesInstalledPlugin(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	seedInstalledPlugin(t, db, "com.test.revoked", "Bad Plugin", "1.0.0", "none", true)
	seedInstalledPlugin(t, db, "com.test.fine", "Fine Plugin", "1.0.0", "none", true)

	// ut-cloud's real wire format: camelCase pluginId, an action field,
	// no developer_id/revoked_at. See revocationFeedServer's own comment.
	srv := revocationFeedServer(t, `{"revocations":[`+
		`{"pluginId":"com.test.revoked","action":"disable","reason":"malware"},`+
		`{"pluginId":"com.test.notinstalled","action":"disable","reason":"x"}`+ // not installed → not counted, no-op
		`],"latestVersion":"3"}`)
	defer srv.Close()

	rc := NewRevocationChecker(db, srv.URL, nil)
	n, err := rc.SyncRevocations(ctx)
	if err != nil {
		t.Fatalf("SyncRevocations: %v", err)
	}
	// 1, not 2: only the genuinely-installed-and-disabled plugin counts.
	// com.test.notinstalled is a legitimate no-op, not a disable — this is
	// the counting-accuracy half of ut-docs#2380's fix (the pre-fix
	// version counted both, which is what let a misleading "disabled 2"
	// log line survive even when the decode bug meant nothing was ever
	// actually found or disabled).
	if n != 1 {
		t.Fatalf("processed = %d, want 1", n)
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

	// Second sync: plugin already disabled → no error, and now correctly
	// counted as 0 disables (it's a no-op, not a fresh disable).
	if n2, err := rc.SyncRevocations(ctx); err != nil {
		t.Fatalf("second sync: %v", err)
	} else if n2 != 0 {
		t.Fatalf("second sync processed = %d, want 0 (already disabled)", n2)
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
