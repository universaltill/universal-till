package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// newFiscalAPIDeps wires the fiscal API against the shared pages fixture,
// with real users (owner/manager/cashier) so both the session-role path and
// the owner-PIN path are exercised against the same lookup production uses.
func newFiscalAPIDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	t.Setenv("UT_AUTH", "on") // never let auth-off dev mode weaken these tests
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	for _, u := range []struct{ id, name, pin, role string }{
		{"own1", "owner", "1111", "admin"},
		{"mgr1", "manager", "2222", "manager"},
		{"csh1", "cashier", "3333", "cashier"},
	} {
		hash, err := auth.HashPIN(u.pin)
		if err != nil {
			t.Fatalf("hash pin: %v", err)
		}
		if _, err := db.Exec(
			`INSERT INTO users (id, username, display_name, pin_hash, role, is_active, created_at) VALUES (?, ?, ?, ?, ?, 1, ?)`,
			u.id, u.name, u.name, hash, u.role, time.Now().UTC().Format(time.RFC3339),
		); err != nil {
			t.Fatalf("seed user %s: %v", u.id, err)
		}
	}

	dp := &common.Deps{
		Cfg:      &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}},
		Db:       db,
		Settings: settings.NewStore(db),
		AuthSvc:  auth.NewService(db),
	}
	dp.SetState(common.LoadState(context.Background(), dp.Settings, dp.Cfg))
	mux := http.NewServeMux()
	registerFiscalAPI(mux, dp)
	return mux, dp
}

// postOverride sends a form-encoded grant request, optionally as a signed-in
// user (sessionUser attached via the auth test seam).
func postOverride(mux *http.ServeMux, sessionUser *auth.User, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/fiscal/tse-override", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if sessionUser != nil {
		req = auth.WithUser(req, *sessionUser)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func validOverrideForm() url.Values {
	return url.Values{
		"reason":           {"TSE dongle failed, replacement ordered"},
		"acknowledgement":  {fiscal.ConfirmationPhrase},
		"duration_minutes": {"120"},
	}
}

func overrideKeysUnset(t *testing.T, dp *common.Deps) {
	t.Helper()
	for _, k := range []string{fiscal.KeyOverrideUntil, fiscal.KeyOverrideReason, fiscal.KeyOverrideActor} {
		if v, ok, _ := dp.Settings.Get(context.Background(), k); ok && strings.TrimSpace(v) != "" {
			t.Fatalf("override key %s must not be set, got %q", k, v)
		}
	}
}

var ownerSession = auth.User{ID: "own1", Username: "owner", Role: "admin"}

// The literal ut-docs#715 acceptance criterion: the never-configured state
// cannot reach the override by ANY path, including a direct handler call
// with a valid owner session/PIN and otherwise-valid fields.
func TestTSEOverride_RefusedWhenNeverConfigured(t *testing.T) {
	mux, dp := newFiscalAPIDeps(t)
	// fiscal.tse_configured deliberately unset.

	rec := postOverride(mux, &ownerSession, validOverrideForm())
	if rec.Code != http.StatusConflict {
		t.Fatalf("never-configured grant must be refused, got %d: %s", rec.Code, rec.Body.String())
	}
	overrideKeysUnset(t, dp)

	// Explicit false is refused identically, and a PIN instead of a
	// session changes nothing.
	if err := dp.Settings.Set(context.Background(), fiscal.KeyTSEConfigured, "false"); err != nil {
		t.Fatal(err)
	}
	form := validOverrideForm()
	form.Set("owner_pin", "1111")
	rec = postOverride(mux, nil, form)
	if rec.Code != http.StatusConflict {
		t.Fatalf("never-configured grant via PIN must be refused, got %d: %s", rec.Code, rec.Body.String())
	}
	overrideKeysUnset(t, dp)

	// No grant audit entry may exist on any refused path.
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type = 'fiscal_override'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("refused grant must not write a grant audit entry, got %d", n)
	}
}

