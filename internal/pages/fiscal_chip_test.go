package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/fiscal"
)

// TSE never configured: the chip must render nothing at all (ut-docs#685) —
// no scary red state for a shop that will never have fiscalisation.
func TestFiscalChip_NotConfigured_RendersNothing(t *testing.T) {
	mux, _ := newFiscalTestDeps(t)
	initPagesI18n(t)

	req := httptest.NewRequest(http.MethodGet, "/ui/fiscal-chip", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected an empty chip when fiscalisation isn't configured, got %q", rec.Body.String())
	}
}

// Configured, no sales yet: healthy — nothing to complain about.
func TestFiscalChip_ConfiguredNoSales_RendersOK(t *testing.T) {
	mux, dp := newFiscalTestDeps(t)
	initPagesI18n(t)
	if err := dp.Settings.Set(context.Background(), fiscal.KeyTSEConfigured, "true"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/ui/fiscal-chip", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fiscal-chip ok") {
		t.Fatalf("expected class=ok with no sales yet, got %q", rec.Body.String())
	}
}

// Configured, most recent sale carries an unresolved gap marker: degraded,
// with the in-business-day unresolved count shown.
func TestFiscalChip_ConfiguredLastSaleGapped_RendersWarnWithCount(t *testing.T) {
	mux, dp := newFiscalTestDeps(t)
	initPagesI18n(t)
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, fiscal.KeyTSEConfigured, "true"); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	if rec := fiscalTender(t, mux); rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	var saleID string
	if err := dp.Db.QueryRow(`SELECT id FROM sales`).Scan(&saleID); err != nil {
		t.Fatal(err)
	}
	// RFC3339, matching exactly what declareUnsignedFiscalSale actually
	// writes (fiscal_sign_hook.go) — SQLite's own datetime('now') uses a
	// different string shape ("YYYY-MM-DD HH:MM:SS") that would sort
	// BEFORE an RFC3339 "since" bound for the same instant and silently
	// fall outside the chip's business-day window.
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := dp.Db.ExecContext(ctx,
		`INSERT INTO audit_log(id, actor_id, entity_type, entity_id, action, data_json, created_at) VALUES ('a1', NULL, 'sale', ?, 'unsigned_fiscal_signing', '{}', ?)`,
		saleID, now); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/ui/fiscal-chip", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "fiscal-chip warn") {
		t.Fatalf("expected class=warn for a gapped last sale, got %q", body)
	}
	if !strings.Contains(body, strconv.Itoa(1)) {
		t.Fatalf("expected the unresolved count (1) rendered, got %q", body)
	}
}
