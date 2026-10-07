package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2781: a till's role (additional | satellite) — chosen at pairing
// time, changeable later by a manager, carried to the joined till by the
// admin pull, and gating the device profile.

func initTillRoleI18n(t *testing.T) {
	t.Helper()
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
}

func postPairRequestWithRole(t *testing.T, mux *http.ServeMux, deviceName, commitment, role, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"device_name": deviceName, "commitment": commitment, "role": role})
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-request", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remoteAddr
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func pairRequestID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-request = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Data.ID == "" {
		t.Fatalf("pair-request body %s: %v", rec.Body, err)
	}
	return out.Data.ID
}

func TestPairRequest_PersistsRequestedRole(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)

	satID := pairRequestID(t, postPairRequestWithRole(t, mux, "Kiosk", commitOf("s1"), "satellite", "10.0.0.31:1"))
	// No role at all (an older till): an additional till.
	defID := pairRequestID(t, postPairRequest(t, mux, "Till 3", commitOf("s2"), "10.0.0.32:1"))

	repo := data.NewPairingRepo(dp.Db)
	for id, want := range map[string]string{satID: "satellite", defID: "additional"} {
		row, ok, err := repo.GetByID(t.Context(), id)
		if err != nil || !ok || row.RequestedRole != want {
			t.Fatalf("row %s: role %q ok=%v err=%v, want %q", id, row.RequestedRole, ok, err, want)
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests", nil))
	if !strings.Contains(rec.Body.String(), `"requested_role":"satellite"`) {
		t.Fatalf("pending list does not expose requested_role: %s", rec.Body)
	}
}

func TestPairRequest_InvalidRoleIs400(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	rec := postPairRequestWithRole(t, mux, "Kiosk", commitOf("s1"), "main", "10.0.0.33:1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("role=main: %d %s, want 400", rec.Code, rec.Body)
	}
	if list, _ := data.NewPairingRepo(dp.Db).ListPending(t.Context()); len(list) != 0 {
		t.Fatalf("a refused request was queued: %+v", list)
	}
}

// The manager's choice at approval is the role the till is enrolled as —
// whatever the replica itself asked for, and whatever it sends at enrol.
func TestApprovePairRequest_PersistsFinalRoleOntoTill(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	secret := "role-secret"
	id := pairRequestID(t, postPairRequestWithRole(t, mux, "Order station", commitOf(secret), "additional", "10.0.0.34:1"))

	approve := func(role string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+id+"/approve", strings.NewReader(url.Values{"role": {role}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := approve("bogus"); rec.Code != http.StatusBadRequest {
		t.Fatalf("approve role=bogus: %d %s, want 400", rec.Code, rec.Body)
	}
	if row, _, _ := data.NewPairingRepo(dp.Db).GetByID(t.Context(), id); row.Status != "pending" {
		t.Fatalf("a refused approve changed the row: %+v", row)
	}
	if rec := approve("satellite"); rec.Code != http.StatusOK {
		t.Fatalf("approve role=satellite: %d %s", rec.Code, rec.Body)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests/"+id+"?request_secret="+secret, nil))
	var tok struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &tok); err != nil || tok.Data.Token == "" {
		t.Fatalf("token retrieval: %s %v", rec.Body, err)
	}
	// The replica still says "additional" at enrol; the approval wins.
	body, _ := json.Marshal(map[string]string{"token": tok.Data.Token, "name": "Order station", "role": "additional"})
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enrol: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"role":"satellite"`) {
		t.Fatalf("enrol response does not carry the final role: %s", rec.Body)
	}
	tills, err := data.NewTillsRepo(dp.Db).ListTills(t.Context())
	if err != nil || len(tills) != 1 || tills[0].Role != data.TillRoleSatellite {
		t.Fatalf("enrolled tills = %+v err=%v, want one satellite", tills, err)
	}
}

// A QR / pairing-code enrolment has no approval: the joining till's own
// choice is the role; anything but the two values is refused before the
// one-time token is spent.
func TestEnroll_RoleFromJoiningTill(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newSyncPairingGateTestDeps(t)
	mux := http.NewServeMux()
	tokens := registerSyncAPI(mux, dp)
	tok := tokens.issue()
	enrol := func(role string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"token": tok, "name": "Kiosk", "role": role})
		req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := enrol("replica"); rec.Code != http.StatusBadRequest {
		t.Fatalf("role=replica: %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := enrol("satellite"); rec.Code != http.StatusOK {
		t.Fatalf("role=satellite: %d %s (the refused attempt must not have burned the token)", rec.Code, rec.Body)
	}
	tills, _ := data.NewTillsRepo(dp.Db).ListTills(t.Context())
	if len(tills) != 1 || tills[0].Role != data.TillRoleSatellite {
		t.Fatalf("tills = %+v, want one satellite", tills)
	}
}

// The approval card offers the role, preselected to what the till asked for.
func TestPendingPairingsUI_ShowsRoleSelect(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	initTillRoleI18n(t)
	mux, dp, _ := newPairingAPITestDeps(t)
	registerPendingPairingsUI(mux, dp)
	id := pairRequestID(t, postPairRequestWithRole(t, mux, "Kiosk", commitOf("s1"), "satellite", "10.0.0.35:1"))

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/tills/pending-pairings", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`id="role-` + id + `"`,
		`<option value="satellite" selected>Satellite</option>`,
		`<option value="additional">Additional till</option>`,
		`hx-include="#pin-` + id + `, #role-` + id + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pending card missing %q:\n%s", want, body)
		}
	}
}

// --- Role change on the Tills page (POST /api/tills/{id}/role) ---

func postTillRole(t *testing.T, mux *http.ServeMux, id, role, userRole string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tills/"+id+"/role", strings.NewReader(url.Values{"role": {role}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = auth.WithUser(req, auth.User{ID: "u-" + userRole, Role: userRole})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func auditRows(t *testing.T, dp *common.Deps, action string) []string {
	t.Helper()
	rows, err := dp.Db.Query(`SELECT entity_type || '|' || entity_id || '|' || actor_id || '|' || data_json FROM audit_log WHERE action = ? ORDER BY created_at`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestTillRoleChange_ManagerOnlyAuditedAndShownOnRoster(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	initTillRoleI18n(t)
	dp := newSyncPairingGateTestDeps(t)
	mux := http.NewServeMux()
	registerTillsRoster(mux, dp)
	registerTillRole(mux, dp)
	id, err := data.NewTillsRepo(dp.Db).InsertTill(t.Context(), "Counter", hashBearer("token-abc"))
	if err != nil {
		t.Fatal(err)
	}
	// The audit row's actor is the session user, a real users row (FK).
	if _, err := dp.Db.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES ('u-manager','mgr','Manager','x','manager')`); err != nil {
		t.Fatal(err)
	}

	if rec := postTillRole(t, mux, id, "satellite", "cashier"); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier: %d, want 403", rec.Code)
	}
	if rec := postTillRole(t, mux, id, "kiosk", "manager"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bogus role: %d, want 400", rec.Code)
	}
	if rec := postTillRole(t, mux, "no-such-till", "satellite", "manager"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown till: %d, want 404", rec.Code)
	}
	if got := auditRows(t, dp, "role_changed"); len(got) != 0 {
		t.Fatalf("refused changes were audited: %v", got)
	}

	rec := postTillRole(t, mux, id, "satellite", "manager")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("manager: %d %s, want 204", rec.Code, rec.Body)
	}
	if role, _, _ := data.NewTillsRepo(dp.Db).RoleByID(t.Context(), id); role != "satellite" {
		t.Fatalf("role after change = %q", role)
	}
	got := auditRows(t, dp, "role_changed")
	if len(got) != 1 || got[0] != `till|`+id+`|u-manager|{"from":"additional","to":"satellite"}` {
		t.Fatalf("audit = %v", got)
	}

	roster := getRoster(t, mux, "manager")
	row := rowOf(t, roster.Body.String(), "Counter")
	for _, want := range []string{"Satellite", `hx-post="/api/tills/` + id + `/role"`, "Change role", "Change Counter to Additional till?"} {
		if !strings.Contains(row, want) {
			t.Errorf("roster row missing %q: %s", want, row)
		}
	}
	if !strings.Contains(roster.Body.String(), "<th>Role") {
		t.Errorf("roster has no Role column: %s", roster.Body)
	}

	// Same role again: nothing changed, nothing audited.
	if rec := postTillRole(t, mux, id, "satellite", "manager"); rec.Code != http.StatusNoContent {
		t.Fatalf("repeat: %d", rec.Code)
	}
	if got := auditRows(t, dp, "role_changed"); len(got) != 1 {
		t.Fatalf("a no-op change was audited: %v", got)
	}
}

// Like revoke: a joined till's roster is a synced copy.
func TestTillRoleChange_RefusedOnAJoinedTill(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newSyncPairingGateTestDeps(t)
	mux := http.NewServeMux()
	registerTillRole(mux, dp)
	id, _ := data.NewTillsRepo(dp.Db).InsertTill(t.Context(), "Counter", hashBearer("token-abc"))
	if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://192.0.2.1:8080"); err != nil {
		t.Fatal(err)
	}
	if rec := postTillRole(t, mux, id, "satellite", "manager"); rec.Code != http.StatusConflict {
		t.Fatalf("on a joined till: %d, want 409", rec.Code)
	}
}

// --- The admin pull carries the requesting till's role ---

func TestSyncAdminAPI_CarriesRequestingTillsRole(t *testing.T) {
	dp := newMigratedSyncDeps(t, "primary.db")
	tills := data.NewTillsRepo(dp.Db)
	satID, err := tills.InsertTillWithRole(t.Context(), "Kiosk", hashBearer("token-kiosk"), data.TillRoleSatellite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tills.InsertTill(t.Context(), "Till 2", hashBearer("token-two")); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerSyncAdmin(mux, dp)
	get := func(bearer, have string) adminBundleResponse {
		req := httptest.NewRequest(http.MethodGet, "/api/sync/admin?have="+have, nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out struct {
			Data adminBundleResponse `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode %s: %v", rec.Body, err)
		}
		return out.Data
	}
	kiosk := get("token-kiosk", "")
	if kiosk.Role != "satellite" {
		t.Fatalf("satellite's pull Role = %q", kiosk.Role)
	}
	if two := get("token-two", ""); two.Role != "additional" {
		t.Fatalf("additional till's pull Role = %q", two.Role)
	}
	// An unchanged poll still says who the till is now.
	if again := get("token-kiosk", kiosk.Version); !again.Unchanged || again.Role != "satellite" {
		t.Fatalf("unchanged poll: %+v", again)
	}
	// A role change moves the bundle (the tills row carries role), so every
	// joined till re-pulls the roster too.
	if _, _, err := tills.SetRole(t.Context(), satID, data.TillRoleAdditional); err != nil {
		t.Fatal(err)
	}
	if after := get("token-kiosk", kiosk.Version); after.Unchanged || after.Role != "additional" {
		t.Fatalf("after the role change: Unchanged=%v Role=%q, want a fresh bundle and additional", after.Unchanged, after.Role)
	}
}

func TestOwnTillRole_RemembersWhatTheMainTillSays(t *testing.T) {
	dp := newMigratedSyncDeps(t, "replica.db")
	ctx := t.Context()
	if got := ownTillRole(ctx, dp); got != "" {
		t.Fatalf("a main/standalone till has no role, got %q", got)
	}
	if err := dp.Settings.Set(ctx, "sync.primary_url", "http://192.0.2.1:8080"); err != nil {
		t.Fatal(err)
	}
	if got := ownTillRole(ctx, dp); got != "additional" {
		t.Fatalf("joined till with nothing known = %q, want additional", got)
	}
	rememberOwnTillRole(ctx, dp, "satellite")
	if got := ownTillRole(ctx, dp); got != "satellite" {
		t.Fatalf("after the pull said satellite = %q", got)
	}
	rememberOwnTillRole(ctx, dp, "")      // an older main till: no field
	rememberOwnTillRole(ctx, dp, "bogus") // garbage
	if got := ownTillRole(ctx, dp); got != "satellite" {
		t.Fatalf("an empty/unknown report overwrote the role: %q", got)
	}
}

// --- POST /api/sync/till-role: a joined till reports its own role ---

func TestSyncTillRole_BearerChangesOnlyItsOwnRow(t *testing.T) {
	dp := newMigratedSyncDeps(t, "main.db")
	tills := data.NewTillsRepo(dp.Db)
	selfID, _ := tills.InsertTill(t.Context(), "Kiosk", hashBearer("token-self"))
	otherID, _ := tills.InsertTill(t.Context(), "Till 2", hashBearer("token-other"))
	mux := http.NewServeMux()
	registerTillRole(mux, dp)
	post := func(bearer, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/sync/till-role", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := post("", `{"role":"satellite"}`); code != http.StatusUnauthorized {
		t.Fatalf("no bearer: %d", code)
	}
	if code := post("token-self", `{"role":"main"}`); code != http.StatusBadRequest {
		t.Fatalf("bogus role: %d", code)
	}
	if code := post("token-self", `{"role":"satellite"}`); code != http.StatusOK {
		t.Fatalf("own report: %d", code)
	}
	if r, _, _ := tills.RoleByID(t.Context(), selfID); r != "satellite" {
		t.Fatalf("own row = %q", r)
	}
	if r, _, _ := tills.RoleByID(t.Context(), otherID); r != "additional" {
		t.Fatalf("a sibling's row moved: %q", r)
	}
	if got := auditRows(t, dp, "role_changed"); len(got) != 1 || !strings.HasPrefix(got[0], "till|"+selfID+"|system|") {
		t.Fatalf("audit = %v", got)
	}
}

// --- Device profile gated by role (POST /api/settings/display-mode) ---

type tillRoleSettingsFixture struct {
	main, replica *common.Deps
	mux           *http.ServeMux // the replica's
	selfID        string
	mainSrv       *httptest.Server
}

func newTillRoleSettingsFixture(t *testing.T, role string) *tillRoleSettingsFixture {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	initTillRoleI18n(t)
	f := &tillRoleSettingsFixture{main: newMigratedSyncDeps(t, "main.db"), replica: newMigratedSyncDeps(t, "replica.db")}
	var err error
	f.selfID, err = data.NewTillsRepo(f.main.Db).InsertTillWithRole(t.Context(), "Kiosk", hashBearer("token-self"), role)
	if err != nil {
		t.Fatal(err)
	}
	mainMux := http.NewServeMux()
	registerTillRole(mainMux, f.main)
	// Behind the real session middleware, as on a main till: the replica's
	// report carries only its sync bearer, so /api/sync/till-role has to be
	// on auth.exempt's list or the middleware bounces it to /login before
	// syncTill ever runs (the /api/sync/stock failure class — a bare mux
	// here hid exactly that in review).
	f.mainSrv = httptest.NewServer(auth.Middleware(mainMux, auth.NewService(f.main.Db)))
	t.Cleanup(f.mainSrv.Close)
	for k, v := range map[string]string{
		"sync.primary_url": f.mainSrv.URL,
		"sync.bearer":      "token-self",
		"sync.till_id":     f.selfID,
		"sync.till_role":   role,
	} {
		if err := f.replica.Settings.Set(t.Context(), k, v); err != nil {
			t.Fatal(err)
		}
	}
	f.mux = http.NewServeMux()
	registerSettings(f.mux, f.replica)
	return f
}

func (f *tillRoleSettingsFixture) setMode(t *testing.T, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func (f *tillRoleSettingsFixture) mode(t *testing.T) string {
	t.Helper()
	v, _, _ := f.replica.Settings.Get(t.Context(), "display.mode")
	return v
}

func TestDisplayMode_SatelliteRefusesRegisterAndBackoffice(t *testing.T) {
	f := newTillRoleSettingsFixture(t, "satellite")
	for _, mode := range []string{"register", "backoffice"} {
		rec := f.setMode(t, url.Values{"mode": {mode}})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s on a satellite: %d %s, want 400", mode, rec.Code, rec.Body)
		}
		if !strings.Contains(rec.Body.String(), "This till is a satellite") {
			t.Fatalf("%s: not the specific message: %q", mode, rec.Body)
		}
	}
	if m := f.mode(t); m != "" {
		t.Fatalf("a refused switch persisted display.mode=%q", m)
	}
	// The kiosk profile itself is what a satellite is for.
	if rec := f.setMode(t, url.Values{"mode": {"self_order"}}); rec.Code != http.StatusNoContent {
		t.Fatalf("self_order on a satellite: %d %s", rec.Code, rec.Body)
	}
}

func TestDisplayMode_AdditionalToSelfOrderAsksFirstThenBecomesSatellite(t *testing.T) {
	f := newTillRoleSettingsFixture(t, "additional")

	rec := f.setMode(t, url.Values{"mode": {"self_order"}})
	if rec.Code != http.StatusOK || rec.Header().Get("X-UT-Response") != "confirm-prompt" {
		t.Fatalf("self_order on an additional till: %d %q, want the confirm prompt", rec.Code, rec.Header().Get("X-UT-Response"))
	}
	body := rec.Body.String()
	for _, want := range []string{`id="confirm-modal"`, "Make this a satellite?", "Make satellite", `name="confirm_satellite" value="1"`, `hx-post="/api/settings/display-mode"`} {
		if !strings.Contains(body, want) {
			t.Errorf("confirm prompt missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "showModal") {
		t.Fatal("the confirm prompt must open with .show(), never showModal()")
	}
	if m := f.mode(t); m != "" {
		t.Fatalf("self_order applied before confirming: %q", m)
	}
	if got := auditRows(t, f.replica, "role_changed"); len(got) != 0 {
		t.Fatalf("role changed before confirming: %v", got)
	}

	rec = f.setMode(t, url.Values{"mode": {"self_order"}, "confirm_satellite": {"1"}})
	if rec.Code != http.StatusNoContent || rec.Header().Get("HX-Redirect") != "/" {
		t.Fatalf("confirmed: %d HX-Redirect=%q %s", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body)
	}
	if m := f.mode(t); m != "self_order" {
		t.Fatalf("display.mode = %q, want self_order", m)
	}
	if got := ownTillRole(t.Context(), f.replica); got != "satellite" {
		t.Fatalf("own role = %q, want satellite", got)
	}
	if r, _, _ := data.NewTillsRepo(f.main.Db).RoleByID(t.Context(), f.selfID); r != "satellite" {
		t.Fatalf("the main till's roster says %q, want satellite", r)
	}
	if got := auditRows(t, f.replica, "role_changed"); len(got) != 1 || !strings.Contains(got[0], `{"from":"additional","to":"satellite"}`) {
		t.Fatalf("replica audit = %v", got)
	}
	if got := auditRows(t, f.main, "role_changed"); len(got) != 1 {
		t.Fatalf("main audit = %v", got)
	}
}

// The main till holds the role: if it can't be told, nothing changes here.
func TestDisplayMode_MakeSatelliteRefusedWhileMainTillUnreachable(t *testing.T) {
	f := newTillRoleSettingsFixture(t, "additional")
	f.mainSrv.Close()
	rec := f.setMode(t, url.Values{"mode": {"self_order"}, "confirm_satellite": {"1"}})
	if rec.Header().Get("X-UT-Response") != "refused" || !strings.Contains(rec.Body.String(), "reach the main till") {
		t.Fatalf("unreachable main: %d %q", rec.Code, rec.Body)
	}
	if m := f.mode(t); m != "" {
		t.Fatalf("display.mode = %q after a refused change", m)
	}
	if got := ownTillRole(t.Context(), f.replica); got != "additional" {
		t.Fatalf("own role = %q", got)
	}
}

// A main/standalone till has no role: nothing is gated or asked.
func TestDisplayMode_MainTillUnaffected(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newMigratedSyncDeps(t, "main.db")
	mux := http.NewServeMux()
	registerSettings(mux, dp)
	for _, mode := range []string{"self_order", "register", "backoffice"} {
		req := httptest.NewRequest(http.MethodPost, "/api/settings/display-mode", strings.NewReader("mode="+mode))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s on a main till: %d %s", mode, rec.Code, rec.Body)
		}
	}
}
