package pages

import (
	"database/sql"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#796 (slice 1): the 8 mutating settings_page.go handlers moved off
// the flat `if !canPerform(d, r, "settings")` gate onto checkOrElevate —
// same pattern (and test shape) as backup_api_test.go's
// TestBackupNow_ElevatesOnValidApproverPIN. These tests cover the three
// elevation outcomes for the highest-value sites (remove-demo-catalogue,
// upsert, till-register — with an audit_log dual-attribution assertion on
// the elevated path) plus lighter deny+elevate coverage for the other five.

// seedElevationUsers creates a real manager (PIN 555222) and a real cashier
// row in deps' DB, returning (managerID, cashierID). Works against both
// newFullAuthDeps' hand-rolled schema and newRealDBDeps' migrated one.
func seedElevationUsers(t *testing.T, d *common.Deps) (string, string) {
	t.Helper()
	authRepo := data.NewAuthRepo(d.Db)
	ctx := t.Context()
	mgrID, err := authRepo.CreateUser(ctx, "mgr-elev", "Elevation Manager", "manager")
	if err != nil {
		t.Fatalf("create manager: %v", err)
	}
	hash, err := auth.HashPIN("555222")
	if err != nil {
		t.Fatalf("hash pin: %v", err)
	}
	if err := authRepo.SetUserPIN(ctx, mgrID, hash); err != nil {
		t.Fatalf("set pin: %v", err)
	}
	// blocked_actor_id carries a real FK to users(id) on a migrated DB —
	// the blocked session user must exist as a real row, same as the
	// approver (mirrors backup_api_test.go).
	cashierID, err := authRepo.CreateUser(ctx, "cashier-elev", "Blocked Cashier", "cashier")
	if err != nil {
		t.Fatalf("create cashier: %v", err)
	}
	return mgrID, cashierID
}

// assertElevationPrompt fails unless rec is a 200 whose body carries the
// elevation dialog and its PIN field.
func assertElevationPrompt(t *testing.T, label string, code int, body string) {
	t.Helper()
	if code != http.StatusOK || !strings.Contains(body, "elevation-dialog") || !strings.Contains(body, `name="override_pin"`) {
		t.Fatalf("%s: code=%d body=%s, want 200 with the elevation prompt", label, code, body)
	}
}

// assertElevatedAudit fails unless exactly the expected dual attribution
// landed on the (single) audit_log row matching action.
func assertElevatedAudit(t *testing.T, d *common.Deps, action, wantActor, wantBlocked string) {
	t.Helper()
	var actorID string
	// blocked_actor_id is nullable (plain InsertAudit leaves it NULL) — a
	// bare string Scan on a NULL row fails with a driver conversion error
	// ("converting NULL to string is unsupported"), which misdiagnosed a
	// genuine dual-attribution bug as "no row found" (review finding #6).
	// sql.NullString lets a real missing-attribution case fail on the
	// wantBlocked comparison below instead, with the actual value on show.
	var blockedActorID sql.NullString
	if err := d.Db.QueryRow(`SELECT actor_id, blocked_actor_id FROM audit_log WHERE action=?`, action).
		Scan(&actorID, &blockedActorID); err != nil {
		t.Fatalf("expected a %s audit row: %v", action, err)
	}
	if actorID != wantActor {
		t.Fatalf("%s audit actor_id = %q, want the approver %q", action, actorID, wantActor)
	}
	if blockedActorID.String != wantBlocked || (!blockedActorID.Valid && wantBlocked != "") {
		t.Fatalf("%s audit blocked_actor_id = %q (valid=%v), want the blocked session user %q", action, blockedActorID.String, blockedActorID.Valid, wantBlocked)
	}
}

// The generic key/value upsert: no PIN → prompt; wrong PIN → re-prompt with
// an inline error; a valid approver PIN → the write lands attributed to the
// approver with the blocked cashier alongside (dual attribution).
func TestSettingsUpsert_ElevationFlow(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	mgrID, cashierID := seedElevationUsers(t, d)
	cashier := auth.User{ID: cashierID, Role: "cashier"}

	// Deny, no PIN: prompt, no error line, nothing written.
	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.tax_inclusive"}, "value": {"true"}}, &cashier)
	assertElevationPrompt(t, "no pin", rec.Code, rec.Body.String())
	if strings.Contains(rec.Body.String(), "login-error") {
		t.Fatalf("first-time prompt must carry no error line: %s", rec.Body.String())
	}
	if _, ok, _ := d.Settings.Get(t.Context(), "store.tax_inclusive"); ok {
		t.Fatal("denied upsert must not have written the setting")
	}

	// Deny, wrong PIN: re-prompt WITH the inline error, still nothing written.
	rec = postForm(mux, "/api/settings/upsert",
		url.Values{"key": {"store.tax_inclusive"}, "value": {"true"}, "override_pin": {"000000"}}, &cashier)
	assertElevationPrompt(t, "wrong pin", rec.Code, rec.Body.String())
	if !strings.Contains(rec.Body.String(), "login-error") {
		t.Fatalf("failed PIN attempt must render the inline error: %s", rec.Body.String())
	}
	if _, ok, _ := d.Settings.Get(t.Context(), "store.tax_inclusive"); ok {
		t.Fatal("wrong-PIN upsert must not have written the setting")
	}

	// Valid approver PIN: elevated — write lands, confirmation body (not a
	// bare 204: the dialog retry needs something to swap), dual-attributed
	// audit row.
	rec = postForm(mux, "/api/settings/upsert",
		url.Values{"key": {"store.tax_inclusive"}, "value": {"true"}, "override_pin": {"555222"}}, &cashier)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("elevated upsert = %d body=%s, want 200 with a confirmation", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), "store.tax_inclusive"); v != "true" {
		t.Fatalf("elevated upsert did not write the setting, got %q", v)
	}
	if !d.CurrentState().TaxInclusive {
		t.Fatal("elevated upsert did not reflect into runtime state")
	}
	assertElevatedAudit(t, d, "setting_upserted", mgrID, cashierID)
}

