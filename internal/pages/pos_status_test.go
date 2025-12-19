package pages

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	_ "modernc.org/sqlite"
)

func setupStatusDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE sales (id TEXT PRIMARY KEY, status TEXT NOT NULL, voided_at TEXT, tender_type TEXT NOT NULL DEFAULT 'unknown', offline INTEGER NOT NULL DEFAULT 0, sync_status TEXT NOT NULL DEFAULT 'queued', sync_attempts INTEGER NOT NULL DEFAULT 0, sync_next_attempt_at TEXT, sync_last_error TEXT);`,
		`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT, entity_id TEXT, action TEXT, data_json TEXT, created_at TEXT);`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup stmt failed: %v", err)
		}
	}
	_, _ = db.Exec(`INSERT INTO sales(id, status) VALUES('sale1','open')`)
	return db
}

func TestStatusEndpoint_ParkAndVoid(t *testing.T) {
	db := setupStatusDB(t)
	defer db.Close()
	dp := &common.Deps{Db: db, State: common.RuntimeState{Currency: "GBP", TaxRatePct: 20}}
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)

	body := map[string]string{"saleId": "sale1", "status": "parked"}
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/pos/sale/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("park status failed: %d %s", rec.Code, rec.Body.String())
	}
	var status string
	_ = db.QueryRow(`SELECT status FROM sales WHERE id='sale1'`).Scan(&status)
	if status != "parked" {
		t.Fatalf("expected parked, got %s", status)
	}
	// void
	body["status"] = "voided"
	b, _ = json.Marshal(body)
	req = httptest.NewRequest(http.MethodPost, "/api/pos/sale/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("void status failed: %d %s", rec.Code, rec.Body.String())
	}
	_ = db.QueryRow(`SELECT status FROM sales WHERE id='sale1'`).Scan(&status)
	if status != "voided" {
		t.Fatalf("expected voided, got %s", status)
	}
	// audit row should exist
	var count int
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_id='sale1' AND action='voided'`).Scan(&count)
	if count == 0 {
		t.Fatalf("expected audit log for void")
	}
}
