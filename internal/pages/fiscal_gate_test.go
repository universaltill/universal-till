package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// newFiscalTestDeps is newPOSTestDeps plus the fiscal override endpoint and
// an AuthSvc (needed for the PIN-approval path), with real role gating
// (UT_AUTH forced non-off so canPerform checks the session user for real).
func newFiscalTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	t.Setenv("UT_AUTH", "")
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100, ItemID: "itm1", TaxRateBP: 2000},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "EUR", TaxRate: 19}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	setStore := settings.NewStore(db)
	state := common.LoadState(t.Context(), setStore, cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Engine:   engine,
		Pm:       pm,
		Settings: setStore,
		AuthSvc:  auth.NewService(db),
	}
	t.Cleanup(dp.WaitForAsyncWork)
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	registerFiscalAPI(mux, dp)
	return mux, dp
}

// makeGermanSystemOfRecord flips the shop into the gated posture.
func makeGermanSystemOfRecord(t *testing.T, dp *common.Deps) {
	t.Helper()
	ctx := context.Background()
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	if err := dp.Settings.Set(ctx, fiscal.KeySystemOfRecord, "true"); err != nil {
		t.Fatal(err)
	}
}

func fiscalTender(t *testing.T, mux *http.ServeMux) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func countSales(t *testing.T, dp *common.Deps) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// German shop, system of record, no TSE configured: a real sale must not
// complete — localized refusal, basket kept, no sale row.
func TestFiscalGate_GermanShopWithoutTSECannotTender(t *testing.T) {
	mux, dp := newFiscalTestDeps(t)
	makeGermanSystemOfRecord(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}

	rec := fiscalTender(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place notice, not an error status), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "pos-notice error") {
		t.Fatalf("expected an error-level pos-notice, got: %s", rec.Body.String())
	}
	// Localized en copy, not a raw Go error string.
	if !strings.Contains(rec.Body.String(), "TSE") {
		t.Fatalf("expected the refusal to mention the TSE, got: %s", rec.Body.String())
	}
	if got := countSales(t, dp); got != 0 {
		t.Fatalf("expected no sale row, got %d", got)
	}
	if len(dp.Engine.Basket().Lines) == 0 {
		t.Fatal("expected the basket to survive the refusal")
	}
}

// Same shop in shadow/demo mode (system_of_record=false): sales complete
// normally regardless of TSE state.
func TestFiscalGate_ShadowModeCompletesNormally(t *testing.T) {
	mux, dp := newFiscalTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) { s.Country = "DE" })
	// No fiscal keys at all — a brand-new German trial shop.
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := fiscalTender(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countSales(t, dp); got != 1 {
		t.Fatalf("expected 1 sale, got %d", got)
	}
}

