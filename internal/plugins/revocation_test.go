package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

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

// ---- ut-docs#5659: since_version cursor, 24 h full refresh, body cap ----

// cursorFeedServer serves body and records the raw query of every request.
type cursorFeedServer struct {
	*httptest.Server
	queries []string
}

func newCursorFeedServer(t *testing.T, body string) *cursorFeedServer {
	t.Helper()
	s := &cursorFeedServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.queries = append(s.queries, r.URL.RawQuery)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *cursorFeedServer) last() string { return s.queries[len(s.queries)-1] }

func feedWithLatest(latest string) string {
	return `{"revocations":[],"latestVersion":"` + latest + `"}`
}

func settingVal(t *testing.T, db *sql.DB, key string) (string, bool) {
	t.Helper()
	v, ok, err := data.NewSettingsRepo(db).Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	return v, ok
}

func setSetting(t *testing.T, db *sql.DB, key, val string) {
	t.Helper()
	if err := data.NewSettingsRepo(db).Set(context.Background(), key, val); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

func checkerAt(db *sql.DB, url string, now time.Time) *RevocationChecker {
	rc := NewRevocationChecker(db, url, nil)
	rc.now = func() time.Time { return now }
	return rc
}

func TestMaxRevocationFeedBytes(t *testing.T) {
	if maxRevocationFeedBytes != 4<<20 {
		t.Fatalf("cap = %d, want 4 MiB", maxRevocationFeedBytes)
	}
}

// AC1
func TestSyncRevocations_FirstRunFullFetchPersistsCursor(t *testing.T) {
	db := managerTestDB(t)
	srv := newCursorFeedServer(t, feedWithLatest("1700000000123456789"))
	now := time.Now()
	if _, err := checkerAt(db, srv.URL, now).SyncRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.last() != "" {
		t.Fatalf("first run query = %q, want none", srv.last())
	}
	if v, ok := settingVal(t, db, revocationCursorKey); !ok || v != "1700000000123456789" {
		t.Fatalf("cursor = %q,%v", v, ok)
	}
	if v, _ := settingVal(t, db, revocationFullAtKey); v != strconv.FormatInt(now.Unix(), 10) {
		t.Fatalf("full_at = %q, want %d", v, now.Unix())
	}
}

// AC2
func TestSyncRevocations_SecondRunSendsCursor(t *testing.T) {
	db := managerTestDB(t)
	srv := newCursorFeedServer(t, feedWithLatest("42"))
	now := time.Now()
	if _, err := checkerAt(db, srv.URL, now).SyncRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	// A brand-new checker on the same DB: the cursor round-trips.
	if _, err := checkerAt(db, srv.URL, now.Add(time.Hour)).SyncRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.last() != "since_version=42" {
		t.Fatalf("second run query = %q, want since_version=42", srv.last())
	}
}

// AC3
func TestSyncRevocations_FullRefreshAfter24h(t *testing.T) {
	db := managerTestDB(t)
	now := time.Now()
	srv := newCursorFeedServer(t, feedWithLatest("42"))
	setSetting(t, db, revocationCursorKey, "42")

	// 23h59m old: still incremental, full_at untouched.
	recent := now.Add(-24*time.Hour + time.Minute).Unix()
	setSetting(t, db, revocationFullAtKey, strconv.FormatInt(recent, 10))
	if _, err := checkerAt(db, srv.URL, now).SyncRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.last() != "since_version=42" {
		t.Fatalf("23h59 query = %q", srv.last())
	}
	if v, _ := settingVal(t, db, revocationFullAtKey); v != strconv.FormatInt(recent, 10) {
		t.Fatalf("incremental poll moved full_at to %q", v)
	}

	// 25h old: full fetch despite the stored cursor, full_at advances.
	setSetting(t, db, revocationFullAtKey, strconv.FormatInt(now.Add(-25*time.Hour).Unix(), 10))
	if _, err := checkerAt(db, srv.URL, now).SyncRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.last() != "" {
		t.Fatalf("25h query = %q, want none", srv.last())
	}
	if v, _ := settingVal(t, db, revocationFullAtKey); v != strconv.FormatInt(now.Unix(), 10) {
		t.Fatalf("full_at = %q, want %d", v, now.Unix())
	}

	// full_at in the future (clock skew) is treated as stale.
	setSetting(t, db, revocationFullAtKey, strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10))
	if _, err := checkerAt(db, srv.URL, now).SyncRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if srv.last() != "" {
		t.Fatalf("future full_at query = %q, want none", srv.last())
	}
}