func TestTSEOverride_ManagerIsRejectedEvenWithValidPIN(t *testing.T) {
	mux, dp := newFiscalAPIDeps(t)
	if err := dp.Settings.Set(context.Background(), fiscal.KeyTSEConfigured, "true"); err != nil {
		t.Fatal(err)
	}

	// A manager's PIN authenticates via AuthorizeManager, but the override
	// is admin/super_admin only (ADR-0048: must not become manager-or-above).
	form := validOverrideForm()
	form.Set("owner_pin", "2222")
	rec := postOverride(mux, &auth.User{ID: "csh1", Username: "cashier", Role: "cashier"}, form)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("manager PIN must be rejected, got %d: %s", rec.Code, rec.Body.String())
	}
	overrideKeysUnset(t, dp)

	// A manager session with no PIN is rejected too.
	rec = postOverride(mux, &auth.User{ID: "mgr1", Username: "manager", Role: "manager"}, validOverrideForm())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("manager session must not self-grant, got %d: %s", rec.Code, rec.Body.String())
	}
	overrideKeysUnset(t, dp)
}

func TestTSEOverride_ValidationRejects(t *testing.T) {
	mux, dp := newFiscalAPIDeps(t)
	if err := dp.Settings.Set(context.Background(), fiscal.KeyTSEConfigured, "true"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name  string
		mutil func(url.Values)
	}{
		{"missing reason", func(f url.Values) { f.Set("reason", "  ") }},
		{"missing acknowledgement", func(f url.Values) { f.Del("acknowledgement") }},
		{"wrong acknowledgement", func(f url.Values) { f.Set("acknowledgement", "i understand these sales will not be tse-signed") }},
		{"duration above 8h cap is rejected, not clamped", func(f url.Values) { f.Set("duration_minutes", "481") }},
		{"zero duration", func(f url.Values) { f.Set("duration_minutes", "0") }},
		{"unparsable duration", func(f url.Values) { f.Set("duration_minutes", "soon") }},
	}
	for _, tc := range cases {
		form := validOverrideForm()
		tc.mutil(form)
		rec := postOverride(mux, &ownerSession, form)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: expected 400, got %d: %s", tc.name, rec.Code, rec.Body.String())
		}
		overrideKeysUnset(t, dp)
	}
}