// Till-register: validation still rejects garbage BEFORE any PIN is asked
// for; a valid approver PIN gets the identity written and dual-audited.
func TestSettingsTillRegister_ElevationFlow(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if _, err := d.Db.Exec(`INSERT INTO registers(id,name,is_active) VALUES('regA','Front Till',1)`); err != nil {
		t.Fatal(err)
	}
	mgrID, cashierID := seedElevationUsers(t, d)
	cashier := auth.User{ID: cashierID, Role: "cashier"}

	// A cashier posting an UNKNOWN register gets the 400 (validation runs
	// before elevation, ut-docs#557 convention) — never a PIN prompt for a
	// request that would be rejected anyway.
	rec := postForm(mux, "/api/settings/till-register", url.Values{"register_id": {"nope"}}, &cashier)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown register as cashier = %d, want 400 before any elevation", rec.Code)
	}

	// Deny, no PIN.
	rec = postForm(mux, "/api/settings/till-register", url.Values{"register_id": {"regA"}}, &cashier)
	assertElevationPrompt(t, "no pin", rec.Code, rec.Body.String())
	if _, ok, _ := d.Settings.Get(t.Context(), pos.SettingsKeyTillRegisterID); ok {
		t.Fatal("denied till-register must not have written the identity")
	}

	// Deny, wrong PIN.
	rec = postForm(mux, "/api/settings/till-register",
		url.Values{"register_id": {"regA"}, "override_pin": {"000000"}}, &cashier)
	assertElevationPrompt(t, "wrong pin", rec.Code, rec.Body.String())
	if !strings.Contains(rec.Body.String(), "login-error") {
		t.Fatalf("failed PIN attempt must render the inline error: %s", rec.Body.String())
	}

	// Valid approver PIN.
	rec = postForm(mux, "/api/settings/till-register",
		url.Values{"register_id": {"regA"}, "override_pin": {"555222"}}, &cashier)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("elevated till-register = %d body=%s, want 200 with a confirmation", rec.Code, rec.Body.String())
	}
	if v, _, _ := d.Settings.Get(t.Context(), pos.SettingsKeyTillRegisterID); v != "regA" {
		t.Fatalf("elevated till-register wrote %q, want regA", v)
	}
	assertElevatedAudit(t, d, "till_register_changed", mgrID, cashierID)
}