// AC4
func TestSyncRevocations_BadLatestVersionKeepsCursor(t *testing.T) {
	for _, latest := range []string{"", "abc", "-5", "0", "10", "41"} {
		t.Run("latest="+latest, func(t *testing.T) {
			db := managerTestDB(t)
			now := time.Now()
			srv := newCursorFeedServer(t, feedWithLatest(latest))
			setSetting(t, db, revocationCursorKey, "42")
			setSetting(t, db, revocationFullAtKey, strconv.FormatInt(now.Unix(), 10))
			if _, err := checkerAt(db, srv.URL, now).SyncRevocations(context.Background()); err != nil {
				t.Fatal(err)
			}
			if v, _ := settingVal(t, db, revocationCursorKey); v != "42" {
				t.Fatalf("cursor = %q, want 42 kept", v)
			}
		})
	}
}

// AC5
func TestSyncRevocations_EntryErrorBlocksAdvance(t *testing.T) {
	db := managerTestDB(t)
	now := time.Now()
	seedInstalledPlugin(t, db, "com.x.ok", "OK", "1.0.0", "none", true)
	// An entry naming only a listing UUID queries plugin_install_status;
	// dropping the table makes that lookup fail.
	if _, err := db.Exec(`DROP TABLE plugin_install_status`); err != nil {
		t.Fatal(err)
	}
	body := `{"revocations":[` +
		`{"pluginId":"` + testListingUUID + `","action":"disable"},` +
		`{"pluginId":"x","manifestId":"com.x.ok","action":"disable"}` +
		`],"latestVersion":"99"}`
	srv := newCursorFeedServer(t, body)
	setSetting(t, db, revocationCursorKey, "42")
	setSetting(t, db, revocationFullAtKey, strconv.FormatInt(now.Unix(), 10))

	// Entry 2 also needs install status to resolve (PluginID is non-empty),
	// so both error; the point under test is only that the cursor holds.
	if _, err := checkerAt(db, srv.URL, now).SyncRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v, _ := settingVal(t, db, revocationCursorKey); v != "42" {
		t.Fatalf("cursor advanced to %q despite entry errors", v)
	}
}

func TestSyncRevocations_EntryErrorStillProcessesOthers(t *testing.T) {
	db := managerTestDB(t)
	now := time.Now()
	seedInstalledPlugin(t, db, "com.x.ok", "OK", "1.0.0", "none", true)
	if _, err := db.Exec(`DROP TABLE plugin_install_status`); err != nil {
		t.Fatal(err)
	}
	// Entry 2 has no pluginId, so it resolves by manifestId without the
	// dropped table.
	body := `{"revocations":[` +
		`{"pluginId":"` + testListingUUID + `","action":"disable"},` +
		`{"manifestId":"com.x.ok","action":"disable"}` +
		`],"latestVersion":"99"}`
	srv := newCursorFeedServer(t, body)
	setSetting(t, db, revocationCursorKey, "42")
	setSetting(t, db, revocationFullAtKey, strconv.FormatInt(now.Unix(), 10))
	n, err := checkerAt(db, srv.URL, now).SyncRevocations(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v, want 1/nil", n, err)
	}
	if active, _ := pluginState(t, db, "com.x.ok"); active != 0 {
		t.Fatal("second entry not disabled after first errored")
	}
	if v, _ := settingVal(t, db, revocationCursorKey); v != "42" {
		t.Fatalf("cursor = %q, want 42", v)
	}
}

func TestSyncRevocations_NoopEntriesDoNotBlockAdvance(t *testing.T) {
	db := managerTestDB(t)
	now := time.Now()
	seedInstalledPlugin(t, db, "com.x.off", "Off", "1.0.0", "none", false) // already inactive
	body := `{"revocations":[` +
		`{"pluginId":"com.x.absent","action":"disable"},` + // not installed
		`{"pluginId":"com.x.off","action":"disable"}` +
		`],"latestVersion":"99"}`
	srv := newCursorFeedServer(t, body)
	setSetting(t, db, revocationCursorKey, "42")
	setSetting(t, db, revocationFullAtKey, strconv.FormatInt(now.Unix(), 10))
	if _, err := checkerAt(db, srv.URL, now).SyncRevocations(context.Background()); err != nil {
		t.Fatal(err)
	}
	if v, _ := settingVal(t, db, revocationCursorKey); v != "99" {
		t.Fatalf("cursor = %q, want 99", v)
	}
}

