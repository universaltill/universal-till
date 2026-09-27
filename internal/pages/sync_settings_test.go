package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// Main-till side of ut-docs#2791: POST /api/sync/settings/apply is where an
// additional till's shop-wide settings change lands (settings_sync_proxy.go
// sends it). Same shape as sync_users_test.go: syncTill bearer, JSON
// envelope, a real migrated database; the actor's permission is decided
// with the MAIN till's own users and role table.

const syncSettingsBearer = "bearer-t2"

type syncSettingsMain struct {
	mux      *http.ServeMux
	dp       *common.Deps
	refreshN atomic.Int64
}

func newSyncSettingsTestDeps(t *testing.T) *syncSettingsMain {
	t.Helper()
	chdirRoot(t)
	dbase := openPagesTestDB(t)
	t.Cleanup(func() { dbase.Close() })
	m := &syncSettingsMain{dp: &common.Deps{Db: dbase, Settings: settings.NewStore(dbase)}}
	seedSyncOrdersTill(t, m.dp, "Till 2", syncSettingsBearer)
	pin, err := auth.HashPIN("7391")
	if err != nil {
		t.Fatal(err)
	}
	insertTestUserWithPIN(t, dbase, "adm-1", "adm1", "Admin One", "admin", pin)
	insertTestUserWithPIN(t, dbase, "m-1", "mia", "Mia", "manager", pin)
	insertTestUserWithPIN(t, dbase, "c-1", "cara", "Cara", "cashier", pin)
	m.mux = http.NewServeMux()
	registerSyncSettings(m.mux, m.dp, func(context.Context) { m.refreshN.Add(1) })
	return m
}