// Remove-demo-catalogue — the irreversible one — against a REAL migrated DB
// (newRealDBDeps: full catalogue tables, real audit_log FKs): denied
// attempts leave every sample row in place; a valid approver PIN runs the
// removal, reports it, and audits removed/kept counts with dual attribution.
func TestSettingsRemoveDemo_ElevationFlow(t *testing.T) {
	mux, d := newRealDBDeps(t)
	repo := data.NewDemoSeedRepo(d.Db)
	if err := repo.SeedDemoCatalogue(t.Context()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	mgrID, cashierID := seedElevationUsers(t, d)
	cashier := auth.User{ID: cashierID, Role: "cashier"}

	// Deny, no PIN: prompt, all 50 sample items intact.
	rec := postForm(mux, "/api/settings/remove-demo-catalogue", url.Values{}, &cashier)
	assertElevationPrompt(t, "no pin", rec.Code, rec.Body.String())
	if n, _ := repo.SampleItemCount(t.Context()); n != 50 {
		t.Fatalf("denied removal ran anyway: %d sample items left, want 50", n)
	}

	// Deny, wrong PIN: re-prompt with the inline error, still intact.
	rec = postForm(mux, "/api/settings/remove-demo-catalogue", url.Values{"override_pin": {"000000"}}, &cashier)
	assertElevationPrompt(t, "wrong pin", rec.Code, rec.Body.String())
	if !strings.Contains(rec.Body.String(), "login-error") {
		t.Fatalf("failed PIN attempt must render the inline error: %s", rec.Body.String())
	}
	if n, _ := repo.SampleItemCount(t.Context()); n != 50 {
		t.Fatalf("wrong-PIN removal ran anyway: %d sample items left, want 50", n)
	}

	// Valid approver PIN: the removal really runs.
	rec = postForm(mux, "/api/settings/remove-demo-catalogue", url.Values{"override_pin": {"555222"}}, &cashier)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Removed 50 sample record") {
		t.Fatalf("elevated removal = %d body=%s, want the removed-count report", rec.Code, rec.Body.String())
	}
	if n, _ := repo.SampleItemCount(t.Context()); n != 0 {
		t.Fatalf("%d sample items left after elevated removal, want 0", n)
	}
	assertElevatedAudit(t, d, "demo_data_removed", mgrID, cashierID)
	// The payload must record the real counts (irreversible deletion — the
	// trail is the only place the numbers survive).
	var payload string
	if err := d.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action='demo_data_removed'`).Scan(&payload); err != nil {
		t.Fatalf("read audit payload: %v", err)
	}
	if !strings.Contains(payload, `"removed":50`) || !strings.Contains(payload, `"kept":0`) {
		t.Fatalf("audit payload %s does not record removed=50 kept=0", payload)
	}
}

// Lighter deny+elevate coverage for the remaining five wired handlers
// (payments-default, payments-fee, shop-type, till-name, save): a denied
// cashier gets the prompt; the same request with a valid approver PIN gets
// past the gate with a visible confirmation.
func TestSettingsElevation_RemainingSites_DenyAndElevate(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	seedElevationUsers(t, d)
	cashier := auth.User{ID: "cashier-x", Role: "cashier"}

	cases := []struct {
		name, path string
		form       url.Values
	}{
		{"payments-default", "/api/settings/payments-default", url.Values{"method": {"cash"}}},
		{"payments-fee", "/api/settings/payments-fee", url.Values{"method": {"card"}, "percent": {"1.5"}, "fixed": {"0.10"}}},
		{"shop-type", "/api/settings/shop-type", url.Values{"shop_type": {"cafe"}}},
		{"till-name", "/api/settings/till-name", url.Values{"name": {"Front"}}},
		{"save", "/api/settings/save", url.Values{"currency": {"EUR"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/deny_no_pin", func(t *testing.T) {
			rec := postForm(mux, tc.path, tc.form, &cashier)
			assertElevationPrompt(t, tc.name, rec.Code, rec.Body.String())
		})
		t.Run(tc.name+"/elevate_valid_pin", func(t *testing.T) {
			form := url.Values{}
			for k, v := range tc.form {
				form[k] = v
			}
			form.Set("override_pin", "555222")
			rec := postForm(mux, tc.path, form, &cashier)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
				t.Fatalf("%s elevated = %d body=%s, want 200 with a confirmation", tc.name, rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "elevation-dialog") {
				t.Fatalf("%s elevated still shows the prompt: %s", tc.name, rec.Body.String())
			}
		})
	}
}

// ut-docs#796 review finding #7: the single highest-risk property of this
// slice — an elevated "settings" approval must never let anyone write a
// fiscal.* key without independently passing ADR-0048's OWN
// "fiscal_tse_override" gate — was verified by hand during review but had
// no regression test pinning it. This is that test, against a REAL migrated
// DB so migration 046's real role_permissions seed (fiscal_tse_override
// granted to admin/super_admin ONLY, never manager) is in effect.
//
// The approver here is a genuine ADMIN who legitimately holds
// fiscal_tse_override on their own session — the strongest case: even an
// approver who COULD themselves write this key must still be refused,
// because canPerform("fiscal_tse_override") reads the blocked CASHIER's
// session off r, not the approver identity checkOrElevate resolved. An
// elevated "settings" approval only ever satisfies the outer "settings"
// gate; it must never be read as blanket authorization for a different,
// more sensitive action gated separately.
func TestSettingsUpsert_ElevatedSettingsApprovalDoesNotGrantFiscalOverride(t *testing.T) {
	mux, d := newRealDBDeps(t)
	authRepo := data.NewAuthRepo(d.Db)
	ctx := t.Context()

	adminID, err := authRepo.CreateUser(ctx, "admin-elev", "Elevation Admin", "admin")
	if err != nil {
		t.Fatalf("create admin: %v", err)
	}
	hash, err := auth.HashPIN("777333")
	if err != nil {
		t.Fatalf("hash admin pin: %v", err)
	}
	if err := authRepo.SetUserPIN(ctx, adminID, hash); err != nil {
		t.Fatalf("set admin pin: %v", err)
	}
	cashierID, err := authRepo.CreateUser(ctx, "cashier-fiscal", "Fiscal-Blocked Cashier", "cashier")
	if err != nil {
		t.Fatalf("create cashier: %v", err)
	}
	cashier := auth.User{ID: cashierID, Role: "cashier"}

	rec := postForm(mux, "/api/settings/upsert",
		url.Values{"key": {"fiscal.system_of_record"}, "value": {"till"}, "override_pin": {"777333"}}, &cashier)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("elevated-settings write to a fiscal.* key = %d body=%s, want 403 (fiscal_tse_override must be checked on the session user, not granted by settings elevation)", rec.Code, rec.Body.String())
	}
	if v, ok, _ := d.Settings.Get(ctx, "fiscal.system_of_record"); ok {
		t.Fatalf("fiscal.system_of_record was written (%q) despite the 403 — the outer settings elevation leaked into the fiscal gate", v)
	}
}