// AC6
func TestSyncRevocations_FailuresLeaveStateUntouched(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"500":      func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) },
		"bad json": func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("{nope")) },
	} {
		t.Run(name, func(t *testing.T) {
			db := managerTestDB(t)
			seedInstalledPlugin(t, db, "com.x.a", "A", "1.0.0", "none", true)
			srv := httptest.NewServer(handler)
			defer srv.Close()
			if _, err := NewRevocationChecker(db, srv.URL, nil).SyncRevocations(context.Background()); err == nil {
				t.Fatal("want error")
			}
			if _, ok := settingVal(t, db, revocationCursorKey); ok {
				t.Fatal("cursor written")
			}
			if _, ok := settingVal(t, db, revocationFullAtKey); ok {
				t.Fatal("full_at written")
			}
			if active, _ := pluginState(t, db, "com.x.a"); active != 1 {
				t.Fatal("plugin disabled")
			}
		})
	}
}

// AC7
func TestSyncRevocations_OversizedBodyRejected(t *testing.T) {
	db := managerTestDB(t)
	seedInstalledPlugin(t, db, "com.x.a", "A", "1.0.0", "none", true)
	const limit = 512
	head := `{"revocations":[{"pluginId":"com.x.a","action":"disable","reason":"`
	tail := `"}],"latestVersion":"7"}`
	pad := func(total int) string {
		return head + strings.Repeat("a", total-len(head)-len(tail)) + tail
	}

	for name, body := range map[string]string{"cap+1": pad(limit + 1), "far over": pad(limit * 10)} {
		t.Run(name, func(t *testing.T) {
			srv := newCursorFeedServer(t, body)
			rc := checkerAt(db, srv.URL, time.Now())
			rc.maxBody = limit
			_, err := rc.SyncRevocations(context.Background())
			if err == nil || !strings.Contains(err.Error(), "exceeds 512 bytes") {
				t.Fatalf("err = %v", err)
			}
			if active, _ := pluginState(t, db, "com.x.a"); active != 1 {
				t.Fatal("plugin disabled from an oversized feed")
			}
			if _, ok := settingVal(t, db, revocationCursorKey); ok {
				t.Fatal("cursor written")
			}
			if _, ok := settingVal(t, db, revocationFullAtKey); ok {
				t.Fatal("full_at written")
			}
		})
	}

	// Exactly at the cap still succeeds.
	srv := newCursorFeedServer(t, pad(limit))
	rc := checkerAt(db, srv.URL, time.Now())
	rc.maxBody = limit
	if n, err := rc.SyncRevocations(context.Background()); err != nil || n != 1 {
		t.Fatalf("at cap: n=%d err=%v", n, err)
	}
}

type countingReader struct{ n int64 }

func (c *countingReader) Read(p []byte) (int, error) {
	c.n += int64(len(p))
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestReadBoundedFeed(t *testing.T) {
	// An endless body is read no further than limit+1 (plus nothing).
	cr := &countingReader{}
	if _, err := readBoundedFeed(cr, -1, 100); err == nil || !strings.Contains(err.Error(), "exceeds 100 bytes") {
		t.Fatalf("err = %v", err)
	}
	if cr.n > 101 {
		t.Fatalf("read %d bytes for a 100-byte cap", cr.n)
	}
	// Declared Content-Length over the cap is refused without reading.
	cr = &countingReader{}
	if _, err := readBoundedFeed(cr, 101, 100); err == nil || cr.n != 0 {
		t.Fatalf("err=%v read=%d", err, cr.n)
	}
	// At the cap is fine.
	b, err := readBoundedFeed(strings.NewReader(strings.Repeat("y", 100)), 100, 100)
	if err != nil || len(b) != 100 {
		t.Fatalf("len=%d err=%v", len(b), err)
	}
}
