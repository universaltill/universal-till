package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func newTelemetryTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Every caller leaked this DB (and its background connectionOpener
	// goroutine) forever — a real, if minor, contributor to the "surviving
	// goroutine" noise in a go test -race timeout dump (ut-docs#2156).
	t.Cleanup(func() { db.Close() })
	stmts := []string{
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, author TEXT, is_active INTEGER NOT NULL DEFAULT 1, install_state TEXT DEFAULT 'installed', runtime TEXT DEFAULT 'go', entrypoint TEXT DEFAULT '', installed_sha256 TEXT);`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at DATETIME);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup schema: %v", err)
		}
	}
	return db
}

func setOptIn(t *testing.T, db *sql.DB, enabled bool) {
	t.Helper()
	val := "false"
	if enabled {
		val = "true"
	}
	if _, err := db.Exec(`INSERT INTO settings(key, value) VALUES('marketplace.telemetry_opt_in', ?)`, val); err != nil {
		t.Fatalf("seed opt-in setting: %v", err)
	}
}

// fixedIdentity is a TelemetryIdentity source that never changes.
func fixedIdentity(url, deviceID, merchantID, storeID, token string) func() TelemetryIdentity {
	return func() TelemetryIdentity {
		return TelemetryIdentity{EndpointURL: url, DeviceID: deviceID, MerchantID: merchantID, StoreID: storeID, Token: token}
	}
}

func TestTelemetryClient_ReportNow_SkipsWhenOptedOut(t *testing.T) {
	db := newTelemetryTestDB(t)
	setOptIn(t, db, false)
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	tc := NewTelemetryClient(db, fixedIdentity(srv.URL, "device-1", "merchant-1", "store-1", "tok-1"))
	if err := tc.ReportNow(context.Background()); err != nil {
		t.Fatalf("ReportNow: %v", err)
	}
	if called {
		t.Fatal("expected no HTTP call when telemetry is opted out")
	}
}

func TestTelemetryClient_ReportNow_SkipsWhenNotYetEnrolled(t *testing.T) {
	db := newTelemetryTestDB(t)
	setOptIn(t, db, true)
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('com.example.faq','FAQ','1.0.0',1)`); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	// A till that hasn't finished ADR-0013 lazy enrolment has no store id
	// or no device credential yet; /telemetry/report requires both
	// (ut-docs#3547), so sending would only produce a guaranteed refusal.
	for name, id := range map[string]func() TelemetryIdentity{
		"no store id":   fixedIdentity(srv.URL, "device-1", "merchant-1", "", "tok-1"),
		"no credential": fixedIdentity(srv.URL, "device-1", "merchant-1", "store-1", ""),
		"no endpoint":   fixedIdentity("", "device-1", "merchant-1", "store-1", "tok-1"),
	} {
		if err := NewTelemetryClient(db, id).ReportNow(context.Background()); err != nil {
			t.Fatalf("%s: ReportNow: %v", name, err)
		}
		if called {
			t.Fatalf("%s: expected no HTTP call before the till is enrolled", name)
		}
	}
}

