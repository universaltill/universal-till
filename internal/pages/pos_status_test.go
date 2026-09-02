package pages

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
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
		`CREATE TABLE audit_log (id TEXT PRIMARY KEY, actor_id TEXT, entity_type TEXT, entity_id TEXT, action TEXT, data_json TEXT, created_at TEXT, blocked_actor_id TEXT);`,
		// vouchers/voucher_transactions: column-identical to the real
		// tables in internal/db/migrations/001_init.sql (originally added
		// by migration 068, ut-docs#1008, since folded into the baseline
		// by the ADR-0074 squash) -- the void path now always checks
		// voucher_transactions for issues to cascade to.
		`CREATE TABLE vouchers (id TEXT PRIMARY KEY, holder_label TEXT, original_amount INTEGER NOT NULL, balance INTEGER NOT NULL, currency TEXT NOT NULL DEFAULT 'EUR', voucher_type TEXT NOT NULL DEFAULT 'multi_purpose' CHECK (voucher_type IN ('multi_purpose')), status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','redeemed','void')), issued_sale_id TEXT, created_at TEXT NOT NULL DEFAULT (datetime('now')));`,
		`CREATE TABLE voucher_transactions (id TEXT PRIMARY KEY, voucher_id TEXT NOT NULL REFERENCES vouchers (id), sale_id TEXT, type TEXT NOT NULL CHECK (type IN ('issue','redemption')), amount INTEGER NOT NULL, created_at TEXT NOT NULL DEFAULT (datetime('now')));`,
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

func TestStatusEndpoint_RecordsSessionOperatorAsActor(t *testing.T) {
	db := setupStatusDB(t)
	defer db.Close()
	dp := &common.Deps{Db: db, State: common.RuntimeState{Currency: "GBP", TaxRatePct: 20}}
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)

	b, _ := json.Marshal(map[string]string{"saleId": "sale1", "status": "voided", "reason": "who did this"})
	req := httptest.NewRequest(http.MethodPost, "/api/pos/sale/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req = auth.WithUser(req, auth.User{ID: "op7", Role: "manager"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("void failed: %d %s", rec.Code, rec.Body.String())
	}
	// The void must be attributed to the logged-in operator, not recorded
	// actor-less (batch 8 review: pos_api passed "" as actorID).
	var actor string
	if err := db.QueryRow(`SELECT COALESCE(actor_id,'') FROM audit_log WHERE entity_id='sale1' AND action='voided'`).Scan(&actor); err != nil {
		t.Fatalf("read audit actor: %v", err)
	}
	if actor != "op7" {
		t.Fatalf("audit actor_id = %q, want op7", actor)
	}
}

func TestStatusEndpoint_UnknownSaleFailsAndWritesNoAudit(t *testing.T) {
	db := setupStatusDB(t)
	defer db.Close()
	dp := &common.Deps{Db: db, State: common.RuntimeState{Currency: "GBP", TaxRatePct: 20}}
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)

	b, _ := json.Marshal(map[string]string{"saleId": "ghost", "status": "voided"})
	req := httptest.NewRequest(http.MethodPost, "/api/pos/sale/status", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("voiding a nonexistent sale returned %d, want 404", rec.Code)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_id='ghost'`).Scan(&count); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("phantom void left %d audit row(s), want 0", count)
	}
}