func postSyncSettingsApply(mux *http.ServeMux, body, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/sync/settings/apply", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func settingsApplyBody(t *testing.T, in syncSettingsApplyRequest) string {
	t.Helper()
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func one(key, value string) []syncSettingKV { return []syncSettingKV{{Key: key, Value: value}} }

type syncSettingsApplyResponse struct {
	Data *struct {
		Settings []syncSettingKV `json:"settings"`
	} `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func wantSyncSettingsError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, status, rec.Body.String())
	}
	var out syncSettingsApplyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (body %q)", err, rec.Body.String())
	}
	if out.Data != nil || out.Error == nil || out.Error.Code != code {
		t.Fatalf("body = %q, want data null and error.code %q", rec.Body.String(), code)
	}
}

func mustSetting(t *testing.T, dp *common.Deps, key string) string {
	t.Helper()
	v, _, err := dp.Settings.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestSyncSettingsApply_AppliesAuditsRefreshesAndAnswers(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	body := settingsApplyBody(t, syncSettingsApplyRequest{
		Settings: []syncSettingKV{{Key: data.OrderTypePromptModeKey, Value: "at_pay"}, {Key: "payments.default_method", Value: "card"}},
		ActorID:  "m-1",
	})
	rec := postSyncSettingsApply(m.mux, body, syncSettingsBearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %q", rec.Code, rec.Body.String())
	}
	var out syncSettingsApplyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Error != nil || out.Data == nil || len(out.Data.Settings) != 2 {
		t.Fatalf("body = %q (err %v)", rec.Body.String(), err)
	}
	if got := mustSetting(t, m.dp, data.OrderTypePromptModeKey); got != "at_pay" {
		t.Fatalf("%s = %q, want at_pay", data.OrderTypePromptModeKey, got)
	}
	if got := mustSetting(t, m.dp, "payments.default_method"); got != "card" {
		t.Fatalf("payments.default_method = %q", got)
	}
	if m.refreshN.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1 (the main till's own cached globals must follow)", m.refreshN.Load())
	}
	assertSettingSyncAudit(t, m.dp, "m-1", data.OrderTypePromptModeKey, "at_pay", "Till 2")
	assertSettingSyncAudit(t, m.dp, "m-1", "payments.default_method", "card", "Till 2")
}

// assertSettingSyncAudit finds the main till's authoritative audit row for
// one key: setting_changed_via_till by actor, provenance via till-sync +
// the calling till's name, and the value.
func assertSettingSyncAudit(t *testing.T, dp *common.Deps, actorID, key, value, till string) {
	t.Helper()
	entries, err := data.NewPOSRepo(dp.Db).ListAudit(context.Background(), data.AuditFilters{EntityType: "settings", ActorID: actorID})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	for _, e := range entries {
		if e.Action != "setting_changed_via_till" || e.EntityID != key {
			continue
		}
		p := e.DataJSON
		if !strings.Contains(p, "till-sync") || !strings.Contains(p, till) || !strings.Contains(p, value) {
			t.Fatalf("audit payload for %s = %s, want via till-sync, till %q, value %q", key, p, till, value)
		}
		return
	}
	t.Fatalf("no setting_changed_via_till audit by %s on %s, got %+v", actorID, key, entries)
}

func TestSyncSettingsApply_RequiresBearer(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	body := settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(data.OrderTypePromptModeKey, "at_pay"), ActorID: "m-1"})
	for _, bearer := range []string{"", "wrong"} {
		wantSyncSettingsError(t, postSyncSettingsApply(m.mux, body, bearer), http.StatusUnauthorized, "unauthorized")
	}
	if mustSetting(t, m.dp, data.OrderTypePromptModeKey) != "" {
		t.Fatal("a refused call must write nothing")
	}
}

func TestSyncSettingsApply_RefusedOnReplica(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	setReplicaSettings(t, m.dp.Settings, "http://192.0.2.1:8080", "b-123")
	body := settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(data.OrderTypePromptModeKey, "at_pay"), ActorID: "m-1"})
	wantSyncSettingsError(t, postSyncSettingsApply(m.mux, body, syncSettingsBearer), http.StatusConflict, "replica")
	if mustSetting(t, m.dp, data.OrderTypePromptModeKey) != "" {
		t.Fatal("a replica must not apply a write-through")
	}
}

func TestSyncSettingsApply_Validation(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	many := make([]syncSettingKV, 51)
	for i := range many {
		many[i] = syncSettingKV{Key: fmt.Sprintf("payments.fee.m%d", i), Value: "{}"}
	}
	for _, tc := range []struct {
		name, body string
		status     int
		code       string
	}{
		{"not json", `{`, http.StatusBadRequest, "invalid_body"},
		{"empty list", settingsApplyBody(t, syncSettingsApplyRequest{ActorID: "m-1"}), http.StatusBadRequest, "invalid_body"},
		{"over 50 entries", settingsApplyBody(t, syncSettingsApplyRequest{Settings: many, ActorID: "m-1"}), http.StatusBadRequest, "invalid_body"},
		{"key too long", settingsApplyBody(t, syncSettingsApplyRequest{Settings: one("store."+strings.Repeat("k", 300), "x"), ActorID: "m-1"}), http.StatusBadRequest, "invalid_body"},
		{"value too long", settingsApplyBody(t, syncSettingsApplyRequest{Settings: one("store.name", strings.Repeat("v", maxSyncSettingValueLen+1)), ActorID: "m-1"}), http.StatusBadRequest, "invalid_body"},
		{"per-till key", settingsApplyBody(t, syncSettingsApplyRequest{Settings: one("printer.host", "10.0.0.9"), ActorID: "m-1"}), http.StatusBadRequest, "not_shop_wide"},
		{"per-till key among shop-wide", settingsApplyBody(t, syncSettingsApplyRequest{Settings: []syncSettingKV{{Key: "store.name", Value: "X"}, {Key: "sync.primary_url", Value: "http://evil"}}, ActorID: "m-1"}), http.StatusBadRequest, "not_shop_wide"},
		{"unclassified key", settingsApplyBody(t, syncSettingsApplyRequest{Settings: one("no_such_family.key", "x"), ActorID: "m-1"}), http.StatusBadRequest, "not_shop_wide"},
		{"fabricated override", settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(fiscal.KeyOverrideUntil, "2099-01-01T00:00:00Z"), ActorID: "adm-1"}), http.StatusBadRequest, "fiscal_not_settable"},
		{"failing_since", settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(wireKeySigningDeviceFailingSince+".de", ""), ActorID: "adm-1"}), http.StatusBadRequest, "fiscal_not_settable"},
		{"no actor", settingsApplyBody(t, syncSettingsApplyRequest{Settings: one("store.name", "X")}), http.StatusBadRequest, "unknown_actor"},
		{"unknown actor", settingsApplyBody(t, syncSettingsApplyRequest{Settings: one("store.name", "X"), ActorID: "ghost"}), http.StatusBadRequest, "unknown_actor"},
		{"unknown approver", settingsApplyBody(t, syncSettingsApplyRequest{Settings: one("store.name", "X"), ActorID: "c-1", ApproverID: "ghost"}), http.StatusBadRequest, "unknown_approver"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantSyncSettingsError(t, postSyncSettingsApply(m.mux, tc.body, syncSettingsBearer), tc.status, tc.code)
		})
	}
	if v := mustSetting(t, m.dp, "store.name"); v == "X" {
		t.Fatal("a refused batch wrote a key")
	}
	if m.refreshN.Load() != 0 {
		t.Fatal("a refused call must not refresh")
	}
}

// The main till decides with ITS roles: a cashier named as the actor is
// refused whatever the additional till believed; an elevated request is
// decided by the approver's role; a deactivated manager is refused.
func TestSyncSettingsApply_Authorization(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	send := func(actor, approver string) *httptest.ResponseRecorder {
		return postSyncSettingsApply(m.mux, settingsApplyBody(t, syncSettingsApplyRequest{
			Settings: one(data.OrderTypePromptModeKey, "at_pay"), ActorID: actor, ApproverID: approver,
		}), syncSettingsBearer)
	}
	wantSyncSettingsError(t, send("c-1", ""), http.StatusForbidden, "forbidden")
	if mustSetting(t, m.dp, data.OrderTypePromptModeKey) != "" {
		t.Fatal("forbidden change was written")
	}
	// The approver's permission decides an elevated request -- a cashier
	// approving for a cashier is still refused.
	insertTestUserWithPIN(t, m.dp.Db, "c-2", "cody", "Cody", "cashier", "")
	wantSyncSettingsError(t, send("c-1", "c-2"), http.StatusForbidden, "forbidden")

	// Deactivated on the main till: refused even though the role holds it.
	if err := data.NewAuthRepo(m.dp.Db).SetUserActive(t.Context(), "m-1", false); err != nil {
		t.Fatal(err)
	}
	wantSyncSettingsError(t, send("m-1", ""), http.StatusForbidden, "forbidden")
	wantSyncSettingsError(t, send("c-1", "m-1"), http.StatusForbidden, "forbidden")

	// A cashier elevated by an active admin: allowed, audited to both.
	rec := send("c-1", "adm-1")
	if rec.Code != http.StatusOK {
		t.Fatalf("elevated apply = %d %q", rec.Code, rec.Body.String())
	}
	if mustSetting(t, m.dp, data.OrderTypePromptModeKey) != "at_pay" {
		t.Fatal("elevated change not written")
	}
	assertSettingSyncAudit(t, m.dp, "adm-1", data.OrderTypePromptModeKey, "at_pay", "Till 2")
}

// Clearing a fiscal override (and the two fiscal posture flags) needs
// fiscal_tse_override on the ACTOR, exactly like the upsert handler: an
// elevated "settings" approval never grants it.
func TestSyncSettingsApply_FiscalKeysNeedActorFiscalPermission(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	for _, key := range []string{fiscal.KeyOverrideUntil, fiscal.KeySystemOfRecord, wireKeySigningDeviceConfigured + ".de"} {
		body := settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(key, ""), ActorID: "m-1"})
		wantSyncSettingsError(t, postSyncSettingsApply(m.mux, body, syncSettingsBearer), http.StatusForbidden, "forbidden")
		body = settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(key, ""), ActorID: "c-1", ApproverID: "adm-1"})
		wantSyncSettingsError(t, postSyncSettingsApply(m.mux, body, syncSettingsBearer), http.StatusForbidden, "forbidden")
		body = settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(key, ""), ActorID: "adm-1"})
		if rec := postSyncSettingsApply(m.mux, body, syncSettingsBearer); rec.Code != http.StatusOK {
			t.Fatalf("%s by an admin = %d %q", key, rec.Code, rec.Body.String())
		}
	}
}

// ut-docs#2980: store.country through the main till. The main till runs
// the same country-change invariant the local handlers do
// (fiscal_country_change.go) against ITS OWN posture and role table: the
// owner-only check while the country being left has a confirmed signing
// device, the old country's posture reset before the write, and
// ut-docs#1027's locale re-derive unless the locale was confirmed.

// seedMainCountry puts the main till on country with a confirmed signing
// device when configured is true.
func seedMainCountry(t *testing.T, dp *common.Deps, country string, configured bool) {
	t.Helper()
	if err := dp.Settings.Set(t.Context(), common.KeyCountry, country); err != nil {
		t.Fatal(err)
	}
	dp.SetState(common.RuntimeState{Country: country, Locale: "en"})
	if configured {
		if err := dp.Settings.Set(t.Context(), fiscal.SigningDeviceConfiguredKey(country), "true"); err != nil {
			t.Fatal(err)
		}
	}
}

func answeredSettings(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var out syncSettingsApplyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Error != nil || out.Data == nil {
		t.Fatalf("body = %q (err %v)", rec.Body.String(), err)
	}
	got := map[string]string{}
	for _, s := range out.Data.Settings {
		got[s.Key] = s.Value
	}
	return got
}

func TestSyncSettingsApply_CountryChangeResetsPostureAndDerivesLocale(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	seedMainCountry(t, m.dp, "GB", true)
	if err := m.dp.Settings.Set(t.Context(), common.KeyLocale, "en"); err != nil {
		t.Fatal(err)
	}
	body := settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(common.KeyCountry, "DE"), ActorID: "adm-1"})
	rec := postSyncSettingsApply(m.mux, body, syncSettingsBearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %q", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, m.dp, common.KeyCountry); got != "DE" {
		t.Fatalf("main till %s = %q, want DE", common.KeyCountry, got)
	}
	if got := mustSetting(t, m.dp, fiscal.SigningDeviceConfiguredKey("GB")); got != "" {
		t.Fatalf("the country being left keeps its posture %q — it must be reset", got)
	}
	entries, err := data.NewPOSRepo(m.dp.Db).ListAudit(t.Context(), data.AuditFilters{EntityType: "settings", ActorID: "adm-1"})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.Action == "tse_configured_changed" && e.EntityID == fiscal.SigningDeviceConfiguredKey("GB") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("posture reset audit rows = %d, want 1", n)
	}
	// ut-docs#1027: the locale follows the new country and travels back in
	// the answer, so the additional till mirrors the pair together.
	if got := mustSetting(t, m.dp, common.KeyLocale); got != "de-DE" {
		t.Fatalf("main till %s = %q, want the derived de-DE", common.KeyLocale, got)
	}
	ans := answeredSettings(t, rec)
	if ans[common.KeyCountry] != "DE" || ans[common.KeyLocale] != "de-DE" {
		t.Fatalf("answer = %v, want store.country DE and store.locale de-DE", ans)
	}
	if _, ok := ans[fiscal.SigningDeviceConfiguredKey("GB")]; ok {
		t.Fatal("the posture reset must reach additional tills through the pull, not the answer")
	}
	// ut-docs#1068: the new country's base plugins are queued here, where
	// the country changed.
	wantPending(t, m.dp, specDE)
	if m.refreshN.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", m.refreshN.Load())
	}
}

func TestSyncSettingsApply_CountryChangeNeedsActorFiscalAuthorityWhileConfigured(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	seedMainCountry(t, m.dp, "DE", true)
	for _, in := range []syncSettingsApplyRequest{
		{Settings: one(common.KeyCountry, "GB"), ActorID: "m-1"},
		// An elevated settings approval never grants the fiscal authority:
		// the upsert handler checks the session user.
		{Settings: one(common.KeyCountry, "GB"), ActorID: "c-1", ApproverID: "adm-1"},
	} {
		wantSyncSettingsError(t, postSyncSettingsApply(m.mux, settingsApplyBody(t, in), syncSettingsBearer), http.StatusForbidden, "forbidden")
	}
	if got := mustSetting(t, m.dp, common.KeyCountry); got != "DE" {
		t.Fatalf("refused change moved the country to %q", got)
	}
	if got := mustSetting(t, m.dp, fiscal.SigningDeviceConfiguredKey("DE")); got != "true" {
		t.Fatalf("refused change touched the posture: %q", got)
	}
	wantPending(t, m.dp)
	if m.refreshN.Load() != 0 {
		t.Fatal("a refused call must not refresh")
	}
}

func TestSyncSettingsApply_CountryChangeByManagerWithoutConfiguredDevice(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	seedMainCountry(t, m.dp, "GB", false)
	if err := m.dp.Settings.Set(t.Context(), common.KeyLocaleConfirmed, "true"); err != nil {
		t.Fatal(err)
	}
	if err := m.dp.Settings.Set(t.Context(), common.KeyLocale, "en"); err != nil {
		t.Fatal(err)
	}
	body := settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(common.KeyCountry, " ES "), ActorID: "m-1"})
	rec := postSyncSettingsApply(m.mux, body, syncSettingsBearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %q", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, m.dp, common.KeyCountry); got != "ES" {
		t.Fatalf("main till %s = %q, want ES", common.KeyCountry, got)
	}
	// ut-docs#1074: a confirmed locale is never re-derived.
	if got := mustSetting(t, m.dp, common.KeyLocale); got != "en" {
		t.Fatalf("confirmed locale changed to %q", got)
	}
	if _, ok := answeredSettings(t, rec)[common.KeyLocale]; ok {
		t.Fatal("no derived locale may be answered when the locale is confirmed")
	}
	assertSettingSyncAudit(t, m.dp, "m-1", common.KeyCountry, "ES", "Till 2")
}

// A batch that names the locale itself keeps it: the additional till's
// /api/settings/save derives or chooses the locale and sends it along.
func TestSyncSettingsApply_CountryWithExplicitLocaleKeepsIt(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	seedMainCountry(t, m.dp, "GB", false)
	body := settingsApplyBody(t, syncSettingsApplyRequest{
		Settings: []syncSettingKV{{Key: common.KeyCountry, Value: "DE"}, {Key: common.KeyLocale, Value: "en"}},
		ActorID:  "m-1",
	})
	rec := postSyncSettingsApply(m.mux, body, syncSettingsBearer)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %q", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, m.dp, common.KeyLocale); got != "en" {
		t.Fatalf("main till %s = %q, want the batch's en", common.KeyLocale, got)
	}
	if ans := answeredSettings(t, rec); len(ans) != 2 || ans[common.KeyLocale] != "en" {
		t.Fatalf("answer = %v", ans)
	}
}

// Re-sending the current country is no change: no posture reset, no
// owner check, nothing queued.
func TestSyncSettingsApply_SameCountryIsNoChange(t *testing.T) {
	m := newSyncSettingsTestDeps(t)
	seedMainCountry(t, m.dp, "DE", true)
	body := settingsApplyBody(t, syncSettingsApplyRequest{Settings: one(common.KeyCountry, "de"), ActorID: "m-1"})
	if rec := postSyncSettingsApply(m.mux, body, syncSettingsBearer); rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %q", rec.Code, rec.Body.String())
	}
	if got := mustSetting(t, m.dp, fiscal.SigningDeviceConfiguredKey("DE")); got != "true" {
		t.Fatalf("no-change re-send reset the posture: %q", got)
	}
	wantPending(t, m.dp)
}