func TestTelemetryClient_ReportNow_SendsActiveInstalledPlugins(t *testing.T) {
	db := newTelemetryTestDB(t)
	setOptIn(t, db, true)
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('com.example.faq','FAQ','1.0.0',1)`); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('com.example.disabled','Disabled','1.0.0',0)`); err != nil {
		t.Fatalf("seed disabled plugin: %v", err)
	}

	var (
		mu      sync.Mutex
		gotPath string
		gotAuth string
		gotBody reportPluginStatusRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tc := NewTelemetryClient(db, fixedIdentity(srv.URL, "device-1", "merchant-1", "store-1", "tok-1"))
	if err := tc.ReportNow(context.Background()); err != nil {
		t.Fatalf("ReportNow: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/telemetry/report" {
		t.Fatalf("path = %s", gotPath)
	}
	if gotAuth != "Bearer tok-1" {
		t.Fatalf("Authorization = %q, want the ADR-0116 device credential as a bearer", gotAuth)
	}
	if gotBody.MerchantID != "merchant-1" || gotBody.StoreID != "store-1" {
		t.Fatalf("unexpected merchant/store: %+v", gotBody)
	}
	if len(gotBody.Statuses) != 1 {
		t.Fatalf("expected only the active plugin reported, got %d statuses: %+v", len(gotBody.Statuses), gotBody.Statuses)
	}
	got := gotBody.Statuses[0]
	if got.PluginID != "com.example.faq" || got.InstalledVersion != "1.0.0" || got.Status != "enabled" || got.DeviceID != "device-1" {
		t.Fatalf("unexpected status entry: %+v", got)
	}
}

func TestTelemetryClient_ReportNow_NoPluginsSendsNothing(t *testing.T) {
	db := newTelemetryTestDB(t)
	setOptIn(t, db, true)
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	tc := NewTelemetryClient(db, fixedIdentity(srv.URL, "device-1", "merchant-1", "store-1", "tok-1"))
	if err := tc.ReportNow(context.Background()); err != nil {
		t.Fatalf("ReportNow: %v", err)
	}
	if called {
		t.Fatal("expected no HTTP call with zero installed plugins")
	}
}

func TestTelemetryClient_ReportNow_SurfacesServerErrors(t *testing.T) {
	db := newTelemetryTestDB(t)
	setOptIn(t, db, true)
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('com.example.faq','FAQ','1.0.0',1)`); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tc := NewTelemetryClient(db, fixedIdentity(srv.URL, "device-1", "merchant-1", "store-1", "tok-1"))
	if err := tc.ReportNow(context.Background()); err == nil {
		t.Fatal("expected an error when the server rejects the report")
	}
}

// A refused credential (401) is one more failed report: surfaced as an error
// for the scheduler to log, sent once, never retried within the tick.
func TestTelemetryClient_ReportNow_SurfacesRefusedCredential(t *testing.T) {
	db := newTelemetryTestDB(t)
	setOptIn(t, db, true)
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('com.example.faq','FAQ','1.0.0',1)`); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	tc := NewTelemetryClient(db, fixedIdentity(srv.URL, "device-1", "merchant-1", "store-1", "stale"))
	if err := tc.ReportNow(context.Background()); err == nil {
		t.Fatal("expected an error when the server refuses the credential")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("server called %d times, want exactly 1 (no retry)", calls)
	}
}

// The identity is read per tick, so a till that enrols or pairs after boot
// reports with its new store and credential without a restart.
func TestTelemetryClient_ReportNow_ReadsLiveIdentityEachTick(t *testing.T) {
	db := newTelemetryTestDB(t)
	setOptIn(t, db, true)
	if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES('com.example.faq','FAQ','1.0.0',1)`); err != nil {
		t.Fatalf("seed plugin: %v", err)
	}
	var (
		mu    sync.Mutex
		auths []string
		store []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body reportPluginStatusRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		auths = append(auths, r.Header.Get("Authorization"))
		store = append(store, body.StoreID)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	id := TelemetryIdentity{EndpointURL: srv.URL, DeviceID: "device-1"}
	tc := NewTelemetryClient(db, func() TelemetryIdentity { return id })
	if err := tc.ReportNow(context.Background()); err != nil { // not enrolled yet: skipped
		t.Fatalf("ReportNow before enrolment: %v", err)
	}
	id.StoreID, id.Token = "store-new", "tok-new"
	if err := tc.ReportNow(context.Background()); err != nil {
		t.Fatalf("ReportNow after enrolment: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(auths) != 1 || auths[0] != "Bearer tok-new" || store[0] != "store-new" {
		t.Fatalf("got auth=%v store=%v, want one report as store-new with Bearer tok-new", auths, store)
	}
}