func TestTSEOverride_GrantSucceedsAndUnblocksGate(t *testing.T) {
	mux, dp := newFiscalAPIDeps(t)
	ctx := context.Background()
	setFiscalTestSettings(t, dp, map[string]string{
		common.KeyCountry:         "DE",
		fiscal.KeySystemOfRecord:  "true",
		fiscal.KeyTSEConfigured:   "true",
		fiscal.KeyTSEFailingSince: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
	})

	// Blocked before the grant.
	if _, err := fiscal.CheckSaleAllowed(ctx, dp.Settings, "DE", time.Now().UTC()); err == nil {
		t.Fatalf("gate must block before the grant")
	}

	// Cashier session + owner PIN: the admin authorizes in place and
	// becomes the audit actor; the cashier is recorded as requested_by.
	form := validOverrideForm()
	form.Set("owner_pin", "1111")
	rec := postOverride(mux, &auth.User{ID: "csh1", Username: "cashier", Role: "cashier"}, form)
	if rec.Code != http.StatusOK {
		t.Fatalf("grant failed: %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Data struct {
			Until string `json:"until"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad envelope: %v: %s", err, rec.Body.String())
	}
	until, err := time.Parse(time.RFC3339, resp.Data.Until)
	if err != nil {
		t.Fatalf("until not RFC3339: %v", err)
	}
	if remaining := time.Until(until); remaining > 121*time.Minute || remaining < 118*time.Minute {
		t.Fatalf("window must match the requested 120 minutes, got %v", remaining)
	}

	// The gate now proceeds, reporting the window.
	res, err := fiscal.CheckSaleAllowed(ctx, dp.Settings, "DE", time.Now().UTC())
	if err != nil {
		t.Fatalf("gate must proceed after the grant, got %v", err)
	}
	if !res.OverrideActive || res.OverrideActor != "own1" {
		t.Fatalf("gate must report the granted window/actor, got %+v", res)
	}

	// Grant audit entry: actor, reason, timestamp, window (ADR-0048).
	var dataJSON string
	if err := dp.Db.QueryRow(
		`SELECT data_json FROM audit_log WHERE entity_type = 'fiscal_override' AND action = 'grant'`,
	).Scan(&dataJSON); err != nil {
		t.Fatalf("expected a grant audit entry: %v", err)
	}
	for _, want := range []string{
		`"actor":"own1"`,
		`"requested_by":"csh1"`,
		`"duration_minutes":120`,
		"TSE dongle failed, replacement ordered",
		`"granted_at"`,
		`"until"`,
	} {
		if !strings.Contains(dataJSON, want) {
			t.Fatalf("grant audit payload missing %s: %s", want, dataJSON)
		}
	}
}

func TestFiscalToggles_OwnerOnlyAndAudited(t *testing.T) {
	mux, dp := newFiscalAPIDeps(t)

	post := func(path, body string, user *auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if user != nil {
			req = auth.WithUser(req, *user)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// Manager (and below) may not touch either toggle.
	for _, path := range []string{"/api/fiscal/system-of-record", "/api/fiscal/tse-configured"} {
		rec := post(path, "enabled=true", &auth.User{ID: "mgr1", Role: "manager"})
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: manager must be forbidden, got %d", path, rec.Code)
		}
	}

	// Owner flips system_of_record on, then off — every write audited with
	// from/to (ADR-0048 Decision 1).
	for i, enabled := range []string{"true", "false"} {
		rec := post("/api/fiscal/system-of-record", "enabled="+enabled, &ownerSession)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("toggle %d: got %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	v, _, _ := dp.Settings.Get(context.Background(), fiscal.KeySystemOfRecord)
	if v != "false" {
		t.Fatalf("expected final value false, got %q", v)
	}
	rows, err := dp.Db.Query(
		`SELECT data_json FROM audit_log WHERE entity_type = 'fiscal_settings' AND action = 'system_of_record_changed' ORDER BY created_at, id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var payloads []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		payloads = append(payloads, s)
	}
	if len(payloads) != 2 {
		t.Fatalf("every system_of_record write must be audited, got %d entries", len(payloads))
	}
	for i, want := range []struct{ from, to string }{{"false", "true"}, {"true", "false"}} {
		if !strings.Contains(payloads[i], fmt.Sprintf(`"from":%s`, want.from)) ||
			!strings.Contains(payloads[i], fmt.Sprintf(`"to":%s`, want.to)) ||
			!strings.Contains(payloads[i], `"actor":"own1"`) {
			t.Fatalf("audit %d must carry actor/from/to, got %s", i, payloads[i])
		}
	}
}

func TestFiscalOverrideBanner_Fragment(t *testing.T) {
	mux, dp := newFiscalAPIDeps(t)

	get := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/ui/fiscal-override-banner", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	// No override -> empty fragment.
	rec := get()
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("no-override banner must be empty 200, got %d %q", rec.Code, rec.Body.String())
	}

	// Active override -> visible warning with the reason.
	setFiscalTestSettings(t, dp, map[string]string{
		fiscal.KeyOverrideUntil:  time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		fiscal.KeyOverrideReason: "dongle failed",
	})
	rec = get()
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "dongle failed") {
		t.Fatalf("active banner must render the reason, got %d %q", rec.Code, rec.Body.String())
	}

	// Expired override -> empty again, with nobody having to reset anything.
	setFiscalTestSettings(t, dp, map[string]string{
		fiscal.KeyOverrideUntil: time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	})
	rec = get()
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("expired banner must be empty 200, got %d %q", rec.Code, rec.Body.String())
	}
}
