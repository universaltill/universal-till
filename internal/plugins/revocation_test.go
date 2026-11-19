package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// TestRevocationEntryDecodesUtCloudWireFormat is the narrowest possible
// regression test for ut-docs#2380: no HTTP, no DB — just prove the JSON
// tags decode ut-cloud's actual field names, not the till-side snake_case
// this struct declared before the fix (which decoded every real feed
// entry's PluginID as "").
func TestRevocationEntryDecodesUtCloudWireFormat(t *testing.T) {
	var feed RevocationFeed
	body := []byte(`{"revocations":[{"pluginId":"com.acme.rogue","manifestId":"com.acme.rogue.manifest","version":"2.1.0","action":"delete","reason":"malware"}],"latestVersion":"7"}`)
	if err := json.Unmarshal(body, &feed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(feed.Revocations) != 1 {
		t.Fatalf("revocations = %d, want 1", len(feed.Revocations))
	}
	got := feed.Revocations[0]
	want := RevocationEntry{PluginID: "com.acme.rogue", ManifestID: "com.acme.rogue.manifest", Version: "2.1.0", Action: "delete", Reason: "malware"}
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

// ---- ut-docs#5653: the feed's pluginId is the marketplace listing UUID ----

const testListingUUID = "7b0c6f0e-2d55-4c1a-9d8e-3f1a2b4c5d6e"

func seedInstallStatus(t *testing.T, db *sql.DB, listingID, pluginID string) {
	t.Helper()
	if err := data.NewInstallStatusRepo(db).Upsert(context.Background(), data.InstallStatusRow{
		ListingID: listingID, PluginID: pluginID, State: "Installed",
		CurrentVersion: "1.2.0", UpdatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("seed install status: %v", err)
	}
}

func revokeFeed(pluginID, manifestID, version string) string {
	b := `{"revocations":[{"pluginId":"` + pluginID + `"`
	if manifestID != "" {
		b += `,"manifestId":"` + manifestID + `"`
	}
	if version != "" {
		b += `,"version":"` + version + `"`
	}
	return b + `,"action":"disable","reason":"malware"}],"latestVersion":"1"}`
}

func pluginState(t *testing.T, db *sql.DB, id string) (int, string) {
	t.Helper()
	var active int
	var state string
	if err := db.QueryRow(`SELECT is_active, install_state FROM plugins WHERE id = ?`, id).Scan(&active, &state); err != nil {
		t.Fatalf("query plugin %s: %v", id, err)
	}
	return active, state
}

func auditCountFor(t *testing.T, db *sql.DB, id string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'disable_revoked' AND entity_id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("audit count: %v", err)
	}
	return n
}

func revAuditDetails(t *testing.T, db *sql.DB, id string) map[string]any {
	t.Helper()
	var raw string
	if err := db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'disable_revoked' AND entity_id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("audit row for %s: %v", id, err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("audit json: %v", err)
	}
	return m
}

func syncFeed(t *testing.T, db *sql.DB, body string) (int, *RevocationChecker) {
	t.Helper()
	srv := revocationFeedServer(t, body)
	t.Cleanup(srv.Close)
	rc := NewRevocationChecker(db, srv.URL, nil)
	n, err := rc.SyncRevocations(context.Background())
	if err != nil {
		t.Fatalf("SyncRevocations: %v", err)
	}
	return n, rc
}

func TestSyncRevocations_ListingUUIDResolvesViaInstallStatus(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.x.y", "XY", "1.2.0", "none", true)
	seedInstallStatus(t, db, testListingUUID, "com.x.y")

	n, _ := syncFeed(t, db, revokeFeed(testListingUUID, "", "1.2.0"))
	if n != 1 {
		t.Fatalf("n = %d, want 1", n)
	}
	if active, state := pluginState(t, db, "com.x.y"); active != 0 || state != "revoked" {
		t.Fatalf("active=%d state=%q", active, state)
	}
	d := revAuditDetails(t, db, "com.x.y")
	if d["listing_id"] != testListingUUID || d["matched_by"] != "listing_id" {
		t.Fatalf("audit details = %v", d)
	}
}

func TestSyncRevocations_VersionMismatchLeavesPluginActive(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.x.y", "XY", "1.2.0", "none", true)
	seedInstallStatus(t, db, testListingUUID, "com.x.y")

	n, _ := syncFeed(t, db, revokeFeed(testListingUUID, "", "1.1.0"))
	if n != 0 {
		t.Fatalf("n = %d, want 0", n)
	}
	if active, _ := pluginState(t, db, "com.x.y"); active != 1 {
		t.Fatal("plugin disabled despite version mismatch")
	}
}

func TestSyncRevocations_AlreadyInactiveIsNoop(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.x.y", "XY", "1.2.0", "none", false)
	seedInstallStatus(t, db, testListingUUID, "com.x.y")

	n, _ := syncFeed(t, db, revokeFeed(testListingUUID, "", "1.2.0"))
	if n != 0 || auditCountFor(t, db, "com.x.y") != 0 {
		t.Fatalf("n=%d audit=%d, want 0/0", n, auditCountFor(t, db, "com.x.y"))
	}
}

func TestSyncRevocations_UnknownUUIDIsNoop(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.x.y", "XY", "1.2.0", "none", true)

	n, _ := syncFeed(t, db, revokeFeed(testListingUUID, "", "1.2.0"))
	if n != 0 {
		t.Fatalf("n = %d, want 0", n)
	}
	if active, _ := pluginState(t, db, "com.x.y"); active != 1 {
		t.Fatal("unrelated plugin disabled")
	}
}

func TestSyncRevocations_BlankInstallStatusPluginIDIsNoop(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.x.y", "XY", "1.2.0", "none", true)
	seedInstallStatus(t, db, testListingUUID, "")

	n, _ := syncFeed(t, db, revokeFeed(testListingUUID, "", "1.2.0"))
	if n != 0 {
		t.Fatalf("n = %d, want 0", n)
	}
	if active, _ := pluginState(t, db, "com.x.y"); active != 1 {
		t.Fatal("plugin disabled via blank install status")
	}
}

func TestSyncRevocations_StaleInstallStatusRowIsNoop(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.x.y", "XY", "1.2.0", "none", true)
	seedInstallStatus(t, db, testListingUUID, "com.gone.away")

	n, _ := syncFeed(t, db, revokeFeed(testListingUUID, "", "1.2.0"))
	if n != 0 {
		t.Fatalf("n = %d, want 0", n)
	}
	if active, _ := pluginState(t, db, "com.x.y"); active != 1 {
		t.Fatal("plugin disabled via stale install status")
	}
}

func TestSyncRevocations_ManifestIDFallbackWithoutInstallStatus(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.x.y", "XY", "1.2.0", "none", true)

	n, _ := syncFeed(t, db, revokeFeed("com.x.y", "", "1.2.0"))
	if n != 1 {
		t.Fatalf("n = %d, want 1", n)
	}
	if d := revAuditDetails(t, db, "com.x.y"); d["matched_by"] != "plugin_id" {
		t.Fatalf("matched_by = %v, want plugin_id", d["matched_by"])
	}
}

func TestSyncRevocations_PrefersFeedManifestID(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.x.y", "XY", "1.2.0", "none", true)

	n, _ := syncFeed(t, db, revokeFeed(testListingUUID, "com.x.y", "1.2.0"))
	if n != 1 {
		t.Fatalf("n = %d, want 1", n)
	}
	d := revAuditDetails(t, db, "com.x.y")
	if d["matched_by"] != "manifest_id" || d["listing_id"] != testListingUUID {
		t.Fatalf("audit details = %v", d)
	}
}

func TestSyncRevocations_InvokesOnRevokedOnceOnlyWhenSomethingDisabled(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.x.a", "A", "1.0.0", "wasm", true)
	seedInstalledPlugin(t, db, "com.x.b", "B", "1.0.0", "wasm", true)

	body := `{"revocations":[` +
		`{"pluginId":"com.x.a","action":"disable","reason":"r"},` +
		`{"pluginId":"com.x.b","action":"disable","reason":"r"}]}`
	srv := revocationFeedServer(t, body)
	defer srv.Close()
	calls := 0
	rc := NewRevocationChecker(db, srv.URL, nil)
	rc.SetOnRevoked(func(context.Context) error { calls++; return errors.New("reload boom") })
	n, err := rc.SyncRevocations(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v, want 2/nil (callback error must not fail sync)", n, err)
	}
	if calls != 1 {
		t.Fatalf("onRevoked calls = %d, want 1", calls)
	}

	// Second pass: everything already disabled -> callback not invoked.
	calls = 0
	if n, err := rc.SyncRevocations(context.Background()); err != nil || n != 0 {
		t.Fatalf("second: n=%d err=%v", n, err)
	}
	if calls != 0 {
		t.Fatalf("onRevoked calls on noop feed = %d, want 0", calls)
	}
}

// ---- ut-docs#5658: durable record, delete = disable, lifting ----

func revokedRecord(t *testing.T, db *sql.DB, id, version string) bool {
	t.Helper()
	_, ok, err := data.NewPluginRepo(db).RevokedRelease(context.Background(), nil, id, version)
	if err != nil {
		t.Fatalf("RevokedRelease: %v", err)
	}
	return ok
}

func TestSyncRevocations_RecordsEvenWhenNotInstalled(t *testing.T) {
	db := managerTestDB(t)
	syncFeed(t, db, revokeFeed("com.test.ghost", "com.test.ghost", "1.0.0"))
	if !revokedRecord(t, db, "com.test.ghost", "1.0.0") {
		t.Fatal("a revocation for a not-installed plugin must still be recorded")
	}
	if revokedRecord(t, db, "com.test.ghost", "2.0.0") {
		t.Fatal("record is per version")
	}
}

func TestSyncRevocations_RecordsViaInstallStatusWhenNoManifestID(t *testing.T) {
	db := managerTestDB(t)
	seedInstallStatus(t, db, testListingUUID, "com.test.viastatus")
	syncFeed(t, db, revokeFeed(testListingUUID, "", "1.2.0"))
	if !revokedRecord(t, db, "com.test.viastatus", "1.2.0") {
		t.Fatal("record must be keyed by the manifest id from plugin_install_status")
	}
}

func TestSyncRevocations_DeleteActionDisablesOnly(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.test.del", "Del", "1.0.0", "none", true)
	if _, err := db.Exec(`INSERT INTO plugin_settings (id, plugin_id, key, value_json) VALUES ('s1', 'com.test.del', 'k', '"v"')`); err != nil {
		t.Fatalf("seed setting: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_storage (plugin_id, key, value) VALUES ('com.test.del', 'k', 'v')`); err != nil {
		t.Fatalf("seed storage: %v", err)
	}
	body := `{"revocations":[{"pluginId":"com.test.del","manifestId":"com.test.del","version":"1.0.0","action":"delete","reason":"gone"}],"latestVersion":"1"}`
	n, _ := syncFeed(t, db, body)
	if n != 1 {
		t.Fatalf("disabled = %d, want 1", n)
	}
	active, state := pluginState(t, db, "com.test.del")
	if active != 0 || state != "revoked" {
		t.Fatalf("active=%d state=%q, want 0/revoked", active, state)
	}
	for _, tbl := range []string{"plugin_settings", "plugin_storage"} {
		var c int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + tbl + ` WHERE plugin_id = 'com.test.del'`).Scan(&c); err != nil || c != 1 {
			t.Fatalf("%s rows = %d err=%v, want 1 (delete must not wipe data)", tbl, c, err)
		}
	}
	if !revokedRecord(t, db, "com.test.del", "1.0.0") {
		t.Fatal("record must exist")
	}
	if got := revAuditDetails(t, db, "com.test.del")["requested_action"]; got != "delete" {
		t.Fatalf("audit requested_action = %v, want delete", got)
	}
}

func TestSyncRevocations_LiftsWhenAbsent_StaysDisabled(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.test.lift", "Lift", "1.0.0", "none", true)
	syncFeed(t, db, revokeFeed("com.test.lift", "com.test.lift", "1.0.0"))
	if !revokedRecord(t, db, "com.test.lift", "1.0.0") {
		t.Fatal("record missing after revoke")
	}
	syncFeed(t, db, `{"revocations":[],"latestVersion":"2"}`)
	if revokedRecord(t, db, "com.test.lift", "1.0.0") {
		t.Fatal("record must be lifted when the feed no longer lists it")
	}
	if active, state := pluginState(t, db, "com.test.lift"); active != 0 || state != "revoked" {
		t.Fatalf("lifting must not re-enable: active=%d state=%q", active, state)
	}
}

func TestSyncRevocations_FailedFetchKeepsRecords(t *testing.T) {
	cases := map[string]func() string{
		"500": func() string {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
			t.Cleanup(s.Close)
			return s.URL
		},
		"garbage": func() string {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{nope")) }))
			t.Cleanup(s.Close)
			return s.URL
		},
		"closed": func() string { return "http://127.0.0.1:1" },
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			db := managerTestDB(t)
			syncFeed(t, db, revokeFeed("com.test.keep", "com.test.keep", "1.0.0"))
			if _, err := NewRevocationChecker(db, mk(), nil).SyncRevocations(context.Background()); err == nil {
				t.Fatal("expected an error")
			}
			if !revokedRecord(t, db, "com.test.keep", "1.0.0") {
				t.Fatal("a failed fetch must keep the existing records")
			}
		})
	}
}
