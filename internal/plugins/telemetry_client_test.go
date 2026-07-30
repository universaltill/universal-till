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
	stmts := []string{
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, author TEXT, is_active INTEGER NOT NULL DEFAULT 1, install_state TEXT DEFAULT 'installed', runtime TEXT DEFAULT 'go', entrypoint TEXT DEFAULT '');`,
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

func TestTelemetryClient_ReportNow_SkipsWhenOptedOut(t *testing.T) {
	db := newTelemetryTestDB(t)
	setOptIn(t, db, false)
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	tc := NewTelemetryClient(db, srv.URL, "device-1", "merchant-1", "store-1")
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

	// Empty merchantID mirrors a till that hasn't finished ADR-0013 lazy
	// enrolment yet — the server requires merchant_id, so sending would
	// only produce a guaranteed-rejected request.
	tc := NewTelemetryClient(db, srv.URL, "device-1", "", "store-1")
	if err := tc.ReportNow(context.Background()); err != nil {
		t.Fatalf("ReportNow: %v", err)
	}
	if called {
		t.Fatal("expected no HTTP call before the till has a merchant_id")
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
		gotBody reportPluginStatusRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tc := NewTelemetryClient(db, srv.URL, "device-1", "merchant-1", "store-1")
	if err := tc.ReportNow(context.Background()); err != nil {
		t.Fatalf("ReportNow: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotPath != "/v1/telemetry/report" {
		t.Fatalf("path = %s", gotPath)
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

	tc := NewTelemetryClient(db, srv.URL, "device-1", "merchant-1", "store-1")
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

	tc := NewTelemetryClient(db, srv.URL, "device-1", "merchant-1", "store-1")
	if err := tc.ReportNow(context.Background()); err == nil {
		t.Fatal("expected an error when the server rejects the report")
	}
}