// Non-German shop is completely unaffected, even with every fiscal key set —
// the regression pin for existing tender flows.
func TestFiscalGate_NonGermanShopUnaffected(t *testing.T) {
	mux, dp := newFiscalTestDeps(t)
	ctx := context.Background()
	// Country stays the GB default. Set every fiscal key to the most
	// blocking combination — none of it may matter outside a gated market.
	for k, v := range map[string]string{
		fiscal.KeySystemOfRecord:  "true",
		fiscal.KeyTSEConfigured:   "false",
		fiscal.KeyTSEFailingSince: "2026-08-14T09:00:00Z",
	} {
		if err := dp.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := fiscalTender(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countSales(t, dp); got != 1 {
		t.Fatalf("expected 1 sale for a non-gated shop, got %d", got)
	}
}

// TSE configured but failing, no override: blocked with its own message.
func TestFiscalGate_FailingTSEBlockedWithoutOverride(t *testing.T) {
	mux, dp := newFiscalTestDeps(t)
	makeGermanSystemOfRecord(t, dp)
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, fiscal.KeyTSEConfigured, "true"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, fiscal.KeyTSEFailingSince, "2026-08-14T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := fiscalTender(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (in-place notice), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "pos-notice error") {
		t.Fatalf("expected an error-level pos-notice, got: %s", rec.Body.String())
	}
	if got := countSales(t, dp); got != 0 {
		t.Fatalf("expected no sale row, got %d", got)
	}
}

// The refusal copy must come from the locale files, not a Go literal —
// request Farsi and assert fa.json's translation renders.
func TestFiscalGate_RefusalIsTranslated(t *testing.T) {
	mux, dp := newFiscalTestDeps(t)
	makeGermanSystemOfRecord(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(filepath.Join("web", "locales", "fa.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fa map[string]string
	if err := json.Unmarshal(raw, &fa); err != nil {
		t.Fatal(err)
	}
	want := fa["pos.toast.fiscal_never_configured"]
	if want == "" {
		t.Fatal("fa.json is missing pos.toast.fiscal_never_configured")
	}

	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender?lang=fa",
		strings.NewReader(`{"payments":[{"method":"cash","amount":120}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("expected fa translation %q in refusal, got: %s", want, rec.Body.String())
	}
}

// grantOverride posts a fully-valid override request as the given user.
func grantOverride(t *testing.T, mux *http.ServeMux, u auth.User, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/fiscal/tse-override", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if u.ID != "" {
		req = auth.WithUser(req, u)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func validOverrideBody(extra string) string {
	return `{"reason":"provider outage, queue at the counter","acknowledgement":"` +
		fiscal.OverrideAcknowledgement + `","duration_minutes":60` + extra + `}`
}

// seedFailingConfiguredTSE puts the shop in the configured-but-failing state
// the override exists for.
func seedFailingConfiguredTSE(t *testing.T, dp *common.Deps) {
	t.Helper()
	ctx := context.Background()
	makeGermanSystemOfRecord(t, dp)
	if err := dp.Settings.Set(ctx, fiscal.KeyTSEConfigured, "true"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, fiscal.KeyTSEFailingSince, "2026-08-14T09:00:00Z"); err != nil {
		t.Fatal(err)
	}
}

// Admin session grants the override; the blocked sale then completes; both
// the one-time grant audit entry and the per-sale unsigned_override marker
// are written with actor/reason/window.
func TestFiscalOverride_AdminGrantUnblocksAndAudits(t *testing.T) {
	mux, dp := newFiscalTestDeps(t)
	seedFailingConfiguredTSE(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}

	// Blocked before the grant.
	if rec := fiscalTender(t, mux); countSales(t, dp) != 0 {
		t.Fatalf("expected the pre-override tender to be refused, body: %s", rec.Body.String())
	}

	rec := grantOverride(t, mux, auth.User{ID: "user1", Role: "admin"}, validOverrideBody(""))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for an admin grant, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Granted bool   `json:"granted"`
			Until   string `json:"until"`
			Actor   string `json:"actor"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad JSON: %v — %s", err, rec.Body.String())
	}
	if out.Error != nil || !out.Data.Granted || out.Data.Actor != "user1" {
		t.Fatalf("unexpected grant response: %s", rec.Body.String())
	}
	until, err := time.Parse(time.RFC3339, out.Data.Until)
	if err != nil {
		t.Fatalf("until is not RFC3339: %q", out.Data.Until)
	}
	if remaining := time.Until(until); remaining <= 55*time.Minute || remaining > 61*time.Minute {
		t.Fatalf("until %v not ~60 minutes out", out.Data.Until)
	}

	// The one-time grant audit entry.
	var grantPayload string
	if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE entity_type='fiscal_override' AND action='grant' AND actor_id='user1'`).
		Scan(&grantPayload); err != nil {
		t.Fatalf("expected a fiscal_override/grant audit row: %v", err)
	}
	for _, want := range []string{"provider outage", "until", "granted_at", "duration_minutes"} {
		if !strings.Contains(grantPayload, want) {
			t.Fatalf("grant audit payload missing %q: %s", want, grantPayload)
		}
	}

	// The same sale now completes...
	rec2 := fiscalTender(t, mux)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if got := countSales(t, dp); got != 1 {
		t.Fatalf("expected 1 sale under an active override, got %d", got)
	}
	// ...and carries its own per-sale unsigned_override marker (distinct
	// from the grant entry).
	var saleID, salePayload string
	if err := dp.Db.QueryRow(`SELECT entity_id, data_json FROM audit_log WHERE entity_type='sale' AND action='unsigned_override'`).
		Scan(&saleID, &salePayload); err != nil {
		t.Fatalf("expected a sale/unsigned_override audit row: %v", err)
	}
	var realSaleID string
	if err := dp.Db.QueryRow(`SELECT id FROM sales`).Scan(&realSaleID); err != nil {
		t.Fatal(err)
	}
	if saleID != realSaleID {
		t.Fatalf("unsigned_override marker not attached to the sale: %q != %q", saleID, realSaleID)
	}
	if !strings.Contains(salePayload, "user1") || !strings.Contains(salePayload, "provider outage") {
		t.Fatalf("per-sale marker missing actor/reason: %s", salePayload)
	}
}

// A cashier can request the override with an admin's PIN (the admin becomes
// the audit actor); a manager's PIN — valid, wrong role — must be refused.
func TestFiscalOverride_PINPaths(t *testing.T) {
	mux, dp := newFiscalTestDeps(t)
	seedFailingConfiguredTSE(t, dp)
	ctx := context.Background()

	adminHash, err := auth.HashPIN("135790")
	if err != nil {
		t.Fatal(err)
	}
	mgrHash, err := auth.HashPIN("246801")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO users(id,username,display_name,pin_hash,role,created_at) VALUES
		('cash1','cash1','Cashier','', 'cashier', datetime('now')),
		('adm2','adm2','Owner',?, 'admin', datetime('now')),
		('mgr2','mgr2','Manager',?, 'manager', datetime('now'))`, adminHash, mgrHash); err != nil {
		t.Fatal(err)
	}

	cashier := auth.User{ID: "cash1", Role: "cashier"}

	// No PIN at all: forbidden.
	rec := grantOverride(t, mux, cashier, validOverrideBody(""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 with no PIN, got %d: %s", rec.Code, rec.Body.String())
	}

	// Manager PIN: authenticates, but the role isn't owner/admin — refused.
	rec = grantOverride(t, mux, cashier, validOverrideBody(`,"owner_pin":"246801"`))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a manager PIN, got %d: %s", rec.Code, rec.Body.String())
	}

	// Admin PIN: granted, and the ADMIN (not the requesting cashier) is the
	// audit actor, with the cashier recorded as requested_by.
	rec = grantOverride(t, mux, cashier, validOverrideBody(`,"owner_pin":"135790"`))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for an admin PIN, got %d: %s", rec.Code, rec.Body.String())
	}
	var actorID, payload string
	if err := dp.Db.QueryRow(`SELECT actor_id, data_json FROM audit_log WHERE entity_type='fiscal_override' AND action='grant'`).
		Scan(&actorID, &payload); err != nil {
		t.Fatal(err)
	}
	if actorID != "adm2" {
		t.Fatalf("audit actor = %q, want the authorizing admin adm2", actorID)
	}
	if !strings.Contains(payload, `"requested_by":"cash1"`) {
		t.Fatalf("expected requested_by=cash1 in payload: %s", payload)
	}
}

// The never-configured state cannot reach the override by ANY path — a
// direct API call with a fully-valid body, an admin session AND a valid
// admin PIN is still refused before anything else is even considered.
func TestFiscalOverride_UnreachableWhenNeverConfigured(t *testing.T) {
	mux, dp := newFiscalTestDeps(t)
	makeGermanSystemOfRecord(t, dp)
	// fiscal.tse_configured deliberately absent/false.
	adminHash, err := auth.HashPIN("135790")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(context.Background(),
		`INSERT INTO users(id,username,display_name,pin_hash,role,created_at) VALUES ('adm2','adm2','Owner',?, 'admin', datetime('now'))`,
		adminHash); err != nil {
		t.Fatal(err)
	}

	rec := grantOverride(t, mux, auth.User{ID: "adm2", Role: "admin"}, validOverrideBody(`,"owner_pin":"135790"`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for never-configured, got %d: %s", rec.Code, rec.Body.String())
	}
	// Nothing stored, nothing audited.
	if v, ok, _ := dp.Settings.Get(context.Background(), fiscal.KeyOverrideUntil); ok && v != "" {
		t.Fatalf("no override window may be stored from the never-configured state, got %q", v)
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='fiscal_override'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected no fiscal_override audit rows, got %d", n)
	}
}

func TestFiscalOverride_ValidationRejects(t *testing.T) {
	mux, dp := newFiscalTestDeps(t)
	seedFailingConfiguredTSE(t, dp)
	admin := auth.User{ID: "user1", Role: "admin"}

	cases := []struct {
		name string
		body string
	}{
		{"missing reason", `{"reason":"","acknowledgement":"` + fiscal.OverrideAcknowledgement + `","duration_minutes":60}`},
		{"wrong acknowledgement", `{"reason":"outage","acknowledgement":"yes yes fine","duration_minutes":60}`},
		{"zero duration", validOverrideBodyWithDuration(0)},
		{"negative duration", validOverrideBodyWithDuration(-5)},
		{"above the 8h cap (rejected, not clamped)", validOverrideBodyWithDuration(481)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := grantOverride(t, mux, admin, c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	if v, ok, _ := dp.Settings.Get(context.Background(), fiscal.KeyOverrideUntil); ok && v != "" {
		t.Fatalf("no override may be stored by a rejected request, got %q", v)
	}
}

func validOverrideBodyWithDuration(minutes int) string {
	b, _ := json.Marshal(map[string]any{
		"reason":           "outage",
		"acknowledgement":  fiscal.OverrideAcknowledgement,
		"duration_minutes": minutes,
	})
	return string(b)
}

// Expired override: blocking resumes on the very next attempt, no job runs.
func TestFiscalOverride_ExpiryBlocksNextSale(t *testing.T) {
	mux, dp := newFiscalTestDeps(t)
	seedFailingConfiguredTSE(t, dp)
	ctx := context.Background()
	// A window that expired a second ago.
	if err := dp.Settings.Set(ctx, fiscal.KeyOverrideUntil, time.Now().Add(-time.Second).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, fiscal.KeyOverrideReason, "expired earlier"); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := fiscalTender(t, mux)
	if rec.Code != http.StatusOK || countSales(t, dp) != 0 {
		t.Fatalf("expected the tender after expiry to be refused with no sale, code=%d sales=%d body=%s",
			rec.Code, countSales(t, dp), rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "pos-notice error") {
		t.Fatalf("expected an error-level pos-notice, got: %s", rec.Body.String())
	}
}

// The generic settings upsert must not be a side door: the reserved override
// window key cannot be written non-empty there by anyone, and the fiscal
// toggles are owner(admin)-gated and audit-logged.
func TestFiscalSettings_UpsertGuards(t *testing.T) {
	dp2mux := func(t *testing.T) (*http.ServeMux, *common.Deps) {
		mux, dp := newFiscalTestDeps(t)
		registerSettings(mux, dp)
		return mux, dp
	}
	post := func(mux *http.ServeMux, u auth.User, key, value string) *httptest.ResponseRecorder {
		form := "key=" + key + "&value=" + value
		req := httptest.NewRequest(http.MethodPost, "/api/settings/upsert", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, u)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	admin := auth.User{ID: "user1", Role: "admin"}
	manager := auth.User{ID: "mgr9", Role: "manager"}

	t.Run("override window unwritable even by admin", func(t *testing.T) {
		mux, dp := dp2mux(t)
		rec := post(mux, admin, fiscal.KeyOverrideUntil, "2030-01-01T00:00:00Z")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 fabricating an override via upsert, got %d: %s", rec.Code, rec.Body.String())
		}
		if v, ok, _ := dp.Settings.Get(context.Background(), fiscal.KeyOverrideUntil); ok && v != "" {
			t.Fatalf("override window must not be stored, got %q", v)
		}
	})

	t.Run("manager cannot flip fiscal toggles", func(t *testing.T) {
		mux, dp := dp2mux(t)
		if _, err := dp.Db.Exec(`INSERT INTO users(id,username,pin_hash,role,created_at) VALUES('mgr9','mgr9','','manager',datetime('now'))`); err != nil {
			t.Fatal(err)
		}
		rec := post(mux, manager, fiscal.KeySystemOfRecord, "true")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("expected 403 for a manager, got %d: %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("admin flip is stored and audited", func(t *testing.T) {
		mux, dp := dp2mux(t)
		rec := post(mux, admin, fiscal.KeySystemOfRecord, "true")
		if rec.Code != http.StatusNoContent {
			t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
		}
		if v, _, _ := dp.Settings.Get(context.Background(), fiscal.KeySystemOfRecord); v != "true" {
			t.Fatalf("expected the toggle stored, got %q", v)
		}
		var payload string
		if err := dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE entity_type='fiscal_settings' AND action='system_of_record_changed' AND actor_id='user1'`).
			Scan(&payload); err != nil {
			t.Fatalf("expected a fiscal_settings audit row: %v", err)
		}
		if !strings.Contains(payload, `"to":"true"`) {
			t.Fatalf("expected the transition recorded, got %s", payload)
		}
	})

	t.Run("tse_failing_since is not settable via this endpoint, even by admin", func(t *testing.T) {
		// ADR-0048 Decision 1: this key is "not operator-settable in this
		// card" — no UI control at all, set or clear. Written only by
		// tests directly, or a future fiscal.sign.ask failure callback
		// (#675). An admin trying to set OR clear it through the generic
		// editor must be refused, not silently accepted.
		mux, dp := dp2mux(t)
		rec := post(mux, admin, fiscal.KeyTSEFailingSince, "2026-08-14T09:00:00Z")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 setting tse_failing_since via upsert, got %d: %s", rec.Code, rec.Body.String())
		}
		if v, ok, _ := dp.Settings.Get(context.Background(), fiscal.KeyTSEFailingSince); ok && v != "" {
			t.Fatalf("tse_failing_since must not be stored via upsert, got %q", v)
		}

		// Also refused when the key already has a (test-seeded) value and
		// the request tries to clear it.
		if err := dp.Settings.Set(context.Background(), fiscal.KeyTSEFailingSince, "2026-08-14T09:00:00Z"); err != nil {
			t.Fatal(err)
		}
		rec = post(mux, admin, fiscal.KeyTSEFailingSince, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 clearing tse_failing_since via upsert, got %d: %s", rec.Code, rec.Body.String())
		}
		if v, _, _ := dp.Settings.Get(context.Background(), fiscal.KeyTSEFailingSince); v != "2026-08-14T09:00:00Z" {
			t.Fatalf("tse_failing_since must be untouched by the refused clear, got %q", v)
		}
	})
}
